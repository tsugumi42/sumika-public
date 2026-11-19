package memory

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/prompts"
)

// evidence 动作从模型的回复一路走到 fact_evidence（W3a）：解析、程序的检查、写入层。名字、号码、聊天内容都是编的

const evGroup = "qq:group:5001001"

var (
	// 她的号上发出、但不是她说的（origin 是 external）：和 herAcct（human）一样不是证据
	herExternal = actor{"character", "suzune", "铃音", "external"}
	autoA       = actor{"character", "suzune", "铃音", "automation:remind"}
	// 名片叫"主人"的群友：身份看 ActorKind 和 Origin，不看名字
	spoofA = actor{"human", "qq:60009", "主人", "peer"}
)

// evScene 是一个群聊场景：各条消息的 seq，和库里预先放好的几条事实
type evScene struct {
	r                                       *rig
	peer, her, herHuman, herExt, other, own int64 // 各条消息的 seq
	backfill, selftest, auto                int64
}

// newEvScene：QQ 群里的一批对话，同时库里有一条本群的候选 CGROUP（阿萍自己的事）、一条本群的有效事实 FKNOWN、一条别的群的候选 COTHER
func newEvScene(t *testing.T) *evScene {
	t.Helper()
	r := newRig(t, evGroup, rigOpts{seed: func(db *sql.DB) {
		seedCand(t, db, "CGROUP", "阿萍下周搬家", "qq:60001", "conv:"+evGroup, 1)
		seedFact(t, db, "FKNOWN", "阿波喜欢猫", "conv:"+evGroup, 2)
		seedCand(t, db, "COTHER", "别的群的听说", "qq:60001", "conv:qq:group:5002002", 3)
	}})
	s := &evScene{r: r}
	s.peer = r.say(pingA, "我下周搬家，累死了。").Seq
	s.her = r.say(herA, "搬去哪里？").Seq
	s.herHuman = r.say(herAcct, "（主人用她的号发的）").Seq
	s.herExt = r.say(herExternal, "（外部发的）").Seq
	s.other = r.say(otherCh, "我也听说了。").Seq
	s.own = r.say(ownerA, "对，阿萍要搬家。").Seq
	s.backfill = r.say(pingA, "补录的一句", func(e *Event) { e.Backfill = true }).Seq
	s.selftest = r.say(pingA, "自检的一句", func(e *Event) { e.Selftest = true }).Seq
	s.auto = r.say(autoA, "提醒").Seq
	return s
}

