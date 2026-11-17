package memory

// 共用场景（testdata/shared/memory-write，格式见该目录 README.md）：记忆写入第一档——remember、memory_forget、写入内核（Batch 和各动作）、取代保护、同文查重、待确认。
// Go 和 Bun 后台验收同一份：这里用真实的 Memory.Remember / Forget / apply 跑，Bun 的 server/test/memory-write.test.ts 读同一批文件逐场景对。
// 全部虚构的数据。没有真模型、没有嵌入、没有共用池（Shared 为 nil）。
// SUMIKA_SHARED_UPDATE=1 把这次的实际结果写回每个场景的 expect（输入不动；提交前人读过，对得上 source_tests 里写的原测试断言）。
//
// 时钟固定为 mwBase，编号由场景给（new_ids，按写入顺序一个个用），证据的“哪一天”按 UTC；库里的时间一律写成“距结束时的时钟多少秒前”（*_ago_s，秒，可以带小数；毫秒数一长就成了像号码的数字），不放绝对时间。
// 工具层（memplugin 里参数取值、类型检查、内部错误换成固定的话）在别的包里，这里用 mwToolRemember / mwToolForget 照 plugin.go 的 tool 函数抄了一份。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/sharedtest"
)

var mwBase = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// ---------- 场景文件的结构（字段名和 JSON 一一对应，读到不认识的字段会报错） ----------

type mwScenario struct {
	About       string    `json:"about"`
	SourceTests []string  `json:"source_tests"`
	Writable    *bool     `json:"writable,omitempty"` // 默认真；假 = 写入没打开（只读角色）
	Options     mwOptions `json:"options"`
	Events      []mwEvent `json:"events,omitempty"`
	Start       mwStart   `json:"start"`
	NewIDs      []string  `json:"new_ids,omitempty"`
	Steps       []mwStep  `json:"steps"`
	Expect      mwFinal   `json:"expect"`
}

type mwOptions struct {
	AutoConfirm      bool     `json:"auto_confirm,omitempty"`
	ConfirmOccasions int      `json:"confirm_occasions,omitempty"`
	ConfirmKinds     []string `json:"confirm_kinds,omitempty"` // 不写 = 默认（fact、preference）
}

// mwEvent 是事件来源里的一条事件。from：owner（默认）、peer（群友）、character（她自己）
type mwEvent struct {
	Seq          int64   `json:"seq"`
	Conversation string  `json:"conversation"`
	Text         string  `json:"text"`
	AgoS         float64 `json:"ago_s,omitempty"`
	From         string  `json:"from,omitempty"`
	Kind         string  `json:"kind,omitempty"` // 默认 message
	NotSeen      bool    `json:"not_seen,omitempty"`
	Revoked      bool    `json:"revoked,omitempty"`
	Unsent       bool    `json:"unsent,omitempty"`
	Backfill     bool    `json:"backfill,omitempty"`
	Selftest     bool    `json:"selftest,omitempty"`
}

type mwStart struct {
	Facts     []mwSeedFact     `json:"facts,omitempty"`
	Episodes  []mwSeedEpisode  `json:"episodes,omitempty"`
	Revisions []mwSeedRevision `json:"revisions,omitempty"`
	Evidence  []mwSeedEvidence `json:"evidence,omitempty"`
}

// mwSeedFact：confirmed_by 不写 = NULL（Bun 导入的）
type mwSeedFact struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind,omitempty"`
	Text         string   `json:"text"`
	Subject      string   `json:"subject,omitempty"`
	Status       string   `json:"status,omitempty"`
	ConfirmedBy  *string  `json:"confirmed_by,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	Temporal     string   `json:"temporal,omitempty"`
	ValidToInS   *float64 `json:"valid_to_in_s,omitempty"` // 距现在多少秒后失效；负数是已经失效
	CreatedAgoS  float64  `json:"created_ago_s,omitempty"`
	EventAgoS    *float64 `json:"event_ago_s,omitempty"`
	Conversation string   `json:"conversation,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
}

