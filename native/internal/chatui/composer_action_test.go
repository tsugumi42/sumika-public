package chatui

import (
	"image"
	"testing"
	"time"
)

// 右下角圆键（主人 2026-11-15“输入框按钮”）：发送、停止、插话是同一颗钮；换状态时图标的轮廓从纸飞机流到停止的方块，中途既不是起点也不是终点，
// 放完以后和直接画终点的图标逐像素一样；减少动态效果时直接换。
func TestComposerActionMorphs(t *testing.T) {
	if MorphGlyph("send", false).Shape == nil || MorphGlyph("stop", true).Shape == nil {
		t.Fatal("发送、停止两个图标要取得到轮廓，不然只会退回缩放替换")
	}
	r := newComposerRig(t)
	r.p.Main.Text = "有字" // 两头都是强调色底：中途和两头不同只能是图标在变，不是底色淡变
	r.tester.Frame()
	img := func() *image.RGBA { return r.tester.Image() }
	box, ok := r.tester.Find("发送")
	if !ok {
		t.Fatal("没有发送键")
	}
	crop := func(a *image.RGBA) []uint8 {
		var out []uint8
		for y := int(box.Y); y < int(box.Y+box.H); y++ {
			i := a.PixOffset(int(box.X), y)
			out = append(out, a.Pix[i:i+int(box.W)*4]...)
		}
		return out
	}
	diff := func(a, b []uint8) int {
		n := 0
		for i := range a {
			if a[i] != b[i] {
				n++
			}
		}
		return n
	}
	// 终点的样子：先切到“停止”放完，记下来，再切回发送放完，记起点
	r.host.Busy = true
	settle(t, r.tester, nil)
	stopEnd := crop(img())
	r.host.Busy = false
	settle(t, r.tester, nil)
	sendEnd := crop(img())
	if diff(stopEnd, sendEnd) == 0 {
		t.Fatal("发送和停止画出来一样")
	}
	// 再切一次，逐帧看
	r.host.Busy = true
	r.tester.Frame()
	mid := 0
	settle(t, r.tester, func(int) {
		cur := crop(img())
		if diff(cur, stopEnd) > 0 && diff(cur, sendEnd) > 0 {
			mid++
		}
	})
	if mid < 3 {
		t.Errorf("切到停止时中途只有 %d 帧不同于两头，应是逐帧变形", mid)
	}
	if d := diff(crop(img()), stopEnd); d != 0 {
		t.Errorf("放完和直接画停止键差 %d 个字节", d)
	}
	if _, ok := r.tester.Find("停止生成"); !ok {
		t.Error("切完找不到停止键")
	}
	if got, _ := r.tester.Find("停止生成"); got != box {
		t.Errorf("停止键的位置 %+v 和发送键 %+v 不同：应是同一颗钮", got, box)
	}
	_ = time.Millisecond
}
