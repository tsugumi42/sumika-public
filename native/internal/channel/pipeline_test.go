package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 流水线的测试：真的入库一站（临时库）加真的链，插件是假的。号、群、名字都是编的。
// 入库一站每存好一条消息、处理完一条通知就调流水线；这里看它：算对 Addressed、交给链的 Batch 对不对、哪些消息不进链、
// 通知怎么存、撤回怎么接、通知和消息在同一个会话里是不是一个接一个。

// spy 是一个记下每次被问到的 Batch 的假插件
type spy struct {
	mu      sync.Mutex
	batches []types.Batch
	verdict func(types.Batch) types.Verdict
	gate    chan struct{} // 非 nil：问到时先卡在这里，直到关闭
	entered chan struct{} // 非 nil：问到时往里放一个信号
}

func (s *spy) plugin(name string, prio int, triggers ...string) types.Interceptor {
	if len(triggers) == 0 {
		triggers = []string{types.TriggerMessage}
	}
	return types.Interceptor{
		Name: name, Priority: prio, Triggers: triggers,
		Occasions: []types.Occasion{{Scene: types.SceneGroup}, {Scene: types.ScenePrivate}},
		Intercept: func(ctx context.Context, b types.Batch) types.Verdict {
			s.mu.Lock()
			s.batches = append(s.batches, b)
			gate, entered, verdict := s.gate, s.entered, s.verdict
			s.mu.Unlock()
			if entered != nil {
				entered <- struct{}{}
			}
			if gate != nil {
				<-gate
			}
			if verdict != nil {
				return verdict(b)
			}
			return types.Passed("")
		},
	}
}

func (s *spy) seen() []types.Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.Batch(nil), s.batches...)
}

// ph 是一套：临时库、入库一站、流水线、链
type ph struct {
	t       *testing.T
	st      *store.Store
	in      *Intake
	pl      *Pipeline
	mu      sync.Mutex
	gate    []Dispatch
	recalls []Recalled
	noticed []Noticed
	logs    []string
}

type phOpt func(*Config, *PipelineConfig, *ChainConfig)

func (h *ph) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (h *ph) dispatches() []Dispatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Dispatch(nil), h.gate...)
}

func (h *ph) recalled() []Recalled {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Recalled(nil), h.recalls...)
}

