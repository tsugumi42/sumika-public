package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/chatui"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/lookpreset"
	"sumika-native/internal/winshell"
)

func opaque(c ui.Color) ui.Color { c.A = 255; return c }

// 新界面外壳的令牌就是原型自己的（lookpreset.Tokens 同一个来源 chatui.Derive），深浅都测；
// 外壳不再经旧主题翻译（ADR-322），所以旧外观页里改的强调色、气泡色不影响新界面。
func TestShellPaletteIsPrototype(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{false, true} {
		set := chatui.DefaultSettings()
		set.Dark = dark
		k := winshell.NewKit(set, "")
		proto := lookpreset.Tokens(dark)
		if k.T.C != proto.C || !k.T.S.Wallpaper {
			t.Fatalf("dark=%v 外壳令牌不是原型的", dark)
		}
		acc := proto.AccentOf(chatui.DefaultChar)
		for name, pair := range map[string][2]ui.Color{
			"窗口底": {k.P.Canvas, proto.C.Bg}, "导航底": {k.P.Rail, proto.C.Rail}, "会话列表底": {k.P.Pane, proto.C.Bg},
			"细线": {k.P.Line, proto.C.Line}, "强调色": {k.P.Accent, acc.Base}, "文字": {k.P.Ink, proto.C.Text},
			"选中行": {k.P.Strong, acc.Strong}, "次要字": {k.P.Muted, proto.C.Text2}, "聊天底": {k.T.C.ChatBg, proto.C.ChatBg},
		} {
			if pair[0] != pair[1] {
				t.Errorf("dark=%v %s：%v，原型 %v", dark, name, pair[0], pair[1])
			}
		}
	}
}

// 真窗口里（新界面、默认外观）画出来的我的气泡底色就是原型的值（入站气泡由上面的令牌测试覆盖）。
func TestWorkbenchDefaultBubblesArePrototypeColors(t *testing.T) {
	for _, dark := range []bool{false, true} {
		h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编的回复")}, nil)
		if dark {
			h.a.look.settings.Mode = "night"
			h.tt.Frame()
		}
		h.wbSend(t, "编的输入")
		st := h.a.localRun("console:char00")
		h.until(t, "回复落入历史", func() bool { return !st.busy && h.tt.HasText("编的回复") })
		h.tt.Frame()
		proto := lookpreset.Tokens(dark)
		acc := proto.AccentOf(chatui.DefaultChar)
		img := h.tt.Image()
		const w = 2048 // 夹具的逻辑宽
		left := func(text string) ui.Color {
			r, ok := h.tt.Find(text) // 第一个气泡是我发的
			if !ok {
				t.Fatalf("没找到 %q", text)
			}
			k := float32(img.Bounds().Dx()) / float32(w)        // 像素比：图宽 / 逻辑宽
			p := img.RGBAAt(int((r.X+4)*k), int((r.Y+r.H/2)*k)) // 取气泡左边缘里面一点的底色
			return ui.RGB(p.R, p.G, p.B)
		}
		if got := left("气泡"); got != opaque(acc.Soft) {
			t.Errorf("dark=%v 我的气泡 %v，原型 %v", dark, got, acc.Soft)
		}
	}
}

// 新界面开着、默认外观时，主题里每一个颜色键都是预设写的，没有一个来自旧的 Telegram 主题。
func TestWorkbenchDefaultThemeHasNoTelegramFallback(t *testing.T) {
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编的回复")}, nil)
	h.tt.Frame()
	for dark, src := range map[bool]*theme.Theme{false: h.a.look.light, true: h.a.look.night} {
		got, want := src.Definition().Colors, lookpreset.Definition(dark).Colors
		if len(got) != len(want) {
			t.Fatalf("dark=%v 颜色键数 %d，预设 %d", dark, len(got), len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("dark=%v %s：主题里是 %s，预设是 %s", dark, k, got[k], v)
			}
		}
	}
}
