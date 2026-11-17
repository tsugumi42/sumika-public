package linkcard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 设置里给了路径就只用设置的；没给走公共字体服务的找法，文渊圆体排在前面（exe 旁、开发目录都有候选）。
func TestPrimaryCandidates(t *testing.T) {
	t.Setenv("SUMIKA_FONT", "")
	set := []string{filepath.Join(t.TempDir(), "a.ttf")}
	if got := primaryCandidates(Options{FontPaths: set}); !slices.Equal(got, set) {
		t.Fatalf("设置给了路径：%v", got)
	}
	got := primaryCandidates(Options{})
	if len(got) < 2 || filepath.Base(got[0]) != "WenYuanRoundedSCVF.ttf" {
		t.Fatalf("默认候选应当以完整版文渊圆体开头：%v", got)
	}
	for _, p := range got {
		if strings.Contains(strings.ToLower(filepath.ToSlash(p)), "/windows/fonts") {
			t.Errorf("文渊圆体不该去系统字体目录找：%s", p)
		}
	}
}

// Maple 只在素材字体目录里找：系统字体目录（WINDIR）和用户另装的字体目录（LOCALAPPDATA）里放了也不看。
func TestFallbackNeverScansSystemFontDirs(t *testing.T) {
	win, local := t.TempDir(), t.TempDir()
	for _, d := range []string{filepath.Join(win, "Fonts"), filepath.Join(local, "Microsoft", "Windows", "Fonts")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "MapleMono-NF-CN-Regular.ttf"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WINDIR", win)
	t.Setenv("LOCALAPPDATA", local)
	got := fallbackCandidates(Options{})
	if len(got) == 0 {
		t.Fatal("默认应当有候选（exe 旁和开发目录）")
	}
	for _, p := range got {
		if strings.HasPrefix(p, win) || strings.HasPrefix(p, local) {
			t.Errorf("Maple 不该去系统字体目录找：%s", p)
		}
		if filepath.Base(p) != "MapleMono-NF-CN-Regular.ttf" {
			t.Errorf("候选应当是常规体：%s", p)
		}
	}
	if got := fallbackCandidates(Options{FallbackFontPaths: []string{}}); len(got) != 0 {
		t.Fatalf("空切片表示不用：%v", got)
	}
}
