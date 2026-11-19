package main

// 报告 C 节（会提升的）和 D 节（受保护的）接上 W2、W3 以后的验收（规格-记忆写入上线 W6 第 8 点、W2、W3b）。
// 副本里 autoConfirm 强制打开；预期的数都是手算的：几条够次数、几条被卡住、受保护的有几条、哪些事实不能被动。
// 名字、号码、聊天内容都是编的

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/memory"
)

// seedEv 是塞进 fact_evidence 的一行
type seedEv struct {
	day, conv string
	seq       int64
	speaker   string
	self      bool
}

// seedFact 是塞进副本记忆库的一条事实（库要先 Migrate 过：confirmed_by、item_scopes、fact_evidence 是迁移加的）
type seedFact struct {
	id, kind, text, subject, status, confirmedBy, scope, conv string
	at                                                        int64
	addBy, wants                                              string // add 修订的 by；想取代谁（wants_supersede）
	oldConfirm                                                bool   // 带一条跑之前就有的 confirm 修订（以前的运行提升的）
	evidence                                                  []seedEv
}

// migrateAndSeed 先把 Go 记忆库迁移好，再放事实和各会话的游标（cursors 的键是会话 id，值是 last_seq）
func migrateAndSeed(t *testing.T, f *fixture, facts []seedFact, cursors map[string]int64) {
	t.Helper()
	if err := memory.Migrate(context.Background(), f.memDB()); err != nil {
		t.Fatal(err)
	}
	f.seedMem(func(db *sql.DB) {
		for _, s := range facts {
			var conf any
			if s.confirmedBy != "" {
				conf = s.confirmedBy
			}
			f.exec(db, `INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope, conversation, confirmed_by) VALUES (?, ?, ?, ?, 'private', '[]', ?, ?, ?, ?, ?)`,
				s.id, s.kind, s.text, s.subject, s.status, s.at, s.scope, s.conv, conf)
			f.exec(db, `INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "f:"+s.id, s.scope)
			if s.addBy != "" || s.wants != "" {
				by := s.addBy
				if by == "" {
					by = "summary"
				}
				after := "{}"
				if s.wants != "" {
					b, _ := json.Marshal(map[string]any{"status": s.status, "wants_supersede": s.wants})
					after = string(b)
				}
				f.exec(db, `INSERT INTO fact_revisions (fact_id, op, at, by, reason, after_json) VALUES (?, 'add', ?, ?, '', ?)`, s.id, s.at, by, after)
			}
			if s.oldConfirm {
				f.exec(db, `INSERT INTO fact_revisions (fact_id, op, at, by, reason, after_json) VALUES (?, 'confirm', ?, 'summary', '以前攒够的', '{}')`, s.id, s.at)
			}
			for _, e := range s.evidence {
				self := 0
				if e.self {
					self = 1
				}
				f.exec(db, `INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					s.id, e.day, e.conv, e.seq, e.speaker, self, s.at)
			}
		}
		for conv, seq := range cursors {
			f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, ?, 1000)`, conv, seq)
		}
	})
}

func (f *fixture) cfgFor(g memory.Generator, m mode) runConfig {
	return runConfig{Data: f.Data, Char: testChar, Mode: m, Gen: g, Ignore: ignoredAlways, Logf: func(string, ...any) {}}
}

func factOf(t *testing.T, ex *extracted, id string) factRow {
	t.Helper()
	for _, f := range ex.Facts {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("副本里没有事实 %s", id)
	return factRow{}
}

// 打开 autoConfirm 的那次启动先补提升一遍：导入的旧候选里攒够证据的真的提升，报告 C 节列出来（不调模型、估算模式也做）；
// 卡住的各有各的原因（次数不够、种类不能自动提升、没有本人说的、想取代的旧事实受保护）。预期的数是手算的
func TestSweepPromotesImportedCandidatesAndReportListsThem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s1 := f.ev(convOwner, "owner", "TEAQUOTE 我喜欢喝茶")
	f.ev(convOwner, "her", "好")
	g1 := f.ev(convGroupA, "qq:5", "GROUPQUOTE 他也喜欢喝茶")
	g2 := f.ev(convGroupA, "qq:6", "GROUPQUOTE2 他说的")
	last := f.ev(convOwner, "owner", "最后一句")
	three := func(first string) []seedEv {
		return []seedEv{
			{day: "2026-10-20", conv: convOwner, seq: s1, speaker: "owner", self: first == "self"},
			{day: "2026-10-21", conv: convGroupA, seq: g1, speaker: "qq:5"},
			{day: "2026-10-22", conv: convGroupA, seq: g2, speaker: "qq:6"},
		}
	}
	migrateAndSeed(t, f, []seedFact{
		{id: "P_OWNER", kind: "fact", text: "主人放行过的老事实", subject: "owner", status: "active", confirmedBy: "owner", scope: "owner", conv: convOwner, at: 1000},
		{id: "C1", kind: "fact", text: "主人喜欢喝茶", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2000, evidence: three("self")},
		{id: "C2", kind: "fact", text: "只有两次证据", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2100, evidence: three("self")[:2]},
		{id: "C3", kind: "promise", text: "承诺不自动提升", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2200, evidence: three("self")},
		{id: "C4", kind: "fact", text: "三次都是转述", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2300, evidence: three("")},
		{id: "C5", kind: "preference", text: "主人喜欢安静", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2400, evidence: three("self")},
		{id: "C7", kind: "fact", text: "想换掉主人放行过的事实", subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 2500, evidence: three("self"), wants: "P_OWNER"},
		{id: "R6", kind: "fact", text: "已经撤销的", subject: "owner", status: "revoked", scope: "owner", conv: convOwner, at: 2600, evidence: three("self")},
		{id: "AIOLD", kind: "fact", text: "以前的运行就提升了的", subject: "owner", status: "active", confirmedBy: "ai", scope: "owner", conv: convOwner, at: 2700, oldConfirm: true, evidence: three("self")},
	}, map[string]int64{convOwner: last})
	f.closeStores()

	res, err := runShadow(context.Background(), f.cfgFor(boomGen{t}, modeEstimate))
	if err != nil {
		t.Fatal(err)
	}
	// 够次数且种类能自动提升的：C1、C4、C5、C7（C2 只有两行，C3 是 promise，R6 不是待确认）；提升 C1、C5；C4 没有本人说的；C7 想换的旧事实受保护
	if res.Sweep.Checked != 4 || res.Sweep.Confirmed != 2 || res.Sweep.Blocked != 1 {
		t.Fatalf("启动补提升应该是 看 4 提 2 卡 1：%+v", res.Sweep)
	}
	ex := res.Extract
	for id, want := range map[string]string{"C1": "active", "C5": "active", "C2": "candidate", "C3": "candidate", "C4": "candidate", "C7": "candidate", "R6": "revoked", "P_OWNER": "active"} {
		if got := factOf(t, ex, id).Status; got != want {
			t.Errorf("%s 的状态是 %s，应该是 %s", id, got, want)
		}
	}
	var refs []string
	for _, c := range ex.confirmedNow() {
		refs = append(refs, c.Ref)
		if c.New || c.ConfirmedBy != "ai" || len(c.Evidence) != 3 {
			t.Errorf("%s：导入的旧候选，被 AI 提升，三行证据：%+v", c.Ref, c)
		}
	}
	if strings.Join(refs, ",") != "f:C1,f:C5" {
		t.Fatalf("这次提升的应该是 C1、C5（AIOLD 是以前的运行提升的，不算这次）：%v", refs)
	}
	// 证据原话回查到了（旧候选也回查），第三个说话人排第 3
	c1 := factOf(t, ex, "C1")
	if c1.Evidence[0].Quote != "TEAQUOTE 我喜欢喝茶" || !c1.Evidence[0].BySubject || c1.Evidence[2].Ord != 3 || c1.Evidence[2].Quote != "GROUPQUOTE2 他说的" {
		t.Errorf("C1 的证据：%+v", c1.Evidence)
	}
	if ex.ImportedCandidates != 6 || ex.ImportedInTop20 != 6 {
		t.Errorf("导入的待确认应该是 6 条（C1～C5、C7，提升走的也算一开始看得到的），都在前 20 条里：%d %d", ex.ImportedCandidates, ex.ImportedInTop20)
	}
	if len(ex.ProtectedChanged) != 0 || res.Check.Overreach != 0 || len(res.Check.ProtectedChanged) != 0 {
		t.Errorf("没有动受保护的事实，也没有越权：%v %+v", ex.ProtectedChanged, res.Check)
	}
	if res.Check.Facts != 2 {
		t.Errorf("自检要把提升的两条导入旧候选也过一遍：%d", res.Check.Facts)
	}

	// 报告：A 节有补提升的账，C 节逐条列出（标明是导入的旧候选），D 节列出卡住的取代
	rep := reportMD(res)
	for _, must := range []string{
		"启动补提升：看了 4 条范围在放行场合里的待确认，提升 2 条，1 条因为想取代的旧事实受保护没提升，另有 0 条范围不在放行的场合里、没碰",
		"### f:C1（导入的旧候选）", "### f:C5（导入的旧候选）", "提升的条目共 2 条",
		"原话 「TEAQUOTE 我喜欢喝茶」",
		"想取代：旧「主人放行过的老事实」→ 新「想换掉主人放行过的事实」（旧事实受保护",
	} {
		if !strings.Contains(rep, must) {
			t.Errorf("报告里应该有 %q", must)
		}
	}
	if strings.Contains(rep, "### f:C4") || strings.Contains(rep, "### f:C2") {
		t.Error("没提升的不该出现在 C 节的逐条列表里")
	}
	// summary.json：提升的条目只有编号、种类、范围、证据次数，没有正文和原话
	sum := string(summaryJSON(res))
	for _, must := range []string{`"promoted"`, `"f:C1"`, `"f:C5"`, `"imported"`, `"checked": 4`, `"blocked": 1`} {
		if !strings.Contains(sum, must) {
			t.Errorf("summary.json 里应该有 %s：\n%s", must, sum)
		}
	}
	for _, leak := range []string{"TEAQUOTE", "GROUPQUOTE", "主人喜欢喝茶", "喜欢安静"} {
		if strings.Contains(sum, leak) {
			t.Errorf("summary.json 里不该有正文 %q", leak)
		}
	}
}

// 启动补提升的账里有"范围不在放行场合里、没碰"的数（ConfirmSweepResult.Skipped）：-places 只有 owner-private 时，攒够证据的候选里
// 范围是群、别人的私聊、本地会话的不进事务，计在 Skipped，不计在 Checked。总览（A 节）、C 节、summary.json、运行日志里都要有这个数。
// 手算：主人范围的候选 3 条攒够（OWN1、OWN2 提升，OWN3 想取代的旧事实受保护）→ 看 3 提 2 卡 1；QQ 群、别人的 QQ 私聊、本地私聊、本地群聊各 1 条攒够
// 但范围不放行 → 没碰 4；QQ 群里另有 1 条只有两行证据，攒不够，哪个数都不算
func TestSweepSkippedOutOfPlacesShownEverywhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s1 := f.ev(convOwner, "owner", "OWNQUOTE 我喜欢喝茶")
	g1 := f.ev(convGroupA, "qq:5", "GRPQUOTE 他也喜欢喝茶")
	g2 := f.ev(convGroupA, "qq:6", "GRPQUOTE2 他说的")
	last := f.ev(convOwner, "owner", "最后一句")
	three := []seedEv{
		{day: "2026-10-20", conv: convOwner, seq: s1, speaker: "owner", self: true},
		{day: "2026-10-21", conv: convGroupA, seq: g1, speaker: "qq:5"},
		{day: "2026-10-22", conv: convGroupA, seq: g2, speaker: "qq:6"},
	}
	cand := func(id, kind, scope, conv string, ev []seedEv) seedFact {
		return seedFact{id: id, kind: kind, text: "候选 " + id, subject: "owner", status: "candidate", scope: scope, conv: conv, at: 2000, evidence: ev}
	}
	own3 := cand("OWN3", "fact", "owner", convOwner, three)
	own3.wants = "P_OWNER"
	migrateAndSeed(t, f, []seedFact{
		{id: "P_OWNER", kind: "fact", text: "主人放行过的老事实", subject: "owner", status: "active", confirmedBy: "owner", scope: "owner", conv: convOwner, at: 1000},
		cand("OWN1", "fact", "owner", convOwner, three),
		cand("OWN2", "preference", "owner", convOwner, three),
		own3,
		cand("GRP1", "fact", "conv:"+convGroupA, convGroupA, three),
		cand("PEER1", "preference", "conv:"+convPeer, convPeer, three),
		cand("LOC1", "fact", "conv:"+convLocal, convLocal, three),
		cand("LGP1", "fact", "conv:"+convLocalGp, convLocalGp, three),
		cand("GRPLOW", "fact", "conv:"+convGroupA, convGroupA, three[:2]),
	}, map[string]int64{convOwner: last})
	f.closeStores()

	cfg := f.cfgFor(boomGen{t}, modeEstimate)
	var logs []string
	cfg.Logf = func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sweep != (memory.ConfirmSweepResult{Checked: 3, Confirmed: 2, Blocked: 1, Skipped: 4}) {
		t.Fatalf("启动补提升应该是 看 3 提 2 卡 1 没碰 4：%+v", res.Sweep)
	}
	for id, want := range map[string]string{"OWN1": "active", "OWN2": "active", "OWN3": "candidate", "GRP1": "candidate", "PEER1": "candidate",
		"LOC1": "candidate", "LGP1": "candidate", "GRPLOW": "candidate"} {
		if got := factOf(t, res.Extract, id).Status; got != want {
			t.Errorf("%s 的状态是 %s，应该是 %s", id, got, want)
		}
	}

	// 没碰的数要在四个地方各自出现：总览（A 节）、C 节、summary.json、运行日志。预期的句子是手写的
	const wantSkipped = "另有 4 条范围不在放行的场合里、没碰"
	const wantChecked = "看了 3 条范围在放行场合里的待确认，提升 2 条，1 条因为想取代的旧事实受保护没提升"
	overview := estimateText(res)
	rep := reportMD(res)
	_, cSection, found := strings.Cut(rep, "## C ")
	if found {
		cSection, _, _ = strings.Cut(cSection, "\n## D ")
	}
	if !found {
		t.Fatal("报告里没有 C 节")
	}
	for name, text := range map[string]string{"总览（A 节）": overview, "C 节": cSection} {
		for _, must := range []string{wantChecked, wantSkipped} {
			if !strings.Contains(text, must) {
				t.Errorf("%s 里应该有 %q：\n%s", name, must, text)
			}
		}
	}
	var sweepLogs []string
	for _, l := range logs {
		if strings.Contains(l, "启动补提升") {
			sweepLogs = append(sweepLogs, l)
		}
	}
	if len(sweepLogs) != 1 || !strings.Contains(sweepLogs[0], wantChecked) || !strings.Contains(sweepLogs[0], wantSkipped) {
		t.Errorf("运行日志里应该有一行带两句话的启动补提升：%q", sweepLogs)
	}
	var js struct {
		Sweep map[string]int `json:"sweep"`
	}
	if err := json.Unmarshal(summaryJSON(res), &js); err != nil {
		t.Fatal(err)
	}
	if got := js.Sweep; len(got) != 4 || got["checked"] != 3 || got["confirmed"] != 2 || got["blocked"] != 1 || got["skipped"] != 4 {
		t.Errorf("summary.json 的 sweep 应该是 checked 3、confirmed 2、blocked 1、skipped 4：%v", got)
	}
}

// 导入的待确认超过 20 条：候选块只给模型最近 20 条，其余看不到、也就补不了证据。25 条一样新的导入候选，能看到的是 20 条；
// 排在前面的口径和 memory 给模型的候选块一样（新的在前，一样新的编号大的在前）
func TestImportedCandidatesBeyondTheTop20(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "owner", "一句话")
	var facts []seedFact
	for i := 0; i < 25; i++ {
		facts = append(facts, seedFact{id: fmt.Sprintf("K%02d", i), kind: "fact", text: fmt.Sprintf("导入的候选 %d", i), subject: "owner", status: "candidate", scope: "owner", conv: convOwner, at: 5000})
	}
	migrateAndSeed(t, f, facts, map[string]int64{convOwner: 99})
	f.closeStores()
	res, err := runShadow(context.Background(), f.cfgFor(boomGen{t}, modeEstimate))
	if err != nil {
		t.Fatal(err)
	}
	if res.Extract.ImportedCandidates != 25 || res.Extract.ImportedInTop20 != 20 {
		t.Errorf("25 条导入的候选，模型一开始只看得到 20 条：%d %d", res.Extract.ImportedCandidates, res.Extract.ImportedInTop20)
	}
}

// 候选块的排序口径：创建时间一样新的，编号大的在前（memory 的 visibleFacts）。22 条一样新的候选，2 条导入的编号小、20 条新写的编号大：
// 前 20 条全是新写的，导入的 2 条排在后面、模型看不到。输入里导入的排在前面，所以口径不对（按输入顺序）就会把它们数进前 20 条
func TestTop20TieBreakLikeMemory(t *testing.T) {
	t.Parallel()
	ex := &extracted{Protected: map[protectedStat]int{}}
	for _, id := range []string{"A0", "A1"} {
		ex.Facts = append(ex.Facts, factRow{ID: id, Ref: "f:" + id, Kind: "fact", Status: "candidate", Scopes: []string{"owner"}, CreatedAt: 7000})
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("Z%02d", i)
		ex.Facts = append(ex.Facts, factRow{ID: id, Ref: "f:" + id, Kind: "fact", Status: "candidate", Scopes: []string{"owner"}, CreatedAt: 7000, New: true})
	}
	classifyProtected(ex)
	if ex.ImportedCandidates != 2 || ex.ImportedInTop20 != 0 {
		t.Errorf("导入的 2 条排在后面，前 20 条里一条都没有：%d %d", ex.ImportedCandidates, ex.ImportedInTop20)
	}
}

// 整理过程中攒够证据：一条候选在三天里各收到一次本人说的证据，第三批提升；副本里 autoConfirm 是强制打开的（正式配置里它默认关），
// 所以 would_confirm（关着时才数）是 0，confirmed 是 1，C 节标明是这次新写的
func TestConfirmDuringTheRunListedInReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	day := func(d int) int64 { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local).UnixMilli() }
	long := strings.Repeat("茶", 3500) // 一条就占满一批（一批 6000 字），三条正好三批
	for i, d := range []int{20, 21, 22} {
		f.at = day(d)
		f.ev(convOwner, "owner", fmt.Sprintf("DAY%d 我最喜欢喝茶了 %s", i+1, long))
	}
	migrateAndSeed(t, f, nil, map[string]int64{convOwner: 0})
	f.closeStores()

	candLine := regexp.MustCompile(`(?m)^([0-9A-Za-z]+)｜fact｜主人喜欢喝茶`)
	g := &recGen{reply: func(prompt string) string {
		seq := firstSeqOf(prompt)
		if m := candLine.FindStringSubmatch(prompt); m != nil {
			return fmt.Sprintf(`{"episode_record":"no","episode_reason":"x","ops":[{"op":"evidence","id":%q,"src":[%d]}]}`, m[1], seq)
		}
		return fmt.Sprintf(`{"episode_record":"no","episode_reason":"x","ops":[{"op":"candidate","kind":"fact","subject":"owner","src":[%d],"text":"主人喜欢喝茶"}]}`, seq)
	}}
	res, err := runShadow(context.Background(), f.cfgFor(g, modeFake))
	if err != nil {
		t.Fatal(err)
	}
	if g.count() != 3 || len(res.Batches) != 3 {
		t.Fatalf("应该三批三次调用：%d %d", g.count(), len(res.Batches))
	}
	if res.Counts["confirmed"] != 1 || res.Counts["would_confirm"] != 0 || res.Sweep.Confirmed != 0 {
		t.Fatalf("第三批提升 1 条，would_confirm 是 0，启动补提升没有：%v %+v", res.Counts, res.Sweep)
	}
	got := res.Extract.confirmedNow()
	if len(got) != 1 || !got[0].New || got[0].Status != "active" || got[0].ConfirmedBy != "ai" || len(got[0].Evidence) != 3 {
		t.Fatalf("应该提升了这条新写的候选，三行证据：%+v", got)
	}
	self := 0
	for _, e := range got[0].Evidence {
		if e.BySubject {
			self++
		}
	}
	if self != 3 {
		t.Errorf("三次都是本人说的：%d", self)
	}
	rep := reportMD(res)
	if !strings.Contains(rep, "（这次新写的）") || !strings.Contains(rep, "整理过程中：confirmed（真提升）计数 1") || !strings.Contains(rep, "would_confirm（autoConfirm 关着时数的") {
		t.Errorf("C 节该写明这次新写的、整理中提升 1 条：\n%s", rep)
	}
	if text := estimateText(res); !strings.Contains(text, "autoConfirm：副本里强制打开") {
		t.Errorf("总览里要写明副本里 autoConfirm 是强制打开的：\n%s", text)
	}
}

// 受保护的有效事实（导入的、主人放行过的、奇怪的值）整理动不了：模型对每一条都写了撤销，还写了一条想取代它们的候选，
// 只有 AI 自己提升的那条被撤销了。受保护统计按来源 × 主体数对，ProtectedChanged 是空的；D 节列出"想取代"的一对和原因
func TestProtectedFactsSurviveRevokeAndSupersedeAttempts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o1 := f.ev(convOwner, "owner", "那些都不对，全都撤掉，换成新的说法")
	f.ev(convOwner, "her", "好")
	migrateAndSeed(t, f, []seedFact{
		{id: "BUN1", kind: "fact", text: "导入的老事实", subject: "owner", status: "active", scope: "owner", conv: convOwner, at: 100, addBy: "summary"},
		{id: "OWN1", kind: "fact", text: "主人放行过的事实", subject: "owner", status: "active", confirmedBy: "owner", scope: "owner", conv: convOwner, at: 200},
		{id: "ODD1", kind: "fact", text: "confirmed_by 是不认识的值", subject: "owner", status: "active", confirmedBy: "AI ", scope: "owner", conv: convOwner, at: 300},
		{id: "OTH1", kind: "fact", text: "导入的、讲别人的", subject: "qq:5", status: "active", scope: "owner", conv: convOwner, at: 400},
		{id: "AI1", kind: "fact", text: "AI 自己提升的", subject: "owner", status: "active", confirmedBy: "ai", scope: "owner", conv: convOwner, at: 500},
	}, map[string]int64{convOwner: 0})
	f.closeStores()

	g := &recGen{reply: func(string) string {
		var ops []string
		for _, id := range []string{"BUN1", "OWN1", "ODD1", "OTH1", "AI1"} {
			ops = append(ops, fmt.Sprintf(`{"op":"revoke","id":%q,"src":[%d]}`, id, o1))
		}
		ops = append(ops, fmt.Sprintf(`{"op":"upsert_fact","kind":"fact","subject":"owner","src":[%d],"text":"新的说法","supersedes":"OWN1"}`, o1))
		return `{"episode_record":"no","episode_reason":"x","ops":[` + strings.Join(ops, ",") + `]}`
	}}
	res, err := runShadow(context.Background(), f.cfgFor(g, modeFake))
	if err != nil {
		t.Fatal(err)
	}
	ex := res.Extract
	for id, want := range map[string]string{"BUN1": "active", "OWN1": "active", "ODD1": "active", "OTH1": "active", "AI1": "revoked"} {
		if got := factOf(t, ex, id).Status; got != want {
			t.Errorf("%s 的状态是 %s，应该是 %s", id, got, want)
		}
	}
	if res.Counts["revoke_protected"] != 4 || res.Counts["revoked"] != 1 || res.Counts["supersede_deferred"] != 1 {
		t.Errorf("撤销被挡 4、撤了 1、取代延后 1：%v", res.Counts)
	}
	if len(ex.ProtectedChanged) != 0 {
		t.Errorf("受保护的事实一条也没被动：%v", ex.ProtectedChanged)
	}
	// 受保护统计（手算）：BUN1 来源是 Bun 整理写的、主体主人；OWN1 你放行的、主人；ODD1 其余、主人；OTH1 其余、主体是别人；
	// 夹具里本来就有的 old1（Bun 形状的库里放的标记事实）也是导入时就有效的、其余、主人；AI1 是 ai 的，不受保护
	want := map[protectedStat]int{
		{srcBunSummary, subjOwner}: 1, {srcOwnerOK, subjOwner}: 1, {srcOther, subjOwner}: 2, {srcOther, subjOther}: 1,
	}
	for k, n := range want {
		if ex.Protected[k] != n {
			t.Errorf("受保护统计 %v 应该是 %d：%d", k, n, ex.Protected[k])
		}
	}
	total := 0
	for _, n := range ex.Protected {
		total += n
	}
	if total != 5 {
		t.Errorf("受保护共 5 条（BUN1、OWN1、ODD1、OTH1 和 old1）：%d（%v）", total, ex.Protected)
	}
	rep := reportMD(res)
	for _, must := range []string{"revoke_protected", "想取代：旧「主人放行过的事实」→ 新「新的说法」（旧事实受保护", "| Bun 整理写的 | 1 | 0 | 0 |", "| 你放行的 | 1 | 0 | 0 |", "| 其余（导入时就有效的） | 2 | 0 | 1 |"} {
		if !strings.Contains(rep, must) {
			t.Errorf("报告里应该有 %q：\n%s", must, rep)
		}
	}
	if !strings.Contains(rep, "受保护的有效事实被整理动了：0") {
		t.Error("自检里要写受保护的被动了 0 条")
	}
}

// 工具自己的第二层检查：跑之前受保护的事实，跑完状态或 confirmed_by 变了就是被动了。这里在整理结束后往副本里塞一个"受保护事实被动了"
// 的结局，检查要红；W2 的保证是整理做不到这件事，但检查本身不能只在"没发生"时才绿
func TestProtectedChangedIsDetected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sql string
	}{
		{"状态变成已撤销", `UPDATE facts SET status = 'revoked' WHERE id = 'BUN1'`},
		{"被取代", `UPDATE facts SET status = 'superseded', superseded_by = 'ZZ' WHERE id = 'BUN1'`},
		{"confirmed_by 被改成 ai", `UPDATE facts SET confirmed_by = 'ai' WHERE id = 'BUN1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.ev(convOwner, "owner", "一句话")
			migrateAndSeed(t, f, []seedFact{
				{id: "BUN1", kind: "fact", text: "导入的老事实", subject: "owner", status: "active", scope: "owner", conv: convOwner, at: 100},
				{id: "AI1", kind: "fact", text: "AI 提升的", subject: "owner", status: "active", confirmedBy: "ai", scope: "owner", conv: convOwner, at: 200},
			}, map[string]int64{convOwner: 0})
			f.closeStores()
			cfg := f.cfgFor(&recGen{}, modeFake)
			cfg.hookBeforeExtract = func(memFile string) {
				db := f.openMem(memFile)
				defer db.Close()
				f.exec(db, tc.sql)
				f.exec(db, `UPDATE facts SET status = 'revoked' WHERE id = 'AI1'`) // AI 自己提升的被撤销不算
			}
			res, err := runShadow(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(res.Check.ProtectedChanged, ","); got != "f:BUN1" {
				t.Fatalf("应该只报 BUN1 被动了：%q", got)
			}
			if !strings.Contains(reportMD(res), "受保护的有效事实被整理动了：1") || !strings.Contains(string(summaryJSON(res)), `"f:BUN1"`) {
				t.Error("报告和 summary.json 里要写出来")
			}
		})
	}
}