// 一条 evidence 动作的各种坏法：都丢并计数、库里一个字节不动；好的写一行，行里的会话、时间、说话人、本人说都是程序按那一行算的
func TestEvidenceOpValidation(t *testing.T) {
	t.Parallel()
	intended(t, "summary-evidence-op")
	type tc struct {
		name   string
		op     func(s *evScene) string
		count  string // 应该数到的计数（各 1）
		rows   int    // CGROUP 的证据行数
		bySubj int    // 其中本人说的
	}
	good := func(seq func(s *evScene) int64) func(s *evScene) string {
		return func(s *evScene) string { return evidenceJSON("CGROUP", seq(s)) }
	}
	for _, c := range []tc{
		{"阿萍自己再说一遍：记一次，本人说的", good(func(s *evScene) int64 { return s.peer }), CountOpEvidence, 1, 1},
		{"主人说了一遍：记一次，不是本人说的（这条是阿萍的事）", good(func(s *evScene) int64 { return s.own }), CountOpEvidence, 1, 0},
		{"编的编号", func(s *evScene) string { return evidenceJSON("NOSUCH", s.peer) }, CountEvidenceUnknown, 0, 0},
		{"这个场合看得见的有效事实的编号", func(s *evScene) string { return evidenceJSON("FKNOWN", s.peer) }, CountEvidenceUnknown, 0, 0},
		{"没写编号", func(s *evScene) string { return fmt.Sprintf(`{"op":"evidence","src":[%d]}`, s.peer) }, CountEvidenceNoID, 0, 0},
		{"没写 src", func(s *evScene) string { return `{"op":"evidence","id":"CGROUP"}` }, CountEvidenceBadSrc, 0, 0},
		{"src 是空数组", func(s *evScene) string { return `{"op":"evidence","id":"CGROUP","src":[]}` }, CountEvidenceBadSrc, 0, 0},
		{"src 写成了数字", func(s *evScene) string { return fmt.Sprintf(`{"op":"evidence","id":"CGROUP","src":%d}`, s.peer) }, CountEvidenceBadSrc, 0, 0},
		{"src 给了两个序号", func(s *evScene) string { return evidenceJSON("CGROUP", s.peer, s.own) }, CountEvidenceBadSrc, 0, 0},
		{"src 给了很多个序号", func(s *evScene) string { return evidenceJSON("CGROUP", s.peer, s.own, s.her, s.other, s.herHuman) }, CountEvidenceBadSrc, 0, 0},
		{"src 不在这一批", func(s *evScene) string { return evidenceJSON("CGROUP", 999) }, CountSrcOutOfBatch, 0, 0},
		{"src 是她自己的话", good(func(s *evScene) int64 { return s.her }), CountEvidenceNotHuman, 0, 0},
		{"src 是她的号上别人发的（human）", good(func(s *evScene) int64 { return s.herHuman }), CountEvidenceNotHuman, 0, 0},
		{"src 是她的号上外部发的（external）", good(func(s *evScene) int64 { return s.herExt }), CountEvidenceNotHuman, 0, 0},
		{"src 是别的角色的话", good(func(s *evScene) int64 { return s.other }), CountEvidenceNotHuman, 0, 0},
		{"src 是补录的话（选材时已丢，不在这一批）", good(func(s *evScene) int64 { return s.backfill }), CountSrcOutOfBatch, 0, 0},
		{"src 是自检的话（同上）", good(func(s *evScene) int64 { return s.selftest }), CountSrcOutOfBatch, 0, 0},
		{"src 是 automation 发的（同上）", good(func(s *evScene) int64 { return s.auto }), CountSrcOutOfBatch, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newEvScene(t)
			before := rowOf(t, s.r.m, "CGROUP")
			s.r.reply("no", "", c.op(s))
			res := s.r.run()
			if res.Counts[c.count] != 1 {
				t.Errorf("应该数到 %s=1：%v", c.count, res.Counts)
			}
			rows := s.r.evidenceOf("CGROUP")
			by := 0
			for _, e := range rows {
				by += e.by
			}
			if len(rows) != c.rows || by != c.bySubj {
				t.Errorf("CGROUP 的证据行 %d（本人说的 %d），应该是 %d（%d）：%+v", len(rows), by, c.rows, c.bySubj, rows)
			}
			if got := rowOf(t, s.r.m, "CGROUP"); got != before {
				t.Errorf("候选本身不该变（状态、范围、修订）：%s → %s", before, got)
			}
			for _, id := range []string{"FKNOWN", "COTHER"} {
				if n := len(s.r.evidenceOf(id)); n != 0 {
					t.Errorf("%s 不是被指的那一条，不该有证据行：%d", id, n)
				}
			}
			if c.rows == 1 && len(rows) == 1 {
				e := rows[0]
				if e.conv != evGroup || e.day != lineDay(e.seq) || e.speaker == "" {
					t.Errorf("会话、时间、说话人取那一行本身：%+v", e)
				}
			}
		})
	}
	t.Run("跨群候选由本人再次提起", func(t *testing.T) {
		t.Parallel()
		s := newEvScene(t)
		before := rowOf(t, s.r.m, "COTHER")
		s.r.reply("no", "", evidenceJSON("COTHER", s.peer))
		res := s.r.run()
		rows := s.r.evidenceOf("COTHER")
		if res.Counts[CountEvidenceAdded] != 1 || len(rows) != 1 || rows[0].by != 1 || len(s.r.evidenceOf("CGROUP")) != 0 || rowOf(t, s.r.m, "COTHER") != before {
			t.Fatalf("跨场合本人证据的身份、归属或原状态改变：%v %+v", res.Counts, rows)
		}
	})

	// 同一批里对同一条写了两次：只认第一次（一次就是一次），第二次计 evidence_dup
	t.Run("同一批里写了两次：只认第一次", func(t *testing.T) {
		t.Parallel()
		s := newEvScene(t)
		s.r.reply("no", "", evidenceJSON("CGROUP", s.peer), evidenceJSON("CGROUP", s.own))
		res := s.r.run()
		rows := s.r.evidenceOf("CGROUP")
		if len(rows) != 1 || rows[0].by != 1 || res.Counts[CountEvidenceDup] != 1 || res.Counts[CountOpEvidence] != 1 {
			t.Errorf("该只有第一次（阿萍本人那一句）：%+v 计数 %v", rows, res.Counts)
		}
	})
	// 好的混在坏的里：坏的丢掉，好的照记
	t.Run("一条坏不拖累另一条好", func(t *testing.T) {
		t.Parallel()
		s := newEvScene(t)
		s.r.reply("no", "", evidenceJSON("NOSUCH", s.peer), evidenceJSON("CGROUP", s.her), evidenceJSON("CGROUP", s.peer))
		res := s.r.run()
		if rows := s.r.evidenceOf("CGROUP"); len(rows) != 1 || res.Counts[CountEvidenceUnknown] != 1 || res.Counts[CountEvidenceNotHuman] != 1 || res.Counts[CountOpEvidence] != 1 {
			t.Errorf("证据 %+v，计数 %v", rows, res.Counts)
		}
	})
	// 候选这一批给模型看过之后、写入之前被别处放行了：写入层不记（evidence_gone）。整批不回滚
	t.Run("写入之前候选已经被放行：不记", func(t *testing.T) {
		t.Parallel()
		s := newEvScene(t)
		s.r.gen.block = func(ctx context.Context) error {
			_, err := s.r.m.w.db.Exec(`UPDATE facts SET status = 'active', confirmed_by = 'owner' WHERE id = 'CGROUP'`)
			return err
		}
		s.r.reply("no", "", evidenceJSON("CGROUP", s.peer))
		res := s.r.run()
		if n := len(s.r.evidenceOf("CGROUP")); n != 0 || res.Counts[CountEvidenceGone] != 1 || res.Skipped != "" {
			t.Errorf("证据行 %d，计数 %v，跳过 %q", n, res.Counts, res.Skipped)
		}
	})
}

