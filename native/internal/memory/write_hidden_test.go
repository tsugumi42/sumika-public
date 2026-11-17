package memory

import (
	"database/sql"
	"testing"
)

// 转述的证据只在看得见这条待确认事实的会话里算（规格-记忆写入上线 W3b 的计次规则④）：同文查重按（主体，文本）全库查、不看范围，
// 别的会话里一个看不到这条候选的人说同一句，不该替它投票。本人说的不受这条限制（那是他自己的话）。
// 名字、号码、会话都是编的；"看不看得见"的预期是照规格-记忆-v2 4.2 的表手写的，不调被测的 CanSee / AllowedScopes

// hiddenScene 是一个听众（谁在看、在哪看）
type hiddenScene struct {
	name string
	a    Audience
}

var hiddenScenes = []hiddenScene{
	{"本地私聊", Audience{Channel: "console", Conversation: "console:suzune", Scene: "private", PrivateOK: true}},
	{"本地群聊", Audience{Channel: "console", Conversation: "console:group:g1", Scene: "group", PrivateOK: true}},
	{"QQ 私聊·主人", Audience{Channel: "qq", Conversation: "qq:private:20001:7001", Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}}},
	{"QQ 私聊·阿萍", Audience{Channel: "qq", Conversation: "qq:private:20001:7002", Scene: "private", PersonIDs: []string{"qq:60001"}}},
	{"QQ 群 A", Audience{Channel: "qq", Conversation: "qq:group:5001001", Scene: "group"}},
	{"QQ 群 A·主人在问", Audience{Channel: "qq", Conversation: "qq:group:5001001", Scene: "group", OwnerAsked: true}},
	{"QQ 群 B", Audience{Channel: "qq", Conversation: "qq:group:5002002", Scene: "group"}},
}

// hiddenShapes：候选的范围（一组），和每个场合（顺序同 hiddenScenes）看不看得见。手写的表：
// 本地私聊里的主人看全部；群里看 public、这个群，主人在问时再加 owner；私聊看 public、这个会话、owner（主人）或 person:他（某人）
var hiddenShapes = []struct {
	name    string
	scopes  []string
	visible [7]bool
}{
	{"public", []string{"public"}, [7]bool{true, true, true, true, true, true, true}},
	{"owner", []string{"owner"}, [7]bool{true, true, true, false, false, true, false}},
	{"群 A", []string{"conv:qq:group:5001001"}, [7]bool{true, false, false, false, true, true, false}},
	{"owner 加群 A", []string{"owner", "conv:qq:group:5001001"}, [7]bool{true, true, true, false, true, true, false}},
	{"阿萍的 person", []string{"person:qq:60001"}, [7]bool{true, false, false, true, false, false, false}},
	{"本地群 g1", []string{"conv:console:group:g1"}, [7]bool{true, true, false, false, false, false, false}},
}

const hiddenText = "阿萍下周搬家"

