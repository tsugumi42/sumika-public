package app

// 本地私聊交给 Bun（local_owner.go，规格-本地私聊切到Bun 第三、八节）：
//   - 锁：go 模式拿 backend.lock 和 local.lock，bun 模式只拿 backend.lock；
//   - bun 模式 local.db 只读、不装本地群聊 / 悄悄话 / 目标 / 定期任务 / 提醒、本地的写方法回“现在由 Bun 管”；
//   - archive 和 receive 都查 data_version；
//   - QQ 一侧不变。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/datalock"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// bunEnv：先用 go 模式把库建好、迁好（Go 在 bun 模式下不建库），再把配置切到 bun
func bunEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	p, err := paths.Open(e.data)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGoWith(p, store.DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	e.configure("backend:\n  local: bun\n")
	return e
}

func localStoreOf(t *testing.T, a *App) *store.Store {
	t.Helper()
	st, ok := a.set.Platform("local")
	if !ok {
		t.Fatal("没有 local 库")
	}
	return st
}

// 每个本地写方法的调用；参数随便给，bun 模式必须在看参数之前就拒绝
func localWriteCalls(r *DesktopReader) map[string]func() error {
	ctx := context.Background()
	k := localKey("console:" + testChar)
	return map[string]func() error{
		"SendLocal":            func() error { return r.SendLocal(ctx, k, "x", nil) },
		"SendLocalImages":      func() error { return r.SendLocalImages(ctx, k, "x", nil, nil) },
		"SendLocalParts":       func() error { return r.SendLocalParts(ctx, k, []api.LocalPart{{Text: "x"}}, nil) },
		"StopLocal":            func() error { return r.StopLocal(ctx, k) },
		"DecideLocal":          func() error { return r.DecideLocal(ctx, k, "id", api.ApproveOnce) },
		"SaveLocalImage":       func() error { _, err := r.SaveLocalImage(ctx, k, nil); return err },
		"SubmitLocal":          func() error { _, err := r.SubmitLocal(ctx, k, api.LocalInputSubmission{}); return err },
		"StopLocalRun":         func() error { _, err := r.StopLocalRun(ctx, k, "run", 0); return err },
		"ReleaseHeldLocal":     func() error { _, err := r.ReleaseHeldLocal(ctx, k, "id"); return err },
		"DiscardHeldLocal":     func() error { return r.DiscardHeldLocal(ctx, k, "id") },
		"WithdrawQueuedLocal":  func() error { return r.WithdrawQueuedLocal(ctx, k, "id") },
		"AnswerLocal":          func() error { return r.AnswerLocal(ctx, k, "id", 0) },
		"AddApprovalRule":      func() error { return r.AddApprovalRule(ctx, "read", "allow", "", "") },
		"RevokeApprovalRule":   func() error { return r.RevokeApprovalRule(ctx, api.ApprovalRule{}) },
		"SetConversationMode":  func() error { return r.SetConversationApprovalMode(ctx, k, "manual") },
		"SetLocalWorkspace":    func() error { _, err := r.SetLocalWorkspace(ctx, k, "."); return err },
		"ClearLocalWorkspace":  func() error { _, err := r.ClearLocalWorkspace(ctx, k); return err },
		"RestoreCheckpoint":    func() error { _, err := r.RestoreCheckpoint(ctx, k, "id", "files", ""); return err },
		"CancelCheckpoint":     func() error { _, err := r.CancelCheckpointRestore(ctx, k, "id"); return err },
		"AddLoop":              func() error { _, err := r.AddLoop(ctx, k, api.LoopSpec{}); return err },
		"EditLoop":             func() error { return r.EditLoop(ctx, k, 1, api.LoopSpec{}) },
		"PauseLoop":            func() error { return r.PauseLoop(ctx, k, 1) },
		"ResumeLoop":           func() error { return r.ResumeLoop(ctx, k, 1) },
		"RemoveLoop":           func() error { return r.RemoveLoop(ctx, k, 1) },
		"CreateReminder":       func() error { return r.CreateReminder(ctx, k, api.ReminderDraft{}) },
		"UpdateReminder":       func() error { return r.UpdateReminder(ctx, k, 1, api.ReminderPatch{}) },
		"FinishReminder":       func() error { return r.FinishReminder(ctx, k, 1, true) },
		"DeleteReminder":       func() error { return r.DeleteReminder(ctx, k, 1) },
		"SendWhisper":          func() error { _, err := r.SendWhisper(ctx, k, nil); return err },
		"CancelWhisper":        func() error { _, err := r.CancelWhisper(ctx, k, "b", "c"); return err },
		"WhisperCancel":        func() error { _, err := r.WhisperCancel(ctx, "b"); return err },
		"SelftestChat":         func() error { _, err := r.SelftestChat(ctx, SelftestRequest{}); return err },
		"SelftestWhisper":      func() error { _, err := r.SelftestWhisper(ctx, SelftestWhisperRequest{}); return err },
		"SlashCommand":         func() error { _, err := r.SlashCommand(ctx, SlashCommandRequest{}); return err },
		"ProactiveCloseThread": func() error { return r.ProactiveCloseThread(ctx, testChar) },
		"ProactiveSnooze":      func() error { return r.ProactiveSnooze(ctx, testChar) },
	}
}