type mwSeedEpisode struct {
	ID           string   `json:"id"`
	Conversation string   `json:"conversation"`
	Text         string   `json:"text"`
	Scopes       []string `json:"scopes,omitempty"`
	FromSeq      int64    `json:"from_seq,omitempty"`
	ToSeq        int64    `json:"to_seq,omitempty"`
}

type mwSeedRevision struct {
	FactID string         `json:"fact_id"`
	Op     string         `json:"op"`
	By     string         `json:"by"`
	After  map[string]any `json:"after,omitempty"`
}

type mwSeedEvidence struct {
	FactID       string `json:"fact_id"`
	DaysAgo      int    `json:"days_ago,omitempty"`
	Conversation string `json:"conversation"`
	Speaker      string `json:"speaker,omitempty"`
	BySubject    bool   `json:"by_subject,omitempty"`
}

// mwStep：do 是 remember / forget / batch / advance / close
type mwStep struct {
	Do string `json:"do"`
	// remember、forget：place 是听众所在的场合（见 README 的表），args 是模型给工具的原始参数，input_seqs 是宿主绑定的本轮输入
	Place         string         `json:"place,omitempty"`
	Args          map[string]any `json:"args,omitempty"`
	InputSeqs     []int64        `json:"input_seqs,omitempty"`
	ValidUntilInS *float64       `json:"valid_until_in_s,omitempty"` // 填进 args.valid_until：现在 + 这么多秒，RFC3339 UTC
	// batch：by 是 remember（主人本人）/ remember-ai / summary；on_dup 是 report / merge（不写：remember 用 report，其余 merge）
	By    string `json:"by,omitempty"`
	OnDup string `json:"on_dup,omitempty"`
	Ops   []mwOp `json:"ops,omitempty"`
	// advance：时钟往后走这么多毫秒
	S      float64      `json:"s,omitempty"`
	Expect mwStepExpect `json:"expect"`
}

