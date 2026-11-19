package policy

import (
	"testing"

	"sumika-native/internal/jsjson"
)

func argsOf(t *testing.T, raw string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 项目级用户钩子的配置在 .sumika 里：写它等于拿到一次不审批的执行，任何模式、任何授权都要每次问
func TestHookConfigWriteAlwaysAsks(t *testing.T) {
	work := `D:\work\proj`
	console := Audience{Channel: "console", PrivateOk: true}
	cases := []struct {
		name  string
		tool  string
		args  string
		shell *Verdict
	}{
		{"edit 相对路径", "edit", `{"path":".sumika/hooks.json"}`, nil},
		{"write 绝对路径反斜杠", "write", `{"path":"D:\\work\\proj\\.sumika\\hooks.json"}`, nil},
		{"write 大小写", "write", `{"path":".SUMIKA\\hooks.json"}`, nil},
		{"write 带尾点", "write", `{"path":".sumika.\\hooks.json"}`, nil},
		{"write 上级写 ..", "write", `{"path":"sub\\..\\.sumika\\hooks.json"}`, nil},
		{"powershell 重定向", "powershell", `{"command":"'x' > .sumika\\hooks.json"}`, &Verdict{Changes: []string{"write"}, Targets: []string{`.sumika\hooks.json`}, FileTargets: []string{`.sumika\hooks.json`}}},
		{"powershell 看不清目标", "powershell", `{"command":"Set-Content -Path \".sumika/hooks.json\" -Value $x"}`, &Verdict{Opaque: true}},
		{"powershell 拆不开", "powershell", `{"command":"iex 'sc .sumika/hooks.json 1'"}`, nil},
	}
	for _, c := range cases {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, grant := range []string{"", "conversation", "always"} {
				env := Env{Mode: mode, Workdir: work, Declared: true, Granted: grant, Audience: console, Shell: c.shell, ShellFailed: c.shell == nil && c.tool == "powershell"}
				d := Decide(c.tool, Specs[c.tool], argsOf(t, c.args), env)
				if d.Kind != "ask" || !d.Sticky || d.Limit != "once" {
					t.Errorf("%s/%s/%s：%+v", c.name, mode, grant, d)
				}
			}
		}
	}
}

// 读不拦；名字像的也不误伤；工作目录里别的文件照旧
func TestHookConfigReadAndNeighboursUntouched(t *testing.T) {
	windowsPathsOnly(t)
	work := `D:\work\proj`
	console := Audience{Channel: "console", PrivateOk: true}
	env := Env{Mode: "yolo", Workdir: work, Declared: true, Audience: console}
	for _, c := range []struct{ tool, args string }{
		{"read", `{"path":".sumika/hooks.json"}`},
		{"write", `{"path":"sumika/hooks.json"}`},
		{"write", `{"path":".sumikax/hooks.json"}`},
		{"write", `{"path":"notes.sumika.txt"}`},
		{"edit", `{"path":"a.go"}`},
	} {
		if d := Decide(c.tool, Specs[c.tool], argsOf(t, c.args), env); d.Kind != "allow" {
			t.Errorf("%s %s：%+v", c.tool, c.args, d)
		}
	}
	ro := env
	ro.Shell = &Verdict{ReadOnly: true, Targets: []string{`.sumika\hooks.json`}}
	args := argsOf(t, `{"command":"Get-Content .sumika\\hooks.json"}`)
	if d := Decide("powershell", Specs["powershell"], args, ro); d.Kind != "allow" {
		t.Errorf("只读命令读 .sumika 不该拦：%+v", d)
	}
	// 同样的命令不是只读（比如接了写）：要问
	rw := env
	rw.Shell = &Verdict{Targets: []string{`.sumika\hooks.json`}, FileTargets: []string{`.sumika\hooks.json`}}
	if d := Decide("powershell", Specs["powershell"], argsOf(t, `{"command":"Remove-Item .sumika\\hooks.json"}`), rw); d.Kind != "ask" {
		t.Errorf("删 .sumika 里的文件要问：%+v", d)
	}
}

func TestHasSumikaSegment(t *testing.T) {
	for p, want := range map[string]bool{
		`.sumika`: true, `a/.sumika/b`: true, `A\.SUMIKA\b`: true, `.sumika.`: true,
		`sumika`: false, `.sumikax`: false, `x.sumika`: false, ``: false, `a/b`: false,
	} {
		if got := hasSumikaSegment(p); got != want {
			t.Errorf("%q：%v", p, got)
		}
	}
}
