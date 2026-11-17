package main

// 先拿数据锁、后台 Open 成功，再拉 Bun（复核 R2）：Open 失败、宿主进 blocked 时 Bun 不能已经在写本地——
// 那样它就成了一个没有宿主的写者。任何进 blocked 的路径都要把 Bun 收掉，恢复后再拉起来。
// 判断“拉没拉”用监护循环起没起（running），不靠睡一会儿再数子进程：监护是异步拉子进程的，数早了会漏。

import (
	"errors"
	"strings"
	"testing"

	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/datalock"
)

// 数据锁被别的进程占着：Open 失败进 blocked，Bun 一个都没拉、监护没起
func TestHostDataLockHeldNeverStartsBun(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	held, err := datalock.LockData(h.opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	h.start()
	s := log.last()
	if s.Phase != phaseBlocked || !strings.Contains(s.Reason, "锁被另一个进程占用") {
		t.Fatalf("数据锁被占该进 blocked：%+v", s)
	}
	if h.bunSup == nil || h.bunSup.running() {
		t.Fatalf("Open 都没成，Bun 的监护不该起：sup=%v", h.bunSup)
	}
	if s.LocalOK || s.localSendOff() == "" {
		t.Fatalf("blocked 时本地私聊不能显示成可用：%+v", s)
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	if probe.count() != 0 {
		t.Fatalf("没拿到数据锁，一个 Bun 子进程都不该拉：%d 个", probe.count())
	}
}

// 第二个 sumika.exe：第一个在跑（拿着数据锁、Bun 在写），第二个进 blocked，不拉自己的 Bun
func TestHostSecondInstanceNeverStartsItsBun(t *testing.T) {
	t.Parallel()
	h1, log1, probe1 := bunHostFixture(t)
	h1.start()
	t.Cleanup(func() { _ = h1.shutdown() })
	waitHost(t, "第一个实例的 Bun 连上", func() bool { return log1.last().LocalOK })

	h2, log2, probe2 := bunHostOn(t, h1.opt)
	h2.start()
	if s := log2.last(); s.Phase != phaseBlocked {
		t.Fatalf("第二个实例该 blocked：%+v", s)
	}
	if h2.bunSup == nil || h2.bunSup.running() {
		t.Fatal("第二个实例的 Bun 监护不该起")
	}
	if err := h2.shutdown(); err != nil {
		t.Fatal(err)
	}
	if probe2.count() != 0 {
		t.Fatalf("第二个实例不该拉 Bun：%d 个", probe2.count())
	}
	if probe1.count() != 1 || probe1.proc(0).wasKilled() {
		t.Fatalf("第二个实例的失败不该动第一个实例的 Bun：子进程 %d 个", probe1.count())
	}
}

// 启动时 Open 进行中就退出：Bun 一个都不拉
func TestHostShutdownDuringOpenNeverStartsBun(t *testing.T) {
	t.Parallel()
	h, _, probe := bunHostFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	h.beforeOpen = func(backend.Mode) { close(entered); <-release }
	done := make(chan struct{})
	go func() { h.start(); close(done) }()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- h.shutdown() }()
	close(release)
	<-done
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if probe.count() != 0 {
		t.Fatalf("退出时 Open 没成，不该拉 Bun：%d 个", probe.count())
	}
}

// 已经在跑，后来后台重开失败（回 archive 时数据锁被别的进程抢走）进 blocked：Bun 收掉；锁放开、重新开成功以后再拉起来
func TestHostBlockedLaterStopsBunAndRecoveryRestartsIt(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	waitHost(t, "Bun 连上", func() bool { return log.last().LocalOK })
	first := probe.proc(0)

	var other *datalock.DataLock
	h.beforeOpen = func(m backend.Mode) {
		if m == backend.Receive && other == nil {
			var err error
			if other, err = datalock.LockData(h.opt.DataDir); err != nil {
				t.Errorf("测试抢锁失败：%v", err)
			}
		}
	}
	h.startReceive() // 先关 archive，开 receive 时锁被抢走，回 archive 也开不了
	s := log.last()
	if s.Phase != phaseBlocked {
		t.Fatalf("锁被抢走后该 blocked：%+v", s)
	}
	if !first.wasKilled() || h.bunSup.running() {
		t.Fatal("宿主 blocked 了，Bun 该已收掉，不留没有宿主的写者")
	}
	if s.LocalOK || !strings.Contains(s.LocalLine, "已停") {
		t.Fatalf("状态行该说明本地私聊的后台已停：%+v", s)
	}

	// 锁放开，托盘再点一次：后台开成功，Bun 重新拉起来
	if other != nil {
		_ = other.Close()
	}
	h.beforeOpen = nil
	h.startReceive() // receive 开不了（没写协议端地址），回 archive 成功
	waitHost(t, "Bun 重新连上", func() bool { return probe.count() == 2 && log.last().LocalOK })
	if log.last().Phase != phaseArchive {
		t.Fatalf("恢复后该回 archive：%+v", log.last())
	}
	if probe.proc(1).wasKilled() {
		t.Fatal("新拉起的 Bun 不该被收掉")
	}
}

// 找不到 bun：照旧 blocked，监护也不起（旧行为不变）
func TestHostBunMissingStillBlockedWithoutSupervisorRunning(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.resolveBun = func(_ config.Backend, _, _ string) (bunSpec, error) { return bunSpec{}, errors.New("找不到 bun") }
	h.start()
	if log.last().Phase != phaseBlocked || h.bunSup != nil || probe.count() != 0 {
		t.Fatalf("%+v sup=%v procs=%d", log.last(), h.bunSup, probe.count())
	}
	_ = h.shutdown()
}
