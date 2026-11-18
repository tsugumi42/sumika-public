package app

// 发送门（qqSendMode 七个条件）在 QQ 私聊逐项验证、排队以后条件变了、"没有记忆库"的约定。
// 现有 send57_test.go 的 TestSend57Conditions 主要测群，这里补私聊，以及群和私聊都没测的几种坏法。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/dbguard"
	"sumika-native/internal/embed"
	"sumika-native/internal/store"
)

var (
	moreGroupConv   = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	morePrivateConv = types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}
)

// breakSendState 把已经装好、条件齐全的 App 里的某一个发送条件弄坏。只动 App 自己读的状态（配置、意图文件、
// 锁、嵌入状态、通道门），所以真实的 a.modeOf 和探针出口看到的是同一个坏法。
func breakSendState(t *testing.T, e *testEnv, a *App, kind string) {
	t.Helper()
	switch kind {
	case "开关关":
		a.cfg.QQ.GoSend = false
	case "意图不是go":
		writeSenderIntent(t, e.data, testAccount, sendlock.HolderBun)
	case "意图账号不符":
		writeSenderIntent(t, e.data, "101", sendlock.HolderGo)
	case "意图文件损坏":
		if err := os.WriteFile(sendlock.IntentFile(e.data), []byte("{不是意图"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "意图文件读不了":
		// 同名目录：ReadFile 报的不是"不存在"，走 intentErr 那一支。
		if err := os.Remove(sendlock.IntentFile(e.data)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(sendlock.IntentFile(e.data), 0o700); err != nil {
			t.Fatal(err)
		}
	case "没有意图文件":
		if err := os.Remove(sendlock.IntentFile(e.data)); err != nil {
			t.Fatal(err)
		}
	case "没有锁":
		if err := a.sendLock.Release(); err != nil {
			t.Fatal(err)
		}
	case "守卫降级":
		emb, ok := a.embedding.(*sendEmbedding)
		if !ok {
			t.Fatalf("嵌入不是测试替身：%T", a.embedding)
		}
		emb.set(embed.StatusDown)
		if a.guardFull(testChar) {
			t.Fatal("嵌入掉线以后守卫仍是 full")
		}
	case "暂停":
		for range 3 {
			a.gate.PartEnded("qq", false, true)
		}
		if _, paused := a.gate.Paused("qq"); !paused {
			t.Fatal("连着三段失败没有暂停 QQ")
		}
	default:
		t.Fatalf("不认识的坏法 %q", kind)
	}
}

// 发送启用条件（复读的听众函数 2026-11-07 起不算，见 TestRepeatAudienceNotASendCondition）在 QQ 私聊（主人私聊交内核那条路）逐项不满足：档位是试运行、零 action；
// 再加意图文件读错、意图里的号不是这个号、adapter 为 nil；最后一格是全齐的对照，证明测试的"发出去"那头是通的。
func TestSend57PrivateConditions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{
		// 没有意图文件、守卫 nil / 只有字面 / 降级：主人私聊照常回（ADR-311），见 TestSend57OwnerPrivateNotClampedByGuardOrMissingIntent
		"开关关", "意图不是go", "意图文件损坏", "意图文件读不了", "意图账号不符",
		"没有锁", "protocol不对", "另一边holding", "失去锁",
		"Check为nil", "尚未核验", "adapter为nil", "全部齐全",
	} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sendConditionCase(t, kind, morePrivateConv)
		})
	}
}

// 群上补现有 TestSend57Conditions 没有的几种坏法：意图文件读错（损坏、读不了、没有）、意图里的号不是这个号、adapter 为 nil、嵌入掉线。
func TestSend57GroupIntentAndAdapter(t *testing.T) {
	t.Parallel()
	// 没有意图文件但锁在 Go 手里：照常发（ADR-311 第 3 条），不在这张表里
	for _, kind := range []string{"意图文件损坏", "意图文件读不了", "意图账号不符", "守卫降级", "adapter为nil"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sendConditionCase(t, kind, moreGroupConv)
		})
	}
}