// 形状（候选的范围）× 场合 × 入口（evidence 动作、同文合并）× 谁说的（本人、转述）：
// 转述的看得见才记，看不见的一行不记、数 evidence_hidden、候选本身一个字节不动；本人说的哪里都记
func TestTransferredEvidenceNeedsASceneThatSeesTheCandidate(t *testing.T) {
	t.Parallel()
	intended(t, "dup-merge-scope-by-subject")
	entries := []struct {
		name string
		op   func(conv string, self bool, scopes []string) Op
	}{
		{"evidence 动作", func(conv string, self bool, _ []string) Op {
			return EvidenceOp{FactID: "CAND", Evidence: Evidence{Conversation: conv, At: evDay(20), Seq: 7, Speaker: "qq:60009", BySubject: self}}
		}},
		{"同文合并", func(conv string, self bool, scopes []string) Op {
			f := candidate(hiddenText, "qq:60001", scopes...) // 范围同候选自己：合并不另加范围，只看证据记不记
			f.Evidence = &Evidence{Conversation: conv, At: evDay(20), Seq: 7, Speaker: "qq:60009", BySubject: self}
			return f
		}},
	}
	for _, sh := range hiddenShapes {
		for si, sc := range hiddenScenes {
			for _, en := range entries {
				for _, self := range []bool{false, true} {
					name := sh.name + "／" + sc.name + "／" + en.name + "／转述"
					if self {
						name = sh.name + "／" + sc.name + "／" + en.name + "／本人说"
					}
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						m, _ := newWritable(t, nil)
						if _, err := m.w.db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope) VALUES ('CAND', 'fact', ?, 'qq:60001', 'private', '[]', 'candidate', 1, ?)`, hiddenText, sh.scopes[0]); err != nil {
							t.Fatal(err)
						}
						for _, s := range sh.scopes {
							if _, err := m.w.db.Exec(`INSERT OR IGNORE INTO item_scopes (ref, scope) VALUES ('f:CAND', ?)`, s); err != nil {
								t.Fatal(err)
							}
						}
						before := rowOf(t, m, "CAND")
						a := sc.a
						res := mustApply(t, m, Batch{By: BySummary, Ops: []Op{en.op(a.Conversation, self, sh.scopes)}, Audience: &a})

						wantRecorded := self || sh.visible[si]
						rows := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = 'CAND'`)
						hidden := res.Counts[CountEvidenceHidden]
						if wantRecorded && (rows != 1 || hidden != 0) {
							t.Errorf("该记一行：证据行 %d，计数 %v", rows, res.Counts)
						}
						if !wantRecorded && (rows != 0 || hidden != 1 || res.Counts[CountEvidenceAdded] != 0) {
							t.Errorf("看不见这条候选的会话里的转述一行都不该记，数 evidence_hidden：证据行 %d，计数 %v", rows, res.Counts)
						}
						if after := rowOf(t, m, "CAND"); after != before {
							t.Errorf("候选本身不该变（状态、谁放行的、范围、修订）：\n前 %s\n后 %s", before, after)
						}
					})
				}
			}
		}
	}
}

// 转述的证据在群里记下以后范围也不扩：主人在问的群里看得见 owner 范围的候选，群友转述同一句，记一次证据，范围不动（scopes_kept）
func TestVisibleTransferKeepsTheScopes(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	mustApply(t, m, summary(candidate(hiddenText, "qq:60001", "owner")))
	id := idOf(str(t, m, `SELECT 'f:' || id FROM facts WHERE text = ?`, hiddenText))
	before := rowOf(t, m, id)
	a := hiddenScenes[5].a // 群 A，主人在问：owner 范围看得见
	dup := candidate(hiddenText, "qq:60001", "conv:"+a.Conversation)
	dup.Evidence = &Evidence{Conversation: a.Conversation, At: evDay(20), Seq: 7, Speaker: "qq:60009"}
	res := mustApply(t, m, Batch{By: BySummary, Ops: []Op{dup}, Audience: &a})
	if res.Counts[CountEvidenceAdded] != 1 || res.Counts[CountScopesKept] != 1 || res.Counts[CountEvidenceHidden] != 0 || res.Counts[CountScopesWidened] != 0 {
		t.Fatalf("看得见：记一次证据、范围不动：%v", res.Counts)
	}
	if after := rowOf(t, m, id); after != before {
		t.Fatalf("候选本身不该变：\n前 %s\n后 %s", before, after)
	}
}

// 整理批没带听众：按最窄的一档算（public 和证据所在的会话自己）。忘了给只会少记，不会多记
func TestMissingAudienceMeansTheNarrowestView(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		scopes []string
		want   int // 证据行数
	}{
		{"owner 范围的看不见", []string{"owner"}, 0},
		{"别的群的看不见", []string{"conv:qq:group:5001001"}, 0},
		{"public 看得见", []string{"public"}, 1},
		{"证据所在的会话自己看得见", []string{"conv:qq:group:5002002"}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritable(t, nil)
			mustApply(t, m, summary(candidate(hiddenText, "qq:60001", c.scopes...)))
			id := idOf(str(t, m, `SELECT 'f:' || id FROM facts WHERE text = ?`, hiddenText))
			ev := Evidence{Conversation: "qq:group:5002002", At: evDay(20), Seq: 7, Speaker: "qq:60009"}
			mustApply(t, m, summary(EvidenceOp{FactID: id, Evidence: ev}))
			if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); n != c.want {
				t.Errorf("证据行 %d，应该是 %d", n, c.want)
			}
		})
	}
}

