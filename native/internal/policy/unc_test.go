package policy

// 网络共享路径和设备路径一律当碰到受保护目录（2026-11-10 实测：指向数据目录的管理共享写法 read 会被放行）。
// 这里钉住：每种写法 × 每个入口 × 三种模式 × 有无授权，都是"每次问、不给记住"；放行规则、授权、YOLO 都越不过。

import (
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

const (
	uncProtected = `C:\Users\x\sdata`
	uncWorkdir   = `D:\work\proj`
)

// 指向受保护目录里同一个文件的各种写法
var uncShapes = []string{
	`//localhost/C$/Users/x/sdata/c.yml`,
	`\\localhost\C$\Users\x\sdata\c.yml`,
	`\\127.0.0.1\c$\Users\x\sdata\c.yml`,
	`\\?\C:\Users\x\sdata\c.yml`,
	`\\.\C:\Users\x\sdata\c.yml`,
	`\\?\UNC\localhost\C$\Users\x\sdata\c.yml`,
	// 大小写、正反斜杠混用
	`\/LocalHost\c$/USERS/x\SDATA/C.YML`,
	`/\localhost/C$/Users/x/sdata/c.yml`,
	`//?/C:/Users/x/sdata/c.yml`,
	`//./C:/Users/x/sdata/c.yml`,
	`//?/UNC/localhost/C$/Users/x/sdata/c.yml`,
	// 末尾带点、带空格
	`\\localhost\C$\Users\x\sdata\c.yml.`,
	`\\localhost\C$\Users\x\sdata\c.yml `,
	// 工具会剥掉的前缀、前导空白
	`@\\localhost\C$\Users\x\sdata\c.yml`,
	"\u00a0\\\\localhost\\C$\\Users\\x\\sdata\\c.yml",
	// 别的主机的共享、file: 写法、NT 对象路径
	`\\fileserver\share\a.txt`,
	`file://fileserver/share/a.txt`,
	`\??\C:\Users\x\sdata\c.yml`,
}

func uncEnv(mode, granted string) Env {
	return Env{
		Mode: mode, Workdir: uncWorkdir, Declared: true,
		Audience:      Audience{Channel: "console", PrivateOk: true},
		ProtectedDirs: []string{uncProtected},
		Granted:       granted, GrantDirs: []string{`\\localhost\C$`, `C:\Users\x`, uncWorkdir},
	}
}

func wantAskOnce(t *testing.T, label string, d Decision) {
	t.Helper()
	if d.Kind != "ask" || !d.Sticky || d.Limit != "once" {
		t.Errorf("%s：该是每次都问（ask、Sticky、once），得到 %+v", label, d)
	}
}

// 基线：本地写法本来就问；UNC 写法要和它一样
func TestUNCPathToolsAlwaysAsk(t *testing.T) {
	windowsPathsOnly(t)
	shapes := append([]string{`C:\Users\x\sdata\c.yml`}, uncShapes...)
	for _, tool := range []string{"read", "ls", "grep", "find", "write", "edit"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				for _, p := range shapes {
					label := fmt.Sprintf("%s %s granted=%q %q", tool, mode, granted, p)
					d := Decide(tool, Specs[tool], ruleArgs("path", p), uncEnv(mode, granted))
					wantAskOnce(t, label, d)
				}
			}
		}
	}
}

