package memsource

// 对着 ports.go 的约定、规格-记忆-v2 W0 的字段清单、记忆线 2026-10-27 定的口径（图片取最新看成功的描述、没发出去的认法）测，
// Bun 写的和 Go 写的两种事件都造

import (
	"context"
	"reflect"
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

const (
	me    = "suzu"
	other = "rin"
	grp   = "qq:group:test-9"
	priv  = "qq:private:100:200"
	cons  = "console:suzu"
)

type fixture struct {
	t  *testing.T
	st *store.Store
}

func newFixture(t *testing.T) *fixture {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fixture{t, st}
}

// add 存一条，observers 是看到它的角色；返回 seq 和 id
func (f *fixture) add(conv, kind, actorKind, actorID, origin, text string, seg any, replyTo string, observers ...string) (int64, string) {
	f.t.Helper()
	e := store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: origin, Kind: kind, Text: text, Segments: seg, CreatedAt: 1000}
	if conv == cons {
		e.Channel = "console"
	}
	if replyTo != "" {
		e.ReplyTo = &replyTo
	}
	seq, err := f.st.AppendEvent(e, observers)
	if err != nil {
		f.t.Fatal(err)
	}
	var id string
	f.st.Events.QueryRow(`SELECT id FROM events WHERE seq = ?`, seq).Scan(&id)
	return seq, id
}

