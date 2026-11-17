package policy

import (
	"os"
	"regexp"
	"strings"
)

// ───────────── 命令文字里的路径：先规整再比，看不清就问 ─────────────
// 命令文字和受保护目录原来是纯子串比，下面这些写法指到同一个地方，字面上却比不上：
// 双反斜杠（'C:\\a\\b'）、\.\ 段、段末尾的点和空格、先 .. 再回来。
// （file:///、FileSystem:: 前缀和 ::$DATA 数据流写在路径前后，原来的子串比对本来就看得到，不用另外处理。）
// 这里把命令文字规整成一个统一的样子再比。命令里“目标是什么”优先用分析器给的信息（语法树里参数的种类、
// 换目录以后的位置、递归搜索从哪里开始，见 judgeextra.go），不再靠命令文字上的正则猜。
// 不做真正的 PowerShell 解析：拼接字符串、动态执行这类本来就归分析器的“拆不开”管。

var (
	reCmdSeps  = regexp.MustCompile(`\\{2,}`)
	reCmdTrail = regexp.MustCompile(`([^\\. ])[. ]+\\`)
	// 正则写法的特征（\w \s \d \( \) \| \{ \} \^ \$ \]），不是路径
	reRegexLike = regexp.MustCompile(`\\[wWsSdDbB]([*+?{)\]|]|$)|\\[(){}|^$.\]]|\.[*+]|\[[^\]]*\][*+?{]|://|^\^|\$$`)
	// 盘符、网络共享开头的写法不会是正则（C:\Users\x\s*\… 里的 \s* 是目录名通配），不套上面的特征
	reDriveHead = regexp.MustCompile(`^([A-Za-z]:[\\/]|[\\/]{2}|~)`)
)

