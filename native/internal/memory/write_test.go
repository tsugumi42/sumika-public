package memory

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 范围的写法：某个会话里能想起是 conv:<会话>
const scopeA, scopeB = "conv:" + GA, "conv:" + GB

func TestApplyWritesFactsAndEpisode(t *testing.T) {
	t.Parallel()
	intended(t, "candidates-enter-index")
	m, _ := newWritable(t, nil)
	res := mustApply(t, m, summary(
		FactOp{Kind: "preference", Text: "主人喜欢养猫", Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"owner"},
			SourceSeqs: []int64{3, 4}, Origin: "owner", Conversation: "console:a", EventAt: 1000, Temporal: "stable"},
		candidate("阿福下周要搬家", "person:qq:10001", scopeA, "person:qq:10001"),
		EpisodeOp{Conversation: "console:a", Text: "聊了养猫的事", FromSeq: 1, ToSeq: 5, Scopes: []string{"owner"}, Origin: "owner", EventAt: 1000},
	))
	if len(res.Outcomes) != 3 || len(res.Added) != 3 {
		t.Fatalf("结局 %d 个、新写 %d 条，要都是 3：%+v", len(res.Outcomes), len(res.Added), res)
	}
	for i, o := range res.Outcomes {
		if o.Result != ResultAdded {
			t.Fatalf("第 %d 个动作应该是新写的：%+v", i+1, o)
		}
	}
	if res.Counts[CountAddedActive] != 1 || res.Counts[CountAddedCandidate] != 1 || res.Counts[CountAddedEpisode] != 1 {
		t.Fatalf("计数不对：%v", res.Counts)
	}

	fact, cand, ep := idOf(res.Added[0]), idOf(res.Added[1]), idOf(res.Added[2])
	if got := str(t, m, `SELECT scope || '|' || coalesce(confirmed_by, '') || '|' || visibility || '|' || source_seqs || '|' || speech_act || '|' || temporal || '|' || origin || '|' || event_at FROM facts WHERE id = ?`, fact); got != "owner|ai|private|[3,4]|assert|stable|owner|1000" {
		t.Fatalf("有效事实的列：%q", got)
	}
	// 待确认：没有 speech_act、没有 confirmed_by；facts.scope 放第一个值，范围组两个都在；旧的 visibility 列按第一个范围反推
	if got := str(t, m, `SELECT scope || '|' || coalesce(confirmed_by, '-') || '|' || visibility || '|' || coalesce(speech_act, '-') FROM facts WHERE id = ?`, cand); got != scopeA+"|-|public|-" {
		t.Fatalf("待确认的列：%q", got)
	}
	if n := count(t, m, `SELECT count(*) FROM item_scopes WHERE ref = ?`, "f:"+cand); n != 2 {
		t.Fatalf("待确认的范围组应该两行：%d", n)
	}
	if got := str(t, m, `SELECT scope || '|' || status || '|' || from_seq || '-' || to_seq FROM episodes WHERE id = ?`, ep); got != "owner|active|1-5" {
		t.Fatalf("片段的列：%q", got)
	}
	// 每条新事实一行 add 修订，记谁写的
	if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE op = 'add' AND by = 'summary'`); n != 2 {
		t.Fatalf("add 修订应该 2 行：%d", n)
	}
	after := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ?`, cand))
	if after["status"] != "candidate" {
		t.Fatalf("修订的 after：%v", after)
	}

	// 内存里的条目写完就刷新了；范围组整个读出来，第一个值排在最前
	items := map[string]Item{}
	for _, it := range m.Items() {
		items[it.Ref] = it
	}
	if len(items) != 3 || items["f:"+cand].Scopes[0] != scopeA || len(items["f:"+cand].Scopes) != 2 || items["f:"+cand].Status != "candidate" {
		t.Fatalf("刷新后的条目不对：%+v", items)
	}

	// 待确认进全文索引并跨场合召回，仍标听说；范围只保留原始归属。
	ctx := context.Background()
	hits := m.Recall(ctx, inGroup(GA), Query{Text: "阿福搬家", Mode: ModeExplicit}).Hits
	if len(hits) != 1 || hits[0].Ref != "f:"+cand || !hits[0].Hearsay || hits[0].Lexical == 0 {
		t.Fatalf("群里应该想起这条待确认的（听说）：%+v", hits)
	}
	if hits := m.Recall(ctx, inGroup(GB), Query{Text: "阿福搬家", Mode: ModeExplicit}).Hits; len(hits) != 1 || !hits[0].Hearsay {
		t.Fatalf("跨群候选缺失或未经确认：%+v", hits)
	}
	if hits := m.Recall(ctx, inGroup(GA), Query{Text: "主人养猫", Mode: ModeExplicit}).Hits; !hasRef(hits, "f:"+fact) {
		t.Fatalf("群里缺少同角色的主人事实：%+v", hits)
	}
	if hits := m.Recall(ctx, ownerHere, Query{Text: "主人养猫", Mode: ModeExplicit}).Hits; len(hits) == 0 || hits[0].Ref != "f:"+fact {
		t.Fatalf("主人的本地私聊应该想起：%+v", hits)
	}
}

// B1：中途出错整批回滚，什么都没留下；修好以后重跑只记一遍
func TestApplyRollsBackTheWholeBatch(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	real := m.w.newID
	m.w.newID = func() string { return "SAMEID" } // 第二条撞上主键
	_, err := m.apply(context.Background(), Batch{By: BySummary, Ops: []Op{ownerFact("甲"), ownerFact("乙")},
		Job: &Job{Conversation: "console:a", From: 1, To: 9, Version: 1}})
	if err == nil {
		t.Fatal("撞了主键应该报错")
	}
	for _, q := range []string{`facts`, `item_scopes`, `fact_revisions`, `memory_jobs`, `summary_state`, `fact_evidence`} {
		if n := count(t, m, `SELECT count(*) FROM `+q); n != 0 {
			t.Errorf("回滚以后 %s 里还有 %d 行", q, n)
		}
	}
	if len(m.Items()) != 0 {
		t.Fatalf("回滚以后内存里的条目也不该变：%+v", m.Items())
	}
	m.w.newID = real
	res := mustApply(t, m, Batch{By: BySummary, Ops: []Op{ownerFact("甲"), ownerFact("乙")}, Job: &Job{Conversation: "console:a", From: 1, To: 9, Version: 1}})
	if res.Duplicate || count(t, m, `SELECT count(*) FROM facts`) != 2 || count(t, m, `SELECT count(*) FROM memory_jobs`) != 1 {
		t.Fatalf("修好以后重跑应该正常写一遍：%+v", res)
	}
}

