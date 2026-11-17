package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 测试里的号、群、名字都是编的（LESSONS：不用真实群名、昵称、号码）。
// 一个主人，两个角色的号：A 号绑角色 ca，B 号绑角色 cb
const (
	chQQ   = "qq"
	owner  = "10000"
	acctA  = "20001"
	acctB  = "20002"
	friend = "30001"
	other  = "30002"
)

var baseTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func testRoster() Roster {
	return Roster{
		Owners:   map[string]string{chQQ: owner},
		Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, acctB, "cb"}},
	}
}

// harness 是一个临时库加一个入库一站；日志收进 logs
type harness struct {
	t     *testing.T
	st    *store.Store
	in    *Intake
	mu    sync.Mutex
	logs  []string
	saved []Stored
}

func (h *harness) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

func (h *harness) stored() []Stored {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Stored(nil), h.saved...)
}

func (h *harness) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

// newHarness 造一套。tweak 可以改配置（挂准入、换 OnStored……）；OnStored 不改的话只记下来
func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st}
	cfg := Config{Stores: store.Single(st), Roster: testRoster(), Logf: h.logf, Now: func() time.Time { return baseTime }}
	cfg.OnStored = func(s Stored) {
		h.mu.Lock()
		h.saved = append(h.saved, s)
		h.mu.Unlock()
	}
	if tweak != nil {
		tweak(&cfg)
	}
	h.in, err = NewIntake(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 收尾的顺序：先关入库一站（等在做的做完），再关库——LESSONS"异步和收尾"：关掉的服务不能再被碰
	t.Cleanup(func() {
		h.in.Close()
		st.Close()
	})
	return h
}

func group(id string) types.Conversation {
	return types.Conversation{Channel: chQQ, Scene: types.SceneGroup, ID: id}
}

func private(self, peer string) types.Conversation {
	return types.Conversation{Channel: chQQ, Scene: types.ScenePrivate, ID: peer, Self: self}
}

// msg 造一条收到的群消息：account 是收到它的号，from 是发言人
func msg(account string, conv types.Conversation, platformID, from, text string) types.Inbound {
	return types.Inbound{
		Kind: types.InboundMessage, Account: account, Conv: conv, PlatformID: platformID,
		From: types.User{ID: from, Nick: "昵称" + from}, Time: baseTime,
		Segs: []types.Seg{{Kind: types.SegText, Text: text}},
	}
}

// waitTicket 等一个凭据，最多 10 秒：改坏的实现卡住时要快点红，不挂到测试超时
func waitTicket(t *testing.T, tk *Ticket) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := tk.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("凭据等了 10 秒还没有结果")
	}
	return r, err
}

// waitEntered 等后续步骤进到"卡住"的那一点，最多 10 秒
func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("后续步骤 10 秒还没被调用")
	}
}

