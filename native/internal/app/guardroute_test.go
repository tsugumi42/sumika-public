package app

// 守卫走退路的提醒（guardroute.go，ADR-186 第 4 条）。
// 单元层用假守卫测"按角色分开、路线变了才提醒、哪些读数不算数"；整包层用假协议端、会坏的假嵌入测
// 嵌入状态仍是就绪、单次调用失败时，QQ 群、QQ 私聊照常发出且只提醒一次，本地群聊、复读同样提醒，确认模型挂了也提醒。
// 名字、号、记忆正文都是编的。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/alert"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/repeat"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/embed"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
)

// 包一层以后三处检查要的接口一个都不能少：少了 CheckRepeat，复读会退回带确认的完整检查；少了 GuardRoute，回合的路线日志没了。
var (
	_ channel.Guard       = (*routedGuard)(nil)
	_ channel.GuardRouter = (*routedGuard)(nil)
	_ repeat.Guard        = (*routedGuard)(nil)
	_ repeat.RepeatGuard  = (*routedGuard)(nil)
)

// ---- 单元层 ----

type fakeCore struct {
	mu    sync.Mutex
	route string
	refs  []string
	err   error
	calls []string // "outbound" / "repeat"
}

func (f *fakeCore) set(route string, refs []string, err error) {
	f.mu.Lock()
	f.route, f.refs, f.err = route, refs, err
	f.mu.Unlock()
}
func (f *fakeCore) result(kind string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind)
	return f.refs, f.err
}
func (f *fakeCore) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	return f.result("outbound")
}
func (f *fakeCore) CheckRepeat(context.Context, string, audience.Audience) ([]string, error) {
	return f.result("repeat")
}
func (f *fakeCore) GuardRoute() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.route
}

var routeBarrier atomic.Int32

// routeChanges 收齐订阅里这个键的变化种类：先放一个标记，标记之前的都已经送到。
func routeChanges(t *testing.T, hub *alert.Hub, sub *alert.Sub, key string) []alert.Change {
	t.Helper()
	barrier := "test:barrier:" + key + ":" + strconv.Itoa(int(routeBarrier.Add(1)))
	hub.Raise(alert.Alert{Key: barrier, Title: "状态已送完"})
	var out []alert.Change
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case c := <-sub.C():
			if c.Alert.Key == barrier {
				return out // 标记不清掉，每次用新键，免得下一次先收到它的恢复通知
			}
			if c.Alert.Key == key {
				out = append(out, c)
			}
		case <-deadline.C:
			t.Fatalf("提醒没送完，已收到 %d 条", len(out))
		}
	}
}

func kindsOf(cs []alert.Change) []alert.Kind {
	var out []alert.Kind
	for _, c := range cs {
		out = append(out, c.Kind)
	}
	return out
}

func wantKinds(t *testing.T, what string, cs []alert.Change, want ...alert.Kind) {
	t.Helper()
	got := kindsOf(cs)
	if len(got) != len(want) {
		t.Fatalf("%s：得到 %v，要 %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s：得到 %v，要 %v", what, got, want)
		}
	}
}

func newRoutedPair() (*alert.Hub, *alert.Sub, *guardRoutes, func(id string, c *fakeCore) *routedGuard) {
	hub := alert.New()
	routes := &guardRoutes{}
	return hub, hub.Subscribe(), routes, func(id string, c *fakeCore) *routedGuard {
		return &routedGuard{id: id, core: c, routes: routes, hub: hub}
	}
}

// 切到退路 Raise 一次，连着几句都在退路上不重复，恢复 Clear 一次；回合和引用 / 转发动作走 CheckOutbound。
func TestGuardRouteRaiseOnceClearOnce(t *testing.T) {
	t.Parallel()
	hub, sub, _, mk := newRoutedPair()
	defer sub.Close()
	core := &fakeCore{}
	g := mk("甲", core)
	key := guardRouteAlertKey("甲")
	check := func() {
		t.Helper()
		if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
			t.Fatal(err)
		}
	}
	check() // 正常读数：没有提醒
	core.set(channel.GuardRouteEmbedDown, nil, nil)
	for range 4 {
		check()
	}
	core.set(channel.GuardRouteNormal, nil, nil)
	check()
	check()
	if len(hub.Open()) != 0 {
		t.Fatalf("恢复以后还有开着的提醒：%+v", hub.Open())
	}
	cs := routeChanges(t, hub, sub, key)
	wantKinds(t, "退路提醒一次、恢复清一次", cs, alert.Raised, alert.Cleared)
	if cs[0].Alert.Title != "发送前检查暂时只比对原文" || !strings.Contains(cs[0].Alert.Body, "换了说法的私事可能拦不住") {
		t.Fatalf("嵌入挂了的标题和后果没写清：%+v", cs[0].Alert)
	}
	if cs[1].Note.Title == "" {
		t.Fatalf("恢复没有提示：%+v", cs[1])
	}
}