// sendConditionCase 按 kind 把发送条件弄坏（"全部齐全"是对照），在 conv 上用探针出口发一条，要求试运行、零 action。
func sendConditionCase(t *testing.T, kind string, conv types.Conversation) {
	e, a, peer := sendApp(t, kind)
	a.cfg.QQ.Reply = true
	private := conv.Scene == types.ScenePrivate
	if !private {
		if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	outCfg := channel.OutletConfig{Stores: a.set, Roster: a.roster, Adapters: []types.Adapter{a.adapter}, Check: a.checkQQSend, OnPart: a.onQQPart, Logf: a.logf}
	probeOnly := false // 只有探针出口能弄坏的条件：真实 App 的 a.modeOf 看不到
	switch kind {
	case "Check为nil":
		outCfg.Check, probeOnly = nil, true
	case "失去锁":
		// 先让它启用一次（下面），再丢锁。
	case "adapter为nil":
		ad := a.adapter
		a.adapter = nil
		t.Cleanup(func() { a.adapter = ad }) // 先于 a.Close 的清理执行，Close 要用它
	case "守卫nil", "只有字面", "protocol不对", "另一边holding", "尚未核验", "全部齐全":
		// 这几种已经由 sendApp 按 kind 装好
	default:
		breakSendState(t, e, a, kind)
	}
	raw := channel.GateModes(a.set, channel.ModeDryRun, a.logf)
	mode := a.qqSendMode(raw, &outCfg)
	outCfg.Mode = func(_, _ string, c types.Conversation) string { return mode(c) }
	probe, err := channel.NewOutlet(outCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { probe.Close() })
	if kind != "尚未核验" && kind != "adapter为nil" {
		startSend(t, e, a)
	}
	if kind == "失去锁" {
		if mode(conv) != channel.ModeSpeak {
			t.Fatal("交出前没有启用")
		}
		breakSendState(t, e, a, "没有锁")
	}
	if kind == "守卫nil" {
		// "没有记忆库"的约定：角色没有 memory.db 就没有守卫，发送门必须把它钳在试运行。
		if g := a.guardFor(testChar); g != nil {
			t.Fatalf("没有记忆库的角色有了守卫：%T", g)
		}
		if a.guardFull(testChar) {
			t.Fatal("没有记忆库的角色守卫是 full")
		}
	}
	id, err := probe.Send(context.Background(), testChar, types.OriginCharacter, conv, []types.Part{types.Message("编的发送验收")})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := a.set.For("qq")
	if kind == "全部齐全" {
		waitFor(t, "假端确认一条消息", func() bool { p, err := st.GetPlan(id); return err == nil && p.State == store.PlanDone })
		if peer.sends.Load() != 1 {
			t.Fatalf("全齐应发一次，实际 %d", peer.sends.Load())
		}
		if got := mode(conv); got != channel.ModeSpeak {
			t.Fatalf("全齐的 %s 档位不是 speak：%s", conv.Scene, got)
		}
		if private && !a.qqPrivateReplyOK(conv) {
			t.Fatal("全齐时主人私聊没有交内核")
		}
		return
	}
	p, err := st.GetPlan(id)
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	if p.Kind != store.PlanKindDryRun || peer.sends.Load() != 0 {
		t.Fatalf("%s 的 %s 仍发出去了：kind=%s action=%d", kind, conv.Scene, p.Kind, peer.sends.Load())
	}
	if got := mode(conv); got != channel.ModeDryRun {
		t.Fatalf("%s 的 %s 档位不是试运行：%s", kind, conv.Scene, got)
	}
	if private && !probeOnly && a.qqPrivateReplyOK(conv) {
		t.Fatalf("%s 时主人私聊仍交给内核", kind)
	}
}

// 主人真发一条私聊过来：每种条件不满足，都不进内核、不发 action、不建发送计划（只存不回）。
func TestSend57PrivateDispatchClamped(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"开关关", "意图不是go", "意图文件损坏", "意图文件读不了", "意图账号不符", "没有锁"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sendKind := kind
			if kind != "守卫nil" && kind != "只有字面" {
				sendKind = "全部齐全"
			}
			e, a, peer := sendApp(t, sendKind)
			a.cfg.QQ.Reply = true
			if sendKind == "全部齐全" {
				breakSendState(t, e, a, kind)
			}
			c := startSend(t, e, a)
			e.peer.send(t, c, privateFrame(e.now, testAccount, "91001", testOwner, "在吗"))
			waitFor(t, "输入入库", func() bool { n, err := e.eventCount(a, morePrivateConv.String()); return err == nil && n >= 1 })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.intake.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.responder.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			kinds, err := e.planKinds(a, morePrivateConv.String())
			if peer.sends.Load() != 0 || a.kernel.calls.Load() != 0 || err != nil || len(kinds) != 0 {
				t.Fatalf("%s 时主人私聊不该回：action=%d 内核=%d 计划=%v err=%v", kind, peer.sends.Load(), a.kernel.calls.Load(), kinds, err)
			}
		})
	}
}

