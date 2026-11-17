package mediafetch

import (
	"context"
	"sumika-native/internal/channel/types"
	"testing"
)

func TestForwardMediaRestartAfterTreeCommit(t *testing.T) {
	e := newEnv(t)
	content := png("编的重启图")
	e.serve("/restart", 200, content)
	tree := types.ForwardTree{ID: "restart", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{imgSeg("/restart"), {Kind: "image", URL: testSegURL("/limited"), Raw: "forward_limit"}}}}}
	if err := e.st.PutForward(context.Background(), tree, e.clk.Now().UnixMilli(), testConv); err != nil {
		t.Fatal(err)
	}
	// 树提交后立即退出，未调用 OfferForward；新工人只靠持久化补扫恢复。
	e.f.Close()
	restarted, err := New(e.f.o)
	if err != nil {
		t.Fatal(err)
	}
	e.f = restarted
	e.start()
	e.f.Resweep() // 新进程连上协议端后才恢复持久化任务。
	waitFor(t, "重启补下转发图", func() bool {
		got, ok, err := e.st.ReadForward(context.Background(), tree.ID)
		return err == nil && ok && got.Tree.Nodes[0].Segs[0].Hash == sha(content)
	})
	e.idle()
	if e.nreq("/restart") != 1 || e.nreq("/limited") != 0 {
		t.Fatalf("下载次数：%v", e.requests())
	}
}