func TestApplyRejectsMalformedBatches(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	good := ownerFact("好的")
	bad := func(mod func(*FactOp)) Op { f := good; mod(&f); return f }
	prev := int64(9)
	cases := []struct {
		name string
		b    Batch
		want string
	}{
		{"没写谁在写", Batch{Ops: []Op{good}}, "By"},
		{"kind 不认", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Kind = "memo" })}}, "kind"},
		{"status 不认", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Status = "rejected" })}}, "status"},
		{"confirmed_by 不认", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.ConfirmedBy = "bot" })}}, "confirmed_by"},
		{"正文是空白", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Text = " \n\t" })}}, "正文"},
		{"没有范围", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Scopes = nil })}}, "范围"},
		{"范围里有空的", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Scopes = []string{"owner", " "} })}}, "范围"},
		{"证据没写会话", Batch{By: BySummary, Ops: []Op{bad(func(f *FactOp) { f.Evidence = &Evidence{} })}}, "证据"},
		{"撤销没写编号", Batch{By: BySummary, Ops: []Op{RevokeOp{}}}, "撤销"},
		{"片段没写会话", Batch{By: BySummary, Ops: []Op{EpisodeOp{Text: "x", Scopes: []string{"owner"}}}}, "片段"},
		{"不认识的动作", Batch{By: BySummary, Ops: []Op{nil}}, "不认识"},
		{"Job 没写会话", Batch{By: BySummary, Job: &Job{From: 1, To: 2}}, "会话"},
		{"Job 起大于止", Batch{By: BySummary, Job: &Job{Conversation: "c", From: 5, To: 2}}, "起"},
		{"Job 的游标不在止之前", Batch{By: BySummary, Job: &Job{Conversation: "c", From: 1, To: 9, Prev: &prev}}, "游标"},
	}
	for _, c := range cases {
		if _, err := m.apply(context.Background(), c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：应该报错并提到 %q，得到 %v", c.name, c.want, err)
		}
	}
	for _, q := range []string{`facts`, `episodes`, `fact_revisions`, `memory_jobs`, `summary_state`} {
		if n := count(t, m, `SELECT count(*) FROM `+q); n != 0 {
			t.Errorf("不合规的批不该写进任何东西，%s 里有 %d 行", q, n)
		}
	}
}