// 递归搜索：搜的目录是受保护目录的上级，用 UNC 写法也要问
func TestUNCRecursiveAncestorAsk(t *testing.T) {
	windowsPathsOnly(t)
	for _, p := range []string{`\\localhost\C$\Users`, `//localhost/C$/Users/x`, `\\?\C:\Users`, `\\.\C:\`, `\\?\UNC\localhost\C$`, `\\?\C:\`} {
		for _, tool := range []string{"grep", "find"} {
			for _, mode := range []string{"manual", "auto", "yolo"} {
				wantAskOnce(t, fmt.Sprintf("%s %s %q", tool, mode, p), Decide(tool, Specs[tool], ruleArgs("path", p), uncEnv(mode, "")))
			}
		}
	}
	// 本地写法的上级本来就问（基线）
	wantAskOnce(t, "本地上级", Decide("grep", Specs["grep"], ruleArgs("path", `C:\Users`), uncEnv("auto", "")))
	// 不相干的本地目录不该被带上
	if d := Decide("grep", Specs["grep"], ruleArgs("path", `D:\work\proj`), uncEnv("auto", "")); d.Kind != "allow" {
		t.Errorf("工作目录里的搜索不该问：%+v", d)
	}
}

// 用户规则：放行规则（再宽也一样）、授权都放不过 UNC；ask 规则只会更严
func TestUNCUserRulesCannotAllow(t *testing.T) {
	for _, spec := range []string{"Read", "Read(*)", "Read(//**)", "Read(**)", `Read(//localhost/C$/**)`, "Write", "Write(*)", "Read(//?/C:/**)"} {
		tool := "read"
		if spec[0] == 'W' {
			tool = "write"
		}
		for _, p := range uncShapes {
			for _, mode := range []string{"manual", "auto", "yolo"} {
				env := uncEnv(mode, "")
				env.Rules = mustRules(t, [2]string{spec, "allow"})
				wantAskOnce(t, fmt.Sprintf("规则 %s %s %q", spec, mode, p), Decide(tool, Specs[tool], ruleArgs("path", p), env))
			}
		}
	}
	// 限定在项目里的放行规则
	env := uncEnv("auto", "conversation")
	env.Conversation = "local:console:a"
	env.Rules = []UserRule{scoped(t, "Read(*)", "allow", uncWorkdir, "")}
	for _, p := range uncShapes {
		wantAskOnce(t, "项目范围放行规则 "+p, Decide("read", Specs["read"], ruleArgs("path", p), env))
	}
	// ask 规则盖住时也是每次问
	env = uncEnv("yolo", "")
	env.Rules = mustRules(t, [2]string{"Read(*)", "ask"})
	wantAskOnce(t, "ask 规则", Decide("read", Specs["read"], ruleArgs("path", uncShapes[0]), env))
	// 拒绝规则照常拒绝
	env = uncEnv("yolo", "")
	env.Rules = mustRules(t, [2]string{"Read(*)", "deny"})
	if d := Decide("read", Specs["read"], ruleArgs("path", uncShapes[0]), env); d.Kind != "deny" {
		t.Errorf("拒绝规则要先于一切：%+v", d)
	}
}

// 命令文字里的路径：拆没拆成功都要问
func TestUNCInCommandAsk(t *testing.T) {
	cmds := []string{
		`Get-Content \\localhost\C$\Users\x\sdata\c.yml`,
		`Get-Content //localhost/C$/Users/x/sdata/c.yml`,
		`Get-Content '\\127.0.0.1\c$\Users\x\sdata\c.yml'`,
		`Get-Content "\\?\C:\Users\x\sdata\c.yml"`,
		`Get-Content -LiteralPath \\.\C:\Users\x\sdata\c.yml`,
		`Get-Content -Path:\\?\UNC\localhost\C$\Users\x\sdata\c.yml`,
		`Get-Content -Path://localhost/C$/Users/x/sdata/c.yml`,
		`gc (\\localhost\C$\Users\x\sdata\c.yml)`,
		`$p=\\localhost\C$\Users\x\sdata\c.yml; gc $p`,
		`cat a.txt|gc \\localhost\C$\x`,
		`Get-Content x>\\localhost\C$\x`,
		`type \\\localhost\C$\x`,
		`Get-Content \\fileserver\share\a.txt`,
	}
	for _, c := range cmds {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				env := uncEnv(mode, granted)
				wantAskOnce(t, fmt.Sprintf("命令无分析 %s %q", mode, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
				// 分析器说它是只读、没有目标时也一样（只看命令文字）
				env.Shell = &Verdict{ReadOnly: true}
				wantAskOnce(t, fmt.Sprintf("命令只读 %s %q", mode, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
			}
		}
	}
	// 分析器拆出来的目标是 UNC，命令文字里却看不出来（变量拼的之类）：按目标问
	for _, target := range uncShapes {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for name, v := range map[string]Verdict{
				"Targets":     {ReadOnly: true, Targets: []string{target}},
				"FileTargets": {ReadOnly: true, FileTargets: []string{target}},
				"Runs":        {ReadOnly: true, Runs: []string{target}},
			} {
				env := uncEnv(mode, "conversation")
				v := v
				env.Shell = &v
				wantAskOnce(t, "拆出目标 "+name+" "+target, Decide("powershell", Specs["powershell"], ruleArgs("command", "gc $p"), env))
			}
		}
	}
	// 放行规则不能放行带 UNC 的命令
	env := uncEnv("auto", "")
	env.Rules = mustRules(t, [2]string{"Bash(Get-Content *)", "allow"}, [2]string{"PowerShell(Get-Content:*)", "allow"})
	env.Shell = &Verdict{ReadOnly: true, Names: []string{"Get-Content"}}
	wantAskOnce(t, "放行规则", Decide("powershell", Specs["powershell"], ruleArgs("command", `Get-Content \\localhost\C$\Users\x\sdata\c.yml`), env))
}

// 不该多问的：URL、普通本地命令、注释里常见的写法
func TestUNCInCommandNoFalseAlarm(t *testing.T) {
	cmds := []string{
		`Invoke-WebRequest https://example.com/a//b`,
		`Invoke-RestMethod -Uri http://localhost:8080/api`,
		`Get-ChildItem D:\work\proj`,
		`Get-Content D:\work\proj\a.txt`,
		`Write-Output "a//b"`,
		`Get-Content C:\\Users\\x\\a.txt`,
		`git log --oneline`,
	}
	for _, c := range cmds {
		if CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("不该当成碰到受保护目录：%q", c)
		}
	}
	// 引号里单独写的两个斜杠或反斜杠是个完整的字符串（替换、拆分用），不是共享路径的开头（比 Bun 宽松）
	for _, c := range []string{`$s -replace '//',''`, `$s.Split('\\')`, `[regex]::Replace($x, '\\', '/')`, `$s -replace '\\','/'`, `"//"`, `Write-Output '\\'`} {
		if CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("单独的引号字符串不该算共享路径：%q", c)
		}
	}
	// 引号里的共享路径照算
	for _, c := range []string{`Get-Content '\\server\share\a'`, `Get-Content "//server/share/a"`, `Get-Content ('\\?\C:\x')`} {
		if !CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("引号里的共享路径该算：%q", c)
		}
	}
}