func (h *harness) submit(in types.Inbound, opt Options) Result {
	h.t.Helper()
	tk, err := h.in.Submit(context.Background(), in, opt)
	if err != nil {
		h.t.Fatalf("Submit：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := tk.Wait(ctx)
	if err != nil {
		h.t.Fatalf("Wait：%v", err)
	}
	return r
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.st.Events.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

type eventRow struct {
	Seq                                                         int64
	ID, Channel, Conversation, ActorKind, ActorID, Origin, Text string
	Segments                                                    sql.NullString
	ReplyTo                                                     sql.NullString
	CreatedAt                                                   int64
}

func (h *harness) events() []eventRow {
	h.t.Helper()
	rows, err := h.st.Events.Query(`SELECT seq, id, channel, conversation, actor_kind, actor_id, origin, text, segments_json, reply_to, created_at FROM events ORDER BY seq`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []eventRow
	for rows.Next() {
		var e eventRow
		if err := rows.Scan(&e.Seq, &e.ID, &e.Channel, &e.Conversation, &e.ActorKind, &e.ActorID, &e.Origin, &e.Text, &e.Segments, &e.ReplyTo, &e.CreatedAt); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (h *harness) observers(seq int64) []string {
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

// 一条收到的消息存下来是什么样：事件的各列、segments_json 的内容、谁看到了
func TestInboundStoresEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	in := msg(acctA, group("900"), "5001", owner, "你好")
	in.Segs = []types.Seg{{Kind: types.SegAt, Target: acctA}, {Kind: types.SegText, Text: "你好"}}
	in.From.Card = "主人的名片"
	in.Time = baseTime.Add(-90 * time.Second)
	r := h.submit(in, Options{})
	if r.Duplicate || r.Dropped || r.Seq == 0 || r.EventID == "" {
		t.Fatalf("结果不对：%+v", r)
	}
	evs := h.events()
	if len(evs) != 1 {
		t.Fatalf("应该 1 条，有 %d", len(evs))
	}
	e := evs[0]
	if e.Channel != "qq" || e.Conversation != "qq:group:900" || e.ActorKind != "owner" || e.ActorID != "owner" || e.Origin != "owner" {
		t.Errorf("事件的列不对：%+v", e)
	}
	if e.Text != "@"+acctA+" 你好" {
		t.Errorf("纯文本 = %q", e.Text)
	}
	if want := in.Time.UnixMilli(); e.CreatedAt != want {
		t.Errorf("created_at 应该是平台时间 %d，是 %d", want, e.CreatedAt)
	}
	var body struct {
		Segs []types.Seg `json:"segs"`
		From types.User  `json:"from"`
		Tags []string    `json:"tags"`
	}
	if err := json.Unmarshal([]byte(e.Segments.String), &body); err != nil {
		t.Fatalf("segments_json 读不出来：%v（%s）", err, e.Segments.String)
	}
	if !reflect.DeepEqual(body.Segs, in.Segs) || body.From != in.From || len(body.Tags) != 0 {
		t.Errorf("segments_json = %s", e.Segments.String)
	}
	// 种类名是中立的：库里不出现 OneBot 的字段名
	for _, bad := range []string{`"qq"`, `"type"`, `"mface"`, `"record"`} {
		if strings.Contains(e.Segments.String, bad) {
			t.Errorf("segments_json 里出现了 %s：%s", bad, e.Segments.String)
		}
	}
	if got := h.observers(e.Seq); !reflect.DeepEqual(got, []string{"ca"}) {
		t.Errorf("谁看到了 = %v，应该是 ca（A 号绑的角色）", got)
	}
}

// 认人：主人 > 角色的号 > 别人；别人的 id 是 渠道:号；origin 跟着走
func TestActorsAndOrigins(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	for i, c := range []struct {
		from             string
		kind, id, origin string
	}{
		{owner, "owner", "owner", "owner"},
		{friend, "human", "qq:" + friend, "peer"},
		{acctA, "character", "ca", "human"}, // 她的号发的、又没被认成 Sumika 的回声：真人手动发的
		{acctB, "character", "cb", "human"}, // 另一个角色的号发的，也按号认
	} {
		h.submit(msg(acctA, group("900"), strconv.Itoa(100+i), c.from, "x"), Options{})
		e := h.events()[i]
		if e.ActorKind != c.kind || e.ActorID != c.id || e.Origin != c.origin {
			t.Errorf("发言人 %s：(%s, %s, %s)，应该是 (%s, %s, %s)", c.from, e.ActorKind, e.ActorID, e.Origin, c.kind, c.id, c.origin)
		}
	}
	// 主人的号也配成了账号：仍算主人（主人先判）
	r := testRoster()
	r.Accounts = append(r.Accounts, Account{chQQ, owner, "cx"})
	if a := r.Resolve(chQQ, owner); a.Kind != ActorOwner {
		t.Errorf("主人的号配成账号以后应该仍是主人：%+v", a)
	}
	// 别的渠道里同号的人不是这里的主人
	if a := r.Resolve("tg", owner); a.Kind != ActorHuman || a.ID != "tg:"+owner {
		t.Errorf("别的渠道的同号不是主人：%+v", a)
	}
}

// 私聊的会话串带她的号；看到的是收信的那个号的角色
func TestPrivateConversation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.submit(msg(acctB, private(acctB, owner), "7", owner, "私聊一句"), Options{})
	e := h.events()[0]
	if e.Conversation != "qq:private:"+acctB+":"+owner {
		t.Errorf("会话串 = %q", e.Conversation)
	}
	if got := h.observers(e.Seq); !reflect.DeepEqual(got, []string{"cb"}) {
		t.Errorf("谁看到了 = %v", got)
	}
}

// 双号观察：同一个群的同一条，两个号各交一份——事件一条，看到了两条，结果指向同一条；订阅者两个角色各通知一次
func TestTwoAccountsSameGroup(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	a := h.submit(msg(acctA, group("900"), "5001", friend, "大家好"), Options{})
	b := h.submit(msg(acctB, group("900"), "5001", friend, "大家好（内容可以和 A 看到的不一样，比如图片链接）"), Options{})
	if a.Duplicate || !b.Duplicate || a.Seq != b.Seq || a.EventID != b.EventID {
		t.Fatalf("A=%+v B=%+v", a, b)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("事件应该 1 条，有 %d", n)
	}
	if got := h.observers(a.Seq); !reflect.DeepEqual(got, []string{"ca", "cb"}) {
		t.Fatalf("看到了 = %v", got)
	}
	if e := h.events()[0]; e.Text != "大家好" {
		t.Errorf("已有的事件不该被第二份覆盖：%q", e.Text)
	}
	if err := h.in.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := h.stored()
	if len(got) != 2 || got[0].Character != "ca" || got[0].Duplicate || got[1].Character != "cb" || !got[1].Duplicate || got[1].Seq != got[0].Seq {
		t.Fatalf("通知不对：%+v", got)
	}
	// 同一个号的第二次（补录和实时重叠）：仍是重复，看到了不增加
	c := h.submit(msg(acctA, group("900"), "5001", friend, "大家好"), Options{Backfill: true})
	if !c.Duplicate || h.count(`SELECT count(*) FROM observations`) != 2 {
		t.Fatalf("同一个号重复来：%+v", c)
	}
}

// 两个号看同一个群的两个不同的号一样的消息——号相同、会话不同，不是重复
func TestSameIDInOtherConversationIsNotDuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.submit(msg(acctA, group("900"), "42", friend, "群 900"), Options{})
	r := h.submit(msg(acctA, group("901"), "42", friend, "群 901"), Options{})
	if r.Duplicate || h.count(`SELECT count(*) FROM events`) != 2 {
		t.Fatalf("不同会话的同号不是重复：%+v", r)
	}
}

// 号为空或 0：每条都入库；超过 2^53 的号往返不变，2^53 和 2^53+1 是两条
func TestPlatformIDEdgeCases(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	for i, id := range []string{"", "", "0", "0"} {
		r := h.submit(msg(acctA, group("900"), id, friend, "第"+strconv.Itoa(i)+"条"), Options{})
		if r.Duplicate {
			t.Fatalf("号 %q 的第 %d 条被当成了重复", id, i)
		}
	}
	for _, id := range []string{"9007199254740992", "9007199254740993"} {
		if r := h.submit(msg(acctA, group("900"), id, friend, id), Options{}); r.Duplicate {
			t.Fatalf("号 %s 被当成了重复", id)
		}
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 6 {
		t.Fatalf("6 条不同的消息都该入库，有 %d", n)
	}
	var big string
	h.st.Events.QueryRow(`SELECT platform_id FROM channel_msgs WHERE platform_id LIKE '%993'`).Scan(&big)
	if big != "9007199254740993" {
		t.Fatalf("大号往返变了：%q", big)
	}
	if n := h.count(`SELECT count(*) FROM channel_msgs`); n != 2 {
		t.Fatalf("没有号的不进映射表：%d 行", n)
	}
}

// 收下之前就拒绝的：什么也不存
func TestRejectedBeforeQueue(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	good := msg(acctA, group("900"), "1", friend, "x")
	tests := map[string]func(*types.Inbound){
		"号不在名册":    func(in *types.Inbound) { in.Account = "99999" },
		"渠道对不上":    func(in *types.Inbound) { in.Conv.Channel = "tg" },
		"Kind 不认识": func(in *types.Inbound) { in.Kind = "poke" },
		"会话没有号":    func(in *types.Inbound) { in.Conv.ID = "" },
		"群会话带了她的号": func(in *types.Inbound) { in.Conv.Self = acctA },
		"私聊没带她的号": func(in *types.Inbound) {
			in.Conv = types.Conversation{Channel: chQQ, Scene: types.ScenePrivate, ID: friend}
		},
		"私聊属于另一个号": func(in *types.Inbound) { in.Conv = private(acctB, friend) }, // 交的是 A 号，会话却是 B 号的私聊
	}
	for name, mutate := range tests {
		in := good
		mutate(&in)
		if _, err := h.in.Submit(context.Background(), in, Options{}); err == nil {
			t.Errorf("%s：应该被拒绝", name)
		}
	}
	if _, err := h.in.Submit(context.Background(), func() types.Inbound { in := good; in.Account = "99999"; return in }(), Options{}); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("不认识的号应该是 ErrUnknownAccount：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.in.Submit(ctx, good, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx 已经取消：%v", err)
	}
	h.in.Idle(context.Background())
	if n := h.count(`SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("被拒绝的不该入库，有 %d 条", n)
	}
}

// 名册：一个号绑两个角色启动即报错；一个角色绑两个号可以
func TestRosterValidation(t *testing.T) {
	t.Parallel()
	for name, accounts := range map[string][]Account{
		"一个号绑两个角色": {{chQQ, acctA, "ca"}, {chQQ, acctA, "cb"}},
		"同一个号配两次":  {{chQQ, acctA, "ca"}, {chQQ, acctA, "ca"}},
		"没填角色":     {{chQQ, acctA, ""}},
		"没填号":      {{chQQ, "", "ca"}},
		"没填渠道":     {{"", acctA, "ca"}},
	} {
		if err := (Roster{Accounts: accounts}).validate(); err == nil {
			t.Errorf("%s：应该报错", name)
		}
	}
	if err := (Roster{Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, acctB, "ca"}}}).validate(); err != nil {
		t.Errorf("一个角色绑两个号不该拦：%v", err)
	}
	// 同一个号在两个渠道里是两回事
	if err := (Roster{Accounts: []Account{{chQQ, "1", "ca"}, {"tg", "1", "cb"}}}).validate(); err != nil {
		t.Errorf("不同渠道的同号不该拦：%v", err)
	}
	p, _ := paths.Init(t.TempDir())
	st, _ := store.OpenFresh(p)
	defer st.Close()
	if _, err := NewIntake(Config{Stores: store.Single(st), Roster: Roster{Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, acctA, "cb"}}}}); err == nil {
		t.Error("NewIntake 应该拒绝不合法的名册")
	}
	if _, err := NewIntake(Config{Roster: testRoster()}); err == nil {
		t.Error("没有库应该报错")
	}
}

// 同一个会话的消息按交来的先后一个接一个处理：入库的 seq 递增，通知的先后和交的先后一致
func TestSameConversationInOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	const n = 60
	var last *Ticket
	for i := 0; i < n; i++ {
		tk, err := h.in.Submit(context.Background(), msg(acctA, group("900"), strconv.Itoa(1000+i), friend, "第"+strconv.Itoa(i)), Options{Backfill: i%3 == 0})
		if err != nil {
			t.Fatal(err)
		}
		last = tk
	}
	if _, err := last.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.in.Idle(context.Background())
	got := h.stored()
	if len(got) != n {
		t.Fatalf("通知 %d 条，应该 %d", len(got), n)
	}
	for i, s := range got {
		if s.Inbound.PlatformID != strconv.Itoa(1000+i) {
			t.Fatalf("第 %d 个通知是 %s，顺序乱了", i, s.Inbound.PlatformID)
		}
		if i > 0 && s.Seq <= got[i-1].Seq {
			t.Fatalf("seq 没有递增：%d 在 %d 后面", s.Seq, got[i-1].Seq)
		}
	}
}

// 不同会话互不等待：一个会话的后续步骤卡住时，它自己后面的排着，别的会话照常入库；交消息的调用也不被卡住
func TestConversationsDoNotWaitForEachOther(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := newHarness(t, func(c *Config) {
		c.OnStored = func(s Stored) {
			if s.Inbound.Conv.ID == "900" && s.Inbound.PlatformID == "1" {
				entered <- struct{}{}
				<-block // 群 900 的第一条的后续步骤（拦截、闸门）卡住
			}
		}
	})
	released := false
	release := func() {
		if !released {
			released = true
			close(block)
		}
	}
	defer release()

	first, err := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "卡住的"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	waitEntered(t, entered)
	// 卡住期间交的：不阻塞调用方
	done := make(chan struct{})
	var second, third *Ticket
	go func() {
		defer close(done)
		second, _ = h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "同群排着的"), Options{})
		third, _ = h.in.Submit(context.Background(), msg(acctA, group("901"), "3", friend, "别的群"), Options{})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("交消息的调用被一个会话的后续步骤卡住了")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := third.Wait(ctx); err != nil {
		t.Fatalf("别的会话应该照常入库：%v", err)
	}
	if _, err := first.Wait(ctx); err != nil {
		t.Fatalf("卡住的那条自己已经入库了：%v", err)
	}
	// 同一个会话的下一条要等上一条的后续步骤做完才开始，所以现在还没入库
	if n := h.count(`SELECT count(*) FROM events WHERE conversation = 'qq:group:900'`); n != 1 {
		t.Fatalf("群 900 里应该只有卡住的那 1 条，有 %d", n)
	}
	if n := h.count(`SELECT count(*) FROM events WHERE conversation = 'qq:group:901'`); n != 1 {
		t.Fatalf("群 901 应该已经入库，有 %d", n)
	}
	release()
	if _, err := second.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM events WHERE conversation = 'qq:group:900'`); n != 2 {
		t.Fatalf("放开以后群 900 的第二条应该入库，有 %d", n)
	}
}

// 实时的和补录的进同一条队列，先后保持；补录的带 backfill 标签（存进事件，也带给订阅者）
func TestBackfillAndLiveShareOneLane(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	var tks []*Ticket
	for i := 0; i < 10; i++ {
		tk, err := h.in.Submit(context.Background(), msg(acctA, group("900"), strconv.Itoa(2000+i), friend, "x"), Options{Backfill: i%2 == 0})
		if err != nil {
			t.Fatal(err)
		}
		tks = append(tks, tk)
	}
	h.in.Idle(context.Background())
	var prev int64
	for i, tk := range tks {
		r, err := tk.Wait(context.Background())
		if err != nil || r.Seq <= prev {
			t.Fatalf("第 %d 条：%+v %v", i, r, err)
		}
		prev = r.Seq
	}
	for i, e := range h.events() {
		var body struct {
			Tags []string `json:"tags"`
		}
		json.Unmarshal([]byte(e.Segments.String), &body)
		has := false
		for _, tag := range body.Tags {
			has = has || tag == TagBackfill
		}
		if has != (i%2 == 0) {
			t.Errorf("第 %d 条的补录标签 = %v", i, has)
		}
	}
	for i, s := range h.stored() {
		if hasTag(s.Tags, TagBackfill) != (i%2 == 0) {
			t.Errorf("第 %d 条通知的补录标签不对：%v", i, s.Tags)
		}
	}
}

// 关闭：正在做的做完，排着的丢掉（凭据得到 ErrClosed）；关闭以后到的不收、不写库（清单④）
func TestCloseDropsQueuedAndRefusesLate(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := newHarness(t, func(c *Config) {
		c.OnStored = func(s Stored) {
			if s.Inbound.PlatformID == "1" {
				entered <- struct{}{}
				<-block
			}
		}
	})
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	defer release() // 中途失败也要放开，不然收尾的 Close 会一直等
	first, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "在做的"), Options{})
	waitEntered(t, entered)
	second, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "排着的"), Options{})
	third, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "3", friend, "也排着"), Options{})

	closed := make(chan struct{})
	go func() { h.in.Close(); close(closed) }()
	// Close 在等正在做的那条
	select {
	case <-closed:
		t.Fatal("Close 不该在正在做的那条还没做完时返回")
	case <-time.After(100 * time.Millisecond):
	}
	// 关闭一开始，新来的就不收了（等到队列标上"已关闭"再试，不靠睡多久碰运气）
	deadline := time.Now().Add(5 * time.Second)
	for !h.in.q.isClosed() {
		if time.Now().After(deadline) {
			t.Fatal("Close 没有把队列标成已关闭")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := h.in.Submit(context.Background(), msg(acctA, group("900"), "4", friend, "关闭中到的"), Options{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 开始以后仍在收新消息：%v", err)
	}
	release()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("放开以后 Close 还没返回")
	}

	for name, tk := range map[string]*Ticket{"排着的": second, "也排着": third} {
		if _, err := waitTicket(t, tk); !errors.Is(err, ErrClosed) {
			t.Errorf("%s：凭据应该得到 ErrClosed：%v", name, err)
		}
	}
	if _, err := waitTicket(t, first); err != nil {
		t.Errorf("正在做的那条应该做完：%v", err)
	}
	if err := h.in.Inbound(context.Background(), msg(acctA, group("900"), "5", friend, "关闭以后到的")); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭以后到的应该是 ErrClosed：%v", err)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("库里应该只有正在做的那 1 条，有 %d", n)
	}
	if !strings.Contains(h.logText(), "丢掉了 2 条") {
		t.Errorf("关闭时丢了几条应该记一笔：%s", h.logText())
	}
	if err := h.in.Idle(context.Background()); err != nil {
		t.Errorf("关闭以后 Idle 应该立即返回：%v", err)
	}
	h.in.Close() // 再关一次不出事
}

