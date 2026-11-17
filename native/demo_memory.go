package main

// 记忆页的演示数据（-demo）：一个假的 api.MemoryReader，全是编的。界面测试和放大十倍的量数也用它
// （按角色数、每个角色多少条造），所以过滤、分页、搜索都在内存里照 api 的约定做一遍。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"sumika-native/internal/api"
)

type demoMemory struct {
	mu     sync.Mutex
	chars  []api.MemoryCharacter
	rows   map[string][]api.MemoryRow // 角色 → 全部条目（新的在前）
	rev    map[string]uint64
	subs   []chan api.Snapshot[api.MemoryOverview]
	nsnap  uint64
	failOn string // 非空时，列表读取失败并带这个原因（测出错态）
}

var demoKinds = []string{"fact", "promise", "preference", "boundary"}
var demoGroups = []string{"owner", "ai", "imported", "candidate", "revoked", "superseded", "other"}
var demoScopes = []api.MemoryScope{
	{Kind: "public", Label: "公开来源"}, {Kind: "owner", Label: "主人场合的来源"},
	{Kind: "conv", Label: "来自 QQ 群 ***01"}, {Kind: "person", Label: "和 小乙（***02） 的私聊来源"},
}

func newDemoMemory() *demoMemory { return newDemoMemoryN([]string{"suzune", "reika", "nodb"}, 80) }

// newDemoMemoryN：ids 里每个角色 perChar 条事实（含各种状态）和 perChar/3 个片段；叫 nodb 的角色没有记忆库
func newDemoMemoryN(ids []string, perChar int) *demoMemory {
	d := &demoMemory{rows: map[string][]api.MemoryRow{}, rev: map[string]uint64{}}
	names := map[string]string{"suzune": "铃音", "reika": "铃花", "nodb": "没有库的角色"}
	for ci, id := range ids {
		mc := api.MemoryCharacter{ID: id, Name: names[id], HasDB: id != "nodb", Migrated: true, ConfirmNeed: 3}
		if mc.Name == "" {
			mc.Name = "编造角色" + strconv.Itoa(ci)
		}
		if mc.HasDB {
			d.rows[id] = demoRows(perChar)
			mc.Total = len(d.rows[id])
		}
		d.chars = append(d.chars, mc)
		d.recount(id)
	}
	return d
}

func demoRows(n int) []api.MemoryRow {
	out := make([]api.MemoryRow, 0, n+n/3)
	base := int64(1_790_000_000_000)
	for i := 0; i < n; i++ {
		g := demoGroups[i%len(demoGroups)]
		r := api.MemoryRow{
			Ref: fmt.Sprintf("f:d%05d", i), Kind: demoKinds[i%len(demoKinds)], Group: g, Status: "active",
			Text:         fmt.Sprintf("编造的记忆第 %d 条：喜欢草莓蛋糕，周末想去海边，下周要考试。", i),
			Subject:      api.MemoryWho{Kind: []string{"owner", "person", "self", "other"}[i%4], Name: "小乙", ID: "***0" + strconv.Itoa(i%10), Raw: "别的写法"},
			Conversation: "QQ 群 ***0" + strconv.Itoa(i%3), Scopes: []api.MemoryScope{demoScopes[i%len(demoScopes)]},
			CreatedAt: base - int64(i)*60_000, LearnedAt: base - int64(i)*60_000,
		}
		switch g {
		case "candidate":
			r.Status = "candidate"
		case "revoked":
			r.Status = "revoked"
		case "superseded":
			r.Status = "superseded"
		case "owner":
			r.ConfirmedBy = "owner"
		case "ai":
			r.ConfirmedBy = "ai"
		}
		if i%53 == 0 {
			r.CreatedAt = 0 // 时间不详
		}
		out = append(out, r)
	}
	for i := 0; i < n/3; i++ {
		g := []string{"active", "revoked"}[i%2]
		st := map[string]string{"active": "active", "revoked": "revoked"}[g]
		out = append(out, api.MemoryRow{
			Ref: fmt.Sprintf("e:d%05d", i), Kind: "episode", Group: g, Status: st, Text: fmt.Sprintf("编造的片段第 %d 段：群里聊了周末去哪玩。", i),
			Conversation: "QQ 群 ***01", Scopes: []api.MemoryScope{demoScopes[2]}, CreatedAt: base - int64(i)*90_000, FromSeq: int64(10 + i*30), ToSeq: int64(40 + i*30),
		})
	}
	return out
}