// 空、缺、类型错的路径参数：不崩，也不把普通情况带成问
func TestUNCOddArgs(t *testing.T) {
	odd := []jsjson.Value{
		ruleArgs("path", ""),
		ruleArgs(),
		{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Null}}},
		{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Number, Num: 5}}},
		{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Array}}},
		{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Object, Obj: map[string]jsjson.Value{}}}},
		{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Bool, Bool: true}}},
	}
	for i, a := range odd {
		d := Decide("read", Specs["read"], a, uncEnv("auto", ""))
		if d.Kind != "allow" {
			t.Errorf("奇怪参数 #%d 读工作目录里的东西不该问：%+v", i, d)
		}
	}
	// 数组里带 UNC：工具把非字符串的 path 当没写（"."），判定也这样取，读到的是工作目录，不问
	arr := jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.Array, Arr: []jsjson.Value{{Kind: jsjson.String, Str: `\\localhost\C$\Users\x\sdata\c.yml`}}}}}
	if d := Decide("read", Specs["read"], arr, uncEnv("auto", "")); d.Kind != "allow" {
		t.Errorf("数组 path 工具当 \".\"，不该问：%+v", d)
	}
}

// path 不是字符串：工具一律当 "."（grep/find 会搜整个工作目录），判定要和它对齐——
// 工作目录是受保护目录的上级时，grep/find 的 path 写成 {}、0、true、["a"] 都要问
func TestNonStringPathArgAlignedWithTools(t *testing.T) {
	odd := map[string]jsjson.Value{
		"数字":   {Kind: jsjson.Number, Num: 0},
		"布尔":   {Kind: jsjson.Bool, Bool: true},
		"空对象":  {Kind: jsjson.Object, Obj: map[string]jsjson.Value{}},
		"数组":   {Kind: jsjson.Array, Arr: []jsjson.Value{{Kind: jsjson.String, Str: "a"}}},
		"空数组":  {Kind: jsjson.Array},
		"null": {Kind: jsjson.Null},
	}
	for name, v := range odd {
		args := jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": v, "pattern": {Kind: jsjson.String, Str: "x"}}}
		for _, tool := range []string{"grep", "find"} {
			for _, mode := range []string{"manual", "auto", "yolo"} {
				env := uncEnv(mode, "")
				env.Workdir = `C:\Users\x` // 受保护目录 C:\Users\x\sdata 的上级
				wantAskOnce(t, fmt.Sprintf("%s path=%s %s", tool, name, mode), Decide(tool, Specs[tool], args, env))
			}
		}
		if got := PathArg(args); got != "." {
			t.Errorf("PathArg(%s) = %q，应该是 \".\"", name, got)
		}
	}
	if PathArg(ruleArgs("path", "a.txt")) != "a.txt" || PathArg(ruleArgs("path", "")) != "." || PathArg(ruleArgs()) != "." {
		t.Error("字符串照用，空和缺当 \".\"")
	}
}

