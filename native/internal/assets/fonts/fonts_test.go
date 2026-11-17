package fonts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 文渊圆体：给了目录就只在这些目录里找；完整版优先、Subset 时子集优先；SUMIKA_FONT 只在没给目录时认
func TestWenYuanOrder(t *testing.T) {
	t.Setenv("SUMIKA_FONT", "")
	a, b := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(a, WenYuanSubset))
	touch(t, filepath.Join(b, WenYuanFull))
	o := Options{Dirs: []string{a, b}}
	want := []string{filepath.Join(a, WenYuanFull), filepath.Join(a, WenYuanSubset), filepath.Join(b, WenYuanFull), filepath.Join(b, WenYuanSubset)}
	if got := WenYuanCandidates(o); !slices.Equal(got, want) {
		t.Fatalf("候选顺序：%v", got)
	}
	if f, ok := WenYuan(o); !ok || f.Path != filepath.Join(a, WenYuanSubset) || f.Source != SourceWenYuan {
		t.Fatalf("应当取 a 目录的子集（a 里没有完整版，目录先后优先于文件名先后）：%+v %v", f, ok)
	}
	o.Subset = true
	if got := WenYuanCandidates(o); got[0] != filepath.Join(a, WenYuanSubset) || got[1] != filepath.Join(a, WenYuanFull) {
		t.Fatalf("Subset 时子集在前：%v", got)
	}
	// SUMIKA_FONT：没给目录时排第一；给了目录时不认
	env := filepath.Join(t.TempDir(), "mine.ttf")
	touch(t, env)
	t.Setenv("SUMIKA_FONT", env)
	if got := WenYuanCandidates(Options{}); got[0] != env {
		t.Fatalf("SUMIKA_FONT 没排第一：%v", got[:1])
	}
	if got := WenYuanCandidates(o); slices.Contains(got, env) {
		t.Fatal("明说了目录就不该认 SUMIKA_FONT")
	}
	if _, ok := WenYuan(Options{Dirs: []string{t.TempDir()}}); ok {
		t.Fatal("空目录不该找到")
	}
}

func TestMapleWeights(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"MapleMono-NF-CN-Regular.ttf", "MapleMono-NF-CN-Bold.ttf", "MapleMono-NF-CN-Italic.ttf", "MapleMono-NF-CN-Light.ttf"} {
		touch(t, filepath.Join(dir, f))
	}
	o := Options{Dirs: []string{dir}}
	for weight, want := range map[int]string{400: "Regular", 700: "Bold", 300: "Light", 500: "Regular", 600: "Bold", 900: "Bold", 100: "Light"} {
		f, ok := Maple(o, weight)
		if !ok || filepath.Base(f.Path) != "MapleMono-NF-CN-"+want+".ttf" {
			t.Errorf("字重 %d：%+v %v，应取 %s", weight, f, ok, want)
		}
	}
	// 一样近取细的：500 离 400（Regular）和 600（没有）……这里 500 到 Regular 100、到 Bold 200
	if f, _ := Maple(o, 400); f.Weight != 400 || f.Source != SourceMaple {
		t.Fatalf("常规体：%+v", f)
	}
	// 斜体不当字重取（Italic 文件不在表里）
	only := t.TempDir()
	touch(t, filepath.Join(only, "MapleMono-NF-CN-Italic.ttf"))
	if _, ok := Maple(Options{Dirs: []string{only}}, 400); ok {
		t.Fatal("只有斜体时不该算找到")
	}
	if MapleFile(450) != "" || MapleFile(400) != "MapleMono-NF-CN-Regular.ttf" {
		t.Fatal("字重名不对")
	}
}

// 系统兜底：只到给定的系统字体目录按固定文件名找，不扫目录，别的字体不管多像都不收
func TestSystemFixedList(t *testing.T) {
	win := t.TempDir()
	for _, f := range []string{"msyh.ttc", "segoeui.ttf", "malgun.ttf", "simsun.ttc", "msyhbd.ttc"} {
		touch(t, filepath.Join(win, f))
	}
	got := System(Options{SystemDir: win})
	var names []string
	for _, f := range got {
		names = append(names, f.Name+":"+f.Script)
		if f.Source != SourceSystem || filepath.Dir(f.Path) != win {
			t.Errorf("%+v", f)
		}
	}
	want := []string{"微软雅黑:zh", "Malgun Gothic:ko", "Segoe UI:latin"}
	if !slices.Equal(names, want) {
		t.Fatalf("系统字体：%v，应为 %v（顺序中日韩符号西文，名单外的 simsun、msyhbd 不收）", names, want)
	}
	if got := System(Options{SystemDir: filepath.Join(win, "没有")}); len(got) != 0 {
		t.Fatalf("目录不存在应当什么都没有：%v", got)
	}
}

// 按用途给一串：正文 文渊→Maple 常规、粗体→系统；等宽 Maple→文渊→系统；缺的不在里面
func TestChainByPurpose(t *testing.T) {
	t.Setenv("SUMIKA_FONT", "")
	dir, win := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(dir, WenYuanFull))
	touch(t, filepath.Join(dir, "MapleMono-NF-CN-Regular.ttf"))
	touch(t, filepath.Join(dir, "MapleMono-NF-CN-Bold.ttf"))
	touch(t, filepath.Join(win, "msyh.ttc"))
	o := Options{Dirs: []string{dir}, SystemDir: win}
	summary := func(fs []Font) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Source.String())
		}
		return out
	}
	if got := summary(Chain(Body, o)); !slices.Equal(got, []string{"文渊圆体", "Maple Mono NF CN", "Maple Mono NF CN", "系统字体"}) {
		t.Fatalf("正文：%v", got)
	}
	if got := summary(Chain(Mono, o)); !slices.Equal(got, []string{"Maple Mono NF CN", "Maple Mono NF CN", "文渊圆体", "系统字体"}) {
		t.Fatalf("等宽：%v", got)
	}
	if m := Missing(Chain(Body, o)); m != "" {
		t.Fatalf("都有时不该报缺：%q", m)
	}
	// 只剩系统字体
	onlySys := Options{Dirs: []string{t.TempDir()}, SystemDir: win}
	c := Chain(Body, onlySys)
	if len(c) != 1 || c[0].Source != SourceSystem {
		t.Fatalf("只有系统字体：%+v", c)
	}
	if m := Missing(c); m != "没找到：Maple Mono NF CN、文渊圆体" && m != "没找到：文渊圆体、Maple Mono NF CN" {
		t.Fatalf("缺的来源：%q", m)
	}
	// 什么都没有：空，不 panic
	if c := Chain(Body, Options{Dirs: []string{t.TempDir()}, SystemDir: t.TempDir()}); len(c) != 0 {
		t.Fatalf("什么都没有：%+v", c)
	}
}
