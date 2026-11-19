package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, twemojiMarker), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 明说的目录只认它自己：有效就用，无效就报“找不到”并列出试过的，不偷偷退到别处。
func TestTwemojiExplicit(t *testing.T) {
	good, bad := t.TempDir(), t.TempDir()
	writeMarker(t, good)
	if d, tried := Twemoji(good); d != good || tried != nil {
		t.Fatalf("有效目录：%q %v", d, tried)
	}
	d, tried := Twemoji(bad)
	if d != "" || len(tried) != 1 || tried[0] != bad {
		t.Fatalf("无效目录应当返回空串并列出试过的：%q %v", d, tried)
	}
}

// 环境变量优先于程序旁和开发目录；它指的目录无效时继续往后找，试过的路径里要有它。
func TestTwemojiEnvOverride(t *testing.T) {
	good := t.TempDir()
	writeMarker(t, good)
	t.Setenv("CHATUI_TWEMOJI", good)
	if d, _ := Twemoji(""); d != good {
		t.Fatalf("环境变量没生效：%q", d)
	}
	bad := filepath.Join(t.TempDir(), "没有这个目录")
	t.Setenv("CHATUI_TWEMOJI", bad)
	d, tried := Twemoji("")
	switch {
	case d == "": // 程序旁和开发目录也都没有：试过的路径里先有环境变量那个
		if len(tried) == 0 || tried[0] != bad {
			t.Fatalf("环境变量指的目录无效时，试过的路径里应当先有它：%v", tried)
		}
	case d == bad:
		t.Fatalf("无效的目录不能当结果：%q", d)
	}
	// 否则是继续往后找、找到了程序旁或开发目录里有效的那个，也对
}

// 开发目录是 native/<name>，并且在 worktree 里再带上主工作区的（靠 .git 文件）。
func TestDevRootsEndWithNative(t *testing.T) {
	roots := devRoots()
	if len(roots) == 0 {
		t.Skip("取不到源码位置（-trimpath 构建）")
	}
	for _, r := range roots {
		if filepath.Base(r) != "native" {
			t.Errorf("开发根目录应当是 native：%s", r)
		}
	}
	for _, d := range devDirs("twemoji") {
		if filepath.Base(d) != "twemoji" {
			t.Errorf("%s", d)
		}
	}
}
