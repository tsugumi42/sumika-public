package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/datalock"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/runstate"
)

func TestDrainWaitsRealExit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	identity := kernel.Invocation{RunID: "timeout-tool-run", ToolID: "tool-host-1", Kind: kernel.KindForeground}
	_, activity, err := a.hub.Cfg.Lifetime.Begin(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	end := sync.OnceFunc(func() { activity.End(context.DeadlineExceeded) })
	defer end()
	a.responderWait = time.Millisecond
	err = a.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout执行尚未退却关机成功 %v", err)
	}
	if err := a.set.TraceStore().Trace.Ping(); err != nil {
		t.Fatalf("排空失败提前关库 %v", err)
	}
	if lock, err := datalock.LockData(e.data); err == nil {
		lock.Close()
		t.Fatal("排空失败提前释放数据锁")
	}
	end()
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if lock, err := datalock.LockData(e.data); err != nil {
		t.Fatalf("实际退出后未释放数据锁 %v", err)
	} else {
		lock.Close()
	}
	t.Log("工具可见 timeout 后：库和数据锁保留；真实 End 后重试关闭，数据锁可重新取得")
}

func TestShutdownAdmissionBarrier(t *testing.T) {
	t.Parallel()
	for _, halfOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("Open半失败=%v", halfOpen), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			d := a.Desktop()
			releaseApproval, asked, written := holdApprovalWrite(t, a)
			defer releaseApproval()
			var activities []kernel.Activity
			var releases []func()
			defer func() {
				for _, release := range releases {
					release()
				}
			}()
			for _, kind := range []string{kernel.KindForeground, kernel.KindCompact, kernel.KindMemory} {
				_, activity, err := a.hub.Cfg.Lifetime.Begin(context.Background(), kernel.Invocation{RunID: kind, Kind: kind})
				if err != nil {
					t.Fatal(err)
				}
				activities = append(activities, activity)
				releases = append(releases, sync.OnceFunc(func() { activity.End(nil) }))
			}
			done := make(chan error, 1)
			if halfOpen {
				go func() { a.cleanup(); done <- nil }()
			} else {
				go func() { done <- a.Close() }()
			}
			decision := shutdownRecv(t, asked, "封准入后取消审批")
			if decision.Kind != "cancelled" {
				t.Fatalf("审批未取消 %+v", decision)
			}
			if _, _, err := a.hub.Cfg.Lifetime.Begin(context.Background(), kernel.Invocation{Kind: kernel.KindForeground}); !errors.Is(err, runstate.ErrSealed) {
				t.Fatalf("关机后仍准入 %v", err)
			}
			if _, _, err := d.enterChat(); err == nil {
				t.Fatal("桌面未封入口")
			}
			if _, err := a.groups.CreateLocalGroup(context.Background(), api.LocalGroupCreate{}); err == nil {
				t.Fatal("群入口未封")
			}
			if err := a.set.TraceStore().Trace.Ping(); err != nil {
				t.Fatal("活动退出前关库")
			}
			releaseApproval()
			if err := shutdownRecv(t, written, "请求持久化"); err != nil {
				t.Fatal(err)
			}
			if err := shutdownRecv(t, written, "取消持久化"); err != nil {
				t.Fatal(err)
			}
			for i, activity := range activities {
				select {
				case err := <-done:
					t.Fatalf("受管活动未退就关闭 %v", err)
				default:
				}
				if _, err := a.set.TraceStore().Trace.Exec(`INSERT INTO kernel_delivery_links(id,run_id,step_id,kind) VALUES(?,?,?,'barrier')`, fmt.Sprintf("barrier-%p", activity), "test", "test"); err != nil {
					t.Fatalf("退出前迟到写库 %v", err)
				}
				releases[i]()
			}
			if err := shutdownRecv(t, done, "真实排空后关闭"); err != nil {
				t.Fatal(err)
			}
			if err := a.set.TraceStore().Trace.Ping(); err == nil {
				t.Fatal("退出后没关库")
			}
			t.Log("桌面/群入口先封；审批2次写入与3个受管屏障均退出后才关库")
		})
	}
}

func TestKernelLifecycleSmoke(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(n int) int {
		if n == 1 {
			return 1
		}
		// 0 会在首段前等待取消；后继队列必须正常结束。
		return -1
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	start := time.Now()
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "编的显式任务", Mode: "start", ClientKey: "s1-start"})
	if err != nil {
		t.Fatal(err)
	}
	accepted := time.Since(start)
	watch, err := d.Reader.WatchLocalRun(ctx, key, rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	for {
		v := localRecv(t, watch.Updates())
		if v.Value.Text != "" {
			break
		}
	}
	steer, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "编的插话", Mode: "steer", ClientKey: "s1-steer", ExpectedRunID: rec.RunID})
	if err != nil {
		t.Fatal(err)
	}
	q, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "编的下一工作", Mode: "queue", ClientKey: "s1-queue"})
	if err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now()
	if _, err = d.Reader.StopLocalRun(ctx, key, rec.RunID, steer.Sequence); err != nil {
		t.Fatal(err)
	}
	var terminal api.LocalRunSnapshot
	for {
		v := localRecv(t, watch.Updates())
		if v.Value.Drained {
			terminal = v.Value
			break
		}
	}
	if terminal.Outcome != "stopped" {
		t.Fatalf("停止被晚完成覆盖 %+v", terminal)
	}
	if terminal.InputID != rec.InputID || terminal.RootID != rec.RunID || terminal.RunID != rec.RunID {
		t.Fatalf("停止快照串了身份链 %+v receipt=%+v", terminal, rec)
	}
	oldExit := time.Since(stoppedAt)
	next, err := d.Reader.WatchLocalRun(ctx, key, q.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		v := localRecv(t, next.Updates())
		if v.Value.Drained {
			if v.Value.Outcome != "done" {
				t.Fatalf("新队列未完成 %+v", v)
			}
			if v.Value.InputID != q.InputID || v.Value.RootID != q.RunID || v.Value.RunID != q.RunID || q.RunID == rec.RunID {
				t.Fatalf("新队列未建立独立工作根 %+v receipt=%+v", v, q)
			}
			break
		}
	}
	next.Close()
	watch.Close()
	a.hub.Seal()
	if err = a.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	requests := len(e.bodies)
	e.mu.Unlock()
	if requests != 2 {
		t.Fatalf("停止/插话触发额外请求 %d", requests)
	}
	t.Logf("实际App/Hub＋假模型/假渠道（未启动适配器）：input=%s root=%s run=%s steer=%s queue=%s requests=%d accept=%v stop-to-exit=%v total=%v", rec.InputID, terminal.RootID, terminal.RunID, steer.InputID, q.RunID, requests, accepted, oldExit, time.Since(start))
}
