package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	chain "sumika-native/internal/toolchain"
)

// restoreToolchainAfterTest 在测试结束、PATH 等环境恢复以后重查一次登记表，后面的用例还看真实环境。
// 必须在 t.Setenv 之前调用：Cleanup 后进先出，先注册的先跑。
func restoreToolchainAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { chain.Refresh(chain.Env{}) })
}

// 验收第 1 条：PATH 里没有 pwsh 时，状态是“不可用”，并且带最近一次起不来的原因（不是静默降级）。
func TestAnalyzerStatusWithoutPwsh(t *testing.T) {
	restoreToolchainAfterTest(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	chain.Refresh(chain.Env{})
	if p, err := exec.LookPath("pwsh"); err == nil {
		t.Fatalf("测试环境里还能找到 pwsh：%s", p)
	}
	a := NewAnalyzer()
	defer a.Close()
	st := a.Status()
	if st.Available {
		t.Fatalf("PATH 里没有 pwsh，却说可用：%+v", st)
	}
	if st.LastErr == "" || !strings.Contains(st.LastErr, "pwsh") {
		t.Fatalf("不可用时要写原因：%+v", st)
	}
	if st.Path != "" {
		t.Fatalf("没找到时不该有路径：%+v", st)
	}
}

// Status 不启动进程：没预热过也能给出状态，PID 仍是 0。
func TestAnalyzerStatusDoesNotStart(t *testing.T) {
	restoreToolchainAfterTest(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	chain.Refresh(chain.Env{})
	a := NewAnalyzer()
	defer a.Close()
	if st := a.Status(); st.Available {
		t.Fatalf("空 PATH 不该可用：%+v", st)
	}
	if pid := a.PID(); pid != 0 {
		t.Fatalf("Status 不该启动进程，PID = %d", pid)
	}
}

// 验收第 3 条：PATH 里没有 pwsh、Program Files 里有，命令分析器可用（登记表查系统安装目录，这是 P2-5 的有意变化）。
func TestAnalyzerUsesProgramFilesPwsh(t *testing.T) {
	restoreToolchainAfterTest(t)
	root := t.TempDir()
	programFiles := filepath.Join(root, "pf")
	pwsh := filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")
	if err := os.MkdirAll(filepath.Dir(pwsh), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pwsh, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "empty-path"))
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	t.Setenv("ProgramFiles", programFiles)
	chain.Refresh(chain.Env{ProgramFiles: programFiles})

	a := NewAnalyzer()
	defer a.Close()
	st := a.Status()
	if !st.Available || !strings.EqualFold(filepath.Clean(st.Path), filepath.Clean(pwsh)) {
		t.Fatalf("PATH 里没有 pwsh、Program Files 有时应可用：%+v", st)
	}
}