func (h *ph) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func newPH(t *testing.T, plugins []types.Interceptor, opts ...phOpt) *ph {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	h := &ph{t: t, st: st}
	chainCfg := ChainConfig{Plugins: plugins, Switch: always, Logf: h.logf}
	cfg := Config{Stores: store.Single(st), Roster: testRoster(), Logf: h.logf, Now: func() time.Time { return baseTime }}
	pcfg := PipelineConfig{Stores: store.Single(st), Roster: testRoster(), Logf: h.logf}
	pcfg.Gate = func(d Dispatch) { h.mu.Lock(); h.gate = append(h.gate, d); h.mu.Unlock() }
	pcfg.OnRecall = func(r Recalled) { h.mu.Lock(); h.recalls = append(h.recalls, r); h.mu.Unlock() }
	for _, o := range opts {
		o(&cfg, &pcfg, &chainCfg)
	}
	chain, err := NewChain(chainCfg)
	if err != nil {
		t.Fatal(err)
	}
	pcfg.Chain = chain
	h.pl, err = NewPipeline(pcfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OnStored = h.pl.OnStored
	prev := cfg.OnNotice
	cfg.OnNotice = func(nd Noticed) {
		h.mu.Lock()
		h.noticed = append(h.noticed, nd)
		h.mu.Unlock()
		h.pl.OnNotice(nd)
		if prev != nil {
			prev(nd)
		}
	}
	h.in, err = NewIntake(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 收尾的顺序：先关流水线（取消在问的插件），再关入库一站（等在做的做完），最后关库
	t.Cleanup(func() {
		h.pl.Close()
		h.in.Close()
		st.Close()
	})
	return h
}

// idle 等到现在收下的都处理完
func (h *ph) idle() { h.t.Helper(); h.idleWithin(10 * time.Second) }

func (h *ph) idleWithin(d time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := h.in.Idle(ctx); err != nil {
		h.t.Fatalf("等了 %v 还没处理完：%v", d, err)
	}
}

func (h *ph) push(in types.Inbound) {
	h.t.Helper()
	if err := h.in.Inbound(context.Background(), in); err != nil {
		h.t.Fatal(err)
	}
}

func (h *ph) notice(n types.Notice) {
	h.t.Helper()
	if err := h.in.Notice(context.Background(), n); err != nil {
		h.t.Fatal(err)
	}
}

func poke(acct string, conv types.Conversation, actor, target string) types.Notice {
	return types.Notice{Kind: types.NoticePoke, Account: acct, Conv: conv, Time: baseTime, Actor: actor, Target: target}
}

func recall(acct string, conv types.Conversation, actor, msgID string) types.Notice {
	return types.Notice{Kind: types.NoticeRecall, Account: acct, Conv: conv, Time: baseTime, Actor: actor, MsgID: msgID}
}

// events 读 events 表里这个会话的行：(kind, actor_kind, actor_id, origin, reply_to)
type evRow struct {
	Seq                                    int64
	Kind, ActorKind, ActorID, Origin, Text string
	ReplyTo, Segments                      sql.NullString
}

func (h *ph) events(conv types.Conversation) []evRow {
	h.t.Helper()
	rows, err := h.st.Events.Query(`SELECT seq, kind, actor_kind, actor_id, COALESCE(origin, ''), text, reply_to, segments_json FROM events WHERE conversation = ? ORDER BY seq`, conv.String())
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []evRow
	for rows.Next() {
		var r evRow
		if err := rows.Scan(&r.Seq, &r.Kind, &r.ActorKind, &r.ActorID, &r.Origin, &r.Text, &r.ReplyTo, &r.Segments); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (h *ph) observers(seq int64) []string {
	h.t.Helper()
	rows, err := h.st.Events.Query(`SELECT character_id FROM observations WHERE event_seq = ? ORDER BY character_id`, seq)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		out = append(out, c)
	}
	return out
}

func withTags(in types.Inbound, segs ...types.Seg) types.Inbound {
	in.Segs = segs
	return in
}

// ---------- 消息 ----------

// 交给链的 Batch：角色、号、会话、触发、新到的这条、Recent 是这条之前的
func TestPipelineBatchFields(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)})
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "第一句"))
	h.push(msg(acctA, g, "102", other, "第二句"))
	h.idle()

	got := s.seen()
	if len(got) != 2 {
		t.Fatalf("问了 %d 次，想要 2 次", len(got))
	}
	b := got[1]
	if b.Character != "ca" || b.Account != acctA || b.Conv != g || b.Trigger != types.TriggerMessage || b.Notice != nil || b.Addressed {
		t.Fatalf("第二批：%+v", b)
	}
	if len(b.Msgs) != 1 || b.Msgs[0].Who != "qq:"+other || b.Msgs[0].Self || b.Msgs[0].Origin != OriginPeer || len(b.Msgs[0].Segs) != 1 || b.Msgs[0].Segs[0].Text != "第二句" {
		t.Fatalf("新到的这条：%+v", b.Msgs)
	}
	// 名字是发言当时的昵称（没有群名片）；这一条没叫到她；Recent 里的那条名字从库里的 from 读出来
	if b.Msgs[0].Name != "昵称"+other || b.Msgs[0].Addressed {
		t.Fatalf("新到的这条的名字和 Addressed：%+v", b.Msgs[0])
	}
	if b.Msgs[0].Seq == 0 || b.Now.IsZero() {
		t.Fatalf("seq 和 Now 要有：%+v %v", b.Msgs[0], b.Now)
	}
	prev, err := b.Recent(10)
	if err != nil || len(prev) != 1 || prev[0].Who != "qq:"+friend || prev[0].Segs[0].Text != "第一句" || prev[0].Seq >= b.Msgs[0].Seq {
		t.Fatalf("Recent 该是这一批之前的那一条：%+v %v", prev, err)
	}
	if prev[0].Name != "昵称"+friend {
		t.Fatalf("Recent 里那条的名字：%q", prev[0].Name)
	}
	first, _ := got[0].Recent(10)
	if len(first) != 0 {
		t.Fatalf("第一批之前没有消息：%+v", first)
	}
}

