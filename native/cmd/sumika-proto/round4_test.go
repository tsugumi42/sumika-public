package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func eachGeometry(n *geometryNode, fn func(*geometryNode)) {
	fn(n)
	for _, ch := range n.Children {
		eachGeometry(ch, fn)
	}
}

// TestJoinedBubbleCorners 已删：相贴小角改由 internal/chatui 的 TestBubbleCorners、TestBubbleGeometry 量。
func TestQuietPrototypeChrome(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark = dark
		tt := tester(a)
		for _, state := range []string{"chat", "scenes", "settings"} {
			if state == "scenes" {
				click(t, tt, "场景")
			}
			if state == "settings" {
				click(t, tt, "收起")
				click(t, tt, "设置")
			}
			root := geometryTree(tt)
			if root.Find("rehearsal-toolbar") != nil {
				t.Fatal("底部横条仍切断各栏")
			}
			entries := 0
			eachGeometry(root, func(n *geometryNode) {
				if n.Key == "scene-open" {
					entries++
				}
				for _, old := range []string{"Enter 发送", "Shift+Enter", "Ctrl+F", "Esc 关闭", "本地会话", "主题仅对本次演练生效。", "主窗口", "演练 ", "已停止，保留已收到的内容"} {
					if strings.Contains(n.Text, old) {
						t.Errorf("%s 仍有冗余文字 %q", state, n.Text)
					}
				}
			})
			if entries != 1 {
				t.Fatal("场景入口不唯一", entries)
			}
		}
	}
}
func TestUnreadCapsuleAndPaneLevels(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark = dark
		a.Group = 1
		a.E.conv("B").Unread = 12
		tt := tester(a)
		root := geometryTree(tt)
		badge := root.Find("unread-B")
		if badge == nil || !badge.Painted {
			t.Fatal("未读数没有胶囊底")
		}
		for _, r := range badge.Radius {
			equal(t, "", "未读胶囊半高", r, badge.Box.H/2)
		}
		img := tt.Image()
		nav, sidebar := root.Find("navigation"), root.Find("sidebar")
		left := img.RGBAAt(int((nav.Box.X+nav.Box.W/2)*1.25), int((nav.Box.Y+nav.Box.H/2)*1.25))
		mid := img.RGBAAt(int((sidebar.Box.X+sidebar.Box.W/2)*1.25), int((sidebar.Box.Y+sidebar.Box.H/2)*1.25))
		if left == mid {
			t.Fatal("导航栏与会话栏仍同底色", left)
		}
		corners := []ui.Point{{X: 0, Y: 0}, {X: 2047, Y: 0}, {X: 0, Y: 1029}, {X: 2047, Y: 1029}}
		for _, p := range corners {
			// 窗口圆角交给系统画，界面四角是实底、不再自己裁透明（ADR-284）
			if img.RGBAAt(int(p.X*1.25), int(p.Y*1.25)).A != 255 {
				t.Fatal("窗口角还是透明的，界面不该自己裁窗口圆角", p)
			}
		}
	}
}