// B2：同一批（会话、起止 seq、版本）整理过，只挪游标，不写记忆
func TestJobIdempotentB2(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	if created, err := m.InitCursor(ctx, "console:a", 10); err != nil || !created {
		t.Fatalf("新会话立游标：%v %v", created, err)
	}
	prev := int64(10)
	job := func(p *int64) *Job {
		return &Job{Conversation: "console:a", From: 11, To: 50, Version: 1, Prev: p, Counts: map[string]int{"read": 40}}
	}
	if done, _ := m.JobDone(ctx, "console:a", 11, 50, 1); done {
		t.Fatal("还没整理过")
	}
	r1 := mustApply(t, m, Batch{By: BySummary, Ops: []Op{ownerFact("甲")}, Job: job(&prev)})
	if r1.Duplicate || count(t, m, `SELECT count(*) FROM facts`) != 1 {
		t.Fatalf("第一次该写：%+v", r1)
	}
	st, ok, _ := m.SummaryState(ctx, "console:a")
	if !ok || st.LastSeq != 50 || st.Failures != 0 {
		t.Fatalf("游标应该到 50：%+v", st)
	}
	// 计数：调用方数的和写入数的存在一起
	counts := jsonMap(t, str(t, m, `SELECT counts_json FROM memory_jobs WHERE conversation = 'console:a'`))
	if counts["read"] != float64(40) || counts[CountAddedActive] != float64(1) {
		t.Fatalf("counts_json：%v", counts)
	}
	for _, c := range []struct {
		from, to int64
		v        int
		want     bool
	}{{11, 50, 1, true}, {12, 50, 1, false}, {11, 49, 1, false}, {11, 50, 2, false}} {
		if done, _ := m.JobDone(ctx, "console:a", c.from, c.to, c.v); done != c.want {
			t.Errorf("JobDone(%d,%d,v%d) = %v，要 %v", c.from, c.to, c.v, done, c.want)
		}
	}

	// 删掉游标再跑同一批：不写记忆，游标回到 50
	if _, err := m.w.db.Exec(`DELETE FROM summary_state`); err != nil {
		t.Fatal(err)
	}
	r2 := mustApply(t, m, Batch{By: BySummary, Ops: []Op{ownerFact("乙（不该写进去）")}, Job: job(nil)})
	if !r2.Duplicate || len(r2.Outcomes) != 0 || r2.Counts[CountJobDuplicate] != 1 {
		t.Fatalf("同一批再来应该是 Duplicate：%+v", r2)
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 1 {
		t.Fatalf("同一批再来不该多写：%d 条", n)
	}
	if st, _, _ := m.SummaryState(ctx, "console:a"); st.LastSeq != 50 {
		t.Fatalf("游标应该补回 50：%+v", st)
	}
	// 游标已经在更后面：只往前，不往回拉
	m.w.db.Exec(`UPDATE summary_state SET last_seq = 80`)
	r3 := mustApply(t, m, Batch{By: BySummary, Job: job(nil)})
	if st, _, _ := m.SummaryState(ctx, "console:a"); !r3.Duplicate || st.LastSeq != 80 {
		t.Fatalf("游标不该被拉回 50：%+v", st)
	}
	if n := count(t, m, `SELECT count(*) FROM memory_jobs`); n != 1 {
		t.Fatalf("memory_jobs 应该还是 1 行：%d", n)
	}
}

// 游标 compare-and-set：旧的批次迟到、两个批次重叠，都不会把游标往回拉或各写各的
func TestCursorCompareAndSet(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	at := func(n int64) *int64 { return &n }
	job := func(prev *int64, from, to int64) Batch {
		return Batch{By: BySummary, Ops: []Op{ownerFact("第 " + string(rune('0'+to%10)) + " 号")}, Job: &Job{Conversation: "console:a", From: from, To: to, Version: 1, Prev: prev}}
	}
	cursor := func() int64 { st, _, _ := m.SummaryState(ctx, "console:a"); return st.LastSeq }

	mustApply(t, m, job(nil, 1, 5)) // 没有这一行，Prev 是 nil：可以
	if cursor() != 5 {
		t.Fatalf("游标应该是 5：%d", cursor())
	}
	factsBefore := count(t, m, `SELECT count(*) FROM facts`)
	for name, b := range map[string]Batch{
		"行已经有了，Prev 却是 nil": job(nil, 6, 8),
		"Prev 过时了":          job(at(3), 6, 8),
	} {
		if _, err := m.apply(ctx, b); !errors.Is(err, ErrCursorMoved) {
			t.Errorf("%s：应该是 ErrCursorMoved：%v", name, err)
		}
	}
	if _, err := m.apply(ctx, job(at(5), 100, 120)); err != nil { // 正常的
		t.Fatal(err)
	}
	if _, err := m.apply(ctx, job(at(5), 6, 12)); !errors.Is(err, ErrCursorMoved) { // 旧批次迟到
		t.Fatalf("旧批次迟到应该被拒：%v", err)
	}
	if cursor() != 120 {
		t.Fatalf("游标不该被往回拉：%d", cursor())
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != factsBefore+1 || count(t, m, `SELECT count(*) FROM memory_jobs`) != 2 {
		t.Fatalf("被拒的批次什么都不该写：事实 %d 条（要 %d），整理记录 %d 行", n, factsBefore+1, count(t, m, `SELECT count(*) FROM memory_jobs`))
	}
	// 库里没有这一行，Prev 却有值（行被删了）：也不让写
	m.w.db.Exec(`DELETE FROM summary_state`)
	if _, err := m.apply(ctx, job(at(120), 121, 130)); !errors.Is(err, ErrCursorMoved) {
		t.Fatalf("行没了 Prev 还有值：%v", err)
	}
}

func TestSummaryStateAndInitCursor(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	if _, ok, err := m.SummaryState(ctx, "console:a"); ok || err != nil {
		t.Fatalf("新会话没有游标：%v %v", ok, err)
	}
	if c, _ := m.InitCursor(ctx, "console:a", 7); !c {
		t.Fatal("第一次应该建出来")
	}
	if c, _ := m.InitCursor(ctx, "console:a", 99); c {
		t.Fatal("已经有了不该再建")
	}
	st, ok, _ := m.SummaryState(ctx, "console:a")
	if !ok || st.LastSeq != 7 || st.Failures != 0 || st.RetryAt != 0 {
		t.Fatalf("已经有的游标不该被挪：%+v", st)
	}
	// 成功一批会把退避清零
	m.w.db.Exec(`UPDATE summary_state SET failures = 3, retry_at = 12345`)
	if st, _, _ := m.SummaryState(ctx, "console:a"); st.Failures != 3 || st.RetryAt != 12345 {
		t.Fatalf("退避读不出来：%+v", st)
	}
	prev := int64(7)
	mustApply(t, m, Batch{By: BySummary, Job: &Job{Conversation: "console:a", From: 8, To: 9, Version: 1, Prev: &prev}})
	if st, _, _ := m.SummaryState(ctx, "console:a"); st.LastSeq != 9 || st.Failures != 0 || st.RetryAt != 0 {
		t.Fatalf("成功以后退避该清零：%+v", st)
	}
}

// 世代号：忘掉、真删期间算出来的结果落不了库（清单 ④）
func TestGenerationDropsStaleBatch(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	g0, _ := m.Generation(ctx)
	if g0 != 0 {
		t.Fatalf("起点：%d", g0)
	}
	mustApply(t, m, Batch{By: BySummary, Ops: []Op{ownerFact("没忘掉时写的")}, Generation: &g0})

	// 整理读了世代号去调模型，调用期间主人把记忆忘掉了
	if g, err := m.BumpGeneration(ctx); err != nil || g != 1 {
		t.Fatalf("加一：%d %v", g, err)
	}
	_, err := m.apply(ctx, Batch{By: BySummary, Ops: []Op{ownerFact("忘掉以后才回来的结果")}, Generation: &g0,
		Job: &Job{Conversation: "console:a", From: 1, To: 4, Version: 1}})
	if !errors.Is(err, ErrGenerationChanged) {
		t.Fatalf("应该是 ErrGenerationChanged：%v", err)
	}
	if n := count(t, m, `SELECT count(*) FROM facts WHERE text LIKE '忘掉以后%'`); n != 0 {
		t.Fatal("作废的结果被写进库了")
	}
	if n := count(t, m, `SELECT count(*) FROM memory_jobs`) + count(t, m, `SELECT count(*) FROM summary_state`); n != 0 {
		t.Fatalf("作废的结果连整理记录、游标都不该留：%d", n)
	}
	// 重新读了世代号的批次照常
	g1, _ := m.Generation(ctx)
	mustApply(t, m, Batch{By: BySummary, Ops: []Op{ownerFact("新世代的结果")}, Generation: &g1})
	if g, _ := m.BumpGeneration(ctx); g != 2 {
		t.Fatalf("再加一：%d", g)
	}
}

// ───── 同文查重 ─────

// 看不见的字符用码点造，别直接写进源码（编辑器和工具会悄悄吞掉）
var (
	zwsp   = string(rune(0x200B))
	zwj    = string(rune(0x200D))
	bom    = string(rune(0xFEFF))
	vs16   = string(rune(0xFE0F))
	keycap = string(rune(0x20E3))
)

func TestSquash(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b string }{
		{"主人喜欢 Go 语言！", "主人喜欢Go语言"},
		{"今天，天气不错。", "今天天气不错"},
		{"「买牛奶」", "买牛奶"},
		{"周五要开会🎉", "周五要开会"},
		{"👨" + zwj + "👩" + zwj + "👧一家人", "一家人"},                 // ZWJ 拼起来的 emoji
		{"数字1" + vs16 + keycap + "和2" + vs16 + keycap, "数字1和2"}, // 变体选择符、键帽符
		{"a" + zwsp + "b" + bom + " c", "abc"},                  // 零宽空格、BOM、空格
		{"３点开会", "３点开会"},                                        // 不转全角
	}
	for _, c := range cases {
		if squash(c.a) != squash(c.b) {
			t.Errorf("%q 和 %q 应该算同一句：%q ≠ %q", c.a, c.b, squash(c.a), squash(c.b))
		}
	}
	for _, c := range [][2]string{{"Go", "go"}, {"3点开会", "３点开会"}, {"喜欢猫", "喜欢狗"}, {"1234", "123"}} {
		if squash(c[0]) == squash(c[1]) {
			t.Errorf("%q 和 %q 不该算同一句（字母数字、大小写、全角都不动）", c[0], c[1])
		}
	}
	if squash("？！…") != "" {
		t.Error("全是标点的应该是空的")
	}
}

func TestDupMergesSameText(t *testing.T) {
	t.Parallel()
	intended(t, "dup-text-merges")
	m, _ := newWritable(t, nil)
	first := mustApply(t, m, summary(ownerFact("主人喜欢 Go 语言！"))).Outcomes[0]
	for i, variant := range []string{"主人喜欢Go语言", "主人喜欢，Go 语言。", "「主人喜欢Go语言」😀"} {
		res := mustApply(t, m, summary(candidate(variant, "owner", "owner")))
		o := res.Outcomes[0]
		if o.Result != ResultMerged || o.Ref != first.Ref || o.Status != "active" {
			t.Fatalf("变体 %d %q 应该合并进第一条：%+v", i, variant, o)
		}
		if res.Counts[CountMerged] != 1 || len(res.Added) != 0 {
			t.Fatalf("合并不算新写：%+v", res)
		}
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 1 {
		t.Fatalf("同一句话应该只有一条：%d", n)
	}
	// 主体不一样不算同一条（「喜欢猫」可以是两个人各自的事）
	mustApply(t, m, summary(candidate("喜欢猫", "person:qq:10001", scopeA), candidate("喜欢猫", "person:qq:10002", scopeA)))
	if n := count(t, m, `SELECT count(*) FROM facts WHERE text = '喜欢猫'`); n != 2 {
		t.Fatalf("不同主体的同一句话应该各一条：%d", n)
	}
	// 同一批里前面刚写的，后面也撞得上
	res := mustApply(t, m, summary(ownerFact("下周三体检"), candidate("下周三，体检。", "owner", "owner")))
	if res.Outcomes[0].Result != ResultAdded || res.Outcomes[1].Result != ResultMerged || res.Outcomes[1].Ref != res.Outcomes[0].Ref {
		t.Fatalf("同一批里的同文应该合并：%+v", res.Outcomes)
	}
	// 被取代的、历史的同文不挡新写（「又搬回北京」是正常的）
	// 旧版是主人改正（remember）退成被取代的：整理的 supersedes 写入时不取代任何有效事实（W2）
	old := mustApply(t, m, summary(FactOp{Kind: "fact", Text: "主人住在北京", Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"owner"}})).Outcomes[0]
	shanghai := ownerFact("主人搬到了上海")
	shanghai.Supersedes = idOf(old.Ref)
	mustApply(t, m, remember(shanghai))
	back := mustApply(t, m, summary(FactOp{Kind: "fact", Text: "主人住在北京", Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"owner"}})).Outcomes[0]
	if back.Result != ResultAdded || back.Ref == old.Ref {
		t.Fatalf("被取代的旧版不挡同文新写：%+v", back)
	}
}