// 叫到她：@ 她的任何一个号、引用她说过的话；@ 别的角色的号、@ 全体、引用别人的话都不算
func TestPipelineAddressed(t *testing.T) {
	t.Parallel()
	g := group("5001")
	at := func(target string) types.Seg { return types.Seg{Kind: types.SegAt, Target: target} }
	txt := types.Seg{Kind: types.SegText, Text: "在吗"}
	reply := func(id string) types.Seg { return types.Seg{Kind: types.SegReply, Target: id} }
	for _, tc := range []struct {
		name string
		segs []types.Seg
		want bool
	}{
		{"@ 她", []types.Seg{at(acctA), txt}, true},
		{"@ 别的角色的号", []types.Seg{at(acctB), txt}, false},
		{"@ 全体", []types.Seg{at(types.AtAll), txt}, false},
		{"@ 路人", []types.Seg{at(other), txt}, false},
		{"引用她说的话", []types.Seg{reply("900"), txt}, true},
		{"引用别人说的话", []types.Seg{reply("901"), txt}, false},
		{"引用同群另一个角色说的话", []types.Seg{reply("902"), txt}, false},
		{"引用库里没有的", []types.Seg{reply("999"), txt}, false},
		{"没叫她", []types.Seg{txt}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &spy{}
			h := newPH(t, []types.Interceptor{s.plugin("spy", 20)})
			// 她的号发过一条（平台消息号 900），路人发过一条（901），同群另一个角色（cb）的号发过一条（902）
			h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "900", From: types.User{ID: acctA}, Time: baseTime,
				Segs: []types.Seg{{Kind: types.SegText, Text: "她说的"}}})
			h.push(msg(acctA, g, "901", other, "路人说的"))
			h.push(types.Inbound{Kind: types.InboundSelf, Account: acctB, Conv: g, PlatformID: "902", From: types.User{ID: acctB}, Time: baseTime,
				Segs: []types.Seg{{Kind: types.SegText, Text: "另一个角色说的"}}})
			h.idle()
			n := len(s.seen())
			h.push(withTags(msg(acctA, g, "950", friend, ""), tc.segs...))
			h.idle()
			got := s.seen()
			if len(got) != n+1 {
				t.Fatalf("该多问一次：%d → %d", n, len(got))
			}
			if got[n].Addressed != tc.want {
				t.Fatalf("Addressed = %v，想要 %v", got[n].Addressed, tc.want)
			}
			if got[n].Msgs[0].Addressed != tc.want {
				t.Fatalf("这一条自己的 Addressed = %v，想要 %v（闸门拼批、算听众靠它）", got[n].Msgs[0].Addressed, tc.want)
			}
		})
	}
}

// 一个角色有两个号：@ 哪一个都算叫到她
func TestPipelineAddressedAnyOfHerAccounts(t *testing.T) {
	t.Parallel()
	roster := Roster{Owners: map[string]string{chQQ: owner}, Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, "20003", "ca"}, {chQQ, acctB, "cb"}}}
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)}, func(c *Config, p *PipelineConfig, _ *ChainConfig) { c.Roster, p.Roster = roster, roster })
	h.push(withTags(msg(acctA, group("5001"), "950", friend, ""), types.Seg{Kind: types.SegAt, Target: "20003"}, types.Seg{Kind: types.SegText, Text: "喂"}))
	h.idle()
	if got := s.seen(); len(got) != 1 || !got[0].Addressed {
		t.Fatalf("@ 她的第二个号也算叫到她：%+v", got)
	}
}

// 不进链、也不交给闸门的消息：补录的、陌生人和自检的（不记看到了）、她自己的号发的
func TestPipelineSkipsWhatShouldNotReact(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)}, func(c *Config, _ *PipelineConfig, _ *ChainConfig) {
		c.Admit = func(in types.Inbound) Admission {
			if in.From.ID == "30009" {
				return Admission{NoObserve: true, Tags: []string{"stranger"}}
			}
			return Admission{}
		}
	})
	g := group("5001")
	if tk, err := h.in.Submit(context.Background(), msg(acctA, g, "101", friend, "补录来的"), Options{Backfill: true}); err != nil {
		t.Fatal(err)
	} else {
		waitTicket(t, tk)
	}
	h.push(msg(acctA, g, "102", "30009", "陌生人"))
	h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "103", From: types.User{ID: acctA}, Time: baseTime, Segs: []types.Seg{{Kind: types.SegText, Text: "她的号发的"}}})
	h.idle()
	if len(s.seen()) != 0 || len(h.dispatches()) != 0 {
		t.Fatalf("这三条都不该进链、不该交给闸门：问了 %d 次，交了 %d 次", len(s.seen()), len(h.dispatches()))
	}
	h.push(msg(acctA, g, "104", friend, "正常的"))
	h.idle()
	if len(s.seen()) != 1 || len(h.dispatches()) != 1 {
		t.Fatalf("正常的一条该进链、交给闸门各一次：%d %d", len(s.seen()), len(h.dispatches()))
	}
}

