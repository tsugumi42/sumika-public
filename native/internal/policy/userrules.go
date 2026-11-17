package policy

// 用户自己写的放行、拒绝规则（B11）：写法对齐 Claude Code 的 permissions（工具名加括号里的匹配），
// 自己映射到 Sumika 的工具。和"记住"共用同一份规则存储（approval.Grants），这里只管解析和匹配。
//
//	Bash(git status:*)   命令以 git status 开头（也认 "git status *"）；写 Bash 不带括号是所有命令。
//	                     Bash 也可以写成 PowerShell、powershell，对应的是 powershell 这个工具。
//	Write(src/**)        写、改文件（write、edit 两个工具）；路径按工作目录算，也认绝对路径和 ~/。
//	Edit(...)            同 Write。
//	Read(secrets/**)     读、列、搜、找（read、ls、grep、find）。
//	WebFetch(domain:x.com)  抓这个域名和它的子域名；WebSearch 没有括号内容。
//	Task(reader)         派这个名字的子任务。
//	mcp__服务器 / mcp__服务器__工具   MCP 的某个服务器或某个原始工具名（* 通配）。
//
// 拒绝先于一切放行（但在"可用场合"后面：不是主人 × 主窗口的，本来就用不了这些工具）；放行排在受保护路径、
// 灾难性命令、自己的端口、不是主人发起的后面，不能越过它们。
// 命令按分隔符（&& || ; | 换行）拆成一段一段比：拒绝只要任何一段命中（拆得过碎只会多拒），放行要每一段都被某条放行规则盖住，
// 而且命令里没有重定向、子表达式、后台符这类拆不稳的东西、命令也拆得开（Names/Unclear/Opaque 干净），否则放行规则不生效、照常问。

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sumika-native/internal/jsjson"
)

const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
	// EffectAsk：匹配到的调用每次都问，哪怕档位、记住、这个对话或别的放行规则会放行（规格-审批分级 8.7）。
	EffectAsk = "ask"
)

// UserRule 是一条解析过的规则。
type UserRule struct {
	Raw    string // 用户写的原文，如 Bash(git status:*)
	Effect string // allow / deny / ask
	Tool   string // 规范化的工具族：powershell、write、read、web_fetch、web_search、task、mcp
	Spec   string // 括号里的内容（去掉首尾空白）；空是这个工具族的全部
	Key    string // 规则存储里的 key：rule:<effect>:<原文>[@dir:项目目录][@conv:会话]
	// 作用范围（只有 allow、ask 能带；两样都空是全局）。两样都写要同时满足。
	Project string // 只对这个项目目录（含里面的子目录）里发起的调用生效；绝对路径
	Session string // 只对这个会话生效
}

// RuleKey 是规则存储里这条规则的 key；同一原文同一效果只留一条。
func RuleKey(effect, raw string) string { return "rule:" + effect + ":" + strings.TrimSpace(raw) }

// ScopedRuleKey 是带作用范围的规则的 key：同一原文在不同范围里是不同的规则，不能互相顶掉。
// 没有范围时和 RuleKey 完全一样，旧文件里的 key 不变。
func ScopedRuleKey(effect, raw, project, session string) string {
	k := RuleKey(effect, raw)
	if project = strings.TrimSpace(project); project != "" {
		k += "@dir:" + NormDir(project)
	}
	if session = strings.TrimSpace(session); session != "" {
		k += "@conv:" + session
	}
	return k
}

