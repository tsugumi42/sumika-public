package turn

// 重建留痕（规格-提示词前缀稳定第八节）：每个触发点记对原因、窗口外的条数数得准、数不出来不影响重建、只在重建后的第一步写。

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/history"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// rebuildRow 是一次回复记的重建留痕（没有 rebuild 键的 Trace 是 nil）
type rebuildRow struct {
	Trace *RebuildTrace
	Raw   string
}

// traces 是所有回复（按先后）的重建留痕
func (h *harness) rebuilds() []rebuildRow {
	h.t.Helper()
	rows, err := h.st.Trace.Query(`SELECT coalesce(context_json, '') FROM model_runs WHERE stage = 'reply' ORDER BY rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []rebuildRow
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			h.t.Fatal(err)
		}
		var ctx struct {
			Rebuild *RebuildTrace `json:"rebuild"`
		}
		if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
			h.t.Fatalf("context_json 不是 JSON：%v：%s", err, raw)
		}
		out = append(out, rebuildRow{ctx.Rebuild, raw})
	}
	return out
}

func (h *harness) wantTrace(i int, reason string) *RebuildTrace {
	h.t.Helper()
	rows := h.rebuilds()
	if i >= len(rows) {
		h.t.Fatalf("只有 %d 次回复，要看第 %d 次", len(rows), i)
	}
	tr := rows[i].Trace
	if tr == nil {
		h.t.Fatalf("第 %d 次回复没有 rebuild 留痕：%s", i, rows[i].Raw)
	}
	if tr.Reason != reason {
		h.t.Fatalf("第 %d 次回复的重建原因 %q（also %v），应是 %q", i, tr.Reason, tr.Also, reason)
	}
	if tr.Count != CountExact {
		h.t.Fatalf("第 %d 次回复的条数没数出来：%+v", i, tr)
	}
	return tr
}

// 头一回：库里没有旧消息记 first；只在重建后的第一步写 rebuild，后面的步骤整个没有这个键（不写 null）
func TestRebuildTraceFirstOnly(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: replies(2)})
	h.send("第一句")
	h.send("第二句")
	tr := h.wantTrace(0, RebuildFirst)
	if tr.Kept != 0 || tr.Dropped != 0 || tr.Covered != 0 || tr.Snapshot {
		t.Fatalf("头一回什么都没带也没丢：%+v", tr)
	}
	if rows := h.rebuilds(); rows[1].Trace != nil || strings.Contains(rows[1].Raw, `"rebuild"`) {
		t.Fatalf("接着用的实例不记重建，也不写 null：%s", rows[1].Raw)
	}
}

// 程序重启后第一次（库里有旧消息）：窗口外的条数数得准。群聊仍是锚定窗口：16 条旧消息、窗口 4，起点锚在第 12 条，带 4 条，丢 12 条，没有摘要
// （本地私聊和主人 QQ 私聊改成从起点重建以后不再丢窗口外的话，这里的锚定口径用群聊钉住）
func TestRebuildTraceRestartDropped(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: replies(1), mutate: func(c *Config) { c.RecentMessages = 4 }})
	for i := range 16 {
		h.storeGroup(fmt.Sprintf("旧话 %d", i))
	}
	o := wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{h.storeGroup("现在这句")}, Text: "现在这句",
		Audience: groupAud, Addressed: true, Scope: "g"}))
	h.commitSaid(groupConv, o, nil)
	tr := h.wantTrace(0, RebuildRestart)
	if tr.Kept != 4 || tr.Dropped != 12 || tr.Covered != 0 || tr.Snapshot {
		t.Fatalf("带 4 条、丢 12 条、没有摘要：%+v", tr)
	}
}

// 闲置超时：同一个会话闲了 idleAfter 以上，下一句重建记 idle；别的条件都没变，also 是空的
func TestRebuildTraceIdle(t *testing.T) {
	t.Parallel()
	c := newClock()
	h := newHarnessOpts(t, harnessOpts{replies: replies(2), mutate: func(cfg *Config) { cfg.Now = c.now }})
	h.send("第一句")
	c.tick(time.Hour)
	h.send("隔了一个小时")
	tr := h.wantTrace(1, RebuildIdle)
	if len(tr.Also) != 0 || tr.Kept != 2 || tr.Dropped != 0 {
		t.Fatalf("只有闲置一个原因，带上头一轮的两条：%+v", tr)
	}
}

// 实例数超限被挤掉、RestartIn、MarkStale、同会话换听众档：各记各的原因
func TestRebuildTraceHostCauses(t *testing.T) {
	t.Parallel()
	c := newClock()
	h := newHarnessOpts(t, harnessOpts{replies: replies(8), mutate: func(cfg *Config) { cfg.MaxLive, cfg.Now = 2, c.now }})
	g1, g2, g3 := recycleGroup(1), recycleGroup(2), recycleGroup(3)
	h.sayIn(g1, "第一个群的话", true)
	c.tick(time.Minute)
	h.sayIn(g2, "第二个群的话", true)
	c.tick(time.Minute)
	h.sayIn(g3, "第三个群的话", true)
	c.tick(time.Minute)
	h.sayIn(g1, "回到第一个群", true) // 第一个群被挤掉过
	h.wantTrace(3, RebuildEvicted)

	h.wantTrace(1, RebuildFirst)
	h.sayIn(g3, "第三个群接着说", true) // 实例还在，接着用，不记重建
	if rows := h.rebuilds(); rows[4].Trace != nil {
		t.Fatalf("接着用的实例不记重建：%s", rows[4].Raw)
	}

	h.hub.RestartIn(h.c.CharacterID, g3)
	h.sayIn(g3, "新话题以后", true)
	h.wantTrace(5, RebuildRestartIn)

	h.hub.MarkStale(h.c.CharacterID, g3)
	h.sayIn(g3, "别的线写了话以后", true)
	h.wantTrace(6, RebuildStale)

	// 同一个会话换了一档（Scope 不同）：本进程见过这个会话、没见过这一档
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: g3, ActorKind: "human", ActorID: "p1", Origin: "human", Kind: "message", Text: "换一档",
		CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: g3, Seqs: []int64{seq}, Text: "换一档",
		Audience: audience.Audience{Channel: "qq", Conversation: g3, Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "g-other"}))
	h.wantTrace(7, RebuildNewAudience)
}

// 回复前过了硬门槛压成了：记 over_limit_compacted，用上快照，窗口外被摘要覆盖的条数大于 0
func TestRebuildTraceOverLimitCompacted(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`})
	h.nearHard()
	h.send("过了门槛")
	tr := h.wantTrace(1, RebuildOverLimitSnap)
	if !tr.Snapshot || tr.Covered <= 0 || tr.Covered > tr.Dropped || tr.Kept <= 0 {
		t.Fatalf("用上了快照，丢掉的有摘要覆盖：%+v", tr)
	}
}