// recount 重算一个角色的计数和下拉
func (d *demoMemory) recount(id string) {
	for i := range d.chars {
		if d.chars[i].ID != id {
			continue
		}
		counts := map[[2]string]int{}
		for _, r := range d.rows[id] {
			counts[[2]string{r.Kind, r.Group}]++
		}
		d.chars[i].Counts = nil
		for k, n := range counts {
			d.chars[i].Counts = append(d.chars[i].Counts, api.MemoryCount{Kind: k[0], Group: k[1], N: n})
		}
		d.chars[i].Total = len(d.rows[id])
		d.chars[i].Rev = d.rev[id]
		d.chars[i].Conversations = []api.MemoryChoice{{Value: "qq:group:30001", Label: "编的测试群（QQ 群 ***01）", N: 40}, {Value: "qq:group:30002", Label: "QQ 群 ***02", N: 20}}
		d.chars[i].Subjects = []api.MemoryChoice{{Value: "owner", Label: "你", N: 30}, {Value: "qq:10002", Label: "小乙（***02）", N: 25}}
	}
}

func (d *demoMemory) snapshot() api.MemoryOverview {
	return api.MemoryOverview{Characters: append([]api.MemoryCharacter(nil), d.chars...)}
}

// Bump 演示"库里有变化"：给角色加一条新的，Rev 加一，推一份新快照
func (d *demoMemory) Bump(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rev[id]++
	n := len(d.rows[id])
	row := api.MemoryRow{Ref: fmt.Sprintf("f:n%05d", n), Kind: "fact", Group: "candidate", Status: "candidate", Text: "刚刚新写的一条（演示）", Scopes: []api.MemoryScope{demoScopes[1]}, CreatedAt: 1_800_000_000_000}
	d.rows[id] = append([]api.MemoryRow{row}, d.rows[id]...)
	d.recount(id)
	d.nsnap++
	for _, ch := range d.subs {
		select {
		case ch <- api.Snapshot[api.MemoryOverview]{Version: d.nsnap, Result: api.Result[api.MemoryOverview]{State: api.Ready, Value: d.snapshot()}}:
		default:
		}
	}
}

type demoMemSub struct {
	ch   chan api.Snapshot[api.MemoryOverview]
	d    *demoMemory
	once sync.Once
}

func (s *demoMemSub) Updates() <-chan api.Snapshot[api.MemoryOverview] { return s.ch }

// Close 先摘掉订阅、再关通道（都在锁里），Bump 不会往关掉的通道里发
func (s *demoMemSub) Close() {
	s.once.Do(func() {
		s.d.mu.Lock()
		for i, c := range s.d.subs {
			if c == s.ch {
				s.d.subs = append(s.d.subs[:i], s.d.subs[i+1:]...)
				break
			}
		}
		close(s.ch)
		s.d.mu.Unlock()
	})
}

func (d *demoMemory) WatchMemory(ctx context.Context) (api.Subscription[api.MemoryOverview], error) {
	d.mu.Lock()
	ch := make(chan api.Snapshot[api.MemoryOverview], 8)
	d.nsnap++
	ch <- api.Snapshot[api.MemoryOverview]{Version: d.nsnap, Result: api.Result[api.MemoryOverview]{State: api.Ready, Value: d.snapshot()}}
	d.subs = append(d.subs, ch)
	d.mu.Unlock()
	sub := &demoMemSub{ch: ch, d: d}
	go func() { <-ctx.Done(); sub.Close() }()
	return sub, nil
}

func (d *demoMemory) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failOn != "" {
		err := fmt.Errorf("%s", d.failOn)
		return api.Result[api.MemoryPage]{State: api.Failed, Err: err}, err
	}
	rows, ok := d.rows[req.Character]
	if !ok {
		err := fmt.Errorf("这个角色还没有记忆库")
		return api.Result[api.MemoryPage]{State: api.Failed, Err: err}, err
	}
	var hit []api.MemoryRow
	for _, r := range rows {
		isEp := r.Kind == "episode"
		if (req.Kind == "episode") != isEp || (req.Kind != "" && req.Kind != "episode" && r.Kind != req.Kind) {
			continue
		}
		if len(req.Groups) > 0 && !contains(req.Groups, r.Group) {
			continue
		}
		if req.ScopeKind != "" && r.Scopes[0].Kind != req.ScopeKind {
			continue
		}
		if req.Query != "" && !strings.Contains(r.Text, req.Query) {
			continue
		}
		hit = append(hit, r)
	}
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	off := 0
	if req.Cursor != "" {
		n, err := strconv.Atoi(string(req.Cursor))
		if err != nil || n < 0 {
			return api.Result[api.MemoryPage]{State: api.Failed, Err: fmt.Errorf("游标不对")}, fmt.Errorf("游标不对")
		}
		off = n
	}
	var out api.MemoryPage
	if req.Query != "" {
		limit = 200
		if len(hit) > limit {
			out.Truncated = true
		}
	}
	end := min(off+limit, len(hit))
	if off < len(hit) {
		out.Items = hit[off:end]
	}
	if req.Query == "" && end < len(hit) {
		out.NextCursor, out.HasMore = api.Cursor(strconv.Itoa(end)), true
	}
	return api.Result[api.MemoryPage]{State: api.Ready, Value: out}, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (d *demoMemory) find(character, ref string) (api.MemoryRow, bool) {
	for _, r := range d.rows[character] {
		if r.Ref == ref {
			return r, true
		}
	}
	return api.MemoryRow{}, false
}