// 无盘符的根相对路径（\data\x、/data/x）：策略按进程当前盘的根、工具接在工作目录后面、还有按工作目录所在盘的根，
// 三种解释任一碰到受保护目录都要问
func TestRootedPathsAllInterpretations(t *testing.T) {
	windowsPathsOnly(t)
	prot := `D:\work\proj\data`
	for _, tool := range []string{"read", "ls", "grep", "find", "write", "edit"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				for _, p := range []string{`\data\memo\x.txt`, `/data/memo/x.txt`, `\DATA\memo\x.txt`, `\data`, `/Data/`, `\data\memo\..\x.txt`, `\data.\x.txt`, `@\data\x.txt`} {
					env := uncEnv(mode, granted)
					env.ProtectedDirs = []string{prot}
					wantAskOnce(t, fmt.Sprintf("%s %s granted=%q %q", tool, mode, granted, p), Decide(tool, Specs[tool], ruleArgs("path", p), env))
				}
			}
		}
	}
	// 工作目录所在盘的根：\data\x 在 E 盘工作目录下，另一种解释是 E:\data\x
	for _, p := range []string{`\data\x.txt`, `/data/x.txt`} {
		env := uncEnv("auto", "")
		env.Workdir, env.ProtectedDirs = `E:\work\proj`, []string{`E:\data`}
		wantAskOnce(t, "工作目录盘的根 "+p, Decide("read", Specs["read"], ruleArgs("path", p), env))
	}
	// 搜根：工作目录是受保护目录的上级，递归搜 \ 会搜进去
	for _, tool := range []string{"grep", "find"} {
		env := uncEnv("auto", "")
		env.ProtectedDirs = []string{prot}
		wantAskOnce(t, tool+` \`, Decide(tool, Specs[tool], ruleArgs("path", `\`), env))
	}
	// 不相干的不该被带上
	for _, p := range []string{`\other\x.txt`, `/other/x.txt`, `\datax\x.txt`} {
		env := uncEnv("auto", "")
		env.ProtectedDirs = []string{prot}
		if d := Decide("read", Specs["read"], ruleArgs("path", p), env); d.Kind != "allow" {
			t.Errorf("%q 不该问：%+v", p, d)
		}
	}
}

// TouchesProtected 自己（项目说明读取、检查点、编辑提示也走它）
func TestUNCTouchesProtectedDirect(t *testing.T) {
	for _, p := range uncShapes {
		for _, rec := range []bool{false, true} {
			if !TouchesProtected(p, uncWorkdir, []string{uncProtected}, rec) {
				t.Errorf("TouchesProtected(%q, recursive=%v) 该为真", p, rec)
			}
		}
	}
	for _, p := range []string{`D:\work\proj\a.txt`, `a.txt`, `C:\Users\x\other\a.txt`, ``} {
		if TouchesProtected(p, uncWorkdir, []string{uncProtected}, false) {
			t.Errorf("TouchesProtected(%q) 不该为真", p)
		}
	}
}

// gate 要先解析联接的路径里不能有网络共享、设备路径：解析会真的连过去（SMB），而它们本来就每次都问
func TestUNCNotResolvedAsLinks(t *testing.T) {
	windowsPathsOnly(t)
	for _, p := range uncShapes {
		env := uncEnv("auto", "")
		got := LinkPaths(Specs["read"], ruleArgs("path", p), env)
		for _, g := range got {
			if uncLike(g) {
				t.Errorf("LinkPaths 不该带上 %q（得到 %q）", p, g)
			}
		}
		env.Shell = &Verdict{FileTargets: []string{p}, Runs: []string{p}}
		for _, g := range LinkPaths(Specs["powershell"], ruleArgs("command", "x"), env) {
			if uncLike(g) {
				t.Errorf("命令目标 LinkPaths 不该带上 %q（得到 %q）", p, g)
			}
		}
	}
	// 本地路径照旧要解析
	got := LinkPaths(Specs["read"], ruleArgs("path", `D:\work\proj\a.txt`), uncEnv("auto", ""))
	if len(got) == 0 || got[0] != `D:\work\proj\a.txt` {
		t.Errorf("本地路径该照旧在里面：%q", got)
	}
}

// IsNetworkOrDevicePath 本身：原文、file: 写法都认，本地写法不误伤
func TestUNCShapeHelper(t *testing.T) {
	for _, p := range uncShapes {
		if !IsNetworkOrDevicePath(p) {
			t.Errorf("IsNetworkOrDevicePath(%q) 该为真", p)
		}
	}
	for _, p := range []string{``, `C:\a`, `/c/Users/x`, `a/b`, `\a`, `/a`, `D:\work\proj\a//b`, `C:\\Users`, `file:///C:/Users/x`, `~/a`, `.`} {
		if IsNetworkOrDevicePath(p) {
			t.Errorf("IsNetworkOrDevicePath(%q) 不该为真", p)
		}
	}
}

// 驱动器相对路径（盘符后面不是分隔符）：当前目录不明，字面比不了受保护目录，看不清就问
var driveRelativeShapes = []string{`C:sdata\c.yml`, `c:sdata/c.yml`, `C:..\x`, `C:`, `D:foo`, `@C:sdata\c.yml`, "\u00a0C:sdata\\c.yml", `C:.\sdata\c.yml`}

