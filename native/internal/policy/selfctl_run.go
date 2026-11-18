package policy

import (
	"regexp"
	"strings"
)

// 看得清的命令怎么判“是不是在运行 sumika-ctl”（分析器给出每条简单命令的程序名和参数，见 selfctl.go 的总说明）。

// ctlGlobalOpts 是 sumika-ctl 的全局选项，放在子命令前后都行
var ctlGlobalOpts = keySet([]string{"--json", "--demo"})

// ctlKnownOpts 是只读子命令各自认得的选项（从 cmd/sumika-ctl 的参数解析和用法文字照着列）。
// 只读子命令后面出现不在这里、也不是全局选项的 - 开头的词，就不当只读，按“要问”处理：新选项没登记时往严的方向错。
// selfctl_test.go 的守卫读用法文字每个只读子命令那一行里出现的选项，缺了这张表里的登记就红（只查命令那一行，续行说明不查）。
var ctlKnownOpts = map[string][]string{
	"logs":          {"-n", "--level", "--module", "--errors"},
	"backfill gaps": {"-n", "--cursor"},
	"link preview":  {"--template", "--site", "--sample"},
	"stats usage":   {"--days", "--year", "--all", "--by", "--full"},
	"stats quality": {"--days", "--all", "--task", "--failures"},
	"stats spans":   {"--days", "--all", "--turn"},
	"stats rebuild": {"--days", "--all"},
	"proactive log": {"-n"},
	"artist list":   {"--status", "--platform", "-n"},
	"goal show":     {"--character"},
	"loop list":     {"--character"},
}

var numericTok = regexp.MustCompile(`^-?\d+(\.\d+)?[smhd]?$`)

// ctlReadOnlyInvocation 认 sumika-ctl 后面的参数是不是一个明确的只读子命令，并且后面带的选项都认得。
func ctlReadOnlyInvocation(args []string) bool {
	for len(args) > 0 && ctlGlobalOpts[strings.ToLower(args[0])] {
		args = args[1:]
	}
	if len(args) == 0 {
		return false // 光一个 sumika-ctl 是打印用法，也不是我们认得的；问一次也无妨
	}
	cmd := strings.ToLower(args[0])
	key, rest := cmd, args[1:]
	if !ctlNoSub[cmd] {
		sub := ""
		if len(args) > 1 {
			sub = strings.ToLower(args[1])
			rest = args[2:]
		}
		key = CtlKey(cmd, sub)
	}
	if !ctlReadOnly[key] {
		return false
	}
	known := keySet(ctlKnownOpts[key])
	for _, tok := range rest {
		if !strings.HasPrefix(tok, "-") || len(tok) < 2 || numericTok.MatchString(tok) {
			continue
		}
		name, _, _ := strings.Cut(strings.ToLower(tok), "=")
		if !ctlGlobalOpts[name] && !known[name] {
			return false // 不认识的选项：不当只读
		}
	}
	return true
}

// launchers 是把命令行当参数去起进程、或存成别名的命令：它们的参数里有 sumika-ctl 就等于要运行它
var launchers = keySet([]string{"schtasks", "set-alias", "new-alias", "sal", "nal", "invoke-cimmethod", "invoke-wmimethod", "wmic",
	"new-scheduledtaskaction", "register-scheduledtask", "start-job", "start-threadjob"})

// argRunners 是“会把后面的参数当命令去执行”的包装程序：env、xargs、timeout 这类。判据：它自己不干活，后面第一个不是选项、
// 不是 VAR=值、不是时长的词就是它要执行的程序（forfiles 是 /c 后面那个带引号的整条命令）。
// 看得清的命令里，只有名单里的程序才去看它后面接的是不是 sumika-ctl；名单外的程序参数里只是提到不算（rg sumika-ctl docs 不能被拦）。
// 名单是往严的方向补的，不求穷举；没列到的包装程序在残留里（规格-本机控制接口）。
var argRunners = keySet([]string{"env", "xargs", "nohup", "time", "timeout", "conhost", "wt", "winpty", "forfiles", "runas",
	"command", "sudo", "gsudo", "doas", "start", "nice", "ionice", "stdbuf", "watch", "setsid", "exec", "call", "builtin", "busybox", "unbuffer"})

// argRunnerValueOpts 是这些包装程序里带一个值的选项：跳过选项时连值一起跳过，不然值会被当成要执行的程序
var argRunnerValueOpts = map[string][]string{
	"env":     {"-u", "-c", "-s", "--unset", "--chdir", "--split-string"},
	"xargs":   {"-n", "-i", "-l", "-p", "-d", "-e", "-s", "-a"},
	"timeout": {"-s", "-k", "--signal", "--kill-after"},
	"nice":    {"-n"},
	"ionice":  {"-c", "-n", "-p"},
	"sudo":    {"-u", "-g", "-c", "-d", "-h", "-p", "-r", "-t"},
	"doas":    {"-u", "-c"},
	"gsudo":   {"-u", "-d"},
	"watch":   {"-n", "-d"},
	"runas":   {"/user", "/profile"},
}

// cdNames 是改当前目录的命令：进了 sumika-ctl 的目录再 go run . 等于 go run 它
var cdNames = keySet([]string{"cd", "chdir", "set-location", "sl", "pushd", "push-location"})

// goRunValueOpts 是 go run 里带一个值的选项（-C 的值还是它要进的目录）
var goRunValueOpts = keySet([]string{"-c", "-tags", "-ldflags", "-gcflags", "-asmflags", "-mod", "-modfile", "-overlay", "-pkgdir", "-p",
	"-exec", "-toolexec", "-buildvcs", "-compiler", "-gccgoflags", "-installsuffix", "-covermode", "-coverpkg"})