func (d *demoMemory) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.find(character, ref)
	if !ok {
		err := fmt.Errorf("库里没有这一条")
		return api.Result[api.MemoryDetail]{State: api.Failed, Err: err}, err
	}
	det := api.MemoryDetail{Row: r, Origin: "整理", SpeechAct: "assert", Migrated: true, Recall: api.MemoryRecall{Can: r.Group != "revoked" && r.Group != "superseded", Reason: "ok"},
		Places: []api.MemoryPlace{{Place: "local-private", Visible: "yes"}, {Place: "local-group", Visible: "yes"}, {Place: "platform-private", Visible: "yes"}, {Place: "platform-group", Visible: "yes"}}}
	switch r.Group {
	case "revoked":
		det.Recall.Reason = "revoked"
	case "superseded":
		det.Recall.Reason = "superseded"
		det.SupersededBy = "f:d00000"
	case "candidate":
		det.Occasions = &api.MemoryOccasions{Have: 1, Need: 3, Self: 1, Rows: 2, Auto: true}
	}
	if strings.HasPrefix(ref, "f:") {
		n, _ := strconv.Atoi(strings.TrimPrefix(ref, "f:d"))
		cnt := []int{0, 1, 3, 5, 12}[n%5]
		det.SourcesTotal = cnt
		for i := 0; i < cnt; i++ {
			det.Sources = append(det.Sources, api.MemorySource{Conversation: "qq:group:30001", Seq: int64(100 + i), Label: "QQ 群 ***01"})
		}
	}
	return api.Result[api.MemoryDetail]{State: api.Ready, Value: det}, nil
}

func (d *demoMemory) ReadMemoryEvidence(ctx context.Context, character, ref string, cursor api.Cursor) (api.Result[api.MemoryEvidencePage], error) {
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(string(cursor))
	}
	const total = 25
	var out api.MemoryEvidencePage
	out.Total = total
	for i := start; i < min(start+20, total); i++ {
		out.Items = append(out.Items, api.MemoryEvidenceRow{Day: fmt.Sprintf("2026-10-%02d", 28-i%28), Speaker: api.MemoryWho{Kind: "person", Name: "小乙", ID: "***02"}, BySubject: i%3 == 0, Verdict: "ok",
			Conversation: api.MemorySource{Conversation: "qq:group:30001", Seq: int64(200 + i), Label: "QQ 群 ***01"}})
	}
	if start+20 < total {
		out.NextCursor, out.HasMore = api.Cursor(strconv.Itoa(start+20)), true
	}
	return api.Result[api.MemoryEvidencePage]{State: api.Ready, Value: out}, nil
}

func (d *demoMemory) ReadMemoryRevisions(ctx context.Context, character, ref string, cursor api.Cursor) (api.Result[api.MemoryRevisionPage], error) {
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(string(cursor))
	}
	const total = 25
	var out api.MemoryRevisionPage
	for i := start; i < min(start+20, total); i++ {
		out.Items = append(out.Items, api.MemoryRevision{ID: int64(total - i), At: 1_790_000_000_000 - int64(i)*3_600_000, Who: "整理", Did: "放行", Change: "待确认 → 有效，放行者：她自己", Reason: "攒够 3 次不同场合"})
	}
	if start+20 < total {
		out.NextCursor, out.HasMore = api.Cursor(strconv.Itoa(start+20)), true
	}
	return api.Result[api.MemoryRevisionPage]{State: api.Ready, Value: out}, nil
}

func (d *demoMemory) ReadMemoryRecent(ctx context.Context, character string) (api.Result[[]api.MemoryRecentChange], error) {
	var out []api.MemoryRecentChange
	for i := 0; i < 50; i++ {
		out = append(out, api.MemoryRecentChange{MemoryRevision: api.MemoryRevision{ID: int64(100 - i), At: 1_790_000_000_000 - int64(i)*600_000, Who: "整理", Did: "撤销", Reason: ""}, Ref: fmt.Sprintf("f:d%05d", i), Text: "编造的最近变动", Exists: true})
	}
	return api.Result[[]api.MemoryRecentChange]{State: api.Ready, Value: out}, nil
}

func (d *demoMemory) ReadMemoryQuote(ctx context.Context, character string, src api.MemorySource) (api.Result[api.MemoryQuote], error) {
	var q api.MemoryQuote
	switch {
	case src.Seq%5 == 0:
		q = api.MemoryQuote{Status: "recalled"}
	case src.Seq%7 == 0:
		q = api.MemoryQuote{Status: "missing"}
	default:
		q = api.MemoryQuote{Status: "ok", Who: api.MemoryWho{Kind: "person", Name: "小乙", ID: "***02"}, At: 1_790_000_000_000, Text: fmt.Sprintf("编造的原话 #%d：我下周搬家。", src.Seq)}
	}
	return api.Result[api.MemoryQuote]{State: api.Ready, Value: q}, nil
}

var _ api.MemoryReader = (*demoMemory)(nil)