func TestBunModeOwnsNothingLocalInGo(t *testing.T) {
	t.Parallel()
	e := bunEnv(t)
	a := e.start()
	d := a.Desktop()
	t.Cleanup(func() { d.Close() })

	// 锁：Go 拿了 backend.lock，没拿 local.lock（Bun 拿）
	if _, err := datalock.LockData(e.data); !errors.Is(err, datalock.ErrDataInUse) {
		t.Fatalf("bun 模式下 Go 该占着 backend.lock：%v", err)
	}
	l, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatalf("bun 模式下 Go 不该占 local.lock（要留给 Bun）：%v", err)
	}
	_ = l.Close()

	// local 只读，qq 照旧可写
	if !localStoreOf(t, a).ReadOnly() {
		t.Fatal("bun 模式下 local.db 该是只读")
	}
	if qq, ok := a.set.Platform("qq"); !ok || qq.ReadOnly() {
		t.Fatal("qq 库该照旧可写")
	}
	if _, err := localStoreOf(t, a).Events.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("bun 模式下 Go 不该能写 local.db")
	}

	// 不装本地的自动活动：到点触发、开轮的东西一个都没有
	if a.goals != nil || a.loops != nil || a.reminders != nil || a.proactive != nil || a.groups != nil || a.whisper != nil || a.stopExt == nil {
		t.Fatalf("bun 模式不该装本地群聊 / 悄悄话 / 目标 / 定期任务 / 提醒 / 主动联系（要查 data_version）：goals=%v loops=%v reminders=%v proactive=%v groups=%v whisper=%v stopExt=%v",
			a.goals != nil, a.loops != nil, a.reminders != nil, a.proactive != nil, a.groups != nil, a.whisper != nil, a.stopExt != nil)
	}

	// 写方法一律回“现在由 Bun 管”
	for name, call := range localWriteCalls(&d.Reader) {
		if err := call(); !errors.Is(err, api.ErrLocalOwnedByBun) {
			t.Errorf("%s：该回 ErrLocalOwnedByBun，实际 %v", name, err)
		}
	}
	if !strings.Contains(api.ErrLocalOwnedByBun.Error(), "Bun") {
		t.Fatal("错误文字要说清是 Bun 管")
	}
}

