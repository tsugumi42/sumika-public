package chatui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// inkIn 数 r 里和左上角外一点（气泡底色）差得明显的像素：字淡掉以后趋近 0。
func inkIn(tt *ui.Tester, r ui.Rect) int {
	img := tt.Image()
	s := float32(1) // msgsTester 是 1 倍
	px := func(x, y int) [3]int {
		i := img.PixOffset(x, y)
		return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	}
	bg := px(int((r.X-1)*s), int(r.Y*s))
	n := 0
	for y := int(r.Y * s); y < int((r.Y+r.H)*s); y++ {
		for x := int(r.X * s); x < int((r.X+r.W)*s); x++ {
			p := px(x, y)
			d := 0
			for k := range p {
				d += max(p[k]-bg[k], bg[k]-p[k])
			}
			if d > 60 {
				n++
			}
		}
	}
	return n
}

// 审批决定后收成一行时的两处硬跳（主人 2026-11-15 看慢放时指出）：
// 一是气泡宽度变两次（行尾的倒计时换成“已允许”、收完后标题字换掉），每次约 30 像素一下子跳过去；
// 二是决定那一帧倒计时直接没了。现在宽度挂利落弹簧，倒计时留一份拷贝淡出。
func TestAskCardWidthSmooth(t *testing.T) {
	results := map[ApprovalState]string{ApprovalAllowed: "已允许", ApprovalDenied: "已拒绝", ApprovalExpired: "超时"}
	for _, reduce := range []bool{false, true} {
		for state, word := range results {
			t.Run(fmt.Sprintf("%s/%s", reduceName(reduce), word), func(t *testing.T) {
				h := askHost(nil, nil)
				st := askStep(time.Time{})
				tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
				tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
				tt.Frame()
				tt.Frame()
				bw := func(tt *ui.Tester) float32 { return tree(tt).find("p").find("bubble").Box.W }
				start := bw(tt)
				box, ok := tt.Find("4:32")
				if !ok {
					t.Fatal("等批准时找不到倒计时")
				}
				ink0 := inkIn(tt, box)

				cold := askStep(time.Time{})
				cold.Approval.State, cold.State = state, StepDone
				end := bw(msgsTester(h, []*Msg{procMsg("p", 0, cold)}, 900))

				st.Approval.State, st.State = state, StepDone
				ws := []float32{}
				tt.Frame()
				ws = append(ws, bw(tt))
				var ink []int // 倒计时那块地方每帧还有多少不是底色的像素
				ink = append(ink, inkIn(tt, box))
				settle(t, tt, func(int) { ws = append(ws, bw(tt)); ink = append(ink, inkIn(tt, box)) })
				t.Logf("倒计时那块的墨：等批准时 %d，之后 %v", ink0, ink)
				// 决定那一帧倒计时还在原处（拷贝开始淡出），不是一下子没了；减少动态效果也照样淡（淡变不算位移）
				if ink[0]*10 < ink0*7 {
					t.Errorf("决定那一帧倒计时那块只剩 %d（原来 %d）：倒计时直接没了，没有淡出", ink[0], ink0)
				}
				if !near(ws[len(ws)-1], end) {
					t.Errorf("终点宽 %.2f，应等于一开始就是结果行的 %.2f（%v）", ws[len(ws)-1], end, ws)
				}
				if reduce {
					return // 减少动态效果：宽度直接到位，不量中途
				}
				prev, jump := start, float32(0)
				for _, w := range ws {
					jump = max(jump, abs32(w-prev))
					prev = w
				}
				// 每帧 16 毫秒，利落弹簧约 150 毫秒到位：几十像素分到十来帧，单帧不该超过 10 像素
				if jump > 10 {
					t.Errorf("宽度单帧最大跳了 %.1f 像素（起 %.1f 终 %.1f）：%v", jump, start, end, ws)
				}
				t.Logf("宽 %.1f → %.1f，单帧最大 %.1f，共 %d 帧", start, end, jump, len(ws))
			})
		}
	}
}
