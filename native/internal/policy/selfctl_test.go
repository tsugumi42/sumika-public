package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ctlprotoConsts 读 internal/ctlproto 里所有字符串常量（名字 → 值）
func ctlprotoConsts(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../ctlproto", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								v, _ := strconv.Unquote(lit.Value)
								out[n.Name] = v
							}
						}
					}
				}
			}
		}
	}
	return out
}

// topLevelCommands 是服务端命令分发表（ctlserver 的 route 里 switch req.Cmd 的各个 case）里的全部命令
func topLevelCommands(t *testing.T, consts map[string]string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../ctlserver/server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "route" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if sel, ok := e.(*ast.SelectorExpr); ok {
					if v, ok := consts[sel.Sel.Name]; ok && strings.HasPrefix(sel.Sel.Name, "Cmd") {
						set[v] = true
					}
				}
			}
			return true
		})
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 守卫：sumika-ctl 的每个命令和子命令都必须被登记进“只读”或“要问”之一（且只在一边）。命令从服务端的命令分发表取，
// 子命令从协议里的子命令常量和用法文字取（用法文字对缩进不敏感，只认行首是已知命令的行）。
// 新加命令、子命令没分类这条就红：名单不会悄悄过期（运行时没登记的一律问，所以过期的方向是多问，但要有人看过）。
func TestSelfCtlEverySubcommandClassified(t *testing.T) {
	consts := ctlprotoConsts(t)
	cmds := topLevelCommands(t, consts)
	if len(cmds) < 20 {
		t.Fatalf("命令分发表里只认出 %d 个命令，解析多半坏了：%v", len(cmds), cmds)
	}
	known := map[string]bool{}
	for _, c := range cmds {
		known[c] = true
	}
	found := map[string]bool{}
	for _, c := range cmds { // 命令本身：要么自己在表里，要么至少有一个“命令 子命令”在表里
		found["cmd:"+c] = true
	}
	// 协议里的子命令常量：XxxYyyCmd = "yyy"，Xxx 是命令（取最长的已知命令前缀）
	for name, val := range consts {
		if !strings.HasSuffix(name, "Cmd") || strings.HasPrefix(name, "Cmd") {
			continue
		}
		low := strings.ToLower(strings.TrimSuffix(name, "Cmd"))
		best := ""
		for c := range known {
			if strings.HasPrefix(low, strings.ReplaceAll(c, "-", "")) && len(c) > len(best) {
				best = c
			}
		}
		if best != "" && regexp.MustCompile(`^[a-z-]+$`).MatchString(val) {
			found[CtlKey(best, val)] = true
		}
	}
	b, err := os.ReadFile("../../cmd/sumika-ctl/run.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(src, "const usage = `")
	if i < 0 {
		t.Fatal("找不到 sumika-ctl 的用法文字")
	}
	usage := src[i+len("const usage = `"):]
	usage = usage[:strings.Index(usage, "`")]
	lineRe := regexp.MustCompile(`^ {2,8}([a-z][a-z-]*)(?: |$)`)
	for _, line := range strings.Split(usage, "\n") {
		m := lineRe.FindStringSubmatch(line)
		if m == nil || !known[m[1]] {
			continue
		}
		for _, k := range ctlKeysOfUsageLine(strings.TrimLeft(line, " ")) {
			found[k] = true
		}
	}
	if len(found) < 50 {
		t.Fatalf("只认出 %d 个命令和子命令，解析多半坏了", len(found))
	}
	var missing, both []string
	for k := range found {
		if strings.HasPrefix(k, "cmd:") {
			c := strings.TrimPrefix(k, "cmd:")
			covered := ctlReadOnly[c] || ctlAsk[c]
			for key := range ctlReadOnly {
				covered = covered || strings.HasPrefix(key, c+" ")
			}
			for key := range ctlAsk {
				covered = covered || strings.HasPrefix(key, c+" ")
			}
			if !covered {
				missing = append(missing, c+"（整个命令）")
			}
			continue
		}
		inRead, inAsk := ctlReadOnly[k], ctlAsk[k]
		switch {
		case !inRead && !inAsk:
			missing = append(missing, k)
		case inRead && inAsk:
			both = append(both, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("sumika-ctl 的这些命令或子命令没分类：%v——只读的放 selfctl.go 的 ctlReadOnlyList，会改状态或拿不准的放 ctlAskList", missing)
	}
	if len(both) > 0 {
		t.Errorf("同时登记在只读和要问里：%v", both)
	}
	for _, k := range []string{"quit", "mcp release", "mcp reconnect", "gate set", "receive start", "receive stop", "backup now", "backup restore", "backup verify", "checkpoint restore", "chat", "goal", "loop"} {
		if ctlReadOnly[k] {
			t.Errorf("%s 会改状态，不能进只读名单", k)
		}
	}
	// 登记的键都得是真有的：名单里写了、命令分发表里没有的命令，多半是改名后忘了改
	for k := range ctlReadOnly {
		if c, _, _ := strings.Cut(k, " "); !known[c] && c != "kernel" {
			t.Errorf("只读名单里的 %q 不在命令分发表里", k)
		}
	}
}

// ctlKeysOfUsageLine 把用法文字里的一行（命令从行首开始）拆成命令和子命令的键。认两种写法：
//
//	gate list ...、backup now [..]、auto-receive on|off、window show   → 命令后第一个词（| 分开的都算）
//	goal [show|set <目标>|edit ...|clear]                                → 方括号里按 | 分开（括号可嵌套），每段的第一个词
//
// 命令后面不是子命令词（比如 logs [-n 50]、status、chat [--character]）的，键就是命令本身。
func ctlKeysOfUsageLine(line string) []string {
	fields := strings.Fields(line)
	cmd := fields[0]
	if len(fields) == 1 {
		return []string{cmd}
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	word := regexp.MustCompile(`^[a-z][a-z-]*`)
	var subs []string
	if strings.HasPrefix(rest, "[") {
		depth, start := 0, 1
		var parts []string
		for i, r := range rest {
			switch r {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					parts = append(parts, rest[start:i])
				}
			case '|':
				if depth == 1 {
					parts = append(parts, rest[start:i])
					start = i + 1
				}
			}
			if depth == 0 {
				break
			}
		}
		for _, p := range parts {
			if w := word.FindString(strings.TrimSpace(p)); w != "" {
				subs = append(subs, w)
			}
		}
	} else {
		first := strings.Fields(rest)[0]
		for _, p := range strings.Split(first, "|") {
			if w := word.FindString(p); w != "" && w == strings.TrimRight(p, "]") {
				subs = append(subs, w)
			}
		}
	}
	if len(subs) == 0 || ctlNoSub[cmd] {
		return []string{cmd}
	}
	keys := make([]string, 0, len(subs))
	for _, s := range subs {
		keys = append(keys, CtlKey(cmd, s))
	}
	return keys
}

func TestSelfCtlUsageParserSanity(t *testing.T) {
	cases := map[string][]string{
		"status                      看现在的状态":                                   {"status"},
		"logs [-n 50]               看日志":                                       {"logs"},
		"gate set <qq:group:群号|句柄> off|dry":                                    {"gate set"},
		"auto-receive on|off         启动后自动接收":                                  {"auto-receive on", "auto-receive off"},
		"goal [show|set <目标>|edit <目标>|pause|resume|clear] [--character <id>]": {"goal show", "goal set", "goal edit", "goal pause", "goal resume", "goal clear"},
		"loop [list|add [间隔] <任务>|edit <编号> [间隔]|pause <编号>|remove <编号>]":      {"loop list", "loop add", "loop edit", "loop pause", "loop remove"},
		"chat [--character <id>] [--new]":                                      {"chat"},
		"trust [--character <id>]    项目说明":                                     {"trust"},
		`mcp release <服务> --reason "<原因>"`:                                     {"mcp release"},
	}
	for line, want := range cases {
		got := ctlKeysOfUsageLine(line)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q：得到 %v，应为 %v", line, got, want)
		}
	}
}

// 文字规则本身（分析器看不清时的退路）：没有分析器也该判对；续行折成空格再判
func TestTouchesSelfCtlWriteText(t *testing.T) {
	for c, want := range map[string]bool{
		"sumika-ctl quit":                            true,
		"sumika-ctl status":                          false,
		`D:\a\sumika-ctl.exe --json mcp status`:      false,
		`D:\a\sumika-ctl.exe mcp release x`:          true,
		`Get-Content cmd\sumika-ctl\run.go`:          false,
		"sumika-ctlx quit":                           false,
		"sumika-ctl.cmd status":                      true,
		"sumika-ctl":                                 true,
		"sumika-ctl status; sumika-ctl quit":         true,
		"sumika-ctl status | sumika-ctl logs":        false,
		"SUMIKA-CTL.EXE QUIT":                        true,
		"echo hi":                                    false,
		"sumika-ctl 'mcp' 'release' x":               true,
		"sumika-ctl mcp status $(sumika-ctl quit)":   true,
		"sumika-ctl goal":                            true,
		"sumika-ctl goal show":                       false,
		"sumika-ctl goal set 做事":                     true,
		"sumika-ctl loop":                            true,
		"sumika-ctl loop list":                       false,
		"sumika-ctl loop add 1h x":                   true,
		"sumika-ctl goal `\nclear":                   true,
		"sumika-ctl goal \\\nclear":                  true,
		"sumika-ctl mcp `\r\n status":                false,
		"sumika-ctl -- quit":                         true,
		"sumika-ctl --demo --json status":            false,
		"sumika-ctl status extra --json":             false,
		"sumika-ctl auto-receive on":                 true,
		"sumika-ctl whisper list":                    true,
		"sumika-ctl whisper status b1":               false,
		"sumika-ctl artist list":                     false,
		"sumika-ctl artist remove 1":                 true,
		"sumika-ctl stats usage --days 3":            false,
		"sumika-ctl stats":                           true,
		"sumika-ctl link preview http://example.com": false,
		"sumika-ctl link":                            true,
		"sumika-ctl proactive log":                   false,
		"sumika-ctl proactive":                       true,
		"sumika-ctl backfill gaps":                   false,
		"sumika-ctl backfill":                        true,
		"sumika-ctl checkpoint":                      true,
		"sumika-ctl checkpoint list a":               false,
		"sumika-ctl checkpoint restore a b c":        true,
		"sumika-ctl kernel faults":                   false,
		"sumika-ctl kernel retry":                    true,
		"sumika-ctl secrets status":                  false,
		"sumika-ctl secrets list":                    false,
		"sumika-ctl secrets set qq.100.token":        true,
		"sumika-ctl secrets set-passphrase":          true,
		"sumika-ctl secrets unlock --recovery":       true,
		"sumika-ctl secrets delete qq.100.token":     true,
		"sumika-ctl secrets":                         true,
		"go run ./cmd/sumika-ctl/ quit":              true,
		`go run .\cmd\sumika-ctl quit`:               true,
		"go test ./cmd/sumika-ctl":                   false,
	} {
		if got := TouchesSelfCtlWrite(c); got != want {
			t.Errorf("%q：得到 %v，应为 %v", c, got, want)
		}
	}
}

// 结构化规则（分析器看得清）：只看被执行的程序，别的程序参数里提到这个词不算
func TestSelfCtlNeedsAskUsesParsedPrograms(t *testing.T) {
	cl := func(name string, args ...string) CmdLine { return CmdLine{Name: name, Args: args} }
	clear := func(cmds ...CmdLine) *Verdict { return &Verdict{Cmds: cmds} }
	cases := []struct {
		name string
		v    *Verdict
		want bool
	}{
		{"rg 提到", clear(cl("rg", "sumika-ctl", "docs")), false},
		{"git commit 提到", clear(cl("git", "commit", "-m", "docs: sumika-ctl quit")), false},
		{"go test", clear(cl("go", "test", "./cmd/sumika-ctl")), false},
		{"go build", clear(cl("go", "build", "-o", "sumika-ctl.exe", "./cmd/sumika-ctl")), false},
		{"go run 那个包", clear(cl("go", "run", "./cmd/sumika-ctl/", "quit")), true},
		{"go run 别的包", clear(cl("go", "run", "./cmd/logotool")), false},
		{"运行 sumika-ctl quit", clear(cl(`D:\x\SUMIKA-CTL.EXE`, "quit")), true},
		{"运行 sumika-ctl status", clear(cl("sumika-ctl", "status")), false},
		{"串联里有一条", clear(cl("rg", "x"), cl("sumika-ctl", "quit")), true},
		{"schtasks 参数里有", clear(cl("schtasks", "/tr", "sumika-ctl quit")), true},
		{"Set-Alias 参数里有", clear(cl("Set-Alias", "c", "sumika-ctl")), true},
		{"Set-Content 写文档", clear(cl("Set-Content", "x.md", "sumika-ctl quit")), false},
		{"看不清退回文字规则", &Verdict{Opaque: true, Cmds: []CmdLine{cl("cmd", "/c", "ls")}}, false},
		{"没有分析结论退回文字规则", nil, true},
	}
	for _, c := range cases {
		cmd := "x"
		if c.v == nil || c.v.Opaque {
			cmd = "sumika-ctl quit"
			if c.name == "看不清退回文字规则" {
				cmd = "cmd /c ls"
			}
		}
		if got := SelfCtlNeedsAsk(cmd, c.v); got != c.want {
			t.Errorf("%s：得到 %v，应为 %v", c.name, got, c.want)
		}
	}
}

// sumika-ctl backup restore 和 backup verify 在任何档位都要问（规格-数据目录与备份 10.10.5）：模型顺手跑它们，
// 会往任意位置写一整份数据目录的副本，或占着备份那把锁写几个 GB 的临时文件；backup list 仍然只读
func TestSelfCtlAsksForBackupRestoreAndVerify(t *testing.T) {
	asks := []string{
		`sumika-ctl backup restore 手动-20261115-0530 --to D:\恢复出的数据目录`,
		`sumika-ctl backup verify`,
		`sumika-ctl backup verify 每天-20261115-0530 --wait=5s`,
		`& 'D:\x\sumika-ctl.exe' backup restore x --to E:\y`,
		`cd D:\x && sumika-ctl backup verify`,
	}
	for _, c := range asks {
		if !TouchesSelfCtlWrite(c) {
			t.Errorf("该要问：%s", c)
		}
	}
	for _, c := range []string{`sumika-ctl backup list`, `sumika-ctl backup list --json`} {
		if TouchesSelfCtlWrite(c) {
			t.Errorf("只读的不该问：%s", c)
		}
	}
	for _, k := range []string{"backup restore", "backup verify"} {
		if !ctlAsk[k] || ctlReadOnly[k] {
			t.Errorf("%s 该在“要问”名单、不在只读名单", k)
		}
	}
}
