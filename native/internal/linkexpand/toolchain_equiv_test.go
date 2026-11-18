package linkexpand

import (
	"os"
	"os/exec"
	"path/filepath"
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
		writeMediaTool(t, filepath.Join(dir, "ffmpeg.exe"))
		writeMediaTool(t, filepath.Join(dir, "ffprobe.exe"))
	}
	t.Setenv("PATH", pathDir)

	t.Run("自带优先", func(t *testing.T) {
		env := mediaToolEnv()
		env.ResourceRoot = root
		toolchain.Refresh(env)
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			got, err := mediaTool(name)
			if err != nil || !sameMediaPath(got, filepath.Join(vendorDir, name+".exe")) {
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
		if err != nil || !sameMediaPath(got, filepath.Join(pathDir, "ffmpeg.exe")) {
			t.Errorf("mediaTool = %q, %v，要 PATH 里的", got, err)
		}
	})
}
