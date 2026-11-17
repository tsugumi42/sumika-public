package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/linktemplate"
)

// 翻译开关：总开关和目标语言、时限在展开设置里；只有接了 $译文 的网站（现在是 X）在自己的页面里有翻译开关。
func TestTranslateSwitchesInPage(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	for _, n := range []string{"翻译成什么语言", "翻译时限（秒，1～60）"} {
		if _, ok := p.tt.Find(n); !ok {
			p.click(t, n)
		}
	}
	if !p.expand().Translate.Enabled {
		t.Fatal("默认应开")
	}
	// B 站没接译文：展开后没有翻译开关
	p.click(t, "哔哩哔哩")
	if p.tt.HasText("X / 推特翻译") {
		t.Fatal("B 站不该有翻译开关")
	}
	p.click(t, "哔哩哔哩")
	// X：有，点一下只关 X
	p.click(t, "X / 推特")
	p.click(t, "X / 推特翻译")
	p.pump(t)
	if p.expand().TranslateOn("x") || !p.expand().Translate.Enabled {
		t.Fatalf("应只关 X：%+v", p.expand().Platforms["x"])
	}
}

func mustPlatform(t *testing.T, id string) linktemplate.CatalogPlatform {
	t.Helper()
	pl, ok := linktemplate.PlatformByID(id)
	if !ok {
		t.Fatal(id)
	}
	return pl
}

func TestTranslatableCatalog(t *testing.T) {
	t.Parallel()
	if !linktemplate.PlatformTranslatable(mustPlatform(t, "x")) || linktemplate.PlatformTranslatable(mustPlatform(t, "bilibili")) {
		t.Fatal("只有 X 接了译文")
	}
}

func TestTranslatePageShot(t *testing.T) {
	dir := os.Getenv("LINK_SHOT_DIR")
	if dir == "" {
		t.Skip("LINK_SHOT_DIR 没设")
	}
	for _, dark := range []bool{false, true} {
		p := newPlatformHarness(t, 2048, 1030, dark)
		p.openLinks(t)
		p.click(t, "X / 推特")
		p.click(t, "翻译成什么语言")
		p.tt.Frame()
		name := "link-translate-light.png"
		if dark {
			name = "link-translate-dark.png"
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, p.tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
