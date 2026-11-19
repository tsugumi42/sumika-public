package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/uitest"
)

func luminance(c color.RGBA) float64 {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= .04045 {
			return x / 12.92
		}
		return math.Pow((x+.055)/1.055, 2.4)
	}
	return .2126*linear(c.R) + .7152*linear(c.G) + .0722*linear(c.B)
}
func contrast(a, b color.RGBA) float64 {
	x, y := luminance(a), luminance(b)
	return (max(x, y) + .05) / (min(x, y) + .05)
}
func pixelRect(r ui.Rect) image.Rectangle {
	return image.Rect(int(math.Ceil(float64(r.X*1.25))), int(math.Ceil(float64(r.Y*1.25))), int((r.X+r.W)*1.25), int((r.Y+r.H)*1.25))
}
func TestSelectedConversationContrast(t *testing.T) {
	uitest.RequireGlyphs(t)
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark = dark
		a.Group = 1
		a.Selected["local"] = "B"
		tt := tester(a)
		title, ok := tt.Find("纸灯小屋")
		if !ok {
			t.Fatal("选中标题不可见")
		}
		img := tt.Image()
		bounds := pixelRect(title).Intersect(img.Bounds())
		bg := color.RGBA{a.P.Strong.R, a.P.Strong.G, a.P.Strong.B, 255} // 选中行的底（ADR-284 起是强调色解出的 Strong 档）
		best := float64(0)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				best = max(best, contrast(img.RGBAAt(x, y), bg))
			}
		}
		if best < 4.5 {
			t.Errorf("深色=%v 选中标题实际像素对比度 %.3f <4.5", dark, best)
		}
		t.Logf("深色=%v 标题实际像素最大对比度=%.3f:1 区域=%v", dark, best, bounds)
	}
}

// TestMessageGroupingAndIdentity 已删：消息合组、名字和头像位置、组内组间间距改由 internal/chatui 的 TestBubbleGeometry、TestJoined 量（ADR-283，照 Telegram）。
func TestSceneEntryAcrossLayers(t *testing.T) {
	for _, w := range []int{2048, 480} {
		for _, layer := range []int{0, 1, 2} {
			a := newApp()
			a.Layer = layer
			tt := ui.NewTester(a.view, w, 560)
			before := geometryTree(tt).Find("workspace").Box
			click(t, tt, "场景")
			if !a.Scenes {
				t.Fatal("场景入口没有打开面板")
			}
			after := geometryTree(tt).Find("workspace").Box
			if after != before {
				t.Fatal("场景浮层改变聊天布局", before, after)
			}
			tt.Key(0, ui.KeyEscape)
			if a.Scenes || a.Layer != layer {
				t.Fatal("Esc未仅关闭当前场景")
			}
			click(t, tt, "场景")
			click(t, tt, "收起")
			if a.Scenes || a.Layer != layer {
				t.Fatal("关闭场景改变当前层")
			}
			t.Log(fmt.Sprintf("宽=%d 层=%d 唯一入口可开合", w, layer))
		}
	}
}

func TestAvatarFamily(t *testing.T) {
	uitest.RequireGlyphs(t)
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark, a.Group, a.Selected["local"] = dark, 1, "B"
		tt := tester(a)
		img := tt.Image()
		for _, key := range []string{"conversation-avatar-B", "header-avatar", "member-r1", "member-r2", "member-r3", "member-r4", "member-r5", "member-r6", "member-r7", "member-r8", "member-r9", "member-r10"} {
			r, ok := tt.Find("头像：" + key)
			want := avatarSize
			if key == "conversation-avatar-B" {
				want = conversationAvatar // 会话行照 Telegram 用 46
			}
			if !ok || r.W != want || r.H != want {
				t.Fatalf("深=%v %s 头像应为%v DIP：%+v", dark, key, want, r)
			}
			box := pixelRect(r)
			bg := img.RGBAAt(box.Min.X+5, box.Min.Y+box.Dy()/2)
			neutral := color.RGBA{a.P.Bubble.R, a.P.Bubble.G, a.P.Bubble.B, 255}
			if bg == neutral || bg.A != 255 {
				t.Fatalf("深=%v %s 头像没有独立实底：%v", dark, key, bg)
			}
			best := float64(0)
			for y := box.Min.Y + 8; y < box.Max.Y-8; y++ {
				for x := box.Min.X + 8; x < box.Max.X-8; x++ {
					best = max(best, contrast(img.RGBAAt(x, y), bg))
				}
			}
			if best < 4.5 {
				t.Fatalf("深=%v %s 首字实际对比度 %.3f <4.5", dark, key, best)
			}
			t.Logf("深=%v %s 尺寸=%.0fx%.0f 底=%v 字对比=%.3f", dark, key, r.W, r.H, bg, best)
		}
	}
}

