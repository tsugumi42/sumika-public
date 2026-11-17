package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"sumika-native/internal/jsstr"
	"sumika-native/internal/store"
)

// ───────────── 命令拆开看：从解析结果判断（daemon/tools/shell.ts 的 judge） ─────────────
// 纯函数：从解析结果里挑出会改动什么、目标路径、看不清的地方。表、原因原文照 shell.ts 搬，改了那边这边跟着改

// Fetch 是要下载一个包来跑的（npx、bunx……）
type Fetch struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Declarable bool   `json:"declarable"`
}

// HiddenRead 是一处“路径是算出来的”：原文，和写它那条命令时的当前目录（$PWD 要用）
type HiddenRead struct {
	Text      string
	Cwd       string // "" 是工作目录
	CwdNull   bool   // 前面换过目录、看不清换到了哪
	Recursive bool   // 这是递归搜索命令的起点：代入出的值是受保护目录的上级也算碰到
}

// Verdict 是 judge 的结论（ShellVerdict）
type Verdict struct {
	Changes     []string `json:"changes"`
	Unclear     []string `json:"unclear"`
	Targets     []string `json:"targets"`
	Names       []string `json:"names"`
	ReadOnly    bool     `json:"readOnly"`
	MethodRisk  []string `json:"methodRisk"`
	Opaque      bool     `json:"opaque"`
	OpaqueKinds []string `json:"opaqueKinds"`
	FileTargets []string `json:"fileTargets"`
	Runs        []string `json:"runs"`
	MustAsk     []string `json:"mustAsk"`
	Fetches     []Fetch  `json:"fetches"`

	// 下面几样不进 JSON（和 Bun 的对拍不受影响），Judge 顺手记下，给“碰受保护目录”的判断用（judgeextra.go）
	AtTargets   []string     `json:"-"` // 换过目录以后，相对目标接在当前目录后面的真实位置
	CwdUnknown  bool         `json:"-"` // 前面换过目录、看不清换到了哪，后面有相对目标
	SearchRoots []string     `json:"-"` // 递归搜索从哪里开始搜（没写搜哪里就是当前目录）
	HiddenReads []HiddenRead `json:"-"` // 读文件、搜文件的命令里路径是变量、括号、展开字符串的写法（原文），或来自管道
	NonPath     []string     `json:"-"` // echo / Write-Host 这类只输出的命令的参数：不是路径，不做“网络共享、驱动器相对”这类形状判断
	PathDrive   []string     `json:"-"` // 绑在路径参数（-Path 等）上的驱动器相对写法（Get-Content C:foo），严格按路径看
	Cmds        []CmdLine    `json:"-"` // 拆出来的每一条简单命令（程序名和参数原文；续行、子表达式、脚本块里的都在），给“谁在运行 sumika-ctl”这类判断用（selfctl.go）
}

// CmdLine 是拆出来的一条简单命令：程序名（& 调用时是被调用的那个，没写死是空）和各个参数的文字（常量用它的值，别的用原文）
type CmdLine struct {
	Name string
	Args []string
}

var (
	readVerbs  = set(`get test find search select where sort group measure compare format convertfrom convertto resolve split join read wait`)
	memoryOnly = set(`start-sleep write-host write-verbose write-warning write-error write-debug write-information write-progress
out-host out-null out-default set-variable new-variable clear-variable remove-variable add-member set-strictmode new-object
new-timespan new-guid set-alias new-alias oss pause clear-host prompt tabexpansion2`)
	builtinFn    = map[string]string{"mkdir": "new-item"}
	fileNouns    = set(`item childitem content itemproperty itempropertyvalue acl archive file csv clixml`)
	fileCmdlets  = set(`tee-object`)
	targetParams = set(`path literalpath filepath`)
	destParams   = set(`destination destinationpath`)
	readSource   = set(`copy-item copy-itemproperty compress-archive expand-archive`)
	netCmdlets   = set(`invoke-webrequest invoke-restmethod`)
	commonParams = `erroraction warningaction informationaction progressaction errorvariable warningvariable informationvariable outvariable outbuffer pipelinevariable verbose debug`
	netFree      = set(commonParams + ` body headers method custommethod contenttype useragent form timeoutsec connectiontimeoutseconds
operationtimeoutseconds maximumredirection maximumretrycount retryintervalsec credential token authentication certificate certificatethumbprint
transferencoding httpversion sslprotocol skipcertificatecheck skipheadervalidation skiphttperrorcheck usebasicparsing disablekeepalive
statuscodevariable responseheadersvariable sessionvariable`)
	contentFree = set(commonParams + ` value inputobject encoding stream width delimiter`)
	cdCmdlets   = set(`set-location push-location pop-location`)
	evalOpaque  = set(`invoke-expression invoke-command start-process add-type cmd powershell pwsh bash sh wsl cscript wscript mshta rundll32 regsvr32`)
	systemProgs = set(`reg net net1 sc schtasks setx taskkill tskill shutdown logoff wmic netsh sfc dism powercfg
runas msiexec winget choco scoop bitsadmin certutil wevtutil auditpol secedit gpupdate mountvol bcdedit diskpart format
vssadmin cipher takeown fsutil pnputil reagentc manage-bde`)
	fileProgs   = set(`robocopy xcopy icacls attrib`)
	netProgs    = set(`curl wget`)
	toolRun     = set(`python python3 pythonw py node bun npm pnpm yarn deno pip pip3 ruby perl php`)
	pythons     = set(`python python3 pythonw py`)
	downloadRun = set(`npx bunx pnpx uvx`)
	pmDownload  = map[string]map[string]bool{"npm": set(`exec x`), "pnpm": set(`dlx`), "yarn": set(`dlx`), "bun": set(`x`)}
	runnerShell = set(`-c --call --shell-mode`)
	pkgManagers = set(`npm pnpm yarn bun`)
	pmDirSwitch = []string{"--prefix", "--dir", "--cwd", "-C"}
	pmReadSub   = set(`ls list ll la outdated view info show root bin prefix search help`)
	runnerValue = set(`-p --package --from --with --python --registry --cache --userconfig --workspace -w --prefix --dir --cwd -C`)
	pipValue    = []string{"-t", "--target", "--prefix", "--root", "--src", "-r", "--requirement", "-c", "--constraint", "-e", "--editable", "-i", "--index-url", "--extra-index-url", "-f", "--find-links", "--cache-dir", "-d", "--dest", "-w", "--wheel-dir"}
	pipPlace    = []string{"-t", "--target", "--prefix", "--root", "--src", "-d", "--dest", "-w", "--wheel-dir"}
	gitGlobalV  = set(`-C -c --git-dir --work-tree --namespace --exec-path --config-env`)
	gitGlobalD  = set(`-C --git-dir --work-tree`)
	gitSubValue = set(`-b -B --branch -o --origin --depth --reference --reference-if-able -c --config --template --separate-git-dir
-u --upload-pack --filter -j --jobs --shallow-since --shallow-exclude --reason -f --file -m --message -F`)
	pipeHead   = set(`get-childitem get-item`)
	pipeFilter = set(`where-object sort-object select-object`)
)

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// 包管理器和工具链的子命令白名单（shell.ts TOOLCHAINS）
type subRule struct {
	kind int // 0 要问、1 放行、2 看 fn
	fn   func(sws, after []string) string
}

