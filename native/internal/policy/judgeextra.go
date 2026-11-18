package policy

import (
	"regexp"
	"strings"

	"sumika-native/internal/jsjson"
)

// ───────────── Judge 顺手记下的、给“碰受保护目录”用的几样（不进 JSON，对拍不受影响） ─────────────
// Targets 是命令里写的原样；这里补：换过目录以后的真实位置、递归搜索从哪里开始搜、读文件命令里“算出来的路径”。
// 只用分析器给的语法树信息（参数是变量、括号、展开字符串、子表达式……），不再靠命令文字上的正则猜。

var (
	// 读文件类命令：路径位置上出现变量、括号、展开字符串，就是“路径是算出来的”
	readClassCmdlets = set(`get-content select-string get-item get-childitem import-csv import-clixml get-filehash get-itemproperty get-itempropertyvalue`)
	// 管道里没有上游命令（`$files | Get-Content`）时，这些命令的路径来自管道
	pipeReadCmdlets = set(`get-content get-item import-csv import-clixml get-filehash get-itemproperty`)
	// 只输出文字的命令：参数不是路径，不做“网络共享、驱动器相对”这类形状判断
	outputCmdlets = set(`write-host write-output write-verbose write-warning write-error write-debug write-information out-host out-string echo`)
	// 外部的搜索程序：第一个位置参数是要找的内容，后面的是路径
	nativeSearchers = set(`rg fd ack ag grep egrep fgrep findstr`)
	// 一定会往下递归搜的
	alwaysRecursive = set(`rg fd ack ag`)
	reGrepRecursive = regexp.MustCompile(`^-[a-zA-Z]*[rR][a-zA-Z]*$|^--recursive$`)
	reFindstrSub    = regexp.MustCompile(`(?i)^/[a-z]*s[a-z]*$`)
	reFindstrPat    = regexp.MustCompile(`(?i)^/[cg]:`)
)

// 常见搜索程序认识的开关：带值的、不带值的。认不全的开关出现，数不准位置参数，就补当前目录当起点
type searchSwitches struct {
	shortVal, shortBool string
	longVal, longBool   map[string]bool
}

var nativeSw = map[string]searchSwitches{
	"rg": {"tTgCABjmefrMEd", "nNilLcvwsSFuaHIqUzopPxhV0", set(`type type-not glob iglob context after-context before-context threads max-count regexp file replace max-columns encoding max-depth maxdepth max-filesize sort sortr type-add type-clear ignore-file pre pre-glob colors color path-separator engine dfa-size-limit regex-size-limit field-context-separator field-match-separator context-separator`),
		set(`files hidden no-ignore follow count json stats line-number no-line-number ignore-case smart-case case-sensitive fixed-strings word-regexp invert-match files-with-matches files-without-match null text multiline heading no-heading with-filename no-filename column vimgrep pretty trim no-messages quiet no-config no-ignore-vcs no-ignore-dot no-ignore-parent unrestricted binary count-matches only-matching line-regexp passthru crlf no-mmap`)},
	"fd": {"tEeSxXjcodC", "HIsiuaLpgFhV0l", set(`type extension exclude max-depth min-depth exact-depth size exec exec-batch threads color owner changed-within changed-before change-newer-than base-directory search-path ignore-file format max-results`),
		set(`hidden no-ignore case-sensitive ignore-case glob fixed-strings absolute-path follow full-path print0 list-details no-ignore-vcs quiet unrestricted`)},
	"grep": {"efmABCdD", "rRnivlLcwxsEFPHhIaoqzUVTZ", set(`regexp file max-count after-context before-context context include exclude exclude-dir exclude-from label binary-files directories devices group-separator color colour`),
		set(`recursive dereference-recursive line-number ignore-case invert-match files-with-matches files-without-match count word-regexp line-regexp no-messages with-filename no-filename only-matching quiet text null null-data extended-regexp fixed-strings perl-regexp initial-tab byte-offset`)},
}

func init() {
	nativeSw["egrep"], nativeSw["fgrep"] = nativeSw["grep"], nativeSw["grep"]
}