// 入库失败只影响这一条：凭据得到错误、日志里有一笔（不含原文），队列不卡住，后面的照常
func TestStoreFailureDoesNotStickQueue(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	if _, err := h.st.Events.Exec(`CREATE TRIGGER boom BEFORE INSERT ON events WHEN NEW.text = '坏的那条' BEGIN SELECT RAISE(ABORT, '模拟写不进'); END`); err != nil {
		t.Fatal(err)
	}
	bad, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "坏的那条"), Options{})
	good, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "好的"), Options{})
	if _, err := waitTicket(t, bad); err == nil {
		t.Fatal("坏的那条应该失败")
	}
	if r, err := waitTicket(t, good); err != nil || r.Seq == 0 {
		t.Fatalf("后面的应该照常入库：%+v %v", r, err)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("库里应该只有好的那 1 条，有 %d", n)
	}
	h.in.Idle(context.Background())
	if len(h.stored()) != 1 {
		t.Errorf("失败的不该通知后面的步骤")
	}
	logs := h.logText()
	if !strings.Contains(logs, "入库失败") || strings.Contains(logs, "坏的那条") {
		t.Errorf("日志里要有失败的一笔，而且不能有消息原文：%q", logs)
	}
}

// 后面的步骤出意外（panic）不影响入库的结果，也不卡住队列
func TestOnStoredPanicIsContained(t *testing.T) {
	t.Parallel()
	var calls int
	var mu sync.Mutex
	h := newHarness(t, func(c *Config) {
		c.OnStored = func(s Stored) {
			mu.Lock()
			calls++
			mu.Unlock()
			if s.Inbound.PlatformID == "1" {
				panic("拦截链出了意外")
			}
		}
	})
	first := h.submit(msg(acctA, group("900"), "1", friend, "会触发意外"), Options{})
	second := h.submit(msg(acctA, group("900"), "2", friend, "后面的"), Options{})
	if first.Seq == 0 || second.Seq == 0 {
		t.Fatalf("入库不该受影响：%+v %+v", first, second)
	}
	h.in.Idle(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || !strings.Contains(h.logText(), "出了意外") {
		t.Errorf("calls=%d logs=%q", calls, h.logText())
	}
}

// 准入：不收的不存；存下但不记"看到了"的（陌生人）没有 observations，标签存进事件、带给订阅者
func TestAdmission(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) {
		c.Admit = func(in types.Inbound) Admission {
			switch in.PlatformID {
			case "1":
				return Admission{Drop: true}
			case "2":
				return Admission{NoObserve: true, Tags: []string{"stranger"}}
			}
			return Admission{}
		}
	})
	if r := h.submit(msg(acctA, group("999"), "1", friend, "白名单外的群"), Options{}); !r.Dropped || r.Seq != 0 {
		t.Fatalf("不收的：%+v", r)
	}
	stranger := h.submit(msg(acctA, private(acctA, other), "2", other, "陌生人私聊"), Options{})
	if stranger.Dropped || stranger.Seq == 0 {
		t.Fatalf("陌生人的要存：%+v", stranger)
	}
	normal := h.submit(msg(acctA, group("900"), "3", friend, "正常的"), Options{})
	if n := h.count(`SELECT count(*) FROM events`); n != 2 {
		t.Fatalf("应该存了 2 条，有 %d", n)
	}
	if got := h.observers(stranger.Seq); len(got) != 0 {
		t.Errorf("陌生人的不该有人看到：%v", got)
	}
	if got := h.observers(normal.Seq); !reflect.DeepEqual(got, []string{"ca"}) {
		t.Errorf("正常的要记看到了：%v", got)
	}
	var seg string
	h.st.Events.QueryRow(`SELECT segments_json FROM events WHERE seq = ?`, stranger.Seq).Scan(&seg)
	if !strings.Contains(seg, `"tags":["stranger"]`) {
		t.Errorf("标签没存进事件：%s", seg)
	}
	h.in.Idle(context.Background())
	var sawStranger, sawDrop bool
	for _, s := range h.stored() {
		if s.Inbound.PlatformID == "2" {
			sawStranger = !s.Observed && s.Seq == stranger.Seq && reflect.DeepEqual(s.Tags, []string{"stranger"})
		}
		if s.Inbound.PlatformID == "1" {
			sawDrop = true
		}
	}
	if !sawStranger || sawDrop {
		t.Errorf("通知不对：stranger=%v drop=%v", sawStranger, sawDrop)
	}
}