// BySubject 由程序按那一行的说话人身份算：候选讲的是谁，那一行是不是他本人。手写的预期表：
// 行 = 候选的主体，列 = 说话人；身份看 ActorKind 和 Origin、ActorID，名字没用
func TestEvidenceBySubject(t *testing.T) {
	t.Parallel()
	speakers := map[string]actor{
		"主人":         ownerA,
		"阿萍本人":       pingA,
		"阿波（别的群友）":   boA,
		"名片叫主人的群友":   spoofA,
		"名片叫阿萍的别的群友": {"human", "qq:60009", "阿萍", "peer"},
	}
	for _, c := range []struct {
		subject string
		want    map[string]int // 说话人 → by_subject
	}{
		{"owner", map[string]int{"主人": 1, "阿萍本人": 0, "阿波（别的群友）": 0, "名片叫主人的群友": 0, "名片叫阿萍的别的群友": 0}},
		{"qq:60001", map[string]int{"主人": 0, "阿萍本人": 1, "阿波（别的群友）": 0, "名片叫主人的群友": 0, "名片叫阿萍的别的群友": 0}},
		// 她自己的事：她的话不当证据，真人说"你喜欢猫"不是本人说，永远 0（收紧，不是 bug）
		{"self", map[string]int{"主人": 0, "阿萍本人": 0, "阿波（别的群友）": 0, "名片叫主人的群友": 0, "名片叫阿萍的别的群友": 0}},
		// 没认出是谁的事：永远 0
		{"", map[string]int{"主人": 0, "阿萍本人": 0, "阿波（别的群友）": 0, "名片叫主人的群友": 0, "名片叫阿萍的别的群友": 0}},
	} {
		for name, a := range speakers {
			t.Run(fmt.Sprintf("候选讲 %q，%s说", c.subject, name), func(t *testing.T) {
				t.Parallel()
				r := newRig(t, evGroup, rigOpts{seed: func(db *sql.DB) {
					seedCand(t, db, "CX", "某件听说的事", c.subject, "conv:"+evGroup, 1)
				}})
				l := r.say(a, "对，就是这样。")
				r.reply("no", "", evidenceJSON("CX", l.Seq))
				r.run()
				rows := r.evidenceOf("CX")
				if len(rows) != 1 || rows[0].by != c.want[name] {
					t.Errorf("by_subject %+v，应该是 %d", rows, c.want[name])
				}
			})
		}
	}
}