// switchTakesValue 说这个开关后面是不是跟着一个值；known 为假是认不出（数位置参数就不准了）
func switchTakesValue(name, sw string) (takes, known bool) {
	t, ok := nativeSw[name]
	if !ok {
		return false, false
	}
	if sw == "--" {
		return false, true
	}
	if strings.HasPrefix(sw, "--") {
		l := strings.ToLower(sw[2:])
		if t.longVal[l] {
			return true, true
		}
		return false, t.longBool[l]
	}
	letters := strings.TrimPrefix(sw, "-")
	for i, r := range letters {
		switch {
		case strings.ContainsRune(t.shortVal, r):
			return i == len(letters)-1, true // 值挂在后面（-A3、-gx）就不再吃下一项
		case strings.ContainsRune(t.shortBool, r) || (r >= '0' && r <= '9'):
		default:
			return false, false
		}
	}
	return false, true
}

// noteExtra 在 Judge 处理每条命令时调用：c 是这条命令，canon 是规范名，toks 是它的参数，at 把相对目标按当前目录接起来
func noteExtra(v *Verdict, a Analysis, c Command, canon, name string, toks []tok, at func(string) (string, bool), cwd string, cwdNull bool) {
	if outputCmdlets[canon] || outputCmdlets[name] {
		for _, t := range toks {
			if t.kind == tokVal {
				v.NonPath = append(v.NonPath, t.val)
			}
		}
		return
	}
	// 1. 换过目录以后的真实位置
	for _, t := range toks {
		if t.kind != tokVal || strings.HasPrefix(t.val, "-") {
			continue
		}
		// 值绑在路径参数上（Get-Content C:foo），盘符后面没有分隔符也是驱动器相对路径；-Format "H:mm" 绑的不是路径参数，不算。
		// 只有盘符的（Get-ChildItem D:）是切到或列出该盘的当前目录，不算
		if isDriveRelative(t.val) && !isBareDrive(t.val) && targetParams[strings.ToLower(t.bind)] {
			v.PathDrive = append(v.PathDrive, t.val)
		}
		if r, ok := at(t.val); !ok {
			if !rooted(t.val) {
				v.CwdUnknown = true // 前面换过目录、看不清换到了哪：后面的相对目标不知道落在哪
			}
		} else if r != t.val {
			v.AtTargets = append(v.AtTargets, r)
		}
	}
	hide := func(text string, recursive bool) {
		v.HiddenReads = append(v.HiddenReads, HiddenRead{Text: text, Cwd: cwd, CwdNull: cwdNull, Recursive: recursive})
	}
	// 2. 读文件类命令的路径位置是变量、括号、展开字符串、子表达式
	if readClassCmdlets[canon] {
		rec := isRecursiveSearch(canon, name, toks)
		for _, t := range toks {
			if t.kind == tokHidden {
				if b := strings.ToLower(t.bind); b == "" || targetParams[b] {
					hide(t.text, rec)
				}
			}
		}
		if pipeReadCmdlets[canon] && c.Piped != nil && *c.Piped && idxOf(c) > 0 && !hasUpstream(a, c) && !hasPathValue(toks) {
			hide("<管道>", false)
		}
	}
	// 3. 递归搜索从哪里开始搜
	switch {
	case nativeSearchers[name]:
		noteNativeSearch(v, name, toks, at, cwd, cwdNull, hide)
	case isRecursiveSearch(canon, name, toks):
		var roots []string
		pos := 0
		for _, t := range toks {
			if t.kind != tokVal || strings.HasPrefix(t.val, "-") {
				continue
			}
			if (t.of == "" && (t.bind == "" || targetParams[strings.ToLower(t.bind)])) || targetParams[strings.TrimPrefix(strings.ToLower(t.of), "-")] {
				pos++
				if r, ok := at(t.val); ok {
					roots = append(roots, r)
				}
			}
		}
		if pos == 0 { // Get-ChildItem -Recurse：没写搜哪里就搜当前目录
			roots = append(roots, cwdRoot(v, cwd, cwdNull)...)
		}
		v.SearchRoots = append(v.SearchRoots, roots...)
	}
}

func cwdRoot(v *Verdict, cwd string, cwdNull bool) []string {
	switch {
	case cwdNull:
		v.CwdUnknown = true
		return nil
	case cwd == "":
		return []string{"."}
	}
	return []string{cwd}
}