// 她的号发出的、没被认成回声的消息：实时的算真人手动发的；补录来的，按"实时接管的时刻"分——之前是别的程序（external），之后是真人（human）。
// 启发式照搬 Bun，没有验证（规格 第十八节）；这里只证明照搬得对，包括：缓存跟着新存的实时事件更新、重开一个入库一站从库里读出来也一样
func TestSelfMessageOriginByTakeover(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	self := func(id string, at time.Duration, backfill bool) string {
		in := msg(acctA, group("900"), id, acctA, "她的号发的 "+id)
		in.Kind = types.InboundSelf
		in.Time = baseTime.Add(at)
		h.submit(in, Options{Backfill: backfill})
		evs := h.events()
		return evs[len(evs)-1].Origin
	}
	live := func(id string, at time.Duration) {
		in := msg(acctA, group("900"), id, friend, "群友 "+id)
		in.Time = baseTime.Add(at)
		h.submit(in, Options{})
	}
	// 还没有任何实时事件：补录来的一律当 external
	if got := self("1", -10*time.Hour, true); got != "external" {
		t.Errorf("还没接管过，补录的她的号应该是 external，是 %s", got)
	}
	live("2", 0) // 接管的时刻：baseTime
	if got := self("3", -time.Hour, true); got != "external" {
		t.Errorf("接管之前发的应该是 external，是 %s", got)
	}
	if got := self("4", time.Hour, true); got != "human" {
		t.Errorf("接管之后发的应该是 human，是 %s", got)
	}
	if got := self("5", 2*time.Hour, false); got != "human" {
		t.Errorf("实时收到的应该是 human，是 %s", got)
	}
	// 补录的群友消息不影响接管时刻
	early := msg(acctA, group("900"), "6", friend, "很早以前的群友消息")
	early.Time = baseTime.Add(-48 * time.Hour)
	h.submit(early, Options{Backfill: true})
	if got := self("7", -time.Hour, true); got != "external" {
		t.Errorf("补录的群友消息不该把接管时刻提前，是 %s", got)
	}
	// 重开一个入库一站（进程重启）：接管时刻从库里读，答案一样
	h.in.Close()
	in2, err := NewIntake(Config{Stores: store.Single(h.st), Roster: testRoster(), Logf: h.logf})
	if err != nil {
		t.Fatal(err)
	}
	defer in2.Close()
	for id, want := range map[string]struct {
		at   time.Duration
		want string
	}{"8": {-time.Hour, "external"}, "9": {time.Hour, "human"}} {
		in := msg(acctA, group("900"), id, acctA, "重启后")
		in.Kind = types.InboundSelf
		in.Time = baseTime.Add(want.at)
		tk, err := in2.Submit(context.Background(), in, Options{Backfill: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tk.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		evs := h.events()
		if got := evs[len(evs)-1].Origin; got != want.want {
			t.Errorf("重启以后消息 %s 的 origin = %s，应该是 %s", id, got, want.want)
		}
	}
}

// 引用：被引用的那条库里有，reply_to 连上；引用段不进纯文本；没收到的引用不挡入库
func TestReplyLinking(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	quoted := h.submit(msg(acctA, group("900"), "5001", friend, "原话"), Options{})
	re := msg(acctA, group("900"), "5002", owner, "")
	re.Segs = []types.Seg{{Kind: types.SegReply, Target: "5001"}, {Kind: types.SegText, Text: "回复"}}
	h.submit(re, Options{})
	missing := msg(acctA, group("900"), "5003", owner, "")
	missing.Segs = []types.Seg{{Kind: types.SegReply, Target: "4000"}, {Kind: types.SegText, Text: "引用了没收到的"}}
	h.submit(missing, Options{})
	evs := h.events()
	if !evs[1].ReplyTo.Valid || evs[1].ReplyTo.String != quoted.EventID || evs[1].Text != "回复" {
		t.Errorf("回复：reply_to=%v text=%q，应该连到 %s", evs[1].ReplyTo, evs[1].Text, quoted.EventID)
	}
	if evs[2].ReplyTo.Valid || evs[2].Text != "引用了没收到的" {
		t.Errorf("引用的那条没收到：reply_to=%v text=%q", evs[2].ReplyTo, evs[2].Text)
	}
	if !strings.Contains(evs[2].Segments.String, `"target":"4000"`) {
		t.Errorf("引用的平台消息号要留在段里，等后补：%s", evs[2].Segments.String)
	}
}

// 没有平台时间就用现在
func TestZeroTimeUsesNow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	in := msg(acctA, group("900"), "1", friend, "x")
	in.Time = time.Time{}
	h.submit(in, Options{})
	if got := h.events()[0].CreatedAt; got != baseTime.UnixMilli() {
		t.Errorf("created_at = %d，应该是现在 %d", got, baseTime.UnixMilli())
	}
}

func TestPlainText(t *testing.T) {
	t.Parallel()
	text := func(s string) types.Seg { return types.Seg{Kind: types.SegText, Text: s} }
	for name, c := range map[string]struct {
		segs []types.Seg
		want string
	}{
		"空":         {nil, ""},
		"文字":        {[]types.Seg{text("你好")}, "你好"},
		"@ 后面补空格":   {[]types.Seg{{Kind: types.SegAt, Target: "1"}, text("你好")}, "@1 你好"},
		"@ 后面已经有空格": {[]types.Seg{{Kind: types.SegAt, Target: "1"}, text(" 你好")}, "@1 你好"},
		"@ 全体":      {[]types.Seg{{Kind: types.SegAt, Target: types.AtAll}, text("开会")}, "@全体成员 开会"},
		"引用不算文字":    {[]types.Seg{{Kind: types.SegReply, Target: "9"}, text("回复")}, "回复"},
		"各种非文字":     {[]types.Seg{{Kind: types.SegImage}, {Kind: types.SegEmoji}, {Kind: types.SegFace}, {Kind: types.SegSuperface}, {Kind: types.SegVoice}, {Kind: types.SegVideo}, {Kind: types.SegFile}, {Kind: types.SegForward}, {Kind: types.SegCard}}, "[图片][表情包][表情][超级表情][语音][视频][文件][合并转发][卡片]"},
		"认不出的段不丢":   {[]types.Seg{{Kind: types.SegOther, Raw: "xx"}}, "[未知消息]"},
		"首尾空白去掉":    {[]types.Seg{text("  你好 \n")}, "你好"},
	} {
		if got := PlainText(c.segs); got != c.want {
			t.Errorf("%s：%q，应该是 %q", name, got, c.want)
		}
	}
}

// 接到假适配器上：适配器往 sink 推的消息经入库一站存进库；Run 返回以后适配器不再推，关闭入库一站以后推不进来
type sinkOf struct{ *Intake }

func (sinkOf) Notice(context.Context, types.Notice) error { return nil }
func (sinkOf) Status(context.Context, types.Status) error { return nil }

func TestThroughFakeAdapter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	ad := fake.New(chQQ, types.Caps{})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- ad.Run(ctx, sinkOf{h.in}) }()
	<-ad.Started()
	if err := ad.PushInbound(ctx, msg(acctA, group("900"), "1", friend, "经适配器来的")); err != nil {
		t.Fatal(err)
	}
	if err := ad.PushInbound(ctx, msg("99999", group("900"), "2", friend, "不认识的号")); err == nil {
		t.Error("不认识的号应该被中间件拒绝，错误回到适配器")
	}
	if err := h.in.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("应该存了 1 条，有 %d", n)
	}
	h.in.Close()
	if err := ad.PushInbound(ctx, msg(acctA, group("900"), "3", friend, "关闭以后来的")); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭以后推进来的应该得到 ErrClosed：%v", err)
	}
	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run 取消后应该返回 nil：%v", err)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("关闭以后不该再写库，有 %d 条", n)
	}
}

