package policy

// 命令里“碰受保护目录”的判断改用分析器给的信息（语法树里参数的种类、换目录以后的位置、递归搜索从哪里开始）后的回归测试。
// 用真的分析器拆命令；机器上起不来 pwsh 就跳过。

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	shAnalyzerOnce sync.Once
	shAnalyzer     *Analyzer
)

// judged 返回命令拆开以后的结论；拆不开返回 nil
func judged(t *testing.T, cmd string) *Verdict {
	t.Helper()
	shAnalyzerOnce.Do(func() { shAnalyzer = NewAnalyzer() })
	an := shAnalyzer.Analyze(cmd, 60*time.Second)
	if an == nil {
		t.Skip("这台机器起不来拆命令的 pwsh")
	}
	v := Judge(*an)
	return &v
}

func shellEnv(t *testing.T, mode, granted, cmd string, prot ...string) Env {
	env := uncEnv(mode, granted)
	env.Workdir = `D:\work\proj`
	env.ProtectedDirs = prot
	env.Shell = judged(t, cmd)
	return env
}

func decideCmd(env Env, cmd string) Decision {
	return Decide("powershell", Specs["powershell"], ruleArgs("command", cmd), env)
}

// 日常的命令不该被多问：正则、glob、格式串、工作目录里的通配
func TestEverydayCommandsNotAsked(t *testing.T) {
	cmds := []string{
		`rg -n "func \(g \*Gate\)" native/internal`,
		`Get-Content a.txt | Where-Object { $_ -match '\s*\d+' }`,
		`Get-Content a.txt | ForEach-Object { $_ -replace '\d*\.\d','' }`,
		`rg '\w+\s*\w+' src`,
		`Select-String '\[ERROR\]' -Path .\log.txt`,
		`git ls-files 'native/**/*.go'`,
		`git diff -- native/internal/**/*.go`,
		`rg foo -g 'src/**/*.ts' src`,
		`Get-ChildItem .\src\*\*.go`,
		`Get-ChildItem -Recurse -Filter *.go src`,
		`Get-Date -Format "H:mm"`,
		`rg 'a.*\d+' src`,
		`Select-String -Pattern 'x*\w+' -Path a.txt`,
		`Write-Host "T:5 done"`,
		`Get-Content src\*.txt`,
		`Get-ChildItem *.go`,
		`git log --oneline -5`,
	}
	for _, prot := range [][]string{{`D:\work\data`, `C:\Users\x\.ssh`}, {`D:\work\proj\data`, `C:\Users\x\.ssh`}} {
		for _, c := range cmds {
			for _, mode := range []string{"auto", "yolo"} {
				env := shellEnv(t, mode, "", c, prot...)
				d := decideCmd(env, c)
				if mode == "auto" && d.Kind != "allow" {
					t.Errorf("auto %v 不该问：%q → %+v", prot[0], c, d)
				}
				if d.Sticky && d.Limit == "once" && strings.Contains(d.Reason, "数据目录") {
					t.Errorf("%s %v 被当成碰到受保护目录：%q", mode, prot[0], c)
				}
			}
		}
	}
}

// 不是只读的日常命令（node、网络请求）：会照旧问，但不能被当成碰到受保护目录
func TestEverydayNonReadOnlyNotProtected(t *testing.T) {
	for _, c := range []string{`node -e "console.log({a:1})"`, `Invoke-WebRequest https://x.com/a?b=1/c`, `curl http://[::1]:8080/x/`, `ssh -L 8080:localhost:80 host`} {
		for _, prot := range [][]string{{`D:\work\data`}, {`D:\work\proj\data`}} {
			env := shellEnv(t, "auto", "", c, prot...)
			if d := decideCmd(env, c); d.Sticky {
				t.Errorf("%v %q 被当成碰到受保护目录：%+v", prot, c, d)
			}
		}
	}
}

