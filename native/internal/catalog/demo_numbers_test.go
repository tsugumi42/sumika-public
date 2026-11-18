package catalog

import (
	"image"
	"regexp"
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

var reClock = regexp.MustCompile(`^\d:\d\d$`)

func clockText(texts []string) string {
	for _, s := range texts {
		if reClock.MatchString(s) {
			return s
		}
	}
	return ""
}

// 数字和图标条目：倒计时一直在往下走（用 c.After 驱动，到点就换一位）、步进器点加减值变、复制钮点了变成勾、两秒后变回来。
func TestNumbersEntry(t *testing.T) {
	font := look()
	e, ok := Find("numbers")
	if !ok {
		t.Fatal("没有登记 numbers 条目")
	}
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	for i := 0; i < 200; i++ { // 让倒计时过半秒，避开刚好整秒的边界
		tt.Advance(10 * time.Millisecond)
	}
	tt.Frame()
	first := clockText(tt.Texts())
	if first == "" {
		t.Fatalf("没找到倒计时：%v", tt.Texts())
	}
	tt.Advance(3 * time.Second)
	tt.Frame()
	for i := 0; tt.FramePending() && i < 100; i++ {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if second := clockText(tt.Texts()); second == "" || second >= first {
		t.Errorf("过了 3 秒倒计时该变小：%q → %q", first, second)
	}

	if err := tt.Click("每次回话最多想起几条 加"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if _, ok := tt.Find("9"); !ok {
		t.Errorf("点加以后步进器该是 9：%v", tt.Texts())
	}

	for _, pr := range [][2]string{{"复制这段话", "已复制"}, {"发送", "停止回复"}, {"打开菜单", "关闭菜单"}, {"播放", "暂停"}} {
		if err := tt.Click(pr[0]); err != nil {
			t.Fatalf("点 %s：%v", pr[0], err)
		}
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if _, ok := tt.Access(pr[1]); !ok {
			t.Errorf("点了 %s 以后钮该叫 %s", pr[0], pr[1])
		}
	}
	tt.Advance(2500 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Access("复制这段话"); !ok {
		t.Error("两秒后复制钮没变回来")
	}
}

// 图标钮在真实的按钮里变形：中间帧里钮的位置上要有墨迹（挂在带缩放的按钮里时，曾经用 Painter.Transform 画出一片空白）。
func TestNumbersIconMorphInsideButton(t *testing.T) {
	font := look()
	e, _ := Find("numbers")
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	a, _ := tt.Access("发送")
	cx, cy := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
	ink := func() int {
		im := tt.Image()
		r := image.Rect(int((cx-10)*1.25), int((cy-10)*1.25), int((cx+10)*1.25), int((cy+10)*1.25))
		n := 0
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if im.RGBAAt(x, y).R < 120 {
					n++
				}
			}
		}
		return n
	}
	before := ink()
	tt.Press(cx, cy)
	tt.Release(cx, cy)
	tt.Frame()
	tt.Advance(70 * time.Millisecond)
	tt.Frame()
	if n := ink(); n < before/3 || !tt.FramePending() {
		t.Errorf("变形中间帧墨迹 %d（起点 %d），或没在要帧：中间应画着形状", n, before)
	}
}