// 压缩没成、只带最近的话：记 over_limit_trimmed，没有快照、没有摘要覆盖，丢的条数是被裁掉的
func TestRebuildTraceOverLimitTrimmed(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `]`})
	h.nearHard()
	h.send("过了门槛")
	tr := h.wantTrace(1, RebuildOverLimitTrim)
	if tr.Snapshot || tr.Covered != 0 || tr.Dropped <= 0 || tr.Kept <= 0 {
		t.Fatalf("没有快照、没有摘要覆盖、丢了被裁掉的：%+v", tr)
	}
}

// 重建后还没发出请求就又重建：先到的当 reason，后到的进 also，两个都留
func TestRebuildTraceKeepsEarlierReason(t *testing.T) {
	t.Parallel()
	var tr RebuildTrace
	tr.add([]string{RebuildIdle})
	tr.add([]string{RebuildOverLimitSnap})
	if tr.Reason != RebuildIdle || !slices.Equal(tr.Also, []string{RebuildOverLimitSnap}) {
		t.Fatalf("先到的当 reason：%+v", tr)
	}
	var multi RebuildTrace
	multi.add([]string{RebuildIdle, RebuildModel, RebuildTools, RebuildModel})
	if multi.Reason != RebuildModel || !slices.Equal(multi.Also, []string{RebuildTools, RebuildIdle}) {
		t.Fatalf("同时有几个按固定优先级、去重：%+v", multi)
	}
	var none RebuildTrace
	none.add(nil)
	if none.Reason != RebuildUnknown {
		t.Fatalf("说不出原因记 unknown：%+v", none)
	}
}

func TestRebuildOrderHasNoDuplicates(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, r := range RebuildReasons() {
		if seen[r] || r == "" {
			t.Fatalf("原因标识重复或为空：%q", r)
		}
		seen[r] = true
	}
	if !seen[RebuildUnknown] {
		t.Fatal("要有 unknown")
	}
}