type toolchain struct {
	subs   map[string]subRule
	script bool
	file   bool
	value  []string
}

func subs(allow, ask string, more map[string]func(sws, after []string) string) map[string]subRule {
	m := map[string]subRule{}
	for _, s := range strings.Fields(ask) {
		m[s] = subRule{kind: 0}
	}
	for _, s := range strings.Fields(allow) {
		m[s] = subRule{kind: 1}
	}
	for k, f := range more {
		m[k] = subRule{kind: 2, fn: f}
	}
	return m
}

func has(sws []string, names ...string) bool {
	for _, s := range sws {
		for _, n := range names {
			if s == n {
				return true
			}
		}
	}
	return false
}

func first(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

var (
	reInstallUpd = regexp.MustCompile(`^(install|uninstall|update)$`)
	reUvPython   = regexp.MustCompile(`^(list|find|pin|dir)$`)
	reInfoList   = regexp.MustCompile(`^(info|list)$`)
	reExportList = regexp.MustCompile(`^(export|list)$`)
)

func condaEnv(sws, _ []string) string {
	if has(sws, "-p", "--prefix") && !has(sws, "-n", "--name") {
		return ""
	}
	return "装进用户目录下的 conda 环境（用 -p 指定工作目录里的环境就不用问）"
}

var toolchains = map[string]toolchain{
	"npm": {value: []string{"--prefix", "-C", "--workspace", "-w", "--loglevel", "--registry", "--userconfig", "--cache", "--location"}, subs: subs(
		"install i in ins inst insta instal isnt isnta isntal isntall add ci clean-install ic install-clean isntall-clean install-ci-test cit clean-install-test sit install-test it "+
			"uninstall remove rm r un update up upgrade udpate run run-script rum urn test tst t start stop restart rebuild rb dedupe ddp prune pack version audit outdated "+
			"ls list la ll explain why view info show v query fund pkg shrinkwrap help help-search search find s se root prefix sbom diff find-dupes init innit create", "", nil)},
	"pnpm": {script: true, value: []string{"-C", "--dir", "--filter", "-F", "--reporter", "--loglevel"}, subs: subs(
		"add install i update up upgrade remove rm uninstall un import rebuild rb prune fetch install-test it patch patch-commit patch-remove audit list ls ll la outdated why "+
			"licenses run run-script test t exec start pack recursive m multi root bin init dedupe approve-builds ignored-builds help view info show v search sbom",
		"link ln unlink publish server store setup config c get set env cat-file cat-index find-hash self-update login logout adduser whoami owner dist-tag team token access "+
			"profile deprecate undeprecate unpublish completion docs home bugs repo ping star stars unstar cache deploy doctor dlx create", nil)},
	"yarn": {script: true, value: []string{"--cwd"}, subs: subs(
		"add audit autoclean bin check dedupe exec explain generate-lock-entry help import info init install licenses list node outdated pack patch patch-commit rebuild remove "+
			"run start test unplug up upgrade upgrade-interactive version versions why workspace workspaces constraints",
		"cache config create dlx global link unlink login logout npm owner plugin policies publish set stage tag team", nil)},
	"bun": {script: true, value: []string{"--cwd", "-r", "--preload", "--config", "-c", "--env-file"}, subs: subs(
		"run test install i add a remove rm update outdated audit build init patch info why help",
		"x exec repl link unlink publish pm create c upgrade completions discord feedback getcompletes", nil)},
	"pip": {value: pipValue, subs: subs("install uninstall download wheel freeze list show check hash inspect index help debug lock", "", nil)},
	"cargo": {value: []string{"--manifest-path", "--config", "-Z", "--color"}, subs: subs(
		"build b check c test t bench run r doc d clean fmt clippy fix add remove rm update fetch tree metadata generate-lockfile vendor package new init locate-project "+
			"verify-project pkgid search help version read-manifest report rustc rustdoc info", "", nil)},
	"go": {subs: subs("build test run vet fmt mod get generate list doc version work tool fix help", "", map[string]func(sws, after []string) string{
		"env": func(sws, _ []string) string {
			if has(sws, "-w", "-u") {
				return "go env -w / -u 改的是用户级的 go 配置"
			}
			return ""
		},
		"clean": func(sws, _ []string) string {
			if has(sws, "-cache", "-modcache", "-testcache", "-fuzzcache") {
				return "go clean 清的是全局缓存"
			}
			return ""
		},
	})},
	"dotnet": {file: true, subs: subs("build test run restore clean publish pack format add remove list sln watch msbuild help vstest package reference", "", map[string]func(sws, after []string) string{
		"new": func(sws, after []string) string {
			if reInstallUpd.MatchString(jsstr.LowerASCII(first(after))) || has(sws, "--install", "--uninstall", "-i", "-u") {
				return "dotnet new install / uninstall 装的是用户级的模板"
			}
			return ""
		},
	})},
	"gem": {subs: subs("list search info contents environment env help specification query which dependency outdated build fetch unpack stale", "", nil)},
	"uv": {value: []string{"--directory", "--project", "--cache-dir", "--config-file", "--color"}, subs: subs("run init add remove sync lock export tree venv build help version format", "", map[string]func(sws, after []string) string{
		"pip": func(sws, _ []string) string {
			if has(sws, "--system", "--break-system-packages") {
				return "uv pip --system 装进系统的 Python"
			}
			return ""
		},
		"python": func(_, after []string) string {
			if reUvPython.MatchString(jsstr.LowerASCII(first(after))) {
				return ""
			}
			return "uv python 装的是用户级的 Python"
		},
	})},
	"pipx": {subs: subs("list help environment", "", nil)},
	"poetry": {value: []string{"-C", "--directory", "-P", "--project"}, subs: subs("install add remove update lock run build check show init new export version search about help list sync source debug", "", map[string]func(sws, after []string) string{
		"env": func(_, after []string) string {
			if reInfoList.MatchString(jsstr.LowerASCII(first(after))) {
				return ""
			}
			return "poetry env 改的是用户目录下的虚拟环境"
		},
	})},
	"conda": {subs: subs("list info search help run compare", "", map[string]func(sws, after []string) string{
		"env": func(_, after []string) string {
			if reExportList.MatchString(jsstr.LowerASCII(first(after))) {
				return ""
			}
			return "conda env 改的是用户目录下的 conda 环境"
		},
		"install": condaEnv, "create": condaEnv, "remove": condaEnv, "uninstall": condaEnv, "update": condaEnv,
	})},
}

// 解释器"把参数里的一段代码直接跑"的开关（shell.ts INTERP）；ruby、perl 的 (?!…) 改成先看第二个字
type interp struct {
	inline     func(sw string) bool
	takesValue map[string]bool
}

var (
	reNodeInline = regexp.MustCompile(`^(-e|-p|-pe|-ep|--eval|--print)$`)
	reBunInline  = regexp.MustCompile(`^(-e|-p|--eval|--print)$`)
	rePyInline   = regexp.MustCompile(`^-[A-Za-z]*c`)
	reRubyInline = regexp.MustCompile(`^-[a-zA-Z]*e`)
	rePerlInline = regexp.MustCompile(`^-[a-zA-Z]*[eE]`)
)

// notAfterDash 像 /^-(?![…])/：- 后面那个字不是这几个之一（没有字也算）
func notAfterDash(sw, chars string) bool {
	return len(sw) < 2 || !strings.ContainsRune(chars, rune(sw[1]))
}

var interps = map[string]interp{
	"node":   {inline: reNodeInline.MatchString, takesValue: set(`-r --require --import --loader --experimental-loader -C --conditions --env-file --input-type --title`)},
	"bun":    {inline: reBunInline.MatchString, takesValue: set(`-r --preload --cwd --env-file --config -c`)},
	"python": {inline: rePyInline.MatchString, takesValue: set(`-X -W -Q`)},
	"ruby":   {inline: func(s string) bool { return notAfterDash(s, "rICEFix") && reRubyInline.MatchString(s) }, takesValue: set(`-r -I -C`)},
	"perl":   {inline: func(s string) bool { return notAfterDash(s, "MmIix") && rePerlInline.MatchString(s) }, takesValue: set(`-I -M -m`)},
	"php":    {inline: func(s string) bool { return s == "-r" }, takesValue: set(`-c -d -f`)},
}

// 已知工具链里"值是内容"的开关（shell.ts CONTENT_ARGS）：现在只有 git commit 的 -m / --message
var contentArgs = map[string]*regexp.Regexp{"git commit": regexp.MustCompile(`^(-[asenqv]*m|--message)$`)}

var (
	reMessageEq = regexp.MustCompile(`^--message=`)
	urlLike     = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
	rootedHead  = regexp.MustCompile(`^([a-z]:|[\\/]|~)`)
	rootedDrive = regexp.MustCompile(`^[a-z]\w+:`)
	elsewhere   = regexp.MustCompile(`^([a-z]:|\\|~)|(^|[\\/])\.\.([\\/]|$)`)
	plainPkg    = regexp.MustCompile(`^(@[a-z0-9][\w.~-]*/)?[a-z0-9][\w.~-]*$`)
	pkgVersion  = regexp.MustCompile(`^(@?[^@]+)@[^\n\r\x{2028}\x{2029}]*$`)
	reDriveOnly = regexp.MustCompile(`^[a-z]:$`)
	reTFN       = regexp.MustCompile(`^(true|false|null)$`)
	reQuoteHead = regexp.MustCompile(`^['"]`)
	reWild      = regexp.MustCompile(`[*?[\]]`)
	reBareDir   = regexp.MustCompile(`^[^\n\r\x{2028}\x{2029}]*[\\/]`)
	reBareExt   = regexp.MustCompile(`\.(exe|cmd|bat|com)$`)
	reVerbNoun  = regexp.MustCompile(`^[a-z]+-[^\n\r\x{2028}\x{2029}]`)
	reNullRedir = regexp.MustCompile(`^\$null$`)
	reFol       = regexp.MustCompile(`^-fol`)
	reE         = regexp.MustCompile(`^-e`)
	reFileArg   = regexp.MustCompile(`[\\/]|\.[a-z0-9]+$`)
	reQuotes    = regexp.MustCompile(`['"]`)
)

func isURL(v string) bool { return urlLike.MatchString(jsstr.LowerASCII(v)) }

func isElsewhere(v string) bool { return elsewhere.MatchString(jsstr.LowerASCII(v)) }

// rooted 像 ROOTED：自己就带着完整位置的路径（盘符、根、~、PSDrive / provider）；[a-z]\w+: 后面要跟着不是空白的字或结尾
func rooted(p string) bool {
	low := jsstr.LowerASCII(p)
	if rootedHead.MatchString(low) || strings.Contains(low, "::") {
		return true
	}
	if m := rootedDrive.FindString(low); m != "" {
		if len(m) == len(low) {
			return true
		}
		return !jsstr.IsSpace([]rune(low[len(m):])[0])
	}
	return false
}

// 静态方法里只放行纯计算、只读的（shell.ts SAFE_STATIC）
var safeStaticAll = setOf(func() []string {
	var out []string
	for _, t := range strings.Fields(`String Math MathF DateTime DateTimeOffset TimeSpan Guid Convert Text.RegularExpressions.Regex Char Boolean Byte
Int16 Int32 Int64 UInt16 UInt32 UInt64 Single Double Decimal BitConverter Enum Version Uri Net.WebUtility Text.Encoding
Linq.Enumerable Globalization.CultureInfo`) {
		out = append(out, "system."+strings.ToLower(t))
	}
	return out
}())

var safeStaticSome = map[string]map[string]bool{
	"system.io.path": set(`combine join getfilename getfilenamewithoutextension getextension getdirectoryname getfullpath ispathrooted ispathfullyqualified
changeextension getrelativepath getpathroot gettemppath getrandomfilename hasextension trimendingdirectoryseparator endsindirectoryseparator
getinvalidfilenamechars getinvalidpathchars exists`),
}

func safeStatic(typ *string, method string) bool {
	if typ == nil || *typ == "" {
		return false
	}
	t := strings.ToLower(*typ)
	if safeStaticAll[t] {
		return true
	}
	return safeStaticSome[t][method]
}

// 实例方法里只读、只动内存的（shell.ts SAFE_INSTANCE）
var safeInstance = set(`tostring trim trimstart trimend split substring tolower toupper tolowerinvariant toupperinvariant
contains startswith endswith indexof lastindexof indexofany padleft padright equals compareto gettype gethashcode normalize
toarray where foreach getenumerator containskey containsvalue add remove clear insert push pop enqueue dequeue peek
getvalue getvaluenames getsubkeynames getfiles getdirectories enumeratefiles enumeratedirectories`)

// tok 是参数拆成的一串里的一项：开关、写死的值、看不清的写法（shell.ts Tok）。of、bind 是空字符串就当没有（JS 里空串也是"没有"）
type tok struct {
	kind int // tokSw、tokVal、tokHidden
	sw   string
	val  string
	of   string
	bind string
	text string
}

const (
	tokSw = iota
	tokVal
	tokHidden
)

func tokens(args []Arg) []tok {
	var out []tok
	var add func(x Arg, of, up string)
	add = func(x Arg, of, up string) {
		bind := up
		if x.Bind != nil {
			bind = *x.Bind
		}
		if x.Kind == "CommandParameterAst" {
			// -Param:值 的值跟在开关后面（标上 of）；不带值的照原样（大小写有用：git -C 和 -c 不一样）
			sw := x.Text
			if x.Arg != nil {
				p := ""
				if x.Param != nil {
					p = *x.Param
				}
				sw = "-" + p
			}
			out = append(out, tok{kind: tokSw, sw: sw})
			if x.Arg != nil {
				add(*x.Arg, sw, "")
			}
			return
		}
		if x.Var != nil && reTFN.MatchString(jsstr.LowerASCII(*x.Var)) {
			return // -Force:$true 这类：开关的值，不是目标
		}
		if x.Elements != nil {
			for _, e := range *x.Elements {
				add(e, of, bind)
			}
			return
		}
		if x.Value != nil {
			v := *x.Value
			// 没加引号、以 - 开头的是开关；--k=v 拆成开关 + 它的值
			if of == "" && strings.HasPrefix(v, "-") && x.Kind == "StringConstantExpressionAst" && !reQuoteHead.MatchString(x.Text) {
				if i := strings.Index(v, "="); i > 0 {
					out = append(out, tok{kind: tokSw, sw: v[:i]}, tok{kind: tokVal, val: v[i+1:], of: v[:i]})
				} else {
					out = append(out, tok{kind: tokSw, sw: v})
				}
				return
			}
			out = append(out, tok{kind: tokVal, val: v, of: of, bind: bind})
			return
		}
		out = append(out, tok{kind: tokHidden, text: x.Text, bind: bind}) // 变量、括号表达式、子表达式、拼出来的字符串、脚本块……
	}
	for _, a := range args {
		add(a, "", "")
	}
	return out
}

type posOpt struct {
	pos []string
	opt map[string][]string
}

// positionals 是位置参数和带值开关的值
func positionals(toks []tok, valueSw map[string]bool) posOpt {
	r := posOpt{opt: map[string][]string{}}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokSw {
			if valueSw[t.sw] && i+1 < len(toks) {
				if n := toks[i+1]; n.kind == tokVal && (n.of == "" || n.of == t.sw) {
					r.opt[t.sw] = append(r.opt[t.sw], n.val)
					i++
				}
			}
			continue
		}
		if t.kind == tokVal {
			if t.of != "" {
				r.opt[t.of] = append(r.opt[t.of], t.val)
			} else {
				r.pos = append(r.pos, t.val)
			}
		}
	}
	return r
}

