package catalog

import (
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// look 注册测试用的字体和表情，一个进程只注册一次（mygo 的字体表只进不出，每个新 Tester 都把表里的字体整份复制一遍）。
// 平时用仓库里的文渊圆体子集（466 KB）；设了 GALLERY_SHOT 要出截图给人看时才用完整字体，没有就退回系统字体。
func look() string {
	lookOnce.Do(func() {
		path, family := filepath.Join("..", "..", "testdata", "fonts", "WenYuanTiny.ttf"), "SumikaTestRounded"
		if os.Getenv("GALLERY_SHOT") != "" {
			path, family = fonts.WenYuanPath(), "WY Rounded"
		}
		if data, err := os.ReadFile(path); err == nil && ui.RegisterFont(data, family) == nil {
			lookFont = family
		}
	})
	chatui.EmojiDir = assets.TwemojiDir()
	return lookFont
}

var (
	lookOnce sync.Once
	lookFont string
)

// openEntry 把一条例子放进一个只有舞台的窗口（宽 1700、高 1000（和画廊默认窗口去掉左边列表后的舞台一样大），逻辑像素）里打开，推进时钟到稳定。
func openEntry(t *testing.T, e Entry, s chatui.Settings, font string) *ui.Tester {
	t.Helper()
	k := winshell.NewKit(s, font)
	store := NewStore()
	view := func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		c.Root().Background(k.P.Canvas)
		ui.Themed(c.Key("stage"), &k.Theme).Fill().Padding(k.G.SpacePanel).Gap(k.G.SpaceSection).Children(func() {
			e.Demo(c, Env{Kit: k, Store: store, ID: e.ID, Room: 1700 - 2*k.G.SpacePanel})
		})
	}
	tt := ui.NewTester(view, 1700, 1000)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Advance(2 * time.Second)
	return tt
}

func TestRegistry(t *testing.T) {
	all := All()
	if len(all) < 5 {
		t.Fatalf("登记表里只有 %d 条，第一版应有按钮、选择类、输入类、聊天气泡、审批卡", len(all))
	}
	seen := map[string]bool{}
	for _, e := range all {
		if seen[e.ID] {
			t.Errorf("ID %q 重复", e.ID)
		}
		seen[e.ID] = true
		if got, ok := Find(e.ID); !ok || got.ID != e.ID {
			t.Errorf("Find(%q) 找不到", e.ID)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("重复登记应当 panic")
		}
	}()
	Add(all[0])
}

// 每条例子在亮、暗 × 外观设置的最小、默认、最大值下都能打开，不 panic；
// 设了 GALLERY_SHOT=目录 时，把默认设置下的亮、暗各存一张 PNG（目录下的 shots 子目录由调用方定）。
func TestEveryEntry(t *testing.T) {
	font := look()
	defer func() { chatui.EmojiDir = "" }()
	shot := os.Getenv("GALLERY_SHOT")
	if shot != "" {
		if err := os.MkdirAll(shot, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sizes := []struct {
		name string
		s    chatui.Settings
	}{
		{"默认", chatui.DefaultSettings()},
		{"最小", func() chatui.Settings {
			s := chatui.DefaultSettings()
			s.Text, s.Radius, s.Density = chatui.TextMin, chatui.RadiusMin, chatui.DensityMin
			return s
		}()},
		{"最大", func() chatui.Settings {
			s := chatui.DefaultSettings()
			s.Text, s.Radius, s.Density = chatui.TextMax, chatui.RadiusMax, chatui.DensityMax
			return s
		}()},
	}
	for _, e := range All() {
		for _, sz := range sizes {
			for _, dark := range []bool{false, true} {
				mode := map[bool]string{false: "亮", true: "暗"}[dark]
				t.Run(e.ID+"/"+sz.name+"/"+mode, func(t *testing.T) {
					s := sz.s
					s.Dark = dark
					tt := openEntry(t, e, s, font)
					if shot == "" || sz.name != "默认" {
						return
					}
					f, err := os.Create(filepath.Join(shot, e.ID+"-"+map[bool]string{false: "light", true: "dark"}[dark]+".png"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					if err := png.Encode(f, tt.Image()); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