func TestActivityHierarchy(t *testing.T) {
	uitest.RequireGlyphs(t)
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark = dark
		tt := tester(a)
		if !tt.HasText("还没有活动，她查资料时会列在这里") || tt.HasText("读了 0 份资料，搜了 0 次") {
			t.Fatal("零值没有进入活动空态")
		}
		empty, _ := tt.Find("还没有活动，她查资料时会列在这里")
		img := tt.Image()
		muted := color.RGBA{a.P.Muted.R, a.P.Muted.G, a.P.Muted.B, 255}
		foundMuted := false
		for y := pixelRect(empty).Min.Y; y < pixelRect(empty).Max.Y; y++ {
			for x := pixelRect(empty).Min.X; x < pixelRect(empty).Max.X; x++ {
				foundMuted = foundMuted || img.RGBAAt(x, y) == muted
			}
		}
		if !foundMuted {
			t.Fatal("活动空态没有使用次要文字色")
		}
		for _, counts := range [][2]int{{1, 0}, {0, 2}, {3, 2}} {
			a.E.ReadCount["A"], a.E.SearchCount["A"] = counts[0], counts[1]
			tt.Frame()
			activity, ok := tt.Find(a.E.activity("A"))
			member, found := tt.Find("头像：member-r1")
			title, _ := tt.Find("活动")
			membersTitle, _ := tt.Find("成员")
			if !ok || !found || tt.HasText("还没有活动，她查资料时会列在这里") || math.Abs(float64(activity.X-member.X-member.W-spaceSmall)) > 1 {
				t.Fatal("活动未按同族列表行布局", activity, member)
			}
			if activity.Y-title.Y-title.H >= membersTitle.Y-activity.Y-activity.H {
				t.Fatal("分节没有组内紧、组间松", title, activity, membersTitle)
			}
			t.Logf("深=%v 读搜=%v 行文字x=%.2f 成员文字x=%.2f 组内/组间=%.2f/%.2f", dark, counts, activity.X, member.X+member.W+spaceSmall, activity.Y-title.Y-title.H, membersTitle.Y-activity.Y-activity.H)
		}
	}
}

func TestEmptySendDisabled(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, id := range []string{"A", "B"} {
			a := newApp()
			a.Dark, a.Selected["local"] = dark, id
			cv := a.E.conv(id)
			disabled := false
			a.Surfaces = func(c *ui.Context, e ui.Element, info surfaceInfo) {
				if info.ID == "send" {
					disabled = e.IsDisabled()
				}
			}
			tt := tester(a)
			for _, draft := range []string{"", " \t\n　"} {
				cv.Draft = draft
				tt.Frame()
				if !disabled {
					t.Fatal("空白草稿发送键没有禁用")
				}
				before := len(cv.Messages)
				_ = tt.Click("发送")
				if len(cv.Messages) != before || cv.PendingDraft != "" {
					t.Fatal("空白草稿触发发送")
				}
				bounds, _ := tt.Find("发送")
				box := pixelRect(bounds)
				pixel := tt.Image().RGBAAt(box.Min.X+5, box.Min.Y+box.Dy()/2)
				// 配色改成 Bun 的冷调中性灰（色相 250）后，灰本身带一点蓝（三分量差约 9）；强调色差上百，门槛 24 照样拦得住
				if max(pixel.R, pixel.G, pixel.B)-min(pixel.R, pixel.G, pixel.B) > 24 {
					t.Fatal("禁用发送仍有明亮强调色", pixel)
				}
			}
			cv.Draft = "沿河散步。"
			tt.Frame()
			if disabled {
				t.Fatal("有正文仍禁用")
			}
			click(t, tt, "发送")
			tt.Advance(600 * time.Millisecond)
			if !tt.HasText("停止") {
				t.Fatal("流式停止入口丢失")
			}
			click(t, tt, "停止")
			if a.E.active(id) {
				t.Fatal("停止无效")
			}
			t.Logf("深=%v 会话=%s 空/空白禁用，正文发送与流式停止通过", dark, id)
		}
	}
}