// 复用条件逐项拆开：每一项单独变都认得出是哪个原因，一项都没变（拆不出）返回空，由调用方记 unknown
func TestReuseBlockersEachCondition(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	sections := []prompts.Section{{ID: "core", Text: "通用"}, {ID: "persona-identity", Text: "身份"}, {ID: "scene-home", Text: "场合"}}
	decls := []tools.Declaration{{Name: "read"}}
	mk := func() (*Live, Input) {
		in := Input{Identity: kernel.Invocation{Scope: kernel.Scope{ActorKind: "owner", ActorID: "o1"}}}
		l := &Live{in: in, model: "m1", system: prompts.Join(sections), staleGen: 1, lastUsed: now, b: &Built{Sections: sections, Decls: decls, EpochSeen: "e1"}}
		return l, in
	}
	call := func(l *Live, in Input, model string, secs []prompts.Section, ds []tools.Declaration, epoch string, stale uint64, at time.Time) []string {
		return reuseBlockers(l, in, model, secs, prompts.Join(secs), ds, epoch, stale, at, 45*time.Minute)
	}
	l, in := mk()
	if got := call(l, in, "m1", sections, decls, "e1", 1, now); len(got) != 0 {
		t.Fatalf("什么都没变不该有原因：%v", got)
	}
	with := func(s string, edit func(*Live, *Input)) {
		t.Helper()
		l, in := mk()
		edit(l, &in)
		got := call(l, in, "m1", sections, decls, "e1", 1, now)
		if !slices.Contains(got, s) {
			t.Fatalf("应该认出 %s：%v", s, got)
		}
	}
	with(RebuildSpeaker, func(l *Live, in *Input) { in.Identity.Scope.ActorID = "o2" })
	with(RebuildScope, func(l *Live, in *Input) { in.Identity.Scope.AuthorityRevision = 9 })
	with(RebuildWorkspace, func(l *Live, in *Input) { in.Workspace.Revision = 3 })
	with(RebuildStale, func(l *Live, in *Input) { l.staleGen = 0 })
	for name, c := range map[string]struct {
		want string
		secs []prompts.Section
	}{
		"改卡":   {RebuildPersona, []prompts.Section{{ID: "core", Text: "通用"}, {ID: "persona-identity", Text: "新身份"}, {ID: "scene-home", Text: "场合"}}},
		"场合":   {RebuildScene, []prompts.Section{{ID: "core", Text: "通用"}, {ID: "persona-identity", Text: "身份"}, {ID: "scene-home", Text: "新场合"}}},
		"通用":   {RebuildPrompt, []prompts.Section{{ID: "core", Text: "新通用"}, {ID: "persona-identity", Text: "身份"}, {ID: "scene-home", Text: "场合"}}},
		"少了一段": {RebuildPersona, []prompts.Section{{ID: "core", Text: "通用"}, {ID: "scene-home", Text: "场合"}}},
	} {
		l, in := mk()
		if got := call(l, in, "m1", c.secs, decls, "e1", 1, now); !slices.Contains(got, c.want) {
			t.Fatalf("%s：应该认出 %s：%v", name, c.want, got)
		}
	}
	l, in = mk()
	if got := call(l, in, "m2", sections, decls, "e1", 1, now); !slices.Contains(got, RebuildModel) {
		t.Fatalf("换模型：%v", got)
	}
	if got := call(l, in, "m1", sections, []tools.Declaration{{Name: "read"}, {Name: "write"}}, "e1", 1, now); !slices.Contains(got, RebuildTools) {
		t.Fatalf("工具清单变：%v", got)
	}
	if got := call(l, in, "m1", sections, decls, "e2", 1, now); !slices.Contains(got, RebuildCompacted) {
		t.Fatalf("有新快照：%v", got)
	}
	if got := call(l, in, "m1", sections, decls, "e1", 1, now.Add(46*time.Minute)); !slices.Equal(got, []string{RebuildIdle}) {
		t.Fatalf("只闲置：%v", got)
	}
}