// NormDir 把目录写法归一：斜杠统一、去掉多余的点和结尾分隔符、不分大小写。路径比较都先过它。
func NormDir(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "/", `\`))
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	return strings.ToLower(strings.TrimRight(p, `\`))
}

// dirInside：wd 就是 dir 或在 dir 里面（都已过 NormDir）。
func dirInside(wd, dir string) bool {
	return wd != "" && dir != "" && (wd == dir || strings.HasPrefix(wd, dir+`\`))
}

// absDir：写进规则的项目目录必须是绝对路径（盘符或 UNC），相对路径没有"相对谁"可言。
func absDir(p string) bool {
	p = strings.ReplaceAll(strings.TrimSpace(p), "/", `\`)
	if strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return false // 设备路径写法：和普通写法的工作目录比不上，写进去永远不命中，让人换写法
	}
	if strings.HasPrefix(p, `\\`) {
		return len(p) > 2
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '\\' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// ParseScopedRule 解析一条带作用范围的规则。只有 allow、ask 能带范围（拒绝一律全局：范围只会让拒绝变窄）；
// MCP 规则不能带项目目录（mcp_call 的判定拿不到工作目录）。写错了说清楚，不悄悄当成全局。
func ParseScopedRule(raw, effect, project, session string) (UserRule, error) {
	r, err := ParseUserRule(raw, effect)
	if err != nil {
		return r, err
	}
	project, session = strings.TrimSpace(project), strings.TrimSpace(session)
	if project == "" && session == "" {
		return r, nil
	}
	if effect == EffectDeny {
		return UserRule{}, fmt.Errorf("拒绝规则不分范围，一律全局生效：%q", raw)
	}
	if project != "" {
		if !absDir(project) {
			return UserRule{}, fmt.Errorf("项目目录要写绝对路径，例如 D:\\Projects\\demo：%q", project)
		}
		if strings.Contains(project, "@") {
			return UserRule{}, fmt.Errorf("项目目录里不能有 @：%q", project)
		}
		if r.Tool == "mcp" {
			return UserRule{}, fmt.Errorf("MCP 规则不能限定项目目录（MCP 调用不带工作目录），可以限定会话：%q", raw)
		}
	}
	if strings.ContainsAny(session, "\r\n\t@") {
		return UserRule{}, fmt.Errorf("会话号写法不对：%q", session)
	}
	r.Project, r.Session = project, session
	r.Key = ScopedRuleKey(effect, raw, project, session)
	return r, nil
}

// scopeApplies：这条规则的作用范围包不包这次调用。
// unknownHits 说看不出场合时（工作目录或会话号是空的：内部调用、MCP 调用）怎么算——
// allow 不吃（范围不明就不放行），ask 要吃（范围不明就照样问）：两边都往"多问"的方向。
// 会话：allow 只认这个会话自己；ask 还认它派出的子任务（子任务替这个会话办事，不能躲开这个会话设的"每次问"）。
func (r UserRule) scopeApplies(env Env, unknownHits bool) bool {
	if r.Project != "" {
		if env.Workdir == "" {
			if !unknownHits {
				return false
			}
		} else {
			// 工作目录的真实路径（联接、符号链接解析后）也要看：allow 两边都得在项目里（不能靠联接绕进来），
			// ask 任一边在项目里就算（多问）
			proj := NormDir(r.Project)
			lit := dirInside(NormDir(env.Workdir), proj)
			real := dirInside(NormDir(placeOf(env).canon(env.Workdir)), proj)
			if (unknownHits && !(lit || real)) || (!unknownHits && !(lit && real)) {
				return false
			}
		}
	}
	if r.Session != "" {
		switch {
		case env.Conversation == "":
			if !unknownHits {
				return false
			}
		case env.Conversation == r.Session:
		case unknownHits && slices.Contains(env.Ancestors, r.Session):
		default:
			return false
		}
	}
	return true
}

var ruleHead = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_*.-]*)\s*(?:\((.*)\))?$`)

