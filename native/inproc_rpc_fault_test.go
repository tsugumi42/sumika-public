package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 内核故障从后台一路走到输入框上方那一行：真后台 + 真的内进程 RPC 桥。
// 收尾写库失败（真往 trace 库里放一个会失败的触发器）→ 这个对话进故障 → 经桥再发一句，得到的错误过了协议只剩文字，
// 界面的 userFail 仍认得出并写成一句话；经后台的重试入口恢复后又能发。
func TestBridgeKernelFaultReachesComposerLine(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("嗯")})
	a, err := backend.Open(context.Background(), e.opt)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	d := a.Desktop()
	b, err := newRPCBridge(context.Background(), &d.Reader, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(); d.Close(); a.Close() })
	ctx := localCtx(t)
	key := bridgeKey()

	p, err := paths.Open(e.opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := store.OpenTraceFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	if _, err := trace.Exec(`CREATE TRIGGER made_settled BEFORE UPDATE ON kernel_runs WHEN NEW.state='settled' BEGIN SELECT RAISE(ABORT,'made-settled-failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := b.SendLocal(ctx, key, "编造收尾失败", nil); err == nil || !strings.Contains(err.Error(), "made-settled-failure") {
		t.Fatalf("这一轮该如实报收尾写库失败：%v", err)
	}
	if _, err := trace.Exec(`DROP TRIGGER made_settled`); err != nil {
		t.Fatal(err)
	}

	faults, err := a.KernelFaults(ctx)
	if err != nil || len(faults.Faults) != 1 || faults.Faults[0].Conversation != key.Conversation || faults.Faults[0].Scope != "conversation" {
		t.Fatalf("后台该记着这个对话的故障：%+v %v", faults, err)
	}

	// 再发一句：被拒；直接调和经桥都是故障，经桥以后只剩文字，界面的那一行仍写得出来
	_, dErr := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "再发一句", Mode: "start", ClientKey: "fault-1"})
	_, bErr := b.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "再发一句", Mode: "start", ClientKey: "fault-1"})
	if !errors.Is(dErr, turn.ErrConversationFaulted) || bErr == nil {
		t.Fatalf("出事的对话该被拒：直接 %v 经桥 %v", dErr, bErr)
	}
	f := userFail("没有发出去", bErr)
	if !strings.HasPrefix(f.Line, "没有发出去：这个对话暂时停下了，刚才有内容没能保存。") || strings.Contains(f.Line, "后台出错了") || !strings.Contains(f.Detail, "sumika-ctl kernel retry") || !strings.Contains(f.Detail, "made-settled-failure") {
		t.Fatalf("经桥以后界面那一行该说清：%+v", f)
	}

	// 重试没写成时如实失败的路径在 turn 包里测；这里库已经好了：经后台入口重试成功，又能发
	out, err := a.KernelRetry(ctx, bChar, key.Conversation)
	if err != nil || len(out.Cleared) != 1 || len(out.Remaining) != 0 {
		t.Fatalf("库好了重试该成功：%+v %v", out, err)
	}
	if _, err := b.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "恢复后的一句", Mode: "start", ClientKey: "fault-2"}); err != nil {
		t.Fatalf("恢复后该能发：%v", err)
	}
}
