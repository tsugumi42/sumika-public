package memory

import (
	"database/sql"
	"testing"
)

// ADR-231：后台整理写事实（upsert_fact、candidate）时，只有 src 恰好引用一行、并且那一行是当事人说的，才算"本人说的"（BySubject）。
// 两行以上一律不算，哪怕每一行都是当事人说的；同一个序号写两遍也是两行。多行时证据取第一句人话，本人那一行不给整条背书。
// 预期全是手写的，不调 source() 算；名字、号码、聊天内容都是编的

var (
	// 名字里夹了零宽字符的群友：身份看 ActorKind 和 ActorID，不看名字
	zeroWidthA = actor{"human", "qq:60088", "主\u200b人", "peer"}
	// 号写成 owner、但类型是真人的群友（现实里群友的号都是 qq:…，这里只是敌对输入）
	idOwnerA = actor{"human", "owner", "主人", "peer"}
	// 名片抄了阿萍的陌生人（号对不上）
	copyPingA = actor{"human", "qq:60099", "阿萍", "peer"}
	// 前两天没说过话的群友
	dingA = actor{"human", "qq:60003", "阿丁", "peer"}
)

// 每一行是一种 src 的形状 × 场合；预期是库里写下的样子
func TestBySubjectNeedsExactlyOneSubjectLine(t *testing.T) {
	t.Parallel()
	intended(t, "summary-by-subject-exactly-one-line")
	const g = groupConv
	grp := []actor{pingA, ownerA, boA, herA, spoofA, zeroWidthA, ownerA, idOwnerA, copyPingA} // 第 i 个是 seq i+1
	loc := []actor{ownerA, herA, ownerA}
	locGrp := []actor{ownerA, otherCh, ownerA}
	qqOwner := []actor{ownerA, herA, ownerA}
	qqOther := []actor{pingA, pingA, herA}

	type tc struct {
		name    string
		conv    string
		lines   []actor
		subject string
		upsert  bool // upsert_fact（visibility 写 public，不收紧范围）；否则 candidate
		src     []int64

		by         int    // 证据行里算本人说的有几行
		evidence   int    // 证据行数
		attributed string // attributed_to
		sources    string // source_seqs
		origin     string // 证据那一句的 origin
		scopes     string
		count      string // 应该数到一次的丢弃计数；"" 不查
	}
	cases := []tc{
		// ── QQ 群 · 主人的事（范围 conv:群 + owner）
		{name: "群·主人的事·一行当事人", conv: g, lines: grp, subject: "owner", src: []int64{2}, by: 1, evidence: 1, sources: "[2]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·一行当事人（upsert_fact）", conv: g, lines: grp, subject: "owner", upsert: true, src: []int64{2}, by: 1, evidence: 1, sources: "[2]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·一行别人", conv: g, lines: grp, subject: "owner", src: []int64{1}, evidence: 1, attributed: "qq:60001", sources: "[1]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·一行是她自己", conv: g, lines: grp, subject: "owner", src: []int64{4}, sources: "[4]", scopes: "conv:" + g + ",owner", count: CountSrcNoEvidence},
		{name: "群·主人的事·src 空", conv: g, lines: grp, subject: "owner", sources: "[]", scopes: "conv:" + g + ",owner", count: CountSrcMissing},
		{name: "群·主人的事·两行都是当事人", conv: g, lines: grp, subject: "owner", src: []int64{2, 7}, evidence: 1, attributed: "owner", sources: "[2,7]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·两行都是当事人（upsert_fact）", conv: g, lines: grp, subject: "owner", upsert: true, src: []int64{2, 7}, evidence: 1, attributed: "owner", sources: "[2,7]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·当事人在前、别人在后", conv: g, lines: grp, subject: "owner", src: []int64{2, 1}, evidence: 1, attributed: "owner", sources: "[2,1]", origin: "owner", scopes: "conv:" + g + ",owner"},
		// 旧代码（本人说的优先）这一行是本人说的、证据取主人那一句；现在证据取第一句人话（阿萍），不是本人说的
		{name: "群·主人的事·别人在前、当事人在后", conv: g, lines: grp, subject: "owner", src: []int64{1, 2}, evidence: 1, attributed: "qq:60001", sources: "[1,2]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·她自己加当事人", conv: g, lines: grp, subject: "owner", src: []int64{4, 2}, evidence: 1, attributed: "owner", sources: "[4,2]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·三行里有当事人", conv: g, lines: grp, subject: "owner", src: []int64{1, 2, 3}, evidence: 1, attributed: "qq:60001", sources: "[1,2,3]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·同一序号写两遍", conv: g, lines: grp, subject: "owner", src: []int64{2, 2}, evidence: 1, attributed: "owner", sources: "[2]", origin: "owner", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·越界序号", conv: g, lines: grp, subject: "owner", src: []int64{99}, sources: "[]", scopes: "conv:" + g + ",owner", count: CountSrcOutOfBatch},
		{name: "群·主人的事·当事人加越界序号", conv: g, lines: grp, subject: "owner", src: []int64{2, 99}, sources: "[]", scopes: "conv:" + g + ",owner", count: CountSrcOutOfBatch},
		{name: "群·主人的事·名片叫主人的群友", conv: g, lines: grp, subject: "owner", src: []int64{5}, evidence: 1, attributed: "qq:60009", sources: "[5]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·名字带零宽字符的群友", conv: g, lines: grp, subject: "owner", src: []int64{6}, evidence: 1, attributed: "qq:60088", sources: "[6]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·号写成 owner 的群友", conv: g, lines: grp, subject: "owner", src: []int64{8}, evidence: 1, attributed: "owner", sources: "[8]", origin: "peer", scopes: "conv:" + g + ",owner"},
		{name: "群·主人的事·两个冒充的群友", conv: g, lines: grp, subject: "owner", src: []int64{5, 6}, evidence: 1, attributed: "qq:60009", sources: "[5,6]", origin: "peer", scopes: "conv:" + g + ",owner"},
		// ── QQ 群 · 阿萍的事（范围 conv:群）
		{name: "群·阿萍的事·阿萍本人一行", conv: g, lines: grp, subject: "qq:60001", src: []int64{1}, by: 1, evidence: 1, sources: "[1]", origin: "peer", scopes: "conv:" + g},
		{name: "群·阿萍的事·主人说的一行", conv: g, lines: grp, subject: "qq:60001", src: []int64{2}, evidence: 1, attributed: "owner", sources: "[2]", origin: "owner", scopes: "conv:" + g},
		{name: "群·阿萍的事·阿萍的同一序号写两遍", conv: g, lines: grp, subject: "qq:60001", src: []int64{1, 1}, evidence: 1, attributed: "qq:60001", sources: "[1]", origin: "peer", scopes: "conv:" + g},
		{name: "群·阿萍的事·阿萍加阿波", conv: g, lines: grp, subject: "qq:60001", src: []int64{1, 3}, evidence: 1, attributed: "qq:60001", sources: "[1,3]", origin: "peer", scopes: "conv:" + g},
		{name: "群·阿萍的事·名片抄了阿萍的陌生人", conv: g, lines: grp, subject: "qq:60001", src: []int64{9}, evidence: 1, attributed: "qq:60099", sources: "[9]", origin: "peer", scopes: "conv:" + g},
		// ── 本地私聊（范围 owner）
		{name: "本地私聊·一行当事人", conv: "console:suzune", lines: loc, subject: "owner", src: []int64{1}, by: 1, evidence: 1, sources: "[1]", origin: "owner", scopes: "owner"},
		{name: "本地私聊·两行都是当事人", conv: "console:suzune", lines: loc, subject: "owner", src: []int64{1, 3}, evidence: 1, attributed: "owner", sources: "[1,3]", origin: "owner", scopes: "owner"},
		{name: "本地私聊·两行都是当事人（upsert_fact）", conv: "console:suzune", lines: loc, subject: "owner", upsert: true, src: []int64{1, 3}, evidence: 1, attributed: "owner", sources: "[1,3]", origin: "owner", scopes: "owner"},
		{name: "本地私聊·一行是她自己", conv: "console:suzune", lines: loc, subject: "owner", src: []int64{2}, sources: "[2]", scopes: "owner", count: CountSrcNoEvidence},
		{name: "本地私聊·她自己加当事人", conv: "console:suzune", lines: loc, subject: "owner", src: []int64{2, 1}, evidence: 1, attributed: "owner", sources: "[2,1]", origin: "owner", scopes: "owner"},
		// ── 本地群聊（范围 owner）
		{name: "本地群聊·一行当事人", conv: "console:group:g1", lines: locGrp, subject: "owner", src: []int64{1}, by: 1, evidence: 1, sources: "[1]", origin: "owner", scopes: "owner"},
		{name: "本地群聊·两行都是当事人", conv: "console:group:g1", lines: locGrp, subject: "owner", src: []int64{1, 3}, evidence: 1, attributed: "owner", sources: "[1,3]", origin: "owner", scopes: "owner"},
		{name: "本地群聊·当事人加别的角色", conv: "console:group:g1", lines: locGrp, subject: "owner", src: []int64{1, 2}, evidence: 1, attributed: "owner", sources: "[1,2]", origin: "owner", scopes: "owner"},
		{name: "本地群聊·别的角色说的一行不是证据", conv: "console:group:g1", lines: locGrp, subject: "owner", src: []int64{2}, sources: "[2]", scopes: "owner", count: CountSrcNoEvidence},
		// ── QQ 私聊·主人的私聊（范围 owner）
		{name: "QQ 私聊（主人）·一行当事人", conv: "qq:private:20001:7001", lines: qqOwner, subject: "owner", src: []int64{1}, by: 1, evidence: 1, sources: "[1]", origin: "owner", scopes: "owner"},
		{name: "QQ 私聊（主人）·两行都是当事人", conv: "qq:private:20001:7001", lines: qqOwner, subject: "owner", src: []int64{1, 3}, evidence: 1, attributed: "owner", sources: "[1,3]", origin: "owner", scopes: "owner"},
		{name: "QQ 私聊（主人）·同一序号写两遍", conv: "qq:private:20001:7001", lines: qqOwner, subject: "owner", src: []int64{3, 3}, evidence: 1, attributed: "owner", sources: "[3]", origin: "owner", scopes: "owner"},
		// ── QQ 私聊·某人的私聊（对方的事范围 person:他，主人的事范围 owner）
		{name: "QQ 私聊（某人）·对方本人一行", conv: "qq:private:20001:7002", lines: qqOther, subject: "qq:60001", src: []int64{1}, by: 1, evidence: 1, sources: "[1]", origin: "peer", scopes: "person:qq:60001"},
		{name: "QQ 私聊（某人）·对方本人两行", conv: "qq:private:20001:7002", lines: qqOther, subject: "qq:60001", src: []int64{1, 2}, evidence: 1, attributed: "qq:60001", sources: "[1,2]", origin: "peer", scopes: "person:qq:60001"},
		{name: "QQ 私聊（某人）·对方说的主人的事", conv: "qq:private:20001:7002", lines: qqOther, subject: "owner", src: []int64{1}, evidence: 1, attributed: "qq:60001", sources: "[1]", origin: "peer", scopes: "owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, c.conv)
			for _, a := range c.lines {
				r.say(a, "说了一句话。")
			}
			const text = "要写下的事"
			op := cand(text, c.subject, c.src...)
			if c.upsert {
				op = upsert(text, c.subject, "public", c.src...)
			}
			r.reply("no", "", op)
			res := r.run()
			f := r.fact(text)
			if f.status != "candidate" {
				t.Errorf("整理写的一律待确认：%s", f.status)
			}
			if f.byself != c.by || f.evidence != c.evidence || f.attributed != c.attributed || f.sources != c.sources || f.origin != c.origin || f.scopes != c.scopes {
				t.Errorf("库里是 本人 %d / 证据 %d / 归属 %q / 来源 %s / origin %q / 范围 %q\n应该是 本人 %d / 证据 %d / 归属 %q / 来源 %s / origin %q / 范围 %q",
					f.byself, f.evidence, f.attributed, f.sources, f.origin, f.scopes,
					c.by, c.evidence, c.attributed, c.sources, c.origin, c.scopes)
			}
			if c.count != "" && res.Counts[c.count] != 1 {
				t.Errorf("计数 %s=%d，应该是 1：%v", c.count, res.Counts[c.count], res.Counts)
			}
		})
	}
}