func normPath(s string) string { return strings.ToLower(strings.ReplaceAll(s, `\`, "/")) }

// goRunTarget 看一条 go 命令：是 go run 且运行的是 sumika-ctl 的包吗？是就把包后面的参数（给程序的）也交回来。
// 认的方式：包路径含 cmd/sumika-ctl；-C 目录含 sumika-ctl；或同一条命令里先 cd 进了 sumika-ctl 的目录（cdCtl）。
func goRunTarget(args []string, cdCtl bool) (isCtl bool, tail []string) {
	if len(args) == 0 || !strings.EqualFold(args[0], "run") {
		return false, nil
	}
	cwdCtl := cdCtl
	for i := 1; i < len(args); i++ {
		tok := args[i]
		if strings.HasPrefix(tok, "-") {
			name, val, hasVal := strings.Cut(strings.ToLower(tok), "=")
			if goRunValueOpts[name] {
				if !hasVal && i+1 < len(args) {
					i++
					val = strings.ToLower(args[i])
				}
				if name == "-c" && strings.Contains(normPath(val), "sumika-ctl") {
					cwdCtl = true
				}
			}
			continue
		}
		pkg := normPath(tok)
		return cwdCtl || strings.Contains(pkg, "cmd/sumika-ctl"), args[i+1:]
	}
	return cwdCtl, nil
}

// programBase 取一条命令的程序名：去引号、路径、末尾的点和空格、.exe .cmd .bat .ps1 .com（可叠），小写
func programBase(name string) string {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), `'"`+"`"))
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	for {
		name = strings.TrimRight(name, ". ")
		cut := false
		for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".com"} {
			if strings.HasSuffix(name, ext) {
				name, cut = strings.TrimSuffix(name, ext), true
			}
		}
		if !cut {
			return name
		}
	}
}

// isCtlProgram：被执行的程序名（去扩展名后）是 sumika-ctl，或以 sumika-ctl- 开头（构建后试跑新版、留着的旧版，是同一个会改状态的程序）。
// 只用在“被执行的程序名”上；文件操作的参数里出现这些名字（Copy-Item、Move-Item、go build -o）不算。
func isCtlProgram(name string) bool {
	b := programBase(name)
	return b == "sumika-ctl" || strings.HasPrefix(b, "sumika-ctl-")
}

// wrappedCommand 把包装程序后面要执行的那条命令的各个词交回来（第一个是程序）；认不出是空
func wrappedCommand(prog string, args []string) []string {
	if prog == "forfiles" {
		for i, a := range args {
			if (strings.EqualFold(a, "/c") || strings.EqualFold(a, "-c")) && i+1 < len(args) {
				return strings.Fields(args[i+1])
			}
		}
		return nil
	}
	var toks []string
	for _, a := range args {
		toks = append(toks, strings.Fields(a)...)
	}
	valueOpts := keySet(argRunnerValueOpts[prog])
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		lower := strings.ToLower(t)
		switch {
		case strings.HasPrefix(t, "-") || (strings.HasPrefix(t, "/") && len(t) <= 8 && !strings.ContainsAny(t[1:], `/\.`)):
			name, _, hasVal := strings.Cut(lower, "=")
			name, _, _ = strings.Cut(name, ":")
			if valueOpts[name] && !hasVal && !strings.Contains(lower, ":") {
				i++
			}
		case strings.Contains(t, "=") && !strings.ContainsAny(strings.SplitN(t, "=", 2)[0], `/\`):
			// VAR=值
		case numericTok.MatchString(lower):
			// timeout 的时长、nice 的数
		default:
			return toks[i:]
		}
	}
	return nil
}

// judgeCommand 判一条简单命令（程序名 + 参数）要不要问；cdCtl 是同一条命令里有没有 cd 进 sumika-ctl 的目录。depth 防包装套包装套太深
func judgeCommand(prog string, args []string, cdCtl bool, depth int) bool {
	switch {
	case isCtlProgram(prog):
		return !ctlReadOnlyInvocation(args)
	case prog == "go":
		if isCtl, tail := goRunTarget(args, cdCtl); isCtl {
			return !ctlReadOnlyInvocation(tail)
		}
	case launchers[prog]:
		for _, a := range args {
			if strings.Contains(strings.ToLower(a), "sumika-ctl") {
				return true
			}
		}
	case argRunners[prog] && depth < 4:
		if w := wrappedCommand(prog, args); len(w) > 0 {
			return judgeCommand(programBase(w[0]), w[1:], cdCtl, depth+1)
		}
	}
	return false
}

// SelfCtlNeedsAsk 是这条命令要不要因为 sumika-ctl 被拦成“只能批一次”。v 是分析器拆出的结论，nil 或看不清时退回文字规则。
func SelfCtlNeedsAsk(command string, v *Verdict) bool {
	if v == nil || v.Opaque {
		return TouchesSelfCtlWrite(command)
	}
	cdCtl := false
	for _, c := range v.Cmds {
		if cdNames[programBase(c.Name)] {
			for _, a := range c.Args {
				if strings.Contains(normPath(a), "sumika-ctl") {
					cdCtl = true
				}
			}
		}
	}
	for _, c := range v.Cmds {
		if judgeCommand(programBase(c.Name), c.Args, cdCtl, 0) {
			return true
		}
	}
	return false
}
