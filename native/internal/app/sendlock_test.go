package app

// QQ 发送锁在装配处的接法（规格-QQ发送上线 5.2、第十一节第 3 步）：qq.goSend 开着、意图是 go，
// 但没有交接记录、或锁在别的进程手里时，发送权接不过来（sendLock 是 nil），进程照常收包、一帧不发；
// 交接齐了才接，关机时先写 released 再放锁。号、群号、句子都是编的，文件只在临时数据目录里。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/store"
)

// writeSenderIntent 写意图文件（<数据目录>/go/qq-sender.json）
func writeSenderIntent(t *testing.T, data, account, holder string) {
	t.Helper()
	dir := filepath.Join(data, "go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 go 目录：%v", err)
	}
	b, err := json.Marshal(sendlock.Intent{Account: account, Holder: holder})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qq-sender.json"), b, 0o644); err != nil {
		t.Fatalf("写意图文件：%v", err)
	}
}

// writeSenderRecord 写一份正常交出以后的交接记录（上一次是 Bun 持有，protocol 1）
func writeSenderRecord(t *testing.T, data, account string) {
	t.Helper()
	dir := filepath.Join(data, "go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 go 目录：%v", err)
	}
	rec := sendlock.Record{Protocol: sendlock.Protocol, Account: account, Holder: sendlock.HolderBun, State: sendlock.StateReleased, PID: 999999, ReleasedAt: 1}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qq-sender.lock"), b, 0o644); err != nil {
		t.Fatalf("写交接记录：%v", err)
	}
}

// readSenderRecord 读锁文件里的交接记录
func readSenderRecord(t *testing.T, data string) sendlock.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, "go", "qq-sender.lock"))
	if err != nil {
		t.Fatalf("读锁文件：%v", err)
	}
	var rec sendlock.Record
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("锁文件里不是交接记录：%q", b)
	}
	return rec
}

// openAlert 在提醒登记处找开着的那条
func openAlert(a *App, key string) (alert.Alert, bool) {
	for _, x := range a.alerts.Open() {
		if x.Key == key {
			return x, true
		}
	}
	return alert.Alert{}, false
}

// sendGroupMessage 让假协议端发一条 @ 她的群消息，等它入库（证明进程照常收包）
func sendGroupMessage(t *testing.T, e *testEnv, a *App) {
	t.Helper()
	conv := "qq:group:" + testGroup
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "501", testOwner, "在吗"))
	waitFor(t, "群消息入库（qq.db 的 events）", func() bool {
		n, err := e.eventCount(a, conv)
		return err == nil && n >= 1
	})
}

// qq.goSend 开着、意图是 go，但没有交接记录（锁文件都没有）：接不过来，提醒一次，照常收包、一帧不发
func TestGoSendWithoutHandoverKeepsClamp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.goSend = true
	e.writeConfig(testToken)
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	e.expectNoActions(t)
	a := e.start()

	if a.sendLock != nil {
		t.Fatalf("没有交接记录，发送权不该接过来")
	}
	al, ok := openAlert(a, "qq:"+testAccount+":lock")
	if !ok || al.Title != "QQ 发送权没有交过来" {
		t.Fatalf("该提醒一次「QQ 发送权没有交过来」，现在是 %+v（开着 %v）", al, ok)
	}
	sendGroupMessage(t, e, a)
	// 这一步本来也钳制：收消息照常，发送类 action 一帧都没有（e.expectNoActions 在关机时再查一次）
	waitFor(t, "写成一条试运行计划", func() bool {
		kinds, err := e.planKinds(a, "qq:group:"+testGroup)
		return err == nil && len(kinds) == 1 && kinds[0] == store.PlanKindDryRun
	})
}

// 锁在别人手里（测试进程自己再开一个句柄拿着）：接不过来，提醒一次，照常收包
func TestGoSendRefusedWhileLocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.goSend = true
	e.writeConfig(testToken)
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	held, err := sendlock.Take(e.data, testAccount)
	if err != nil {
		t.Fatalf("测试自己先拿锁：%v", err)
	}
	defer func() {
		if err := held.Release(); err != nil {
			t.Errorf("测试自己放锁：%v", err)
		}
	}()

	e.expectNoActions(t)
	a := e.start()
	if a.sendLock != nil {
		t.Fatalf("锁在别的句柄手里，发送权不该接过来")
	}
	if al, ok := openAlert(a, "qq:"+testAccount+":lock"); !ok || al.Title != "QQ 发送权没有交过来" {
		t.Fatalf("该提醒一次「QQ 发送权没有交过来」，现在是 %+v（开着 %v）", al, ok)
	}
	sendGroupMessage(t, e, a)
}

// qq.goSend 没开（默认 false）时不去抢锁：哪怕交接都齐了也不占着，Bun 随时拿得回来
func TestGoSendOffDoesNotTakeLock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	a := e.start()

	if a.sendLock != nil {
		t.Fatalf("qq.goSend 没开，不该抢锁")
	}
	if _, ok := openAlert(a, "qq:"+testAccount+":lock"); ok {
		t.Fatalf("qq.goSend 没开不算出错，不该提醒")
	}
	// 记录原样没动，锁也空着
	if rec := readSenderRecord(t, e.data); rec.State != sendlock.StateReleased || rec.Holder != sendlock.HolderBun {
		t.Fatalf("qq.goSend 没开却改了交接记录：%+v", rec)
	}
	h, err := sendlock.Take(e.data, testAccount)
	if err != nil {
		t.Fatalf("qq.goSend 没开，锁该空着：%v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("放掉测试接的那把：%v", err)
	}
}

// 交接齐了：接过来（记录写成 holding、holder go、本进程 pid）；关机时先写 released 再放锁，之后另一个句柄拿得到
func TestGoSendTakesLockAndReleasesOnShutdown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.goSend = true
	e.writeConfig(testToken)
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)

	a, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	if a.sendLock == nil {
		t.Fatalf("交接齐了，发送权该接过来")
	}
	rec := readSenderRecord(t, e.data)
	if rec.State != sendlock.StateHolding || rec.Holder != sendlock.HolderGo || rec.PID != os.Getpid() || rec.Protocol != sendlock.Protocol {
		t.Fatalf("接过来以后的记录是 %+v", rec)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	e.peer.accept(t)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("30 秒内 Run 没返回")
	}

	rec = readSenderRecord(t, e.data)
	if rec.State != sendlock.StateReleased || rec.ReleasedAt == 0 {
		t.Fatalf("关机以后的记录该是 released 加时刻，现在是 %+v", rec)
	}
	h, err := sendlock.Take(e.data, testAccount)
	if err != nil {
		t.Fatalf("关机放锁以后该接得回来：%v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("放掉测试接的那把：%v", err)
	}
}