// 链的结果交给闸门：接了的 Taken 为真（闸门只计数，不放行）；没接的为假
func TestPipelineHandsChainResultToGate(t *testing.T) {
	t.Parallel()
	s := &spy{verdict: func(b types.Batch) types.Verdict {
		if b.Msgs[0].Segs[0].Text == "复读" {
			return types.Took("跟了")
		}
		return types.Passed("不关我事")
	}}
	h := newPH(t, []types.Interceptor{s.plugin("repeater", 20)})
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "复读"))
	h.push(msg(acctA, g, "102", friend, "聊别的"))
	h.idle()
	d := h.dispatches()
	if len(d) != 2 {
		t.Fatalf("交了 %d 次", len(d))
	}
	if !d[0].Result.Taken || d[0].Result.By != "repeater" || !d[0].Result.Ran || d[0].Batch.Msgs[0].Segs[0].Text != "复读" {
		t.Fatalf("第一批：%+v", d[0].Result)
	}
	if d[1].Result.Taken || !d[1].Result.Ran {
		t.Fatalf("第二批：%+v", d[1].Result)
	}
	if d[0].Batch.Now.IsZero() || d[1].Batch.Now.IsZero() {
		t.Fatal("交给闸门的 Batch 也带着链用的那个时刻")
	}
}

// 同一条消息两个号都看到、绑了两个角色：每个角色各过一遍链；同一个号重推一遍不再过
func TestPipelineEachCharacterOncePerMessage(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)})
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "大家好"))
	h.push(msg(acctB, g, "101", friend, "大家好"))
	h.push(msg(acctA, g, "101", friend, "大家好")) // 重连后协议端重推
	h.idle()
	got := s.seen()
	if len(got) != 2 || got[0].Character != "ca" || got[1].Character != "cb" || got[1].Account != acctB {
		t.Fatalf("每个角色一次：%+v", got)
	}
	if got[0].Msgs[0].Seq != got[1].Msgs[0].Seq {
		t.Fatal("同一条消息只有一个 seq")
	}
}

// 同群另一个角色（cb）说的话：交给这个角色（ca）的链和闸门，发言人是 cb，不算 ca 自己说的；cb 自己的号收到的那一份不进 cb 自己的链。
// 角色之间接得上话（第 5 步的闸门）、复读把别的角色算作一个人，都靠这一条；两份谁先到都一样
func TestPipelineOtherCharactersSpeechReachesChainAndGate(t *testing.T) {
	t.Parallel()
	g := group("5001")
	said := types.Seg{Kind: types.SegText, Text: "大家好"}
	own := func() types.Inbound { // cb 的号自己发的那一份
		return types.Inbound{Kind: types.InboundSelf, Account: acctB, Conv: g, PlatformID: "801", From: types.User{ID: acctB}, Time: baseTime, Segs: []types.Seg{said}}
	}
	seenByCa := func() types.Inbound { return msg(acctA, g, "801", acctB, "大家好") } // ca 的号看到的：别人（cb）发的
	for name, order := range map[string][]types.Inbound{"cb 自己的号先到": {own(), seenByCa()}, "ca 的号先到": {seenByCa(), own()}} {
		t.Run(name, func(t *testing.T) {
			s := &spy{}
			h := newPH(t, []types.Interceptor{s.plugin("spy", 20)})
			for _, in := range order {
				h.push(in)
			}
			h.idle()
			got := s.seen()
			if len(got) != 1 || got[0].Character != "ca" || got[0].Account != acctA {
				t.Fatalf("只该是 ca 的链被问到一次（cb 自己说的不进 cb 的链）：%+v", got)
			}
			if m := got[0].Msgs[0]; m.Who != "cb" || m.Self || len(m.Segs) != 1 || m.Segs[0].Text != "大家好" {
				t.Fatalf("发言人是 cb，不是 ca 自己：%+v", m)
			}
			if d := h.dispatches(); len(d) != 1 || d[0].Batch.Character != "ca" || d[0].Batch.Msgs[0].Who != "cb" {
				t.Fatalf("闸门也只收到 ca 看到的这一份：%+v", d)
			}

			// 接着有人说话：ca 这一批的 Recent 里，cb 的那句是别人说的（算一个人），不是 ca 自己的
			h.push(msg(acctA, g, "802", friend, "你好"))
			h.idle()
			all := s.seen()
			prev, err := all[len(all)-1].Recent(10)
			if err != nil || len(prev) != 1 || prev[0].Who != "cb" || prev[0].Self {
				t.Fatalf("Recent 里 cb 的话不是 ca 自己的：%+v %v", prev, err)
			}
		})
	}
}

