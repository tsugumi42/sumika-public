package channel

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/qq"
	"sumika-native/internal/channel/repeat"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 拦截链连起来的测试：假适配器 → 入库一站 → 流水线 → 链（真的复读、真的戳回去）→ 真的出口 → 假适配器，临时库，假钟。
// 号、群、话都是编的。"内核"这一步还没有，用闸门钩子代替：闸门收到的 Taken 为假的，才是会交给内核的。

type mh struct {
	t   *testing.T
	st  *store.Store
	ad  *fake.Adapter
	clk *fake.Clock
	out *Outlet
	in  *Intake
	pl  *Pipeline

	mode atomic.Value // string；空就是说话

	mu      sync.Mutex
	on      map[string]bool // "插件|会话号" → 设过的开关（没有这一项就是没设过，走插件的默认）
	gate    []Dispatch
	recalls []Recalled
	logs    []string

	stopAd context.CancelFunc
	adDone chan struct{}
}

// fullSink 把入库一站接成完整的 Sink：消息和通知它自己收；连接状态这一步还没有接收方（第 9 步才有），这里丢掉
type fullSink struct{ *Intake }

func (fullSink) Status(context.Context, types.Status) error { return nil }

func (h *mh) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

func (h *mh) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func (h *mh) modeOf(types.Conversation) string {
	if m, _ := h.mode.Load().(string); m != "" {
		return m
	}
	return ModeSpeak
}

func (h *mh) setMode(m string) { h.mode.Store(m) }

// enable、disable 把某个插件在某些会话里的开关设成开、设成关（会话号：群是群号，私聊是对方的号）
func (h *mh) enable(plugin string, convIDs ...string)  { h.set(plugin, true, convIDs) }
func (h *mh) disable(plugin string, convIDs ...string) { h.set(plugin, false, convIDs) }

func (h *mh) set(plugin string, on bool, convIDs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range convIDs {
		h.on[plugin+"|"+id] = on
	}
}

func (h *mh) dispatches() []Dispatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Dispatch(nil), h.gate...)
}

func (h *mh) recalled() []Recalled {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Recalled(nil), h.recalls...)
}

type mhOpt func(*mhCfg)

type mhCfg struct {
	roster      Roster // 空就用 testRoster()
	admitNotice func(types.Notice) Admission
	extra       []types.Interceptor
	timeout     time.Duration
	repeat      func(*repeat.Config)
	seen        SeenNotifier
	admit       func(types.Inbound) Admission
}