// normCommandText 把命令文字规整成统一的样子（小写、反斜杠、合并分隔符、去 \.\ 、去段末尾点和空格、消掉 ..），只用来比。
func normCommandText(cmd string) string {
	s := strings.ReplaceAll(strings.ToLower(cmd), "/", `\`)
	s = reCmdSeps.ReplaceAllString(s, `\`)
	for strings.Contains(s, `\.\`) {
		s = strings.ReplaceAll(s, `\.\`, `\`)
	}
	s = reCmdTrail.ReplaceAllString(s, `$1\`)
	return cleanDotDot(s)
}

// cleanDotDot 在命令文字里每个“路径样”的片段（不含空白、引号、括号、逗号、分号、竖线）内消掉 x\..\
func cleanDotDot(s string) string {
	if !strings.Contains(s, `\..`) {
		return s
	}
	var out strings.Builder
	start := 0
	flush := func(end int) {
		tok := s[start:end]
		if strings.Contains(tok, `\..`) {
			tok = cleanTokenDotDot(tok)
		}
		out.WriteString(tok)
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(" \t\r\n'\"(),;|=", s[i]) >= 0 {
			flush(i)
			out.WriteByte(s[i])
			start = i + 1
		}
	}
	flush(len(s))
	return out.String()
}

func cleanTokenDotDot(tok string) string {
	parts := strings.Split(tok, `\`)
	var stack []string
	for _, p := range parts {
		if p == ".." && len(stack) > 0 && stack[len(stack)-1] != ".." && stack[len(stack)-1] != "" && !strings.HasSuffix(stack[len(stack)-1], ":") {
			stack = stack[:len(stack)-1]
			continue
		}
		stack = append(stack, p)
	}
	return strings.Join(stack, `\`)
}

// shellTargetsTouchProtected：分析器给的各种目标碰到受保护目录——命令里写的原样、换过目录以后接起来的真实位置、
// 重定向写到哪、按路径运行的程序，再加递归搜索从哪里开始（目标是受保护目录的上级也算）。
func shellTargetsTouchProtected(env Env, command string) bool {
	sh := env.Shell
	if sh == nil {
		return false
	}
	pl := placeOf(env)
	nonPath := map[string]bool{}
	for _, s := range sh.NonPath {
		nonPath[s] = true
	}
	for i, group := range [][]string{sh.Targets, sh.AtTargets, sh.FileTargets, sh.Runs, sh.PathDrive} {
		for _, t := range group {
			// 命令里的普通值（-Format "H:mm"、"T:5 done"）不是路径：Targets 和 AtTargets 里盘符后面没有分隔符的不当驱动器相对路径
			// （重定向、按路径运行的程序是真的路径，照旧严格）
			if i < 2 && isDriveRelative(t) && (!strings.ContainsAny(t[2:], `\/`) || nonPath[t]) {
				continue
			}
			// 命令里的值像 //\s*TODO、// TODO 这样两个斜杠开头但不像共享（主机名再接共享名）的，是正则或注释，不是网络共享
			if i < 2 && uncLike(t) && !isShareShape(t) {
				continue
			}
			if TouchesProtected(expandHome(t), env.Workdir, env.ProtectedDirs, false) || touchesViaLink(pl, env, t, false) {
				return true
			}
		}
	}
	for _, t := range sh.SearchRoots {
		if TouchesProtected(expandHome(t), env.Workdir, env.ProtectedDirs, true) || touchesViaLink(pl, env, t, true) {
			return true
		}
	}
	return false
}

// wildcardTouchesProtected：通配符写在目录名里（C:\Users\x\sd?ta\c.yml、.\*\x.txt）时，通配符之前的目录部分
// 是受保护目录或它的上级，才算碰到——通配的那一段可能正好匹配上受保护目录。
// 通配符只在最后一段（*.go）、通配之前的目录部分在别处（src/**/*.ts）、正则写法（\s*\d+、\[ERROR\]）都不算。
func wildcardTouchesProtected(env Env, command string) bool {
	var cands []string
	if sh := env.Shell; sh != nil {
		cands = append(append(append(append(cands, sh.Targets...), sh.AtTargets...), sh.FileTargets...), sh.Runs...)
	} else {
		cands = strings.FieldsFunc(command, func(r rune) bool { return strings.ContainsRune(" \t\r\n'\"(),;|", r) })
	}
	for _, s := range cands {
		if wildcardDirTouches(s, env) {
			return true
		}
	}
	return false
}

func wildcardDirTouches(s string, env Env) bool {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "*?[")
	if i < 0 || (!reDriveHead.MatchString(s) && reRegexLike.MatchString(s)) {
		return false
	}
	// 通配符所在的那一段后面还得有分隔符（通配的是目录名，不是最后的文件名）
	if strings.IndexAny(s[i:], `\/`) < 0 {
		return false
	}
	cut := strings.LastIndexAny(s[:i], `\/`)
	prefix := ""
	if cut >= 0 {
		prefix = s[:cut+1]
	}
	switch {
	case prefix == "" && i == 0 && s != "":
		prefix = "." // *\x.txt：从当前目录往下通配
	case prefix == "":
		prefix = "." // a*\x
	case strings.Trim(prefix, `\/`) == "":
		return false // 开头就是分隔符再通配（\w+、/*/x）：多半是正则或写着玩，判不了
	}
	return TouchesProtected(expandHome(prefix), env.Workdir, env.ProtectedDirs, true)
}

// knownVars 把展开字符串里能静态知道值的变量换成值：$HOME、$env:USERPROFILE、$PWD 和环境里有的 $env:NAME
var reEnvVar = regexp.MustCompile(`(?i)\$\{?env:([a-z_][a-z0-9_]*)\}?`)

func substituteKnownVars(text, homeDir, pwd string, pwdKnown bool) string {
	s := strings.TrimSpace(text)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	s = reEnvVar.ReplaceAllStringFunc(s, func(m string) string {
		name := reEnvVar.FindStringSubmatch(m)[1]
		if strings.EqualFold(name, "USERPROFILE") || strings.EqualFold(name, "HOME") {
			return homeDir
		}
		if v := os.Getenv(name); v != "" {
			return v
		}
		return m
	})
	for _, v := range []string{"${HOME}", "$HOME"} {
		s = replaceFold(s, v, homeDir)
	}
	if pwdKnown {
		for _, v := range []string{"${PWD}", "$PWD"} {
			s = replaceFold(s, v, pwd)
		}
	}
	return s
}

func replaceFold(s, old, new string) string {
	low, lo := strings.ToLower(s), strings.ToLower(old)
	var b strings.Builder
	for {
		i := strings.Index(low, lo)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		// $HOME 后面紧跟标识符字符的不是它（$HOMEX）
		end := i + len(old)
		if strings.HasSuffix(lo, "}") || end >= len(s) || !isIdentByte(s[end]) {
			b.WriteString(s[:i])
			b.WriteString(new)
		} else {
			b.WriteString(s[:end])
		}
		s, low = s[end:], low[end:]
	}
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// hiddenReadsVerdict：读文件类命令里路径是算出来的（变量、括号、展开字符串、来自管道、前面换过目录看不清）。
//   - protected：静态能看出前缀（"$HOME\.ssh\…"、"$PWD\data\…"）并且代入已知的值以后落在受保护目录里，按受保护的问；
//   - unclear：代不出来，值不确定。算普通的问（可以记“这个对话”），不是每次都问。
func hiddenReadsVerdict(env Env) (protected, unclear bool) {
	sh := env.Shell
	if sh == nil {
		return false, false
	}
	if sh.CwdUnknown {
		unclear = true
	}
	for _, h := range sh.HiddenReads {
		if h.Text == "<管道>" {
			unclear = true
			continue
		}
		// $PWD 是写这条命令时的当前目录（前面 cd 过就不是工作目录）；当前目录看不清就不代入，$PWD 留着当“值不确定”
		pwd, pwdKnown := env.Workdir, true
		if h.CwdNull {
			pwdKnown = false
		} else if h.Cwd != "" {
			pwd = resolve(env.Workdir, expandHome(h.Cwd))
		}
		s := substituteKnownVars(h.Text, home(), pwd, pwdKnown)
		prefix := s
		if k := strings.IndexAny(s, "$`("); k >= 0 {
			prefix = s[:k]
			unclear = true
		}
		// 递归搜索的起点，代入出来的值完整（没有剩下的变量）：是受保护目录或它的上级都算碰到
		up := h.Recursive && strings.IndexAny(s, "$`(") < 0
		if prefix != "" && prefix != "." && TouchesProtected(expandHome(prefix), env.Workdir, env.ProtectedDirs, up) {
			protected = true
		}
		if up { // "$HOME\*" 这类末尾带通配的文件名模式：通配之前的目录才是搜索起点
			if i := strings.IndexAny(s, "*?["); i >= 0 {
				if dir := s[:strings.LastIndexAny(s[:i], `\/`)+1]; dir != "" && TouchesProtected(expandHome(dir), env.Workdir, env.ProtectedDirs, true) {
					protected = true
				}
			}
		}
		if prefix == "" || !strings.ContainsAny(s, `\/`) {
			unclear = true
		}
	}
	return protected, unclear
}

// touchesViaLink：目标联接解析（env.Real，gate 先查好的）以后的真实路径落在受保护目录里。没解析过的原样，等于没多看。
func touchesViaLink(pl placeT, env Env, t string, recursive bool) bool {
	t = expandHome(t)
	if t == "" || OutsideForm(t) || IsOpaquePath(t) {
		return false
	}
	real := pl.canon(resolve(env.Workdir, t))
	return TouchesProtected(real, env.Workdir, env.ProtectedDirs, recursive)
}
