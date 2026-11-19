package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 会话行照 Telegram（dialogs.style:95，ADR-284）：铺满列表整宽、直角，行高 62、头像 46，头像离上、下、左都是 8；
// 头像模式每格同样 62 高、铺满 62 宽，头像位置不变；选中的行填强调色。
func TestConversationRowLikeTelegram(t *testing.T) {
	for _, compact := range []bool{false, true} {
		a := newApp()
		a.ListCompact = compact
		tt := ui.NewTester(a.view, 1400, 700)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		row, av, list := root.Find("conversation-A"), root.Find("avatar-conversation-avatar-A"), root.Find("sidebar")
		if row == nil || av == nil || list == nil {
			t.Fatalf("头像模式=%v 找不到会话行、头像或列表", compact)
		}
		r, v := row.Box, av.Box
		t.Logf("头像模式=%v 列表=%+v 行=%+v 头像=%+v", compact, list.Box, r, v)
		if !near(v.W, conversationAvatar) || !near(v.H, conversationAvatar) {
			t.Errorf("头像模式=%v 头像 %.1f×%.1f，期望 46", compact, v.W, v.H)
		}
		if !near(r.H, 62) || !near(r.X, list.Box.X) || !near(r.W, list.Box.W) {
			t.Errorf("头像模式=%v 行 %+v 没有铺满列表 %+v 或行高不是 62", compact, r, list.Box)
		}
		if top, bottom, left := v.Y-r.Y, r.Y+r.H-v.Y-v.H, v.X-r.X; !near(top, 8) || !near(bottom, 8) || !near(left, 8) {
			t.Errorf("头像模式=%v 头像离行 上 %.1f 下 %.1f 左 %.1f，期望都是 8", compact, top, bottom, left)
		}
		if px := tt.Image().RGBAAt(int((r.X+r.W-3)*1), int((r.Y+r.H/2)*1)); px.R != a.P.Strong.R || px.G != a.P.Strong.G || px.B != a.P.Strong.B {
			t.Errorf("头像模式=%v 选中的行右边缘不是强调色：%v，期望 %v", compact, px, a.P.Strong)
		}
	}
}

// 头像模式点放大镜：展开列表、焦点进搜索框。
func TestCompactSearchExpands(t *testing.T) {
	a := newApp()
	a.ListCompact, a.ListDragged = true, true
	tt := ui.NewTester(a.view, 1400, 700)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("搜索会话"); err != nil {
		t.Fatal("头像模式没有放大镜按钮：", err)
	}
	tt.Frame()
	tt.Frame()
	if a.ListCompact || a.ListWidth < listMin {
		t.Fatalf("点放大镜没展开：compact=%v 宽=%.1f", a.ListCompact, a.ListWidth)
	}
	tt.Type("纸")
	tt.Frame()
	if a.Search != "纸" {
		t.Errorf("展开后打字没进搜索框：%q", a.Search)
	}
}

// 未读角标照 Telegram、Bun 用强调色（审查 A4）：没选中的行上是强调深档底。
// （选中的会话一打开未读就清零，选中行上不会出现角标，所以只量没选中的。）
func TestUnreadBadgeAccent(t *testing.T) {
	for _, selected := range []bool{false} {
		a := newApp()
		a.Group = 0
		a.E.conv("A").Unread = 3
		if !selected {
			a.Selected["local"] = "B"
		}
		tt := ui.NewTester(a.view, 1400, 700)
		tt.Frame()
		tt.Frame()
		n := geometryTree(tt).Find("unread-A")
		if n == nil {
			t.Fatalf("选中=%v 没有未读角标", selected)
		}
		px := tt.Image().RGBAAt(int(n.Box.X+2), int(n.Box.Y+n.Box.H/2)) // 角标左边内距里，还没碰到数字
		want := a.P.Strong
		if selected {
			want = a.P.OnStrong
		}
		if px.R != want.R || px.G != want.G || px.B != want.B {
			t.Errorf("选中=%v 未读角标底色 %v，期望 %v", selected, px, want)
		}
	}
}

// 头像模式和展开时列同一份会话（当前分段），未读也照样显示（审查 A6）。
func TestCompactListMatchesExpanded(t *testing.T) {
	a := newApp()
	a.ListCompact, a.ListDragged, a.Group = true, true, 1
	a.E.conv("B").Unread = 2
	tt := ui.NewTester(a.view, 1400, 700)
	tt.Frame()
	tt.Frame()
	root := geometryTree(tt)
	if root.Find("conversation-A") != nil {
		t.Error("群聊分段的头像模式里出现了私聊会话 A")
	}
	if root.Find("conversation-B") == nil {
		t.Fatal("群聊分段的头像模式里没有会话 B")
	}
	if root.Find("unread-B") == nil {
		t.Error("头像模式没有未读角标")
	}
}

// 窄窗右栏抽屉：Esc、点遮罩都能关，效果和"返回聊天"一样（审查 A7、R059）。
func TestDrawerCloses(t *testing.T) {
	for _, how := range []string{"Esc", "点遮罩"} {
		a := newApp()
		a.Layer, a.Right = 2, true
		tt := ui.NewTester(a.view, 900, 700)
		tt.Frame()
		tt.Frame()
		if geometryTree(tt).Find("scrim-close") == nil {
			t.Fatalf("%s：抽屉没打开", how)
		}
		if how == "Esc" {
			tt.Key(0, ui.KeyEscape)
		} else if err := tt.Click("关闭资料"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if a.Layer != 1 || a.Right {
			t.Errorf("%s 没关掉抽屉：Layer=%d Right=%v", how, a.Layer, a.Right)
		}
	}
}