// 本地私聊、本地群聊不受 QQ 钳制影响：档位原样、checkQQSend 在 QQ 暂停时也放行；档位本来就不是 speak 的 QQ 会话原样返回，不会被抬高。
// QQ 那一半要先让适配器通过登录核验（startSend），不然条件 7 恒不满足，QQ 不管哪项坏都是试运行，证明不了是哪项钳的；
// 「全部齐全」一格是对照：同一套装法下 QQ 群、QQ 私聊都是 speak。
func TestSend57LocalUnaffectedByQQClamp(t *testing.T) {
	t.Parallel()
	local := []types.Conversation{
		{Channel: "console", Scene: types.ScenePrivate, ID: "90003"},
		{Channel: "console", Scene: types.SceneGroup, ID: "90003"},
	}
	for _, kind := range []string{"开关关", "意图不是go", "守卫nil", "没有锁", "暂停", "全部齐全"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sendKind := "全部齐全"
			if kind == "守卫nil" {
				sendKind = kind
			}
			e, a, _ := sendApp(t, sendKind)
			if err := a.set.TraceStore().SetGateMode(moreGroupConv.String(), "speak", e.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if sendKind == "全部齐全" && kind != "全部齐全" && kind != "暂停" {
				breakSendState(t, e, a, kind)
			}
			startSend(t, e, a)
			if kind == "暂停" {
				breakSendState(t, e, a, kind) // 登录核验通过时通道门会清零，暂停要在这之后记
			}
			for _, conv := range local {
				if got := a.modeOf(conv); got != channel.ModeSpeak {
					t.Fatalf("本地 %s 受到 QQ 钳制影响：%s", conv.Scene, got)
				}
				if err := a.checkQQSend(testChar, types.OriginCharacter, conv, nil); err != nil {
					t.Fatalf("本地 %s 被 QQ 的发送检查拒绝：%v", conv.Scene, err)
				}
			}
			for _, conv := range []types.Conversation{moreGroupConv, morePrivateConv} {
				// 暂停不改档位（出口的 Check 另拦），其余几项把 QQ 钳成试运行；全部齐全是 speak。
				wantMode := channel.ModeDryRun
				if kind == "暂停" || kind == "全部齐全" || (kind == "守卫nil" && conv.Scene == types.ScenePrivate) {
					wantMode = channel.ModeSpeak // 守卫没就绪不钳主人私聊（ADR-311 第 1 条）
				}
				if got := a.modeOf(conv); got != wantMode {
					t.Fatalf("QQ %s 的档位不对：%s，要 %s", conv.Scene, got, wantMode)
				}
				err := a.checkQQSend(testChar, types.OriginCharacter, conv, nil)
				if (kind == "暂停") != (err != nil) {
					t.Fatalf("QQ %s 的发送检查结果不对：kind=%s err=%v", conv.Scene, kind, err)
				}
			}
		})
	}
	t.Run("非speak原样返回", func(t *testing.T) {
		t.Parallel()
		_, a, _ := sendApp(t, "全部齐全")
		outCfg := channel.OutletConfig{Check: a.checkQQSend}
		for _, rawMode := range []string{channel.ModeRecord, channel.ModeDryRun} {
			mode := a.qqSendMode(func(types.Conversation) string { return rawMode }, &outCfg)
			for _, conv := range []types.Conversation{moreGroupConv, morePrivateConv} {
				if got := mode(conv); got != rawMode {
					t.Fatalf("QQ %s 原来是 %s，被改成 %s", conv.Scene, rawMode, got)
				}
			}
		}
	})
}

