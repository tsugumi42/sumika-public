package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/qq"
	"sumika-native/internal/channel/repeat"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/store"
)

type sendEmbedding struct{ *guardLocal }

func (e *sendEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{0, 1}
		if strings.Contains(text, "蓝柜") {
			out[i] = []float32{1, 0}
		}
	}
	return out, nil
}

type sendPeer struct {
	result atomic.Int32
	sends  atomic.Int32
	echo   atomic.Bool
}

func (p *sendPeer) handle(c *websocket.Conn, data []byte) {
	if !isSendAction(string(data)) {
		answerInfo(c, data)
		return
	}
	n := p.sends.Add(1)
	var frame struct {
		Action string `json:"action"`
		Echo   string `json:"echo"`
		Params struct {
			Group   string `json:"-"`
			Message []struct {
				Data struct {
					Text string `json:"text"`
				} `json:"data"`
			} `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if p.echo.Load() {
		if len(frame.Params.Message) == 0 {
			return
		}
		b := fmt.Sprintf(`{"post_type":"message_sent","message_type":"group","self_id":100,"user_id":100,"group_id":300,"message_id":%d,"message":[{"type":"text","data":{"text":%q}}]}`, 98000+n, frame.Params.Message[0].Data.Text)
		_ = c.Write(ctx, websocket.MessageText, []byte(b))
		return // 回包不来，由回声取消等待。
	}
	if p.result.Load() == 3 { // 收下发送帧但永远不回包（测等不到回包的超时）
		return
	}
	if p.result.Load() == 2 {
		_ = c.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"echo":%q,"retcode":1}`, frame.Echo)))
		return
	}
	body := fmt.Sprintf(`{"echo":%q,"retcode":0,"data":{"message_id":%d}}`, frame.Echo, 97000+n)
	if p.result.Load() == 1 {
		body = fmt.Sprintf(`{"echo":%q,"status":"failed","retcode":1}`, frame.Echo)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(body))
}

func sendApp(t *testing.T, kind string) (*testEnv, *App, *sendPeer) {
	t.Helper()
	return sendAppWith(t, kind, func(cfg embed.LocalConfig) LocalEmbedding { return &sendEmbedding{&guardLocal{cfg: cfg}} }, true)
}

// sendAppWith 是 sendApp 的底：嵌入器由调用方给；awaitFull 为真就等句向量预热完、守卫 full 以后才返回
func sendAppWith(t *testing.T, kind string, newEmbed func(embed.LocalConfig) LocalEmbedding, awaitFull bool) (*testEnv, *App, *sendPeer) {
	t.Helper()
	return sendAppFrom(t, newEnv(t), kind, newEmbed, awaitFull)
}