// 数条数：这一批、排队没轮到的更新消息不算丢；一条原文都没带上时数到这一批之前；数出错记 failed，不影响调用方
func TestCountRebuild(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const conv = "console:count"
	add := func(origin, kind string) int64 {
		t.Helper()
		s, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "o", Origin: origin, Kind: kind, Text: "x", CreatedAt: time.Now().UnixMilli()}, []string{"c"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	add("owner", "message")
	add("owner", "message")
	add("owner", "notice") // 通知不算数
	s3 := add("owner", "message")
	add("automation:dry-run", "message") // 没说出口的不算数
	batch := add("owner", "message")     // 这一批
	later := add("owner", "message")     // 排队没轮到的更新消息
	// 带上了 s3 起的原文：s1、s2 算丢
	got := countRebuild(st, "c", conv, []int64{batch}, false, "", []history.Message{{Seq: s3}}, false)
	if got.Count != CountExact || got.Dropped != 2 || got.Kept != 1 || got.Covered != 0 {
		t.Fatalf("丢 s1、s2：%+v", got)
	}
	// 一条原文都没带上：数到这一批之前（s1、s2、s3），更新的那条不算
	got = countRebuild(st, "c", conv, []int64{batch}, false, "", nil, false)
	if got.Dropped != 3 || got.Kept != 0 {
		t.Fatalf("没带上任何原文：丢 s1、s2、s3：%+v", got)
	}
	// 快照切点在 s3：s1、s2 被摘要覆盖；s3 起带上的不算丢
	if _, err := st.Events.Exec(`INSERT INTO context_epochs (id, conversation, character_id, from_seq, summary, kind, trigger, tokens_before, tokens_after, dropped, kept, model, status, ms, created_at)
		VALUES ('e1', ?, 'c', ?, '摘要', 'summary', 'rule', 0, 0, 0, 0, 'm', 'ok', 0, 1)`, conv, s3); err != nil {
		t.Fatal(err)
	}
	got = countRebuild(st, "c", conv, []int64{batch}, true, "e1", []history.Message{{Seq: s3}}, false)
	if got.Dropped != 2 || got.Covered != 2 || !got.Snapshot {
		t.Fatalf("丢的都有摘要覆盖：%+v", got)
	}
	// 快照之后又被裁掉一条：带上的是更新的那条，s3 也丢了但没有摘要覆盖
	got = countRebuild(st, "c", conv, []int64{batch}, true, "e1", []history.Message{{Seq: later}}, false)
	if got.Dropped != 3 || got.Covered != 2 {
		t.Fatalf("快照之后裁掉的不算覆盖：%+v", got)
	}
	// 找不到这份快照、库关了：记 failed，条数记 0
	got = countRebuild(st, "c", conv, []int64{batch}, true, "没有这份", []history.Message{{Seq: s3}}, false)
	if got.Count != CountFailed || got.Dropped != 0 || got.Covered != 0 {
		t.Fatalf("找不到快照：%+v", got)
	}
	st.Close()
	got = countRebuild(st, "c", conv, []int64{batch}, false, "", []history.Message{{Seq: s3}}, false)
	if got.Count != CountFailed || got.Dropped != 0 || got.Kept != 1 {
		t.Fatalf("库关了：%+v", got)
	}
}

// 从上下文起点重建时，最后一个新话题分隔之前的话不算丢：只有裁剪兜底才让 dropped 非 0
func TestCountRebuildFromStartExcludesBeforeBreak(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const conv = "console:count"
	add := func(kind string, seg any) int64 {
		t.Helper()
		s, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "o", Origin: "owner", Kind: kind, Text: "x", Segments: seg, CreatedAt: time.Now().UnixMilli()}, []string{"c"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	add("message", nil)
	add("message", nil)
	add("notice", map[string]any{"notice": history.ContextBreak})
	kept := add("message", nil)
	batch := add("message", nil)
	// 分隔之前的两条不算丢
	got := countRebuild(st, "c", conv, []int64{batch}, false, "", []history.Message{{Seq: kept}}, true)
	if got.Dropped != 0 || got.Kept != 1 {
		t.Fatalf("新话题之前的不算丢：%+v", got)
	}
	// 分隔之后被裁掉的才算
	got = countRebuild(st, "c", conv, []int64{batch}, false, "", nil, true)
	if got.Dropped != 1 || got.Kept != 0 {
		t.Fatalf("只有分隔之后被裁掉的算丢：%+v", got)
	}
	// 同一批数据照旧口径（不是从起点重建）仍把分隔之前数进去
	got = countRebuild(st, "c", conv, []int64{batch}, false, "", []history.Message{{Seq: kept}}, false)
	if got.Dropped != 2 {
		t.Fatalf("不是从起点重建时口径不变：%+v", got)
	}
}

// 说不清有没有旧消息（数出错）的头一回：不猜 first 还是 restart，记 unknown
func TestSettleFirst(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		t    RebuildTrace
		want string
	}{
		{RebuildTrace{Count: CountExact}, RebuildFirst},
		{RebuildTrace{Count: CountExact, Kept: 1}, RebuildRestart},
		{RebuildTrace{Count: CountExact, Dropped: 3}, RebuildRestart},
		{RebuildTrace{Count: CountExact, Snapshot: true}, RebuildRestart},
		{RebuildTrace{Count: CountFailed}, RebuildUnknown},
	} {
		if got := settleFirst(&c.t); got != c.want {
			t.Errorf("%+v：%s，应是 %s", c.t, got, c.want)
		}
	}
}

// 显示层（ctlproto）的原因表和这里的固定优先级一一对应：新加原因漏了中文说法、顺序对不上都会红
func TestRebuildReasonLabelsMatch(t *testing.T) {
	t.Parallel()
	order := RebuildReasons()
	if len(order) != len(ctlproto.RebuildReasons) {
		t.Fatalf("turn 有 %d 种原因，ctlproto 的表有 %d 种", len(order), len(ctlproto.RebuildReasons))
	}
	for i, id := range order {
		if got := ctlproto.RebuildReasons[i]; got.ID != id || got.Label == "" {
			t.Errorf("第 %d 项：turn 是 %q，ctlproto 是 %+v", i, id, got)
		}
	}
}