// 放大：几千条消息、几十个会话同时来，全部入库，每个会话内顺序不乱。时间只打印，不当断言
func TestManyConversationsManyMessages(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short 模式不跑")
	}
	h := newHarness(t, nil)
	const convs, per = 20, 100
	var wg sync.WaitGroup
	start := time.Now()
	for c := 0; c < convs; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				// 两个号都看到每一条：一半是重复
				id := strconv.Itoa(100000 + i)
				h.in.Inbound(context.Background(), msg(acctA, group(strconv.Itoa(900+c)), id, friend, "x"))
				h.in.Inbound(context.Background(), msg(acctB, group(strconv.Itoa(900+c)), id, friend, "x"))
			}
		}(c)
	}
	wg.Wait()
	if err := h.in.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d 个会话各 %d 条、两个号各交一份，共 %d 次交，入库用时 %v", convs, per, convs*per*2, time.Since(start))
	if n := h.count(`SELECT count(*) FROM events`); n != convs*per {
		t.Fatalf("事件应该 %d 条，有 %d", convs*per, n)
	}
	if n := h.count(`SELECT count(*) FROM observations`); n != convs*per*2 {
		t.Fatalf("看到了应该 %d 条，有 %d", convs*per*2, n)
	}
	// 每个会话内：seq 的先后和平台消息号的先后一致
	for c := 0; c < convs; c++ {
		rows, err := h.st.Events.Query(`SELECT m.platform_id FROM events e JOIN channel_msgs m ON m.event_seq = e.seq WHERE e.conversation = ? ORDER BY e.seq`, "qq:group:"+strconv.Itoa(900+c))
		if err != nil {
			t.Fatal(err)
		}
		i := 0
		for rows.Next() {
			var id string
			rows.Scan(&id)
			if id != strconv.Itoa(100000+i) {
				t.Fatalf("会话 %d 第 %d 条是 %s，顺序乱了", c, i, id)
			}
			i++
		}
		rows.Close()
	}
}