// 模型写的别的字段一律不读：状态、谁放行、范围、谁写的、本人说、说话人、哪一天、哪个会话，都由程序定
func TestModelCannotSetStatus(t *testing.T) {
	t.Parallel()
	r := newRig(t, evGroup, rigOpts{seed: func(db *sql.DB) {
		seedCand(t, db, "CGROUP", "阿萍下周搬家", "qq:60001", "conv:"+evGroup, 1)
	}})
	pg := r.say(pingA, "我下周搬家。")
	ow := r.say(ownerA, "好的。")
	r.reply("no", "",
		fmt.Sprintf(`{"op":"candidate","kind":"fact","text":"阿萍想换工作","subject":"qq:60001","src":[%d],"status":"active","confirmed_by":"owner","scope":"public","scopes":["public"],"written_by":"owner","by_subject":true,"attributed_to":"owner"}`, ow.Seq),
		fmt.Sprintf(`{"op":"upsert_fact","kind":"fact","text":"阿萍养了狗","subject":"qq:60001","src":[%d],"status":"active","confirmed_by":"owner","scope":"public"}`, pg.Seq),
		fmt.Sprintf(`{"op":"evidence","id":"CGROUP","src":[%d],"status":"active","confirmed_by":"owner","scope":"public","by_subject":false,"speaker":"owner","day":"2020-01-01","at":1,"conversation":"console:suzune","seq":999,"verdict":"pass"}`, pg.Seq))
	r.run()
	for _, text := range []string{"阿萍想换工作", "阿萍养了狗"} {
		f := r.fact(text)
		if f.status != "candidate" || f.scopes != "conv:"+evGroup {
			t.Errorf("%s：模型写的 status、scope 不该起作用，状态 %s，范围 %s", text, f.status, f.scopes)
		}
		if by := str(t, r.m, `SELECT coalesce(confirmed_by, '<NULL>') FROM facts WHERE text = ?`, text); by != "<NULL>" {
			t.Errorf("%s：confirmed_by 该是 NULL，是 %s", text, by)
		}
	}
	rows := r.evidenceOf("CGROUP")
	want := []evRow{{day: lineDay(pg.Seq), conv: evGroup, speaker: "qq:60001", seq: pg.Seq, by: 1}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("证据行是程序按那一行算的：%+v，应该是 %+v", rows, want)
	}
	if st := str(t, r.m, `SELECT status || '|' || coalesce(confirmed_by, '<NULL>') FROM facts WHERE id = 'CGROUP'`); st != "candidate|<NULL>" {
		t.Errorf("候选不该被提升或被放行：%s", st)
	}
	if v := str(t, r.m, `SELECT coalesce(verdict, '<NULL>') FROM fact_evidence WHERE fact_id = 'CGROUP'`); v != "<NULL>" {
		t.Errorf("verdict 没人写：%s", v)
	}
}