// ParseUserRule 解析一条规则。写错了说清楚错在哪、能写什么，不悄悄当成别的。
func ParseUserRule(raw, effect string) (UserRule, error) {
	if effect != EffectAllow && effect != EffectDeny && effect != EffectAsk {
		return UserRule{}, fmt.Errorf("规则效果只能是 allow、ask 或 deny：%q", effect)
	}
	text := strings.TrimSpace(raw)
	m := ruleHead.FindStringSubmatch(text)
	if m == nil {
		return UserRule{}, fmt.Errorf("规则写法不对：%q。写成 工具名 或 工具名(匹配)，例如 Bash(git status:*)", raw)
	}
	if strings.Contains(text, "@dir:") || strings.Contains(text, "@conv:") {
		return UserRule{}, fmt.Errorf("规则里不能出现 @dir: 或 @conv:（存储用它们标作用范围）：%q", raw)
	}
	r := UserRule{Raw: text, Effect: effect, Spec: strings.TrimSpace(m[2]), Key: RuleKey(effect, text)}
	name := strings.ToLower(m[1])
	switch {
	case name == "bash" || name == "powershell" || name == "shell":
		r.Tool = "powershell"
	case name == "write" || name == "edit" || name == "multiedit":
		r.Tool = "write"
	case name == "read" || name == "ls" || name == "grep" || name == "glob" || name == "find":
		r.Tool = "read"
	case name == "webfetch":
		r.Tool = "web_fetch"
		if r.Spec != "" && !strings.HasPrefix(strings.ToLower(r.Spec), "domain:") {
			return UserRule{}, fmt.Errorf("WebFetch 只认 domain:域名，例如 WebFetch(domain:example.com)：%q", raw)
		}
	case name == "websearch":
		r.Tool = "web_search"
	case name == "task":
		r.Tool = "task"
	case strings.HasPrefix(name, "mcp__"):
		r.Tool = "mcp"
		if m[2] != "" {
			return UserRule{}, fmt.Errorf("MCP 规则不带括号，写成 mcp__服务器 或 mcp__服务器__工具：%q", raw)
		}
		r.Spec = strings.TrimSpace(text[len("mcp__"):])
		if r.Spec == "" {
			return UserRule{}, fmt.Errorf("MCP 规则要写服务器名：%q", raw)
		}
	default:
		return UserRule{}, fmt.Errorf("不认识的工具名 %q。能写 Bash、Write、Edit、Read、WebFetch、WebSearch、Task，或 mcp__服务器__工具", m[1])
	}
	return r, nil
}

// MCPMatches：mcp__服务器 / mcp__服务器__工具（* 通配，大小写不分）是否盖住这个服务器的这个原始工具名。
func (r UserRule) MCPMatches(server, tool string) bool {
	if r.Tool != "mcp" {
		return false
	}
	spec := strings.ToLower(r.Spec)
	target := strings.ToLower(server + "__" + tool)
	if !strings.Contains(spec, "__") {
		// 只写了服务器：盖住它的所有工具
		return globText(spec, strings.ToLower(server))
	}
	return globText(spec, target)
}

// toolFamily 是一次调用属于哪个规则族；认不出（没有规则能写它）返回空。
func toolFamily(tool string) string {
	switch tool {
	case "powershell":
		return "powershell"
	case "write", "edit":
		return "write"
	case "read", "ls", "grep", "find":
		return "read"
	case "web_fetch", "web_search", "task":
		return tool
	case "mcp_call":
		return "mcp"
	}
	return ""
}

// MatchDeny 返回第一条命中的拒绝规则。命令按过碎的拆法比，路径连联接解析后的真实路径一起比。
func MatchDeny(rules []UserRule, tool string, args jsjson.Value, env Env) *UserRule {
	return matchLoose(rules, EffectDeny, tool, args, env)
}

// MatchAsk 返回第一条命中的 ask 规则：比法和拒绝规则一样宽（任何一段命中、原文里出现、分析器给的名字命中都算），
// 多问一次比漏问好；另外要作用范围包得住这次调用（范围不明时照样算命中）。
func MatchAsk(rules []UserRule, tool string, args jsjson.Value, env Env) *UserRule {
	return matchLoose(rules, EffectAsk, tool, args, env)
}

// matchLoose 是拒绝、ask 共用的宽松比法。
func matchLoose(rules []UserRule, effect, tool string, args jsjson.Value, env Env) *UserRule {
	fam := toolFamily(tool)
	if fam == "" {
		return nil
	}
	var segs []string
	if fam == "powershell" {
		segs = commandSegments(JSString(args, "command", ""))
	}
	for i := range rules {
		r := &rules[i]
		if r.Effect != effect || r.Tool != fam {
			continue
		}
		if effect != EffectDeny && !r.scopeApplies(env, true) {
			continue
		}
		if ruleHits(*r, tool, args, env, segs, true) {
			return r
		}
		// 分析器看穿的写法（-C、.exe、引号、反引号、别名）原文比不出来，按它给的命令名再比一遍
		if fam == "powershell" && r.Spec != "" && env.Shell != nil && namesHit(*r, env.Shell.Names) {
			return r
		}
	}
	return nil
}