// 证据计数（13.2.13）：同一句话两天、两个会话各出现一次是 2 次，同一天同一会话说几遍是 1 次
func TestDupEvidenceCountsOncePerDayAndConversation(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	m.w.loc = time.UTC
	day := func(d, h int) int64 { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC).UnixMilli() }
	ev := func(conv string, at int64, self bool) *Evidence {
		return &Evidence{Conversation: conv, At: at, Seq: at / 1000, Speaker: "qq:10001", BySubject: self}
	}
	op := func(e *Evidence) FactOp {
		// 范围是两个群：转述的证据只在看得见这条候选的会话里算（W3b），两个会话都看得见才能各记一次
		f := candidate("阿福在学做饭", "person:qq:10001", scopeA, scopeB)
		f.Evidence = e
		return f
	}
	o := mustApply(t, m, summary(op(ev(GA, day(1, 9), false)))).Outcomes[0]
	id := idOf(o.Ref)
	evidence := func() int { return count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id) }
	if evidence() != 1 {
		t.Fatalf("新写的这条本身算第一次证据：%d", evidence())
	}
	for _, h := range []int{9, 10, 23} { // 同一天同一会话又说了几遍
		res := mustApply(t, m, summary(op(ev(GA, day(1, h), false))))
		if res.Counts[CountEvidenceRepeat] != 1 || res.Counts[CountEvidenceAdded] != 0 {
			t.Fatalf("同一天同一会话还是一次：%v", res.Counts)
		}
	}
	if evidence() != 1 {
		t.Fatalf("同一天同一会话说几遍应该还是 1 次：%d", evidence())
	}
	mustApply(t, m, summary(op(ev(GA, day(2, 9), false)))) // 第二天
	if evidence() != 2 {
		t.Fatalf("换一天是第二次：%d", evidence())
	}
	mustApply(t, m, summary(op(ev(GB, day(1, 9), false)))) // 同一天换个会话
	if evidence() != 3 {
		t.Fatalf("同一天换个会话也是一次新的：%d", evidence())
	}
	// 当天先是转述、后来本人也说了：还是那一行，补上「本人说过」
	if got := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ? AND by_subject = 1`, id); got != 0 {
		t.Fatalf("到现在都是转述：%d", got)
	}
	res := mustApply(t, m, summary(op(ev(GA, day(1, 20), true))))
	if res.Counts[CountEvidenceBySelf] != 1 || evidence() != 3 || count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ? AND by_subject = 1`, id) != 1 {
		t.Fatalf("本人说的应该补在同一行上：%v，共 %d 行", res.Counts, evidence())
	}
	// 一天的边界按时区算：UTC 3 日 17 点是东八区 4 日凌晨 1 点，day 列写 2026-10-04（按 UTC 算会是 10-03）
	m.w.loc = time.FixedZone("UTC+8", 8*3600)
	mustApply(t, m, summary(op(ev(GA, day(3, 17), false))))
	if evidence() != 4 || count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ? AND day = '2026-10-04'`, id) != 1 {
		t.Fatalf("按东八区算，这一次该记在 10-04：共 %d 行", evidence())
	}
}

// 合并时的范围（13.2.4 第 8(a) 步）：本人说的并上去，别人转述的不扩
func TestDupScopeUnionOnlyWhenSubjectSpoke(t *testing.T) {
	t.Parallel()
	intended(t, "dup-merge-scope-by-subject")
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	g3 := "qq:group:30003"
	base := candidate("阿福爱吃辣", "person:qq:10001", scopeA)
	base.Evidence = &Evidence{Conversation: GA, Speaker: "qq:10001", BySubject: true}
	id := mustApply(t, m, summary(base)).Outcomes[0].Ref

	say := func(conv string, self bool) Applied {
		f := candidate("阿福爱吃辣", "person:qq:10001", "conv:"+conv)
		f.Evidence = &Evidence{Conversation: conv, Speaker: "qq:10001", BySubject: self}
		return mustApply(t, m, summary(f))
	}
	scopes := func() string {
		return str(t, m, `SELECT group_concat(scope, ',') FROM (SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope)`, id)
	}
	// 本人在群 B 又说了一遍：范围并上，群 B 里也想得起来
	res := say(GB, true)
	if res.Counts[CountScopesWidened] != 1 || scopes() != "conv:"+GA+",conv:"+GB {
		t.Fatalf("本人说的应该并上范围：%v %q", res.Counts, scopes())
	}
	if hits := m.Recall(ctx, inGroup(GB), Query{Text: "阿福爱吃辣", Mode: ModeExplicit}).Hits; len(hits) != 1 || hits[0].Ref != id {
		t.Fatalf("并上以后群 B 里该想得起来：%+v", hits)
	}
	rev := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'scopes'`, idOf(id))
	if rev != 1 {
		t.Fatalf("并范围应该留一条修订：%d", rev)
	}
	before := jsonMap(t, str(t, m, `SELECT before_json FROM fact_revisions WHERE fact_id = ? AND op = 'scopes'`, idOf(id)))
	after := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'scopes'`, idOf(id)))
	if len(before["scopes"].([]any)) != 1 || len(after["scopes"].([]any)) != 2 {
		t.Fatalf("修订该记前后的范围：%v → %v", before, after)
	}
	// 群 3 转述仍不能替无关联的候选投票；想得起不等于这句转述能记成证据。
	res = say(g3, false)
	if res.Counts[CountEvidenceHidden] != 1 || res.Counts[CountScopesWidened] != 0 || scopes() != "conv:"+GA+",conv:"+GB {
		t.Fatalf("转述不该扩范围、也不该记证据：%v %q", res.Counts, scopes())
	}
	if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ? AND conversation = ?`, idOf(id), g3); n != 0 {
		t.Fatalf("看不见这条候选的会话里的转述不该记证据：%d", n)
	}
	if hits := m.Recall(ctx, inGroup(g3), Query{Text: "阿福爱吃辣", Mode: ModeExplicit}).Hits; len(hits) != 1 || hits[0].Ref != id || !hits[0].Hearsay {
		t.Fatalf("跨场合候选仍应想起并标听说：%+v", hits)
	}
	// 没给证据的合并（调用方没提供）：什么都不动
	f := candidate("阿福爱吃辣", "person:qq:10001", "conv:"+g3)
	res = mustApply(t, m, summary(f))
	if res.Outcomes[0].Result != ResultMerged || scopes() != "conv:"+GA+",conv:"+GB {
		t.Fatalf("没证据就只合并，不动范围：%+v %q", res.Outcomes, scopes())
	}
}