// queueSecond 起一个会堵住第一条发送的假协议端，发一条在途的、再排一条，返回两条计划和放行函数。
func queueSecond(t *testing.T, e *testEnv, a *App, peer *sendPeer, conv types.Conversation) (first, queued string, release func()) {
	t.Helper()
	entered, gate := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	e.peer.onAction = func(c *websocket.Conn, b []byte) {
		if isSendAction(string(b)) {
			enteredOnce.Do(func() { close(entered) })
			<-gate
		}
		peer.handle(c, b)
	}
	startSend(t, e, a)
	send := func(text string) string {
		id, err := a.outlet.Send(context.Background(), testChar, types.OriginCharacter, conv, []types.Part{types.Message(text)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first = send("编的在途消息")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("第一条没有进入套接字")
	}
	queued = send("编的排队消息")
	st, _ := a.set.For("qq")
	p, err := st.GetPlan(queued)
	if err != nil || p.Kind != store.PlanKindSend || len(p.Parts) != 1 || p.Parts[0].State != store.PartPending {
		t.Fatalf("没有排在在途消息后面：%+v err=%v", p, err)
	}
	return first, queued, release
}

// 排队以后条件变了：已经在发的那一段可以完成，排着的不发 action。QQ 群和 QQ 私聊都测；
// 守卫降级、意图撤回（三种）、开关关掉、通道门暂停。
func TestSend57QueuedConditionChanged(t *testing.T) {
	t.Parallel()
	for _, conv := range []types.Conversation{moreGroupConv, morePrivateConv} {
		for _, kind := range []string{"守卫降级", "意图不是go", "意图账号不符", "意图文件损坏", "开关关", "暂停"} {
			t.Run(conv.Scene+"/"+kind, func(t *testing.T) {
				t.Parallel()
				if kind == "守卫降级" && conv.Scene == types.ScenePrivate {
					t.Skip("守卫降级不钳主人私聊（ADR-311 第 1 条），照发由 TestSend57OwnerPrivateNotClampedByGuardOrMissingIntent 覆盖")
				}
				e, a, peer := sendApp(t, "全部齐全")
				if conv.Scene == types.SceneGroup {
					if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
						t.Fatal(err)
					}
				}
				first, queued, release := queueSecond(t, e, a, peer, conv)
				breakSendState(t, e, a, kind)
				release()
				waitSendPlan(t, a, first, store.PlanDone)
				st, _ := a.set.For("qq")
				waitFor(t, "排队的计划结束", func() bool { p, err := st.GetPlan(queued); return err == nil && p.Parts[0].State != store.PartPending })
				a.outlet.Close()
				p, err := st.GetPlan(queued)
				if err != nil || p.Parts[0].State != store.PartAborted || peer.sends.Load() != 1 {
					t.Fatalf("已排队的消息在 %s 之后仍发出：state=%+v action=%d，要只有在途的 1 次，err=%v", kind, p, peer.sends.Load(), err)
				}
			})
		}
	}
}

// “没有记忆库”只剩“建库失败”一种：那个角色守卫是真正的 nil 接口（不是装着空指针的接口），日志和提醒各一条，
// QQ 上被钳住。没有库时启动自动建库，建好后守卫在；嵌入没就绪时群和非主人私聊仍钳住。
// 嵌入就绪、发送七项都满足时，新建的库和原有库结果相同（规格-记忆库从零建库 设计第 4 条）。
func TestSend57NoMemoryDBContract(t *testing.T) {
	t.Parallel()
	t.Run("建库失败", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.goSend = true
		e.configure("memory:\n  write:\n    characters: [" + testChar + "]\n")
		writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
		writeSenderRecord(t, e.data, testAccount)
		opt := e.options()
		opt.NewMemoryDB = func(ctx context.Context, data, file, snapshotDir, relName string) (bool, dbguard.Report, error) {
			return false, dbguard.Report{File: file, Action: dbguard.ActionCreate}, &dbguard.Unavailable{
				File: file, Reason: dbguard.ReasonCreateFailed, Detail: "编的建库失败",
			}
		}
		a, err := Open(context.Background(), opt)
		if err != nil {
			t.Fatalf("建库失败拦住了启动：%v", err)
		}
		t.Cleanup(func() { a.Close() })
		db := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		if _, err := os.Stat(db); !os.IsNotExist(err) {
			t.Fatalf("建库失败留下了文件：%v", err)
		}
		if a.memories[testChar] != nil {
			t.Fatal("建库失败的角色挂上了记忆")
		}
		if g := a.guardFor(testChar); g != nil {
			t.Fatalf("建库失败的角色拿到了守卫：%T", g)
		}
		if a.guardFull(testChar) {
			t.Fatal("建库失败的角色守卫是 full")
		}
		if !e.logContains("没建成") || !e.logContains("no_memory_db") {
			t.Fatalf("建库失败要有日志和写入只读原因：%v", e.logSnapshot())
		}
		found := false
		for _, al := range a.alerts.Open() {
			if al.Key != "memory:create:"+testChar {
				continue
			}
			found = true
			if !strings.Contains(al.Body, db) || !strings.Contains(al.Body, dbguard.ReasonCreateFailed) {
				t.Fatalf("提醒没写路径和原因词：%+v", al)
			}
		}
		if !found {
			t.Fatalf("建库失败没有提醒：%+v", a.alerts.Open())
		}
		if err := a.set.TraceStore().SetGateMode(moreGroupConv.String(), "speak", e.now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		startSend(t, e, a)
		nonOwner := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: "201"}
		for _, conv := range []types.Conversation{moreGroupConv, nonOwner} {
			if got := a.modeOf(conv); got != channel.ModeDryRun {
				t.Fatalf("建库失败的角色在 QQ %s 没被钳住：%s", conv.Scene, got)
			}
		}
	})

	t.Run("没有库自动建，嵌入没就绪仍钳住", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.goSend = true
		e.writeConfig(testToken)
		writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
		writeSenderRecord(t, e.data, testAccount)
		a, err := Open(context.Background(), e.options())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		db := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		if _, err := os.Stat(db); err != nil {
			t.Fatalf("没有库的角色没自动建库：%v", err)
		}
		if a.memories[testChar] == nil || a.guardFor(testChar) == nil {
			t.Fatalf("自动建库后没有守卫：memories=%v guardFor=%v", a.memories[testChar], a.guardFor(testChar))
		}
		if a.guardFull(testChar) {
			t.Fatal("嵌入没就绪，守卫不该是 full")
		}
		if err := a.set.TraceStore().SetGateMode(moreGroupConv.String(), "speak", e.now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		startSend(t, e, a)
		nonOwner := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: "201"}
		for _, conv := range []types.Conversation{moreGroupConv, nonOwner} {
			if got := a.modeOf(conv); got != channel.ModeDryRun {
				t.Fatalf("嵌入没就绪时 %s 没被钳住：%s", conv.Scene, got)
			}
		}
	})

	t.Run("嵌入就绪、七项齐全：新库和原有库结果相同", func(t *testing.T) {
		t.Parallel()
		type trio struct {
			e *testEnv
			a *App
		}
		open := func(t *testing.T, dropDB bool) trio {
			t.Helper()
			e := newEnv(t)
			e.goSend = true
			e.writeConfig(testToken)
			file := guardFixture(t, e, true)
			if dropDB {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
			writeSenderRecord(t, e.data, testAccount)
			peer := &sendPeer{}
			e.peer.onAction = peer.handle
			opt := e.options()
			opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
				return &sendEmbedding{&guardLocal{cfg: cfg}}
			}
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { a.Close() })
			awaitGuardFull(t, a)
			return trio{e: e, a: a}
		}
		existing, created := open(t, false), open(t, true)
		if _, err := os.Stat(filepath.Join(created.e.data, "go", "characters", testChar, "memory.db")); err != nil {
			t.Fatalf("新角色没有自动建库：%v", err)
		}
		for _, app := range []trio{existing, created} {
			if !app.a.guardFull(testChar) {
				t.Fatalf("守卫没到 full：%v", app.a.memories[testChar])
			}
			if err := app.a.set.TraceStore().SetGateMode(moreGroupConv.String(), "speak", app.e.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			startSend(t, app.e, app.a)
		}
		nonOwner := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: "201"}
		for _, conv := range []types.Conversation{moreGroupConv, morePrivateConv, nonOwner} {
			want := existing.a.modeOf(conv)
			if want != channel.ModeSpeak {
				t.Fatalf("条件齐全时原有库的 %s 是 %s，要说话", conv.Scene, want)
			}
			if got := created.a.modeOf(conv); got != want {
				t.Fatalf("新建库的 %s 是 %s，原有库是 %s", conv.Scene, got, want)
			}
		}
	})
}

