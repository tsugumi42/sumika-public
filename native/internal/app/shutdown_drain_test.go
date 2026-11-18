package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
)

func shutdownRecv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("关机测试等待超时：%s", what)
		var zero T
		return zero
	}
}

// 卡片已进入真正的步骤持久化路径，先挡在写库之前；取消 Ask 返回不代表写库结束。
func holdApprovalWrite(t *testing.T, a *App) (func(), <-chan approval.Decision, <-chan error) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	persist := a.approvals.Persist
	written := make(chan error, 2)
	a.approvals.Persist = func(r approval.Request, d *approval.Decision) error {
		if d == nil {
			close(entered)
			<-release
		}
		err := persist(r, d)
		written <- err
		return err
	}
	a.approvals.OnRequest = nil
	asked := make(chan approval.Decision, 1)
	go func() {
		asked <- a.approvals.Ask(context.Background(), approval.Request{
			ID: "shutdown-card", ToolCallID: "shutdown-tool", CharacterID: testChar,
			Tool: "write", Title: "写编的文件", CreatedAt: 1, ExpiresAt: 600001,
		}, nil)
	}()
	shutdownRecv(t, entered, "审批开始写库")
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n, err := a.approvals.Drain(ctx); n != 0 || err != nil {
			t.Errorf("测试审批未收好：%d %v", n, err)
		}
	})
	return unblock, asked, written
}

func TestShutdownDrainsApprovalBeforeClosingStore(t *testing.T) {
	t.Parallel()
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint("启动失败清理=", cleanup), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			unblock, asked, written := holdApprovalWrite(t, a)
			done := make(chan error, 1)
			if cleanup {
				go func() { a.cleanup(); done <- nil }()
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() { done <- a.Run(ctx) }()
				e.peer.accept(t)
				cancel()
			}
			// Drain 先取消卡片。Ask 的取消宽限结束后仍卡在写库，关机必须继续等。
			early := false
			select {
			case d := <-asked:
				if d.Kind != "cancelled" {
					t.Errorf("关机审批应取消：%+v", d)
				}
			case err := <-done:
				early = true
				t.Errorf("审批尚未写完就关库返回：%v", err)
			case <-time.After(5 * time.Second):
				t.Error("关机没有取消审批")
			}
			if !early {
				select {
				case err := <-done:
					early = true
					t.Errorf("取消返回以后仍须等步骤写库：%v", err)
				default:
				}
				if err := a.set.TraceStore().Trace.Ping(); err != nil {
					t.Errorf("等待审批期间提前关库：%v", err)
				}
			}
			unblock()
			if err := shutdownRecv(t, written, "等待步骤写成"); err != nil {
				t.Errorf("迟到写库：%v", err)
			}
			if err := shutdownRecv(t, written, "取消结局写成"); err != nil {
				t.Errorf("结局未写成：%v", err)
			}
			if !early {
				if err := shutdownRecv(t, done, "关机返回"); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.set.TraceStore().Trace.Ping(); err == nil {
				t.Fatal("关机返回后库仍开着")
			}
			db, err := sql.Open("sqlite", filepath.Join(e.data, "go", "trace.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var status, decision string
			if err := db.QueryRow(`SELECT status, json_extract(approval_json, '$.decision') FROM steps WHERE id='shutdown-tool'`).Scan(&status, &decision); err != nil {
				t.Fatal(err)
			}
			if status != "cancelled" || decision != "cancelled" {
				t.Fatalf("审批结局不完整：%s %s", status, decision)
			}
			// 关闭准入：关机以后新卡片不会再尝试落库。
			d := a.approvals.Ask(context.Background(), approval.Request{ID: "after-shutdown"}, nil)
			if d.Kind != "cancelled" || d.Reason != "审批已经关闭" {
				t.Fatalf("关机以后仍接审批：%+v", d)
			}
			t.Log("关库前完成 2 次持久化，取消结局完整，关机后拒收新审批")
		})
	}
}

func TestShutdownApprovalDrainDeadline(t *testing.T) {
	t.Parallel()
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint("启动失败清理=", cleanup), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			a.responderWait, a.shutdownWait = 30*time.Millisecond, 2*time.Second
			if cleanup {
				a.shutdownWait = 30 * time.Millisecond
			}
			unblock, asked, written := holdApprovalWrite(t, a)
			done := make(chan error, 1)
			go func() {
				if cleanup {
					a.cleanup()
					done <- nil
				} else {
					done <- a.Close()
				}
			}()
			err := shutdownRecv(t, done, "Drain 到期返回未排空")
			if !cleanup && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("排空失败未报期限：%v", err)
			}
			e.logMu.Lock()
			logs := strings.Join(e.logs, "\n")
			e.logMu.Unlock()
			if !strings.Contains(logs, "审批收尾未完成：剩余 1 张") || !strings.Contains(logs, context.DeadlineExceeded.Error()) {
				t.Errorf("没有记录未收尾数量和错误：%s", logs)
			}
			if err := a.set.TraceStore().Trace.Ping(); err != nil {
				t.Errorf("Drain 到期提前关库：%v", err)
			}
			unblock()
			if err := shutdownRecv(t, written, "到期后挂起的写入退出"); err != nil {
				t.Errorf("保留的库无法完成既有写入：%v", err)
			}
			shutdownRecv(t, written, "到期后的结局写入退出")
			shutdownRecv(t, asked, "到期后审批返回")
			a.shutdownWait = time.Minute // 上面的 30 毫秒只为造到期；重试关机要等真的收尾，不能拿它量负载
			if err := a.Close(); err != nil {
				t.Fatalf("实际退出后重试关机：%v", err)
			}
			t.Log("已检查期限错误、库锁保留、挂起写入成功退出和再次关闭")
		})
	}
}