// ---------- 通知 ----------

// 戳一戳存成通知（events 一行 kind = notice），两个号都看到只存一份、各记一条看到了；通知不进给模型看的历史
func TestPipelineStoresPokeOnceForTwoAccounts(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("poke", 10, types.NoticePoke)})
	g := group("5001")
	h.notice(poke(acctA, g, friend, acctA))
	h.notice(poke(acctB, g, friend, acctA)) // 同一个戳，另一个号也收到了
	h.notice(poke(acctA, g, friend, acctA)) // 同号重推
	h.idle()
	rows := h.events(g)
	if len(rows) != 1 || rows[0].Kind != "notice" || rows[0].ActorKind != ActorHuman || rows[0].ActorID != "qq:"+friend || rows[0].Origin != OriginPeer {
		t.Fatalf("events：%+v", rows)
	}
	var body struct{ Notice, Actor, Target string }
	if err := json.Unmarshal([]byte(rows[0].Segments.String), &body); err != nil || body.Notice != "poke" || body.Actor != friend || body.Target != acctA {
		t.Fatalf("segments_json：%s %v", rows[0].Segments.String, err)
	}
	if obs := h.observers(rows[0].Seq); strings.Join(obs, ",") != "ca,cb" {
		t.Fatalf("两个角色各看到一次：%v", obs)
	}
	got := s.seen()
	if len(got) != 2 || got[0].Character != "ca" || got[1].Character != "cb" || got[0].Notice == nil || got[0].Notice.Target != acctA || got[0].Trigger != types.NoticePoke {
		t.Fatalf("链该按角色各问一次（同号重推不再问）：%+v", got)
	}
	if got[0].Addressed || len(got[0].Msgs) != 0 {
		t.Fatalf("通知批没有消息，也不算叫到她：%+v", got[0])
	}
	if len(h.dispatches()) != 0 {
		t.Fatal("通知不交给闸门：通知本来就不计")
	}
}

// 不同秒的两次戳、不同的人戳、戳的是不同的人，各是一条通知
func TestPipelineKeepsSeparatePokes(t *testing.T) {
	t.Parallel()
	h := newPH(t, nil)
	g := group("5001")
	n1, n2 := poke(acctA, g, friend, acctA), poke(acctA, g, friend, acctA)
	n2.Time = baseTime.Add(5 * time.Second)
	other := poke(acctA, g, other, acctA)
	h.notice(n1)
	h.notice(n2)
	h.notice(other)
	// 同一个人、同一秒，戳的是同群两个不同角色的号：也是两条通知（键里要有"戳的是谁"）
	h.notice(poke(acctA, g, friend, acctB))
	h.idle()
	if rows := h.events(g); len(rows) != 4 {
		t.Fatalf("四条不同的通知：%+v", rows)
	}
}