// 两个角色的路线各记各的：一个角色走了退路，另一个角色的正常读数不能把它清掉，也不会替它提醒。
func TestGuardRoutePerCharacter(t *testing.T) {
	t.Parallel()
	hub, sub, _, mk := newRoutedPair()
	defer sub.Close()
	ca, cb := &fakeCore{}, &fakeCore{}
	a, b := mk("甲", ca), mk("乙", cb)
	call := func(g *routedGuard) {
		t.Helper()
		if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
			t.Fatal(err)
		}
	}
	ca.set(channel.GuardRouteEmbedDown, nil, nil)
	call(a)
	call(b) // 乙一直正常：不清甲的提醒
	call(b)
	call(a)
	ka, kb := guardRouteAlertKey("甲"), guardRouteAlertKey("乙")
	if open := hub.Open(); len(open) != 1 || open[0].Key != ka {
		t.Fatalf("乙正常不该影响甲的提醒：%+v", open)
	}
	cb.set(channel.GuardRouteModelDown, nil, nil)
	call(b)
	if open := hub.Open(); len(open) != 2 {
		t.Fatalf("乙也走了退路，应各有一条：%+v", open)
	}
	ca.set(channel.GuardRouteNormal, nil, nil)
	call(a) // 甲恢复：只清甲的
	open := hub.Open()
	if len(open) != 1 || open[0].Key != kb {
		t.Fatalf("甲恢复只清甲的：%+v", open)
	}
	all := routeChanges(t, hub, sub, ka)
	wantKinds(t, "甲", all, alert.Raised, alert.Cleared)
}

// 复读走 CheckRepeat：路线变化同样提醒，而且仍然调到记忆的免确认检查。
func TestGuardRouteRepeatPath(t *testing.T) {
	t.Parallel()
	hub, sub, _, mk := newRoutedPair()
	defer sub.Close()
	core := &fakeCore{route: channel.GuardRouteEmbedDown}
	g := mk("甲", core)
	if _, err := g.CheckRepeat(context.Background(), "编的同一句", audience.Audience{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CheckRepeat(context.Background(), "编的同一句", audience.Audience{}); err != nil {
		t.Fatal(err)
	}
	wantKinds(t, "复读退路", routeChanges(t, hub, sub, guardRouteAlertKey("甲")), alert.Raised)
	if len(core.calls) != 2 || core.calls[0] != "repeat" {
		t.Fatalf("复读没有转给记忆的免确认检查：%v", core.calls)
	}
}

// 确认模型挂了和嵌入挂了是两件事：直接切换时标题不同，各提醒一次，恢复清一次；正文说清后果是多拦而不是漏拦。
func TestGuardRouteModelDownAndSwitch(t *testing.T) {
	t.Parallel()
	hub, sub, _, mk := newRoutedPair()
	defer sub.Close()
	core := &fakeCore{}
	g := mk("甲", core)
	call := func() {
		t.Helper()
		if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
			t.Fatal(err)
		}
	}
	core.set(channel.GuardRouteModelDown, []string{"m1"}, nil)
	call()
	call()
	core.set(channel.GuardRouteEmbedDown, nil, nil)
	call()
	core.set(channel.GuardRouteNormal, nil, nil)
	call()
	cs := routeChanges(t, hub, sub, guardRouteAlertKey("甲"))
	wantKinds(t, "模型挂了、改成嵌入挂了、恢复", cs, alert.Raised, alert.Raised, alert.Cleared)
	if cs[0].Alert.Title != "发送前检查的确认模型没用上" || !strings.Contains(cs[0].Alert.Body, "只按相似度") || !strings.Contains(cs[0].Alert.Body, "多拦") {
		t.Fatalf("模型挂了的标题和后果没写清：%+v", cs[0].Alert)
	}
	if cs[1].Alert.Title == cs[0].Alert.Title {
		t.Fatalf("两条退路标题相同：%q", cs[1].Alert.Title)
	}
}

// 出错的那次、字面命中拦下的那次，记忆里的路线是"正常"，其实没走到嵌入，不能当作恢复。
func TestGuardRouteUninformativeReadingsDoNotClear(t *testing.T) {
	t.Parallel()
	hub, sub, _, mk := newRoutedPair()
	defer sub.Close()
	core := &fakeCore{route: channel.GuardRouteEmbedDown}
	g := mk("甲", core)
	if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
		t.Fatal(err)
	}
	core.set(channel.GuardRouteNormal, nil, errors.New("编的超时"))
	if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err == nil {
		t.Fatal("错误要原样交回")
	}
	core.set(channel.GuardRouteNormal, []string{"m1"}, nil)
	if refs, _ := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); len(refs) != 1 {
		t.Fatalf("拦下的结果要原样交回：%v", refs)
	}
	if len(hub.Open()) != 1 {
		t.Fatalf("出错或字面命中不该清掉退路提醒：%+v", hub.Open())
	}
	core.set(channel.GuardRouteNormal, nil, nil)
	if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
		t.Fatal(err)
	}
	wantKinds(t, "真正走完整三步才算恢复", routeChanges(t, hub, sub, guardRouteAlertKey("甲")), alert.Raised, alert.Cleared)
}