// bare 是查表用的名字：去掉目录和扩展名
func bare(n string) string {
	n = reBareDir.ReplaceAllString(n, "")
	if m := reBareExt.FindStringIndex(jsstr.LowerASCII(n)); m != nil {
		n = n[:m[0]]
	}
	return strings.ToLower(n)
}

func isLinkType(toks []tok) bool {
	for _, t := range toks {
		if t.kind == tokVal && strings.ToLower(t.bind) == "itemtype" {
			v := strings.ToLower(t.val)
			ok := v != "" && !reWild.MatchString(v) && (strings.HasPrefix("directory", v) || strings.HasPrefix("container", v) || strings.HasPrefix("file", v))
			return !ok
		}
	}
	return false
}

func looseOf(tool string, toks []tok) []string {
	rule := contentArgs[tool]
	var out []string
	for k, t := range toks {
		if t.kind != tokHidden {
			continue
		}
		if rule != nil {
			if k > 0 && toks[k-1].kind == tokSw && rule.MatchString(toks[k-1].sw) {
				continue // -m $msg
			}
			if reMessageEq.MatchString(t.text) {
				continue // --message=$msg 连在一起写
			}
		}
		out = append(out, t.text)
	}
	return out
}

func vals(toks []tok, pred func(tok) bool) []string {
	var out []string
	for _, t := range toks {
		if t.kind == tokVal && (pred == nil || pred(t)) {
			out = append(out, t.val)
		}
	}
	return out
}