// 两个号同时把同一条交进来（各一个 goroutine，卡在同一个起跑线上）：不管谁先，事件都是一条、看到了都是两条（清单⑬）
func TestConcurrentDuplicateDelivery(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	const rounds = 100
	for i := 0; i < rounds; i++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		tks := make([]*Ticket, 2)
		for k, acct := range []string{acctA, acctB} {
			wg.Add(1)
			go func(k int, acct string) {
				defer wg.Done()
				<-start
				tk, err := h.in.Submit(context.Background(), msg(acct, group("900"), strconv.Itoa(5000+i), friend, "同一条"), Options{})
				if err != nil {
					t.Error(err)
					return
				}
				tks[k] = tk
			}(k, acct)
		}
		close(start)
		wg.Wait()
		var dup int
		var seqs []int64
		for _, tk := range tks {
			if tk == nil {
				t.Fatal("有一边没交成功")
			}
			r, err := waitTicket(t, tk)
			if err != nil {
				t.Fatalf("第 %d 轮：%v", i, err)
			}
			if r.Duplicate {
				dup++
			}
			seqs = append(seqs, r.Seq)
		}
		if dup != 1 || seqs[0] != seqs[1] {
			t.Fatalf("第 %d 轮：重复 %d 个、seq %v，应该一个重复、同一个 seq", i, dup, seqs)
		}
	}
	if n := h.count(`SELECT count(*) FROM events`); n != rounds {
		t.Fatalf("事件应该 %d 条，有 %d", rounds, n)
	}
	if n := h.count(`SELECT count(*) FROM observations`); n != rounds*2 {
		t.Fatalf("看到了应该 %d 条，有 %d", rounds*2, n)
	}
}

// 同一个人私聊两个号：两个会话，平台消息号碰巧相同也不是重复（会话串带她的号，清单⑤）
func TestSamePeerPrivateToTwoAccounts(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	a := h.submit(msg(acctA, private(acctA, owner), "7", owner, "对 A 说"), Options{})
	b := h.submit(msg(acctB, private(acctB, owner), "7", owner, "对 B 说"), Options{})
	if a.Duplicate || b.Duplicate || a.Seq == b.Seq {
		t.Fatalf("两个号的私聊是两个会话：%+v %+v", a, b)
	}
	evs := h.events()
	if evs[0].Conversation == evs[1].Conversation {
		t.Fatalf("会话串一样：%s", evs[0].Conversation)
	}
	if !reflect.DeepEqual(h.observers(a.Seq), []string{"ca"}) || !reflect.DeepEqual(h.observers(b.Seq), []string{"cb"}) {
		t.Errorf("谁看到了：%v %v", h.observers(a.Seq), h.observers(b.Seq))
	}
}

// 少量、空值、非法值（清单⑯⑳）：没有段的消息、没有发言人、平台时间是 1970、Raw 过长
func TestOddInputs(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	empty := msg(acctA, group("900"), "1", friend, "")
	empty.Segs = nil
	h.submit(empty, Options{})
	e := h.events()[0]
	if e.Text != "" || !strings.Contains(e.Segments.String, `"segs":[]`) {
		t.Errorf("没有段的消息：text=%q segments=%s", e.Text, e.Segments.String)
	}

	noSender := msg(acctA, group("900"), "2", "", "x")
	if _, err := h.in.Submit(context.Background(), noSender, Options{}); err == nil {
		t.Error("没有发言人的号应该拒绝")
	}

	epoch := msg(acctA, group("900"), "3", friend, "平台时间是 0")
	epoch.Time = time.Unix(0, 0)
	h.submit(epoch, Options{})
	if got := h.events()[1].CreatedAt; got != baseTime.UnixMilli() {
		t.Errorf("1970 的平台时间当作没给，用现在：%d", got)
	}

	long := msg(acctA, group("900"), "4", friend, "")
	long.Segs = []types.Seg{{Kind: types.SegCard, Raw: strings.Repeat("字", 5000)}, {Kind: types.SegOther, Raw: strings.Repeat("😀", 300)}}
	h.submit(long, Options{})
	var body struct {
		Segs []types.Seg `json:"segs"`
	}
	if err := json.Unmarshal([]byte(h.events()[2].Segments.String), &body); err != nil {
		t.Fatal(err)
	}
	for i, s := range body.Segs {
		if n := len([]rune(s.Raw)); n != types.RawMax {
			t.Errorf("第 %d 段的 Raw 有 %d 个字，应该截到 %d", i, n, types.RawMax)
		}
	}
	if !strings.HasPrefix(body.Segs[1].Raw, "😀") || strings.ContainsRune(body.Segs[1].Raw, '�') {
		t.Errorf("截短不能切开一个字：%q", body.Segs[1].Raw)
	}
}