// Archive 模式没有提醒中心（hub 为 nil）：只记路线，不能 panic。
func TestGuardRouteNilHub(t *testing.T) {
	t.Parallel()
	routes := &guardRoutes{}
	g := &routedGuard{id: "甲", core: &fakeCore{route: channel.GuardRouteEmbedDown}, routes: routes}
	if _, err := g.CheckOutbound(context.Background(), "编的话", audience.Audience{}); err != nil {
		t.Fatal(err)
	}
}

// ---- 整包层 ----

// flakyEmbedding 的状态一直是就绪，只有单次 Embed 调用会按开关失败。
type flakyEmbedding struct {
	*guardLocal
	fail       atomic.Bool
	embedCalls atomic.Int32
}

func (e *flakyEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.embedCalls.Add(1)
	if e.fail.Load() {
		return nil, errors.New("编的嵌入故障")
	}
	// 三个方向互相垂直：蓝柜的事、花盆的事、其余的话。只有说到同一件事才过 0.70 的相似度门槛。
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{0, 1, 0}
		switch {
		case strings.Contains(text, "蓝柜"):
			out[i] = []float32{1, 0, 0}
		case strings.Contains(text, "花盆"):
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

// routeApp 照 sendApp("全部齐全") 装一套能真发的 QQ 后台，记忆库里除了主人的事再放一条别人的私事（主人在私聊里也看不见）。
// reply 非空就换一个固定回话的假网关（回合和确认模型共用它）。
func routeApp(t *testing.T, reply string) (*testEnv, *App, *sendPeer, *flakyEmbedding) {
	t.Helper()
	e := newEnv(t)
	e.goSend = true
	e.writeConfig(testToken)
	file := guardFixture(t, e, true)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts VALUES ('101', 'fact', 'active', '九零零九说旧钥匙压在花盆下面了', 'person', 'person:90009', '', 1, 1, NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	peer := &sendPeer{}
	e.peer.onAction = peer.handle
	opt := e.options()
	if reply != "" {
		var bodies []string
		var mu sync.Mutex
		srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: llmtest.Text(reply)}, &bodies, &mu))
		t.Cleanup(srv.Close)
		opt.LLM = llm.New(llm.Magpie(srv.URL))
	}
	emb := &flakyEmbedding{}
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		emb.guardLocal = &guardLocal{cfg: cfg}
		return emb
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	awaitGuardFull(t, a) // 测的是 full 以后的退路；full 要等句向量预热完
	return e, a, peer, emb
}

func routeSpeak(t *testing.T, e *testEnv, a *App) *websocket.Conn {
	t.Helper()
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return startSend(t, e, a)
}

// waitSends 等假协议端收到第 n 次发送 action，并确认没有多发。
func waitSends(t *testing.T, peer *sendPeer, n int32) {
	t.Helper()
	waitFor(t, "协议端收到发送 action", func() bool { return peer.sends.Load() >= n })
	if got := peer.sends.Load(); got != n {
		t.Fatalf("发送次数 %d，要 %d", got, n)
	}
}

