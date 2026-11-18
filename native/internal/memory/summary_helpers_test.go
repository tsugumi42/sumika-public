package memory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/prompts"
)

// 整理（SummarizeBatch）的测试台：假的事件来源、假的后台模型、一个迁移过的临时库。测试里的名字、号码都是编的

// repoRoot：出厂提示词在项目根的 prompts/ 下（native/internal/memory → 往上三层）
var repoRoot = filepath.Join("..", "..", "..")

// actor 是编出来的说话人
type actor struct{ kind, id, name, origin string }

var (
	ownerA = actor{"owner", "owner", "主人", "owner"}
	herA   = actor{"character", "suzune", "铃音", "character"}
	// 她的号上别人发的：ActorKind 还是 character（号是她的），origin 是 human（不是她说的）
	herAcct = actor{"character", "suzune", "铃音", "human"}
	pingA   = actor{"human", "qq:60001", "阿萍", "peer"}
	boA     = actor{"human", "qq:60002", "阿波", "peer"}
	otherCh = actor{"character", "kaede", "枫", "character"}
)

// memEvents 是内存里的事件来源：按 seq 升序，Events 取游标之后最早的 N 条
type memEvents struct {
	mu       sync.Mutex
	evs      []Event
	reads    int
	said     func(conv string) (Event, bool, error) // 不为空时 LastSaid 用它
	saidCall int
}

// add 加一条消息：seq 自己排，时间是 base 往后一分钟一条
func (e *memEvents) add(conv string, a actor, text string, mod ...func(*Event)) Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	seq := int64(len(e.evs) + 1)
	if len(e.evs) > 0 {
		seq = e.evs[len(e.evs)-1].Seq + 1
	}
	ev := Event{Seq: seq, Conversation: conv, At: testNow.Add(-90*time.Minute + time.Duration(seq)*time.Minute).UnixMilli(),
		ActorKind: a.kind, ActorID: a.id, ActorName: a.name, Origin: a.origin, Kind: "message", Text: text, Seen: true}
	if a.id == "owner" {
		ev.ActorName = "" // 主人固定叫"主人"，不查名字
	}
	for _, f := range mod {
		f(&ev)
	}
	e.evs = append(e.evs, ev)
	return ev
}

func (e *memEvents) Events(_ context.Context, conv string, after int64, limit int) ([]Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reads++
	var out []Event
	for _, ev := range e.evs {
		if ev.Conversation == conv && ev.Seq > after {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (e *memEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) {
	return nil, nil
}

// Conversations：每个会话最大的 seq，最近有动静的（最大 seq 大的）在前
func (e *memEvents) Conversations(context.Context) ([]ConvHead, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	last := map[string]int64{}
	for _, ev := range e.evs {
		last[ev.Conversation] = max(last[ev.Conversation], ev.Seq)
	}
	var out []ConvHead
	for c, s := range last {
		out = append(out, ConvHead{Conversation: c, LastSeq: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeq > out[j].LastSeq })
	return out, nil
}
func (e *memEvents) Find(context.Context, string, []string, int) ([]Event, error) {
	return nil, nil
}

// LastSaid：这个会话里最近一句 owner 或 peer 的话
func (e *memEvents) LastSaid(_ context.Context, conv string) (Event, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.saidCall++
	if e.said != nil {
		return e.said(conv)
	}
	for i := len(e.evs) - 1; i >= 0; i-- {
		if ev := e.evs[i]; ev.Conversation == conv && ev.Kind == "message" && (ev.Origin == "owner" || ev.Origin == "peer") {
			return ev, true, nil
		}
	}
	return Event{}, false, nil
}

func (e *memEvents) readCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reads
}

// scriptGen 是假的后台模型：依次回 replies（用完重复最后一个），记下每次的参数；block 不为空时先跑它（测试里拿来卡住、放行）
type scriptGen struct {
	mu      sync.Mutex
	replies []string
	calls   []genCall
	err     error
	block   func(ctx context.Context) error
}

type genCall struct{ model, prompt, key string }

func (g *scriptGen) Call(ctx context.Context, model, prompt, key string) (Reply, error) {
	g.mu.Lock()
	g.calls = append(g.calls, genCall{model, prompt, key})
	n, replies, err, block := len(g.calls), g.replies, g.err, g.block
	g.mu.Unlock()
	if block != nil {
		if err := block(ctx); err != nil {
			return Reply{}, err
		}
	}
	if err != nil {
		return Reply{}, err
	}
	if len(replies) == 0 {
		return Reply{Text: `{"episode_record":"no","ops":[]}`}, nil
	}
	return Reply{Text: replies[min(n, len(replies))-1]}, nil
}

func (g *scriptGen) set(replies ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replies = replies
}

func (g *scriptGen) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *scriptGen) last() genCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) == 0 {
		return genCall{}
	}
	return g.calls[len(g.calls)-1]
}

// testNow：测试里的"现在"（本地时间，行首的时间和星期都按它排）
var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)

// rig 是一个角色的记忆库副本 + 假的事件来源和模型
type rig struct {
	t     *testing.T
	m     *Memory
	ev    *memEvents
	gen   *scriptGen
	sink  *logSink
	conv  string
	model string
	clock time.Time // 模型和写库眼里的"现在"，测试里可以往后拨
	file  string    // 库副本的路径（重启、再开一个只读的看）
}

type rigOpts struct {
	seed  func(db *sql.DB) // 在迁移之前往 Bun 样子的库里放几行
	noGen bool             // Options.Gen 不配
	mod   func(*Options)
}

