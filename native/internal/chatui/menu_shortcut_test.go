package chatui

import (
	"strings"
	"testing"
)

func hasCtrlC(got string) bool {
	return strings.Contains(got, "Ctrl+C") || strings.Contains(got, "⌘C")
}

// 选中了文字：复制右边标 Ctrl+C（mygo 的可选文字自带，复制的就是选区）；没选中复制整条，没有对应的键，不标。
func TestMessageMenuCopyShortcutOnlyWithSelection(t *testing.T) {
	r := newMenuRig(t, false, true, false)
	x, y := r.selectMiddle(t)
	r.tt.RightClickAt(x, y)
	r.tt.Frame()
	if got := r.texts(); !hasCtrlC(got) {
		t.Fatalf("有选区时复制该标 Ctrl+C：%s", got)
	}
	r2 := newMenuRig(t, false, true, false)
	rect, _ := findText(r2.tt, "第一句在这")
	r2.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	r2.tt.Frame()
	if got := r2.texts(); hasCtrlC(got) {
		t.Fatalf("没选区时复制整条，不该标 Ctrl+C：%s", got)
	}
}

// 能回复的会话里，纯图片没有文字可引用：回复变灰、右边写原因，点了没反应；不是整项消失。
func TestMessageMenuReplyGreysOutWithoutText(t *testing.T) {
	r := newMenuRig(t, false, true, true)
	img, ok := r.tt.Find("查看图片")
	if !ok {
		t.Fatalf("找不到图片：%s", r.texts())
	}
	r.tt.RightClickAt(img.X+img.W/2, img.Y+img.H/2)
	r.tt.Frame()
	got := r.texts()
	if !strings.Contains(got, "回复") || !strings.Contains(got, "没有文字") {
		t.Fatalf("回复该变灰并写原因，不是消失：%s", got)
	}
	_ = r.tt.Click("回复")
	r.tt.Frame()
	if len(r.replies) != 0 {
		t.Fatalf("灰掉的回复不该响应：%+v", r.replies)
	}
}