// 嵌入状态仍是就绪、单次调用失败：QQ 群照常发出（ADR-186），提醒正好一次；提醒里不带消息原文、不带令牌。
// 同一个群一分钟内再 @ 会被闸门合并，所以这里只走一个回合（回复拆成三段发出，守卫对整个回复判一次）；
// 连发几句不重复、恢复清一次的路径由 QQ 私聊、本地群聊两条测试和单元层负责，这里恢复用同一个角色的守卫直接判一次群的听众。
func TestGuardRouteQQGroupEmbedDown(t *testing.T) {
	t.Parallel()
	e, a, peer, emb := routeApp(t, "第一句话。"+"\n\n"+"第二句话。"+"\n\n"+"第三句话。")
	sub := a.alerts.Subscribe()
	defer sub.Close()
	c := routeSpeak(t, e, a)
	emb.fail.Store(true)
	before := emb.embedCalls.Load()
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "91001", "90001", "在吗"))
	waitSends(t, peer, 3)
	if st, _ := emb.Status(); st != embed.StatusReady {
		t.Fatalf("测试前提：嵌入状态应一直是就绪，现在 %v", st)
	}
	if emb.embedCalls.Load() == before {
		t.Fatal("整个过程没有调过嵌入，测的不是嵌入失败")
	}
	cs := routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar))
	wantKinds(t, "退路只提醒一次", cs, alert.Raised)
	if body := cs[0].Alert.Title + cs[0].Alert.Body + cs[0].Alert.Detail; strings.Contains(body, "第一句话") || strings.Contains(body, testToken) || strings.Contains(body, "蓝柜") || strings.Contains(body, "花盆") {
		t.Fatalf("提醒带了消息原文、记忆或令牌：%+v", cs[0].Alert)
	}
	emb.fail.Store(false)
	aud := audience.Audience{Channel: "qq", Conversation: "qq:group:" + testGroup, Scene: "group"}
	for range 2 {
		if refs, err := a.guardFor(testChar).CheckOutbound(context.Background(), "第一句话。", aud); err != nil || len(refs) != 0 {
			t.Fatalf("嵌入恢复后不该拦：%v %v", refs, err)
		}
	}
	wantKinds(t, "恢复只清一次", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Cleared)
}

// QQ 私聊：主人的私聊里看不见别人的私事，同样走到嵌入、同样照常发出并提醒一次。
func TestGuardRouteQQPrivateEmbedDown(t *testing.T) {
	t.Parallel()
	e, a, peer, emb := routeApp(t, "")
	a.cfg.QQ.Reply = true
	sub := a.alerts.Subscribe()
	defer sub.Close()
	c := routeSpeak(t, e, a)
	emb.fail.Store(true)
	before := emb.embedCalls.Load()
	e.peer.send(t, c, privateFrame(e.now, testAccount, "91011", testOwner, "在吗"))
	waitSends(t, peer, 1)
	e.peer.send(t, c, privateFrame(e.now, testAccount, "91012", testOwner, "在吗"))
	waitSends(t, peer, 2)
	if emb.embedCalls.Load() == before {
		t.Fatal("私聊没有走到嵌入")
	}
	wantKinds(t, "私聊退路只提醒一次", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Raised)
	emb.fail.Store(false)
	e.peer.send(t, c, privateFrame(e.now, testAccount, "91013", testOwner, "在吗"))
	waitSends(t, peer, 3)
	wantKinds(t, "私聊恢复清一次", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Cleared)
}

// 本地群聊：主人在场，别人的私事仍看不见；守卫走到嵌入失败的退路同样提醒。
func TestGuardRouteLocalGroupEmbedDown(t *testing.T) {
	t.Parallel()
	_, a, _, emb := routeApp(t, "")
	sub := a.alerts.Subscribe()
	defer sub.Close()
	emb.fail.Store(true)
	aud := audience.Audience{Channel: "console", Conversation: "console:group:" + testGroup, Scene: "group", PrivateOK: true, OwnerAsked: false}
	g := a.guardFor(testChar)
	for range 3 {
		refs, err := g.CheckOutbound(context.Background(), "嗯，在的。", aud)
		if err != nil || len(refs) != 0 {
			t.Fatalf("嵌入挂了应退回只比原文、照常放行：%v %v", refs, err)
		}
	}
	wantKinds(t, "本地群聊退路", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Raised)
	emb.fail.Store(false)
	if _, err := g.CheckOutbound(context.Background(), "嗯，在的。", aud); err != nil {
		t.Fatal(err)
	}
	wantKinds(t, "本地群聊恢复", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Cleared)
}