func TestDriveRelativePathAsk(t *testing.T) {
	for _, tool := range []string{"read", "ls", "grep", "find", "write", "edit"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				for _, p := range driveRelativeShapes {
					wantAskOnce(t, fmt.Sprintf("%s %s granted=%q %q", tool, mode, granted, p), Decide(tool, Specs[tool], ruleArgs("path", p), uncEnv(mode, granted)))
				}
			}
		}
	}
	// 放行规则也越不过
	for _, mode := range []string{"manual", "auto", "yolo"} {
		env := uncEnv(mode, "")
		env.Rules = mustRules(t, [2]string{"Read(*)", "allow"})
		wantAskOnce(t, "放行规则 "+mode, Decide("read", Specs["read"], ruleArgs("path", `C:sdata\c.yml`), env))
	}
	// 不误伤：完整的本地路径、git bash 写法、file: 写法
	for _, p := range []string{`D:\work\proj\a.txt`, `D:/work/proj/a.txt`, `/d/work/proj/a.txt`, `file:///D:/work/proj/a.txt`, `a.txt`, `sub\a:b.txt`} {
		if d := Decide("read", Specs["read"], ruleArgs("path", p), uncEnv("auto", "")); d.Kind != "allow" {
			t.Errorf("%q 不该问：%+v", p, d)
		}
	}
	// 分析器给的目标
	for _, p := range driveRelativeShapes {
		for name, v := range map[string]Verdict{"Targets": {ReadOnly: true, Targets: []string{p}}, "FileTargets": {ReadOnly: true, FileTargets: []string{p}}, "Runs": {ReadOnly: true, Runs: []string{p}}} {
			v := v
			if name == "Targets" && !strings.ContainsAny(p[2:], `\/`) {
				continue // 命令里的普通值（-Format "H:mm"）不是路径：Targets 里盘符后面没有分隔符的不当驱动器相对路径
			}
			env := uncEnv("yolo", "conversation")
			env.Shell = &v
			wantAskOnce(t, "拆出目标 "+name+" "+p, Decide("powershell", Specs["powershell"], ruleArgs("command", "gc $p"), env))
		}
	}
	// LinkPaths 不解析它们
	for _, p := range driveRelativeShapes {
		for _, g := range LinkPaths(Specs["read"], ruleArgs("path", p), uncEnv("auto", "")) {
			if g != uncWorkdir && g != uncProtected && !isAbsWin(g) {
				t.Errorf("LinkPaths 带上了 %q 的解析：%q", p, g)
			}
		}
	}
	if len(LinkPaths(Specs["read"], ruleArgs("path", `C:sdata\c.yml`), uncEnv("auto", ""))) != 2 { // 只剩工作目录和受保护目录
		t.Errorf("驱动器相对路径不该进联接解析：%q", LinkPaths(Specs["read"], ruleArgs("path", `C:sdata\c.yml`), uncEnv("auto", "")))
	}
}

func TestDriveRelativeInCommand(t *testing.T) {
	asks := []string{
		`Get-Content C:sdata\c.yml`, `gc 'C:sdata\c.yml'`, `gc "c:sdata/c.yml"`, `gc -Path:C:sdata\c.yml`, `type (C:sdata\c.yml)`,
		`cat a.txt|gc C:s\c.yml`, `$p=C:sdata\c.yml; gc $p`, `gc C:..\x`, `gc C:~\x`,
	}
	for _, c := range asks {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				env := uncEnv(mode, granted)
				wantAskOnce(t, fmt.Sprintf("命令 %s %q", mode, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
				// 分析器拆开了：以它给的为准（路径参数上的 C:foo 记进 PathDrive；括号、变量算出来的是“值不确定”），不再看命令文字上的驱动器相对写法
			}
		}
		for _, mode := range []string{"auto", "yolo"} {
			env := shellEnv(t, mode, "", c, uncProtected)
			if d := decideCmd(env, c); d.Kind != "ask" {
				t.Errorf("分析器拆开后 %s %q 也该问：%+v", mode, c, d)
			}
		}
	}
	for _, c := range []string{
		`Get-Content C:\Users\x\other.txt`, `Get-Content C:/a.txt`, `Get-Content D:\work\proj\a.txt`, `Invoke-WebRequest https://example.com/a:b`,
		`Write-Output "Note: ok"`, `Write-Output 'Result:ok'`, `Set-Location C:`, `Get-ChildItem 'C:'`, `git log --format=%h:%s`, `Get-Date -Format HH:mm:ss`,
	} {
		if CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("不该当成碰到受保护目录：%q", c)
		}
	}
}

func TestIsOpaquePathHelper(t *testing.T) {
	for _, p := range append(append([]string{}, uncShapes...), driveRelativeShapes...) {
		if !IsOpaquePath(p) {
			t.Errorf("IsOpaquePath(%q) 该为真", p)
		}
	}
	for _, p := range []string{``, `C:\a`, `C:/a`, `/c/Users/x`, `a/b`, `\a`, `.`, `~/a`, `file:///C:/Users/x`, `https://example.com/a`, `ab:c`, `1:a`} {
		if IsOpaquePath(p) {
			t.Errorf("IsOpaquePath(%q) 不该为真", p)
		}
	}
}

// OutsideTargets：不透明路径不进“外面的目录”列表（记目录要 Stat），只记成看不清
func TestOutsideTargetsOpaque(t *testing.T) {
	for _, p := range append(append([]string{}, uncShapes...), driveRelativeShapes...) {
		env := uncEnv("auto", "")
		env.Shell = &Verdict{FileTargets: []string{p}, Targets: []string{p}}
		paths, blind := OutsideTargets(env)
		if len(paths) != 0 || !blind {
			t.Errorf("%q：该记成看不清，得到 %q blind=%v", p, paths, blind)
		}
	}
	env := uncEnv("auto", "")
	env.Shell = &Verdict{FileTargets: []string{`E:\other\a.txt`}}
	if paths, blind := OutsideTargets(env); len(paths) != 1 || blind {
		t.Errorf("本地的外面目录照旧：%q %v", paths, blind)
	}
}