// 候选块里的编号只能用在 evidence 上：对候选写 revoke、supersedes 都对不上（它们只认已知事实）
func TestCandidateIdsAreOnlyForEvidence(t *testing.T) {
	t.Parallel()
	r := newRig(t, evGroup, rigOpts{seed: func(db *sql.DB) {
		seedCand(t, db, "CGROUP", "阿萍下周搬家", "qq:60001", "conv:"+evGroup, 1)
		seedFact(t, db, "FKNOWN", "阿波喜欢猫", "conv:"+evGroup, 2)
	}})
	ow := r.say(ownerA, "那条不对，别记了。")
	r.reply("no", "",
		fmt.Sprintf(`{"op":"revoke","id":"CGROUP","src":[%d]}`, ow.Seq),
		fmt.Sprintf(`{"op":"upsert_fact","kind":"fact","text":"阿萍下周不搬了","subject":"owner","supersedes":"CGROUP","src":[%d]}`, ow.Seq))
	res := r.run()
	if res.Counts[CountRevokeUnknown] != 1 || res.Counts[CountSupersedesUnknown] != 1 || res.Counts[CountRevoked] != 0 {
		t.Errorf("候选的编号不在已知事实里：%v", res.Counts)
	}
	if st := str(t, r.m, `SELECT status FROM facts WHERE id = 'CGROUP'`); st != "candidate" {
		t.Errorf("候选没被动：%s", st)
	}
	// 反过来，已知事实的编号在候选块里不出现
	if ids := candidateIDs(r.gen.last().prompt); !reflect.DeepEqual(ids, []string{"CGROUP"}) {
		t.Errorf("候选块里只该有候选：%v", ids)
	}
	if known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的"); strings.Contains(known, "CGROUP") {
		t.Errorf("已知事实里不该有候选：%s", known)
	}
}

// 撤销要 src 恰好一个序号、那一行是主人自己说的话（W3a 收紧）。主人说的无关话夹在别人的话里，不能替别人的话作证
func TestRevokeNeedsSingleOwnerSrc(t *testing.T) {
	t.Parallel()
	intended(t, "summary-revoke-needs-owner-words")
	for _, c := range []struct {
		name  string
		src   func(o, o2, p, h int64) string
		count string
		want  string
	}{
		{"唯一那一行是主人说的：撤销", func(o, o2, p, h int64) string { return seqs([]int64{o}) }, CountRevoked, "revoked"},
		{"主人一句加群友一句：不撤销", func(o, o2, p, h int64) string { return seqs([]int64{o, p}) }, CountRevokeRefused, "active"},
		{"主人两句：不撤销（只收一个）", func(o, o2, p, h int64) string { return seqs([]int64{o, o2}) }, CountRevokeRefused, "active"},
		{"主人一句加不在批里的：不撤销", func(o, o2, p, h int64) string { return seqs([]int64{o, 999}) }, CountRevokeRefused, "active"},
		{"只有群友的：不撤销", func(o, o2, p, h int64) string { return seqs([]int64{p}) }, CountRevokeRefused, "active"},
		{"只有她自己的：不撤销", func(o, o2, p, h int64) string { return seqs([]int64{h}) }, CountRevokeRefused, "active"},
		{"不在批里的：不撤销", func(o, o2, p, h int64) string { return seqs([]int64{999}) }, CountRevokeRefused, "active"},
		{"没写：不撤销", func(o, o2, p, h int64) string { return "" }, CountRevokeRefused, "active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, evGroup, rigOpts{seed: func(db *sql.DB) { seedFact(t, db, "FTARGET", "要被撤销的事", "conv:"+evGroup, 1) }})
			if _, err := r.m.w.db.Exec(`UPDATE facts SET confirmed_by = 'ai' WHERE id = 'FTARGET'`); err != nil {
				t.Fatal(err)
			}
			o := r.say(ownerA, "那条不对。")
			o2 := r.say(ownerA, "真的不对。")
			p := r.say(pingA, "我觉得是对的。")
			h := r.say(herA, "好的。")
			op := `{"op":"revoke","id":"FTARGET"}`
			if src := c.src(o.Seq, o2.Seq, p.Seq, h.Seq); src != "" {
				op = fmt.Sprintf(`{"op":"revoke","id":"FTARGET","src":%s}`, src)
			}
			r.reply("no", "", op)
			res := r.run()
			if st := str(t, r.m, `SELECT status FROM facts WHERE id = 'FTARGET'`); res.Counts[c.count] != 1 || st != c.want {
				t.Errorf("计数 %v，状态 %s，应该 %s 数到 %s", res.Counts, st, c.want, c.count)
			}
		})
	}
}