// go 模式（默认）：两把锁都拿；写方法不被这条规则拦（邻居不变）；没有 data_version 轮询
func TestGoModeKeepsBothLocksAndWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	if _, err := datalock.LockData(e.data); !errors.Is(err, datalock.ErrDataInUse) {
		t.Fatalf("backend.lock 该被占：%v", err)
	}
	if _, err := datalock.LockLocal(e.data); !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("go 模式该占着 local.lock，手动起的 Bun 拿不到：%v", err)
	}
	if localStoreOf(t, a).ReadOnly() || a.stopExt != nil {
		t.Fatal("go 模式下 local.db 可写、不查 data_version")
	}
	if a.goals == nil || a.loops == nil || a.reminders == nil || a.groups == nil || a.whisper == nil {
		t.Fatal("go 模式照旧装目标、定期任务、提醒、本地群聊、悄悄话")
	}
	for name, call := range localWriteCalls(&d.Reader) {
		if err := call(); errors.Is(err, api.ErrLocalOwnedByBun) {
			t.Errorf("%s：go 模式不该回 ErrLocalOwnedByBun", name)
		}
	}
}

// 手动起了 Bun 写本地（它拿着 local.lock）：go 模式的 Go 起不来，说清是哪把锁
func TestGoModeRefusesWhenLocalLockHeld(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	held, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	_, err = Open(context.Background(), e.options())
	if !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("go 模式 local.lock 被占该拒绝启动并报 ErrLocalInUse：%v", err)
	}
	// 拒绝之后 backend.lock 也要放掉
	l, err := datalock.LockData(e.data)
	if err != nil {
		t.Fatalf("启动失败后 backend.lock 该放掉：%v", err)
	}
	_ = l.Close()
}

// bun 模式下 Go 不建库、不迁移：local.db 不存在就拒绝启动并说原因，不留下半成品
func TestBunModeRefusesWithoutLocalDB(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.configure("backend:\n  local: bun\n")
	_, err := Open(context.Background(), e.options())
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("没有 local.db 该拒绝并说不存在：%v", err)
	}
	l, lerr := datalock.LockData(e.data)
	if lerr != nil {
		t.Fatalf("启动失败后锁该放掉：%v", lerr)
	}
	_ = l.Close()
}

// archive 也一样：bun 模式下 local.db 是别人在写，要查 data_version，不拿 local.lock
func TestBunModeArchivePollsAndDoesNotTakeLocalLock(t *testing.T) {
	t.Parallel()
	e := bunEnv(t)
	opt := e.options()
	opt.Mode, opt.Account = Archive, ""
	opt.ExternalPollEvery = 5 * time.Millisecond
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("Open archive：%v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if a.stopExt == nil {
		t.Fatal("archive 在 bun 模式下也要查 data_version")
	}
	l, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatalf("archive 不该占 local.lock：%v", err)
	}
	_ = l.Close()

	// 另一个“进程”（这里是另一组连接）写 local.db：订阅被叫醒
	sub := localStoreOf(t, a).SubscribeChanges()
	defer sub.Close()
	p, _ := paths.Open(e.data)
	writer, err := store.OpenGoWith(p, store.DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ws, _ := writer.For("console")
	if _, err := ws.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:" + testChar, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "Bun 写的", CreatedAt: 1}, []string{testChar}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Wake():
	case <-time.After(10 * time.Second):
		t.Fatal("Bun 写了 local.db，订阅该被叫醒")
	}
}

// go 模式的 archive 只读：不拿也不建 local.lock（之后切到 receive 才拿）
func TestGoModeArchiveDoesNotTouchLocalLock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	set, err := store.OpenGoWith(mustPaths(t, e.data), store.DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	_ = set.Close()
	opt := e.options()
	opt.Mode, opt.Account = Archive, ""
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	l, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatalf("archive 不该占 local.lock：%v", err)
	}
	_ = l.Close()
}