// 撤回：存成通知并指向被撤回的那条（reply_to）；告诉 OnRecall 被撤回的是哪条；库里没有的 TargetSeq 为 0；之后 Recent 里没有被撤回的
func TestPipelineRecall(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)})
	g := group("5001")
	h.push(msg(acctA, g, "701", friend, "说错了"))
	h.idle()
	rows := h.events(g)
	h.notice(recall(acctA, g, friend, "701"))
	h.notice(recall(acctA, g, friend, "未知的号")) // 库里没有这一条
	h.idle()

	rec := h.recalled()
	if len(rec) != 2 {
		t.Fatalf("撤回该告诉 OnRecall 两次：%+v", rec)
	}
	if rec[0].Character != "ca" || rec[0].Conv != g || rec[0].MsgID != "701" || rec[0].TargetSeq != rows[0].Seq || rec[0].Actor != friend {
		t.Fatalf("第一次：%+v（被撤回的 seq 该是 %d）", rec[0], rows[0].Seq)
	}
	if rec[1].TargetSeq != 0 || rec[1].TargetID != "" {
		t.Fatalf("库里没有的那一条 TargetSeq 为 0：%+v", rec[1])
	}
	all := h.events(g)
	if len(all) != 3 || all[1].Kind != "notice" || !all[1].ReplyTo.Valid {
		t.Fatalf("events：%+v", all)
	}
	var id string
	h.st.Events.QueryRow(`SELECT id FROM events WHERE seq = ?`, rows[0].Seq).Scan(&id)
	if all[1].ReplyTo.String != id || all[2].ReplyTo.Valid {
		t.Fatalf("撤回指向被撤回的那条，指不到的留空：%+v", all)
	}

	// 被撤回的不算数：之后的批次 Recent 里没有它
	h.push(msg(acctA, g, "702", other, "接着聊"))
	h.idle()
	got := s.seen()
	prev, err := got[len(got)-1].Recent(10)
	if err != nil || len(prev) != 0 {
		t.Fatalf("Recent 里不该有被撤回的：%+v %v", prev, err)
	}
}

// 角色自己的号做的通知不存（她经出口戳出去的，计划表里已经有）；链仍然看得到它（插件自己认）
func TestPipelineDoesNotStoreNoticesFromCharacterAccounts(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("poke", 10, types.NoticePoke)})
	g := group("5001")
	h.notice(poke(acctA, g, acctA, friend)) // 她戳了别人的回声
	h.notice(poke(acctA, g, acctB, acctA))  // 另一个角色戳了她
	h.idle()
	if rows := h.events(g); len(rows) != 0 {
		t.Fatalf("角色号做的通知不存：%+v", rows)
	}
	if got := s.seen(); len(got) != 2 {
		t.Fatalf("链该看到这两条：%d", len(got))
	}
}

// 被踢没有会话（types.Notice 的约定，Conv 是零值）：靠号找账号，收下，不存，不进链；坏通知不收
func TestPipelineNoticeValidation(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("poke", 10, types.NoticePoke)})
	g := group("5001")
	if err := h.in.Notice(context.Background(), types.Notice{Kind: types.NoticeKicked, Account: acctA, Time: baseTime}); err != nil {
		t.Fatalf("被踢是整个号的事，没有会话也该收：%v", err)
	}
	for name, n := range map[string]types.Notice{
		"不认识的号":    poke("99999", g, friend, "99999"),
		"不认识的种类":   {Kind: "鬼", Account: acctA, Conv: g},
		"没有渠道":     {Kind: types.NoticePoke, Account: acctA},
		"群会话带了她的号": {Kind: types.NoticePoke, Account: acctA, Conv: types.Conversation{Channel: chQQ, Scene: types.SceneGroup, ID: "5001", Self: acctA}},
		"私聊属于别的号":  poke(acctA, private(acctB, friend), friend, acctA),
		"被踢的号不认识":  {Kind: types.NoticeKicked, Account: "99999", Time: baseTime},
	} {
		if err := h.in.Notice(context.Background(), n); err == nil {
			t.Fatalf("%s：该拒绝", name)
		}
	}
	h.idle()
	if len(s.seen()) != 0 || len(h.events(g)) != 0 {
		t.Fatal("被踢不进链、不存")
	}
}

// 被踢的号在两个渠道里恰好同号：分不清是哪个，不猜，拒绝；给了渠道就精确找
func TestKickedNoticeWithSameNumberInTwoChannels(t *testing.T) {
	t.Parallel()
	roster := Roster{Accounts: []Account{{chQQ, acctA, "ca"}, {"wx", acctA, "cb"}}}
	h := newPH(t, nil, func(c *Config, p *PipelineConfig, _ *ChainConfig) { c.Roster, p.Roster = roster, roster })
	if err := h.in.Notice(context.Background(), types.Notice{Kind: types.NoticeKicked, Account: acctA}); err == nil {
		t.Fatal("两个渠道都有这个号，不带渠道的被踢通知分不清，该拒绝")
	}
	if err := h.in.Notice(context.Background(), types.Notice{Kind: types.NoticeKicked, Account: acctA, Conv: types.Conversation{Channel: "wx"}}); err != nil {
		t.Fatalf("给了渠道就能精确找到：%v", err)
	}
}