// 复核找到的误报：格式串、JSON 样的写法、时间不是路径
func TestCommandNoFalseAlarmReview(t *testing.T) {
	for _, c := range []string{
		`Get-Date -Format "H:mm"`, `Get-Date -Format 'yyyy-MM-dd H:mm'`, `node -e "console.log({a:1})"`, `Write-Host "T:5 done"`,
		`git log --format='%an: %s' -5`, `ffmpeg -i in.mp4 -vf "scale=1280:720" out.mp4`, `ssh -L 8080:localhost:80 host`,
		`Get-Date -Format "h:mm:ss tt"`, `echo {a:1,b:2}`,
		`Invoke-WebRequest https://x.com/a?b=1/c`, `curl http://[::1]:8080/x/`, `Invoke-RestMethod "https://api.example.com/v1/items?id=*/x"`,
	} {
		if CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("不该当成碰到受保护目录：%q", c)
		}
	}
	// 收窄后仍要报的
	for _, c := range []string{`gc C:s\c.yml`, `gc "C:s/c.yml"`, `gc C:..\x`, `gc C:~\x`, `gc -Path:C:sdata\c.yml`} {
		if !CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("该当成碰到受保护目录：%q", c)
		}
	}
}

// 数据流名、末尾点和空格：Windows 规整以后指到受保护的文件，字面比不上
func TestStreamAndDotFormsAsk(t *testing.T) {
	windowsPathsOnly(t)
	for _, p := range []string{
		`C:\Users\x\sdata\c.yml::$DATA`, `C:\Users\x\sdata::$INDEX_ALLOCATION\c.yml`, `C:\Users\x\sdata\c.yml:alt`,
		`C:\Users\x\sdata::$INDEX_ALLOCATION\new.txt`, `D:\work\proj\.env:x`, `D:\work\proj\.env::$DATA`, `D:\work\proj\.env.local::$DATA`,
		`D:\work\proj\.env\`, `C:\Users\x\sdata\memo\..\c.yml`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			wantAskOnce(t, mode+" "+p, Decide("read", Specs["read"], ruleArgs("path", p), uncEnv(mode, "")))
		}
	}
	if d := Decide("read", Specs["read"], ruleArgs("path", `D:\work\proj\a:b.txt`), uncEnv("auto", "")); d.Kind != "allow" {
		t.Errorf("无关的带冒号文件名不该问：%+v", d)
	}
	for in, want := range map[string]string{`C:\a\b.txt::$DATA`: `C:\a\b.txt`, `C:\d::$INDEX_ALLOCATION\x`: `C:\d\x`, `C:\.env:x`: `C:\.env`, `C:\a`: `C:\a`, `:x`: `:x`} {
		if got := dropTrailingDotSpace(in); got != want {
			t.Errorf("dropTrailingDotSpace(%q) = %q，应该是 %q", in, got, want)
		}
	}
}

// 拒绝规则：指向本机盘的共享、设备路径写法、末尾点和数据流，都要判拒绝而不是降成问；放行、ask 方向不还原
func TestDenyRuleSeesThroughShareForms(t *testing.T) {
	windowsPathsOnly(t)
	deny := mustRules(t, [2]string{"Read(D:/secret/**)", "deny"}, [2]string{"Write(D:/secret/**)", "deny"})
	env := Env{Mode: "yolo", Workdir: `D:\Work`, ProtectedDirs: []string{`D:\Data`}, Audience: ownerConsole, Declared: true, Rules: deny, Conversation: "c"}
	for _, p := range []string{
		`\\?\D:\secret\a.txt`, `\\.\D:\secret\a.txt`, `\\localhost\D$\secret\a.txt`, `//localhost/d$/secret/a.txt`, `\\127.0.0.1\D$\secret\a.txt`,
		`\\?\UNC\localhost\D$\secret\a.txt`, `//?/UNC/LOCALHOST/D$/secret/a.txt`,
		`D:\secret.\a.txt`, `D:\secret \a.txt`, `D:\secret\a.txt.`, `D:\secret\a.txt::$DATA`, `D:\secret::$INDEX_ALLOCATION\a.txt`, `D:\secret\a.txt:alt`,
	} {
		for _, tool := range []string{"read", "write"} {
			if d := Decide(tool, Specs[tool], ruleArgs("path", p), env); d.Kind != "deny" {
				t.Errorf("%s %q 该被拒绝规则拒绝：%+v", tool, p, d)
			}
		}
	}
	if d := Decide("read", Specs["read"], ruleArgs("path", `D:\other\a.txt`), env); d.Kind == "deny" {
		t.Errorf("无关路径不该拒绝：%+v", d)
	}
	for in, want := range map[string]string{`\\localhost\C$\x`: `C:\x`, `\\?\C:\x`: `C:\x`, `\\fileserver\C$\x`: `\\fileserver\C$\x`, `\\localhost\share\x`: `\\localhost\share\x`, `D:\x`: `D:\x`} {
		if got := localOfShare(in); got != want {
			t.Errorf("localOfShare(%q) = %q，应该是 %q", in, got, want)
		}
	}
}

