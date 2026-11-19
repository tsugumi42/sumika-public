package chatui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 代码高亮配色跟 chatui 自己的深浅走，不跟窗口主题。
func TestCodeColorsFollowChatui(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		h := Host{T: Derive(s)}
		var th markdown.Theme
		h.mdTheme(&th, ui.Color{}, Accent{})
		want := editor.DefaultTheme(ui.LightTheme())
		if dark {
			want = editor.DefaultTheme(ui.DarkTheme())
		}
		if th.CodeColors == nil || !reflect.DeepEqual(*th.CodeColors, *want) {
			t.Errorf("深=%v 代码配色没跟着 chatui 的深浅", dark)
		}
	}
}
