package approval

// 网络共享、设备路径写法指向数据目录：从门进去（含放行规则、三种模式）也要问，不弹"记住"之类的选项。

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

func TestGateAsksForUNCOfProtectedDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("管理共享写法只在 Windows 上有意义")
	}
	g, work := ruleGate(t, [2]string{"Read(*)", "allow"}, [2]string{"Write(*)", "allow"})
	file := filepath.Join(g.ProtectedDirs[0], "config", "sumika.yml")
	drive, rest := file[:2], file[3:] // C:  和  Users\…
	shapes := []string{
		`\\localhost\` + drive[:1] + `$\` + rest,
		strings.ReplaceAll(`\\localhost\`+drive[:1]+`$\`+rest, `\`, `/`),
		`\\127.0.0.1\` + strings.ToLower(drive[:1]) + `$\` + rest,
		`\\?\` + file,
		`\\.\` + file,
		`\\?\UNC\localhost\` + drive[:1] + `$\` + rest,
	}
	for _, mode := range []string{"manual", "auto", "yolo"} {
		g.Mode = mode
		for _, tool := range []string{"read", "write"} {
			for _, p := range shapes {
				args := `{"path":` + quote(p) + `,"content":"x"}`
				cl, asked := ruleCheck(t, g, work, "console:suzu", tool, args)
				if asked != 1 {
					t.Errorf("%s %s %q：该问一次，问了 %d 次，结果 %+v", mode, tool, p, asked, cl)
				}
			}
		}
	}
}

// 网络共享、设备路径、驱动器相对路径：审批卡出来前不许对目标做任何文件访问（Stat、打开、读内容），指纹按“未知”算。
func TestOpaquePathsAreNeverTouched(t *testing.T) {
	var touched []string
	oldStat, oldOpen, oldRead := fileStat, fileOpen, fileReadFile
	fileStat = func(p string) (os.FileInfo, error) { touched = append(touched, "stat "+p); return oldStat(p) }
	fileOpen = func(p string) (io.ReadCloser, error) { touched = append(touched, "open "+p); return oldOpen(p) }
	fileReadFile = func(p string) ([]byte, error) { touched = append(touched, "read "+p); return oldRead(p) }
	t.Cleanup(func() { fileStat, fileOpen, fileReadFile = oldStat, oldOpen, oldRead })

	g, work := ruleGate(t)
	shapes := []string{
		`\\localhost\C$\Users\x\sdata\c.yml`, `//localhost/C$/Users/x/sdata/c.yml`, `\\fileserver\share\a.txt`, `file://fileserver/share/a.txt`,
		`\\?\C:\Users\x\sdata\c.yml`, `\\.\C:\Users\x\sdata\c.yml`, `\\?\UNC\localhost\C$\x`, `C:sdata\c.yml`, `D:foo`, `@\\fileserver\share\a.txt`,
	}
	const extra = `,"content":"x","edits":[{"oldText":"a","newText":"b"}]}`
	for _, tool := range []string{"write", "edit"} {
		for _, p := range shapes {
			args, err := jsjson.Parse(`{"path":` + quote(p) + extra)
			if err != nil {
				t.Fatal(err)
			}
			c := Call{ID: "c1", Name: tool, Args: args}
			touched = nil
			g.Describe(work, "suzu", "c1", tool, args)
			a := g.targetPrint(policy.Specs[tool], c, policy.Env{Workdir: work}, work)()
			b := g.targetPrint(policy.Specs[tool], c, policy.Env{Workdir: work}, work)()
			if a != b {
				t.Errorf("%s %q：未知目标的指纹要稳定", tool, p)
			}
			if got := RealOf(p); got != p {
				t.Errorf("RealOf(%q) 不该解析：%q", p, got)
			}
			if len(touched) != 0 {
				t.Errorf("%s %q：不该对目标做文件访问：%v", tool, p, touched)
			}
			// 整条门路径
			for _, mode := range []string{"manual", "auto", "yolo"} {
				g.Mode = mode
				touched = nil
				_, asked := ruleCheck(t, g, work, "console:suzu", tool, `{"path":`+quote(p)+extra)
				if asked != 1 || len(touched) != 0 {
					t.Errorf("%s %s %q：该问一次且不碰文件，问了 %d 次，碰了 %v", mode, tool, p, asked, touched)
				}
			}
		}
	}
	// 对照：本地文件照旧读内容算指纹，预览照旧能读到旧内容
	local := filepath.Join(work, "a.txt")
	if err := os.WriteFile(local, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := jsjson.Parse(`{"path":` + quote(local) + `,"content":"new"}`)
	touched = nil
	r := g.Describe(work, "suzu", "c1", "write", args)
	g.targetPrint(policy.Specs["write"], Call{ID: "c1", Name: "write", Args: args}, policy.Env{Workdir: work}, work)()
	if len(touched) == 0 || r.Title != "覆盖文件" {
		t.Errorf("本地文件该照旧读：%v %q", touched, r.Title)
	}
}