// 命令文字里受保护路径的各种写法：规整以后再比
func TestCommandTextNormalizedBeforeMatch(t *testing.T) {
	windowsPathsOnly(t)
	cmds := []string{
		`Get-Content 'C:\\Users\\x\\sdata\\c.yml'`,
		`Get-Content ('C:\\Users\\x\\sdata\\c.yml')`,
		`Get-Content @('C:\\Users\\x\\sdata\\c.yml')`,
		`$p = 'C:\\Users\\x\\sdata\\c.yml'; Get-Content $p`,
		`curl.exe -s file:///C:/Users/x/./sdata/c.yml`,
		`curl.exe -s file:///C:/Users/x/sdata/./c.yml`,
		`Get-Content 'FileSystem::C:\Users\x\sdata\.\c.yml'`,
		`Get-Content 'Microsoft.PowerShell.Core\FileSystem::C:\Users\x\.\sdata\c.yml'`,
		`gc C:\Users\x\sdata.\c.yml`,
		`gc "C:\Users\x\sdata \c.yml"`,
		`gc C:\Users\x\foo\..\sdata\c.yml`,
		`gc C:\Users\x\foo\bar\..\..\sdata\c.yml`,
		`gc C:\Users\x\sdata\c.yml::$DATA`,
		`gc C:\Users\x\SDATA\.\.\c.yml`,
		`gc C:///Users//x/sdata/c.yml`,
		`gc C:\Users\x.\sdata\c.yml`,
		`gc "C:\Users\x \sdata\c.yml"`,
	}
	for _, c := range cmds {
		if !CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("CommandTouchesProtected 该为真：%q", c)
		}
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				env := uncEnv(mode, granted)
				wantAskOnce(t, fmt.Sprintf("%s %q", mode, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
				env.Shell = &Verdict{ReadOnly: true}
				wantAskOnce(t, fmt.Sprintf("只读 %s %q", mode, c), Decide("powershell", Specs["powershell"], ruleArgs("command", c), env))
			}
		}
	}
	for _, c := range []string{`Get-Content D:\work\proj\a.txt`, `Get-ChildItem D:\work\proj -Recurse`, `git log --oneline`, `Get-ChildItem *.go`, `ls .\src\*.go`, `Get-Content C:\Users\x\other\..\y.txt`, `Get-Date`} {
		if CommandTouchesProtected(c, []string{uncProtected}) {
			t.Errorf("不该当成碰到受保护目录：%q", c)
		}
	}
}

// open 工具的 target 是路径：受保护、不透明的写法和别的路径工具一样每次问，授权不吃；网址和普通路径照旧
func TestOpenTargetProtected(t *testing.T) {
	windowsPathsOnly(t)
	targets := append([]string{`C:\Users\x\sdata\c.yml`, `C:\Users\x\sdata`, `C:\Users\x\sdata.\c.yml`, `C:\Users\x\sdata::$DATA`, `  C:\Users\x\sdata\c.yml  `, `@C:\Users\x\sdata\c.yml`}, uncShapes...)
	targets = append(targets, driveRelativeShapes...)
	for _, p := range targets {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				wantAskOnce(t, fmt.Sprintf("open %s granted=%q %q", mode, granted, p), Decide("open", Specs["open"], ruleArgs("target", p), uncEnv(mode, granted)))
			}
		}
	}
	for _, mode := range []string{"auto", "yolo"} { // 无盘符根相对：接在工作目录后面就是受保护目录
		env := uncEnv(mode, "conversation")
		env.ProtectedDirs = []string{`D:\work\proj\data`}
		wantAskOnce(t, "open 根相对 "+mode, Decide("open", Specs["open"], ruleArgs("target", `\data\x.txt`), env))
	}
	// 普通路径和网址：照旧（有这个对话的授权就放行，不被多问）
	for _, p := range []string{`D:\work\proj\a.txt`, `a.txt`, `https://example.com/a`, `http://localhost:8080/x//y`} {
		d := Decide("open", Specs["open"], ruleArgs("target", p), uncEnv("auto", "conversation"))
		if d.Kind != "allow" || d.By != ByConversation {
			t.Errorf("open %q 照旧：%+v", p, d)
		}
		if d := Decide("open", Specs["open"], ruleArgs("target", p), uncEnv("yolo", "")); d.Kind != "ask" || d.Sticky {
			t.Errorf("open %q 没授权照旧问（不带 Sticky）：%+v", p, d)
		}
	}
}