// 从聊天一路走到写库：群 B 里有人转述了一句话，同文撞上群 A 的候选（群 B 看不见它）→ 不记证据，范围不动；
// 同一句话由阿萍本人在群 B 说 → 记证据，范围并上群 B（本人说的，4.2）
func TestRestatementFromAnInvisibleGroupIsNotAVote(t *testing.T) {
	t.Parallel()
	const groupA, groupB = "qq:group:5001001", "qq:group:5002002"
	seed := func(db *sql.DB) { seedCand(t, db, "CA", hiddenText, "qq:60001", "conv:"+groupA, 1) }

	t.Run("群友转述", func(t *testing.T) {
		r := newRig(t, groupB, rigOpts{seed: seed})
		ping := r.say(pingA, "我今天累死了。").Seq
		hear := r.say(boA, "听说阿萍下周搬家。").Seq
		_ = ping
		r.reply("no", "", cand(hiddenText, "qq:60001", hear))
		before := rowOf(t, r.m, "CA")
		res := r.run()
		if res.Counts[CountEvidenceHidden] != 1 || res.Counts[CountEvidenceAdded] != 0 {
			t.Errorf("转述的证据来自看不见这条候选的群：该是 evidence_hidden：%v", res.Counts)
		}
		if rows := r.evidenceOf("CA"); len(rows) != 0 {
			t.Errorf("一行证据都不该记：%+v", rows)
		}
		if after := rowOf(t, r.m, "CA"); after != before {
			t.Errorf("候选本身不该变：\n前 %s\n后 %s", before, after)
		}
		if got := r.jobCounts(); got[CountEvidenceHidden] != 1 {
			t.Errorf("memory_jobs.counts_json 里该有 evidence_hidden：%v", got)
		}
	})
	t.Run("阿萍本人说", func(t *testing.T) {
		r := newRig(t, groupB, rigOpts{seed: seed})
		own := r.say(pingA, "我下周搬家。").Seq
		r.reply("no", "", cand(hiddenText, "qq:60001", own))
		res := r.run()
		rows := r.evidenceOf("CA")
		if res.Counts[CountEvidenceHidden] != 0 || len(rows) != 1 || rows[0].by != 1 || rows[0].conv != groupB {
			t.Errorf("本人说的记一次：%v 证据 %+v", res.Counts, rows)
		}
		if got := r.scopesOf("f:CA"); got != "conv:"+groupA+",conv:"+groupB {
			t.Errorf("本人说的并上群 B：%q", got)
		}
	})
}

// jobCounts：最近一批整理写进 memory_jobs.counts_json 的计数
func (r *rig) jobCounts() map[string]int {
	r.t.Helper()
	raw := jsonMap(r.t, str(r.t, r.m, `SELECT counts_json FROM memory_jobs ORDER BY done_at DESC, to_seq DESC LIMIT 1`))
	out := map[string]int{}
	for k, v := range raw {
		out[k] = int(v.(float64))
	}
	return out
}

// 主人的私聊（本地、QQ）看得见 owner 范围的候选：别人的事（主体没认出是谁，本人说不上）在这里转述一遍，证据照记。
// 整理批的听众要是丢了，会被当成最窄的一档，这里就记不上
func TestOwnerPrivateChatSeesOwnerScopedCandidates(t *testing.T) {
	t.Parallel()
	for _, conv := range []string{"console:suzune", "qq:private:20001:7001"} {
		t.Run(conv, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, conv, rigOpts{seed: func(db *sql.DB) {
				if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope) VALUES ('CP', 'fact', ?, NULL, 'private', '[]', 'candidate', 1, 'owner')`, "邻居家的猫很凶"); err != nil {
					t.Fatal(err)
				}
			}})
			own := r.say(ownerA, "邻居家的猫很凶。").Seq
			r.reply("no", "", cand("邻居家的猫很凶", "", own))
			res := r.run()
			if rows := r.evidenceOf("CP"); len(rows) != 1 || rows[0].by != 0 || res.Counts[CountEvidenceHidden] != 0 {
				t.Errorf("主人的私聊看得见 owner 范围的候选，转述该记一次：%+v 计数 %v", rows, res.Counts)
			}
		})
	}
}