// seedBySubjectRig：库里先有一条同文的候选 CAND（范围只有 scope），再把 lines 放进会话；others 为真时再放两行不同群友转述的证据、
// 打开自动提升（说话人是 qq:60011、qq:60012，不是任何一个 lines 里的人，次数够 3 就只差"至少一次本人说"）
func seedBySubjectRig(t *testing.T, conv string, lines []actor, subject, scope string, others bool) *rig {
	t.Helper()
	opts := rigOpts{seed: func(db *sql.DB) { seedCand(t, db, "CAND", bsText, subject, scope, 1) }}
	if others {
		opts.mod = autoConfirm
	}
	r := newRig(t, conv, opts)
	if others {
		if _, err := r.m.w.db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES
			('CAND', '2026-10-20', ?, 1, 'qq:60011', 0, 1), ('CAND', '2026-10-21', ?, 2, 'qq:60012', 0, 1)`, conv, conv); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range lines {
		r.say(a, "说了一句话。")
	}
	return r
}

const bsText = "要合并的事"

// 同文合并：本人说的把新范围并进已有的那条，不是本人说的范围不动（write.go 的 merge、mayRecord）。
// 库里已有一条同文的候选（范围比这一批要写的窄），这一批整理又写了同一句话
func TestMultiLineSrcDoesNotWidenAnExistingFactsScope(t *testing.T) {
	t.Parallel()
	intended(t, "summary-by-subject-exactly-one-line")
	const g = groupConv
	// 群里：seq 1、2 主人，3 阿萍，4 名片叫主人的群友，5 她自己，6 名字带零宽字符的群友
	grp := []actor{ownerA, ownerA, pingA, spoofA, herA, zeroWidthA}
	type tc struct {
		name     string
		conv     string
		lines    []actor
		subject  string
		scope    string // 库里那条候选原来的范围
		src      []int64
		widened  int    // scopes_widened
		kept     int    // scopes_kept
		scopes   string // 合并以后候选的范围
		evidence int    // 记下的证据行数
		by       int    // 其中本人说的
		speaker  string // 那一行证据的说话人
	}
	own := "conv:" + g
	cases := []tc{
		{"群·一行当事人：范围并上", g, grp, "owner", own, []int64{1}, 1, 0, own + ",owner", 1, 1, "owner"},
		{"群·两行都是当事人：范围不动", g, grp, "owner", own, []int64{1, 2}, 0, 1, own, 1, 0, "owner"},
		{"群·同一序号写两遍：范围不动", g, grp, "owner", own, []int64{1, 1}, 0, 1, own, 1, 0, "owner"},
		{"群·当事人在前、别人在后：范围不动", g, grp, "owner", own, []int64{1, 3}, 0, 1, own, 1, 0, "owner"},
		{"群·别人在前、当事人在后：范围不动", g, grp, "owner", own, []int64{3, 1}, 0, 1, own, 1, 0, "qq:60001"},
		{"群·一行别人：范围不动", g, grp, "owner", own, []int64{3}, 0, 1, own, 1, 0, "qq:60001"},
		{"群·名片叫主人的群友：范围不动", g, grp, "owner", own, []int64{4}, 0, 1, own, 1, 0, "qq:60009"},
		{"群·名字带零宽字符的群友：范围不动", g, grp, "owner", own, []int64{6}, 0, 1, own, 1, 0, "qq:60088"},
		// 没有证据的 src：不记证据，范围更不动
		{"群·一行是她自己：没有证据", g, grp, "owner", own, []int64{5}, 0, 0, own, 0, 0, ""},
		{"群·越界序号：没有证据", g, grp, "owner", own, []int64{99}, 0, 0, own, 0, 0, ""},
		{"群·没有 src：没有证据", g, grp, "owner", own, nil, 0, 0, own, 0, 0, ""},
		// 别的场合：本地私聊、本地群聊、QQ 私聊（主人）里主人的事的范围只有 owner，候选本来就带着它，没有可并的，这三格只能验证据行的本人说；
		// 某人的私聊里对方说主人的事，本来就不是本人说的；真能并范围的另一条路是某人的私聊里对方本人说自己的事（候选原来只在这个会话，并上 person:他）
		{"本地私聊·两行都是当事人", "console:suzune", []actor{ownerA, ownerA}, "owner", "owner", []int64{1, 2}, 0, 0, "owner", 1, 0, "owner"},
		{"本地群聊·两行都是当事人", "console:group:g1", []actor{ownerA, ownerA, otherCh}, "owner", "owner", []int64{1, 2}, 0, 0, "owner", 1, 0, "owner"},
		{"QQ 私聊（主人）·两行都是当事人", "qq:private:20001:7001", []actor{ownerA, ownerA}, "owner", "owner", []int64{1, 2}, 0, 0, "owner", 1, 0, "owner"},
		{"QQ 私聊（某人）·对方说主人的事：范围不并到 owner", "qq:private:20001:7002", []actor{pingA, pingA}, "owner", "conv:qq:private:20001:7002", []int64{1}, 0, 1, "conv:qq:private:20001:7002", 1, 0, "qq:60001"},
		{"QQ 私聊（某人）·对方说主人的事（两行）：范围不并到 owner", "qq:private:20001:7002", []actor{pingA, pingA}, "owner", "conv:qq:private:20001:7002", []int64{1, 2}, 0, 1, "conv:qq:private:20001:7002", 1, 0, "qq:60001"},
		{"QQ 私聊（某人）·对方本人一行说自己的事：范围并上", "qq:private:20001:7002", []actor{pingA, pingA}, "qq:60001", "conv:qq:private:20001:7002", []int64{1}, 1, 0, "conv:qq:private:20001:7002,person:qq:60001", 1, 1, "qq:60001"},
		{"QQ 私聊（某人）·对方本人两行说自己的事：范围不动", "qq:private:20001:7002", []actor{pingA, pingA}, "qq:60001", "conv:qq:private:20001:7002", []int64{1, 2}, 0, 1, "conv:qq:private:20001:7002", 1, 0, "qq:60001"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := seedBySubjectRig(t, c.conv, c.lines, c.subject, c.scope, false)
			r.reply("no", "", cand(bsText, c.subject, c.src...))
			res := r.run()
			if got := res.Counts[CountScopesWidened]; got != c.widened {
				t.Errorf("scopes_widened=%d，应该是 %d：%v", got, c.widened, res.Counts)
			}
			if got := res.Counts[CountScopesKept]; got != c.kept {
				t.Errorf("scopes_kept=%d，应该是 %d：%v", got, c.kept, res.Counts)
			}
			if got := r.scopesOf("f:CAND"); got != c.scopes {
				t.Errorf("候选的范围 %q，应该是 %q", got, c.scopes)
			}
			if n := count(t, r.m, `SELECT count(*) FROM facts`); n != 1 {
				t.Errorf("同文合并，库里该只有一条：%d", n)
			}
			ev := r.evidenceOf("CAND")
			if len(ev) != c.evidence {
				t.Fatalf("证据 %+v，应该 %d 行", ev, c.evidence)
			}
			if c.evidence == 1 && (ev[0].by != c.by || ev[0].speaker != c.speaker) {
				t.Errorf("证据 %+v，应该是（本人 %d、说话人 %s）", ev[0], c.by, c.speaker)
			}
		})
	}
}

// 提升的票：本人说的至少一次才够（write_confirm.go 的 tally）。候选已经有两个不同群友转述的证据，
// 第三次来自这一批的 src：一行当事人是本人说的 → 提升；两行（哪怕都是当事人）是转述，次数够了也不提升
func TestMultiLineSrcIsNotASelfVoteForPromotion(t *testing.T) {
	t.Parallel()
	intended(t, "summary-by-subject-exactly-one-line")
	const g = groupConv
	// 群里：seq 1、2 主人，3 前两天没转述过的群友，4 名片叫主人的群友，5 她自己，6 名字带零宽字符的群友
	grp := []actor{ownerA, ownerA, dingA, spoofA, herA, zeroWidthA}
	type tc struct {
		name     string
		conv     string
		lines    []actor
		subject  string
		scope    string
		src      []int64
		promoted bool
		rows     int // 证据行数：转述的也要真的记上，不提升是因为没有本人说的，不是因为被挡在门外
	}
	own := "conv:" + g
	cases := []tc{
		{"群·一行当事人：提升（对照）", g, grp, "owner", own, []int64{1}, true, 3},
		{"群·两行都是当事人", g, grp, "owner", own, []int64{1, 2}, false, 3},
		{"群·同一序号写两遍", g, grp, "owner", own, []int64{1, 1}, false, 3},
		{"群·当事人在前、别人在后", g, grp, "owner", own, []int64{1, 3}, false, 3},
		{"群·别人在前、当事人在后", g, grp, "owner", own, []int64{3, 1}, false, 3},
		{"群·一行别人", g, grp, "owner", own, []int64{3}, false, 3},
		{"群·名片叫主人的群友", g, grp, "owner", own, []int64{4}, false, 3},
		{"群·名字带零宽字符的群友", g, grp, "owner", own, []int64{6}, false, 3},
		{"群·一行是她自己：没有证据", g, grp, "owner", own, []int64{5}, false, 2},
		{"群·越界序号：没有证据", g, grp, "owner", own, []int64{99}, false, 2},
		{"群·没有 src：没有证据", g, grp, "owner", own, nil, false, 2},
		{"本地私聊·一行当事人：提升（对照）", "console:suzune", []actor{ownerA, ownerA}, "owner", "owner", []int64{1}, true, 3},
		{"本地私聊·两行都是当事人", "console:suzune", []actor{ownerA, ownerA}, "owner", "owner", []int64{1, 2}, false, 3},
		{"本地群聊·一行当事人：提升（对照）", "console:group:g1", []actor{ownerA, ownerA, otherCh}, "owner", "owner", []int64{1}, true, 3},
		{"本地群聊·两行都是当事人", "console:group:g1", []actor{ownerA, ownerA, otherCh}, "owner", "owner", []int64{1, 2}, false, 3},
		{"QQ 私聊（主人）·一行当事人：提升（对照）", "qq:private:20001:7001", []actor{ownerA, ownerA}, "owner", "owner", []int64{1}, true, 3},
		{"QQ 私聊（主人）·两行都是当事人", "qq:private:20001:7001", []actor{ownerA, ownerA}, "owner", "owner", []int64{1, 2}, false, 3},
		{"QQ 私聊（某人）·对方本人一行：提升（对照）", "qq:private:20001:7002", []actor{pingA, pingA}, "qq:60001", "person:qq:60001", []int64{1}, true, 3},
		{"QQ 私聊（某人）·对方本人两行", "qq:private:20001:7002", []actor{pingA, pingA}, "qq:60001", "person:qq:60001", []int64{1, 2}, false, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := seedBySubjectRig(t, c.conv, c.lines, c.subject, c.scope, true)
			r.reply("no", "", cand(bsText, c.subject, c.src...))
			res := r.run()
			status := str(t, r.m, `SELECT status FROM facts WHERE id = 'CAND'`)
			if (status == "active") != c.promoted || (res.Counts[CountConfirmed] == 1) != c.promoted {
				t.Errorf("状态 %s、confirmed=%d，提升了应该是 %v：%v", status, res.Counts[CountConfirmed], c.promoted, res.Counts)
			}
			if n := len(r.evidenceOf("CAND")); n != c.rows {
				t.Errorf("证据 %d 行，应该是 %d：%v", n, c.rows, res.Counts)
			}
		})
	}
}