func idxOf(c Command) int {
	if c.Idx == nil {
		return 0
	}
	return *c.Idx
}

func canonOf(c Command) string {
	if c.Canon != nil {
		return *c.Canon
	}
	if c.Name != nil && *c.Name != "" {
		return bare(*c.Name)
	}
	return ""
}

// pipeRoots 是管道送来要改的东西出自哪里（shell.ts pipeRoots）；认不出返回 nil
func pipeRoots(a Analysis, c Command) []string {
	k := idxOf(c)
	if c.Pipe == nil || *c.Pipe == "" || k < 1 {
		return nil
	}
	var line []Command
	for _, x := range a.Commands {
		if x.Pipe != nil && *x.Pipe == *c.Pipe && idxOf(x) < k {
			line = append(line, x)
		}
	}
	sort.SliceStable(line, func(i, j int) bool { return idxOf(line[i]) < idxOf(line[j]) })
	if len(line) != k {
		return nil
	}
	for i, x := range line {
		if x.Idx == nil || *x.Idx != i {
			return nil
		}
	}
	head, mid := line[0], line[1:]
	if !pipeHead[canonOf(head)] || head.Meta == nil || !*head.Meta {
		return nil
	}
	ht := tokens(head.Args)
	for _, t := range ht {
		if (t.kind == tokHidden && (t.bind == "" || targetParams[strings.ToLower(t.bind)])) || (t.kind == tokSw && reFol.MatchString(jsstr.LowerASCII(t.sw))) {
			return nil
		}
	}
	for _, m := range mid {
		n := canonOf(m)
		if !pipeFilter[n] || m.Meta == nil || !*m.Meta {
			return nil
		}
		if n == "select-object" {
			for _, t := range tokens(m.Args) {
				if t.kind == tokHidden || (t.kind == tokSw && reE.MatchString(jsstr.LowerASCII(t.sw))) {
					return nil
				}
			}
		}
	}
	roots := vals(ht, func(t tok) bool { return t.bind != "" && targetParams[strings.ToLower(t.bind)] })
	if len(roots) == 0 {
		return []string{"."}
	}
	return roots
}