// sendAppFrom 同 sendAppWith，但环境由调用方先备好（换假网关、先写 extraConfig）
func sendAppFrom(t *testing.T, e *testEnv, kind string, newEmbed func(embed.LocalConfig) LocalEmbedding, awaitFull bool) (*testEnv, *App, *sendPeer) {
	t.Helper()
	e.goSend = true
	e.writeConfig(testToken)
	if kind != "守卫nil" {
		guardFixture(t, e, kind != "只有字面")
	}
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	if kind == "protocol不对" {
		if err := os.WriteFile(sendlock.LockFile(e.data), []byte(`{"protocol":2}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "另一边holding" || kind == "自己holding重启" {
		holder := sendlock.HolderBun
		if kind == "自己holding重启" {
			holder = sendlock.HolderGo
		}
		b, err := json.Marshal(sendlock.Record{Protocol: 1, Account: testAccount, Holder: holder, State: sendlock.StateHolding, PID: 90001})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sendlock.LockFile(e.data), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	peer := &sendPeer{}
	e.peer.onAction = peer.handle
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding { return newEmbed(cfg) }
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if awaitFull && kind != "只有字面" && kind != "守卫nil" {
		awaitGuardFull(t, a) // 这些夹具的嵌入是就绪的；full 要等句向量预热完，不是 Open 一返回就有
	}
	return e, a, peer
}

func startSend(t *testing.T, e *testEnv, a *App) *websocket.Conn {
	t.Helper()
	e.run(a)
	c := e.peer.accept(t)
	e.peer.send(t, c, `{"post_type":"meta_event","meta_event_type":"heartbeat","self_id":100}`)
	waitFor(t, "协议端登录核验通过", a.adapter.Ready)
	return c
}

func sendPlan(t *testing.T, a *App, text string) string {
	t.Helper()
	id, err := a.outlet.Send(context.Background(), testChar, types.OriginCharacter, types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}, []types.Part{types.Message(text)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func waitSendPlan(t *testing.T, a *App, id, state string) {
	t.Helper()
	st, _ := a.set.For("qq")
	waitFor(t, "发送计划变成 "+state, func() bool { p, err := st.GetPlan(id); return err == nil && p.State == state })
}
func assertSendTitle(t *testing.T, a *App, title string) {
	t.Helper()
	waitFor(t, "发送提醒变成 "+title, func() bool {
		al, ok := openAlert(a, "qq:"+testAccount+":send")
		return (title == "" && !ok) || (ok && al.Title == title)
	})
}

func TestSend57Conditions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"开关关", "意图不是go", "没有锁", "protocol不对", "另一边holding", "自己holding重启", "守卫nil", "只有字面", "Check为nil", "尚未核验", "全部齐全", "失去锁"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e, a, peer := sendApp(t, kind)
			conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			outCfg := channel.OutletConfig{Stores: a.set, Roster: a.roster, Adapters: []types.Adapter{a.adapter}, Check: a.checkQQSend, OnPart: a.onQQPart, Logf: a.logf}
			switch kind {
			case "开关关":
				a.cfg.QQ.GoSend = false
			case "意图不是go":
				writeSenderIntent(t, e.data, testAccount, sendlock.HolderBun)
			case "没有锁":
				if err := a.sendLock.Release(); err != nil {
					t.Fatal(err)
				}
			case "Check为nil":
				outCfg.Check = nil
			}
			raw := channel.GateModes(a.set, channel.ModeDryRun, a.logf)
			mode := a.qqSendMode(raw, &outCfg)
			outCfg.Mode = func(_, _ string, c types.Conversation) string { return mode(c) }
			probe, err := channel.NewOutlet(outCfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { probe.Close() })
			if kind != "尚未核验" {
				startSend(t, e, a)
			}
			if kind == "失去锁" {
				if mode(conv) != channel.ModeSpeak {
					t.Fatal("交出前没有启用")
				}
				if err := a.sendLock.Release(); err != nil {
					t.Fatal(err)
				}
			}
			id, err := probe.Send(context.Background(), testChar, types.OriginCharacter, conv, []types.Part{types.Message("编的发送验收")})
			if err != nil {
				t.Fatal(err)
			}
			st, _ := a.set.For("qq")
			if kind == "全部齐全" || kind == "自己holding重启" {
				waitFor(t, "假端确认一条群消息", func() bool { p, err := st.GetPlan(id); return err == nil && p.State == store.PlanDone })
				if peer.sends.Load() != 1 {
					t.Fatalf("全齐应发一次，实际 %d", peer.sends.Load())
				}
			} else {
				p, err := st.GetPlan(id)
				if err != nil {
					t.Fatal(err)
				}
				if p.Kind == store.PlanKindSend {
					waitFor(t, "改坏后实际发送结束", func() bool {
						p, err := st.GetPlan(id)
						return err == nil && (p.State == store.PlanDone || p.State == store.PlanFailed || p.State == store.PlanUnknown)
					})
				}
				probe.Close()
				if p.Kind != store.PlanKindDryRun || peer.sends.Load() != 0 {
					t.Fatalf("%s 仍发出去了：kind=%s action=%d", kind, p.Kind, peer.sends.Load())
				}
			}
			for _, scene := range []string{types.ScenePrivate, types.SceneGroup} {
				if got := mode(types.Conversation{Channel: "console", Scene: scene, ID: "90003"}); got != channel.ModeSpeak {
					t.Fatalf("本地 %s 受到 QQ 钳制影响：%s", scene, got)
				}
			}
			if got := mode(types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "90004"}); got != channel.ModeDryRun {
				t.Fatalf("别的群被放开：%s", got)
			}
		})
	}
}

func TestSend57KernelDispatch(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"群说话", "主人私聊", "别人私聊", "昵称冒充主人", "reply关闭", "仍钳制"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e, a, peer := sendApp(t, "全部齐全")
			a.cfg.QQ.Reply = kind != "reply关闭"
			if kind == "仍钳制" {
				a.cfg.QQ.GoSend = false
			}
			conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			c := startSend(t, e, a)
			target := testOwner
			if kind == "别人私聊" || kind == "昵称冒充主人" {
				target = "90005"
				a.cfg.QQ.PrivateAllow = []config.QQID{config.QQID(target)}
			}
			frame := privateFrame(e.now, testAccount, "91001", target, "在吗")
			if kind == "昵称冒充主人" {
				// 只认会话号：昵称写成"主人"加主人的号也不算主人
				frame = strings.Replace(frame, `"nickname":"编的甲"`, `"nickname":"主人`+testOwner+`"`, 1)
				if !strings.Contains(frame, `"nickname":"主人`) {
					t.Fatal("昵称没换上")
				}
			}
			if kind == "群说话" {
				frame = groupAtFrame(e.now, testAccount, "91001", testOwner, "在吗")
			} else {
				conv = types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: target}
			}
			e.peer.send(t, c, frame)
			waitFor(t, "输入入库", func() bool { n, err := e.eventCount(a, conv.String()); return err == nil && n >= 1 })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.intake.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			want := kind == "群说话" || kind == "主人私聊"
			if want {
				waitFor(t, "内核回复并收到发送确认", func() bool {
					st, _ := a.set.For("qq")
					var n int
					err := st.Events.QueryRow(`SELECT count(*) FROM channel_msgs WHERE platform_id='97001'`).Scan(&n)
					return err == nil && n == 1
				})
				if peer.sends.Load() != 1 || a.kernel.calls.Load() != 1 {
					t.Fatalf("应只回一次：action=%d 内核=%d", peer.sends.Load(), a.kernel.calls.Load())
				}
			} else {
				if err := a.responder.Idle(ctx); err != nil {
					t.Fatal(err)
				}
				if peer.sends.Load() != 0 || a.kernel.calls.Load() != 0 {
					t.Fatalf("不该回复：action=%d 内核=%d", peer.sends.Load(), a.kernel.calls.Load())
				}
			}
		})
	}
}

