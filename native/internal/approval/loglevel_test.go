package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type levelLog struct {
	mu          sync.Mutex
	info, error []string
}

func (l *levelLog) infof(f string, a ...any)  { l.add(&l.info, f, a...) }
func (l *levelLog) errorf(f string, a ...any) { l.add(&l.error, f, a...) }
func (l *levelLog) add(to *[]string, f string, a ...any) {
	l.mu.Lock()
	*to = append(*to, strings.TrimSpace(fmt.Sprintf(f, a...)))
	l.mu.Unlock()
}

// 规格-日志分级 5.1：审批结果写不进库、始终允许没记下，都是“数据丢了、要人处理”，记 ERROR；
// 同文件里“等待审批没记上”早就是 ERROR，这两处以前是 INFO，过滤 --errors 时看不到。
func TestApprovalRecordFailuresAreErrors(t *testing.T) {
	t.Run("结局写不进库", func(t *testing.T) {
		a := newApprovals(t)
		a.Persist = func(_ Request, d *Decision) error {
			if d != nil {
				return errors.New("步骤库写失败")
			}
			return nil
		}
		lg := &levelLog{}
		a.Logf, a.Errorf = lg.infof, lg.errorf
		a.Report = func(string, string) {}
		a.OnRequest = []func(Request){func(r Request) { a.Decide(r.ID, "once", "") }}
		done := make(chan Decision, 1)
		go func() { done <- a.Ask(context.Background(), card("persist"), nil) }()
		approvalRecv(t, done, "写库失败交回原因")
		lg.mu.Lock()
		defer lg.mu.Unlock()
		if len(lg.error) != 1 || !strings.Contains(lg.error[0], "记录审批 persist 失败") || len(lg.info) != 0 {
			t.Errorf("该是一条 ERROR、没有 INFO：error=%v info=%v", lg.error, lg.info)
		}
	})
	t.Run("始终允许没记下", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "占着")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		a := newApprovals(t)
		a.Grants = LoadGrants(filepath.Join(blocker, "tool-grants.yml")) // 父路径是个文件，写不进去
		lg := &levelLog{}
		a.Logf, a.Errorf = lg.infof, lg.errorf
		reported := make(chan string, 1)
		a.Report = func(title, _ string) { reported <- title }
		requests := make(chan Request, 1)
		a.Subscribe(func(r Request) { requests <- r }, func(Request, Decision) {})
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		go a.Ask(ctx, card("grant"), &Pending{Key: "write:a", Label: "这一类", Tool: "write"})
		approvalRecv(t, requests, "卡片")
		if ok, err := a.Decide("grant", "always", ""); ok && err == nil {
			t.Fatal("授权写不进去时 Decide 该交回错误")
		}
		approvalRecv(t, reported, "提醒主人")
		lg.mu.Lock()
		defer lg.mu.Unlock()
		if len(lg.error) != 1 || !strings.Contains(lg.error[0], "始终允许失败") || len(lg.info) != 0 {
			t.Errorf("该是一条 ERROR、没有 INFO：error=%v info=%v", lg.error, lg.info)
		}
	})
}