// 撤销的、AI 没通过的同文：整理里不新建、不复活；主人明说（remember）不受限
func TestDupRevokedNotRevived(t *testing.T) {
	t.Parallel()
	intended(t, "dup-revoked-not-revived")
	m, _ := newWritable(t, nil)
	aiFact := func(text string) FactOp {
		return FactOp{Kind: "fact", Text: text, Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"owner"}}
	}
	a := mustApply(t, m, summary(aiFact("主人讨厌香菜"))).Outcomes[0]
	mustApply(t, m, summary(RevokeOp{ID: idOf(a.Ref)}))
	if s := factStatus(t, m, "主人讨厌香菜"); s != "revoked" {
		t.Fatalf("撤销没生效：%s", s)
	}
	revs := count(t, m, `SELECT count(*) FROM fact_revisions`)

	// AI 没通过的（4.4 的 rejected，现在只有手工设置，晋升逻辑在第 3 步）
	r := mustApply(t, m, summary(aiFact("主人不吃海鲜"))).Outcomes[0]
	m.w.db.Exec(`UPDATE facts SET status = 'rejected' WHERE id = ?`, idOf(r.Ref))
	revs = count(t, m, `SELECT count(*) FROM fact_revisions`)

	for _, text := range []string{"主人讨厌香菜", "主人，讨厌香菜！", "主人不吃海鲜"} {
		res := mustApply(t, m, summary(candidate(text, "owner", "owner")))
		o := res.Outcomes[0]
		if o.Result != ResultRefused || res.Counts[CountDupRevoked] != 1 || len(res.Added) != 0 {
			t.Fatalf("%q：撤销或没通过的同文应该被拒：%+v %v", text, o, res.Counts)
		}
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 2 {
		t.Fatalf("不该新建：%d 条", n)
	}
	if n := count(t, m, `SELECT count(*) FROM fact_revisions`); n != revs {
		t.Fatalf("被拒的不该留修订：%d → %d", revs, n)
	}
	if s := factStatus(t, m, "主人讨厌香菜") + factStatus(t, m, "主人不吃海鲜"); s != "revokedrejected" {
		t.Fatalf("状态不该变：%s", s)
	}
	// 主人在私聊里明说要记（DupReport）：撤销过的不挡，新写一条有效的
	res := mustApply(t, m, remember(ownerFact("主人讨厌香菜")))
	if res.Outcomes[0].Result != ResultAdded || res.Outcomes[0].Status != "active" {
		t.Fatalf("主人明说要记，撤销过的同文不该挡：%+v", res.Outcomes[0])
	}
}

// remember 遇到同文：什么都不写，告诉调用方已经有了
func TestDupReportWritesNothing(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	have := mustApply(t, m, summary(ownerFact("主人的生日在三月"), candidate("阿福怕狗", "person:qq:10001", scopeA))).Outcomes
	revs, scopes := count(t, m, `SELECT count(*) FROM fact_revisions`), count(t, m, `SELECT count(*) FROM item_scopes`)

	res := mustApply(t, m, remember(ownerFact("主人的生日，在三月。")))
	if o := res.Outcomes[0]; o.Result != ResultExists || o.Ref != have[0].Ref || o.Status != "active" || res.Counts[CountExists] != 1 {
		t.Fatalf("同文应该报已经有了：%+v %v", o, res.Counts)
	}
	if count(t, m, `SELECT count(*) FROM facts`) != 2 || count(t, m, `SELECT count(*) FROM fact_revisions`) != revs || count(t, m, `SELECT count(*) FROM item_scopes`) != scopes || count(t, m, `SELECT count(*) FROM fact_evidence`) != 0 {
		t.Fatal("已经有了的同文什么都不该写")
	}
	// 同文是待确认的：主人明说要记就是放行，提成有效，不新写；放行记一条 confirm 修订、范围并上 owner
	f := FactOp{Kind: "fact", Text: "阿福怕狗", Subject: "person:qq:10001", Status: "active", ConfirmedBy: "owner", Scopes: []string{"owner"}}
	res = mustApply(t, m, remember(f))
	if o := res.Outcomes[0]; o.Result != ResultExists || o.Status != "active" || !o.Promoted || o.Ref != have[1].Ref || res.Counts[CountPromoted] != 1 {
		t.Fatalf("同文的待确认该被放行：%+v", o)
	}
	if count(t, m, `SELECT count(*) FROM facts`) != 2 || count(t, m, `SELECT count(*) FROM fact_revisions WHERE op = 'confirm'`) != 1 ||
		str(t, m, `SELECT group_concat(scope) FROM (SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope)`, have[1].Ref) != scopeA+",owner" {
		t.Fatal("放行不新写、记一条 confirm、范围并上")
	}
	if factStatus(t, m, "阿福怕狗") != "active" || str(t, m, `SELECT confirmed_by FROM facts WHERE text = '阿福怕狗'`) != "owner" {
		t.Fatal("该是有效、主人放行")
	}
}

// ───── 取代、撤销（B9、B14、清单 ⑤）─────

func aiFact(text string) FactOp {
	return FactOp{Kind: "fact", Text: text, Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"owner"}}
}

