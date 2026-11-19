package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 主人 2026-11-16 在画廊里发现：同一个气泡里“重新来”换上一张新卡（新的步骤 ID）以后，再做决定，卡就不再逐帧收成一行了。
// 第二张、第三张卡都要和第一张一样：高度逐帧收、终点等于冷渲染的结果行。
func TestAskCardCollapsesAgainAfterNewStep(t *testing.T) {
	h := askHost(nil, nil)
	st := askStep(time.Time{})
	m := procMsg("p", 0, st)
	tt := msgsTester(h, []*Msg{m}, 900)
	tt.Frame()
	tt.Frame()
	for round := 0; round < 3; round++ {
		start := bubbleH(tt, "p")
		st.Approval.State, st.State = ApprovalAllowed, StepDone
		tt.Frame()
		var hs []float32
		settle(t, tt, func(int) { hs = append(hs, bubbleH(tt, "p")) })
		end := hs[len(hs)-1]
		mid := 0
		for _, v := range hs {
			if v > end+0.5 && v < start-0.5 {
				mid++
			}
		}
		if mid < 3 {
			t.Fatalf("第 %d 张卡：收起中途只有 %d 帧（%.1f → %.1f）：%v", round+1, mid, start, end, hs)
		}
		// 重新来：同一条消息换上新的一步
		next := askStep(time.Time{}) // 和画廊“审批卡”的“重来”一样：新的一步，编号和上一张一样
		if round%2 == 1 {
			next.ID = st.ID + "x" // 也试编号不同的（画廊“消息和审批”的“重新来”）
		}
		*st = *next
		settle(t, tt, nil)
		if !tt.HasText("允许一次") {
			t.Fatalf("第 %d 次重新来以后没有出现新的审批卡：%v", round+1, tt.Texts())
		}
	}
	_ = ui.Rect{}
}

// 第一张卡收成一行以后，用户点开那一行看了细节（这条消息记成“展开着”）；“重来”换上新卡、再做决定，
// 新卡照样要自己收成一行，不能因为上一张卡留下的“展开着”就直接把内容换掉（主人 2026-11-16 在画廊“审批卡”试一试里碰到）。
func TestAskCardCollapsesAgainAfterUserOpened(t *testing.T) {
	h := askHost(nil, nil)
	opened := map[string]*bool{}
	h.StepOpen = func(id string) *bool {
		if opened[id] == nil {
			opened[id] = new(bool)
		}
		return opened[id]
	}
	st := askStep(time.Time{})
	tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
	tt.Frame()
	tt.Frame()
	st.Approval.State, st.State = ApprovalAllowed, StepDone
	settle(t, tt, nil)
	*h.StepOpen("p") = true // 用户点开了收好的那一行
	settle(t, tt, nil)
	*st = *askStep(time.Time{}) // 重来：同一条消息、同一个编号的新一步
	settle(t, tt, nil)
	start := bubbleH(tt, "p")
	st.Approval.State, st.State = ApprovalAllowed, StepDone
	tt.Frame()
	var hs []float32
	settle(t, tt, func(int) { hs = append(hs, bubbleH(tt, "p")) })
	end := hs[len(hs)-1]
	mid := 0
	for _, v := range hs {
		if v > end+0.5 && v < start-0.5 {
			mid++
		}
	}
	if mid < 3 || end > start-20 {
		t.Errorf("用户点开过以后的新卡：收起中途只有 %d 帧（%.1f → %.1f），应逐帧收成一行：%v", mid, start, end, hs)
	}
}
