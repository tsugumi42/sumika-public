// Package policy 是审批怎么判（daemon/tools/risk.ts、shell.ts 的 judge、policy.ts）：只看声明、参数、命令拆开看的结果和当前设置，
// 不碰文件、不执行任何东西。规则宁严勿松，判断不了的一律当成"要问"。
//
// 正则照 JS 的写法搬：JS 不带 u 的 /i 只在 ASCII 字母之间不分大小写，这里先 jsstr.LowerASCII 再用小写的正则匹配；
// JS 的 \s 换成 jsstr.S 那一组；Go 的正则没有前后断言（(?<!…)、(?=…)），用到的几处改成手写的判断
package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"sumika-native/internal/jsstr"
)

const s = jsstr.S

// readOnly 是只读、不改任何东西的 PowerShell 命令（小写，含常见别名；risk.ts READ_ONLY）
var readOnly = set(`get-childitem gci ls dir get-content gc cat type select-string sls get-item gi
get-itemproperty gp test-path resolve-path rvpa get-location gl pwd measure-object measure
select-object select where-object where ? sort-object sort group-object group format-table
ft format-list fl format-wide fw out-string get-date get-process ps get-command gcm
get-help help echo write-output write get-filehash split-path join-path convertfrom-json
convertto-json convertfrom-csv import-csv foreach-object foreach % get-unique compare-object
rg fd findstr where.exe hostname whoami git`)

var gitReadOnly = set(`status log diff show branch rev-parse ls-files blame remote describe shortlog`)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

const bareWords = `iex|irm|iwr|curl|wget|reg|net|sc|shutdown|del|erase|rd|rmdir|mkdir|md|mv|cp|rm|ren|kill|taskkill|powershell|pwsh|cmd|bun|bunx|npm|npx|pnpm|pip|python|py|node|winget|scoop|choco`

// 出现在任何位置都不算只读：重定向、调用运算符、改动类 cmdlet、{ ( 后面藏着的命令（risk.ts NOT_READ_ONLY）
var notReadOnly = regexp.MustCompile(`(>|\b(remove|move|rename|copy|set|new|clear|stop|restart|start|invoke|install|uninstall|update|add|enable|disable|register|unregister|import-module|out-file|tee-object|expand-archive|compress-archive)-|&[` + s + `]*['"\w.$]|[{(][` + s + `]*(` + bareWords + `)\b)`)

// 可能毁掉整台电脑的操作：即使 YOLO 也要问（risk.ts CATASTROPHIC）
var catastrophic = regexp.MustCompile(`(format-volume|\bformat[` + s + `]+[a-z]:|\bdiskpart\b|\bbcdedit\b|vssadmin[` + s + `]+delete|\bcipher[` + s + `]+/w|clear-disk|initialize-disk|remove-partition|\breg[` + s + `]+delete[` + s + `]+hk(lm|ey_local_machine)|set-mppreference|\btakeown\b|(remove-item|\brm\b|\bdel\b|\brd\b|\brmdir\b)[` + s + `]+[^|;\n]*\b[a-z]:\\?[` + s + `]*($|[|;\n]|-)|rm[` + s + `]+-rf[` + s + `]+/([` + s + `]|$))`)

var deleteVerb = regexp.MustCompile(`\b(remove-item|ri|rm|rmdir|rd|del|erase)\b`)

// 用户目录根的各种写法 + 后面那一截（risk.ts HOME_RE，去掉了前后断言：(?<![\w.]) 和 ~ 后面的 (?=…) 在 homeAt 里判）
var homeHead = regexp.MustCompile(`^(%userprofile%|%homepath%|\$env:userprofile|\$env:homepath|\$home|~|[a-z]:[\\/]users[\\/][^\\/:*?"<>|` + s + `]+)([^` + s + `"'|;&]*)`)

// 用户目录下放凭据的目录（risk.ts CRED_DIR）
var credDir = regexp.MustCompile(`[\\/](\.ssh|\.aws|\.gnupg|\.azure|\.docker|\.kube|\.gitconfig|\.config[\\/](gh|gcloud))([\\/]|$)|appdata[\\/][^` + s + `"']*microsoft[\\/](credentials|protect)`)

var homeSplit = regexp.MustCompile(`[\\/]+`)