func TestSupersedeRechecksStateInTheTransaction(t *testing.T) {
	t.Parallel()
	intended(t, "supersede-revision-only-when-changed")
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	x := mustApply(t, m, summary(aiFact("主人住在北京"))).Outcomes[0]
	xid := idOf(x.Ref)
	replace := func(text string) FactOp { f := ownerFact(text); f.Supersedes = xid; return f }

	// 第一个取代（主人自己，remember）：旧的变成被取代，新的有效，修订记前后
	res := mustApply(t, m, remember(replace("主人搬到了上海")))
	o := res.Outcomes[0]
	if o.Status != "active" || o.Replaced == nil || o.Replaced.ID != xid || o.Replaced.Text != "主人住在北京" || res.Counts[CountSuperseded] != 1 {
		t.Fatalf("取代没成：%+v %v", o, res.Counts)
	}
	if got := str(t, m, `SELECT status || '>' || superseded_by FROM facts WHERE id = ?`, xid); got != "superseded>"+idOf(o.Ref) {
		t.Fatalf("旧的：%q", got)
	}
	before := jsonMap(t, str(t, m, `SELECT before_json FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, xid))
	after := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, xid))
	if before["status"] != "active" || after["status"] != "superseded" || after["superseded_by"] != idOf(o.Ref) {
		t.Fatalf("修订该记前后：%v → %v", before, after)
	}
	if got := str(t, m, `SELECT reason FROM fact_revisions WHERE fact_id = ? AND op = 'add'`, idOf(o.Ref)); got != "替换 "+xid {
		t.Fatalf("add 修订的原因：%q", got)
	}

	// 第二个人拿着过时的看法也来取代 X：X 已经不是有效的了。
	// remember：整批作废，什么都不写（ErrReplaceGone，成功文字才不会说谎）；编号压根不存在也一样
	sizes := tableSizes(t, m)
	for name, gone := range map[string]string{"X 刚被取代": xid, "编号不存在": "NOSUCHID"} {
		f := ownerFact("主人搬到了深圳")
		f.Supersedes = gone
		if _, err := m.apply(ctx, remember(f)); !errors.Is(err, ErrReplaceGone) || tableSizes(t, m) != sizes {
			t.Fatalf("%s：remember 该整批作废：%v，%s → %s", name, err, sizes, tableSizes(t, m))
		}
	}
	// 整理：写入时本来就不取代（W2），写的是待确认；带 supersedes 的有效事实也降成待确认，不带放行和 speech_act；X 的修订不再多一条
	replaceByAI := func(text, target string) FactOp { f := aiFact(text); f.Supersedes = target; return f }
	res = mustApply(t, m, summary(replaceByAI("主人搬到了深圳", xid)))
	o2 := res.Outcomes[0]
	if o2.Result != ResultAdded || o2.Status != "candidate" || o2.Replaced != nil || res.Counts[CountSupersedeDeferred] != 1 || res.Counts[CountSuperseded] != 0 {
		t.Fatalf("整理带 supersedes：新的该是待确认、不取代：%+v %v", o2, res.Counts)
	}
	if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, xid); n != 1 {
		t.Fatalf("没改到的取代不该再写修订（Bun 无条件写）：X 有 %d 条 supersede 修订", n)
	}
	if got := str(t, m, `SELECT superseded_by FROM facts WHERE id = ?`, xid); got != idOf(o.Ref) {
		t.Fatalf("X 的 superseded_by 不该被后来的改掉：%q", got)
	}
	if got := str(t, m, `SELECT coalesce(confirmed_by, '-') || '|' || coalesce(speech_act, '-') FROM facts WHERE id = ?`, idOf(o2.Ref)); got != "-|-" {
		t.Fatalf("降成待确认以后不该带放行和 speech_act：%q", got)
	}
	if o3 := mustApply(t, m, summary(replaceByAI("主人搬到了成都", "NOSUCHID"))).Outcomes[0]; o3.Status != "candidate" {
		t.Fatalf("编号不存在：%+v", o3)
	}

	// 取代这个动作本身（提升候选时 W3b 用）：事务里现读状态，X 不是有效的了就是 supersedeGone，不计保护、不写修订
	revs := count(t, m, `SELECT count(*) FROM fact_revisions`)
	if got, counts := supersedeDirect(t, m, BySummary, xid, "NEWER"); got != supersedeGone || len(counts) != 0 {
		t.Fatalf("X 已经不是有效的：%v %v", got, counts)
	}
	if got, counts := supersedeDirect(t, m, BySummary, "NOSUCHID", "NEWER"); got != supersedeGone || len(counts) != 0 {
		t.Fatalf("编号不存在：%v %v", got, counts)
	}
	if n := count(t, m, `SELECT count(*) FROM fact_revisions`); n != revs {
		t.Fatalf("没取代成不该写修订：%d → %d", revs, n)
	}
}

// 主人放行过的事实，AI 不改；主人自己可以（4.4 第 188 行）。整理的取代写入时本来就不发生（延后到提升，W2），撤销被保护挡下
func TestSupersedeProtectsOwnerConfirmed(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	y := mustApply(t, m, remember(ownerFact("主人的猫叫年糕"))).Outcomes[0]
	yid := idOf(y.Ref)
	replace := func(text string) FactOp { f := aiFact(text); f.Supersedes = yid; return f }

	res := mustApply(t, m, summary(replace("主人的猫叫团子")))
	if o := res.Outcomes[0]; o.Status != "candidate" || res.Counts[CountSupersedeDeferred] != 1 || res.Counts[CountSuperseded] != 0 {
		t.Fatalf("AI 不能取代主人放行过的：新的该是待确认、不取代：%+v %v", o, res.Counts)
	}
	if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, yid); s != "active" {
		t.Fatalf("主人放行过的不该被改：%s", s)
	}
	// 取代这个动作本身也挡：W3b 提升时调它，受保护的是 supersedeProtected，计数 supersede_protected
	if got, counts := supersedeDirect(t, m, BySummary, yid, "NEWER"); got != supersedeProtected || counts[CountSupersedeProtected] != 1 {
		t.Fatalf("取代动作该被保护挡下：%v %v", got, counts)
	}
	// 撤销也一样：没动，也没留修订，计 revoke_protected（不是 revoke_noop）
	revs := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ?`, yid)
	if res := mustApply(t, m, summary(RevokeOp{ID: yid})); res.Outcomes[0].Result != ResultNoop || res.Counts[CountRevokeProtected] != 1 || res.Counts[CountRevokeNoop] != 0 {
		t.Fatalf("AI 不能撤销主人放行过的：%+v %v", res.Outcomes[0], res.Counts)
	}
	if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, yid); s != "active" || count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ?`, yid) != revs {
		t.Fatal("主人放行过的被改了，或多了修订")
	}

	// 主人自己（remember）可以取代，原来的正文带回去给成功文字用
	f := ownerFact("主人的猫叫团团")
	f.Supersedes = yid
	res = mustApply(t, m, remember(f))
	if o := res.Outcomes[0]; o.Status != "active" || o.Replaced == nil || o.Replaced.Text != "主人的猫叫年糕" {
		t.Fatalf("主人自己应该能取代：%+v", o)
	}
	if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, yid); s != "superseded" {
		t.Fatalf("旧的该是被取代：%s", s)
	}
	// 要取代的已经不是有效的了（yid 刚被取代）：remember 整批作废，什么都不写，成功文字才不会说谎（规格 13.2.3）。
	// 排在它前面的动作也跟着回滚；编号压根不存在、要取代的是待确认的，同样
	cand := mustApply(t, m, summary(candidate("阿福想养狗", "person:qq:10001", scopeA))).Outcomes[0]
	before := tableSizes(t, m)
	for name, gone := range map[string]string{"刚被取代": yid, "编号不存在": "NOSUCHID", "只是待确认": idOf(cand.Ref)} {
		g := ownerFact("主人的猫叫团团团")
		g.Supersedes = gone
		_, err := m.apply(context.Background(), remember(ownerFact("同一批里排在前面的"), g))
		if !errors.Is(err, ErrReplaceGone) {
			t.Fatalf("%s：应该是 ErrReplaceGone：%v", name, err)
		}
		if after := tableSizes(t, m); after != before {
			t.Fatalf("%s：整批该回滚，什么都不留：%s → %s", name, before, after)
		}
		if n := count(t, m, `SELECT count(*) FROM facts WHERE text IN ('同一批里排在前面的', '主人的猫叫团团团')`); n != 0 {
			t.Fatalf("%s：这一批的话不该进库：%d 条", name, n)
		}
	}
	if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, idOf(cand.Ref)); s != "candidate" {
		t.Fatalf("待确认的那条不该被动：%s", s)
	}
	// 整理那一路：同样的情形（旧的已经被取代 / 编号不存在）也只写待确认，话还在，不报错
	for i, gone := range []string{yid, "NOSUCHID"} {
		f := aiFact("主人的猫叫" + strings.Repeat("团", 4+i))
		f.Supersedes = gone
		if o := mustApply(t, m, summary(f)).Outcomes[0]; o.Result != ResultAdded || o.Status != "candidate" || o.Replaced != nil {
			t.Fatalf("整理里带 supersedes 该只写待确认：%+v", o)
		}
	}
}

// tableSizes：写入侧几张表现在各有几行，查「整批回滚、什么都没留」
func tableSizes(t *testing.T, m *Memory) string {
	t.Helper()
	var parts []string
	for _, tb := range []string{"facts", "fact_revisions", "item_scopes", "fact_evidence", "episodes", "memory_jobs", "summary_state"} {
		parts = append(parts, tb+"="+strconv.Itoa(count(t, m, `SELECT count(*) FROM `+tb)))
	}
	return strings.Join(parts, " ")
}

// 复核（W2）：同一批里先有同主体的写（同文缓存已经装上）、再撤销、再写同一句，不能新建一条：
// 撤销只把缓存里那一行改成已撤销，不是拿掉，不然结果取决于这一批里动作的先后（13.2.4 第 8(b) 步、13.2.14）
func TestRevokeThenSameTextInOneBatchDoesNotRevive(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	x := mustApply(t, m, summary(aiFact("主人讨厌香菜"))).Outcomes[0]

	// 同一个主体的缓存先被前面的动作装上
	res := mustApply(t, m, summary(candidate("主人周末去爬山", "owner", "owner"), RevokeOp{ID: idOf(x.Ref)}, aiFact("主人讨厌香菜")))
	if o := res.Outcomes[1]; o.Result != ResultRevoked {
		t.Fatalf("撤销应该成：%+v", o)
	}
	if o := res.Outcomes[2]; o.Result != ResultRefused || o.Ref != x.Ref || o.Status != "revoked" || res.Counts[CountDupRevoked] != 1 || len(res.Added) != 1 {
		t.Fatalf("撤销后同一批里再说同一句，不新建、不复活：%+v %v", o, res.Counts)
	}
	if n := count(t, m, `SELECT count(*) FROM facts WHERE text = '主人讨厌香菜'`); n != 1 {
		t.Fatalf("同文只该有被撤销的那一条：%d 条", n)
	}

	// 缓存没装上就撤销：和上面是同一个结局（原来顺序不同结果不同）
	y := mustApply(t, m, summary(aiFact("主人怕蛇"))).Outcomes[0]
	res = mustApply(t, m, summary(RevokeOp{ID: idOf(y.Ref)}, aiFact("主人怕蛇")))
	if o := res.Outcomes[1]; o.Result != ResultRefused || o.Ref != y.Ref {
		t.Fatalf("先撤销再写同一句：%+v", o)
	}

	// 邻居：主人自己明说要再记（remember），撤销过的不挡；同一批里也一样
	z := mustApply(t, m, summary(aiFact("主人怕打雷"))).Outcomes[0]
	res = mustApply(t, m, remember(ownerFact("主人周末爬山回来"), RevokeOp{ID: idOf(z.Ref)}, ownerFact("主人怕打雷")))
	if o := res.Outcomes[2]; o.Result != ResultAdded || o.Status != "active" || o.Ref == z.Ref {
		t.Fatalf("主人明说再记的，撤销过的不挡：%+v", o)
	}
}

// B14：撤销只动有效的
func TestRevokeOnlyTouchesActiveFacts(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	a := mustApply(t, m, summary(aiFact("主人讨厌下雨"))).Outcomes[0]
	res := mustApply(t, m, summary(RevokeOp{ID: idOf(a.Ref)}))
	if o := res.Outcomes[0]; o.Result != ResultRevoked || o.Status != "revoked" || res.Counts[CountRevoked] != 1 {
		t.Fatalf("撤销有效的：%+v", o)
	}
	b4 := jsonMap(t, str(t, m, `SELECT before_json FROM fact_revisions WHERE fact_id = ? AND op = 'revoke'`, idOf(a.Ref)))
	af := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'revoke'`, idOf(a.Ref)))
	if b4["status"] != "active" || af["status"] != "revoked" {
		t.Fatalf("修订：%v → %v", b4, af)
	}
	if hits := m.Recall(context.Background(), ownerHere, Query{Text: "主人讨厌下雨", Mode: ModeExplicit}).Hits; len(hits) != 0 {
		t.Fatalf("撤销以后不该再被想起：%+v", hits)
	}

	// 已经撤过、被取代的、待确认的、不存在的：不动，也不记修订
	old := mustApply(t, m, summary(aiFact("主人住在甲地"))).Outcomes[0]
	newer := ownerFact("主人住在乙地") // 主人改正（remember）才取代得了；整理的 supersedes 写入时不取代（W2）
	newer.Supersedes = idOf(old.Ref)
	mustApply(t, m, remember(newer))
	cand := mustApply(t, m, summary(candidate("阿福会弹琴", "person:qq:10001", scopeA))).Outcomes[0]
	revs := count(t, m, `SELECT count(*) FROM fact_revisions`)
	for name, id := range map[string]string{"已经撤过": idOf(a.Ref), "被取代的旧版": idOf(old.Ref), "待确认": idOf(cand.Ref), "不存在": "NOSUCHID"} {
		res := mustApply(t, m, summary(RevokeOp{ID: id}))
		if res.Outcomes[0].Result != ResultNoop || res.Counts[CountRevokeNoop] != 1 {
			t.Errorf("%s：撤销应该没动：%+v", name, res.Outcomes[0])
		}
	}
	if n := count(t, m, `SELECT count(*) FROM fact_revisions`); n != revs {
		t.Fatalf("没动的撤销不该留修订：%d → %d", revs, n)
	}
	if got := factStatus(t, m, "主人住在甲地") + "," + factStatus(t, m, "阿福会弹琴"); got != "superseded,candidate" {
		t.Fatalf("状态不该变（被取代的旧版不因撤销复活或改状态）：%s", got)
	}
}