// MatchAllow 返回盖住这次调用的放行规则（命令有多段时返回第一段命中的）；没有、或者放行条件不满足返回 nil。
func MatchAllow(rules []UserRule, tool string, args jsjson.Value, env Env) *UserRule {
	fam := toolFamily(tool)
	if fam == "" {
		return nil
	}
	if fam == "powershell" {
		sh := env.Shell
		cmd := JSString(args, "command", "")
		if sh == nil || sh.Opaque || len(sh.Unclear) > 0 || len(sh.MethodRisk) > 0 || len(sh.MustAsk) > 0 || unstableShell.MatchString(cmd) {
			return nil
		}
		segs := commandSegments(cmd)
		if len(segs) == 0 || !namesAreSegmentHeads(sh.Names, segs) {
			return nil
		}
		var first *UserRule
		for _, seg := range segs {
			hit := false
			for i := range rules {
				r := &rules[i]
				if r.Effect == EffectAllow && r.Tool == fam && r.scopeApplies(env, false) && segmentHits(*r, seg) {
					hit = true
					if first == nil {
						first = r
					}
					break
				}
			}
			if !hit {
				return nil
			}
		}
		return first
	}
	for i := range rules {
		r := &rules[i]
		if r.Effect == EffectAllow && r.Tool == fam && r.scopeApplies(env, false) && ruleHits(*r, tool, args, env, nil, false) {
			return r
		}
	}
	return nil
}

// unstableShell：拆不稳的写法，放行规则不认（拒绝规则不受影响）：重定向、括号（子表达式，以及括号里先执行的命令）、
// 反引号、单个 &（后台）、变量当命令。
var unstableShell = regexp.MustCompile("[<>`()]|(^|[^&])&([^&]|$)|\\$\\{")

// namesAreSegmentHeads：分析出的每个命令名都是某一段的开头命令。放行规则只盖住"每段开头"，
// 夹在参数里的命令（脚本块、嵌套写法）不在规则里，不能顺带放行。名字带子命令（git push）的只比第一个词。
func namesAreSegmentHeads(names, segs []string) bool {
	heads := map[string]bool{}
	for _, s := range segs {
		heads[headWord(s)] = true
	}
	for _, n := range names {
		if !heads[headWord(n)] {
			return false
		}
	}
	return true
}

// headWord：一段命令的第一个词，小写，去掉调用符、引号和 .exe。
func headWord(seg string) string {
	f := strings.Fields(strings.ToLower(seg))
	if len(f) == 0 {
		return ""
	}
	w := strings.Trim(f[0], "&'\"")
	if len(f) > 1 && w == "" { // "& git push"：& 单独成词
		w = strings.Trim(f[1], "&'\"")
	}
	return strings.TrimSuffix(w, ".exe")
}

// cmdAliases：常见别名归到同一组，拒绝规则写其中一个名字，换成别的写法也算命中。
var cmdAliases = map[string]string{
	"rm": "remove-item", "ri": "remove-item", "del": "remove-item", "erase": "remove-item", "rd": "remove-item", "rmdir": "remove-item",
	"cat": "get-content", "gc": "get-content", "type": "get-content",
	"cp": "copy-item", "copy": "copy-item", "cpi": "copy-item",
	"mv": "move-item", "move": "move-item", "mi": "move-item",
	"ls": "get-childitem", "dir": "get-childitem", "gci": "get-childitem",
	"sc": "set-content", "ni": "new-item",
	"iwr": "invoke-webrequest", "wget": "invoke-webrequest", "curl": "invoke-webrequest", "irm": "invoke-restmethod",
	"iex": "invoke-expression", "kill": "stop-process", "spps": "stop-process", "saps": "start-process", "start": "start-process",
}