// 排队以后才做的活不能被适配器改了内存：交进去以后调用方改掉原来的段，存下来的仍是交的那一刻的样子
func TestSegsCopiedAtSubmit(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := newHarness(t, func(c *Config) {
		c.OnStored = func(s Stored) {
			if s.Inbound.PlatformID == "1" {
				entered <- struct{}{}
				<-block
			}
		}
	})
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	defer release()
	h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "挡住队列"), Options{})
	waitEntered(t, entered)

	in := msg(acctA, group("900"), "2", friend, "")
	segs := []types.Seg{{Kind: types.SegText, Text: "交的时候的样子"}}
	in.Segs = segs
	tk, err := h.in.Submit(context.Background(), in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	segs[0].Text = "后来被改掉了" // 适配器复用了这块内存
	release()
	if _, err := waitTicket(t, tk); err != nil {
		t.Fatal(err)
	}
	if got := h.events()[1].Text; got != "交的时候的样子" {
		t.Fatalf("存下来的是被改过的内存：%q", got)
	}
}

// 重复到达时，通知里的身份和来源是库里那条的：先补录来的角色 ca 的号发的一条存成 external，后来接管了，
// 另一个号 B 把同一条实时收到（重复，cb 第一次看到它），按这一次算会是 human，但库里是 external，通知也得是 external。
// 同一个号 A 再交一遍不通知（ca 早就看到过，见 TestRedeliveryOnlyNotifiesWhatIsNewToTheCharacter）
func TestDuplicateKeepsStoredOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	self := msg(acctA, group("900"), "5001", acctA, "她的号发的")
	self.Kind = types.InboundSelf
	self.Time = baseTime.Add(-time.Hour)
	h.submit(self, Options{Backfill: true}) // 还没接管：external
	live := msg(acctA, group("900"), "5002", friend, "实时收到的群消息")
	h.submit(live, Options{}) // 接管的时刻：baseTime
	if again := h.submit(self, Options{}); !again.Duplicate {
		t.Fatal("同一个号再交一遍，应该是重复")
	}
	seenByB := self
	seenByB.Account = acctB
	seenByB.Kind = types.InboundMessage // 对 B 号来说，这是群里另一个号发的消息
	dup := h.submit(seenByB, Options{})
	if !dup.Duplicate {
		t.Fatal("应该是重复")
	}
	h.in.Idle(context.Background())
	got := h.stored()
	if len(got) != 3 || got[2].Character != "cb" || got[0].Origin != "external" || got[2].Origin != "external" || !got[2].Duplicate || got[2].Actor != got[0].Actor {
		t.Fatalf("重复到达的 Origin 应该和库里一样（external）：%+v", got)
	}
	if hasTag(got[2].Tags, TagBackfill) || !hasTag(got[0].Tags, TagBackfill) {
		t.Errorf("Tags 说的是这一份交来的情况：%v %v", got[0].Tags, got[2].Tags)
	}
	if e := h.events()[0]; e.Origin != "external" {
		t.Errorf("库里的 origin = %s", e.Origin)
	}
}

// B 号先因为补录她的号发的消息把接管时刻查成"还没有"，后来 A 号存的一条实时群消息 B 号也交来了（重复到达，B 第一次看到它）：
// B 的接管时刻应该变成那条的时间——缓存要作废重查，不能还说"没有"
func TestTakeoverCacheRefreshedWhenOtherAccountSeesLiveEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	selfB := func(id string, at time.Duration) string {
		in := msg(acctB, group("900"), id, acctB, "B 号发的 "+id)
		in.Kind = types.InboundSelf
		in.Time = baseTime.Add(at)
		h.submit(in, Options{Backfill: true})
		evs := h.events()
		return evs[len(evs)-1].Origin
	}
	if got := selfB("1", time.Hour); got != "external" {
		t.Fatalf("B 还没有看到过实时事件：%s", got)
	}
	live := msg(acctA, group("900"), "2", friend, "A 号实时收到的群消息")
	live.Time = baseTime
	h.submit(live, Options{})
	liveB := live
	liveB.Account = acctB
	if r := h.submit(liveB, Options{}); !r.Duplicate {
		t.Fatal("应该是重复")
	}
	if got := selfB("3", time.Hour); got != "human" {
		t.Fatalf("B 看到了那条实时事件以后，接管时刻就有了：%s", got)
	}
}

// settle 等到现在收下的消息都处理完（入库了，通知也跑完了）
func (h *harness) settle() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.in.Idle(ctx); err != nil {
		h.t.Fatalf("等了 10 秒还有没处理完的：%v", err)
	}
}

