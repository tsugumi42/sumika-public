package app

// 整包测试（规格-Go后台装配 第八节第 4 步）：一条群消息从假协议端进来，走完入库、闸门、内核，写成试运行计划、一帧都不发；
// 私聊只入库；档位钳制；令牌取不到就报错；关机再开；self_id 对不上的帧不入库；复读接下的一批补记到闸门。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// waitFor 等一件事发生（异步的入库、回合、写计划）；超时就把等的什么说出来
func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("60 秒内没等到：%s", what)
}

// eventCount 是 QQ 库里这个会话的事件条数
func (e *testEnv) eventCount(a *App, conv string) (int, error) {
	st, err := a.set.For("qq")
	if err != nil {
		return 0, err
	}
	var n int
	err = st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ?`, conv).Scan(&n)
	return n, err
}

// planKinds 是这个会话写了哪些发送计划
func (e *testEnv) planKinds(a *App, conv string) ([]string, error) {
	st, err := a.set.For("qq")
	if err != nil {
		return nil, err
	}
	rows, err := st.Events.Query(`SELECT kind FROM outbound_plans WHERE conversation = ? ORDER BY created_at`, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// latestDecision 是这个会话最近的一条闸门判断
func (e *testEnv) latestDecision(a *App, conv string) (store.GateDecisionRow, bool, error) {
	rows, err := a.set.TraceStore().GateDecisions(conv, 5)
	if err != nil || len(rows) == 0 {
		return store.GateDecisionRow{}, false, err
	}
	return rows[0], true, nil
}

// expectNoActions 在关机以后再查一次：整个测试里假协议端一帧 action 都没收到
func (e *testEnv) expectNoActions(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { e.peer.assertNoActions(t) })
}

// 群消息（@ 她）走完 入库 → 闸门放行 → 内核 → 试运行计划，一帧都不发
func TestGroupMessageGoesToDryRunPlan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.expectNoActions(t)
	a := e.start()
	conv := "qq:group:" + testGroup
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "501", testOwner, "在吗"))
	// 她试运行的那一句也写成事件（outbound_plans.event_seq），所以这里只要求"至少一条"
	waitFor(t, "群消息入库（qq.db 的 events）", func() bool {
		n, err := e.eventCount(a, conv)
		return err == nil && n >= 1
	})
	waitFor(t, "写成一条试运行计划", func() bool {
		kinds, err := e.planKinds(a, conv)
		return err == nil && len(kinds) == 1 && kinds[0] == store.PlanKindDryRun
	})
	waitFor(t, "判断结局是 dry", func() bool {
		d, ok, err := e.latestDecision(a, conv)
		return err == nil && ok && d.Outcome == store.GateOutcomeDry
	})
	if n := e.llmCalls(); n == 0 {
		t.Fatalf("闸门放行了，但假模型一次都没被调用")
	}
}

// 私聊（主人发的）在 qq.reply 没开、发送条件不齐时只入库，不交给内核
func TestPrivateMessageStoredNotAnswered(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.start()
	conv := "qq:private:" + testAccount + ":" + testOwner
	conn := e.peer.accept(t)
	e.peer.send(t, conn, privateFrame(e.now, testAccount, "502", testOwner, "在吗"))
	waitFor(t, "私聊入库", func() bool {
		n, err := e.eventCount(a, conv)
		return err == nil && n >= 1
	})
	// Intake 和 Responder 的路由队列都空了以后，Respond 入口计数就不会再晚到。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.intake.Idle(ctx); err != nil {
		t.Fatalf("等入库做完：%v", err)
	}
	if err := a.responder.Idle(ctx); err != nil {
		t.Fatalf("等回合做完：%v", err)
	}
	if n := a.kernel.calls.Load(); n != 0 {
		t.Fatalf("qq.reply 没开时私聊只存不回，却调用了 Respond %d 次", n)
	}
	kinds, err := e.planKinds(a, conv)
	if err != nil {
		t.Fatalf("读发送计划：%v", err)
	}
	if len(kinds) != 0 {
		t.Fatalf("私聊不该写发送计划，写的是 %v", kinds)
	}
}

// 群里设成"说话"，发送启用条件不齐时也照样按试运行办（qqSendMode 的钳制）
func TestSpeakModeClampedToDryRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.expectNoActions(t)
	a := e.open()
	conv := "qq:group:" + testGroup
	if err := a.set.TraceStore().SetGateMode(conv, "speak", e.now.UnixMilli()); err != nil {
		t.Fatalf("把群的档位设成 speak：%v", err)
	}
	if m := a.modeOf(types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}); m != channel.ModeDryRun {
		t.Fatalf("档位函数对 QQ 群算出的是 %q，发送条件不齐时应该是 %q", m, channel.ModeDryRun)
	}
	e.run(a)
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "503", testOwner, "在吗"))
	waitFor(t, "写成一条试运行计划", func() bool {
		kinds, err := e.planKinds(a, conv)
		return err == nil && len(kinds) == 1 && kinds[0] == store.PlanKindDryRun
	})
	waitFor(t, "判断结局是 dry", func() bool {
		d, ok, err := e.latestDecision(a, conv)
		return err == nil && ok && d.Outcome == store.GateOutcomeDry
	})
}

// 令牌：配置没写、密钥也没有 → 报错说缺什么；${变量} 展开不出来 → 报错；两种错误里都不带令牌原文
func TestOpenTokenErrors(t *testing.T) {
	t.Parallel()
	t.Run("配置没写令牌、密钥也没有", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.writeConfig("")
		_, err := Open(context.Background(), e.options())
		if err == nil {
			t.Fatalf("没有令牌也 Open 成功了")
		}
		if !strings.Contains(err.Error(), "没有访问令牌") {
			t.Fatalf("错误里没说缺什么：%v", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("错误里带上了令牌原文：%v", err)
		}
	})
	t.Run("配置写 ${变量} 但变量没设", func(t *testing.T) {
		t.Parallel()
		const name = "SUMIKA_TEST_UNSET_TOKEN_VAR"
		if v := os.Getenv(name); v != "" {
			t.Fatalf("测试要求 %s 没设，现在是 %q", name, v)
		}
		e := newEnv(t)
		e.writeConfig("${" + name + "}-" + testToken)
		_, err := Open(context.Background(), e.options())
		if err == nil {
			t.Fatalf("变量没设也 Open 成功了")
		}
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("错误里没说缺哪个变量：%v", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("错误里带上了令牌原文：%v", err)
		}
	})
}

// 关机：ctx 取消以后 Run 在限时内返回，库都关干净了（同一个数据目录能再开一次）
func TestShutdownThenReopen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	e.peer.accept(t) // 连上了再关，走的是"正在收消息"的关机
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("30 秒内 Run 没返回")
	}
	if d := time.Since(start); d > shutdownLimit {
		t.Fatalf("关机用了 %v，超过上限 %v", d, shutdownLimit)
	}
	// 库都关干净了：Windows 上还有句柄开着的话文件挪不动（Go 打开文件不带 FILE_SHARE_DELETE）
	for _, f := range []string{"go/chat/qq.db", "go/chat/local.db", "go/trace.db"} {
		file := filepath.Join(e.data, filepath.FromSlash(f))
		if err := os.Rename(file, file+".moved"); err != nil {
			t.Fatalf("关机以后 %s 还被占着：%v", f, err)
		}
		if err := os.Rename(file+".moved", file); err != nil {
			t.Fatalf("把 %s 挪回来：%v", f, err)
		}
	}

	// 同一个数据目录再开一次：库都关了、没有残留的锁
	b, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("关机以后再 Open 同一个数据目录：%v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- b.Run(ctx2) }()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("第二个 App 的 Run 返回错误：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("第二个 App 30 秒内没关完")
	}
}

// 帧里的 self_id 和 -account 对不上（连错了端点）：记日志、不入库
func TestForeignSelfIDDropped(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.expectNoActions(t)
	a := e.start()
	conn := e.peer.accept(t)
	// 先推一条对不上的，再推一条正经的私聊：同一个连接上按先后到，前一条到这时已经处理过了
	e.peer.send(t, conn, groupAtFrame(e.now, "999", "601", testOwner, "在吗"))
	privateConv := "qq:private:" + testAccount + ":" + testOwner
	e.peer.send(t, conn, privateFrame(e.now, testAccount, "602", testOwner, "在吗"))
	waitFor(t, "后面那条私聊入库", func() bool {
		n, err := e.eventCount(a, privateConv)
		return err == nil && n == 1
	})
	if n, err := e.eventCount(a, "qq:group:"+testGroup); err != nil || n != 0 {
		t.Fatalf("self_id 对不上的帧入库了（%d 条，err=%v）", n, err)
	}
	// 接收端先按 -account 拦下（号打码）再交入库，日志里看得到这一句
	waitFor(t, "记一行号对不上的日志", func() bool { return e.logContains("对不上，不入库") })
	if !e.logContains("收到号 ***99 的一条消息") {
		t.Fatalf("号对不上的日志里号码要打码成 ***99，现在的日志：%v", e.logSnapshot())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.intake.Idle(ctx); err != nil {
		t.Fatalf("等入库做完：%v", err)
	}
	if err := a.responder.Idle(ctx); err != nil {
		t.Fatalf("等回合做完：%v", err)
	}
	if n := e.llmCalls(); n != 0 {
		t.Fatalf("self_id 对不上的帧往下走了：假模型被调了 %d 次", n)
	}
}

// 复读接下一批时补调闸门的 TurnDone（6a）：这小时说了几次加一、空闲退避清零；没接下的不记
func TestRepeatTurnDoneReachesGate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open() // 只用到闸门的 TurnDone，不连网
	t.Cleanup(a.cleanup)
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	key := testChar + "|" + conv.String()
	seed := func() string {
		until := e.now.Add(10 * time.Minute).UnixMilli()
		return fmt.Sprintf(`{"rounds":[],"forced":[],"replies":[],"roundAt":null,"lastForcedAt":null,"idle":{"count":2,"until":%d}}`, until)
	}
	if err := a.set.TraceStore().SaveGateState(key, seed(), 0, e.now.UnixMilli()); err != nil {
		t.Fatalf("放一份窗口状态：%v", err)
	}
	took := types.Interceptor{Name: "repeat", Priority: 20,
		Occasions: []types.Occasion{{Scene: types.SceneGroup}}, Triggers: []string{types.TriggerMessage},
		Intercept: func(context.Context, types.Batch) types.Verdict { return types.Took("接下了（编的）") }}
	wrapped := countRepeatTurnDone(took, a.gate.TurnDone)
	if v := wrapped.Intercept(context.Background(), types.Batch{Conv: conv, Character: testChar, Now: e.now}); !v.Taken {
		t.Fatalf("包装以后的拦截插件把接下的结果改了：%+v", v)
	}
	st := gateStateOf(t, a, key)
	if len(st.Replies) != 1 {
		t.Fatalf("复读接下一批以后，这小时说了几次应该是 1，是 %d", len(st.Replies))
	}
	if st.Idle.Count != 0 || st.Idle.Until != 0 {
		t.Fatalf("复读接下以后空闲退避应该清零，是 %+v", st.Idle)
	}

	// 没接下的一批不记进去
	if err := a.set.TraceStore().SaveGateState(key, seed(), 0, e.now.UnixMilli()); err != nil {
		t.Fatalf("再放一份窗口状态：%v", err)
	}
	seedUntil := gateStateOf(t, a, key).Idle.Until
	passed := took
	passed.Intercept = func(context.Context, types.Batch) types.Verdict { return types.Passed("不接（编的）") }
	wrapped = countRepeatTurnDone(passed, a.gate.TurnDone)
	wrapped.Intercept(context.Background(), types.Batch{Conv: conv, Character: testChar, Now: e.now})
	st = gateStateOf(t, a, key)
	if len(st.Replies) != 0 || st.Idle.Count != 2 || st.Idle.Until != seedUntil {
		t.Fatalf("没接下的那一批不该动闸门的窗口状态，现在是 %+v", st)
	}
}

// gateStateOf 读一个（角色，会话）的窗口状态（闸门存成 JSON）
func gateStateOf(t *testing.T, a *App, key string) channel.GateState {
	t.Helper()
	raw, _, ok, err := a.set.TraceStore().LoadGateState(key)
	if err != nil {
		t.Fatalf("读窗口状态 %s：%v", key, err)
	}
	if !ok {
		t.Fatalf("库里没有窗口状态 %s", key)
	}
	var st channel.GateState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("窗口状态 %s 解不开：%v", key, err)
	}
	return st
}

// group.frequency 是 0 的时候往 gate_rules 写一条 "*" 的 0（闸门的 0 是"用默认 0.3"，光给配置会悄悄变成 0.3）
func TestZeroFrequencyWritesStarRule(t *testing.T) {
	t.Parallel()
	t.Run("频率 0 写 \"*\" 的 0", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.writeConfigFreq(testToken, "0")
		a := e.open()
		t.Cleanup(a.cleanup)
		rows, err := a.set.TraceStore().GateRules("qq:group:" + testGroup)
		if err != nil {
			t.Fatalf("读 gate_rules：%v", err)
		}
		for _, r := range rows {
			if r.Conversation == "*" && r.Slot == "" && r.Frequency == 0 {
				return
			}
		}
		t.Fatalf("频率是 0 时要写一条 \"*\" 的 0，现在 gate_rules 里是 %+v", rows)
	})
	t.Run("频率不是 0 不写", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.open()
		t.Cleanup(a.cleanup)
		rows, err := a.set.TraceStore().GateRules("qq:group:" + testGroup)
		if err != nil {
			t.Fatalf("读 gate_rules：%v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("频率不是 0 不该往 gate_rules 写东西，现在是 %+v", rows)
		}
	})
}

// qq:<号>:conn 的提醒写进 QQ 库的 qq_alerts（down / up，试运行统计要靠它排除掉线时段），别的键只记日志
func TestConnAlertsRecordedToQQAlerts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	t.Cleanup(a.cleanup)
	key := "qq:" + testAccount + ":conn"
	a.alerts.Raise(alert.Alert{Key: key, Title: "编的掉线", Body: "编的原因"})
	waitFor(t, "掉线记进 qq_alerts", func() bool {
		rows, err := e.qqAlerts(a)
		return err == nil && len(rows) == 1
	})
	a.alerts.Clear(key, alert.Note{Title: "编的恢复", Body: "编的好了"})
	waitFor(t, "恢复记进 qq_alerts", func() bool {
		rows, err := e.qqAlerts(a)
		return err == nil && len(rows) == 2
	})
	rows, err := e.qqAlerts(a)
	if err != nil {
		t.Fatalf("读 qq_alerts：%v", err)
	}
	wantAt := e.now.UnixMilli()
	if r := rows[0]; r.Kind != "down" || r.Text != "编的掉线：编的原因" || r.At != wantAt || r.Detail != nil {
		t.Fatalf("down 那一行是 %+v", r)
	}
	if r := rows[1]; r.Kind != "up" || r.Text != "编的恢复：编的好了" || r.At != wantAt || r.Detail != nil {
		t.Fatalf("up 那一行是 %+v", r)
	}

	// 别的键只记日志，不写这张表
	a.alerts.Raise(alert.Alert{Key: "guard:route", Title: "编的守卫换路了", Body: "编的原因"})
	waitFor(t, "别的键记一行日志", func() bool { return e.logContains("编的守卫换路了") })
	if rows, err := e.qqAlerts(a); err != nil || len(rows) != 2 {
		t.Fatalf("别的键不该写 qq_alerts（现在 %d 行，err=%v）", len(rows), err)
	}
}

// qqAlerts 是 QQ 库里这个号的提醒
func (e *testEnv) qqAlerts(a *App) ([]store.QQAlert, error) {
	st, err := a.set.For("qq")
	if err != nil {
		return nil, err
	}
	return st.QQAlerts(testAccount, 0)
}

func (e *testEnv) logSnapshot() []string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return append([]string(nil), e.logs...)
}

func (e *testEnv) logContains(sub string) bool {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	for _, l := range e.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// 缺库的角色启动时自动建一个空库并挂上记忆（规格-记忆库从零建库 验收 1）
func TestMemoriesAutoCreatedWhenMissing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	t.Cleanup(a.cleanup)
	db := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("缺库的角色没有自动建库：%v", err)
	}
	if m := a.memories[testChar]; m == nil {
		t.Fatal("自动建库以后该挂上记忆")
	}
	if !e.logContains("已新建空库") {
		t.Fatalf("新建要在日志里说一声，现在的日志：%v", e.logSnapshot())
	}
}