// 通知也有准入：白名单外的群的通知不存、不处理；陌生人的通知存下但不记看到了、不进链
func TestPipelineNoticeAdmission(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("poke", 10, types.NoticePoke)}, func(c *Config, _ *PipelineConfig, _ *ChainConfig) {
		c.AdmitNotice = func(n types.Notice) Admission {
			switch {
			case n.Conv.ID == "5999":
				return Admission{Drop: true}
			case n.Actor == "30009":
				return Admission{NoObserve: true}
			}
			return Admission{}
		}
	})
	h.notice(poke(acctA, group("5999"), friend, acctA))
	h.notice(poke(acctA, group("5001"), "30009", acctA))
	h.idle()
	if rows := h.events(group("5999")); len(rows) != 0 {
		t.Fatalf("白名单外的群不存：%+v", rows)
	}
	rows := h.events(group("5001"))
	if len(rows) != 1 || len(h.observers(rows[0].Seq)) != 0 {
		t.Fatalf("陌生人的存下、不记看到了：%+v", rows)
	}
	if len(s.seen()) != 0 {
		t.Fatal("陌生人戳她，链不该问（不回）")
	}
}

// ---------- 队列和收尾 ----------

// 同一个会话里，通知和消息一个接一个：前一条的链没跑完，后面的通知不开始（插件的状态不会被两条路同时改）；别的会话不受影响
func TestPipelineNoticeWaitsBehindMessageInSameConversation(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	slow := &spy{gate: gate, entered: entered}
	notices := &spy{}
	h := newPH(t, []types.Interceptor{slow.plugin("slow", 20), notices.plugin("poke", 10, types.NoticePoke)})
	g, g2 := group("5001"), group("5002")
	h.push(msg(acctA, g, "101", friend, "卡住"))
	waitEntered(t, entered)
	h.notice(poke(acctA, g, friend, acctA))
	h.push(msg(acctA, g2, "201", friend, "别的群"))
	// 别的会话照常走（也会卡在 slow 里，所以先放行再看）；这个会话的通知还不能开始
	time.Sleep(50 * time.Millisecond)
	if len(notices.seen()) != 0 {
		t.Fatal("同一个会话里前一条还没处理完，通知不该开始")
	}
	close(gate)
	h.idle()
	if len(notices.seen()) != 1 {
		t.Fatalf("放行以后通知该处理：%d", len(notices.seen()))
	}
	if len(h.dispatches()) != 2 {
		t.Fatalf("两条消息都该交给闸门：%d", len(h.dispatches()))
	}
}

// 关闭以后（清单④）：流水线什么也不做，不碰闸门；入库一站不再收通知
func TestPipelineAfterClose(t *testing.T) {
	t.Parallel()
	s := &spy{}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20), s.plugin("poke", 10, types.NoticePoke)})
	g := group("5001")
	h.pl.Close()
	h.push(msg(acctA, g, "101", friend, "关了以后到的"))
	h.notice(poke(acctA, g, friend, acctA))
	h.notice(recall(acctA, g, friend, "101"))
	h.idle()
	if len(s.seen()) != 0 || len(h.dispatches()) != 0 || len(h.recalled()) != 0 {
		t.Fatalf("流水线关了以后不该问插件、不该碰闸门：%d %d %d", len(s.seen()), len(h.dispatches()), len(h.recalled()))
	}
	h.in.Close()
	if err := h.in.Notice(context.Background(), poke(acctA, g, friend, acctA)); !errors.Is(err, ErrClosed) {
		t.Fatalf("入库一站关了以后不收通知：%v", err)
	}
}