// 解析：evidence 的 id 和 src 原样读出来（检查在 plan 里）；占 20 条的名额，不单算；其余字段不读
func TestParseEvidenceOp(t *testing.T) {
	t.Parallel()
	out := mustParse(t, `{"ops":[{"op":"evidence","id":" CA ","src":[3],"status":"active","by_subject":true}]}`)
	if len(out.ops) != 1 || out.ops[0].op != "evidence" || out.ops[0].id != "CA" || !reflect.DeepEqual(out.ops[0].src, []int64{3}) {
		t.Errorf("读出来 %+v", out.ops)
	}
	var parts []string
	for i := 0; i < 21; i++ {
		parts = append(parts, fmt.Sprintf(`{"op":"evidence","id":"C%d","src":[1]}`, i))
	}
	out = mustParse(t, `{"ops":[`+strings.Join(parts, ",")+`]}`)
	if len(out.ops) != 20 || out.counts[CountOpsOverLimit] != 1 {
		t.Errorf("evidence 占 20 条的名额：留 %d 条，丢 %d 条", len(out.ops), out.counts[CountOpsOverLimit])
	}
	// 前面 19 条 evidence，后面一条 candidate 在第 20 个位置、再后面的丢
	parts = parts[:19]
	parts = append(parts, goodOp, goodOp)
	out = mustParse(t, `{"ops":[`+strings.Join(parts, ",")+`]}`)
	if len(out.ops) != 20 || out.counts[CountOpsOverLimit] != 1 {
		t.Errorf("evidence 和别的动作共用名额：留 %d 条，丢 %d 条", len(out.ops), out.counts[CountOpsOverLimit])
	}
}

// 提示词和解析一起换：版本是 2；v1 整理过的同一段对话，v2 会重新整理；提示词里的每个占位符都有人填、没有漏下的
func TestSummaryPromptV2(t *testing.T) {
	t.Parallel()
	if SummaryVersion != 3 {
		t.Errorf("W3a 的提示词和解析一起换了，版本该是 3：%d", SummaryVersion)
	}
	r := newRig(t, "console:suzune")
	o := r.say(ownerA, "我下周三考试。")
	// v1 整理过同一段（起 1，止 o.Seq）：幂等键带版本，不挡 v2
	if _, err := r.m.w.db.Exec(`INSERT INTO memory_jobs (conversation, from_seq, to_seq, version, done_at, counts_json) VALUES ('console:suzune', 1, ?, 1, 1, '{}')`, o.Seq); err != nil {
		t.Fatal(err)
	}
	r.reply("no", "", cand("主人下周三考试", "owner", o.Seq))
	res := r.run()
	if res.Skipped != "" || r.gen.count() != 1 {
		t.Fatalf("v1 整理过的同一段 v2 该重新整理：跳过 %q，调了 %d 次", res.Skipped, r.gen.count())
	}
	prompt := r.gen.last().prompt
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Errorf("提示词里有没填的占位符：\n%s", prompt)
	}
	for _, want := range []string{`"op":"evidence"`, `"op":"revoke","id":"已知事实编号","src"`, "已知事实：\n", "听说过、还没确认的：\n（暂无）", "说话的人（subject 照这里写）："} {
		if !strings.Contains(prompt, want) {
			t.Errorf("提示词里该有 %q", want)
		}
	}
	// Bun 读的旧版一字不动：它没有候选块和 evidence
	old, _, err := prompts.Store{Root: repoRoot}.Read("summarize")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(old, "{{candidates}}") || strings.Contains(old, `"evidence"`) {
		t.Errorf("summarize.md 是 Bun 读的旧版，不该有 W3a 的内容")
	}
}
