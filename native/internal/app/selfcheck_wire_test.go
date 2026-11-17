package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/filelog"
	"sumika-native/internal/selfcheck"
)

// 十四条检查都接上了各自的数据源：全新的后台评估一次，除 S9 外没有一条是 unknown（unknown 就是没接上或查不动），也没有 bad（含 S10 内核停止受理、S11 MCP 隔离、S13 嵌入、S14 平台库；测试配置里嵌入是关着的，S13 记 ok）；
// S9（每天备份）刚开机时备份调度还没做过第一次检查，该是 unknown 并写明这个原因（不是“没有接备份状态”：那说明没接上）。
func TestSelfcheckEveryCheckHasItsSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	a.selfcheck.runner.Evaluate(context.Background())
	b, err := os.ReadFile(filepath.Join(a.paths.SelfcheckDir(), a.now().Format(filelog.DayLayout)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f selfcheck.DayFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	want := []string{"intake.unaccounted", "intake.store_failed", "outlet.receipt_unknown", "turn.stuck", "channel.reconnect_storm", "memory.compact_failed", "model.stage_failing", "events.seq_gap", "backup.daily_stale", "kernel.faulted", "mcp.quarantined", "backup.drill_stale", "embed.unavailable", "store.unavailable"}
	if len(f.Checks) != len(want) {
		t.Fatalf("应有 %d 条检查：%+v", len(want), f.Checks)
	}
	for i, row := range f.Checks {
		if row.ID != want[i] {
			t.Errorf("第 %d 条应是 %s（按规格编号排），是 %s", i, want[i], row.ID)
		}
		if row.ID == "backup.daily_stale" {
			if row.State != selfcheck.Unknown || !strings.Contains(row.Note, "第一次检查") {
				t.Errorf("S9 在刚开机的后台上该是 unknown（等备份调度第一次检查）：%+v", row)
			}
			continue
		}
		if row.ID == "backup.drill_stale" {
			if row.State != selfcheck.Unknown || !strings.Contains(row.Note, "还没读过") {
				t.Errorf("S12 在刚开机的后台上该是 unknown（等备份调度第一次读抽验记录）：%+v", row)
			}
			continue
		}
		if row.State != selfcheck.Ok {
			t.Errorf("%s 在全新的后台上应是 ok：%+v", row.ID, row)
		}
	}
}

// 计数器接进了入库一站和适配器：收到一条私聊，帧、交给入库、入库各数一次，两条守恒差额都是 0；对端断开记一次断开事件
func TestSelfcheckCountersWiredIntoPipeline(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.start()
	conv := "qq:private:" + testAccount + ":" + testOwner
	conn := e.peer.accept(t)
	e.peer.send(t, conn, privateFrame(e.now, testAccount, "502", testOwner, "在吗"))
	waitFor(t, "私聊入库", func() bool {
		n, err := e.eventCount(a, conv)
		return err == nil && n >= 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.intake.Idle(ctx); err != nil {
		t.Fatalf("等入库做完：%v", err)
	}
	sc := a.selfcheck.counters
	if sc.Get(selfcheck.FrameToIntake) != 1 || sc.Get(selfcheck.IntakeSubmitted) != 1 || sc.Get(selfcheck.IntakeStored) != 1 {
		t.Fatalf("一条消息：交给入库 %d、交来 %d、入库 %d", sc.Get(selfcheck.FrameToIntake), sc.Get(selfcheck.IntakeSubmitted), sc.Get(selfcheck.IntakeStored))
	}
	if d := sc.Get(selfcheck.FrameReceived) - (sc.Get(selfcheck.FrameDecodeFailed) + sc.Get(selfcheck.FrameNonMessage) + sc.Get(selfcheck.FrameToIntake)); d != 0 {
		t.Fatalf("适配器的守恒差额应是 0：%d", d)
	}
	if live := sc.Get(selfcheck.IntakeLive); live != 1 || sc.Get(selfcheck.FrameSinkDropped) != 0 {
		t.Fatalf("交接：实时交来 %d（要 1）、接收端丢弃 %d（要 0）", live, sc.Get(selfcheck.FrameSinkDropped))
	}
	if n := sc.Get(selfcheck.Reconnects); n != 0 {
		t.Fatalf("连着的时候没有断开事件：%d", n)
	}
	conn.CloseNow()
	waitFor(t, "断开事件", func() bool { return sc.Get(selfcheck.Reconnects) == 1 })
}

// 接收端因号对不上（连错了端点）有意丢掉的消息：数 FrameSinkDropped，不入库；交接守恒式（交给入库 = 接收端丢弃 + 实时交来）仍平衡
func TestSelfcheckSinkDropCountedWhenAccountMismatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.start()
	conn := e.peer.accept(t)
	e.peer.send(t, conn, privateFrame(e.now, "7777", "503", testOwner, "发错号了"))
	sc := a.selfcheck.counters
	waitFor(t, "接收端丢弃", func() bool { return sc.Get(selfcheck.FrameSinkDropped) == 1 })
	if sc.Get(selfcheck.FrameToIntake) != 1 || sc.Get(selfcheck.IntakeLive) != 0 || sc.Get(selfcheck.IntakeSubmitted) != 0 {
		t.Fatalf("交给入库 %d（要 1）、实时交来 %d（要 0）、交来 %d（要 0）", sc.Get(selfcheck.FrameToIntake), sc.Get(selfcheck.IntakeLive), sc.Get(selfcheck.IntakeSubmitted))
	}
	if d := sc.Get(selfcheck.FrameToIntake) - (sc.Get(selfcheck.FrameSinkDropped) + sc.Get(selfcheck.IntakeLive)); d != 0 {
		t.Fatalf("交接差额应是 0：%d", d)
	}
}

// 自检例子里的会话号经 RedactLog 打码（S4、S6 的例子带会话号）：群号、QQ 号不出现在文件和日志里
func TestSelfcheckExampleConversationIsRedacted(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"c1@qq:group:123456789", "qq:private:100200300:987654321"} {
		got := RedactLog(in, nil)
		if strings.Contains(got, "123456789") || strings.Contains(got, "987654321") || strings.Contains(got, "100200300") {
			t.Errorf("%q 打码以后还带着号码：%q", in, got)
		}
	}
}