func mustPaths(t *testing.T, data string) paths.Paths {
	t.Helper()
	p, err := paths.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// local.db 里留着一条“发送中”的计划（上次是 go 模式时没发完）：bun 模式下出口启动不去清只读的库，
// 照常起来，那条计划原样留着（local.db 归 Bun，Go 一个字也不写）
func TestBunModeOutletSkipsReadOnlyStoreWithPendingPlan(t *testing.T) {
	t.Parallel()
	e := bunEnv(t)
	set, err := store.OpenGoWith(mustPaths(t, e.data), store.DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := set.Platform("local")
	if _, err := local.Events.Exec(`INSERT INTO outbound_plans (id, character_id, conversation, account, origin, kind, state, created_at) VALUES ('p1', ?, ?, 'console', 'test', 'reply', 'sending', 1)`, testChar, "console:"+testChar); err != nil {
		t.Fatal(err)
	}
	_ = set.Close()
	a, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("bun 模式下 local.db 里有没发完的计划也该照常启动：%v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	var state string
	if err := localStoreOf(t, a).Events.QueryRow(`SELECT state FROM outbound_plans WHERE id='p1'`).Scan(&state); err != nil || state != "sending" {
		t.Fatalf("Go 不该改只读库里的计划：%q %v", state, err)
	}
}

// 宿主启动时定下的归属（Options.LocalOwner）压过配置文件现在的值：App 里所有判断只看这一个结果，
// 不再各读一次 backend.local（复核 R1：运行中改了 sumika.yml，宿主拉了 Bun、App 却按 go 开了本地的写）
func TestHostDecidedLocalOwnerBeatsConfigFile(t *testing.T) {
	t.Parallel()
	archive := func(e *testEnv, owner string) (*App, error) {
		opt := e.options()
		opt.Mode, opt.Account, opt.LocalOwner = Archive, "", owner
		return Open(context.Background(), opt)
	}
	t.Run("宿主定的是 bun，配置现在写 go：按 bun 走（查 data_version、写方法回 Bun 管），并记一行日志", func(t *testing.T) {
		e := bunEnv(t)
		e.configure("backend:\n  local: go\n")
		a, err := archive(e, "bun")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		if !a.localByBun() || a.stopExt == nil {
			t.Fatalf("该照宿主的决定按 bun 走：byBun=%v stopExt=%v", a.localByBun(), a.stopExt != nil)
		}
		d := a.Desktop()
		t.Cleanup(func() { d.Close() })
		if err := d.Reader.SendLocal(context.Background(), localKey("console:"+testChar), "x", nil); !errors.Is(err, api.ErrLocalOwnedByBun) {
			t.Fatalf("写方法该回 ErrLocalOwnedByBun：%v", err)
		}
		if !e.logContains("backend.local") || !e.logContains("重启 sumika.exe") {
			t.Fatalf("和配置不一致该记一行日志：%v", e.logSnapshot())
		}
	})
	t.Run("宿主定的是 go，配置现在写 bun：按 go 走（不查 data_version），并记一行日志", func(t *testing.T) {
		e := bunEnv(t) // 配置写 bun
		a, err := archive(e, "go")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		if a.localByBun() || a.stopExt != nil {
			t.Fatalf("该照宿主的决定按 go 走：byBun=%v stopExt=%v", a.localByBun(), a.stopExt != nil)
		}
		if !e.logContains("backend.local") {
			t.Fatalf("和配置不一致该记一行日志：%v", e.logSnapshot())
		}
	})
	t.Run("宿主和配置一致、或宿主没给：不多记日志，没给就照配置", func(t *testing.T) {
		e := bunEnv(t)
		a, err := archive(e, "bun")
		if err != nil {
			t.Fatal(err)
		}
		if e.logContains("和启动时的决定") {
			t.Fatalf("一致时不该记不一致的日志：%v", e.logSnapshot())
		}
		_ = a.Close()
		a, err = archive(e, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		if !a.localByBun() {
			t.Fatal("没给 LocalOwner 该照配置（bun）")
		}
	})
	t.Run("乱写的值：拒绝启动，不悄悄当 go", func(t *testing.T) {
		e := newEnv(t)
		if _, err := archive(e, "Bun"); !errors.Is(err, ErrArguments) {
			t.Fatalf("LocalOwner 写错该报 ErrArguments：%v", err)
		}
		l, lerr := datalock.LockData(e.data)
		if lerr != nil {
			t.Fatalf("拒绝后不该留锁：%v", lerr)
		}
		_ = l.Close()
	})
}
