package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/toolchain"
)

func writeToolFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func toolEnv(root string) toolchain.Env {
	return toolchain.Env{
		LookPath:     exec.LookPath,
		Exists:       func(p string) bool { _, err := os.Stat(p); return err == nil },
		ResourceRoot: root,
	}
}

func sameToolPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// realToolEnv 是进程自己的环境：测试改了 PATH 以后按它重查登记表，系统程序（taskkill）还要靠这些根。
func realToolEnv() toolchain.Env {
	return toolchain.Env{
		ProgramFiles: os.Getenv("ProgramFiles"),
		ProgramW6432: os.Getenv("ProgramW6432"),
		LocalAppData: os.Getenv("LocalAppData"),
		SystemRoot:   os.Getenv("SystemRoot"),
	}
}

// seedTools 换 PATH 前注册恢复：测试结束、PATH 还原以后让登记表重查真实环境，后面的用例还按真实 PATH 找。
// 登记表有缓存，测试自己改 PATH 就要自己重查（生产由宿主在配置保存时重查）。
func seedTools(t *testing.T, path string, env toolchain.Env) {
	t.Helper()
	t.Cleanup(func() { toolchain.Refresh(realToolEnv()) })
	if path != "" {
		t.Setenv("PATH", path)
	}
	toolchain.Refresh(env)
}

func setToolPath(t *testing.T, path string) {
	t.Helper()
	seedTools(t, path, realToolEnv())
}

// rg/fd 的调用方拿到的就是登记表给的那一个：自带优先，PATH 兜底，fd 认 fdfind。
func TestToolPathUsesRegistry(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")

	t.Run("自带优先", func(t *testing.T) {
		root := t.TempDir()
		pathDir := filepath.Join(root, "path")
		for _, name := range []string{"rg", "fd"} {
			writeToolFile(t, filepath.Join(root, "vendor", "bin", name+".exe"))
			writeToolFile(t, filepath.Join(pathDir, name+".exe"))
		}
		seedTools(t, pathDir, toolEnv(root))
		for _, name := range []string{"rg", "fd"} {
			r, ok := toolchain.Lookup(name)
			if !ok || !sameToolPath(toolPath(name), r.Path) ||
				!sameToolPath(toolPath(name), filepath.Join(root, "vendor", "bin", name+".exe")) {
				t.Errorf("%s：toolPath = %q，登记表 = %+v", name, toolPath(name), r)
			}
		}
	})

	t.Run("PATH 兜底", func(t *testing.T) {
		root := t.TempDir()
		pathDir := filepath.Join(root, "path")
		for _, name := range []string{"rg", "fd"} {
			writeToolFile(t, filepath.Join(pathDir, name+".exe"))
		}
		seedTools(t, pathDir, toolchain.Env{})
		for _, name := range []string{"rg", "fd"} {
			if got := toolPath(name); !sameToolPath(got, filepath.Join(pathDir, name+".exe")) {
				t.Errorf("%s：toolPath = %q", name, got)
			}
		}
	})

	t.Run("fd 装成 fdfind", func(t *testing.T) {
		root := t.TempDir()
		pathDir := filepath.Join(root, "path")
		writeToolFile(t, filepath.Join(pathDir, "fdfind.exe"))
		seedTools(t, pathDir, toolchain.Env{})
		if got := toolPath("fd"); !sameToolPath(got, filepath.Join(pathDir, "fdfind.exe")) {
			t.Errorf("fd：toolPath = %q", got)
		}
	})
}

// 执行工具的 PowerShell 向登记表要；命令分析器那一行（pwsh）现在也看 Program Files 的 7，这是 P2-5 的有意变化。
func TestFindPowerShellUsesRegistry(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	cases := []struct {
		name  string
		setup func(t *testing.T, pathDir, programFiles, systemRoot string)
		found bool
	}{
		{name: "PATH 里的 pwsh", found: true, setup: func(t *testing.T, pathDir, _, _ string) {
			writeToolFile(t, filepath.Join(pathDir, "pwsh.exe"))
		}},
		{name: "Program Files 里的 pwsh 7", found: true, setup: func(t *testing.T, _, programFiles, _ string) {
			writeToolFile(t, filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe"))
		}},
		{name: "PATH 里的 powershell", found: true, setup: func(t *testing.T, pathDir, _, _ string) {
			writeToolFile(t, filepath.Join(pathDir, "powershell.exe"))
		}},
		{name: "System32 里的 5.1", found: true, setup: func(t *testing.T, _, _, systemRoot string) {
			writeToolFile(t, filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
		}},
		{name: "都没有", setup: func(*testing.T, string, string, string) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pathDir := filepath.Join(root, "path")
			programFiles := filepath.Join(root, "pf")
			systemRoot := filepath.Join(root, "sys")
			if err := os.MkdirAll(pathDir, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, pathDir, programFiles, systemRoot)
			env := toolEnv("")
			env.ProgramFiles, env.ProgramW6432 = programFiles, programFiles
			env.LocalAppData, env.SystemRoot = filepath.Join(root, "local"), systemRoot
			seedTools(t, pathDir, env)

			want, err := findPowerShell()
			r, ok := toolchain.Lookup("powershell")
			if !tc.found {
				if err == nil || ok {
					t.Fatalf("没放任何 PowerShell：findPowerShell = %q，登记表 %+v", want, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("findPowerShell：%v", err)
			}
			if !ok || !sameToolPath(want, r.Path) {
				t.Errorf("findPowerShell = %q，登记表 %+v", want, r)
			}
		})
	}
}

// Git Bash 从登记表 git 的安装根推 bash.exe。
func TestFindGitBashUsesRegistryGit(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")

	t.Run("标准安装根", func(t *testing.T) {
		root := t.TempDir()
		programFiles := filepath.Join(root, "pf")
		gitDir := filepath.Join(programFiles, "Git")
		writeToolFile(t, filepath.Join(gitDir, "cmd", "git.exe"))
		writeToolFile(t, filepath.Join(gitDir, "bin", "bash.exe"))
		env := toolEnv("")
		env.ProgramFiles, env.ProgramW6432 = programFiles, filepath.Join(root, "pw6432")
		env.LocalAppData = filepath.Join(root, "local")
		seedTools(t, filepath.Join(root, "empty-path"), env)

		git, ok := toolchain.Lookup("git")
		bash, err := findGitBash()
		if !ok || err != nil {
			t.Fatalf("git = %+v，findGitBash = %q, %v", git, bash, err)
		}
		if filepath.Dir(filepath.Dir(git.Path)) != gitDir || filepath.Dir(filepath.Dir(bash)) != gitDir {
			t.Errorf("git = %q，bash = %q，不在同一个安装根", git.Path, bash)
		}
	})

	t.Run("都没有", func(t *testing.T) {
		root := t.TempDir()
		env := toolEnv("")
		env.ProgramFiles, env.ProgramW6432 = filepath.Join(root, "pf"), filepath.Join(root, "pw6432")
		env.LocalAppData = filepath.Join(root, "local")
		seedTools(t, filepath.Join(root, "empty-path"), env)
		if _, ok := toolchain.Lookup("git"); ok {
			t.Fatalf("没有 Git，登记表却找到")
		}
		if bash, err := findGitBash(); err == nil {
			t.Errorf("没有 Git，findGitBash 却找到 %q", bash)
		}
	})
}
