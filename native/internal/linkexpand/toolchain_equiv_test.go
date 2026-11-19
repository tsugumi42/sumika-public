package linkexpand

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/toolchain"
)

func writeMediaTool(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mediaToolEnv() toolchain.Env {
	return toolchain.Env{
		LookPath: exec.LookPath,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// fakeExe 是测试里造假程序文件的名字：和 toolchain 登记表找程序的规则一致，Windows 带 .exe，别的系统不带
func fakeExe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func sameMediaPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// 验收第 1、2 条：链接展开向登记表要 ffmpeg/ffprobe；自带优先，qq.media.* 配置其次，PATH 兜底。
func TestMediaToolUsesRegistry(t *testing.T) {
	restore := func() { toolchain.Refresh(toolchain.Env{}) }
	t.Cleanup(restore) // 先注册：PATH 恢复以后才重查登记表，后面的用例还看真实环境
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	root := t.TempDir()
	pathDir := filepath.Join(root, "path")
	vendorDir := filepath.Join(root, "vendor", "bin")
	for _, dir := range []string{pathDir, vendorDir} {
		writeMediaTool(t, filepath.Join(dir, fakeExe("ffmpeg")))
		writeMediaTool(t, filepath.Join(dir, fakeExe("ffprobe")))
	}
	t.Setenv("PATH", pathDir)

	t.Run("自带优先", func(t *testing.T) {
		env := mediaToolEnv()
		env.ResourceRoot = root
		toolchain.Refresh(env)
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			got, err := mediaTool(name)
			if err != nil || !sameMediaPath(got, filepath.Join(vendorDir, fakeExe(name))) {
				t.Errorf("%s：mediaTool = %q, %v，要 vendor/bin", name, got, err)
			}
		}
	})

	t.Run("配置优先于 PATH", func(t *testing.T) {
		configured := filepath.Join(root, "config", "custom-ffmpeg.exe")
		writeMediaTool(t, configured)
		env := mediaToolEnv()
		env.FFmpeg = configured
		toolchain.Refresh(env)
		got, err := mediaTool("ffmpeg")
		if err != nil || !sameMediaPath(got, configured) {
			t.Errorf("mediaTool = %q, %v，要配置里的 %s", got, err, configured)
		}
	})

	t.Run("PATH 兜底", func(t *testing.T) {
		toolchain.Refresh(mediaToolEnv())
		got, err := mediaTool("ffmpeg")
		if err != nil || !sameMediaPath(got, filepath.Join(pathDir, fakeExe("ffmpeg"))) {
			t.Errorf("mediaTool = %q, %v，要 PATH 里的", got, err)
		}
	})
}