// newRig：conv 是整理的那个会话，游标立在 0（从头整理）
func newRig(t *testing.T, conv string, o ...rigOpts) *rig {
	t.Helper()
	var opt rigOpts
	if len(o) > 0 {
		opt = o[0]
	}
	r := &rig{t: t, ev: &memEvents{}, gen: &scriptGen{}, sink: &logSink{}, conv: conv, model: "bg-model", clock: testNow}
	file := migratedDB(t, opt.seed)
	r.file = file
	options := Options{Writable: true, SummarizeEnabled: true, WritePlaces: AllWritePlaces(), Character: "suzune", Name: "铃音", Logf: r.sink.logf, Events: r.ev,
		Model: func() string { return r.model }, Prompts: prompts.Store{Root: repoRoot}, Now: func() time.Time { return r.clock }}
	if !opt.noGen {
		options.Gen = r.gen
	}
	if opt.mod != nil {
		opt.mod(&options)
	}
	m, err := Open(context.Background(), file, nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	r.m = m
	if _, err := m.InitCursor(context.Background(), conv, 0); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) run() BatchResult {
	r.t.Helper()
	res, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
	if err != nil {
		r.t.Fatalf("SummarizeBatch：%v", err)
	}
	return res
}

func (r *rig) cursor() int64 {
	r.t.Helper()
	st, ok, err := r.m.SummaryState(context.Background(), r.conv)
	if err != nil || !ok {
		r.t.Fatalf("读游标：%v %v", ok, err)
	}
	return st.LastSeq
}

func (r *rig) say(a actor, text string, mod ...func(*Event)) Event {
	return r.ev.add(r.conv, a, text, mod...)
}

// reply 排好下一次模型的回复（一个整理输出的 JSON）
func (r *rig) reply(episodeRecord, episode string, ops ...string) {
	body := `{"episode_record":"` + episodeRecord + `","ops":[` + strings.Join(ops, ",") + `]`
	if episode != "" {
		body += `,"episode":"` + episode + `"`
	}
	r.gen.set(body + `}`)
}

// 常用的动作 JSON
func cand(text, subject string, src ...int64) string {
	return fmt.Sprintf(`{"op":"candidate","kind":"fact","text":%q,"subject":%q,"src":%s}`, text, subject, seqs(src))
}

func upsert(text, subject, visibility string, src ...int64) string {
	vis := ""
	if visibility != "" {
		vis = fmt.Sprintf(`,"visibility":%q`, visibility)
	}
	return fmt.Sprintf(`{"op":"upsert_fact","kind":"fact","text":%q,"subject":%q%s,"src":%s}`, text, subject, vis, seqs(src))
}

// upsertSup：upsert_fact 带 supersedes，visibility 写 private
func upsertSup(text, supersedes string, src ...int64) string {
	return fmt.Sprintf(`{"op":"upsert_fact","kind":"fact","text":%q,"subject":"owner","visibility":"private","supersedes":%q,"src":%s}`, text, supersedes, seqs(src))
}

func seqs(s []int64) string {
	parts := make([]string, len(s))
	for i, n := range s {
		parts[i] = fmt.Sprint(n)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// factRow 是库里一条事实的样子
type factRow struct {
	id, text, subject, status, attributed, origin, audience, sources string
	scopes                                                           string // 排好序、逗号隔开
	evidence                                                         int
	byself                                                           int // 其中本人说的
}

func (r *rig) facts() []factRow {
	r.t.Helper()
	rows, err := r.m.w.db.Query(`SELECT id, text, coalesce(subject, ''), status, coalesce(attributed_to, ''), coalesce(origin, ''), coalesce(audience_at_origin, ''), source_seqs FROM facts ORDER BY created_at, id`)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []factRow
	for rows.Next() {
		var f factRow
		if err := rows.Scan(&f.id, &f.text, &f.subject, &f.status, &f.attributed, &f.origin, &f.audience, &f.sources); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, f)
	}
	rows.Close()
	for i := range out {
		out[i].scopes = r.scopesOf("f:" + out[i].id)
		r.m.w.db.QueryRow(`SELECT count(*), coalesce(sum(by_subject), 0) FROM fact_evidence WHERE fact_id = ?`, out[i].id).Scan(&out[i].evidence, &out[i].byself)
	}
	return out
}

func (r *rig) scopesOf(ref string) string {
	rows, err := r.m.w.db.Query(`SELECT scope FROM item_scopes WHERE ref = ?`, ref)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var s []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		s = append(s, v)
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

// fact：按正文找一条（正文在一个库里不重复时用）
func (r *rig) fact(text string) factRow {
	r.t.Helper()
	for _, f := range r.facts() {
		if f.text == text {
			return f
		}
	}
	r.t.Fatalf("库里没有事实 %q：%v", text, r.factTexts())
	return factRow{}
}

func (r *rig) factTexts() []string {
	var out []string
	for _, f := range r.facts() {
		out = append(out, f.text)
	}
	return out
}

func (r *rig) episodes() []string {
	r.t.Helper()
	rows, err := r.m.w.db.Query(`SELECT text FROM episodes ORDER BY created_at, id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// jobs 是 memory_jobs 里的行数
func (r *rig) jobs() int { return count(r.t, r.m, `SELECT count(*) FROM memory_jobs`) }

// seedFact 往 Bun 样子的库里放一条有效事实（rigOpts.seed 里用）
func seedFact(t *testing.T, db *sql.DB, id, text, scope string, createdAt int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope) VALUES (?, 'fact', ?, 'owner', 'private', '[]', 'active', ?, ?)`,
		id, text, createdAt, scope); err != nil {
		t.Fatal(err)
	}
}