// ───── 并发、写锁、停掉 ─────

// 清单 ⑤⑬：两个会话同时替换同一条事实。
// 主人那一路（remember）：恰好一个赢，另一个整批作废（ErrReplaceGone），只有一条新说法有效，旧的只被取代一次；
// 整理那一路（W2）：写入时都不取代，两条都是待确认，旧的原样有效，一条 supersede 修订都没有
func TestTwoSessionsReplacingTheSameFactLeaveOneActive(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	race := func(round int, batch func(FactOp) Batch, check func(errs []error)) (x Outcome, texts []string) {
		x = mustApply(t, m, summary(aiFact("第"+strconv.Itoa(round)+"轮的旧事实"))).Outcomes[0]
		start := make(chan struct{})
		var wg sync.WaitGroup
		texts = []string{"第" + strconv.Itoa(round) + "轮甲的新说法", "第" + strconv.Itoa(round) + "轮乙的新说法"}
		errs := make([]error, len(texts))
		for i, text := range texts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f := ownerFact(text)
				f.Supersedes = idOf(x.Ref)
				<-start
				_, errs[i] = m.apply(context.Background(), batch(f))
			}()
		}
		close(start)
		wg.Wait()
		check(errs)
		return x, texts
	}
	for round := 0; round < 25; round++ {
		x, texts := race(round, func(f FactOp) Batch { return remember(f) }, func(errs []error) {
			won, lost := 0, 0
			for _, err := range errs {
				switch {
				case err == nil:
					won++
				case errors.Is(err, ErrReplaceGone):
					lost++
				default:
					t.Errorf("apply：%v", err)
				}
			}
			if won != 1 || lost != 1 {
				t.Fatalf("第 %d 轮 remember：赢 %d 输 %d，要恰好一赢一输", round, won, lost)
			}
		})
		var active int
		for _, text := range texts {
			if factStatus0(t, m, text) == "active" {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("第 %d 轮：两条新说法里有 %d 条有效，要恰好 1 条", round, active)
		}
		if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, idOf(x.Ref)); s != "superseded" {
			t.Fatalf("旧的该被取代：%s", s)
		}
		if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, idOf(x.Ref)); n != 1 {
			t.Fatalf("旧的只被取代一次，修订 %d 条", n)
		}
	}
	for round := 25; round < 40; round++ {
		x, texts := race(round, func(f FactOp) Batch { f.Status, f.ConfirmedBy = "candidate", ""; return summary(f) }, func(errs []error) {
			for _, err := range errs {
				if err != nil {
					t.Errorf("apply：%v", err)
				}
			}
		})
		for _, text := range texts {
			if s := factStatus(t, m, text); s != "candidate" {
				t.Fatalf("第 %d 轮 summary：%q 该是待确认：%s", round, text, s)
			}
		}
		if s := str(t, m, `SELECT status FROM facts WHERE id = ?`, idOf(x.Ref)); s != "active" {
			t.Fatalf("第 %d 轮 summary：旧的不该被整理取代：%s", round, s)
		}
		if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, idOf(x.Ref)); n != 0 {
			t.Fatalf("第 %d 轮 summary：旧的不该有 supersede 修订：%d 条", round, n)
		}
	}
}