// 嵌入挂着时，同一个角色在别的场合（主人私聊没有禁看条目、回复全是标点没有可判的句子）判一句，
// 这些提前返回的没验证过嵌入，不能算恢复：提醒不清，也不因为下一句又在群里挂了而重复提醒。真恢复以后才清一次。
func TestGuardRouteEarlyExitsDoNotClear(t *testing.T) {
	t.Parallel()
	_, a, _, emb := routeApp(t, "")
	sub := a.alerts.Subscribe()
	defer sub.Close()
	emb.fail.Store(true)
	group := audience.Audience{Channel: "console", Conversation: "console:group:" + testGroup, Scene: "group", PrivateOK: true}
	ownerPrivate := audience.Audience{Channel: "console", Conversation: "console:private", Scene: "private", PrivateOK: true}
	g := a.guardFor(testChar)
	check := func(text string, aud audience.Audience) {
		t.Helper()
		if refs, err := g.CheckOutbound(context.Background(), text, aud); err != nil || len(refs) != 0 {
			t.Fatalf("应照常放行：%v %v", refs, err)
		}
	}
	check("嗯，在的。", group)
	for range 2 {
		check("。。。", group)
		check("嗯，在的。", ownerPrivate)
		check("嗯，在的。", group)
	}
	wantKinds(t, "提前返回既不清也不重复提醒", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Raised)
	emb.fail.Store(false)
	check("。。。", group)
	if cs := routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)); len(cs) != 0 {
		t.Fatalf("嵌入还没验证过，不该清：%v", kindsOf(cs))
	}
	check("嗯，在的。", group)
	wantKinds(t, "走完嵌入才清一次", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Cleared)
}

// 复读：repeat 用 guardFor 取守卫，走 CheckRepeat；嵌入失败时复读照常发出，同样提醒一次。
func TestGuardRouteRepeatEmbedDown(t *testing.T) {
	t.Parallel()
	e, a, peer, emb := routeApp(t, "")
	sub := a.alerts.Subscribe()
	defer sub.Close()
	routeSpeak(t, e, a)
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	rep, err := repeat.New(repeat.Config{Out: a.outlet, GuardFor: func(id string) repeat.Guard { return a.guardFor(id) }, Audience: repeatAudience, Rand: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	emb.fail.Store(true)
	before := emb.embedCalls.Load()
	old := types.Msg{Seq: 90001, Time: e.now, Who: "qq:90007", Segs: []types.Seg{{Kind: types.SegText, Text: "编的同一句"}}}
	fresh := old
	fresh.Seq++
	fresh.Who = "qq:90008"
	batch := types.Batch{Conv: conv, Character: testChar, Account: testAccount, Now: e.now, Trigger: types.TriggerMessage, Msgs: []types.Msg{fresh}, Recent: func(int) ([]types.Msg, error) { return []types.Msg{old}, nil }}
	if v := rep.Interceptor().Intercept(context.Background(), batch); !v.Taken {
		t.Fatalf("嵌入挂了复读应退回只比原文、照常接下：%+v", v)
	}
	waitSends(t, peer, 1)
	if emb.embedCalls.Load() == before {
		t.Fatal("复读没有走到嵌入")
	}
	wantKinds(t, "复读退路", routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar)), alert.Raised)
}

// 确认模型挂了（网关回的不是约定的 JSON）：嵌入相似度过了门槛，按相似度单独判，这一句拦下，同时提醒。
func TestGuardRouteModelDownQQGroup(t *testing.T) {
	t.Parallel()
	e, a, peer, emb := routeApp(t, "他那备用电池好像塞蓝柜里了")
	sub := a.alerts.Subscribe()
	defer sub.Close()
	c := routeSpeak(t, e, a)
	before := emb.embedCalls.Load()
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "91021", "90001", "告诉我吧"))
	conv := "qq:group:" + testGroup
	waitFor(t, "这一句被守卫拦下", func() bool {
		kinds, err := e.planKinds(a, conv)
		return err == nil && len(kinds) == 1 && kinds[0] == "blocked"
	})
	if emb.embedCalls.Load() == before {
		t.Fatal("没有走到嵌入，测不到确认模型这一步")
	}
	cs := routeChanges(t, a.alerts, sub, guardRouteAlertKey(testChar))
	wantKinds(t, "确认模型挂了提醒一次", cs, alert.Raised)
	if cs[0].Alert.Title != "发送前检查的确认模型没用上" || strings.Contains(cs[0].Alert.Body, "蓝柜") {
		t.Fatalf("标题不对或带了原文：%+v", cs[0].Alert)
	}
	if peer.sends.Load() != 0 {
		t.Fatalf("拦下的话不该发出：%d", peer.sends.Load())
	}
}