func TestSend57PauseAndEcho(t *testing.T) {
	t.Parallel()
	e, a, peer := sendApp(t, "全部齐全")
	a.cfg.QQ.Reply = true
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c := startSend(t, e, a)
	peer.result.Store(1)
	id := sendPlan(t, a, "编的第一段失败")
	waitSendPlan(t, a, id, store.PlanFailed)
	assertSendTitle(t, a, "有消息发不出去")
	peer.echo.Store(true)
	id = sendPlan(t, a, "编的回声先到")
	waitSendPlan(t, a, id, store.PlanDone)
	assertSendTitle(t, a, "")
	peer.echo.Store(false)
	for i := range 2 {
		id = sendPlan(t, a, fmt.Sprintf("编的失败%d", i))
		waitSendPlan(t, a, id, store.PlanFailed)
	}
	assertSendTitle(t, a, "有消息发不出去")
	if _, paused := a.gate.Paused("qq"); paused {
		t.Fatal("回声确认没有清掉连续失败数")
	}
	id = sendPlan(t, a, "编的第三段失败")
	waitSendPlan(t, a, id, store.PlanFailed)
	assertSendTitle(t, a, "暂停了 QQ 发送")
	before := peer.sends.Load()
	for _, item := range []struct {
		origin string
		conv   types.Conversation
		part   types.Part
	}{
		{types.OriginCharacter, conv, types.Message("暂停的回合")},
		{"automation:poke", conv, types.Part{Kind: types.PartPoke, Ref: "90006"}},
		{"automation:repeater", conv, types.Message("暂停的复读")},
		{types.OriginCharacter, types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}, types.Message("暂停的私聊")},
	} {
		if _, err := a.outlet.Send(context.Background(), testChar, item.origin, item.conv, []types.Part{item.part}); err == nil {
			t.Fatalf("暂停没有拒绝 %s %s", item.origin, item.conv.String())
		}
	}
	rep, err := repeat.New(repeat.Config{Out: a.outlet, GuardFor: func(id string) repeat.Guard { return a.guardFor(id) }, Audience: repeatAudience, Rand: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	old := types.Msg{Seq: 90001, Time: e.now, Who: "qq:90007", Segs: []types.Seg{{Kind: types.SegText, Text: "编的同一句"}}}
	fresh := old
	fresh.Seq++
	fresh.Who = "qq:90008"
	batch := types.Batch{Conv: conv, Character: testChar, Account: testAccount, Now: e.now, Trigger: types.TriggerMessage, Msgs: []types.Msg{fresh}, Recent: func(int) ([]types.Msg, error) { return []types.Msg{old}, nil }}
	if v := rep.Interceptor().Intercept(context.Background(), batch); v.Taken || !strings.Contains(v.Reason, "暂停") {
		t.Fatalf("暂停时复读没有经过出口拒绝：%+v", v)
	}
	poke, err := qq.NewPoke(qq.PokeConfig{Out: a.outlet})
	if err != nil {
		t.Fatal(err)
	}
	batch.Notice = &types.Notice{Kind: types.NoticePoke, Account: testAccount, Actor: "90009", Target: testAccount}
	batch.Trigger = types.NoticePoke
	if v := poke.Interceptor().Intercept(context.Background(), batch); v.Taken || !strings.Contains(v.Reason, "暂停") {
		t.Fatalf("暂停时戳回去没有经过出口拒绝：%+v", v)
	}
	// 真私聊还会到内核；出口 Check 必须挡住它。
	e.peer.send(t, c, privateFrame(e.now, testAccount, "91002", testOwner, "暂停期间在吗"))
	waitFor(t, "暂停私聊进内核", func() bool { return a.kernel.calls.Load() == 1 })
	waitFor(t, "暂停私聊被出口拒绝并结算", func() bool {
		kinds, err := e.planKinds(a, "qq:private:"+testAccount+":"+testOwner)
		return err == nil && len(kinds) == 1 && kinds[0] == store.PlanKindBlocked
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.responder.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	if peer.sends.Load() != before {
		t.Fatalf("暂停后仍发 action：前%d 后%d", before, peer.sends.Load())
	}
	st, _ := a.set.For("qq")
	var repeats int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE origin='automation:repeater'`).Scan(&repeats); err != nil || repeats != 0 {
		t.Fatalf("暂停仍新增复读事件：%d %v", repeats, err)
	}
	a.gate.Resume("qq")
	assertSendTitle(t, a, "")
	peer.result.Store(0)
	id = sendPlan(t, a, "恢复以后能发")
	waitSendPlan(t, a, id, store.PlanDone)
	// 尚未暂停时的 Resume 也必须清第一段失败提醒。
	peer.result.Store(1)
	id = sendPlan(t, a, "恢复后失败")
	waitSendPlan(t, a, id, store.PlanFailed)
	assertSendTitle(t, a, "有消息发不出去")
	a.gate.Resume("qq")
	assertSendTitle(t, a, "")
	waitFor(t, "发送提醒记入 QQ 库", func() bool {
		rows, err := e.qqAlerts(a)
		if err != nil {
			return false
		}
		var failed, paused bool
		for _, r := range rows {
			if r.Kind == "send-failed" && strings.Contains(r.Text, "有消息发不出去") {
				failed = true
			}
			if r.Kind == "send-failed" && strings.Contains(r.Text, "暂停了 QQ 发送") {
				paused = true
			}
			if r.Kind == "down" || r.Kind == "up" {
				t.Fatalf("发送提醒混进掉线时段：%+v", r)
			}
		}
		return failed && paused
	})
}

func TestSend57UnknownAndHealth(t *testing.T) {
	t.Parallel()
	e, a, peer := sendApp(t, "全部齐全")
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c := startSend(t, e, a)
	peer.result.Store(2)
	id := sendPlan(t, a, "编的结果未知")
	waitSendPlan(t, a, id, store.PlanUnknown)
	if _, ok := openAlert(a, "qq:"+testAccount+":send"); ok {
		t.Fatal("未知也 Raise 了发不出去")
	}
	peer.result.Store(1)
	for i := range 2 {
		id = sendPlan(t, a, fmt.Sprintf("未知后明确失败%d", i))
		waitSendPlan(t, a, id, store.PlanFailed)
	}
	if _, paused := a.gate.Paused("qq"); paused {
		t.Fatal("未知被计成了明确失败")
	}
	a.gate.Resume("qq")
	if got := a.modeOf(conv); got != channel.ModeSpeak {
		t.Fatalf("健康时没有启用：%s", got)
	}
	before := peer.sends.Load()
	e.peer.send(t, c, `{"post_type":"meta_event","meta_event_type":"heartbeat","self_id":90010}`)
	waitFor(t, "连接核验健康失效", func() bool { return !a.adapter.Ready() })
	if got := a.modeOf(conv); got != channel.ModeSpeak {
		t.Fatalf("健康变化悄悄改了档位：%s", got)
	}
	id = sendPlan(t, a, "健康失效以后不发")
	waitSendPlan(t, a, id, store.PlanUnknown)
	if peer.sends.Load() != before {
		t.Fatalf("核验失效仍发出了 action：前%d 后%d", before, peer.sends.Load())
	}
}

// 复读没有听众函数不再是 QQ 发送的启用条件（2026-11-07，notes/盘点-插件解耦，ADR-289）：
// 其余条件齐全时普通回复照常发出；复读自己因为没有听众拦下（"守卫没有听众"），一条 action 也不多发。
// 旧写法把它当成第七个条件，会把整个 QQ 发送钳成试运行，下面"普通回复发出"那一步就红。
func TestRepeatAudienceNotASendCondition(t *testing.T) {
	t.Parallel()
	e, a, peer := sendApp(t, "全部齐全")
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	outCfg := channel.OutletConfig{Stores: a.set, Roster: a.roster, Adapters: []types.Adapter{a.adapter}, Check: a.checkQQSend, OnPart: a.onQQPart, Logf: a.logf}
	mode := a.qqSendMode(channel.GateModes(a.set, channel.ModeDryRun, a.logf), &outCfg)
	outCfg.Mode = func(_, _ string, c types.Conversation) string { return mode(c) }
	probe, err := channel.NewOutlet(outCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { probe.Close() })
	startSend(t, e, a)

	rep, err := repeat.New(repeat.Config{Out: probe, GuardFor: func(id string) repeat.Guard { return a.guardFor(id) }, Audience: nil, Rand: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	old := types.Msg{Seq: 90001, Time: e.now, Who: "qq:90007", Segs: []types.Seg{{Kind: types.SegText, Text: "编的同一句"}}}
	fresh := old
	fresh.Seq++
	fresh.Who = "qq:90008"
	batch := types.Batch{Conv: conv, Character: testChar, Account: testAccount, Now: e.now, Trigger: types.TriggerMessage, Msgs: []types.Msg{fresh}, Recent: func(int) ([]types.Msg, error) { return []types.Msg{old}, nil }}
	if v := rep.Interceptor().Intercept(context.Background(), batch); v.Taken || !strings.Contains(v.Reason, "守卫没有听众") {
		t.Fatalf("复读没有听众时应该自己拦下：%+v", v)
	}
	if peer.sends.Load() != 0 {
		t.Fatalf("复读没有听众却发了 %d 条", peer.sends.Load())
	}

	if got := mode(conv); got != channel.ModeSpeak {
		t.Fatalf("复读没有听众不该钳住 QQ 发送：档位 %s", got)
	}
	id, err := probe.Send(context.Background(), testChar, types.OriginCharacter, conv, []types.Part{types.Message("编的普通回复")})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := a.set.For("qq")
	waitFor(t, "普通回复发出", func() bool { p, err := st.GetPlan(id); return err == nil && p.State == store.PlanDone })
	if peer.sends.Load() != 1 {
		t.Fatalf("应该只发普通回复这一条，实际 %d", peer.sends.Load())
	}
}