// 计数桩：记下所有走 fileStat / fileOpen / fileReadFile 的访问
func countFileAccess(t *testing.T) *[]string {
	t.Helper()
	var touched []string
	oldStat, oldOpen, oldRead := fileStat, fileOpen, fileReadFile
	fileStat = func(p string) (os.FileInfo, error) { touched = append(touched, "stat "+p); return oldStat(p) }
	fileOpen = func(p string) (io.ReadCloser, error) { touched = append(touched, "open "+p); return oldOpen(p) }
	fileReadFile = func(p string) ([]byte, error) { touched = append(touched, "read "+p); return oldRead(p) }
	t.Cleanup(func() { fileStat, fileOpen, fileReadFile = oldStat, oldOpen, oldRead })
	return &touched
}

// 真的分析器拆出来的命令：规整后的文字比对、cd 之后的相对目标、递归搜上级、看不清的路径，在 auto / yolo 下（带一条 Bash(*) 放行规则）也要问
func TestPowershellProtectedFormsThroughAnalyzer(t *testing.T) {
	g, work := ruleGate(t, [2]string{"Bash(*)", "allow"})
	if g.Analyzer.Analyze("Get-Date", 30*time.Second) == nil {
		t.Skip("这台机器起不来拆命令的 pwsh")
	}
	data := g.ProtectedDirs[0]
	parent, base := filepath.Dir(data), filepath.Base(data)
	f := filepath.Join(data, "memo", "x.txt")
	dbl := strings.ReplaceAll(f, `\`, `\\`)
	fwd := strings.ReplaceAll(f, `\`, `/`)
	cmds := []string{
		"Get-Content '" + dbl + "'",
		"Get-Content ('" + dbl + "')",
		"Get-Content @('" + dbl + "')",
		"$p = '" + dbl + "'; Get-Content $p",
		"curl.exe -s file:///" + strings.Replace(fwd, "/"+base+"/", "/./"+base+"/", 1),
		"Get-Content 'FileSystem::" + strings.Replace(f, `\`+base+`\`, `\.\`+base+`\`, 1) + "'",
		"Get-Content 'Microsoft.PowerShell.Core\\FileSystem::" + f + "'",
		"Set-Location '" + parent + "'; Get-Content .\\" + base + "\\memo\\x.txt",
		"cd '" + parent + "'; cat " + base + "\\memo\\x.txt",
		"Get-Content '" + parent + "\\" + base[:len(base)-1] + "?\\memo\\x.txt'",
		"Get-Content '" + parent + "\\[" + base[:1] + "]" + base[1:] + "\\memo\\x.txt'",
		"Get-ChildItem '" + parent + "' -Recurse -Filter x.txt",
		"Get-ChildItem '" + parent + "' -r -Filter x.txt",
		"Get-Content $p",
	}
	for _, mode := range []string{"auto", "yolo"} {
		g.Mode = mode
		for _, c := range cmds {
			cl, asked := ruleCheck(t, g, work, "console:suzu", "powershell", `{"command":`+quote(c)+`}`)
			if asked != 1 {
				t.Errorf("%s 该问一次，问了 %d 次（by=%q）：%s", mode, asked, cl.By, strings.ReplaceAll(c, parent, "<P>"))
			}
		}
	}
	// 不该多问：工作目录里的普通只读命令
	g.Mode = "auto"
	for _, c := range []string{"Get-ChildItem '" + work + "'", "Get-Content '" + filepath.Join(work, "a.txt") + "'", "Get-Date"} {
		if cl, asked := ruleCheck(t, g, work, "console:suzu", "powershell", `{"command":`+quote(c)+`}`); asked != 0 {
			t.Errorf("不该问：%s（%+v）", c, cl)
		}
	}
}

// 卡片上写的位置就是工具实际操作的位置（ToolPath）：~、@、file:///、/c/…
func TestDescribeSubjectIsToolPath(t *testing.T) {
	g, _ := ruleGate(t)
	work := `D:\work\proj`
	t.Setenv("USERPROFILE", `C:\Users\x`)
	t.Setenv("HOME", `C:\Users\x`)
	for _, tool := range []string{"read", "write", "edit", "ls", "grep"} {
		for _, p := range []string{`~/.ssh/id_rsa`, `~\.ssh\id_rsa`, `@data\x.db`, `file:///C:/Users/x/.ssh/id_rsa`, `/c/Users/x/.ssh/id_rsa`, `data\x.db`, `D:\work\proj\data\..\data\x.db`, "\u00a0data\\x.db"} {
			args, err := jsjson.Parse(`{"path":` + quote(p) + `,"content":"x","pattern":"k","edits":[{"oldText":"a","newText":"b"}]}`)
			if err != nil {
				t.Fatal(err)
			}
			r := g.Describe(work, "c", "id", tool, args)
			want := policy.ToolPath(p, work)
			if r.Subject != want && !strings.HasPrefix(r.Subject, want+"\n") {
				t.Errorf("%s %q：卡片写的是 %q，工具实际操作 %q", tool, p, r.Subject, want)
			}
		}
	}
}

