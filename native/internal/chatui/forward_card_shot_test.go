package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// shotRect 把气泡周围的区域存成 PNG（像素比 1.25）。字体取 CHATUI_FONT 并确认注册成功。
func shotMessage(t *testing.T, h Host, m *Msg, dir, name string) {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(520).Padding(20).Background(h.T.C.ChatBg).Children(func() { h.Message(c, m, nil, nil) })
	}, 520, 520)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	bub, ok := tt.Find("气泡")
	if !ok {
		t.Fatal("没找到气泡")
	}
	box := image.Rect(int((bub.X-24)*1.25), int((bub.Y-24)*1.25), int((bub.X+bub.W+24)*1.25), int((bub.Y+bub.H+24)*1.25)).Intersect(tt.Image().Bounds())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image().SubImage(box)); err != nil {
		t.Fatal(err)
	}
}

func shotFont(t *testing.T) string {
	t.Helper()
	p := os.Getenv("CHATUI_FONT")
	if p == "" {
		t.Fatal("没设 CHATUI_FONT，截出来不是文渊圆体")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerTestFont(data, "WY Rounded"); err != nil {
		t.Fatal(err)
	}
	return "WY Rounded"
}

// 设 CHATUI_SHOT=目录 时出图：合并转发卡片的正常、加载中、取不到三种，浅色深色各一张。
func TestShotForwardCards(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT 才出图")
	}
	font := shotFont(t)
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		h := Host{T: Derive(s), Font: font, SelfChar: CharColors[0], Column: 560, NoEnter: true}
		for name, f := range map[string]*segview.Forward{
			"normal":  {Title: "晴枝和小满的聊天记录", Lines: [3]string{"晴枝：今天的纸灯真好看", "小满：[图片]", "晴枝：[合并转发]"}, Count: 7, State: "ready"},
			"loading": {Title: "聊天记录", Count: 7, State: "fetching"},
			"failed":  {Title: "聊天记录", Count: 7, State: "not_receiving"},
		} {
			m := fwdMsg(f)
			m.Char = CharColors[0]
			suffix := "-light.png"
			if dark {
				suffix = "-dark.png"
			}
			shotMessage(t, h, m, dir, "forward-"+name+suffix)
		}
	}
}
