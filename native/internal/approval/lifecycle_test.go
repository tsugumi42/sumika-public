package approval

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestApprovalDrainWaitsForWrites(t *testing.T) {
	for _, phase := range []string{"request", "resolved", "grant"} {
		t.Run(phase, func(t *testing.T) {
			a := newApprovals(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var writes int
			a.Persist = func(_ Request, d *Decision) error {
				if (phase == "request" && d == nil) || (phase == "resolved" && d != nil) {
					close(entered)
					<-release
				}
				writes++
				return nil
			}
			requests := make(chan Request, 1)
			a.Subscribe(func(r Request) { requests <- r }, nil)
			asked := make(chan Decision, 1)
			go func() { asked <- a.Ask(context.Background(), card("drain"), &Pending{Key: "write:x", Tool: "write"}) }()
			if phase != "request" {
				approvalRecv(t, requests, "待收尾卡片")
			}
			grantDone := make(chan struct{})
			if phase == "resolved" {
				a.Decide("drain", "once", "")
			} else if phase == "grant" {
				writeFile = func(name string, b []byte, mode os.FileMode) error {
					close(entered)
					<-release
					return os.WriteFile(name, b, mode)
				}
				t.Cleanup(func() { unblock(); approvalRecv(t, grantDone, "授权写入退出"); writeFile = os.WriteFile })
				go func() { defer close(grantDone); a.Decide("drain", "always", "") }()
			}
			approvalRecv(t, entered, "写入卡住")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if n, err := a.Drain(ctx); n != 1 || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("截止时应报 1 张未收尾，实际 %d %v", n, err)
			}
			closedCtx, stopClosed := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer stopClosed()
			if d := a.Ask(closedCtx, card("after-close"), nil); d.Kind != "cancelled" || d.Reason != "审批已经关闭" {
				t.Fatalf("关掉后不能接新审批：%+v", d)
			}
			finishCtx, stopFinish := context.WithTimeout(context.Background(), time.Second)
			defer stopFinish()
			drained := make(chan error, 1)
			go func() {
				n, err := a.Drain(finishCtx)
				if n != 0 {
					err = errors.New("收尾后仍有卡片")
				}
				drained <- err
			}()
			select {
			case err := <-drained:
				t.Fatalf("写入没返回就收尾了：%v", err)
			default:
			}
			unblock()
			if err := approvalRecv(t, drained, "写成后 Drain 返回"); err != nil {
				t.Fatal(err)
			}
			approvalRecv(t, asked, "审批调用退出")
			if writes != 2 {
				t.Fatalf("收尾必须完成等待和结局两次持久化，实际 %d", writes)
			}
			if phase == "grant" && !LoadGrants(a.Grants.file).Has("write:x") {
				t.Fatal("Drain 返回前授权必须已落盘")
			}
			t.Logf("截止时未收尾 1 张；放开后未收尾 0 张，完成 %d 次步骤持久化", writes)
		})
	}
}

func TestApprovalConversationExpiryMatchesExecution(t *testing.T) {
	for _, winner := range []string{"approved", "expired", "failed", "cancelled", "written"} {
		t.Run(winner, func(t *testing.T) {
			a := newApprovals(t)
			requests := make(chan Request, 1)
			resolved := make(chan Decision, 1)
			notifyRelease := make(chan struct{})
			var notifyOnce sync.Once
			unblockNotify := func() { notifyOnce.Do(func() { close(notifyRelease) }) }
			defer unblockNotify()
			a.Subscribe(func(r Request) { requests <- r }, func(_ Request, d Decision) {
				if winner == "written" {
					<-notifyRelease
				}
				resolved <- d
			})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			disk := make(chan Decision, 1)
			a.Persist = func(_ Request, d *Decision) error {
				if d != nil {
					close(entered)
					<-release
					if winner == "failed" {
						return errors.New("批准写库失败")
					}
					disk <- *d
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			asked := make(chan Decision, 1)
			executed := make(chan int, 1)
			go func() {
				d := a.Ask(ctx, card("boundary"), &Pending{Key: "write:x", Conversation: "console:c", Dirs: []string{"outside"}})
				n := 0
				if d.Kind == "approved" && ctx.Err() == nil {
					n++
				}
				executed <- n
				asked <- d
			}()
			approvalRecv(t, requests, "边界审批卡片")
			a.mu.Lock()
			p := a.pending["boundary"]
			a.mu.Unlock()
			if winner == "expired" {
				p.timer.Reset(0)
				approvalRecv(t, p.expired, "计时器提交到期")
				if ok, err := a.Decide("boundary", "conversation", ""); ok || err != nil {
					t.Fatalf("到期后批准应被拒绝：%v %v", ok, err)
				}
			} else {
				if ok, err := a.Decide("boundary", "conversation", ""); !ok || err != nil {
					t.Fatalf("批准认领失败：%v %v", ok, err)
				}
			}
			approvalRecv(t, entered, "结局写库屏障")
			if _, ok := a.Conv.Match("console:c", "", "write:x", ""); ok {
				t.Fatal("工具还没放行就留下对话授权")
			}
			if winner != "expired" {
				// 和计时器使用同一个到期入口；写库屏障尚未放开。
				a.expire(p)
				select {
				case <-p.expired:
					t.Fatal("已认领批准被到期信号覆盖")
				default:
				}
			}
			if winner == "cancelled" {
				cancel()
				if d := approvalRecv(t, asked, "写库未返回时取消"); d.Kind != "cancelled" {
					t.Fatalf("取消未停止：%+v", d)
				}
			}
			unblock()
			if winner == "written" {
				approvalRecv(t, p.persisted, "批准已经写成")
				cancel() // 通知还卡着，Ask 同时看到已写成的批准和停止信号。
			}
			finishCtx, stopFinish := context.WithTimeout(context.Background(), time.Second)
			defer stopFinish()
			if n, err := a.Drain(finishCtx); n != 0 || err != nil {
				t.Fatalf("收尾失败：%d %v", n, err)
			}
			want := winner
			if winner == "failed" {
				want = "denied"
			} else if winner == "written" {
				want = "approved"
			}
			if winner != "cancelled" {
				if d := approvalRecv(t, asked, "竞争后 Ask 结局"); d.Kind != want {
					t.Fatalf("Ask 应为 %s，实际 %+v", want, d)
				}
			}
			wantExec := 0
			if winner == "approved" {
				wantExec = 1
			}
			if n := approvalRecv(t, executed, "工具执行次数"); n != wantExec {
				t.Fatalf("执行次数应为 %d，实际 %d", wantExec, n)
			}
			if _, ok := a.Conv.Match("console:c", "", "write:x", ""); ok != (wantExec == 1) {
				t.Fatalf("未执行的操作不能留下对话授权：%v", ok)
			}
			unblockNotify()
			d := approvalRecv(t, resolved, "订阅方结局")
			if winner == "failed" {
				if d.Kind != "denied" {
					t.Fatalf("写失败还发布批准：%+v", d)
				}
			} else if stored := approvalRecv(t, disk, "写库结局"); stored.Kind != d.Kind || (winner != "cancelled" && d.Kind != want) {
				t.Fatalf("库和通知结局不一致：%+v %+v", stored, d)
			}
		})
	}
}