type mwOp struct {
	Op               string   `json:"op"` // fact / revoke / episode / evidence / confirm
	Kind             string   `json:"kind,omitempty"`
	Text             string   `json:"text,omitempty"`
	Subject          string   `json:"subject,omitempty"`
	Status           string   `json:"status,omitempty"`
	ConfirmedBy      string   `json:"confirmed_by,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	Supersedes       string   `json:"supersedes,omitempty"`
	SupersedesCode   string   `json:"supersedes_code,omitempty"`
	Temporal         string   `json:"temporal,omitempty"`
	ValidToInS       *float64 `json:"valid_to_in_s,omitempty"`
	SourceSeqs       []int64  `json:"source_seqs,omitempty"`
	Origin           string   `json:"origin,omitempty"`
	Conversation     string   `json:"conversation,omitempty"`
	AudienceAtOrigin string   `json:"audience_at_origin,omitempty"`
	AttributedTo     string   `json:"attributed_to,omitempty"`
	EventAgoS        *float64 `json:"event_ago_s,omitempty"`
	Evidence         *mwEvid  `json:"evidence,omitempty"`
	OwnerSaid        bool     `json:"owner_said,omitempty"`
	ID               string   `json:"id,omitempty"`   // revoke：全长编号
	Code             string   `json:"code,omitempty"` // revoke：短码
	Reason           string   `json:"reason,omitempty"`
	FactID           string   `json:"fact_id,omitempty"` // evidence、confirm
	FromSeq          int64    `json:"from_seq,omitempty"`
	ToSeq            int64    `json:"to_seq,omitempty"`
}

type mwEvid struct {
	Conversation string   `json:"conversation"`
	AgoS         *float64 `json:"ago_s,omitempty"` // 不写 = 现在
	Seq          int64    `json:"seq,omitempty"`
	Speaker      string   `json:"speaker,omitempty"`
	BySubject    bool     `json:"by_subject,omitempty"`
}

// mwStepExpect：工具步骤给 text 和 is_error；batch 步骤给 outcomes、counts，或者 error
type mwStepExpect struct {
	Text     string         `json:"text,omitempty"`
	IsError  bool           `json:"is_error,omitempty"`
	Error    *mwErr         `json:"error,omitempty"`
	Outcomes []mwOutcome    `json:"outcomes,omitempty"`
	Counts   map[string]int `json:"counts,omitempty"`
}

type mwErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type mwOutcome struct {
	Result    string `json:"result"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
	Replaced  string `json:"replaced,omitempty"` // 被取代的那条原来的正文
	Promoted  bool   `json:"promoted,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// mwFinal：场景结束时相关表的行。键是列名（时间列换成 *_ago_s，单位秒），scopes 是 item_scopes 里排好序的一组
type mwFinal struct {
	Facts     []map[string]any `json:"facts"`
	Episodes  []map[string]any `json:"episodes"`
	Revisions []map[string]any `json:"revisions"`
	Evidence  []map[string]any `json:"evidence"`
}

// mwDur：场景里的秒（可以带小数）换成时长，精确到毫秒
func mwDur(s float64) time.Duration { return time.Duration(math.Round(s*1000)) * time.Millisecond }

// ---------- 替身 ----------

type mwClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mwClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mwClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// mwEvents 是事件来源：按场景给的事件按 seq 升序读，其余口子用不到
type mwEvents struct{ evs []Event }

func (e *mwEvents) Events(_ context.Context, conv string, after int64, limit int) ([]Event, error) {
	var out []Event
	for _, ev := range e.evs {
		if ev.Conversation == conv && ev.Seq > after {
			out = append(out, ev)
		}
	}
	slices.SortFunc(out, func(a, b Event) int { return int(a.Seq - b.Seq) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (e *mwEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) { return nil, nil }
func (e *mwEvents) LastSaid(context.Context, string) (Event, bool, error)            { return Event{}, false, nil }
func (e *mwEvents) Find(context.Context, string, []string, int) ([]Event, error)     { return nil, nil }
func (e *mwEvents) Conversations(context.Context) ([]ConvHead, error)                { return nil, nil }

func mwEventOf(se mwEvent, now time.Time) Event {
	ev := Event{Seq: se.Seq, Conversation: se.Conversation, Text: se.Text, At: now.Add(-mwDur(se.AgoS)).UnixMilli(),
		ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Seen: !se.NotSeen,
		Revoked: se.Revoked, Unsent: se.Unsent, Backfill: se.Backfill, Selftest: se.Selftest}
	if se.Kind != "" {
		ev.Kind = se.Kind
	}
	switch se.From {
	case "peer":
		ev.ActorKind, ev.ActorID, ev.ActorName, ev.Origin = "human", "qq:u2", "小明", "peer"
	case "character":
		ev.ActorKind, ev.ActorID, ev.Origin = "character", "suzune", "character"
	}
	return ev
}

// mwPlaces 是 step.place 对应的听众（和 README 的表一致）
var mwPlaces = map[string]Audience{
	"local_private":          {Channel: "console", Conversation: "console:a", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true},
	"qq_private_owner":       {Channel: "qq", Conversation: "qq:private:u1", Scene: "private", PrivateOK: true, OwnerAsked: true},
	"local_group":            {Channel: "console", Conversation: "console:group:g1", Scene: "group", PrivateOK: true, OwnerAsked: true, PersonIDs: []string{"owner"}},
	"qq_group_owner_asked":   {Channel: "qq", Conversation: "qq:group:ga", Scene: "group", PrivateOK: true, OwnerAsked: true, PersonIDs: []string{"owner"}},
	"qq_group":               {Channel: "qq", Conversation: "qq:group:ga", Scene: "group", PrivateOK: true},
	"qq_group_no_scene":      {Channel: "qq", Conversation: "qq:group:ga", PrivateOK: true, OwnerAsked: true},
	"qq_private_other":       {Channel: "qq", Conversation: "qq:private:u2", Scene: "private", PersonIDs: []string{"u2"}},
	"local_private_no_owner": {Channel: "console", Conversation: "console:a", Scene: "private"},
	"qq_private_no_owner":    {Channel: "qq", Conversation: "qq:private:u1", Scene: "private", OwnerAsked: true},
}

func mwAudience(t *testing.T, place string) Audience {
	t.Helper()
	if place == "" {
		place = "local_private"
	}
	a, ok := mwPlaces[place]
	if !ok {
		t.Fatalf("place %q 不认识", place)
	}
	return a
}

// ---------- 跑一个场景 ----------

func TestSharedMemoryWrite(t *testing.T) {
	t.Parallel()
	for _, file := range sharedtest.Files(t, "memory-write") {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			t.Parallel()
			mwRun(t, file)
		})
	}
}

func mwRun(t *testing.T, file string) {
	var sc mwScenario
	sharedtest.Load(t, file, &sc)
	if len(sc.Steps) == 0 || sc.About == "" || len(sc.SourceTests) == 0 {
		t.Fatalf("%s：about、source_tests、steps 都要写", filepath.Base(file))
	}
	clock := &mwClock{now: mwBase}
	dbFile := migratedDB(t, nil)
	mwSeed(t, dbFile, sc.Start, clock.now)

	events := &mwEvents{}
	for _, se := range sc.Events {
		events.evs = append(events.evs, mwEventOf(se, clock.now))
	}
	writable := sc.Writable == nil || *sc.Writable
	sink := &logSink{}
	m, err := Open(context.Background(), dbFile, nil, nil, Options{Writable: writable, Character: "suzune", Events: events, Logf: sink.logf, Now: clock.Now,
		AutoConfirm: sc.Options.AutoConfirm, ConfirmOccasions: sc.Options.ConfirmOccasions, ConfirmKinds: sc.Options.ConfirmKinds})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if m.w != nil {
		m.w.loc = time.UTC
		queue := slices.Clone(sc.NewIDs)
		var mu sync.Mutex
		m.w.newID = func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(queue) == 0 {
				t.Errorf("new_ids 不够用了")
				return fmt.Sprintf("OVERFLOW%018d", time.Now().UnixNano())
			}
			id := queue[0]
			queue = queue[1:]
			return id
		}
	}

	got := make([]mwStepExpect, len(sc.Steps))
	for i, st := range sc.Steps {
		got[i] = mwStep1(t, m, clock, st)
	}
	final := mwTables(t, dbFile, clock.Now())

	if sharedtest.Update() {
		for i := range sc.Steps {
			sc.Steps[i].Expect = got[i]
		}
		sc.Expect = final
		sharedtest.Save(t, file, sc)
		return
	}
	for i := range sc.Steps {
		if ok, want, have := sharedtest.Same(sc.Steps[i].Expect, got[i]); !ok {
			t.Errorf("第 %d 步（%s）不一样：\n期望 %s\n实际 %s", i+1, sc.Steps[i].Do, want, have)
		}
	}
	if ok, want, have := sharedtest.Same(sc.Expect, final); !ok {
		t.Errorf("结束时的表不一样：\n期望 %s\n实际 %s", want, have)
	}
}

func mwStep1(t *testing.T, m *Memory, clock *mwClock, st mwStep) mwStepExpect {
	t.Helper()
	switch st.Do {
	case "advance":
		clock.advance(mwDur(st.S))
		return mwStepExpect{}
	case "close":
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		return mwStepExpect{}
	case "remember", "forget":
		args := map[string]any{}
		for k, v := range st.Args {
			args[k] = v
		}
		if st.ValidUntilInS != nil {
			args["valid_until"] = clock.Now().Add(mwDur(*st.ValidUntilInS)).UTC().Format(time.RFC3339)
		}
		a := mwAudience(t, st.Place)
		var r ToolResult
		if st.Do == "remember" {
			r = mwToolRemember(m, a, args, st.InputSeqs)
		} else {
			r = mwToolForget(m, a, args, st.InputSeqs)
		}
		return mwStepExpect{Text: r.Text, IsError: r.IsError}
	case "batch":
		b := Batch{By: map[string]string{"remember": ByRemember, "remember-ai": ByRememberAI, "summary": BySummary}[st.By]}
		if b.By == "" {
			b.By = st.By // 空的、不认识的原样交给 validate
		}
		b.OnDup = DupMerge
		if st.OnDup == "report" || (st.OnDup == "" && st.By != BySummary) {
			b.OnDup = DupReport
		}
		if st.Place != "" {
			a := mwAudience(t, st.Place)
			b.Audience = &a
		}
		for _, o := range st.Ops {
			b.Ops = append(b.Ops, mwOpOf(t, o, clock.Now()))
		}
		res, err := m.apply(context.Background(), b)
		if err != nil {
			return mwStepExpect{Error: &mwErr{Code: mwErrCode(err), Message: err.Error()}}
		}
		out := mwStepExpect{Counts: res.Counts}
		for _, o := range res.Outcomes {
			oc := mwOutcome{Result: string(o.Result), Ref: o.Ref, Status: o.Status, Promoted: o.Promoted, Confirmed: o.Confirmed}
			if o.Replaced != nil {
				oc.Replaced = o.Replaced.Text
			}
			out.Outcomes = append(out.Outcomes, oc)
		}
		if len(out.Counts) == 0 {
			out.Counts = nil
		}
		return out
	}
	t.Fatalf("do %q 不认识", st.Do)
	return mwStepExpect{}
}

func mwErrCode(err error) string {
	for _, c := range []struct {
		err  error
		code string
	}{
		{ErrBusy, "busy"}, {ErrClosed, "closed"}, {ErrNotWritable, "not_writable"}, {ErrRememberExpired, "remember_expired"},
		{ErrRememberRevoked, "remember_revoked"}, {ErrReplaceGone, "replace_gone"}, {ErrReplaceProtected, "replace_protected"},
		{ErrReplaceAmbiguous, "replace_ambiguous"}, {ErrGenerationChanged, "generation_changed"}, {ErrCursorMoved, "cursor_moved"},
	} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	if s := err.Error(); strings.HasPrefix(s, "第 ") || strings.HasPrefix(s, "Batch.By") {
		return "invalid"
	}
	return "other"
}

func mwOpOf(t *testing.T, o mwOp, now time.Time) Op {
	t.Helper()
	ms := func(p *float64) *int64 {
		if p == nil {
			return nil
		}
		v := now.Add(mwDur(*p)).UnixMilli()
		return &v
	}
	ago := func(p *float64) int64 {
		if p == nil {
			return 0
		}
		return now.Add(-mwDur(*p)).UnixMilli()
	}
	switch o.Op {
	case "fact":
		f := FactOp{Kind: o.Kind, Text: o.Text, Subject: o.Subject, Status: o.Status, ConfirmedBy: o.ConfirmedBy, Scopes: o.Scopes, Supersedes: o.Supersedes, SupersedesCode: o.SupersedesCode,
			Temporal: o.Temporal, ValidTo: ms(o.ValidToInS), SourceSeqs: o.SourceSeqs, Origin: o.Origin, Conversation: o.Conversation, AudienceAtOrigin: o.AudienceAtOrigin,
			AttributedTo: o.AttributedTo, EventAt: ago(o.EventAgoS), OwnerSaid: o.OwnerSaid}
		if e := o.Evidence; e != nil {
			f.Evidence = &Evidence{Conversation: e.Conversation, At: ago(e.AgoS), Seq: e.Seq, Speaker: e.Speaker, BySubject: e.BySubject}
		}
		return f
	case "revoke":
		return RevokeOp{ID: o.ID, Code: o.Code, Reason: o.Reason}
	case "episode":
		return EpisodeOp{Conversation: o.Conversation, Text: o.Text, FromSeq: o.FromSeq, ToSeq: o.ToSeq, Scopes: o.Scopes, Origin: o.Origin, AudienceAtOrigin: o.AudienceAtOrigin, EventAt: ago(o.EventAgoS)}
	case "evidence":
		e := o.Evidence
		if e == nil {
			t.Fatalf("evidence 动作要写 evidence")
		}
		return EvidenceOp{FactID: o.FactID, Evidence: Evidence{Conversation: e.Conversation, At: ago(e.AgoS), Seq: e.Seq, Speaker: e.Speaker, BySubject: e.BySubject}}
	case "confirm":
		return ConfirmOp{FactID: o.FactID}
	}
	t.Fatalf("op %q 不认识", o.Op)
	return nil
}

// mwToolRemember：照 memplugin/plugin.go 的 tool 函数抄的一份参数取值：temporal、valid_until、source_quote 给了不是字符串就拒；
// 别的字段不是字符串当空串；多出来的字段不看；内部错误换成固定的话
func mwToolRemember(m *Memory, a Audience, args map[string]any, seqs []int64) ToolResult {
	for _, key := range []string{"temporal", "valid_until", "source_quote"} {
		if v, exists := args[key]; exists {
			if _, isStr := v.(string); !isStr {
				return ToolResult{Text: "记忆参数类型不正确，这次没有记。", IsError: true}
			}
		}
	}
	arg := func(key string) string { s, _ := args[key].(string); return s }
	r, err := m.Remember(context.Background(), a, RememberArgs{InputSeqs: seqs, Kind: arg("kind"), Text: arg("text"), Subject: arg("subject"), Replaces: arg("replaces"),
		Temporal: arg("temporal"), ValidUntil: arg("valid_until"), SourceQuote: arg("source_quote")})
	if err != nil {
		return ToolResult{Text: "记忆这边出了点问题，这次没记上，过一会儿再说一次。", IsError: true}
	}
	return r
}

func mwToolForget(m *Memory, a Audience, args map[string]any, seqs []int64) ToolResult {
	for _, key := range []string{"id", "reason", "source_quote"} {
		if v, exists := args[key]; exists {
			if _, isStr := v.(string); !isStr {
				return ToolResult{Text: "记忆参数类型不正确，这次没有忘。", IsError: true}
			}
		}
	}
	arg := func(key string) string { s, _ := args[key].(string); return s }
	r, err := m.Forget(context.Background(), a, ForgetArgs{InputSeqs: seqs, ID: arg("id"), Reason: arg("reason"), SourceQuote: arg("source_quote")})
	if err != nil {
		return ToolResult{Text: "记忆这边出了点问题，这次没忘掉，过一会儿再说一次。", IsError: true}
	}
	return r
}

// ---------- 起始数据和结束时的表 ----------

func mwSeed(t *testing.T, file string, s mwStart, now time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	at := func(ago float64) int64 { return now.Add(-mwDur(ago)).UnixMilli() }
	for _, f := range s.Facts {
		if f.Kind == "" {
			f.Kind = "fact"
		}
		if f.Status == "" {
			f.Status = "active"
		}
		if len(f.Scopes) == 0 {
			f.Scopes = []string{"owner"}
		}
		var confirmed, validTo, eventAt any
		if f.ConfirmedBy != nil {
			confirmed = *f.ConfirmedBy
		}
		if f.ValidToInS != nil {
			validTo = now.Add(mwDur(*f.ValidToInS)).UnixMilli()
		}
		if f.EventAgoS != nil {
			eventAt = at(*f.EventAgoS)
		}
		exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, superseded_by, created_at, learned_at, origin, conversation, scope, temporal, valid_to, confirmed_by, event_at)
			VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.Kind, f.Text, nullIfEmpty(f.Subject), legacyVisibility(f.Scopes), f.Status, nullIfEmpty(f.SupersededBy), at(f.CreatedAgoS), at(f.CreatedAgoS),
			nullIfEmpty(f.Origin), nullIfEmpty(f.Conversation), f.Scopes[0], nullIfEmpty(f.Temporal), validTo, confirmed, eventAt)
		for _, sc := range f.Scopes {
			exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "f:"+f.ID, sc)
		}
	}
	for _, e := range s.Episodes {
		if len(e.Scopes) == 0 {
			e.Scopes = []string{"owner"}
		}
		exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope, learned_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Conversation, e.Text, legacyVisibility(e.Scopes), e.FromSeq, e.ToSeq, now.UnixMilli(), e.Scopes[0], now.UnixMilli())
		for _, sc := range e.Scopes {
			exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "e:"+e.ID, sc)
		}
	}
	for _, r := range s.Revisions {
		var after any
		if r.After != nil {
			raw, _ := json.Marshal(r.After)
			after = string(raw)
		}
		exec(`INSERT INTO fact_revisions (fact_id, op, at, by, after_json) VALUES (?, ?, ?, ?, ?)`, r.FactID, r.Op, now.UnixMilli(), r.By, after)
	}
	for _, e := range s.Evidence {
		by := 0
		if e.BySubject {
			by = 1
		}
		exec(`INSERT INTO fact_evidence (fact_id, day, conversation, speaker, by_subject, at) VALUES (?, ?, ?, ?, ?, ?)`,
			e.FactID, now.AddDate(0, 0, -e.DaysAgo).Format("2006-01-02"), e.Conversation, nullIfEmpty(e.Speaker), by, now.UnixMilli())
	}
}

// mwRows 查一张表，每行一个 map：[]byte 变字符串，NULL 是 nil；时间列（timeCols 里的）换成“距 now 多少毫秒前”
func mwRows(t *testing.T, db *sql.DB, now time.Time, query string, timeCols map[string]string) []map[string]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s：%v", query, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if name, isTime := timeCols[c]; isTime {
				if n, ok := v.(int64); ok {
					v = float64(now.UnixMilli()-n) / 1000
				}
				c = name
			}
			row[c] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func mwTables(t *testing.T, file string, now time.Time) mwFinal {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scopes := func(rows []map[string]any, ref string) {
		for _, r := range rows {
			list := []string{}
			rows, err := db.Query(`SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope`, ref+r["id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					t.Fatal(err)
				}
				list = append(list, s)
			}
			rows.Close()
			r["scopes"] = list
		}
	}
	var f mwFinal
	f.Facts = mwRows(t, db, now, `SELECT id, kind, text, subject, visibility, source_seqs, status, superseded_by, created_at, origin, conversation, audience_at_origin,
		speech_act, attributed_to, scope, no_mention, event_at, learned_at, valid_from, valid_to, temporal, confirmed_by, only_her, learned_by, whisper_json FROM facts ORDER BY id`,
		map[string]string{"created_at": "created_ago_s", "event_at": "event_ago_s", "learned_at": "learned_ago_s", "valid_from": "valid_from_ago_s", "valid_to": "valid_to_ago_s"})
	scopes(f.Facts, "f:")
	f.Episodes = mwRows(t, db, now, `SELECT id, conversation, text, visibility, from_seq, to_seq, created_at, scope, origin, audience_at_origin, event_at, learned_at, status FROM episodes ORDER BY id`,
		map[string]string{"created_at": "created_ago_s", "event_at": "event_ago_s", "learned_at": "learned_ago_s"})
	scopes(f.Episodes, "e:")
	f.Revisions = mwRows(t, db, now, `SELECT fact_id, op, at, by, reason, before_json, after_json FROM fact_revisions ORDER BY id`, map[string]string{"at": "at_ago_s"})
	for _, r := range f.Revisions {
		for _, k := range []string{"before_json", "after_json"} {
			name := strings.TrimSuffix(k, "_json")
			if s, ok := r[k].(string); ok {
				var obj map[string]any
				if err := json.Unmarshal([]byte(s), &obj); err != nil {
					t.Fatalf("修订里的 %s 不是 JSON：%v", k, err)
				}
				for key, tk := range map[string]string{"valid_to": "valid_to_ago_s", "event_at": "event_ago_s"} {
					if n, ok := obj[key].(float64); ok && n != 0 { // 0 是“没有”（旧事实没写 event_at），不换算
						delete(obj, key)
						obj[tk] = float64(now.UnixMilli()-int64(n)) / 1000
					}
				}
				r[name] = obj
			} else {
				r[name] = nil
			}
			delete(r, k)
		}
	}
	f.Evidence = mwRows(t, db, now, `SELECT fact_id, day, conversation, seq, speaker, by_subject, verdict, at FROM fact_evidence ORDER BY fact_id, day, conversation`, map[string]string{"at": "at_ago_s"})
	today := now.Format("2006-01-02")
	for _, r := range f.Evidence {
		d, err := time.Parse("2006-01-02", r["day"].(string))
		if err != nil {
			t.Fatal(err)
		}
		t0, _ := time.Parse("2006-01-02", today)
		r["days_ago"] = int(t0.Sub(d).Hours() / 24)
		delete(r, "day")
	}
	return f
}