// 路径是变量、括号、子表达式的读文件命令：值不确定，普通的问，“这个对话”授权管用，不是每次都问
func TestComputedReadPathsAskButGrantable(t *testing.T) {
	cmds := []string{
		`Get-Content $file`, `Get-Content -Path $file`, `Get-Content -Encoding utf8 $file`, `Get-Content (Join-Path $a b)`,
		`foreach ($f in 1) { Get-Content $f }`, `Get-Content -LiteralPath:$f`, `gc @($a,$b)`, `Get-ChildItem $dir`,
		`Select-String foo $files`, `Get-Content "$base\x.txt"`, `Get-Content "$($x.Name).txt"`, `$files | Get-Content`, `Import-Csv $p`,
	}
	for _, c := range cmds {
		env := shellEnv(t, "auto", "", c, `D:\work\data`)
		d := decideCmd(env, c)
		if d.Kind != "ask" || d.Sticky || d.Limit == "once" {
			t.Errorf("auto %q 该是普通的问（不 Sticky、不 once）：%+v", c, d)
		}
		env = shellEnv(t, "yolo", "", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Kind != "ask" || d.Sticky {
			t.Errorf("yolo %q 该问（不 Sticky）：%+v", c, d)
		}
		env = shellEnv(t, "auto", "conversation", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Kind != "allow" || d.By != ByConversation {
			t.Errorf("这个对话授权过：%q → %+v", c, d)
		}
		env = shellEnv(t, "auto", "always", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Kind == "allow" {
			t.Errorf("路径不确定的不吃“始终”：%q → %+v", c, d)
		}
		if CanAlways("powershell", ruleArgs("command", c), true, env.Shell) {
			t.Errorf("路径不确定的不该给“始终允许”：%q", c)
		}
	}
	// 写死的路径照旧放行
	for _, c := range []string{`Get-Content a.txt`, `Get-Content -Path .\src\a.go`, `Get-ChildItem src`} {
		env := shellEnv(t, "auto", "", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Kind != "allow" {
			t.Errorf("写死的路径不该问：%q → %+v", c, d)
		}
	}
}

// 展开字符串里能静态知道前缀的：代入 HOME / USERPROFILE / PWD 再判受保护，命中就每次问
func TestExpandableStringKnownPrefix(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\x`)
	t.Setenv("HOME", `C:\Users\x`)
	prot := []string{`C:\Users\x\.ssh`, `D:\work\proj\data`}
	protectedCmds := []string{
		`Get-Content "$HOME\.ssh\id_rsa"`, `Get-Content "$env:USERPROFILE\.ssh\id_rsa"`, `Get-Content "${env:USERPROFILE}\.ssh\id_rsa"`,
		`Get-Content "$PWD\data\x.db"`, `Get-Content "$home\.ssh\$name"`, `Get-Content -Path "$HOME\.ssh\config"`, `Get-Content "$env:USERPROFILE\.SSH\id_rsa"`,
	}
	for _, c := range protectedCmds {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation", "always"} {
				env := shellEnv(t, mode, granted, c, prot...)
				d := decideCmd(env, c)
				wantAskOnce(t, fmt.Sprintf("%s granted=%q %q", mode, granted, c), d)
			}
		}
	}
	for _, c := range []string{`Get-Content "$HOME\notes\a.txt"`, `Get-Content "$PWD\src\a.go"`} {
		env := shellEnv(t, "auto", "", c, prot...)
		if d := decideCmd(env, c); d.Kind != "allow" {
			t.Errorf("代入后不在受保护目录里，不该问：%q → %+v", c, d)
		}
	}
	if got := substituteKnownVars(`"$HOMEX\a"`, `C:\h`, `D:\w`, true); !strings.Contains(got, "$HOMEX") {
		t.Errorf("$HOMEX 不是 $HOME：%q", got)
	}
}

// 按顺序模拟换目录：cd / Set-Location / Push-Location / sl / chdir，含 ..、花括号块里；换到看不清的地方后相对目标当值不确定
func TestCwdSimulation(t *testing.T) {
	prot := []string{`D:\work\proj\data`, `D:\work\sdata`}
	protectedCmds := []string{
		`cd data; Get-Content x.db`, `Set-Location .\data; gc x.db`, `Push-Location data; Get-Content x.db`, `sl data; cat x.db`, `chdir data; type x.db`,
		`cd ..; cd sdata; gc a.txt`, `cd ..\sdata; gc a.txt`, `& { cd data; gc x.db }`, `cd D:\work; cd sdata; gc a.txt`,
		`cd D:\work\proj; Set-Location data; Get-Content x.db`, `Set-Location D:\work\sdata; Get-Content a.txt`,
	}
	for _, c := range protectedCmds {
		for _, mode := range []string{"auto", "yolo"} {
			env := shellEnv(t, mode, "conversation", c, prot...)
			wantAskOnce(t, mode+" "+c, decideCmd(env, c))
		}
	}
	// 不在受保护目录里的：cd 之后照旧
	for _, c := range []string{`cd src; Get-Content a.go`, `Set-Location .\native; gc go.mod`} {
		env := shellEnv(t, "yolo", "", c, prot...)
		if d := decideCmd(env, c); d.Kind == "ask" && d.Sticky {
			t.Errorf("%q 不该当受保护：%+v", c, d)
		}
	}
	// 换到看不清的地方：后面的相对目标值不确定，普通问，“这个对话”授权管用，“始终”不管用
	for _, c := range []string{`Set-Location $x; Get-Content y.txt`, `cd (Join-Path $a b); gc y.txt`} {
		env := shellEnv(t, "auto", "", c, prot...)
		if d := decideCmd(env, c); d.Kind != "ask" || d.Sticky {
			t.Errorf("%q 该是普通的问：%+v", c, d)
		}
		if !env.Shell.CwdUnknown {
			t.Errorf("%q 该记下“换到哪看不清”", c)
		}
		env = shellEnv(t, "auto", "always", c, prot...)
		if d := decideCmd(env, c); d.Kind == "allow" {
			t.Errorf("%q 路径值不确定，不吃“始终”：%+v", c, d)
		}
	}
}

// 重定向写到哪、按路径运行的程序：并进碰受保护的比较
func TestRedirectAndRunTargetsProtected(t *testing.T) {
	prot := []string{`D:\work\proj\data`}
	for _, c := range []string{
		`Get-Date > data\x.txt`, `"x" >> .\data\log.txt`, `Get-Date | Out-File data\y.txt`, `.\data\tool.exe`, `& D:\work\proj\data\tool.exe --x`,
		`cd data; Get-Date > out.txt`, `Write-Output hi 2> data\err.txt`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, granted := range []string{"", "conversation"} {
				env := shellEnv(t, mode, granted, c, prot...)
				wantAskOnce(t, fmt.Sprintf("%s granted=%q %q", mode, granted, c), decideCmd(env, c))
			}
		}
	}
	for _, c := range []string{`Get-Date > out.txt`, `.\build.ps1`} {
		env := shellEnv(t, "yolo", "conversation", c, prot...)
		if d := decideCmd(env, c); d.Sticky {
			t.Errorf("%q 不该当受保护：%+v", c, d)
		}
	}
}

// 没写路径的递归命令：搜的是当前目录，当前目录是受保护目录或它的上级就要问；grep / findstr 不看只读
func TestRecursiveWithoutPath(t *testing.T) {
	inside := []string{`D:\work\proj\data`} // 工作目录是受保护目录的上级
	for _, c := range []string{
		`rg foo`, `rg -n foo`, `fd x`, `grep -rn foo`, `grep -R foo`, `findstr /s foo *.txt`, `findstr /s /i foo`, `Get-ChildItem -Recurse`, `gci -r -Filter *.go`,
		`ls -Recurse`, `Get-ChildItem -Recurse -Name`, `cd ..; rg foo`, `Get-ChildItem D:\work -Recurse`, `rg foo D:\work`, `grep -r foo ..`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			env := shellEnv(t, mode, "conversation", c, inside...)
			wantAskOnce(t, mode+" "+c, decideCmd(env, c))
		}
	}
	// 写了搜哪里、而且不是上级：不问
	for _, c := range []string{`rg foo src`, `grep -rn foo src`, `findstr /s foo src\*.go`, `Get-ChildItem src -Recurse`, `rg foo -g '*.go' src`} {
		env := shellEnv(t, "auto", "", c, inside...)
		if d := decideCmd(env, c); d.Sticky {
			t.Errorf("%q 不该当受保护：%+v", c, d)
		}
	}
	// 工作目录不是受保护目录的上级：不问
	for _, c := range []string{`rg foo`, `grep -rn foo`, `Get-ChildItem -Recurse`} {
		env := shellEnv(t, "auto", "", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Sticky {
			t.Errorf("工作目录不在受保护目录上面，%q 不该当受保护：%+v", c, d)
		}
	}
	// 删除类的上级目标交给灾难性命令那一关（对拍里这一组的标签不变）
	env := shellEnv(t, "auto", "", `Remove-Item D:\ -Recurse`, `D:\work\proj\data`)
	if len(env.Shell.SearchRoots) != 0 {
		t.Errorf("Remove-Item 不是搜索：%q", env.Shell.SearchRoots)
	}
}

// 通配符当目录名：通配之前的目录部分是受保护目录或它的上级才问
func TestWildcardDirOnlyWhenItCanReachProtected(t *testing.T) {
	prot := []string{`C:\Users\x\sdata`}
	for _, c := range []string{
		`Get-Content 'C:\Users\x\sd?ta\c.yml'`, `Get-Content 'C:\Users\x\[s]data\c.yml'`, `gc C:\Users\x\s*\c.yml`, `gc C:\Users\*\sdata\c.yml`, `gc C:\*\Users\x\sdata\c.yml`,
	} {
		for _, mode := range []string{"auto", "yolo"} {
			env := shellEnv(t, mode, "conversation", c, prot...)
			wantAskOnce(t, mode+" "+c, decideCmd(env, c))
		}
	}
	// 工作目录是受保护目录的上级：.\*\x 可能匹配到它
	env := shellEnv(t, "auto", "", `Get-Content .\*\x.txt`, `D:\work\proj\data`)
	wantAskOnce(t, `.\*\x.txt`, decideCmd(env, `Get-Content .\*\x.txt`))
	env = shellEnv(t, "auto", "", `Get-Content .\src\*\x.txt`, `D:\work\proj\data`)
	if d := decideCmd(env, `Get-Content .\src\*\x.txt`); d.Sticky {
		t.Errorf("src 下面的通配不会碰到 data：%+v", d)
	}
}

// 驱动器相对路径绑在路径参数上（Get-Content C:foo）要问；不是路径参数的值（-Format "H:mm"）不问
func TestDriveRelativeBoundToPathParam(t *testing.T) {
	prot := `D:\work\data`
	for _, c := range []string{`Get-Content C:foo`, `gc D:foo`, `Get-Content -Path C:sdata`, `Get-Item -LiteralPath D:x`} {
		for _, mode := range []string{"auto", "yolo"} {
			env := shellEnv(t, mode, "conversation", c, prot)
			wantAskOnce(t, mode+" "+c, decideCmd(env, c))
		}
	}
	for _, c := range []string{`Get-Date -Format "H:mm"`, `Write-Host "T:5"`, `Get-Date -Format "h:mm"`} {
		env := shellEnv(t, "auto", "", c, prot)
		if d := decideCmd(env, c); d.Kind != "allow" {
			t.Errorf("%q 不是路径参数的值，不该问：%+v", c, d)
		}
	}
}

// 外部搜索程序（rg、fd、grep、findstr）的路径位置是展开字符串或变量：和读文件命令一样按代入后判；认识常见带值开关再数位置参数
func TestNativeSearchersHiddenPaths(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\x`)
	t.Setenv("HOME", `C:\Users\x`)
	prot := []string{`C:\Users\x\.ssh`, `D:\work\data`}
	for _, c := range []string{
		`fd . "$HOME\.ssh"`, `rg foo "$HOME\.ssh"`, `grep -r foo "$HOME\.ssh"`, `grep foo "$HOME\.ssh\config"`, `rg --files "$HOME\.ssh"`,
		`rg -e foo "$HOME\.ssh"`, `rg -t go -g '*.go' foo "$env:USERPROFILE\.ssh"`, `findstr /s foo "$HOME\.ssh\*"`, `rg -A 3 -C 2 foo "$HOME\.ssh"`,
	} {
		for _, mode := range []string{"auto", "yolo"} {
			for _, granted := range []string{"", "conversation"} {
				env := shellEnv(t, mode, granted, c, prot...)
				wantAskOnce(t, fmt.Sprintf("%s granted=%q %q", mode, granted, c), decideCmd(env, c))
			}
		}
	}
	// 代不出来：普通的问，“这个对话”授权管用
	for _, c := range []string{`rg foo $dir`, `fd . "$base\x"`, `grep -r foo $d`} {
		env := shellEnv(t, "auto", "", c, prot...)
		if d := decideCmd(env, c); d.Kind != "ask" || d.Sticky {
			t.Errorf("auto %q 该是普通的问：%+v", c, d)
		}
		if !strings.HasPrefix(c, "grep") { // grep 本身不算只读，变量参数又让它拆不透，授权也不放行
			env = shellEnv(t, "auto", "conversation", c, prot...)
			if d := decideCmd(env, c); d.Kind != "allow" {
				t.Errorf("这个对话授权过：%q → %+v", c, d)
			}
		}
	}
	// 带值开关不算位置参数；要找的内容是变量不算路径
	inside := []string{`D:\work\proj\data`}
	for _, c := range []string{`rg -t go foo src`, `rg -g '*.go' foo src`, `rg -A 3 foo src`, `fd -e go . src`, `rg -n -g 'src/**' foo src`, `rg --files src`, `grep -rn -m 3 foo src`, `rg $pat src`} {
		env := shellEnv(t, "auto", "", c, inside...)
		d := decideCmd(env, c)
		if d.Sticky || (d.Kind != "allow" && !strings.Contains(c, "$pat") && !strings.HasPrefix(c, "grep")) { // grep 不算只读，普通的问不算误伤
			t.Errorf("inside %q 不该问：%+v", c, d)
		}
	}
	// 没写路径，或开关认不全：把当前目录当起点
	for _, c := range []string{`rg --files`, `rg -t go foo`, `rg --weird-switch foo src`, `fd -e go`, `rg -e foo`} {
		env := shellEnv(t, "auto", "conversation", c, inside...)
		wantAskOnce(t, c, decideCmd(env, c))
	}
}

// 递归搜索的起点用已知值代入（$HOME、$PWD）：代入出来是受保护目录的上级也要问（以前只看“在不在受保护目录里”）
func TestRecursiveSearchWithKnownVarRoots(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\x`)
	t.Setenv("HOME", `C:\Users\x`)
	cases := []struct {
		cmd  string
		prot []string
	}{
		{`rg foo $HOME`, []string{`C:\Users\x\.ssh`}},
		{`rg foo "$HOME"`, []string{`C:\Users\x\.ssh`}},
		{`rg foo $PWD`, []string{`D:\work\proj\data`}},
		{`fd . $HOME`, []string{`C:\Users\x\.ssh`}},
		{`grep -r foo $HOME`, []string{`C:\Users\x\.ssh`}},
		{`grep -rn foo "$PWD"`, []string{`D:\work\proj\data`}},
		{`findstr /s foo "$HOME\*"`, []string{`C:\Users\x\.ssh`}},
		{`Get-ChildItem -Recurse $HOME`, []string{`C:\Users\x\.ssh`}},
		{`gci -r "$PWD"`, []string{`D:\work\proj\data`}},
		{`cd src; rg foo $PWD`, []string{`D:\work\proj\src\data`}},
	}
	for _, c := range cases {
		for _, mode := range []string{"auto", "yolo"} {
			for _, granted := range []string{"", "conversation"} {
				env := shellEnv(t, mode, granted, c.cmd, c.prot...)
				wantAskOnce(t, fmt.Sprintf("%s granted=%q %q", mode, granted, c.cmd), decideCmd(env, c.cmd))
			}
		}
	}
	// 不递归的、或代入出来碰不到受保护目录的：不当受保护
	for _, c := range []struct {
		cmd  string
		prot []string
	}{
		{`Get-Content $HOME`, []string{`C:\Users\x\.ssh`}},   // 不是搜索
		{`rg foo $HOME`, []string{`D:\work\data`}},           // 代入后不是上级
		{`rg foo "$PWD\src"`, []string{`D:\work\proj\data`}}, // 起点在别处
		{`grep foo $HOME`, []string{`C:\Users\x\.ssh`}},      // grep 没带 -r，不递归
	} {
		env := shellEnv(t, "yolo", "conversation", c.cmd, c.prot...)
		if d := decideCmd(env, c.cmd); d.Sticky {
			t.Errorf("%q %v 不该当受保护：%+v", c.cmd, c.prot, d)
		}
	}
}

// $PWD 是写这条命令时的当前目录（cd 过就不是工作目录）；当前目录看不清就当值不确定
func TestPWDFollowsSimulatedCwd(t *testing.T) {
	prot := []string{`D:\work\proj\sub\data`}
	c := `cd sub; Get-Content "$PWD\data\x.db"`
	for _, mode := range []string{"auto", "yolo"} {
		env := shellEnv(t, mode, "conversation", c, prot...)
		wantAskOnce(t, mode+" "+c, decideCmd(env, c))
	}
	// 没 cd：$PWD 就是工作目录，data 在 sub 下面，不碰
	c = `Get-Content "$PWD\data\x.db"`
	if d := decideCmd(shellEnv(t, "auto", "", c, prot...), c); d.Sticky {
		t.Errorf("没 cd，$PWD\\data 不是受保护目录：%+v", d)
	}
	// cd 到看不清的地方：$PWD 不知道，普通的问，不是每次都问
	c = `cd $x; Get-Content "$PWD\data\x.db"`
	for _, p := range [][]string{prot, {`D:\work\proj\data`}} { // 第二组：如果错把 $PWD 当工作目录代入，会落在受保护目录里
		env := shellEnv(t, "auto", "", c, p...)
		if d := decideCmd(env, c); d.Kind != "ask" || d.Sticky {
			t.Errorf("%v %q 该是普通的问：%+v", p, c, d)
		}
	}
}

// 第三轮清掉的误伤：引号后紧跟 // 的正则或注释、正则当通配目录、裸盘符、输出文字
func TestThirdRoundFalsePositives(t *testing.T) {
	for _, prot := range [][]string{{`D:\work\data`}, {`D:\work\proj\data`}} {
		for _, c := range []string{
			`rg '//\s*TODO' src`, `rg "// TODO" src`, `Select-String -Pattern '//\s*FIXME' -Path a.txt`, `rg '//\d+' src`, `rg '//' src`,
			`rg '\.go$' src`, `rg 'a\.*\bfoo' src`, `rg '^foo.*$' src`, `rg '[a-z]+\.go' src`, `rg 'https?://x/*' src`, `rg 'foo\b.*bar\b' src`,
			`rg --files src`, `rg 'https?://x/y' src`, `rg "https?://example.com/a" src`, `ls D:`, `Get-ChildItem D:`, `echo a:b/c`, `Write-Host a:b/c`, `Write-Output "x:y/z"`,
		} {
			env := shellEnv(t, "auto", "", c, prot...)
			if d := decideCmd(env, c); d.Kind != "allow" {
				t.Errorf("%v auto %q 不该问：%+v", prot, c, d)
			}
		}
	}
	// 裸盘符：cd D: / Set-Location D: 是切到该盘的当前目录，当前目录看不清：后面的相对目标值不确定，普通的问，不是每次都问
	for _, c := range []string{`cd D:; Get-Content x.txt`, `Set-Location D:; gc a.txt`} {
		env := shellEnv(t, "auto", "", c, `D:\work\data`)
		if d := decideCmd(env, c); d.Sticky {
			t.Errorf("%q 不该当受保护：%+v", c, d)
		}
		if !env.Shell.CwdUnknown {
			t.Errorf("%q 该记下当前目录看不清", c)
		}
	}
	env := shellEnv(t, "auto", "", `cd D:`, `D:\work\data`)
	if d := decideCmd(env, `cd D:`); d.Sticky {
		t.Errorf("cd D: 不该当受保护：%+v", d)
	}
	// 真的共享写法仍然要问
	for _, c := range []string{`Get-Content \\localhost\C$\Users\x\sdata\c.yml`, `rg foo '\\server\share\x'`, `Get-Content "//server/share/a"`, `Get-Content '\\server\~share\x'`, `Get-Content '\\server\(a)\x'`} {
		env := shellEnv(t, "auto", "conversation", c, `C:\Users\x\sdata`)
		wantAskOnce(t, c, decideCmd(env, c))
	}
}

// 联接指进受保护目录：命令里的目标、open 的 target 过联接解析后的真实路径再比
func TestLinkedTargetsResolvedBeforeCompare(t *testing.T) {
	prot := `D:\work\data`
	cmds := []string{`Get-Content link\x.db`, `Get-Content .\link\x.db`, `Get-ChildItem link -Recurse`, `cd link; Get-Content x.db`}
	for _, c := range cmds {
		env := shellEnv(t, "yolo", "conversation", c, prot)
		env.Real = map[string]string{}
		for _, p := range append(LinkPaths(Specs["powershell"], ruleArgs("command", c), env), ExtraLinkPaths(Specs["powershell"], ruleArgs("command", c), env)...) {
			real := p
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(`D:\work\proj\link`)) { // 假设 link 是指向受保护目录的联接
				real = prot + p[len(`D:\work\proj\link`):]
			}
			env.Real[strings.ToLower(p)] = real
		}
		wantAskOnce(t, c, decideCmd(env, c))
		// 对照：联接没解析（gate 没给 Real）时按字面，不在受保护目录里
		env.Real = nil
		if d := decideCmd(env, c); d.Sticky {
			t.Errorf("没解析联接时不该当受保护：%q → %+v", c, d)
		}
	}
	// 联接指向受保护目录的上级：递归搜它会搜进去
	{
		c := `Get-ChildItem link -Recurse -Filter x.db`
		env := shellEnv(t, "yolo", "conversation", c, prot)
		env.Real = map[string]string{}
		for _, p := range ExtraLinkPaths(Specs["powershell"], ruleArgs("command", c), env) {
			env.Real[strings.ToLower(p)] = `D:\work`
		}
		wantAskOnce(t, c+"（联接指向上级）", decideCmd(env, c))
	}
	// open 的 target
	env := uncEnv("auto", "conversation")
	env.Workdir, env.ProtectedDirs = `D:\work\proj`, []string{prot}
	args := ruleArgs("target", `link\x.db`)
	env.Real = map[string]string{}
	for _, p := range ExtraLinkPaths(Specs["open"], args, env) {
		env.Real[strings.ToLower(p)] = prot + `\x.db`
	}
	if len(env.Real) == 0 {
		t.Fatal("ExtraLinkPaths 该带上 open 的 target")
	}
	wantAskOnce(t, "open 联接", Decide("open", Specs["open"], args, env))
}

// 本机的各种叫法也能还原（拒绝规则用）
func TestLocalHostAliasesInDenyRule(t *testing.T) {
	host, _ := os.Hostname()
	deny := mustRules(t, [2]string{"Read(D:/secret/**)", "deny"})
	for _, h := range []string{"localhost", "LOCALHOST", "localhost.", "127.0.0.1", "127.0.0.2", "127.1", "127.255.255.254", "::1.ipv6-literal.net", host} {
		for _, sep := range []string{`\`, `/`} {
			p := sep + sep + h + sep + "D$" + sep + "secret" + sep + "a.txt"
			env := Env{Mode: "yolo", Workdir: `D:\work`, Declared: true, Audience: ownerConsole, Rules: deny, Conversation: "c"}
			if d := Decide("read", Specs["read"], ruleArgs("path", p), env); d.Kind != "deny" {
				t.Errorf("%q 该被拒绝规则拒绝：%+v", p, d)
			}
		}
	}
	for _, h := range []string{"fileserver", "10.0.0.1", "1270.0.0.1", "localhostx"} {
		p := `\\` + h + `\D$\secret\a.txt`
		env := Env{Mode: "yolo", Workdir: `D:\work`, Declared: true, Audience: ownerConsole, Rules: deny, Conversation: "c"}
		if d := Decide("read", Specs["read"], ruleArgs("path", p), env); d.Kind == "deny" {
			t.Errorf("别的主机 %q 不该按本机还原：%+v", p, d)
		}
	}
}
