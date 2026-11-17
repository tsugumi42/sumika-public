package userhooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/taskrun"
)

// 这些用真 PowerShell 跑（和后台同一套 tools.RunShellCommand）：退出码被 pwsh -Command 压成 0 或 1 的坑只有真跑才看得见
func realRun(t *testing.T, ctx context.Context, h Handler, env []string, stdin string) outcome {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("钩子的命令层目前只在 Windows 上验收")
	}
	if h.Timeout == 0 {
		h.Timeout = 30 * time.Second
	}
	p := New(Options{})
	return p.runOne(ctx, EventPostToolUse, h, t.TempDir(), env, []byte(stdin))
}

func TestRunOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		class     string
		result    string
		code      int
		stdoutHas string
		stderrHas string
	}{
		{"成功有输出", `Write-Output 'hello'`, "", taskrun.OutcomeOK, 0, "hello", ""},
		{"成功没输出", `$null = 1`, "", taskrun.OutcomeOK, 0, "", ""},
		{"exit 2 是检查没过", "[Console]::Error.WriteLine('bad'); exit 2", taskrun.ClassCheckFailed, taskrun.OutcomeFailed, 2, "", "bad"},
		{"外部程序退出码 2 也传得回来", "cmd /c exit 2", taskrun.ClassCheckFailed, taskrun.OutcomeFailed, 2, "", ""},
		{"外部程序退出码 3", "cmd /c exit 3", taskrun.ClassExitNonzero, taskrun.OutcomeFailed, 3, "", ""},
		{"命令本身抛错是 1", "throw 'boom'", taskrun.ClassExitNonzero, taskrun.OutcomeFailed, 1, "", "boom"},
		{"最后一条是注释也不影响", "exit 3\n# 末尾注释", taskrun.ClassExitNonzero, taskrun.OutcomeFailed, 3, "", ""},
		{"gofmt -l 式：退出 0 有输出", "Write-Output 'a.go'; exit 0", "", taskrun.OutcomeOK, 0, "a.go", ""},
		{"找不到的程序", "this-program-does-not-exist-xyz", taskrun.ClassExitNonzero, taskrun.OutcomeFailed, 1, "", "this-program-does-not-exist-xyz"},
	}
	for _, c := range cases {
		o := realRun(t, context.Background(), Handler{Command: c.command, Feedback: "always"}, nil, "")
		if o.class != c.class || o.result != c.result || o.code != c.code {
			t.Errorf("%s：class=%q result=%q code=%d（stdout=%q stderr=%q failure=%q）", c.name, o.class, o.result, o.code, o.stdout, o.stderr, o.failure)
		}
		if !strings.Contains(o.stdout, c.stdoutHas) || !strings.Contains(o.stderr, c.stderrHas) {
			t.Errorf("%s：stdout=%q stderr=%q", c.name, o.stdout, o.stderr)
		}
	}
}

func TestRunStdinEnvAndJSON(t *testing.T) {
	cmd := `$in = [Console]::In.ReadToEnd() | ConvertFrom-Json; Write-Output ("tool=" + $in.tool_name + " file=" + $env:SUMIKA_FILE_PATH + " ev=" + $env:SUMIKA_HOOK_EVENT)`
	o := realRun(t, context.Background(), Handler{Command: cmd}, []string{"SUMIKA_FILE_PATH=C:\\x y\\a.go", "SUMIKA_HOOK_EVENT=PostToolUse"}, `{"tool_name":"edit"}`)
	if want := `tool=edit file=C:\x y\a.go ev=PostToolUse`; o.stdout != want {
		t.Errorf("%q（%q）", o.stdout, o.stderr)
	}
	// JSON 输出：完整、以 { 开头才算
	o = realRun(t, context.Background(), Handler{Command: `Write-Output '{"decision":"block","reason":"vet 没过"}'`}, nil, "")
	if o.json == nil || o.class != taskrun.ClassCheckFailed || o.json.Reason != "vet 没过" {
		t.Errorf("%+v", o)
	}
	o = realRun(t, context.Background(), Handler{Command: `Write-Output '{"decision":"block"}'`}, nil, "")
	if o.class != taskrun.ClassInvalidOutput {
		t.Errorf("block 没给 reason 要记 invalid_output：%+v", o)
	}
	o = realRun(t, context.Background(), Handler{Command: `Write-Output 'not {json}'`}, nil, "")
	if o.json != nil || o.class != "" {
		t.Errorf("%+v", o)
	}
}

func TestRunTimeoutKillsTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived.txt")
	// 起一个子 PowerShell，睡过超时再写文件：整棵树要一起被杀，文件不该出现
	cmd := `Start-Process -NoNewWindow -FilePath powershell -ArgumentList '-NoProfile','-Command','Start-Sleep 6; Set-Content -Path "` + marker + `" -Value x'; Start-Sleep 60`
	start := time.Now()
	o := realRun(t, context.Background(), Handler{Command: cmd, Timeout: time.Second}, nil, "")
	if o.class != taskrun.ClassTimeout || o.result != taskrun.OutcomeTimeout || o.failure == "" {
		t.Fatalf("%+v", o)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("超时后拖了 %v 才返回", d)
	}
	time.Sleep(8 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("超时后子进程还活着")
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	o := realRun(t, ctx, Handler{Command: `Start-Sleep 60`}, nil, "")
	if o.class != classCancelled || o.result != taskrun.OutcomeCancelled {
		t.Fatalf("%+v", o)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("被停下后没及时返回")
	}
}

func TestOutputLimit(t *testing.T) {
	// 4 MiB 输出：内存里只留头 1/4、尾 3/4 的 256 KiB，不是 JSON，给模型时再截
	o := realRun(t, context.Background(), Handler{Command: `[Console]::Out.Write(('x' * 4194304)); [Console]::Out.Write('END')`}, nil, "")
	if len(o.stdout) > captureBytes+200 || !strings.Contains(o.stdout, "省略") || !strings.HasSuffix(o.stdout, "END") {
		t.Fatalf("长度 %d，头 %q", len(o.stdout), o.stdout[:min(40, len(o.stdout))])
	}
	if o.json != nil {
		t.Fatal("被截过的不能当 JSON")
	}
	p := New(Options{MaxOutputBytes: 1024})
	note := p.toolNote(outcome{handler: Handler{Command: "x", Feedback: "always"}, stdout: o.stdout})
	if len(note) > 1024+300 || !strings.Contains(note, "省略") {
		t.Fatalf("尾注 %d 字节", len(note))
	}
	// 截断不切断汉字
	text := strings.Repeat("检", 2000)
	if got := clipMiddle(text, 1000); !strings.Contains(got, "省略") || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("%q", got[:60])
	}
}

func TestBashShellMissingIsSpawnFailure(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip()
	}
	// 没有 Git Bash 的机器上要记 spawn_failed，而不是让回合出错；有 Git Bash 就跑通
	o := realRun(t, context.Background(), Handler{Shell: "bash", Command: "echo hi"}, nil, "")
	switch o.class {
	case "":
		if o.stdout != "hi" {
			t.Errorf("%+v", o)
		}
	case taskrun.ClassSpawnFailed:
	default:
		t.Errorf("%+v", o)
	}
}