// Judge 从解析结果里挑出会改动什么、目标路径、看不清的地方（shell.ts judge）
func Judge(a Analysis) Verdict {
	v := Verdict{Changes: []string{}, Unclear: []string{}, Targets: []string{}, Names: []string{}, MethodRisk: []string{}, OpaqueKinds: []string{}, FileTargets: []string{}, Runs: []string{}, MustAsk: []string{}, Fetches: []Fetch{}}
	hide := func(why, kind string) {
		v.Unclear = append(v.Unclear, why)
		v.Opaque = true
		for _, k := range v.OpaqueKinds {
			if k == kind {
				return
			}
		}
		v.OpaqueKinds = append(v.OpaqueKinds, kind)
	}
	if len(a.Errors) > 0 {
		v.Opaque = true
		v.OpaqueKinds = append(v.OpaqueKinds, "parse")
	}
	for _, e := range a.Errors {
		v.Unclear = append(v.Unclear, "语法有问题："+e)
	}
	// 当前目录：cwd 为 "" 是工作目录；cwdNull 是换过、看不清换到了哪
	cwd, cwdNull := "", false
	type place struct {
		v    string
		null bool
	}
	var stack []place
	at := func(p string) (string, bool) {
		if (!cwdNull && cwd == "") || rooted(p) {
			return p, true
		}
		if cwdNull {
			return "", false
		}
		return win32Join(cwd, p), true
	}
	fileAt := func(p, who string) {
		if t, ok := at(p); !ok {
			hide(fmt.Sprintf("%s：前面换过目录、看不清换到了哪，%s 落在哪看不出来", who, p), "cwd")
		} else {
			v.FileTargets = append(v.FileTargets, t)
		}
	}
	type event struct {
		at       int
		cmd      *Command
		redirect string
		isRedir  bool
	}
	for _, c := range a.Commands {
		line := CmdLine{}
		if c.Name != nil {
			line.Name = *c.Name
		}
		for _, x := range c.Args {
			if x.Value != nil {
				line.Args = append(line.Args, *x.Value)
			} else {
				line.Args = append(line.Args, x.Text)
			}
		}
		v.Cmds = append(v.Cmds, line)
	}
	var events []event
	for i := range a.Commands {
		c := &a.Commands[i]
		p := i
		if c.At != nil {
			p = *c.At
		}
		events = append(events, event{at: p, cmd: c})
	}
	for i, r := range a.Redirects {
		p := 1<<53 - 1
		if a.RedirectAt != nil && i < len(*a.RedirectAt) {
			p = (*a.RedirectAt)[i]
		}
		events = append(events, event{at: p, redirect: r, isRedir: true})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at < events[j].at })
	for _, ev := range events {
		if ev.isRedir { // > $null、2>$null 是扔掉输出，不算写
			t := jsstr.Trim(ev.redirect)
			if reNullRedir.MatchString(jsstr.LowerASCII(t)) {
				continue
			}
			v.Changes = append(v.Changes, "写到文件："+ev.redirect)
			if strings.Contains(t, "$") {
				hide("重定向到变量拼的路径："+t, "loose")
			} else {
				if strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "'") {
					t = t[1:]
				}
				if strings.HasSuffix(t, `"`) || strings.HasSuffix(t, "'") {
					t = t[:len(t)-1]
				}
				fileAt(t, "重定向")
			}
			continue
		}
		c := *ev.cmd
		if c.Name == nil || *c.Name == "" {
			hide("有一段命令名是算出来的（& 后面跟变量或表达式）", "dynamic")
			continue
		}
		raw := *c.Name
		byPath := strings.ContainsAny(raw, `\/`)
		name := bare(raw)
		canon0 := name
		if c.Canon != nil {
			canon0 = *c.Canon
		}
		canon := canon0
		if b, ok := builtinFn[canon0]; ok {
			canon = b
		}
		toks := tokens(c.Args)
		vs := vals(toks, nil)
		var sws, hidden []string
		for _, t := range toks {
			switch t.kind {
			case tokSw:
				sws = append(sws, t.sw)
			case tokHidden:
				hidden = append(hidden, t.text)
			}
		}
		v.Targets = append(v.Targets, vs...)
		noteExtra(&v, a, c, canon, name, toks, at, cwd, cwdNull)
		named := func(n string) {
			for _, x := range append([]string{n}, func() []string {
				if byPath {
					return []string{strings.ToLower(raw)}
				}
				return nil
			}()...) {
				if !contains(v.Names, x) {
					v.Names = append(v.Names, x)
				}
			}
		}
		isCmdlet := reVerbNoun.MatchString(name)
		if c.Cmdlet != nil {
			isCmdlet = *c.Cmdlet
		}
		if byPath && !isCmdlet {
			if r, ok := at(raw); !ok {
				hide(fmt.Sprintf("前面换过目录、看不清 %s 在哪", raw), "cwd")
			} else {
				v.Runs = append(v.Runs, r)
			}
		}

		// ——— git：先跳过 git 自己的全局开关，第一个剩下的才是子命令 ———
		if name == "git" {
			var dirs []string
			i, sub := 0, ""
			for ; i < len(toks); i++ {
				t := toks[i]
				if t.kind == tokSw {
					if gitGlobalV[t.sw] && i+1 < len(toks) {
						if n := toks[i+1]; n.kind == tokVal && (n.of == "" || n.of == t.sw) {
							if gitGlobalD[t.sw] {
								dirs = append(dirs, n.val)
							}
							i++
						}
					}
					continue
				}
				if t.kind == tokVal && t.of == "" {
					sub = strings.ToLower(t.val)
					i++
				}
				break
			}
			if sub != "" {
				named("git " + sub)
			} else {
				named("git")
			}
			if sub == "" && len(hidden) == 0 {
				continue // 光一个 git：打印帮助
			}
			if gitReadOnly[sub] {
				if len(hidden) > 0 {
					hide(fmt.Sprintf("git %s 的参数用了变量或表达式：%s", sub, strings.Join(hidden, " ")), "loose")
					continue
				}
				subToks := toks[i:]
				outs := positionals(subToks, set(`--output`)).opt["--output"]
				var away []string
				for _, t := range subToks {
					if t.kind == tokVal && t.of != "--output" && !contains(outs, t.val) && !isURL(t.val) && isElsewhere(t.val) {
						away = append(away, t.val)
					}
				}
				if len(outs) == 0 && len(away) == 0 {
					continue
				}
				v.Changes = append(v.Changes, "git "+strings.Join(vs, " "))
				for _, o := range outs {
					fileAt(o, "git "+sub+" --output")
				}
				for _, x := range away {
					fileAt(x, "git "+sub)
				}
				continue
			}
			v.Changes = append(v.Changes, "git "+strings.Join(vs, " "))
			if loose := looseOf("git "+sub, toks); len(loose) > 0 {
				hide("git 的参数用了变量或表达式："+strings.Join(loose, " "), "loose")
				continue
			}
			po := positionals(toks[i:], gitSubValue)
			for _, d := range dirs {
				fileAt(d, "git -C")
			}
			for _, d := range po.opt["--separate-git-dir"] {
				fileAt(d, "git "+sub)
			}
			if sub == "clone" && len(po.pos) > 1 && po.pos[1] != "" {
				fileAt(po.pos[1], "git clone")
			}
			if sub == "init" {
				for _, d := range po.pos {
					fileAt(d, "git init")
				}
			}
			if sub == "worktree" && len(po.pos) > 1 {
				for _, d := range po.pos[1:] {
					fileAt(d, "git worktree")
				}
			}
			if sub == "config" {
				for _, s := range sws {
					if s == "--global" || s == "--system" {
						v.MustAsk = append(v.MustAsk, fmt.Sprintf("git config %s：改的是这台电脑上全局的 git 配置", s))
						break
					}
				}
				for _, f := range append(append([]string(nil), po.opt["-f"]...), po.opt["--file"]...) {
					fileAt(f, "git config --file")
				}
			}
			for _, x := range vs {
				if !isURL(x) && isElsewhere(x) {
					fileAt(x, "git "+sub)
				}
			}
			if cwdNull || cwd != "" {
				fileAt(".", "git "+sub) // 换过目录：git 在那个目录里干活
			}
			continue
		}
		hadName := contains(v.Names, name)
		named(name)

		// ——— 程序（不是 cmdlet） ———
		if !isCmdlet {
			if readOnly[name] && !byPath {
				continue
			}
			if evalOpaque[name] {
				hide(raw+" 会运行别的程序或脚本，里面做什么看不出来", "dynamic")
				continue
			}
			if systemProgs[name] {
				v.Changes = append(v.Changes, jsstr.Trim(raw+" "+strings.Join(vs, " ")))
				v.MustAsk = append(v.MustAsk, raw+" 改的是系统状态（服务、注册表、计划任务、环境变量、进程、装软件……）")
				continue
			}
			fam := name
			if pythons[name] {
				fam = "python"
			}
			module, rest := "", toks
			if ip, ok := interps[fam]; ok {
				stdin := raw + " 的代码从标准输入来（前面接了管道、或者写了单独的 -），里面做什么看不出来"
				script := false
				for i := 0; i < len(toks); i++ {
					t := toks[i]
					if t.kind == tokSw {
						if t.sw == "-" {
							hide(stdin, "dynamic")
							script = true
							break
						}
						if ip.inline(t.sw) {
							hide(fmt.Sprintf("%s %s 会把参数里的一段代码直接跑，里面做什么看不出来", raw, t.sw), "dynamic")
						}
						if fam == "python" && t.sw == "-m" {
							if i+1 < len(toks) && toks[i+1].kind == tokVal {
								module = strings.ToLower(toks[i+1].val)
							}
							if i+2 <= len(toks) {
								rest = toks[i+2:]
							} else {
								rest = nil
							}
							script = true
							break
						}
						if ip.takesValue[t.sw] && i+1 < len(toks) && toks[i+1].kind == tokVal {
							i++
						}
						continue
					}
					if t.kind == tokVal && t.of != "" {
						continue
					}
					if t.kind == tokVal && t.val == "-" {
						hide(stdin, "dynamic")
					}
					script = true
					break
				}
				if !script && c.Piped != nil && *c.Piped {
					hide(stdin, "dynamic")
				}
			}
			pm := positionals(toks, setOf(append(append([]string(nil), pmDirSwitch...), "--location")))
			firstPos := strings.ToLower(first(pm.pos))
			if fam == "deno" && (firstPos == "eval" || contains(sws, "-") || anyURL(pm.pos)) {
				if firstPos == "eval" || contains(sws, "-") {
					if firstPos == "eval" {
						hide(raw+" eval 会把参数里的一段代码直接跑", "dynamic")
					} else {
						hide(raw+" 的代码从标准输入来，里面做什么看不出来", "dynamic")
					}
				} else {
					hide(raw+" 会下载网上的脚本来跑，里面做什么看不出来", "download")
				}
				continue
			}
			// ——— 下载一个包来跑的 ———
			sub := ""
			if pmDownload[name][firstPos] {
				sub = firstPos
			}
			npmCreate := name == "npm" && (firstPos == "create" || ((firstPos == "init" || firstPos == "innit") && len(pm.pos) > 1 && pm.pos[1] != ""))
			if downloadRun[name] || sub != "" || npmCreate {
				at0 := 0
				if sub != "" || npmCreate {
					at0 = 1
				}
				opt := map[string][]string{}
				var pos, rsw []string
				for i := 0; i < len(toks) && len(pos) <= at0; i++ {
					t := toks[i]
					if t.kind == tokSw {
						rsw = append(rsw, t.sw)
						if runnerValue[t.sw] && i+1 < len(toks) {
							if n := toks[i+1]; n.kind == tokVal && (n.of == "" || n.of == t.sw) {
								opt[t.sw] = append(opt[t.sw], n.val)
								i++
							}
						}
						continue
					}
					if t.kind == tokVal {
						if t.of != "" {
							opt[t.of] = append(opt[t.of], t.val)
						} else {
							pos = append(pos, t.val)
						}
					}
				}
				if s := firstWhere(rsw, func(s string) bool { return runnerShell[s] }); s != "" {
					hide(fmt.Sprintf("%s %s 会把一段文字当命令跑，里面做什么看不出来", raw, s), "dynamic")
					continue
				}
				pkgs := append(append([]string(nil), opt["-p"]...), opt["--package"]...)
				var specs []string
				switch {
				case npmCreate:
					specs = []string{nth(pos, at0)}
				case len(pkgs) > 0:
					specs = pkgs
				case nth(pos, at0) != "":
					specs = []string{pos[at0]}
				}
				var dirSw []string
				for _, k := range pmDirSwitch {
					dirSw = append(dirSw, opt[k]...)
				}
				d0 := "."
				if len(dirSw) > 0 {
					d0 = dirSw[0]
				}
				dir, ok := at(d0)
				if !ok {
					hide(raw+"：前面换过目录、看不清在哪个目录跑", "cwd")
					continue
				}
				alwaysFetch := npmCreate || name == "uvx" || name == "pnpx" || sub == "dlx"
				if dir == "" {
					dir = "."
				}
				for _, s := range specs {
					v.Fetches = append(v.Fetches, Fetch{Name: specName(s), Dir: dir, Declarable: !alwaysFetch && plainPkg.MatchString(jsstr.LowerASCII(s))})
				}
				if !hadName {
					v.Names = removeFirst(v.Names, name)
				}
				label := name
				if sub != "" {
					label += " " + sub
				} else if npmCreate {
					label += " " + firstPos
				}
				named(strings.ToLower(jsstr.Trim(label + " " + strings.Join(specs, " "))))
				if len(hidden) > 0 {
					hide(fmt.Sprintf("%s 的参数用了变量或表达式：%s", raw, strings.Join(hidden, " ")), "loose")
					continue
				}
				for _, d := range dirSw {
					fileAt(d, raw+" 的目录开关")
				}
				for _, x := range vs {
					if !isURL(x) && isElsewhere(x) {
						fileAt(x, raw)
					}
				}
				what := strings.Join(specs, "、")
				if what == "" {
					what = "一个包"
				}
				v.Unclear = append(v.Unclear, fmt.Sprintf("%s 会运行 %s 里的程序，里面做什么看不出来", raw, what))
				if (cwdNull || cwd != "") && len(dirSw) == 0 {
					fileAt(".", raw)
				}
				continue
			}
			if len(hidden) > 0 {
				hide(fmt.Sprintf("%s 的参数用了变量或表达式：%s", raw, strings.Join(hidden, " ")), "loose")
				continue
			}
			// 认得的包管理器和工具链：子命令要在白名单里；python -m pip 照 pip 查
			tcName := name
			if (fam == "python" && module == "pip") || name == "pip3" {
				tcName = "pip"
			}
			if tc, ok := toolchains[tcName]; ok {
				src := toks
				if tcName == "pip" && fam == "python" {
					src = rest
				}
				tp := positionals(src, setOf(tc.value))
				k := -1
				for j, p := range tp.pos {
					if !strings.HasPrefix(p, "+") {
						k = j
						break
					}
				}
				if k >= 0 {
					sub := strings.ToLower(tp.pos[k])
					rule, known := tc.subs[sub]
					file := reFileArg.MatchString(jsstr.LowerASCII(sub))
					away := fmt.Sprintf("%s %s：不是只作用于当前项目的子命令（改用户级配置、装到用户目录、登录、发布、清全局缓存这类）", raw, sub)
					why := ""
					switch {
					case !known:
						if !(tc.script || (tc.file && file)) {
							why = away
						}
					case rule.kind == 0:
						why = away
					case rule.kind == 2:
						why = rule.fn(sws, tp.pos[k+1:])
					}
					if why != "" {
						if strings.HasPrefix(why, raw) {
							v.MustAsk = append(v.MustAsk, why)
						} else {
							v.MustAsk = append(v.MustAsk, fmt.Sprintf("%s %s：%s", raw, sub, why))
						}
					}
				}
			}
			// pip 装东西：直接写的 pip 看不出装进哪个 Python，install、uninstall 要问；工作目录里虚拟环境的 pip 放行（由 policy 按 runs 查）
			if name == "pip" || name == "pip3" || (fam == "python" && module == "pip") {
				pp := positionals(rest, setOf(pipValue))
				sub := strings.ToLower(first(pp.pos))
				who := raw
				if fam == "python" {
					who = raw + " -m pip"
				}
				if sub == "install" || sub == "uninstall" {
					if !byPath {
						v.MustAsk = append(v.MustAsk, fmt.Sprintf("%s %s：看不出装进哪个 Python（多半是全局的），用工作目录里虚拟环境的 pip 就不用问", who, sub))
					}
					if contains(sws, "--user") {
						v.MustAsk = append(v.MustAsk, fmt.Sprintf("%s %s --user：装进用户目录", who, sub))
					}
				}
				for _, k := range pipPlace {
					for _, d := range pp.opt[k] {
						fileAt(d, who+" "+k)
					}
				}
			}
			if pkgManagers[name] {
				global := contains(sws, "-g") || contains(sws, "--global") || contains(pm.opt["--location"], "global") || (name == "yarn" && firstPos == "global")
				if global && !pmReadSub[firstPos] {
					v.MustAsk = append(v.MustAsk, raw+" 全局安装：装到工作目录外面")
				}
				for _, k := range pmDirSwitch {
					for _, d := range pm.opt[k] {
						fileAt(d, raw+" "+k)
					}
				}
			}
			if fileProgs[name] {
				var lits []string
				for _, x := range vs {
					if !strings.HasPrefix(x, "/") && !isURL(x) {
						lits = append(lits, x)
					}
				}
				if len(lits) > 0 {
					v.Changes = append(v.Changes, raw+" → "+strings.Join(lits, "、"))
				} else {
					v.Changes = append(v.Changes, raw)
					hide(raw+" 没写明要改哪里", "loose")
					continue
				}
				for _, x := range lits {
					fileAt(x, raw)
				}
			} else {
				for _, x := range vs {
					if !isURL(x) && isElsewhere(x) {
						fileAt(x, raw)
					}
				}
				if netProgs[name] {
					v.Changes = append(v.Changes, jsstr.Trim(raw+" "+strings.Join(vs, " ")))
				} else if _, tc := toolchains[name]; toolRun[name] || tc || byPath {
					v.Unclear = append(v.Unclear, raw+" 会运行别的程序或脚本，里面做什么看不出来")
				} else {
					v.Unclear = append(v.Unclear, "不认识的程序："+raw)
				}
			}
			if cwdNull || cwd != "" {
				fileAt(".", raw)
			}
			continue
		}

		// ——— cmdlet ———
		if readOnly[name] || readOnly[canon] || memoryOnly[canon] {
			continue
		}
		if jump, isJump := jumpOf(canon); isJump {
			v.Changes = append(v.Changes, "换目录："+raw)
			if jump == nil {
				cwd, cwdNull = "", true
			} else {
				nw, ok := at(*jump)
				cwd, cwdNull = nw, !ok
			}
			continue
		}
		if !strings.Contains(canon, "-") {
			v.Changes = append(v.Changes, raw)
			hide(raw+" 是 PowerShell 函数，看不出它做什么", "dynamic")
			continue
		}
		dash := strings.Index(canon, "-")
		verb, noun := canon[:dash], canon[dash+1:]
		if readVerbs[verb] {
			continue
		}
		if evalOpaque[canon] {
			if canon == "invoke-expression" {
				hide(raw+" 会把一段文字当命令跑，里面做什么看不出来", "dynamic")
			} else {
				hide(raw+" 会运行别的程序或脚本，里面做什么看不出来", "dynamic")
			}
			continue
		}
		if cdCmdlets[canon] {
			ch := "换目录：" + raw
			if len(vs) > 0 {
				ch += " " + strings.Join(vs, " ")
			}
			v.Changes = append(v.Changes, ch)
			if canon == "pop-location" {
				if n := len(stack); n > 0 {
					cwd, cwdNull = stack[n-1].v, stack[n-1].null
					stack = stack[:n-1]
				} else {
					cwd, cwdNull = "", true
				}
				continue
			}
			if canon == "push-location" {
				stack = append(stack, place{cwd, cwdNull})
			}
			switch {
			case len(hidden) > 0 || contains(sws, "-") || contains(sws, "+"):
				cwd, cwdNull = "", true
			case len(vs) == 0:
				cwd, cwdNull = "~", false
			default:
				nw, ok := at(vs[0])
				cwd, cwdNull = nw, !ok || isBareDrive(vs[0]) // cd D: 是切到 D 盘的当前目录，当前目录是什么看不出
			}
			continue
		}
		meta := c.Meta != nil && *c.Meta
		if netCmdlets[canon] {
			ch := raw
			if len(vs) > 0 {
				ch += " → " + strings.Join(vs, "、")
			}
			v.Changes = append(v.Changes, ch)
			var loose []string
			for _, t := range toks {
				if t.kind == tokHidden && !(meta && t.bind != "" && netFree[strings.ToLower(t.bind)]) {
					loose = append(loose, t.text)
				}
			}
			if len(loose) > 0 {
				hide(fmt.Sprintf("%s 的地址或落地文件用了变量或表达式：%s", raw, strings.Join(loose, " ")), "loose")
				continue
			}
			if len(vs) == 0 {
				hide(raw+" 没写明访问哪里", "loose")
				continue
			}
			for _, x := range vs {
				if !isURL(x) {
					fileAt(x, raw)
				}
			}
			continue
		}
		if fileNouns[noun] || fileCmdlets[canon] {
			ch := raw
			if len(vs) > 0 {
				ch += " → " + strings.Join(vs, "、")
			}
			v.Changes = append(v.Changes, ch)
			link := canon == "new-item" && isLinkType(toks)
			free := func(b string) bool {
				lb := strings.ToLower(b)
				return meta && b != "" && contentFree[lb] && !(link && lb == "value")
			}
			var loose []string
			for _, t := range toks {
				if t.kind == tokHidden && !free(t.bind) {
					loose = append(loose, t.text)
				}
			}
			if len(loose) > 0 {
				hide(fmt.Sprintf("%s 的参数用了变量或表达式：%s", raw, strings.Join(loose, " ")), "loose")
				continue
			}
			if !meta {
				hide(raw+" 的参数认不出（绑定出错，或者不是 PowerShell 自带模块里的命令），看不清改的是哪里", "unbound")
				continue
			}
			bound := map[string]bool{}
			for _, t := range toks {
				if t.kind == tokVal && t.bind != "" {
					bound[strings.ToLower(t.bind)] = true
				}
			}
			target, dest := false, false
			for p := range bound {
				target = target || targetParams[p]
				dest = dest || destParams[p]
			}
			target = target || (canon == "new-item" && bound["name"])
			if !target && !(readSource[canon] && dest) {
				var roots []string
				if c.Piped != nil && *c.Piped {
					roots = pipeRoots(a, c)
				}
				if roots == nil {
					if c.Piped != nil && *c.Piped {
						hide(raw+" 要改的东西从管道来，看不出出自哪里", "loose")
					} else {
						hide(raw+" 没写明要改哪里（没写路径）", "loose")
					}
					continue
				}
				hide(fmt.Sprintf("%s 要改的东西从管道来（出自 %s）", raw, strings.Join(roots, "、")), "piped")
				for _, r := range roots {
					fileAt(r, raw+" 的管道源头")
				}
			}
			for _, x := range vs {
				if !isURL(x) {
					fileAt(x, raw)
				}
			}
			continue
		}
		// 别的会改东西的 cmdlet：改的不是工作目录里的文件
		ch := raw
		if len(vs) > 0 {
			ch += " → " + strings.Join(vs, "、")
		}
		v.Changes = append(v.Changes, ch)
		v.MustAsk = append(v.MustAsk, raw+" 改的不是工作目录里的文件（服务、进程、模块、计划任务、系统设置之类）")
	}
	for i, m := range a.Methods {
		var member *Member
		if a.Members != nil && i < len(*a.Members) {
			member = &(*a.Members)[i]
		}
		name := ""
		if member != nil {
			name = strings.ToLower(reQuotes.ReplaceAllString(member.Name, ""))
		}
		risky := member == nil || (member.Static && !safeStatic(member.Type, name)) || (!member.Static && !safeInstance[name])
		if !risky {
			continue
		}
		short := store.JSSlice(m, 80)
		v.MethodRisk = append(v.MethodRisk, short)
		v.Changes = append(v.Changes, "调用 "+short)
		hide("直接调用 .NET 方法改东西（看不出改什么）："+short, "dynamic")
	}
	v.ReadOnly = len(v.Changes) == 0 && len(v.Unclear) == 0
	return v
}

// jumpOf 是 pwsh 自带的换目录函数：cd.. 回上一级、cd\ 回根、cd~ 回用户目录；C:、D: 这类切到那个盘上次的位置（看不清在哪，返回 nil）
func jumpOf(canon string) (*string, bool) {
	if reDriveOnly.MatchString(canon) {
		return nil, true
	}
	to, ok := map[string]string{"cd..": "..", `cd\`: `\`, "cd~": "~"}[canon]
	if !ok {
		return nil, false
	}
	return &to, true
}

// nth 像 JS 的 xs[i] ?? ”
func nth(xs []string, i int) string {
	if i < len(xs) {
		return xs[i]
	}
	return ""
}

func specName(s string) string { return strings.ToLower(pkgVersion.ReplaceAllString(s, "$1")) }

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func removeFirst(xs []string, x string) []string {
	for i, y := range xs {
		if y == x {
			return append(append([]string(nil), xs[:i]...), xs[i+1:]...)
		}
	}
	return xs
}

func firstWhere(xs []string, pred func(string) bool) string {
	for _, x := range xs {
		if pred(x) {
			return x
		}
	}
	return ""
}

func anyURL(xs []string) bool {
	for _, x := range xs {
		if isURL(x) {
			return true
		}
	}
	return false
}
