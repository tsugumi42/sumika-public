package turn

import (
	"context"
	"testing"
	"time"
)

// RunningTurnIDs（运行自检 S4 用）：工具卡住时在跑的回合 id 就是 turns 表里那一行的 id；排空以后没有了
func TestRunningTurnIDsMatchesTurnRow(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: stuckWrite(entered)})
	if ids := h.hub.RunningTurnIDs(); len(ids) != 0 {
		t.Fatalf("还没开始：%v", ids)
	}
	h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "工具开始执行")
	ids := h.hub.RunningTurnIDs()
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("工具在跑应有一个回合 id：%v", ids)
	}
	if ok, err := h.hub.trace().HasTurn(context.Background(), ids[0]); err != nil || !ok {
		t.Fatalf("给出的 id 应是 turns 表里的回合：%v %v", ok, err)
	}
	h.hub.Seal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if ids := h.hub.RunningTurnIDs(); len(ids) != 0 {
		t.Fatalf("排空以后没有在跑的：%v", ids)
	}
}