// 关闭时在问的插件：ctx 到期，链不再往下问，也不交给闸门
func TestPipelineCloseWhileAsking(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	released := make(chan struct{})
	p := types.Interceptor{
		Name: "stuck", Priority: 20, Occasions: []types.Occasion{{Scene: types.SceneGroup}}, Triggers: []string{types.TriggerMessage},
		Intercept: func(ctx context.Context, _ types.Batch) types.Verdict {
			entered <- struct{}{}
			<-ctx.Done()
			close(released)
			return types.Took("迟到")
		},
	}
	h := newPH(t, []types.Interceptor{p})
	h.push(msg(acctA, group("5001"), "101", friend, "x"))
	waitEntered(t, entered)
	h.pl.Close()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("流水线关了，插件的 ctx 没到期")
	}
	h.idle()
	if len(h.dispatches()) != 0 {
		t.Fatal("关了以后不该交给闸门")
	}
}

// 插件里 panic、Gate 里 panic 都不能让队列停：后面的消息照常处理
func TestPipelineSurvivesHookPanics(t *testing.T) {
	t.Parallel()
	h := newPH(t, nil, func(_ *Config, p *PipelineConfig, _ *ChainConfig) {
		n := 0
		p.Gate = func(Dispatch) {
			n++
			if n == 1 {
				panic("闸门坏了")
			}
		}
	})
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "一"))
	h.push(msg(acctA, g, "102", friend, "二"))
	h.idle()
	if rows := h.events(g); len(rows) != 2 {
		t.Fatalf("两条都该入库：%+v", rows)
	}
	if !strings.Contains(h.logText(), "闸门坏了") {
		t.Fatalf("panic 该记日志：%s", h.logText())
	}
}

// obSpy 是记下每条观察的假观察者；boom 为真时每次都 panic
type obSpy struct {
	mu   sync.Mutex
	got  []Observation
	boom bool
}

func (o *obSpy) Name() string { return "obspy" }
func (o *obSpy) Observe(ob Observation) {
	if o.boom {
		panic("坏了")
	}
	o.mu.Lock()
	o.got = append(o.got, ob)
	o.mu.Unlock()
}
func (o *obSpy) seen() []Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Observation(nil), o.got...)
}

// 观察者收到的和进链的是同一批：补录、陌生人、她自己的不给；两个角色各一次，第二次 Duplicate；只记录的会话不给；
// 一个观察者 panic 不耽误别的观察者和拦截链
func TestPipelineObservers(t *testing.T) {
	t.Parallel()
	s := &spy{}
	bad, ob := &obSpy{boom: true}, &obSpy{}
	quiet := group("5002")
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)}, func(c *Config, p *PipelineConfig, cc *ChainConfig) {
		c.Admit = func(in types.Inbound) Admission {
			if in.From.ID == "30009" {
				return Admission{NoObserve: true, Tags: []string{"stranger"}}
			}
			return Admission{}
		}
		p.Observers = []Observer{bad, ob}
		cc.Mode = func(conv types.Conversation) string {
			if conv == quiet {
				return ModeRecord
			}
			return ModeSpeak
		}
	})
	g := group("5001")
	if tk, err := h.in.Submit(context.Background(), msg(acctA, g, "101", friend, "补录来的"), Options{Backfill: true}); err != nil {
		t.Fatal(err)
	} else {
		waitTicket(t, tk)
	}
	h.push(msg(acctA, g, "102", "30009", "陌生人"))
	h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "103", From: types.User{ID: acctA}, Time: baseTime, Segs: []types.Seg{{Kind: types.SegText, Text: "她的号发的"}}})
	h.push(msg(acctA, quiet, "105", friend, "只记录的群"))
	h.push(msg(acctA, g, "104", friend, "看这个 https://x.com/a/status/1"))
	h.push(msg(acctB, g, "104", friend, "看这个 https://x.com/a/status/1"))
	h.idle()
	got := ob.seen()
	if len(got) != 2 || got[0].Character != "ca" || got[1].Character != "cb" || got[0].Duplicate || !got[1].Duplicate {
		t.Fatalf("该是 ca、cb 各一次、第二次重复：%+v", got)
	}
	if got[0].Seq != got[1].Seq || got[0].Conv != g || len(got[0].Segs) != 1 || got[0].Segs[0].Text != "看这个 https://x.com/a/status/1" {
		t.Fatalf("观察的内容不对：%+v", got[0])
	}
	if len(s.seen()) != 2 {
		t.Fatalf("观察者 panic 以后拦截链照跑：问了 %d 次", len(s.seen()))
	}
	if !strings.Contains(h.logText(), "观察者 obspy") {
		t.Fatalf("panic 要记日志：%s", h.logText())
	}
}