func isWordOrDot(c byte) bool {
	return c == '_' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// homeMatches 像 HOME_RE.exec 一次次往后找：前面紧挨着字母、数字、下划线、点的不算开头；~ 后面要是分隔符、空白、引号、; | & 或者结尾
func homeMatches(low string) [][2]string {
	var out [][2]string
	for i := 0; i < len(low); {
		if i > 0 && isWordOrDot(low[i-1]) {
			i++
			continue
		}
		m := homeHead.FindStringSubmatch(low[i:])
		if m == nil {
			i++
			continue
		}
		if m[1] == "~" {
			if j := i + 1; j < len(low) {
				r := []rune(low[j:])[0]
				if !(r == '\\' || r == '/' || r == '"' || r == '\'' || r == ';' || r == '|' || r == '&' || jsstr.IsSpace(r)) {
					i++
					continue
				}
			}
		}
		out = append(out, [2]string{m[0], m[2]})
		i += len(m[0])
	}
	return out
}

// DeletesUserDir 是删用户目录、它的直接下一级、或里面的凭据目录（ADR-106）：撤不回，算灾难性
func DeletesUserDir(command string) bool {
	low := jsstr.LowerASCII(command)
	if !deleteVerb.MatchString(low) {
		return false
	}
	for _, m := range homeMatches(low) {
		if credDir.MatchString(m[0]) {
			return true
		}
		tail := strings.TrimLeft(m[1], `\/`)
		if tail == "" {
			return true
		}
		n := 0
		for _, p := range homeSplit.Split(tail, -1) {
			if p != "" {
				n++
			}
		}
		if n <= 1 {
			return true
		}
	}
	return false
}

// IsCatastrophic 是可能毁掉整台电脑的命令（格式化、分区、删盘符根、删用户目录……）
func IsCatastrophic(command string) bool {
	return catastrophic.MatchString(jsstr.LowerASCII(command)) || DeletesUserDir(command)
}

// HasDeleteVerb 是命令文字里有没有删除动词（只看静态读得出来的）
func HasDeleteVerb(command string) bool { return deleteVerb.MatchString(jsstr.LowerASCII(command)) }

var (
	quoteConcat = regexp.MustCompile(`["'][` + s + `]*\+[` + s + `]*["']`)
	segSplitAmp = regexp.MustCompile(`\||;|&&|\|\||&|\r?\n`)
	segSplit    = regexp.MustCompile(`\||;|&&|\|\||\r?\n`)
	localAddr   = regexp.MustCompile(`\b(127\.\d{1,3}\.\d{1,3}\.\d{1,3}|localhost|0\.0\.0\.0)\b|\[?::1\]?`)
	spaces      = regexp.MustCompile(`[` + s + `]+`)
)

// hasPort 像 /(?<!\d)端口(?!\d)/：前后都不是数字
func hasPort(seg string, port int) bool {
	p := strconv.Itoa(port)
	for i := 0; ; {
		k := strings.Index(seg[i:], p)
		if k < 0 {
			return false
		}
		at := i + k
		end := at + len(p)
		if (at == 0 || seg[at-1] < '0' || seg[at-1] > '9') && (end == len(seg) || seg[end] < '0' || seg[end] > '9') {
			return true
		}
		i = at + 1
	}
}

// TouchesSelfEndpoint 是命令的同一段里既有本机地址、又有后台自己监听的端口（ADR-106）：免得她调自己的接口
func TouchesSelfEndpoint(command string, ports []int) bool {
	if len(ports) == 0 {
		return false
	}
	cmd := quoteConcat.ReplaceAllString(command, "")
	for _, seg := range segSplitAmp.Split(cmd, -1) {
		if !localAddr.MatchString(jsstr.LowerASCII(seg)) {
			continue
		}
		for _, p := range ports {
			if hasPort(seg, p) {
				return true
			}
		}
	}
	return false
}

// IsReadOnlyCommand 是命令的每一段都只读（文字规则；拆开看的结果另外要求只读，见 policy）
func IsReadOnlyCommand(command string) bool {
	if notReadOnly.MatchString(jsstr.LowerASCII(command)) {
		return false
	}
	var segs []string
	for _, x := range segSplit.Split(command, -1) {
		if x = jsstr.Trim(x); x != "" {
			segs = append(segs, x)
		}
	}
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		parts := spaces.Split(seg, -1)
		cmd := strings.ToLower(parts[0])
		if !readOnly[cmd] {
			return false
		}
		if cmd == "git" && (len(parts) < 2 || !gitReadOnly[strings.ToLower(parts[1])]) {
			return false
		}
	}
	return true
}

// resolve 像 Node 的 path.win32.resolve(base, p)：绝对路径照原样规范化；只有根（\x）的接在 base 的盘符上；别的接在 base 后面
func resolve(base, p string) string {
	switch {
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	case strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "/"):
		return filepath.Clean(filepath.VolumeName(base) + p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// resolveCwd 像 path.resolve(p)：相对进程的当前目录
func resolveCwd(p string) string {
	wd, _ := os.Getwd()
	return resolve(wd, p)
}

// IsInside 是目标路径在允许的目录里（不分大小写，按 Windows 处理；绝对路径也 resolve，免得 工作目录\..\..\x 按字面算在里面）。
// 目录和下一级之间的分隔符用本机的（filepath.Separator）：Windows 上就是反斜杠，和以前一样；以前写死反斜杠，
// Linux 上工作目录里的文件全被判成"在外面"，yolo 下写文件也要问（2026-10-26 Linux CI 的 -race 卡住查出来的）
func IsInside(target string, dirs []string, workdir string) bool {
	sep := string(filepath.Separator)
	abs := strings.ToLower(resolve(workdir, target))
	for _, d := range dirs {
		b := strings.ToLower(resolveCwd(d)) // 允许目录照 resolve(d)：相对的按进程当前目录算
		if abs == b || strings.HasPrefix(abs, strings.TrimSuffix(b, sep)+sep) {
			return true
		}
	}
	return false
}
