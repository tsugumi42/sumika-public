package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// 宿主自动跑的轮进行时（目标续轮、定期任务），输入框里有字：回车和点“发送”都要真的发出去（按 start，后台转成插话或排队），
// 不能画着发送键却什么都不做。
func TestAutoRunEnterAndClickReallySend(t *testing.T) {
	for _, how := range []string{"enter", "click"} {
		t.Run(how, func(t *testing.T) {
			f := &fakeInputs{}
			r := newWbRig(t, f)
			r.a.localStatus = map[string]api.LocalStatus{r.key.Conversation: {Key: r.key, AutoRunning: true}}
			r.settle()
			if _, ok := r.tt.Find("停止生成"); !ok {
				t.Fatal("自动轮进行、没字时应当是停止键")
			}
			typeIn(t, r, "顺便改一下标题")
			if _, ok := r.tt.Find("发送（插话）"); !ok {
				t.Fatalf("有字时应当是发送键：%v", r.tt.Texts())
			}
			if how == "enter" {
				r.tt.Key(0, ui.KeyEnter)
			} else if err := r.tt.Click("发送（插话）"); err != nil {
				t.Fatal(err)
			}
			r.submitted(t)
			got := f.submissions()
			if len(got) != 1 || got[0].Mode != "start" || got[0].Text != "顺便改一下标题" {
				t.Fatalf("自动轮里发话应当真的提交（start，由后台转插话或排队）：%+v", got)
			}
		})
	}
}

// 收尾时才到的插话被后台改排成新一轮：界面要跟上这一轮（忙碌、能停止），不能显示空闲。
func TestRequeuedSteerAtSettleIsFollowedByComposer(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "先写个排序")
	r.settle()
	f.finish("run1", api.LocalRunSnapshot{Outcome: "done", Requeued: []api.LocalRequeued{{InputID: "late1", RunID: "runX", Text: "最后一刻到的插话", Seq: 9}}})
	r.settleUntil(t, func() bool { return st.runID == "runX" })
	if !st.busy || st.runID != "runX" {
		t.Fatalf("应当跟上改排出的新一轮：busy=%v runID=%q", st.busy, st.runID)
	}
	if _, ok := r.tt.Find("停止生成"); !ok {
		t.Fatalf("跟上新一轮后应当有停止键：%v", r.tt.Texts())
	}
	// 新一轮收尾后回到空闲，插话不会被重复排两次
	f.finish("runX", api.LocalRunSnapshot{Outcome: "done"})
	r.settleUntil(t, func() bool { return !st.busy }) // 机器忙时一次 settle 不一定够
	if st.busy || len(st.queued) != 0 {
		t.Fatalf("新一轮收尾后应当空闲：%+v", st)
	}
}
