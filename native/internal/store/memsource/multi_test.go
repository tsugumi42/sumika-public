package memsource

// 跨平台读事件的路由（规格-聊天记录按平台分库 第五节第 4 步、第六、七节）：用 store.OpenGo 开真的 local、qq 两个库，
// 造数据用各平台库的写方法。两个库的 seq 各从 1 起，所以每个断言都要求按会话选库、不混

import (
	"strings"
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type multiFixture struct {
	t     *testing.T
	set   *store.Set
	local *store.Store
	qq    *store.Store
}

func newMultiFixture(t *testing.T) *multiFixture {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	local, err := set.For("console")
	if err != nil {
		t.Fatal(err)
	}
	qq, err := set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	return &multiFixture{t, set, local, qq}
}

// add 往指定平台的库写一条；channel 从会话串第一段取（写库的方法不收渠道，列要跟会话对得上），at 是毫秒
func (f *multiFixture) add(st *store.Store, conv, kind, actorKind, actorID, origin, text string, at int64, seg any, replyTo string, observers ...string) (int64, string) {
	f.t.Helper()
	channel, _, _ := strings.Cut(conv, ":")
	e := store.NewEvent{Channel: channel, Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: origin, Kind: kind, Text: text, Segments: seg, CreatedAt: at}
	if replyTo != "" {
		e.ReplyTo = &replyTo
	}
	seq, err := st.AppendEvent(e, observers)
	if err != nil {
		f.t.Fatal(err)
	}
	var id string
	if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq = ?`, seq).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return seq, id
}

// msg 写一条普通的群友（human / peer）消息
func (f *multiFixture) msg(st *store.Store, conv, actorID, text string, at int64, observers ...string) (int64, string) {
	f.t.Helper()
	return f.add(st, conv, "message", "human", actorID, "peer", text, at, nil, "", observers...)
}

func (f *multiFixture) exec(st *store.Store, q string, args ...any) {
	f.t.Helper()
	if _, err := st.Events.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *multiFixture) multi(char string) *Multi {
	f.t.Helper()
	m, err := NewMulti(f.set, char)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func texts(evs []memory.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Text
	}
	return out
}

func equalTexts(got []memory.Event, want ...string) bool {
	g := texts(got)
	if len(g) != len(want) {
		return false
	}
	for i := range want {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// Find(不限会话)：两个库各取前 limit 条合并，按 At 降序；同一毫秒按平台名升序（local 在 qq 前）、再按 seq 降序；
// 取够 limit 条、不多不少；别的角色的、两库 seq 相同的都不串
func TestMultiFindAcrossLibraries(t *testing.T) {
	f := newMultiFixture(t)
	f.msg(f.local, cons, "qq:1", "关键词 本地 早", 1000, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地 同刻一", 3000, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地 同刻二", 3000, me) // 同平台同毫秒：seq 大的在前
	f.msg(f.local, cons, "qq:2", "关键词 只有凛看到的", 5000, other)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ 同刻", 3000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ 晚", 4000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ 早", 2000, me)
	m := f.multi(me)
	got, err := m.Find(ctx, "", []string{"%关键词%"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 别的角色 5000 那条不在；同一毫秒 local 的三条按 seq 降序在 qq 前面
	if !equalTexts(got, "关键词 QQ 晚", "关键词 本地 同刻二", "关键词 本地 同刻一", "关键词 QQ 同刻", "关键词 QQ 早", "关键词 本地 早") {
		t.Fatalf("跨库合并：%v", texts(got))
	}
	got, err = m.Find(ctx, "", []string{"%关键词%"}, 3)
	if err != nil || !equalTexts(got, "关键词 QQ 晚", "关键词 本地 同刻二", "关键词 本地 同刻一") {
		t.Fatalf("limit 3：%v %v", texts(got), err)
	}
}

// Conversations：两个库的会话都列出，按 LastAt 降序；同一毫秒按会话串升序；LastAt 是会话最新一条的时间
func TestMultiConversations(t *testing.T) {
	f := newMultiFixture(t)
	// 本地：她看到的早，最新那条她没看到（会话里的都算）
	f.msg(f.local, cons, "qq:1", "本地 早", 1000, me)
	lastLocal, _ := f.msg(f.local, cons, "qq:2", "本地 新（她没看到）", 4000, other)
	grpLast, _ := f.msg(f.qq, grp, "qq:1", "群里 最新", 7000, me)
	privLast, _ := f.msg(f.qq, priv, "qq:1", "私聊", 2000, me)
	sameAt, _ := f.msg(f.qq, "qq:group:same-at", "qq:1", "另一群 同一毫秒", 2000, me)
	f.msg(f.qq, "qq:group:only-rin", "qq:1", "只有凛", 9000, other)
	m := f.multi(me)
	got, err := m.Conversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 2000 那一秒两个会话：会话串升序，"qq:group:same-at" 在 "qq:private:..." 前
	want := []memory.ConvHead{
		{Conversation: grp, LastSeq: grpLast, LastAt: 7000},
		{Conversation: cons, LastSeq: lastLocal, LastAt: 4000},
		{Conversation: "qq:group:same-at", LastSeq: sameAt, LastAt: 2000},
		{Conversation: priv, LastSeq: privLast, LastAt: 2000},
	}
	if len(got) != len(want) {
		t.Fatalf("两个库的会话：%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个：%+v，应该是 %+v", i, got[i], want[i])
		}
	}
}

// Events、LastSaid、Find(有会话) 只从会话所在的库读：两个库都有 seq=1 时各拿各的，不合并
func TestMultiRoutesByConversation(t *testing.T) {
	f := newMultiFixture(t)
	f.msg(f.local, cons, "qq:1", "关键词 本地", 1000, me)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ", 9000, me)
	m := f.multi(me)
	got, err := m.Events(ctx, cons, 0, 10)
	if err != nil || !equalTexts(got, "关键词 本地") {
		t.Fatalf("本地会话：%v %v", texts(got), err)
	}
	if got, err = m.Events(ctx, grp, 0, 10); err != nil || !equalTexts(got, "关键词 QQ") {
		t.Fatalf("QQ 会话：%v %v", texts(got), err)
	}
	if ev, ok, err := m.LastSaid(ctx, cons); err != nil || !ok || ev.Text != "关键词 本地" {
		t.Fatalf("本地 LastSaid：%+v %v %v", ev, ok, err)
	}
	if ev, ok, err := m.LastSaid(ctx, grp); err != nil || !ok || ev.Text != "关键词 QQ" {
		t.Fatalf("QQ LastSaid：%+v %v %v", ev, ok, err)
	}
	if got, err = m.Find(ctx, cons, []string{"%关键词%"}, 10); err != nil || !equalTexts(got, "关键词 本地") {
		t.Fatalf("有会话的 Find 不合并：%v %v", texts(got), err)
	}
	if got, err = m.Find(ctx, grp, []string{"%关键词%"}, 10); err != nil || !equalTexts(got, "关键词 QQ") {
		t.Fatalf("有会话的 Find 不合并：%v %v", texts(got), err)
	}
}

// Revoked 按（会话，seq）：两个库都有 seq=1，本地那条撤回了、QQ 那条没有，各问各的
func TestMultiRevokedByConversation(t *testing.T) {
	f := newMultiFixture(t)
	localSeq, localID := f.msg(f.local, cons, "qq:1", "本地要撤回的", 1000, me)
	f.add(f.local, cons, "notice", "human", "qq:1", "peer", "撤回了一条消息", 2000, map[string]any{"notice": "recall"}, localID, me)
	qqSeq, _ := f.msg(f.qq, grp, "qq:1", "QQ 没撤回", 9000, me)
	if localSeq != 1 || qqSeq != 1 {
		t.Fatalf("两个库都要有 seq=1：%d %d", localSeq, qqSeq)
	}
	m := f.multi(me)
	got, err := m.Revoked(ctx, cons, []int64{localSeq})
	if err != nil || len(got) != 1 || !got[localSeq] {
		t.Fatalf("本地 seq=1 该撤回了：%v %v", got, err)
	}
	got, err = m.Revoked(ctx, grp, []int64{qqSeq})
	if err != nil || len(got) != 1 || got[qqSeq] {
		t.Fatalf("QQ seq=1 没撤回：%v %v", got, err)
	}
}

// 没登记的会话：每个入口都报错，不落到本地库
func TestMultiUnregisteredConversation(t *testing.T) {
	f := newMultiFixture(t)
	f.msg(f.local, cons, "qq:1", "本地的", 1000, me)
	m := f.multi(me)
	conv := "telegram:group:1"
	if _, err := m.Events(ctx, conv, 0, 10); err == nil {
		t.Error("Events 没报错")
	}
	if _, err := m.Find(ctx, conv, []string{"%本地%"}, 10); err == nil {
		t.Error("Find 没报错")
	}
	if _, err := m.Revoked(ctx, conv, []int64{1}); err == nil {
		t.Error("Revoked 没报错")
	}
	if _, _, err := m.LastSaid(ctx, conv); err == nil {
		t.Error("LastSaid 没报错")
	}
}

// 本地库没有 qq_people：本地会话照常读、Go 存下的名字还在、查不到的名字留空，不报错；QQ 库的名字照旧
func TestMultiLocalWithoutQQPeople(t *testing.T) {
	f := newMultiFixture(t)
	localSeq, _ := f.msg(f.local, cons, "qq:1", "本地没有 qq_people", 1000, me)
	f.add(f.local, cons, "message", "human", "qq:9", "peer", "Go 存了名字", 2000, map[string]any{"from": map[string]any{"nick": "九"}}, "", me)
	f.exec(f.qq, `INSERT INTO qq_people (uid, nickname, cards_json) VALUES ('1', '小一', '{}')`)
	qqSeq, _ := f.msg(f.qq, grp, "qq:1", "QQ 查得到名字", 9000, me)
	m := f.multi(me)
	got, err := m.Events(ctx, cons, 0, 10)
	if err != nil {
		t.Fatalf("本地库没有 qq_people 也要照常读：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("本地会话：%v", texts(got))
	}
	byText := map[string]memory.Event{}
	for _, ev := range got {
		byText[ev.Text] = ev
	}
	if ev := byText["本地没有 qq_people"]; ev.Seq != localSeq || ev.ActorName != "" {
		t.Errorf("本地取不到名字就给空：%+v", ev)
	}
	if ev := byText["Go 存了名字"]; ev.ActorName != "九" {
		t.Errorf("Go 存下的名字照用：%+v", ev)
	}
	qqEvents, err := m.Events(ctx, grp, 0, 10)
	if err != nil || len(qqEvents) != 1 || qqEvents[0].Seq != qqSeq || qqEvents[0].ActorName != "小一" {
		t.Fatalf("QQ 库按 qq_people 取名字：%+v %v", qqEvents, err)
	}
}

// 补录、导入的旧消息入库晚、seq 大、时间早：每个库的"前 limit 条"也得按时间取，不然合并时漏掉时间上最新的那条
// （第二轮复核给的反例：本地库实时那条 seq 最小、时间最新，被后来补录的三条挤出单库的前 2 条）
func TestMultiFindBackfillOrder(t *testing.T) {
	f := newMultiFixture(t)
	f.msg(f.local, cons, "qq:1", "关键词 本地 实时", 9000, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地 补录一", 1000, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地 补录二", 1100, me)
	f.msg(f.local, cons, "qq:1", "关键词 本地 补录三", 1200, me)
	f.msg(f.qq, grp, "qq:1", "关键词 QQ", 5000, me)
	got, err := f.multi(me).Find(ctx, "", []string{"%关键词%"}, 2)
	if err != nil || !equalTexts(got, "关键词 本地 实时", "关键词 QQ") {
		t.Fatalf("按时间的前 2 条该是本地实时和 QQ：%v %v", texts(got), err)
	}
	// 单库（有会话）也按时间排
	got, err = f.multi(me).Find(ctx, cons, []string{"%关键词%"}, 2)
	if err != nil || !equalTexts(got, "关键词 本地 实时", "关键词 本地 补录三") {
		t.Fatalf("单库按时间的前 2 条：%v %v", texts(got), err)
	}
}

// 平局规则真的被约束住：同一毫秒时平台名升序和"只按 seq 降序"给出不同的结果（本地那条 seq 1、QQ 那条 seq 5）；
// 同一毫秒的两个会话，会话串升序和"按 LastSeq 降序"也给出不同的结果
func TestMultiTieBreaks(t *testing.T) {
	f := newMultiFixture(t)
	for i, at := range []int64{100, 200, 300, 400} {
		f.msg(f.qq, grp, "qq:1", "垫着的"+string(rune('a'+i)), at, me)
	}
	f.msg(f.qq, grp, "qq:1", "关键词 QQ 同刻", 6000, me)     // QQ 库 seq 5
	f.msg(f.local, cons, "qq:1", "关键词 本地 同刻", 6000, me) // 本地库 seq 1
	got, err := f.multi(me).Find(ctx, "", []string{"%关键词%"}, 10)
	if err != nil || !equalTexts(got, "关键词 本地 同刻", "关键词 QQ 同刻") {
		t.Fatalf("同一毫秒该按平台名升序（local 在 qq 前），不是按 seq：%v %v", texts(got), err)
	}

	g := newMultiFixture(t)
	g.msg(g.qq, "qq:group:aaa", "qq:1", "a", 7000, me) // seq 1
	g.msg(g.qq, "qq:group:zzz", "qq:1", "z1", 100, me)
	g.msg(g.qq, "qq:group:zzz", "qq:1", "z2", 7000, me) // seq 3
	heads, err := g.multi(me).Conversations(ctx)
	if err != nil || len(heads) != 2 || heads[0].Conversation != "qq:group:aaa" || heads[1].Conversation != "qq:group:zzz" {
		t.Fatalf("同一毫秒该按会话串升序，不是按 LastSeq：%+v %v", heads, err)
	}
}

// 渠道登记了、但这一组没打开那个平台的库：按会话选库报错，不落到别的库
func TestMultiPlatformNotOpened(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	m, err := NewMulti(set, me)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Events(ctx, grp, 0, 10); err == nil {
		t.Fatal("只开了本地库，QQ 会话的 Events 该报错")
	}
	if _, err := m.Revoked(ctx, grp, []int64{1}); err == nil {
		t.Fatal("只开了本地库，QQ 会话的 Revoked 该报错")
	}
}