func (f *fixture) msg(conv, actorID, text string, seg any, observers ...string) (int64, string) {
	kind, origin := "human", "peer"
	switch actorID {
	case "owner":
		kind, origin = "owner", "owner"
	case me:
		kind, origin = "character", "character"
	}
	return f.add(conv, "message", kind, actorID, origin, text, seg, "", observers...)
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Events.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) source(char string) *Source {
	f.t.Helper()
	s, err := New(f.st, char)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func bySeq(evs []memory.Event) map[int64]memory.Event {
	m := map[int64]memory.Event{}
	for _, e := range evs {
		m[e.Seq] = e
	}
	return m
}

var ctx = context.Background()

// Events：游标之后最早的 limit 条、升序；通知、自动化的、别人看不到的照样给（不过滤，B8）；Seen 按这个角色
func TestEventsOrderAndSeen(t *testing.T) {
	f := newFixture(t)
	a, _ := f.msg(grp, "qq:1", "一", nil, me)
	b, _ := f.add(grp, "notice", "human", "qq:1", "peer", "某人戳了戳你", nil, "", me)
	c, _ := f.add(grp, "message", "character", me, "automation:dry-run", "试运行的话", nil, "", me)
	d, _ := f.msg(grp, "qq:2", "只有凛看到", nil, other)
	f.msg(priv, "qq:3", "别的会话", nil, me)
	got, err := f.source(me).Events(ctx, grp, a, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != b || got[1].Seq != c || got[0].Kind != "notice" || got[1].Origin != "automation:dry-run" {
		t.Fatalf("游标之后最早的两条、升序、不过滤：%+v", got)
	}
	all, _ := f.source(me).Events(ctx, grp, 0, 10)
	if m := bySeq(all); len(all) != 4 || !m[a].Seen || m[d].Seen {
		t.Fatalf("只有这个角色看到的才 Seen：%+v", all)
	}
	if m := bySeq(mustEvents(t, f.source(other), grp)); m[a].Seen || !m[d].Seen {
		t.Fatalf("凛的视图：%+v", m)
	}
}

func mustEvents(t *testing.T, s *Source, conv string) []memory.Event {
	t.Helper()
	got, err := s.Events(ctx, conv, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 补录、自检：Bun 是顶层布尔，Go 在 tags 里；两种都认
func TestBackfillSelftest(t *testing.T) {
	f := newFixture(t)
	a, _ := f.msg(grp, "qq:1", "Bun 补录", map[string]any{"backfill": true}, me)
	b, _ := f.msg(grp, "qq:1", "Bun 自检", map[string]any{"selftest": true}, me)
	c, _ := f.msg(grp, "qq:1", "Go 补录", map[string]any{"tags": []string{"backfill"}}, me)
	d, _ := f.msg(grp, "qq:1", "Go 自检", map[string]any{"tags": []string{"fromSelf", "selftest"}}, me)
	e, _ := f.msg(grp, "qq:1", "普通的", map[string]any{"tags": []string{"fromSelf"}}, me)
	m := bySeq(mustEvents(t, f.source(me), grp))
	if !m[a].Backfill || m[a].Selftest || !m[b].Selftest || !m[c].Backfill || !m[d].Selftest || m[d].Backfill || m[e].Backfill || m[e].Selftest {
		t.Fatalf("%+v", m)
	}
}

// 撤回：撤回通知在后、同一个会话、reply_to 是被撤回那条的 id；Go 是 recall、Bun 是 group_recall / friend_recall；别的通知指着它不算
func TestRevoked(t *testing.T) {
	f := newFixture(t)
	a, aid := f.msg(grp, "qq:1", "撤回了的（Go）", nil, me)
	b, bid := f.msg(grp, "qq:1", "撤回了的（Bun）", nil, me)
	c, cid := f.msg(grp, "qq:1", "被戳的", nil, me)
	d, _ := f.msg(grp, "qq:1", "没事的", nil, me)
	p, pid := f.msg(priv, "qq:3", "私聊撤回（Bun）", nil, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "撤回了一条消息", map[string]any{"notice": "recall"}, aid, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "撤回了一条消息", map[string]any{"notice": "group_recall"}, bid, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "回应了一个表情", map[string]any{"notice": "reaction"}, cid, me)
	f.add(priv, "notice", "human", "qq:3", "peer", "撤回了一条消息", map[string]any{"notice": "friend_recall"}, pid, me)
	m := bySeq(mustEvents(t, f.source(me), grp))
	if !m[a].Revoked || !m[b].Revoked || m[c].Revoked || m[d].Revoked {
		t.Fatalf("群里：%+v", m)
	}
	// 按（会话，seq）问：别的会话里同号的 seq 不算（私聊那条虽然在这个库里，按群的会话问不该出现）
	g, err := f.source(me).Revoked(ctx, grp, []int64{a, c, p, 9999})
	if err != nil || !g[a] || g[c] || len(g) != 2 {
		t.Fatalf("群里按 seq 问：%v %v", g, err)
	}
	pv, err := f.source(me).Revoked(ctx, priv, []int64{p, a})
	if err != nil || !pv[p] || len(pv) != 1 {
		t.Fatalf("私聊按 seq 问：%v %v", pv, err)
	}
}

// 没发出去：Bun 是 unsent 有值且没有 partial；Go 看发送计划（试运行、失败、中止算，发完、发了一半、不知道、还在发不算）；
// 主窗口说到一半被停下的不算
func TestUnsent(t *testing.T) {
	f := newFixture(t)
	bunUnsent, _ := f.msg(grp, me, "Bun 没发出去", map[string]any{"unsent": "被守卫拦下"}, me)
	bunPartial, _ := f.msg(grp, me, "Bun 发了一半", map[string]any{"unsent": "断了", "partial": true}, me)
	aborted, _ := f.msg(cons, me, "主窗口说到一半", map[string]any{"aborted": true}, me)
	plans := map[string][2]string{"failed": {"send", "failed"}, "aborted": {"send", "aborted"}, "dry": {"dry-run", "dry-run"},
		"done": {"send", "done"}, "partial": {"send", "partial"}, "unknown": {"send", "unknown"}, "sending": {"send", "sending"}}
	seqOf := map[string]int64{}
	for name, kp := range plans {
		seq, _ := f.msg(grp, me, "Go "+name, map[string]any{"parts": []map[string]any{{"kind": "text", "text": "x"}}}, me)
		seqOf[name] = seq
		f.exec(`INSERT INTO outbound_plans (id, character_id, conversation, event_seq, account, origin, kind, state, created_at) VALUES (?, ?, ?, ?, 'a', 'character', ?, ?, 0)`,
			"p-"+name, me, grp, seq, kp[0], kp[1])
	}
	m := bySeq(mustEvents(t, f.source(me), grp))
	for name, want := range map[string]bool{"failed": true, "aborted": true, "dry": true, "done": false, "partial": false, "unknown": false, "sending": false} {
		if m[seqOf[name]].Unsent != want {
			t.Errorf("Go 计划 %s：Unsent 应该是 %v", name, want)
		}
	}
	if !m[bunUnsent].Unsent || m[bunPartial].Unsent {
		t.Errorf("Bun：没发出去的算、发了一半的不算：%+v %+v", m[bunUnsent], m[bunPartial])
	}
	if c := mustEvents(t, f.source(me), cons); len(c) != 1 || c[0].Seq != aborted || c[0].Unsent {
		t.Errorf("主窗口说到一半被停下的不算没发出去：%+v", c)
	}
}

// Bun 的真实库没有发送计划的表：照样能读，Bun 的写法照认
func TestBunLibraryWithoutPlans(t *testing.T) {
	f := newFixture(t)
	f.exec(`DROP TABLE outbound_parts`)
	f.exec(`DROP TABLE outbound_plans`)
	s := f.source(me)
	if s.plans {
		t.Fatal("没有这张表")
	}
	a, _ := f.msg(grp, me, "Bun 没发出去", map[string]any{"unsent": "断连"}, me)
	// 试运行、看完决定不说（Bun agent.ts 的 NOT_SAID）：没有发送计划可查，按 origin 认
	dry, _ := f.add(grp, "message", "character", me, "automation:dry-run", "试运行时想说的 关键词", map[string]any{"dryRun": true}, "", me)
	skip, _ := f.add(grp, "message", "character", me, "automation:self-skip", "[不说] 关键词 不用接", map[string]any{"selfSkip": "不用接"}, "", me)
	// 主人 QQ 私聊审批的卡片和被截走的回复行（规格-主人QQ私聊审批）：不算她说过的，也不是主人说的证据
	card, _ := f.add(grp, "message", "character", me, "automation:remote-approval", "【待批准 1234】关键词", nil, "", me)
	said, _ := f.msg(grp, me, "真说出口的 关键词", nil, me)
	if m := bySeq(mustEvents(t, s, grp)); !m[a].Unsent || !m[dry].Unsent || !m[skip].Unsent || !m[card].Unsent || m[said].Unsent {
		t.Fatalf("%+v", m)
	}
	if got, _ := s.Find(ctx, grp, []string{"%关键词%"}, 10); len(got) != 1 || got[0].Seq != said {
		t.Fatalf("没说出口的不算她说过，找原话找不到：%+v", got)
	}
}

// 名字：只给人。Go 存的发言当时的群名片 > 昵称；Bun 的按 qq_people 现在的：这个群的群名片 > 昵称（私聊只看昵称）；取不到给空，不退回号码
func TestActorName(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO qq_people (uid, nickname, cards_json) VALUES ('1', '小一', '{"test-9":"一号群名片","other":"别的群"}'), ('2', '小二', '{}'), ('4', NULL, '{}')`)
	goCard, _ := f.msg(grp, "qq:9", "Go 群名片", map[string]any{"from": map[string]any{"id": "9", "nick": "九", "card": "九号群名片"}}, me)
	goNick, _ := f.msg(grp, "qq:9", "Go 昵称", map[string]any{"from": map[string]any{"id": "9", "nick": "九"}}, me)
	bunCard, _ := f.msg(grp, "qq:1", "Bun 群名片", nil, me)
	bunNick, _ := f.msg(grp, "qq:2", "Bun 昵称", nil, me)
	unknown, _ := f.msg(grp, "qq:3", "查不到", nil, me)
	noNick, _ := f.msg(grp, "qq:4", "没有昵称", nil, me)
	owner, _ := f.msg(grp, "owner", "主人", map[string]any{"from": map[string]any{"id": "8", "nick": "主人的昵称"}}, me)
	privNick, _ := f.msg(priv, "qq:1", "私聊", nil, me)
	m := bySeq(mustEvents(t, f.source(me), grp))
	for seq, want := range map[int64]string{goCard: "九号群名片", goNick: "九", bunCard: "一号群名片", bunNick: "小二", unknown: "", noNick: "", owner: ""} {
		if m[seq].ActorName != want {
			t.Errorf("seq %d：名字 %q，应该是 %q", seq, m[seq].ActorName, want)
		}
	}
	if p := mustEvents(t, f.source(me), priv); p[0].Seq != privNick || p[0].ActorName != "小一" {
		t.Errorf("私聊只看昵称：%+v", p)
	}
}

// 引用：reply_to 是被引用那条的 id，带出说话人、名字（同一套取法）和开头的字；被引用的不在库里就没有
func TestQuote(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO qq_people (uid, nickname, cards_json) VALUES ('1', '小一', '{}')`)
	long := ""
	for range 80 {
		long += "字"
	}
	_, qid := f.msg(grp, "qq:1", long, nil, me)
	a, _ := f.add(grp, "message", "owner", "owner", "owner", "回复那条", nil, qid, me)
	b, _ := f.add(grp, "message", "human", "qq:2", "peer", "回复不在库里的", nil, "01NOTEXIST", me)
	// 被引用的那条后来撤回了：引用整条不给，撤回的话开头几个字也不带出来
	_, gone := f.msg(grp, "qq:1", "说错了要撤回的话", nil, me)
	c, _ := f.add(grp, "message", "human", "qq:2", "peer", "回复撤回了的", nil, gone, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "撤回了一条消息", map[string]any{"notice": "group_recall"}, gone, me)
	m := bySeq(mustEvents(t, f.source(me), grp))
	q := m[a].Quote
	if q == nil || q.ActorKind != "human" || q.ActorID != "qq:1" || q.ActorName != "小一" || len([]rune(q.Text)) != quoteRunes || m[b].Quote != nil {
		t.Fatalf("引用：%+v；不在库里的 %+v", q, m[b].Quote)
	}
	if m[c].Quote != nil {
		t.Fatalf("被引用的撤回了，不该带出它：%+v", m[c].Quote)
	}
}

// 图片：按占位先后；Go 的 image / emoji 段和她发的 parts，Bun 的 image 段（表情包也算、商城表情不算，没存下来的当就绪没描述）；
// 描述取最新一行看成功的，最新一次看失败而更早成功过的用成功那行；只看失败过的就绪没描述；没看过的没就绪
func TestImages(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO media_descriptions (hash, model, prompt_version, prep_version, note_id, ok, brief, at) VALUES
		('h-ok', 'm', 'v1', 1, 0, 1, '旧的描述', 1), ('h-ok', 'm', 'v2', 1, 0, 1, '新的描述', 2),
		('h-refail', 'm', 'v1', 1, 0, 1, '成功过的描述', 1), ('h-refail', 'm', 'v2', 1, 0, 0, NULL, 5),
		('h-fail', 'm', 'v1', 1, 0, 0, NULL, 1)`)
	goSeq, _ := f.msg(grp, "qq:1", "[图片][表情包]", map[string]any{"segs": []map[string]any{{"kind": "image", "hash": "h-ok"}, {"kind": "text", "text": "x"}, {"kind": "emoji", "hash": "h-none"}}}, me)
	// 没存下来的（skipped）带着哈希也当没存下来：Bun 识图只看 hash && !skipped，这张永远不会有描述
	bunSeq, _ := f.msg(grp, "qq:1", "[图片][商城表情：哈][表情包][图片][图片]", map[string]any{"segs": []map[string]any{
		{"type": "image", "hash": "h-refail"}, {"type": "image", "market": true, "hash": "h-market"}, {"type": "image", "sticker": true, "hash": "h-fail"},
		{"type": "image", "skipped": "failed"}, {"type": "image", "skipped": "too-big", "hash": "h-skip"}}}, me)
	herSeq, _ := f.msg(grp, me, "[图片]", map[string]any{"parts": []map[string]any{{"kind": "text", "text": "看"}, {"kind": "image", "ref": "h-ok"}}}, me)
	m := bySeq(mustEvents(t, f.source(me), grp))
	want := map[int64][]memory.Image{
		goSeq:  {{Hash: "h-ok", Brief: "新的描述", Ready: true}, {Hash: "h-none"}},
		bunSeq: {{Hash: "h-refail", Brief: "成功过的描述", Ready: true}, {Hash: "h-fail", Ready: true}, {Hash: "", Ready: true}, {Hash: "", Ready: true}},
		herSeq: {{Hash: "h-ok", Brief: "新的描述", Ready: true}},
	}
	for seq, w := range want {
		got := m[seq].Images
		if len(got) != len(w) {
			t.Errorf("seq %d：%+v，应该是 %+v", seq, got, w)
			continue
		}
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("seq %d 第 %d 张：%+v，应该是 %+v", seq, i, got[i], w[i])
			}
		}
	}
}

// LastSaid：这个会话里她看到的最近一句人说的话（owner 或 peer 的 message），她自己的、通知、她没看到的不算；没有就 ok 假
func TestLastSaid(t *testing.T) {
	f := newFixture(t)
	if _, ok, err := f.source(me).LastSaid(ctx, grp); ok || err != nil {
		t.Fatalf("空的会话：%v %v", ok, err)
	}
	a, _ := f.msg(grp, "qq:1", "群友说的", nil, me)
	f.msg(grp, me, "她自己说的", nil, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "戳了戳", nil, "", me)
	f.msg(grp, "qq:2", "她没看到的", nil, other)
	ev, ok, err := f.source(me).LastSaid(ctx, grp)
	if err != nil || !ok || ev.Seq != a || ev.Text != "群友说的" || !ev.Seen || ev.Conversation != grp {
		t.Fatalf("%+v %v %v", ev, ok, err)
	}
}

// Find：每一项都要匹配（LIKE ESCAPE '\'），conv 空是所有会话，她没看到的、撤回的、没发出去的不要，最新的在前、最多 limit 条；
// 去掉的比一页还多时接着往前翻
func TestFind(t *testing.T) {
	f := newFixture(t)
	a, _ := f.msg(grp, "qq:1", "周三去看牙医", nil, me)
	b, _ := f.msg(priv, "qq:3", "牙医说周三再来", nil, me)
	f.msg(grp, "qq:2", "周三看牙医（她没看到）", nil, other)
	f.msg(grp, "qq:1", "周三去爬山（只对上一项）", nil, me)
	_, rid := f.msg(grp, "qq:1", "周三牙医撤回了", nil, me)
	f.add(grp, "notice", "human", "qq:1", "peer", "撤回了一条消息", map[string]any{"notice": "recall"}, rid, me)
	f.msg(grp, me, "周三牙医没发出去", map[string]any{"unsent": "断连"}, me)
	pct, _ := f.msg(grp, "qq:1", "打了 50% 折", nil, me)
	f.msg(grp, "qq:1", "打了 500 折", nil, me)
	got, err := f.source(me).Find(ctx, "", []string{"%周三%", "%牙医%"}, 10)
	if err != nil || len(got) != 2 || got[0].Seq != b || got[1].Seq != a {
		t.Fatalf("所有会话、最新的在前、撤回的没发出去的没看到的不要：%+v %v", got, err)
	}
	if got, _ := f.source(me).Find(ctx, grp, []string{"%周三%", "%牙医%"}, 10); len(got) != 1 || got[0].Seq != a {
		t.Fatalf("只在这个会话：%+v", got)
	}
	if got, _ := f.source(me).Find(ctx, "", []string{`%50\%%`}, 10); len(got) != 1 || got[0].Seq != pct {
		t.Fatalf("转义过的 %% 不当通配符：%+v", got)
	}
	// 去掉的比一页还多：最早那条要翻过一整页撤回的才找得到
	first, _ := f.msg(grp, "qq:1", "关键词 最早的", nil, me)
	for range findPage + 5 {
		_, id := f.msg(grp, "qq:1", "关键词 撤回的", nil, me)
		f.add(grp, "notice", "human", "qq:1", "peer", "撤回了一条消息", map[string]any{"notice": "recall"}, id, me)
	}
	if got, _ := f.source(me).Find(ctx, grp, []string{"%关键词%"}, 3); len(got) != 1 || got[0].Seq != first {
		t.Fatalf("翻过一整页撤回的：%+v", got)
	}
	if got, _ := f.source(me).Find(ctx, grp, []string{"%周%"}, 1); len(got) != 1 {
		t.Fatalf("最多 limit 条：%+v", got)
	}
}

// Conversations：这个角色看到过事件的会话，LastSeq 是会话里全部事件最大的 seq（不只她看见的），
// LastAt 是那条的时间（不是会话里最大的时间：补录的行可以更晚进库、时间更早），最近有动静的在前
func TestConversations(t *testing.T) {
	f := newFixture(t)
	first, _ := f.msg(grp, "qq:1", "她看到的", nil, me)
	last, _ := f.msg(grp, "qq:2", "她没看到的、更新的", nil, other)
	p, _ := f.msg(priv, "qq:3", "私聊", nil, me)
	f.msg("qq:group:only-rin", "qq:4", "只有凛的群", nil, other)
	f.exec(`UPDATE events SET created_at = 9000 WHERE seq = ?`, first)
	f.exec(`UPDATE events SET created_at = 5000 WHERE seq = ?`, last)
	f.exec(`UPDATE events SET created_at = 3000 WHERE seq = ?`, p)
	got, err := f.source(me).Conversations(ctx)
	if err != nil || len(got) != 2 || got[0] != (memory.ConvHead{Conversation: priv, LastSeq: p, LastAt: 3000}) || got[1] != (memory.ConvHead{Conversation: grp, LastSeq: last, LastAt: 5000}) {
		t.Fatalf("%+v %v", got, err)
	}
}

// 从 Bun 导入的历史事件带 import_meta.legacy_segments：识图槽按旧段用 Bun 的规则取（商城图、mface 不收），
// 所以正文里唯一的"[图片]"配到普通图的描述，Images 里没有商城哈希，也没有一张没就绪的图（不然整理会一直等它）
func TestImagesUseLegacySegmentsForImportedEvents(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO media_descriptions (hash, model, prompt_version, prep_version, note_id, ok, brief, at) VALUES ('h-real', 'm', 'v1', 1, 0, 1, '普通图的描述', 1)`)
	const hMarket = "h-market"
	// 商城图在前、普通图在后：中立段里两个都是 emoji/image，照 kind 收会多出一个商城槽
	marketFirst, _ := f.msg(grp, "qq:1", "[商城表情：哈][图片]", map[string]any{
		"segs": []map[string]any{{"kind": "emoji", "hash": hMarket}, {"kind": "image", "hash": "h-real"}},
		"import_meta": map[string]any{"version": 1, "legacy_segments": []map[string]any{
			{"type": "image", "market": true, "hash": hMarket},
			{"type": "image", "hash": "h-real"},
		}},
	}, me)
	// mface 在前的变体：mface 不算占位
	mfaceFirst, _ := f.msg(grp, "qq:1", "[商城表情：嗯][图片]", map[string]any{
		"segs": []map[string]any{{"kind": "emoji", "ref": "e-1"}, {"kind": "image", "hash": "h-real"}},
		"import_meta": map[string]any{"version": 1, "legacy_segments": []map[string]any{
			{"type": "mface", "emojiId": "e-1", "name": "编的名字"},
			{"type": "image", "hash": "h-real"},
		}},
	}, me)

	m := bySeq(mustEvents(t, f.source(me), grp))
	for name, seq := range map[string]int64{"商城图在前": marketFirst, "mface 在前": mfaceFirst} {
		got := m[seq].Images
		if len(got) != 1 {
			t.Fatalf("%s：取了 %d 个识图槽（%+v），要 1 个——商城图不该占槽", name, len(got), got)
		}
		if got[0].Hash != "h-real" || got[0].Brief != "普通图的描述" || !got[0].Ready {
			t.Fatalf("%s：[图片] 配到的是 %+v，要普通图带描述且就绪", name, got[0])
		}
	}
}

// 旧段定槽位；只在槽数一致时按 image 出现顺序补 hash，已有 hash 和 skipped 都不能被盖掉。
func TestImportedImageSlotsUseMergedHashes(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO media_descriptions (hash, model, prompt_version, prep_version, note_id, ok, brief, at)
		VALUES ('h-merged', 'm', 'v1', 1, 0, 1, '编的合并图片描述', 1),
		('h-old', 'm', 'v1', 1, 0, 1, '编的旧图片描述', 1)`)
	cases := []struct {
		name            string
		legacy, neutral []map[string]any
		want            []memory.Image
	}{
		{"补合并哈希", []map[string]any{{"type": "image"}}, []map[string]any{{"kind": "image", "hash": "h-merged"}},
			[]memory.Image{{Hash: "h-merged", Brief: "编的合并图片描述", Ready: true}}},
		{"商城和 mface 不占槽", []map[string]any{{"type": "image", "market": true}, {"type": "mface"}, {"type": "image"}},
			[]map[string]any{{"kind": "emoji", "hash": "h-market"}, {"kind": "emoji"}, {"kind": "image", "hash": "h-merged"}},
			[]memory.Image{{Hash: "h-merged", Brief: "编的合并图片描述", Ready: true}}},
		{"保留旧哈希", []map[string]any{{"type": "image", "hash": "h-old"}}, []map[string]any{{"kind": "image", "hash": "h-merged"}},
			[]memory.Image{{Hash: "h-old", Brief: "编的旧图片描述", Ready: true}}},
		{"跳过图仍占空槽", []map[string]any{{"type": "image", "skipped": true}, {"type": "image"}},
			[]map[string]any{{"kind": "image", "hash": "h-old"}, {"kind": "image", "hash": "h-merged"}},
			[]memory.Image{{Ready: true}, {Hash: "h-merged", Brief: "编的合并图片描述", Ready: true}}},
		{"槽数不符不补", []map[string]any{{"type": "image"}, {"type": "image", "hash": "h-old"}},
			[]map[string]any{{"kind": "image", "hash": "h-merged"}},
			[]memory.Image{{Ready: true}, {Hash: "h-old", Brief: "编的旧图片描述", Ready: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seq, _ := f.msg(grp, "qq:100001", "编的图片消息", map[string]any{
				"segs": c.neutral, "import_meta": map[string]any{"version": 1, "legacy_segments": c.legacy},
			}, me)
			got := bySeq(mustEvents(t, f.source(me), grp))[seq].Images
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("识图槽应为 %+v，得到 %+v", c.want, got)
			}
		})
	}
}