// 工作目录本身在网络共享上：write / edit 的相对路径解出来也是网络共享，卡片阶段同样不碰；hashFile 自己也兜一道
func TestUNCWorkdirNeverTouched(t *testing.T) {
	touched := countFileAccess(t)
	g, _ := ruleGate(t)
	work := `\\fileserver\share\proj`
	const extra = `,"content":"x","edits":[{"oldText":"a","newText":"b"}]}`
	for _, tool := range []string{"write", "edit"} {
		for _, p := range []string{"a.txt", `sub\a.txt`, `..\a.txt`, `.\a.txt`, `\other\a.txt`, "@a.txt"} {
			args, err := jsjson.Parse(`{"path":` + quote(p) + extra)
			if err != nil {
				t.Fatal(err)
			}
			*touched = nil
			g.Describe(work, "suzu", "c1", tool, args)
			g.targetPrint(policy.Specs[tool], Call{ID: "c1", Name: tool, Args: args}, policy.Env{Workdir: work}, work)()
			if len(*touched) != 0 {
				t.Errorf("%s %q：工作目录在共享上，卡片阶段不该碰文件：%v", tool, p, *touched)
			}
			for _, mode := range []string{"manual", "auto", "yolo"} {
				g.Mode = mode
				*touched = nil
				ruleCheck(t, g, work, "console:suzu", tool, `{"path":`+quote(p)+extra)
				if len(*touched) != 0 {
					t.Errorf("%s %s %q：整条门也不该碰文件：%v", mode, tool, p, *touched)
				}
			}
		}
	}
	*touched = nil
	h := sha256.New()
	hashFile(h, `\\fileserver\share\a.txt`)
	hashFile(h, `C:foo`)
	if len(*touched) != 0 {
		t.Errorf("hashFile 不该碰不透明路径：%v", *touched)
	}
}

// powershell 写、删、运行类命令的网络共享目标：问主人之前（算“这个对话”记哪个目录时）也不许 Stat 过去
func TestPowershellOpaqueTargetsNeverStatted(t *testing.T) {
	g, work := ruleGate(t)
	if g.Analyzer.Analyze("Get-Date", 30*time.Second) == nil {
		t.Skip("这台机器起不来拆命令的 pwsh")
	}
	touched := countFileAccess(t)
	srv := `\\fileserver\share`
	cmds := []string{
		`Remove-Item ` + srv + `\a.txt`,
		`Remove-Item -Recurse -Force ` + srv + `\dir`,
		`Set-Content ` + srv + `\a.txt -Value x`,
		`Out-File ` + srv + `\a.txt`,
		`"x" > ` + srv + `\a.txt`,
		`& ` + srv + `\x.exe`,
		`New-Item -ItemType Directory ` + srv + `\d`,
		`Copy-Item a.txt ` + srv + `\a.txt`,
		`Remove-Item C:sdata\a.txt`,
		`Set-Content '\\?\C:\Users\x\sdata\c.yml' x`,
	}
	for _, c := range cmds {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			g.Mode = mode
			*touched = nil
			_, asked := ruleCheck(t, g, work, "console:suzu", "powershell", `{"command":`+quote(c)+`}`)
			if asked != 1 || len(*touched) != 0 {
				t.Errorf("%s %q：该问一次且不碰目标，问了 %d 次，碰了 %v", mode, c, asked, *touched)
			}
		}
	}
	*touched = nil
	for _, p := range []string{srv + `\a.txt`, `//fileserver/share/a`, `\\?\C:\x`, `C:foo`} {
		for _, selfOnly := range []bool{false, true} {
			if got := GrantDirOf(p, selfOnly); got != p {
				t.Errorf("GrantDirOf(%q) 原样返回，得到 %q", p, got)
			}
		}
	}
	if len(*touched) != 0 {
		t.Errorf("GrantDirOf 不该 Stat：%v", *touched)
	}
}