func TestWriteLockWaitsThenGivesUp(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	ctx := context.Background()
	m.w.sem <- struct{}{} // 另一个写的人拿着锁

	start := time.Now()
	_, err := m.apply(ctx, Batch{By: BySummary, Ops: []Op{ownerFact("等不到锁")}, Wait: 40 * time.Millisecond})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("等不到锁应该是 ErrBusy：%v", err)
	}
	// 只卡下限和"没按默认的 5 秒等"：上限放在默认值上，机器忙时 40ms 的定时器晚几秒醒不算错
	if d := time.Since(start); d < 30*time.Millisecond || d >= DefaultWriteWait {
		t.Fatalf("应该等了 40ms 左右：%v", d)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := m.apply(cctx, Batch{By: BySummary, Ops: []Op{ownerFact("等锁时被取消")}, Wait: 5 * time.Second}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等锁时 ctx 到期应该返回 ctx 的错：%v", err)
	}
	if _, err := m.BumpGeneration(cctx); err == nil {
		t.Fatal("等锁时 ctx 已到期，BumpGeneration 也该出错")
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 0 {
		t.Fatalf("没拿到锁的什么都不写：%d", n)
	}

	// 锁很快就放了：等得到，Waited 记着等了多久
	done := make(chan Applied, 1)
	go func() {
		res, err := m.apply(ctx, summary(ownerFact("等到了锁")))
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(60 * time.Millisecond)
	<-m.w.sem
	res := <-done
	if res.Waited < 40*time.Millisecond || count(t, m, `SELECT count(*) FROM facts`) != 1 {
		t.Fatalf("放锁以后应该写进去，Waited 该有 60ms 左右：%v", res.Waited)
	}
}

func TestCloseStopsWrites(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	mustApply(t, m, summary(ownerFact("关之前写的")))
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("重复 Close 不该出错：%v", err)
	}
	for name, err := range map[string]error{
		"apply": func() error { _, err := m.apply(context.Background(), summary(ownerFact("关了以后"))); return err }(),
		"Bump":  func() error { _, err := m.BumpGeneration(context.Background()); return err }(),
		"Init":  func() error { _, err := m.InitCursor(context.Background(), "console:a", 1); return err }(),
	} {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("关了以后 %s 应该是 ErrClosed：%v", name, err)
		}
	}
}

// 清单 ④：Close 要等写到一半的事务写完（整体提交），不留半截
func TestCloseWaitsForAnInFlightWrite(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, nil)
	m, _ := openW(t, file, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	real := m.w.newID
	m.w.newID = func() string {
		once.Do(func() { close(entered) })
		<-release // 事务已经开了，卡在写第一条事实上
		return real()
	}
	applied := make(chan error, 1)
	go func() {
		_, err := m.apply(context.Background(), summary(ownerFact("写到一半时关库"), ownerFact("同一批的第二条")))
		applied <- err
	}()
	<-entered
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("事务还没写完，Close 就返回了")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-applied; err != nil {
		t.Fatalf("在途的写应该完整提交：%v", err)
	}
	<-closed
	// 关库以后单独打开看：两条都在，没有半截
	m2, _ := openW(t, file, nil)
	if n := count(t, m2, `SELECT count(*) FROM facts`); n != 2 {
		t.Fatalf("两条应该都写进去了：%d", n)
	}
	if n := count(t, m2, `SELECT count(*) FROM item_scopes`) + count(t, m2, `SELECT count(*) FROM fact_revisions`); n != 4 {
		t.Fatalf("范围组和修订也该齐：%d", n)
	}
}

// 清单 ⑤：两个角色两个库，互不串
func TestTwoRolesDoNotShareAnything(t *testing.T) {
	t.Parallel()
	a, _ := newWritable(t, nil)
	b, _ := newWritable(t, nil)
	mustApply(t, a, summary(ownerFact("只有甲角色知道的事")))
	if len(b.Items()) != 0 || count(t, b, `SELECT count(*) FROM facts`) != 0 {
		t.Fatal("乙角色的库里不该有甲写的东西")
	}
	if hits := b.Recall(context.Background(), ownerHere, Query{Text: "甲角色知道", Mode: ModeExplicit}).Hits; len(hits) != 0 {
		t.Fatalf("乙角色想起了甲的事：%+v", hits)
	}
	// 同一句话在两个库里各记各的，不当成重复
	res := mustApply(t, b, summary(ownerFact("只有甲角色知道的事")))
	if res.Outcomes[0].Result != ResultAdded {
		t.Fatalf("另一个库里不是重复：%+v", res.Outcomes[0])
	}
}

// 清单 ④：写到一半 panic 了（被上层接住），写锁要放掉，后面的写和 Close 不能永远等下去；这一批整个回滚
func TestApplyPanicReleasesTheWriteLock(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	// 失败时锁还扣着，后面 Close 会一直等；这条清理排在 Close 之前，先把锁让出来，让测试报错而不是挂住
	t.Cleanup(func() {
		select {
		case <-m.w.sem:
		default:
		}
	})
	real := m.w.newID
	m.w.newID = func() string { panic("写到一半炸了") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("替身该 panic")
			}
		}()
		m.apply(context.Background(), summary(ownerFact("炸掉的那一条"), ownerFact("同一批的另一条")))
	}()
	m.w.newID = real

	if _, err := m.apply(context.Background(), Batch{By: BySummary, Ops: []Op{ownerFact("炸了以后写的")}, Wait: 300 * time.Millisecond}); err != nil {
		t.Fatalf("panic 以后写锁该已经放了：%v", err)
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 1 || factStatus(t, m, "炸了以后写的") != "active" {
		t.Fatalf("炸掉的那一批整个回滚，只剩后写的一条：%d 条", n)
	}
}
