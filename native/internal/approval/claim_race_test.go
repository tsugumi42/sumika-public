package approval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestApprovalConversationClaimRacesExpiry(t *testing.T) {
	const rounds = 100
	var approvals, expirations int
	for i := range rounds {
		a := newApprovals(t)
		id := fmt.Sprint(i)
		requested := make(chan struct{}, 1)
		resolved, stored := make(chan Decision, 1), make(chan Decision, 1)
		a.Subscribe(func(Request) { requested <- struct{}{} }, func(_ Request, d Decision) { resolved <- d })
		release := make(chan struct{})
		a.Persist = func(_ Request, d *Decision) error {
			if d != nil {
				<-release
				stored <- *d
			}
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var executions atomic.Int32
		file := filepath.Join(filepath.Dir(a.Grants.file), "执行记录.txt")
		asked := make(chan Decision, 1)
		go func() {
			d := a.Ask(ctx, card(id), &Pending{Key: "write:x", Conversation: "console:c"})
			if d.Kind == "approved" {
				executions.Add(1)
				if err := os.WriteFile(file, []byte("写成"), 0o600); err != nil {
					t.Errorf("第 %d 次执行写文件：%v", i, err)
				}
			}
			asked <- d
		}()
		func() {
			defer close(release)
			approvalRecv(t, requested, "并发审批卡片")
			a.mu.Lock()
			p := a.pending[id]
			a.mu.Unlock()
			start, ready := make(chan struct{}), make(chan struct{}, 2)
			decided, expired := make(chan bool, 1), make(chan struct{}, 1)
			go func() {
				ready <- struct{}{}
				<-start
				ok, err := a.Decide(id, "conversation", "")
				if err != nil {
					t.Errorf("回答：%v", err)
				}
				decided <- ok
			}()
			go func() { ready <- struct{}{}; <-start; a.expire(p); expired <- struct{}{} }()
			approvalRecv(t, ready, "批准就绪")
			approvalRecv(t, ready, "到期就绪")
			close(start)
			ok := approvalRecv(t, decided, "并发批准完成")
			approvalRecv(t, expired, "并发到期完成")
			approvalRecv(t, p.done, "唯一事务结局")
			if (p.result.Kind == "approved") != ok {
				t.Fatalf("第 %d 次认领与事务结局不一致：%v %+v", i, ok, p.result)
			}
		}()
		d := approvalRecv(t, asked, "竞争后执行路径返回")
		persisted, notified := approvalRecv(t, stored, "竞争后写库"), approvalRecv(t, resolved, "竞争后通知")
		cancel()
		if d.Kind != persisted.Kind || d.Kind != notified.Kind {
			t.Fatalf("第 %d 次结局分裂：Ask=%+v 库=%+v 通知=%+v", i, d, persisted, notified)
		}
		wantRuns := int32(0)
		switch d.Kind {
		case "approved":
			approvals++
			wantRuns = 1
		case "expired":
			expirations++
		default:
			t.Fatalf("第 %d 次出现竞争以外的结局：%+v", i, d)
		}
		_, fileErr := os.Stat(file)
		_, grant := a.Conv.Match("console:c", "", "write:x", "")
		if executions.Load() != wantRuns || grant != (wantRuns == 1) || (fileErr == nil) != (wantRuns == 1) || (wantRuns == 0 && !os.IsNotExist(fileErr)) {
			t.Fatalf("第 %d 次执行与授权不一致：decision=%s runs=%d grant=%v fileErr=%v", i, d.Kind, executions.Load(), grant, fileErr)
		}
	}
	t.Logf("同时争锁 %d 次：批准执行 %d 次，到期未执行 %d 次；Ask、库、通知和授权全部一致", rounds, approvals, expirations)
}