// memory.db 文件在但打不开：app 打开报错，不当成没有记忆库照跑。
func TestSend57BrokenMemoryDBStopsOpen(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"乱码", "是目录"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "乱码" {
				if err := os.WriteFile(file, []byte(strings.Repeat("这不是 sqlite 文件。", 64)), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(file, 0o700); err != nil {
				t.Fatal(err)
			}
			a, err := Open(context.Background(), e.options())
			if a != nil {
				a.cleanup()
			}
			if err == nil {
				t.Fatal("坏掉的 memory.db 被当成了没有记忆库")
			}
			if !strings.Contains(err.Error(), "记忆") {
				t.Fatalf("错误没说是记忆库的事：%v", err)
			}
			if e.logContains("没有记忆库") {
				t.Fatalf("文件在却说了没有记忆库：%v", e.logSnapshot())
			}
		})
	}
}

// ADR-311 第 1、3 条：守卫没到完整能力（nil、只有字面、降级）不钳主人私聊；运行中意图文件被删、锁仍在 Go 手里，照常回并提醒。
func TestSend57OwnerPrivateNotClampedByGuardOrMissingIntent(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"守卫nil", "只有字面", "守卫降级", "没有意图文件"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sendKind := kind
			if kind != "守卫nil" && kind != "只有字面" {
				sendKind = "全部齐全"
			}
			e, a, _ := sendApp(t, sendKind)
			a.cfg.QQ.Reply = true
			if sendKind == "全部齐全" {
				breakSendState(t, e, a, kind)
			}
			c := startSend(t, e, a)
			e.peer.send(t, c, privateFrame(e.now, testAccount, "91001", testOwner, "在吗"))
			waitFor(t, "主人私聊交给内核", func() bool { return a.kernel.calls.Load() > 0 })
			if kind == "没有意图文件" {
				waitFor(t, "意图文件不见了的提醒", func() bool { _, ok := openAlert(a, "qq:"+testAccount+":intent"); return ok })
			}
			group := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			if err := a.set.TraceStore().SetGateMode(group.String(), "speak", e.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if kind != "没有意图文件" && a.modeOf(group) == channel.ModeSpeak {
				t.Fatalf("%s 时群仍要钳", kind)
			}
		})
	}
}
