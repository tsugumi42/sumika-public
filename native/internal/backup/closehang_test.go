package backup

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// 调度驱动的备份卡在不看 ctx 的读上：Close 里等调度 goroutine 退出也不能无限等（关机、换 exe 不能挂住）
func TestSchedulerCloseDoesNotHang(t *testing.T) {
	f := newFx(t, func(o *Options) {
		o.CloseWait = 150 * time.Millisecond
		o.Media = MediaNone
		o.StartDelay = time.Millisecond
	})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		once.Do(func() { close(started) })
		<-release // 不看 ctx：卡在一次读上
		return nil
	}
	f.svc.Start()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("调度没有开始备份")
	}
	done := make(chan struct{})
	go func() { f.svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("Close 卡住了（调度器驱动的备份卡在读上）")
	}
	close(release)
}

func TestStopAloneDoesNotHangEither(t *testing.T) {
	f := newFx(t, func(o *Options) {
		o.CloseWait = 100 * time.Millisecond
		o.Media = MediaNone
		o.StartDelay = time.Millisecond
	})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	}
	f.svc.Start()
	<-started
	done := make(chan struct{})
	go func() { f.svc.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("Stop 卡住了")
	}
	close(release)
}