// noteNativeSearch：rg / fd / grep / findstr 这类外部搜索程序。认识常见的带值开关（-t -g -C -A -B -m -e -f --glob …），
// 数出位置参数：第一个是要找的内容（有 --files、-e / -f、findstr 的 /c: /g: 就没有这一项），后面的是路径。
// 路径位置是变量、展开字符串的记 HiddenReads；会递归的，没写路径（或开关认不全）就把当前目录当起点。
func noteNativeSearch(v *Verdict, name string, toks []tok, at func(string) (string, bool), cwd string, cwdNull bool, hide func(string, bool)) {
	var pos []tok
	filesMode, patternViaSwitch, unknown, recursive := false, false, false, alwaysRecursive[name]
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.kind == tokSw:
			sw := t.sw
			takes, known := switchTakesValue(name, sw)
			if !known {
				unknown = true
			}
			switch sw {
			case "--files":
				filesMode = true
			case "-e", "-f", "--regexp", "--file":
				patternViaSwitch = name != "fd" // fd 的 -e 是扩展名，不是要找的内容
			}
			if (name == "grep" || name == "egrep" || name == "fgrep") && reGrepRecursive.MatchString(sw) {
				recursive = true
			}
			if takes && i+1 < len(toks) && toks[i+1].kind != tokSw {
				i++
			}
		case t.kind == tokVal && t.of != "":
			// --k=v 的值，不是位置参数
		case t.kind == tokVal && name == "findstr" && strings.HasPrefix(t.val, "/"):
			if reFindstrSub.MatchString(t.val) {
				recursive = true
			}
			if reFindstrPat.MatchString(t.val) {
				patternViaSwitch = true
			}
		default:
			pos = append(pos, t)
		}
	}
	start := 1
	if filesMode || patternViaSwitch {
		start = 0
	}
	var pathToks []tok
	if len(pos) > start {
		pathToks = pos[start:]
	}
	maskWithDir := false
	for _, t := range pathToks {
		if t.kind == tokHidden {
			hide(t.text, recursive)
			continue
		}
		if strings.ContainsAny(t.val, `\/`) {
			maskWithDir = true
		}
		if recursive {
			if r, ok := at(t.val); ok {
				v.SearchRoots = append(v.SearchRoots, r)
			}
		}
	}
	// 没写搜哪里、开关认不全（可能多吃或少吃一项）、findstr 的文件名模式没带目录：搜的是当前目录
	if recursive && (len(pathToks) == 0 || unknown || (name == "findstr" && !maskWithDir)) {
		v.SearchRoots = append(v.SearchRoots, cwdRoot(v, cwd, cwdNull)...)
	}
}

func hasUpstream(a Analysis, c Command) bool {
	if c.Pipe == nil {
		return false
	}
	k := idxOf(c)
	for _, x := range a.Commands {
		if x.Pipe != nil && *x.Pipe == *c.Pipe && idxOf(x) == k-1 {
			return true
		}
	}
	return false
}

func hasPathValue(toks []tok) bool {
	for _, t := range toks {
		if t.kind == tokVal && !strings.HasPrefix(t.val, "-") {
			return true
		}
	}
	return false
}

func isRecursiveSearch(canon, name string, toks []tok) bool {
	if canon != "get-childitem" {
		return false
	}
	for _, t := range toks {
		if t.kind == tokSw {
			s := strings.ToLower(strings.TrimPrefix(t.sw, "-"))
			if s != "" && strings.HasPrefix("recurse", s) {
				return true
			}
		}
	}
	return false
}

// ExtraLinkPaths 是 gate 除了 LinkPaths 之外还要先解析联接的路径：open 的 target，命令里写的各种目标（原样、换目录以后接起来的、
// 递归搜索的起点）。LinkPaths 保持和 Bun 一样（对拍比它），这些放在这里；判受保护时用解析后的真实路径再比一遍，联接指进数据目录也认得出。
func ExtraLinkPaths(spec Spec, args jsjson.Value, env Env) []string {
	var out []string
	if t, ok := openTargetArg(args); ok && !IsOpaquePath(t) {
		out = append(out, ToolPath(t, env.Workdir))
	}
	if sh := env.Shell; sh != nil {
		for _, group := range [][]string{sh.Targets, sh.AtTargets, sh.SearchRoots} {
			for _, p := range group {
				if p = expandHome(p); p == "" || OutsideForm(p) || IsOpaquePath(p) || isURL(p) || strings.ContainsAny(p, "*?<>|\"") {
					continue
				}
				out = append(out, resolve(env.Workdir, p))
			}
		}
	}
	for i, p := range out {
		out[i] = resolveCwd(p)
	}
	return out
}