// 拒绝、ask 规则遇到无盘符根相对路径：三种解释任一命中就算；放行方向不变（放行规则不因此多放行）
func TestRulesSeeRootedPaths(t *testing.T) {
	windowsPathsOnly(t)
	wd := `D:\work\proj`
	mk := func(raw, effect string) []UserRule { return mustRules(t, [2]string{raw, effect}) }
	for _, p := range []string{`\secrets\a.txt`, `/secrets/a.txt`, `\SECRETS\a.txt`, `\secrets\x\..\a.txt`, `@/secrets/a.txt`} {
		for _, tool := range []string{"read", "write", "edit", "ls", "grep", "find"} {
			fam := "Read"
			if tool == "write" || tool == "edit" {
				fam = "Write"
			}
			env := Env{Mode: "yolo", Workdir: wd, Declared: true, Audience: ownerConsole, Conversation: "c", Rules: mk(fam+"(secrets/**)", "deny")}
			if d := Decide(tool, Specs[tool], ruleArgs("path", p), env); d.Kind != "deny" {
				t.Errorf("deny %s %q 该拒绝：%+v", tool, p, d)
			}
			env.Rules = mk(fam+"(secrets/**)", "ask")
			if d := Decide(tool, Specs[tool], ruleArgs("path", p), env); d.Kind != "ask" || d.RuleKey == "" || d.Limit != "once" {
				t.Errorf("ask %s %q 该被 ask 规则盖住：%+v", tool, p, d)
			}
		}
	}
	// 绝对规则：D:/work/proj/src/** 和 \work\proj\src\a.go（工作目录所在盘的根）
	env := Env{Mode: "yolo", Workdir: wd, Declared: true, Audience: ownerConsole, Conversation: "c", Rules: mk("Write(D:/work/proj/src/**)", "deny")}
	if d := Decide("write", Specs["write"], ruleArgs("path", `\work\proj\src\a.go`), env); d.Kind != "deny" {
		t.Errorf("工作目录所在盘的根解释该命中：%+v", d)
	}
	// 放行方向不变：放行规则不因为“另一种解释命中”就放行
	env.Rules = mk("Write(secrets/**)", "allow")
	env.Mode = "manual"
	if d := Decide("write", Specs["write"], ruleArgs("path", `\secrets\a.txt`), env); d.By == ByRule {
		t.Errorf("放行规则不看另外的解释：%+v", d)
	}
	// 不相干的不被拒
	env.Rules = mk("Read(secrets/**)", "deny")
	env.Mode = "yolo"
	if d := Decide("read", Specs["read"], ruleArgs("path", `\other\a.txt`), env); d.Kind == "deny" {
		t.Errorf("不相干路径不该拒绝：%+v", d)
	}
}

// 工作目录本身在网络共享上：相对路径解出来也是 UNC，读写都每次问（和 Bun 一样；在共享上干活的人会一直被问）
func TestUNCWorkdirRelativePath(t *testing.T) {
	for _, mode := range []string{"manual", "auto", "yolo"} {
		env := uncEnv(mode, "")
		env.Workdir = `\\fileserver\share\proj`
		for _, p := range []string{"a.txt", `sub\a.txt`, `.\a.txt`} {
			wantAskOnce(t, mode+" 相对路径 "+p, Decide("read", Specs["read"], ruleArgs("path", p), env))
		}
	}
}

// 本地写法里每段末尾的点和空格：Windows 会去掉，sdata.\c.yml 就是 sdata\c.yml，字面上却比不上受保护目录
func TestTrailingDotSpaceLocalForms(t *testing.T) {
	windowsPathsOnly(t)
	asks := []string{
		`C:\Users\x\sdata.\c.yml`, `C:\Users\x\sdata \c.yml`, `C:\Users\x\sdata. .\c.yml`, `C:\Users\x\sdata\c.yml.`, `C:\Users\x\sdata\c.yml `,
		`C:\Users\x\sdata.`, `C:\Users\x.\sdata\c.yml`, `C:/Users/x/sdata./c.yml`, `D:\work\proj\.env `, `D:\work\proj\.env.`, `D:\work\proj\sub.\.env.local`,
	}
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, p := range asks {
			wantAskOnce(t, fmt.Sprintf("%s %q", mode, p), Decide("read", Specs["read"], ruleArgs("path", p), uncEnv(mode, "")))
		}
	}
	for _, p := range []string{`C:\Users`, `C:\Users\x.`, `C:\Users\x \`} { // 上一级：递归搜索会搜进去
		for _, tool := range []string{"grep", "find"} {
			wantAskOnce(t, tool+" "+p, Decide(tool, Specs[tool], ruleArgs("path", p), uncEnv("auto", "")))
		}
	}
	// 不相干的不该被带上
	for _, p := range []string{`D:\work\proj\a.txt.`, `D:\work\proj\sub. \a.txt`, `D:\work\proj\..\proj\a.txt`, `D:\work\proj\...\a.txt`, `C:\Users\x\sdata2.\c.yml`} {
		if d := Decide("read", Specs["read"], ruleArgs("path", p), uncEnv("auto", "")); d.Kind != "allow" {
			t.Errorf("%q 不该问：%+v", p, d)
		}
	}
	for in, want := range map[string]string{`a.\b `: `a\b`, `.`: `.`, `..\x.`: `..\x`, `...\x`: `...\x`, `C:\a b \c`: `C:\a b\c`, ``: ``, `\\`: `\\`, `x. /y`: `x/y`} {
		if got := dropTrailingDotSpace(in); got != want {
			t.Errorf("dropTrailingDotSpace(%q) = %q，应该是 %q", in, got, want)
		}
	}
}
