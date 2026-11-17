package mediafetch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/toolchain"
)

func writeVideoTool(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func videoEnv() toolchain.Env {
	return toolchain.Env{
		LookPath: exec.LookPath,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// 收视频的调用方拿到的是登记表给的那一个：自带优先、配置其次、PATH 兜底。
func TestFindVideoToolsUsesRegistry(t *testing.T) {
	t.Cleanup(func() { toolchain.Refresh(toolchain.Env{}) }) // 先注册：PATH 还原以后再重查
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	root := t.TempDir()
	pathDir := filepath.Join(root, "path")
	vendorDir := filepath.Join(root, "vendor", "bin")
	writeVideoTool(t, filepath.Join(pathDir, "ffmpeg.exe"))
	writeVideoTool(t, filepath.Join(pathDir, "ffprobe.exe"))
	t.Setenv("PATH", pathDir)

	check := func(t *testing.T, env toolchain.Env, wantFFmpeg, wantFFprobe string) {
		t.Helper()
		toolchain.Refresh(env)
		got := findVideoTools()
		ffmpeg, ok := toolchain.Lookup("ffmpeg")
		if !ok || !strings.EqualFold(filepath.Clean(got.ffmpeg), filepath.Clean(ffmpeg.Path)) {
			t.Errorf("ffmpeg：findVideoTools = %q，登记表 = %+v", got.ffmpeg, ffmpeg)
		}
		ffprobe, ok := toolchain.Lookup("ffprobe")
		if !ok || !strings.EqualFold(filepath.Clean(got.ffprobe), filepath.Clean(ffprobe.Path)) {
			t.Errorf("ffprobe：findVideoTools = %q，登记表 = %+v", got.ffprobe, ffprobe)
		}
		if !strings.EqualFold(filepath.Clean(got.ffmpeg), filepath.Clean(wantFFmpeg)) ||
			!strings.EqualFold(filepath.Clean(got.ffprobe), filepath.Clean(wantFFprobe)) {
			t.Errorf("拿到 %+v，要 %s、%s", got, wantFFmpeg, wantFFprobe)
		}
	}

	t.Run("自带优先", func(t *testing.T) {
		writeVideoTool(t, filepath.Join(vendorDir, "ffmpeg.exe"))
		writeVideoTool(t, filepath.Join(vendorDir, "ffprobe.exe"))
		env := videoEnv()
		env.ResourceRoot = root
		check(t, env, filepath.Join(vendorDir, "ffmpeg.exe"), filepath.Join(vendorDir, "ffprobe.exe"))
	})

	t.Run("配置优先于 PATH", func(t *testing.T) {
		configured := filepath.Join(root, "config", "custom-ffmpeg.exe")
		writeVideoTool(t, configured)
		env := videoEnv()
		env.FFmpeg = configured
		check(t, env, configured, filepath.Join(pathDir, "ffprobe.exe"))
	})

	t.Run("只有 PATH", func(t *testing.T) {
		check(t, videoEnv(), filepath.Join(pathDir, "ffmpeg.exe"), filepath.Join(pathDir, "ffprobe.exe"))
	})

	t.Run("都没有", func(t *testing.T) {
		t.Setenv("PATH", filepath.Join(root, "empty"))
		toolchain.Refresh(toolchain.Env{})
		if got := findVideoTools(); got.ffmpeg != "" || got.ffprobe != "" {
			t.Errorf("没有程序却找到 %+v", got)
		}
	})
}