func aliasForms(name string) []string {
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	head := strings.Fields(name + " ")[0]
	canon, ok := cmdAliases[head]
	if !ok {
		canon = head // 本身可能就是规范名（remove-item）
	}
	out := []string{name}
	for a, c := range cmdAliases {
		if c == canon && a != name {
			out = append(out, a)
		}
	}
	if canon != name {
		out = append(out, canon)
	}
	return out
}

// namesHit：分析器给出的命令名（已经去掉 -C 之类开关、.exe、引号、反引号）有没有被这条拒绝规则盖住。
func namesHit(r UserRule, names []string) bool {
	for _, n := range names {
		for _, f := range aliasForms(n) {
			if segmentHits(r, f) {
				return true
			}
		}
	}
	return false
}

var cmdSplit = regexp.MustCompile(`&&|\|\||[;|&\r\n]`)

func commandSegments(cmd string) []string {
	var out []string
	for _, s := range cmdSplit.Split(cmd, -1) {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ruleHits(r UserRule, tool string, args jsjson.Value, env Env, segs []string, deny bool) bool {
	switch r.Tool {
	case "powershell":
		if r.Spec == "" || r.Spec == "*" {
			return true
		}
		for _, s := range segs {
			if segmentHits(r, s) {
				return true
			}
		}
		// 拆得过碎也不放过：原文里按词边界出现这个前缀（引号里的、嵌套的），拒绝宁可多拒
		return deny && containsPrefixWords(JSString(args, "command", ""), r.Spec)
	case "write", "read":
		if r.Spec == "" || r.Spec == "*" {
			return true
		}
		raw := PathArg(args)
		one := func(p string) bool {
			if deny { // 指向本机盘的网络共享、设备路径写法，拒绝规则按还原后的本地路径比（多拒不吃亏）
				p = localOfShare(p)
			}
			if pathHits(r.Spec, p, env.Workdir) {
				if !deny { // 放行还要联接解析后的真实路径也在规则里，不能靠联接绕出去
					return pathHits(r.Spec, placeOf(env).canon(p), env.Workdir)
				}
				return true
			}
			if deny && (tool == "grep" || tool == "find") { // 递归搜索：被搜的目录是规则的上级，搜得到规则里面的内容
				pl := placeOf(env)
				return pathHits(r.Spec, pl.canon(p), env.Workdir) || specInside(r.Spec, p, env.Workdir) || specInside(r.Spec, pl.canon(p), env.Workdir)
			}
			return deny && pathHits(r.Spec, placeOf(env).canon(p), env.Workdir)
		}
		if one(ToolPath(raw, env.Workdir)) {
			return true
		}
		if deny { // 无盘符根相对路径（\secrets\a、/secrets/a）：策略和工具解释得不一样，三种解释任一命中就算（放行方向不变）
			for _, alt := range rootedAlternatives(raw, env.Workdir) {
				if one(alt) {
					return true
				}
			}
		}
		return false
	case "web_fetch":
		if r.Spec == "" {
			return true
		}
		return domainHits(strings.TrimSpace(r.Spec[len("domain:"):]), JSString(args, "url", ""), deny)
	case "web_search":
		return true
	case "mcp":
		return env.MCPServer != "" && r.MCPMatches(env.MCPServer, env.MCPTool)
	case "task":
		if r.Spec == "" || r.Spec == "*" {
			return true
		}
		return strings.EqualFold(r.Spec, JSString(args, "agent", ""))
	}
	return false
}

// segmentHits：一段命令是否被这条 Bash 规则盖住。
func segmentHits(r UserRule, seg string) bool {
	spec := strings.ToLower(strings.Join(strings.Fields(r.Spec), " "))
	if spec == "" || spec == "*" {
		return true
	}
	seg = strings.ToLower(seg)
	switch {
	case strings.HasSuffix(spec, ":*"):
		return prefixWords(seg, strings.TrimSpace(strings.TrimSuffix(spec, ":*")))
	case strings.HasSuffix(spec, " *") && !strings.Contains(strings.TrimSuffix(spec, " *"), "*"):
		return prefixWords(seg, strings.TrimSuffix(spec, " *"))
	case strings.Contains(spec, "*"):
		return globText(spec, seg)
	}
	return seg == spec
}

// prefixWords：seg 就是 prefix，或者以 prefix 加空格开头（按词边界，git status 不盖住 git statusx）。
func prefixWords(seg, prefix string) bool {
	if prefix == "" {
		return true
	}
	return seg == prefix || strings.HasPrefix(seg, prefix+" ")
}

// containsPrefixWords：原文里按词边界出现这个前缀（只给拒绝用的保守比法）。
func containsPrefixWords(text, spec string) bool {
	spec = strings.ToLower(strings.Join(strings.Fields(spec), " "))
	spec = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(spec, ":*"), " *"))
	if spec == "" || strings.Contains(spec, "*") {
		return false
	}
	words := strings.Fields(spec)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)(^|[^\w-])` + strings.Join(words, `\s+`) + `($|[^\w-])`).MatchString(text)
}

// globText：* 匹配任意字符（含空白）的整串通配，spec 和 text 都已小写。
func globText(spec, text string) bool {
	parts := strings.Split(spec, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`^` + strings.Join(parts, `.*`) + `$`).MatchString(text)
}

// domainHits：lenient 为真（拒绝、ask 用）时，主机名末尾的点（example.com.）也算同一个域名，宁多勿漏。
func domainHits(domain, rawURL string, lenient bool) bool {
	domain = strings.ToLower(strings.TrimPrefix(domain, "*."))
	if domain == "" {
		return false
	}
	u := strings.ToLower(rawURL)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.LastIndex(u, ":"); i >= 0 && !strings.Contains(u[i:], "]") {
		u = u[:i]
	}
	if lenient {
		u = strings.TrimRight(u, ".")
	}
	return u == domain || strings.HasSuffix(u, "."+domain)
}

// specInside：路径规则指到的位置（通配符之前的那一段目录）在 dir 里面或就是 dir——在 dir 下递归搜索会搜到规则里的内容。
func specInside(spec, dir, workdir string) bool {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "//") {
		spec = spec[1:]
	}
	spec = strings.ToLower(resolve(workdir, strings.ReplaceAll(expandHome(spec), "/", `\`)))
	if i := strings.IndexAny(spec, "*?"); i >= 0 {
		spec = spec[:i]
		if j := strings.LastIndex(spec, `\`); j >= 0 {
			spec = spec[:j]
		}
	}
	spec = strings.TrimRight(spec, `\`)
	low := strings.TrimRight(strings.ToLower(strings.ReplaceAll(dir, "/", `\`)), `\`)
	return spec != "" && (spec == low || strings.HasPrefix(spec, low+`\`))
}

// pathHits：绝对路径 p 是否被路径规则盖住。规则可以是绝对路径、~/ 开头、或者相对工作目录的；
// 带 * ? ** 的当通配（** 跨目录，* 不跨），不带的当目录或文件本身（目录盖住里面所有）。
func pathHits(spec, p, workdir string) bool {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "//") { // Claude Code 的绝对路径写法
		spec = spec[1:]
	}
	spec = expandHome(spec)
	spec = resolve(workdir, strings.ReplaceAll(spec, "/", `\`))
	low := strings.ToLower(strings.ReplaceAll(dropTrailingDotSpace(p), "/", `\`)) // 末尾的点和空格、数据流名按 Windows 的规整
	pat := strings.ToLower(spec)
	if !strings.ContainsAny(pat, "*?") {
		pat = strings.TrimRight(pat, `\`)
		return low == pat || strings.HasPrefix(low, pat+`\`)
	}
	var b strings.Builder
	b.WriteString("^")
	rs := []rune(pat) // 按字符走：路径里有中文，按字节拼正则会把多字节字符拆坏
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '*' && i+1 < len(rs) && rs[i+1] == '*':
			i++
			if i+1 < len(rs) && rs[i+1] == '\\' {
				i++
				b.WriteString(`(?:.*\\)?`)
			} else {
				b.WriteString(`.*`)
			}
		case c == '*':
			b.WriteString(`[^\\]*`)
		case c == '?':
			b.WriteString(`[^\\]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(low)
}