func newMH(t *testing.T, opts ...mhOpt) *mh {
	t.Helper()
	var cfg mhCfg
	for _, o := range opts {
		o(&cfg)
	}
	roster := cfg.roster
	if len(roster.Accounts) == 0 {
		roster = testRoster()
	}
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	h := &mh{t: t, st: st, clk: fake.NewClock(baseTime), on: map[string]bool{}}
	h.ad = fake.New(chQQ, types.Caps{MediaGroup: types.MediaCaps{Image: true}, Poke: true, Echo: true})
	h.ad.Now = h.clk.Now
	h.out, err = NewOutlet(OutletConfig{Stores: store.Single(st), Roster: roster, Adapters: []types.Adapter{h.ad}, Clock: h.clk, Mode: func(_, _ string, c types.Conversation) string { return h.modeOf(c) }, Logf: h.logf, Seen: cfg.seen})
	if err != nil {
		t.Fatal(err)
	}
	rcfg := repeat.Config{Out: h.out, Rand: func() float64 { return 0 }}
	rcfg.Media = func(_ context.Context, _ types.Conversation, hash string) (*types.MediaResource, error) {
		return types.MemoryMedia(hash, "image/png", 1, 1, []byte{1}), nil
	}
	rcfg.GuardFor = func(string) repeat.Guard { return &fakeGuard{} }
	rcfg.Audience = func(c types.Conversation) (audience.Audience, error) {
		return audience.Audience{Channel: c.Channel, Scene: c.Scene, Conversation: c.String()}, nil
	}
	if cfg.repeat != nil {
		cfg.repeat(&rcfg)
	}
	rep, err := repeat.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := qq.NewPoke(qq.PokeConfig{Out: h.out})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := NewChain(ChainConfig{
		Plugins: append([]types.Interceptor{pk.Interceptor(), rep.Interceptor()}, cfg.extra...),
		Switch: func(plugin, _ string, conv types.Conversation) (on, set bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			on, set = h.on[plugin+"|"+conv.ID]
			return
		},
		Mode: h.modeOf, Clock: h.clk, Timeout: cfg.timeout, Logf: h.logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.pl, err = NewPipeline(PipelineConfig{
		Stores: store.Single(st), Roster: roster, Chain: chain, Logf: h.logf, Seen: cfg.seen,
		Gate:     func(d Dispatch) { h.mu.Lock(); h.gate = append(h.gate, d); h.mu.Unlock() },
		OnRecall: func(r Recalled) { h.mu.Lock(); h.recalls = append(h.recalls, r); h.mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.in, err = NewIntake(Config{
		Stores: store.Single(st), Roster: roster, Echo: h.out, Logf: h.logf, Now: h.clk.Now,
		OnStored: h.pl.OnStored, OnNotice: h.pl.OnNotice, AdmitNotice: cfg.admitNotice, Admit: cfg.admit,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stopAd = cancel
	h.adDone = make(chan struct{})
	go func() { defer close(h.adDone); h.ad.Run(ctx, fullSink{h.in}) }()
	select {
	case <-h.ad.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("假适配器没起来")
	}
	t.Cleanup(h.shutdown)
	return h
}

// shutdown 收尾的顺序：停适配器 → 关流水线（取消在问的插件）→ 关入库一站 → 关出口（假钟没人拨的话它会等 5 秒，所以边拨边等）→ 关库
func (h *mh) shutdown() {
	h.stopAd()
	<-h.adDone
	h.pl.Close()
	h.in.Close()
	done := make(chan struct{})
	go func() { h.out.Close(); close(done) }()
	for {
		select {
		case <-done:
			h.st.Close()
			return
		case <-time.After(time.Millisecond):
			h.clk.Advance(10 * time.Second)
		}
	}
}

// settle 等到入库一站收下的都处理完，再等出口排着的发完
func (h *mh) settle() { h.t.Helper(); h.settleWithin(10 * time.Second) }

func (h *mh) settleWithin(d time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := h.in.Idle(ctx); err != nil {
		h.t.Fatalf("等了 %v 入库一站还没处理完：%v", d, err)
	}
	select {
	case <-h.out.q.idleChan():
	case <-ctx.Done():
		h.t.Fatalf("等了 %v 出口还没发完", d)
	}
}

func (h *mh) push(in types.Inbound) {
	h.t.Helper()
	if err := h.ad.PushInbound(context.Background(), in); err != nil {
		h.t.Fatal(err)
	}
}

func (h *mh) pushNotice(n types.Notice) {
	h.t.Helper()
	if err := h.ad.PushNotice(context.Background(), n); err != nil {
		h.t.Fatal(err)
	}
}

func (h *mh) events(conv types.Conversation) []evRow { return readEvents(h.t, h.st, conv) }

// herEvents 是她这个角色在这个会话里自己说的（actor_kind = character）
func (h *mh) herEvents(conv types.Conversation) []evRow {
	var out []evRow
	for _, e := range h.events(conv) {
		if e.ActorKind == ActorCharacter {
			out = append(out, e)
		}
	}
	return out
}

func (h *mh) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.st.Events.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func readEvents(t *testing.T, st *store.Store, conv types.Conversation) []evRow {
	t.Helper()
	rows, err := st.Events.Query(`SELECT seq, kind, actor_kind, actor_id, COALESCE(origin, ''), text, reply_to, segments_json FROM events WHERE conversation = ? ORDER BY seq`, conv.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []evRow
	for rows.Next() {
		var r evRow
		if err := rows.Scan(&r.Seq, &r.Kind, &r.ActorKind, &r.ActorID, &r.Origin, &r.Text, &r.ReplyTo, &r.Segments); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// 两个不同的人在同一个群里接连发了同一句
func (h *mh) twoSay(conv types.Conversation, account, text string, ids ...string) {
	h.t.Helper()
	for i, id := range ids {
		h.push(msg(account, conv, id, []string{friend, other}[i], text))
	}
}

// ---------- 复读：说话、试运行、只记录三个档 ----------

// 说话档：两个人复读，她跟一遍（假适配器收到那一段），库里是她的一条（automation:repeater），闸门收到的第二批 Taken 为真
func TestIntegrationRepeatSpeaks(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.twoSay(g, acctA, "草", "101", "102")
	h.settle()

	sent := h.ad.Sent()
	if len(sent) != 1 || sent[0].Part.Kind != types.PartMessage || types.BodyTextOf(sent[0].Part.Body) != "草" || sent[0].Account != acctA || sent[0].Part.Conv != g {
		t.Fatalf("适配器收到的：%+v", sent)
	}
	her := h.herEvents(g)
	if len(her) != 1 || her[0].Origin != "automation:repeater" || her[0].ActorID != "ca" || her[0].Text != "草" {
		t.Fatalf("库里她的一条：%+v", her)
	}
	var kind, origin, state string
	if err := h.st.Events.QueryRow(`SELECT kind, origin, state FROM outbound_plans`).Scan(&kind, &origin, &state); err != nil || kind != "send" || origin != "automation:repeater" || state != "done" {
		t.Fatalf("计划：%s %s %s %v", kind, origin, state, err)
	}
	d := h.dispatches()
	if len(d) != 2 || d[0].Result.Taken || !d[1].Result.Taken || d[1].Result.By != "repeater" {
		t.Fatalf("闸门收到的：%+v", d)
	}
	// 她跟的这句，下一轮历史里是"系统替你自动发的"，不是她说的（history.toMessage）；但不是试运行，在历史里
	_, msgs, _, err := history.For(h.st.Events, "ca", g.String(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || !strings.Contains(last.Text, "自动发的（repeater）") {
		t.Fatalf("历史里最后一条：%+v", last)
	}
}

// 她跟的那句被协议端报回来（回声）：认领成她发的那一段，不会存成"她的号上别人发的"，也不会再触发链
func TestIntegrationRepeatEchoIsClaimed(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.twoSay(g, acctA, "草", "101", "102")
	h.settle()
	h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "1001", From: types.User{ID: acctA}, Time: baseTime,
		Segs: []types.Seg{{Kind: types.SegText, Text: "草"}}}) // 假适配器给她那一段的回包号是 1001
	h.settle()
	if rows := h.events(g); len(rows) != 3 {
		t.Fatalf("回声不该多存一条：%+v", rows)
	}
	if got := len(h.dispatches()); got != 2 {
		t.Fatalf("她自己的话不交给闸门：%d", got)
	}
	if len(h.ad.Sent()) != 1 {
		t.Fatal("没有再跟一次")
	}
}

// 试运行档：链照跑，复读"接了"，但不发；库里是 origin = automation:dry-run 的一行，计划 kind 是试运行；不进她的下一轮历史
func TestIntegrationRepeatDryRun(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.setMode(ModeDryRun)
	h.twoSay(g, acctA, "草", "101", "102")
	h.settle()

	if len(h.ad.Sent()) != 0 {
		t.Fatalf("试运行不调 Send：%+v", h.ad.Sent())
	}
	her := h.herEvents(g)
	if len(her) != 1 || her[0].Origin != "automation:dry-run" || her[0].Text != "草" {
		t.Fatalf("库里她的一条：%+v", her)
	}
	var kind, origin string
	if err := h.st.Events.QueryRow(`SELECT kind, origin FROM outbound_plans`).Scan(&kind, &origin); err != nil || kind != "dry-run" || origin != "automation:repeater" {
		t.Fatalf("计划：%s %s %v", kind, origin, err)
	}
	if d := h.dispatches(); len(d) != 2 || !d[1].Result.Taken || !d[1].Result.Ran {
		t.Fatalf("试运行档链照跑、复读照接：%+v", d)
	}
	_, msgs, _, err := history.For(h.st.Events, "ca", g.String(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "自动发的") || m.Role == "assistant" {
			t.Fatalf("试运行的不进她的下一轮历史：%+v", msgs)
		}
	}
	// 试运行的冷却也算（同一次试运行里，不会一分钟跟三次）
	h.twoSay(g, acctA, "笑", "103", "104")
	h.settle()
	if d := h.dispatches(); len(d) != 4 || d[3].Result.Taken || len(h.herEvents(g)) != 1 {
		t.Fatalf("冷却里不跟：%+v", d)
	}
}

// 只记录档：链不跑；闸门收到的 Ran 为假、Taken 为假；消息照常入库；什么也不发，没有计划
func TestIntegrationRepeatRecordMode(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.setMode(ModeRecord)
	h.twoSay(g, acctA, "草", "101", "102")
	h.settle()
	if len(h.ad.Sent()) != 0 || h.count(`SELECT count(*) FROM outbound_plans`) != 0 {
		t.Fatal("只记录档不发、不写计划")
	}
	if rows := h.events(g); len(rows) != 2 {
		t.Fatalf("消息照常入库：%+v", rows)
	}
	d := h.dispatches()
	if len(d) != 2 || d[1].Result.Ran || d[1].Result.Taken {
		t.Fatalf("闸门收到的：%+v", d)
	}
}

// 开关默认全关：说话档也不跟；只开一个群，别的群不跟；开关是按插件分的（开了复读不等于开了戳回去）
func TestIntegrationSwitchesDefaultOff(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g1, g2 := group("5001"), group("5002")
	h.twoSay(g1, acctA, "草", "101", "102")
	h.pushNotice(poke(acctA, g1, friend, acctA))
	h.settle()
	if len(h.ad.Sent()) != 0 || len(h.ad.Pokes()) != 0 {
		t.Fatal("没开开关，什么都不该发")
	}
	h.enable("repeater", "5001")
	h.twoSay(g2, acctA, "草", "201", "202")
	h.pushNotice(poke(acctA, g1, other, acctA))
	h.settle()
	if len(h.ad.Sent()) != 0 || len(h.ad.Pokes()) != 0 {
		t.Fatalf("开了 5001 的复读：5002 不跟，戳回去还是关的：%+v %+v", h.ad.Sent(), h.ad.Pokes())
	}
	h.twoSay(g1, acctA, "笑", "103", "104")
	h.settle()
	if len(h.ad.Sent()) != 1 {
		t.Fatalf("开着的群该跟：%d", len(h.ad.Sent()))
	}
}

// 同一个群两个角色（两个号）都开了复读：两个号各看到同样的消息，只有一个角色跟
func TestIntegrationTwoCharactersFollowOnce(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.push(msg(acctA, g, "101", friend, "草"))
	h.push(msg(acctB, g, "101", friend, "草"))
	h.push(msg(acctA, g, "102", other, "草"))
	h.push(msg(acctB, g, "102", other, "草"))
	h.settle()
	if sent := h.ad.Sent(); len(sent) != 1 {
		t.Fatalf("两个角色同群，只该有一个跟：%+v", sent)
	}
	if h.count(`SELECT count(*) FROM outbound_plans`) != 1 {
		t.Fatal("只该有一条计划")
	}
	if !strings.Contains(h.logText(), "冷却中") {
		t.Fatalf("另一个角色该是冷却中：\n%s", h.logText())
	}
	d := h.dispatches()
	if len(d) != 4 || d[2].Batch.Character != "ca" || !d[2].Result.Taken || d[3].Batch.Character != "cb" || d[3].Result.Taken {
		t.Fatalf("闸门各收到一次，ca 接了、cb 没接：%+v", d)
	}
}

// 补录的消息不进链：补录来的第二个人不会让她跟；补录的消息也算这一串里的人
func TestIntegrationBackfillDoesNotTriggerRepeat(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	for _, in := range []types.Inbound{msg(acctA, g, "101", friend, "草"), msg(acctA, g, "102", other, "草")} {
		tk, err := h.in.Submit(context.Background(), in, Options{Backfill: true})
		if err != nil {
			t.Fatal(err)
		}
		waitTicket(t, tk)
	}
	h.settle()
	if len(h.ad.Sent()) != 0 || len(h.dispatches()) != 0 {
		t.Fatalf("补录的不触发复读、不交给闸门：%+v %d", h.ad.Sent(), len(h.dispatches()))
	}
	// 实时来的第三个人：前面补录的两个人已经复读过了，不跟
	h.push(msg(acctA, g, "103", "30003", "草"))
	h.settle()
	if len(h.ad.Sent()) != 0 {
		t.Fatal("补录来的两个人已经是一轮复读，第三个人不跟")
	}
}

// ---------- 戳回去 ----------

// 戳回去：假适配器收到 Poke（戳那个人，用被戳的那个号）；计划是 automation:poke、没有 events（不计开口、不计退避）；通知不交给闸门；
// 60 秒内再戳不回；过了 60 秒再回
func TestIntegrationPokeBack(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	h.pushNotice(poke(acctA, g, friend, acctA))
	h.settle()
	pokes := h.ad.Pokes()
	if len(pokes) != 1 || pokes[0].Target != friend || pokes[0].Account != acctA || pokes[0].Conv != g {
		t.Fatalf("适配器收到的戳：%+v", pokes)
	}
	var origin, kind string
	var evSeq sql.NullInt64
	if err := h.st.Events.QueryRow(`SELECT origin, kind, event_seq FROM outbound_plans`).Scan(&origin, &kind, &evSeq); err != nil || origin != "automation:poke" || kind != "send" || evSeq.Valid {
		t.Fatalf("计划：%s %s %v %v", origin, kind, evSeq, err)
	}
	if her := h.herEvents(g); len(her) != 0 {
		t.Fatalf("戳回去不是她说话，库里不该有她的话：%+v", her)
	}
	if len(h.dispatches()) != 0 {
		t.Fatal("通知不交给闸门：戳一戳不计热闹、不计退避")
	}
	if rows := h.events(g); len(rows) != 1 || rows[0].Kind != "notice" {
		t.Fatalf("那个戳存成通知：%+v", rows)
	}

	h.clk.Advance(30 * time.Second)
	h.pushNotice(types.Notice{Kind: types.NoticePoke, Account: acctA, Conv: g, Time: h.clk.Now(), Actor: friend, Target: acctA})
	h.settle()
	if len(h.ad.Pokes()) != 1 {
		t.Fatalf("30 秒内再戳不回：%d", len(h.ad.Pokes()))
	}
	if !strings.Contains(h.logText(), "同一个人 60 秒内只戳回一次") {
		t.Fatalf("日志里要有限流的原因：\n%s", h.logText())
	}
	h.clk.Advance(31 * time.Second)
	h.pushNotice(types.Notice{Kind: types.NoticePoke, Account: acctA, Conv: g, Time: h.clk.Now(), Actor: friend, Target: acctA})
	h.settle()
	if len(h.ad.Pokes()) != 2 {
		t.Fatalf("过了 60 秒该再回：%d", len(h.ad.Pokes()))
	}
}

// 私聊里戳回去默认开（ADR-189，什么开关都没设）；用的是私聊会话里她的号。
// 群里（开了）戳的是别人、她自己戳出去的回声都不回
func TestIntegrationPokeInPrivateAndNotAtHer(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	h.pushNotice(poke(acctA, private(acctA, friend), friend, acctA))
	h.pushNotice(poke(acctA, g, friend, other)) // 戳的是别人
	h.pushNotice(poke(acctA, g, acctA, friend)) // 她戳出去的回声
	h.settle()
	pokes := h.ad.Pokes()
	if len(pokes) != 1 || pokes[0].Conv != private(acctA, friend) || pokes[0].Account != acctA {
		t.Fatalf("只有私聊里戳她的那一下该回：%+v", pokes)
	}
	// 她自己戳出去的那条通知不存（计划表里已经有）；另外两条存了
	if rows := h.events(g); len(rows) != 1 || rows[0].ActorID != "qq:"+friend {
		t.Fatalf("群里的通知：%+v", rows)
	}
}

// 试运行档：戳回去也只写计划（kind 是试运行），不调适配器的 Poke
func TestIntegrationPokeDryRun(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	h.setMode(ModeDryRun)
	h.pushNotice(poke(acctA, g, friend, acctA))
	h.settle()
	if len(h.ad.Pokes()) != 0 {
		t.Fatal("试运行不戳")
	}
	var kind, origin string
	if err := h.st.Events.QueryRow(`SELECT kind, origin FROM outbound_plans`).Scan(&kind, &origin); err != nil || kind != "dry-run" || origin != "automation:poke" {
		t.Fatalf("计划：%s %s %v", kind, origin, err)
	}
	// 只记录档链不跑
	h.setMode(ModeRecord)
	h.clk.Advance(2 * time.Minute)
	h.pushNotice(types.Notice{Kind: types.NoticePoke, Account: acctA, Conv: g, Time: h.clk.Now(), Actor: other, Target: acctA})
	h.settle()
	if n := h.count(`SELECT count(*) FROM outbound_plans`); n != 1 {
		t.Fatalf("只记录档不写计划：%d", n)
	}
}

// 戳她，同时有人 @ 她提问：戳回去，提问照常交给闸门、不被拦（拦截只消费这一条通知）
func TestIntegrationPokeDoesNotSwallowAQuestion(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	h.enable("repeater", "5001")
	h.pushNotice(poke(acctA, g, friend, acctA))
	h.push(withTags(msg(acctA, g, "101", friend, ""),
		types.Seg{Kind: types.SegAt, Target: acctA}, types.Seg{Kind: types.SegText, Text: "今天几号？"}))
	h.settle()
	if len(h.ad.Pokes()) != 1 {
		t.Fatalf("该戳回去：%d", len(h.ad.Pokes()))
	}
	d := h.dispatches()
	if len(d) != 1 || d[0].Result.Taken || !d[0].Batch.Addressed || d[0].Batch.Msgs[0].Segs[1].Text != "今天几号？" {
		t.Fatalf("提问该原样交给闸门，不被拦：%+v", d)
	}
}

// 被叫到的批次复读不接：两个人复读，第二个人这条 @ 了她，她该回答而不是跟
func TestIntegrationAddressedBatchIsNotRepeated(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("repeater", "5001")
	h.push(withTags(msg(acctA, g, "101", friend, ""), types.Seg{Kind: types.SegText, Text: "草"}))
	h.push(withTags(msg(acctA, g, "102", other, ""), types.Seg{Kind: types.SegText, Text: "草"}, types.Seg{Kind: types.SegAt, Target: acctA}))
	h.settle()
	if len(h.ad.Sent()) != 0 {
		t.Fatal("被叫到不复读")
	}
}

// ---------- 撤回 ----------

// 撤回告诉第 3 步：被撤回的是哪一条。第 3 步用它判断"她正准备回的就是这条"——这里用一个最小的"在途回合登记"演示这个接口够用：
// 已放行给内核的批里有被撤回的那条，就取消；撤回的是别的、更早的，不取消
func TestIntegrationRecallTellsTheTurn(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.push(msg(acctA, g, "701", friend, "旧话"))
	h.push(msg(acctA, g, "702", other, "正准备回的这条"))
	h.settle()
	d := h.dispatches()
	if len(d) != 2 {
		t.Fatal("两条都交给闸门")
	}
	inflight := map[int64]bool{d[1].Batch.Msgs[0].Seq: true} // 内核正在回第二条
	cancelled := false
	check := func(r Recalled) {
		if inflight[r.TargetSeq] {
			cancelled = true
		}
	}

	h.pushNotice(recall(acctA, g, friend, "701")) // 撤回更早的：不取消
	h.settle()
	for _, r := range h.recalled() {
		check(r)
	}
	if cancelled {
		t.Fatal("撤回的不是在回的这条，不该取消")
	}
	h.pushNotice(recall(acctA, g, other, "702"))
	h.settle()
	rec := h.recalled()
	if len(rec) != 2 || rec[1].TargetSeq != d[1].Batch.Msgs[0].Seq || rec[1].Character != "ca" || rec[1].Conv != g {
		t.Fatalf("撤回该带上被撤回那条的 seq：%+v", rec)
	}
	check(rec[1])
	if !cancelled {
		t.Fatal("撤回的正是在回的这条，该取消")
	}
}

// ---------- 关闭后旧任务还在回来（清单④） ----------

// 插件超时以后才醒过来去发东西：链已经把它当成没接放行了，它拿到的 ctx 到期，出口拒绝，库里什么也不会多出来
func TestIntegrationLateSendAfterTimeoutIsRefused(t *testing.T) {
	t.Parallel()
	g := group("5001")
	release := make(chan struct{})
	lateErr := make(chan error, 1)
	var outbox types.Outbox
	slow := types.Interceptor{
		Name: "slow", Priority: 5, Occasions: []types.Occasion{{Scene: types.SceneGroup}}, Triggers: []string{types.TriggerMessage},
		Intercept: func(ctx context.Context, b types.Batch) types.Verdict {
			<-release
			_, err := outbox.Send(ctx, b.Character, "automation:slow", b.Conv, []types.Part{types.Message("迟到的话")})
			lateErr <- err
			return types.Took("迟到的回答")
		},
	}
	h := newMH(t, func(c *mhCfg) { c.extra = []types.Interceptor{slow} })
	outbox = h.out
	h.enable("slow", "5001")

	h.push(msg(acctA, g, "101", friend, "草"))
	h.clk.WaitTimer(t, DefaultInterceptTimeout)
	h.clk.Advance(DefaultInterceptTimeout) // 链等了 3 秒，放行
	h.settle()
	d := h.dispatches()
	if len(d) != 1 || d[0].Result.Taken {
		t.Fatalf("超时当没接，放行给闸门：%+v", d)
	}
	close(release)
	select {
	case err := <-lateErr:
		if err == nil {
			t.Fatal("迟到的 Send 该被拒绝")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("插件没有醒")
	}
	h.settle()
	if len(h.ad.Sent()) != 0 || h.count(`SELECT count(*) FROM outbound_plans`) != 0 || len(h.herEvents(g)) != 0 {
		t.Fatal("迟到的话不该发出去、不该写计划、不该进库")
	}
}

// 私聊里戳回去默认开，每个私聊能单独关（ADR-189）；群里默认关，不会因为私聊默认开而跟着开；陌生人（不记看到了）的不回
func TestIntegrationPrivatePokeDefaultOnAndPerChatOff(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	h.pushNotice(poke(acctA, private(acctA, friend), friend, acctA))
	h.pushNotice(poke(acctA, private(acctA, other), other, acctA))
	h.pushNotice(poke(acctA, group("5001"), friend, acctA)) // 群里没开
	h.settle()
	pokes := h.ad.Pokes()
	targets := map[string]bool{}
	for _, p := range pokes {
		targets[p.Target] = true
	}
	// 两个私聊是两个会话、两条队列，谁先处理完不一定，所以不比先后
	if len(pokes) != 2 || !targets[friend] || !targets[other] {
		t.Fatalf("两个私聊都该回，群里不回：%+v", pokes)
	}

	h.disable("poke", friend) // 把和 friend 的私聊单独关掉
	h.clk.Advance(2 * time.Minute)
	for _, who := range []string{friend, other} {
		h.pushNotice(types.Notice{Kind: types.NoticePoke, Account: acctA, Conv: private(acctA, who), Time: h.clk.Now(), Actor: who, Target: acctA})
	}
	h.settle()
	pokes = h.ad.Pokes()
	if len(pokes) != 3 || pokes[2].Target != other {
		t.Fatalf("关掉的那个私聊不回，另一个照回：%+v", pokes)
	}
	// 通知照常存（关掉的只是戳回去）
	if rows := h.events(private(acctA, friend)); len(rows) != 2 || rows[0].Kind != "notice" {
		t.Fatalf("关掉的私聊里，通知照存：%+v", rows)
	}
}

// 不是她本来就接的人（准入检查标成不记看到了）：戳她，不回，哪怕私聊默认开
func TestIntegrationStrangerPokeIsNotAnswered(t *testing.T) {
	t.Parallel()
	h := newMH(t, func(c *mhCfg) {
		c.admitNotice = func(n types.Notice) Admission {
			if n.Actor == "30009" {
				return Admission{NoObserve: true}
			}
			return Admission{}
		}
	})
	h.pushNotice(poke(acctA, private(acctA, "30009"), "30009", acctA))
	h.pushNotice(poke(acctA, private(acctA, friend), friend, acctA))
	h.settle()
	if pokes := h.ad.Pokes(); len(pokes) != 1 || pokes[0].Target != friend {
		t.Fatalf("陌生人戳她不回：%+v", pokes)
	}
}

// 一个角色在同一个群里绑了两个号（ca：20001 和 20003），有人戳她的任何一个号：两个号各收到一份通知，不管哪份先到，都只戳回一次。
// 入库一站按角色去重，后到的那份不会再交给链，所以"被戳的是不是收到通知的那个号"不能当判断依据——
// 复核时量到：戳她的第二个号、第一个号的那份先到，戳回去 0 次。同群另一个角色（cb）收到同一条通知，戳的不是它的号，不回
func TestIntegrationPokeAtEitherAccountOfOneCharacterPokesBackOnce(t *testing.T) {
	t.Parallel()
	const second = "20003"
	roster := Roster{Owners: map[string]string{chQQ: owner}, Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, second, "ca"}, {chQQ, acctB, "cb"}}}
	for _, target := range []string{acctA, second} {
		for name, order := range map[string][]string{"第一个号的先到": {acctA, second}, "第二个号的先到": {second, acctA}} {
			t.Run("戳"+target+"/"+name, func(t *testing.T) {
				h := newMH(t, func(c *mhCfg) { c.roster = roster })
				g := group("5001")
				h.enable("poke", "5001")
				for _, acct := range order {
					h.pushNotice(poke(acct, g, friend, target))
				}
				h.pushNotice(poke(acctB, g, friend, target)) // 同群另一个角色的号也看到了这一下
				h.settle()
				pokes := h.ad.Pokes()
				if len(pokes) != 1 || pokes[0].Target != friend || pokes[0].Account == acctB {
					t.Fatalf("她该戳回去正好一次，而且是她自己（ca）的号戳的：%+v", pokes)
				}
				if rows := h.events(g); len(rows) != 1 {
					t.Fatalf("三份通知只存一份：%+v", rows)
				}
			})
		}
	}
}

// 同一个人在同一秒里先后戳了同群两个角色的号：这是两条通知，两个角色各戳回一次。
// 通知的去重键里没有"戳的是谁"的话，第二下会被当成第一下的重复，cb 永远收不到
func TestIntegrationTwoPokesInOneSecondAtTwoCharacters(t *testing.T) {
	t.Parallel()
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	for _, target := range []string{acctA, acctB} {
		for _, recv := range []string{acctA, acctB} { // 两个号都看到每一下
			h.pushNotice(poke(recv, g, friend, target))
		}
	}
	h.settle()
	accounts := map[string]int{}
	for _, p := range h.ad.Pokes() {
		accounts[p.Account]++
	}
	if len(accounts) != 2 || accounts[acctA] != 1 || accounts[acctB] != 1 {
		t.Fatalf("两个角色各戳回一次：%+v", h.ad.Pokes())
	}
	if rows := h.events(g); len(rows) != 2 {
		t.Fatalf("两下是两条通知：%+v", rows)
	}
}