// 后面的步骤（拦截链、闸门）只处理"这个角色这一次看到了新东西"的：同一个号把同一条再交一遍
// （重连后协议端重推；补录先存了、同号又实时到；补录和实时重叠）不通知，不然补录先存的那条旧消息、同号又实时到的那一份没有 backfill 标签，
// 会被当成新消息而对它开口；另一个角色第一次看到才通知（各角色的闸门各算各的）
func TestRedeliveryOnlyNotifiesWhatIsNewToTheCharacter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) {
		c.Admit = func(in types.Inbound) Admission {
			if in.Conv.ID == "901" { // 存下来但不记"看到了"的会话（陌生人、自检这类）
				return Admission{NoObserve: true}
			}
			return Admission{}
		}
	})
	var want []string
	step := func(acct string, conv types.Conversation, id string, backfill bool, notified string) Result {
		t.Helper()
		r := h.submit(msg(acct, conv, id, friend, "x"), Options{Backfill: backfill})
		h.settle()
		if notified != "" {
			want = append(want, notified)
		}
		var got []string
		for _, s := range h.stored() {
			got = append(got, fmt.Sprintf("%s/%s/dup=%v/backfill=%v/observed=%v", s.Character, s.Inbound.PlatformID, s.Duplicate, hasTag(s.Tags, TagBackfill), s.Observed))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("账号 %s 交 %s（补录 %v）以后，通知 %v，应该是 %v", acct, id, backfill, got, want)
		}
		return r
	}
	g := group("900")
	step(acctA, g, "5001", false, "ca/5001/dup=false/backfill=false/observed=true")
	if r := step(acctA, g, "5001", false, ""); !r.Duplicate { // 重连后协议端重推
		t.Errorf("同一个号再交一遍，凭据里仍是重复：%+v", r)
	}
	step(acctA, g, "5001", true, "") // 补录又取了一遍
	step(acctB, g, "5001", false, "cb/5001/dup=true/backfill=false/observed=true")
	step(acctB, g, "5001", false, "")
	// 补录先存了，同号又实时到：只有补录那一份通知（带 backfill 标签），实时的那一份没有新东西
	step(acctA, g, "5002", true, "ca/5002/dup=false/backfill=true/observed=true")
	step(acctA, g, "5002", false, "")
	// 不记"看到了"的会话：新存的通知一次，重复的什么也没新增
	n := group("901")
	step(acctA, n, "7", false, "ca/7/dup=false/backfill=false/observed=false")
	step(acctA, n, "7", false, "")
	step(acctB, n, "7", false, "")
	// 邻居：别的新消息照常通知，不被前面的"已看过"挡住
	step(acctA, g, "5003", false, "ca/5003/dup=false/backfill=false/observed=true")
	step(acctB, g, "5003", false, "cb/5003/dup=true/backfill=false/observed=true")
	if n := h.count(`SELECT count(*) FROM events`); n != 4 {
		t.Errorf("事件应该 4 条（5001、5002、7、5003），有 %d", n)
	}
}

// 两个号同在一个群，第二个号补录前一个号已经补过的历史：第二个号看到的每一条都是"第一次看到"，
// 接管时刻的缓存按已有那条的时间和标记精确更新，不整个作废——否则第二个号自己发的每一条补录都要回库把这个角色的观察扫一遍，
// 耗时随历史长度平方增长。数 FirstLiveAt 被调了几次：两个角色各读一次就够
func TestSecondAccountBackfillDoesNotRescanTakeover(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	var calls atomic.Int64
	real := h.in.firstLive
	h.in.firstLive = func(channel, character string) (int64, bool, error) {
		calls.Add(1)
		return real(channel, character)
	}
	const n = 60
	backfillHistory := func(acct string) {
		for i := 0; i < n; i++ {
			sender := friend
			switch i % 10 {
			case 3:
				sender = acctB
			case 7:
				sender = acctA
			}
			in := msg(acct, group("900"), strconv.Itoa(6000+i), sender, "第"+strconv.Itoa(i)+"条")
			if sender == acct {
				in.Kind = types.InboundSelf
			}
			in.Time = baseTime.Add(time.Duration(i-n) * time.Minute)
			h.submit(in, Options{Backfill: true})
		}
	}
	backfillHistory(acctA)
	backfillHistory(acctB)
	if got := calls.Load(); got > 2 {
		t.Fatalf("FirstLiveAt 被调了 %d 次，两个角色各读一次（共 2 次）就够", got)
	}
	if e, o := h.count(`SELECT count(*) FROM events`), h.count(`SELECT count(*) FROM observations`); e != n || o != 2*n {
		t.Fatalf("事件应该 %d 条、看到了 %d 条，有 %d、%d", n, 2*n, e, o)
	}
}

// 接管时刻的缓存在任何一次交付以后都和库里查出来的一样（实时的、补录的、两个号交叉、重复的、时间乱序的随机混合，种子固定）：
// 重复到达时库里已有那条的标记不一定和这一份一样，缓存要按已有那条的时间和标记更新
func TestTakeoverCacheMatchesStoreAfterEveryDelivery(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	characters := []string{"ca", "cb"}
	for _, c := range characters {
		if _, _, err := h.in.takeoverAt(chQQ, c); err != nil { // 先读一次，缓存从"已读"开始
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(20261026))
	accounts := []string{acctA, acctB}
	senders := []string{friend, owner, acctA, acctB}
	// 发送权属于要紧的那类，-short（合并前、推送前）也跑满（ADR-225 第 4 条）
	steps := 400
	for step := 0; step < steps; step++ {
		acct := accounts[rng.Intn(len(accounts))]
		sender := senders[rng.Intn(len(senders))]
		in := msg(acct, group("900"), strconv.Itoa(8000+rng.Intn(60)), sender, "随机第"+strconv.Itoa(step)+"次")
		if sender == acct {
			in.Kind = types.InboundSelf
		}
		in.Time = baseTime.Add(time.Duration(rng.Intn(1000)-500) * time.Minute)
		backfill := rng.Intn(2) == 0
		h.submit(in, Options{Backfill: backfill})
		for _, c := range characters {
			wantAt, wantHas, err := h.st.FirstLiveAt(chQQ, c)
			if err != nil {
				t.Fatal(err)
			}
			h.in.mu.Lock()
			st := *h.in.takeover[[2]string{chQQ, c}]
			h.in.mu.Unlock()
			if !st.loaded || st.has != wantHas || (wantHas && st.at != wantAt) {
				t.Fatalf("第 %d 次（账号 %s、发言人 %s、补录 %v）以后，%s 的缓存是 %+v，库里查出来是 has=%v at=%d",
					step, acct, sender, backfill, c, st, wantHas, wantAt)
			}
		}
	}
}

// 没有平台消息号（空、0）的消息不能去重，每条按单条入库，并留一行日志；日志里只有会话和号的原值，不写消息原文
func TestNoPlatformIDIsLogged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	const secret = "这句原文不该出现在日志里"
	for _, id := range []string{"", "0", "000"} {
		h.submit(msg(acctA, group("900"), id, friend, secret), Options{})
	}
	h.submit(msg(acctA, group("900"), "5001", friend, secret), Options{})
	h.submit(msg(acctA, group("900"), "5001", friend, secret), Options{}) // 有号的重复不是"没有号"
	var lines []string
	for _, l := range strings.Split(h.logText(), "\n") {
		if strings.Contains(l, "没有平台消息号") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("没有号的 3 条应该各留一行日志，有 %d 行：%q", len(lines), lines)
	}
	for i, id := range []string{`""`, `"0"`, `"000"`} {
		if !strings.Contains(lines[i], "qq:group:900") || !strings.Contains(lines[i], id) {
			t.Errorf("第 %d 行日志要有会话和号的原值 %s：%q", i, id, lines[i])
		}
	}
	if strings.Contains(h.logText(), secret) {
		t.Errorf("日志里不该有消息原文：%q", h.logText())
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 4 {
		t.Errorf("事件应该 4 条（3 条没号的各存一条，有号的 5001 重复一次只存一条），有 %d", n)
	}
}
