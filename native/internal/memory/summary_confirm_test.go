package memory

import (
	"database/sql"
	"strings"
	"testing"
)

// 待确认攒够证据变有效，从聊天一路走到库（规格-记忆写入上线 W3b）：假的后台模型每批写一条 evidence 动作，三批在三天里。
// 名字、号码、聊天内容都是编的

// 一条候选在三个不同的天各收到一次证据：第三批提升（开着自动提升），提升之后进已知事实、不再在候选块里；记账模式只数 would_confirm
func TestThreeDaysOfEvidencePromoteInTheThirdBatch(t *testing.T) {
	t.Parallel()
	intended(t, "summary-evidence-confirm")
	const conv = "qq:private:20001:7001"
	for _, auto := range []bool{true, false} {
		name := "开着自动提升"
		if !auto {
			name = "记账模式"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, conv, rigOpts{
				seed: func(db *sql.DB) { seedCand(t, db, "CAND", "主人喜欢喝茶", "owner", "owner", 1) },
				mod:  func(o *Options) { o.AutoConfirm = auto },
			})
			for i, day := range []int{20, 21, 22} {
				own := r.say(ownerA, "我最喜欢喝茶了。", func(e *Event) { e.At = evDay(day) }).Seq
				r.reply("no", "", evidenceJSON("CAND", own))
				res := r.run()
				if res.Counts[CountOpEvidence] != 1 || res.Counts[CountEvidenceAdded] != 1 {
					t.Fatalf("第 %d 批：证据该记上：%v", i+1, res.Counts)
				}
				status := str(t, r.m, `SELECT status FROM facts WHERE id = 'CAND'`)
				promoted := auto && i == 2
				if (status == "active") != promoted {
					t.Fatalf("第 %d 批之后状态 %s", i+1, status)
				}
				jc := r.jobCounts()
				switch {
				case promoted && jc[CountConfirmed] != 1:
					t.Errorf("第 3 批该数 confirmed：%v", jc)
				case !auto && i == 2 && (jc[CountWouldConfirm] != 1 || jc[CountConfirmed] != 0):
					t.Errorf("记账模式第 3 批该数 would_confirm：%v", jc)
				case i < 2 && (jc[CountWouldConfirm] != 0 || jc[CountConfirmed] != 0):
					t.Errorf("第 %d 批还不到：%v", i+1, jc)
				}
			}
			revs := count(t, r.m, `SELECT count(*) FROM fact_revisions WHERE fact_id = 'CAND' AND op = 'confirm'`)
			if auto {
				if revs != 1 || str(t, r.m, `SELECT confirmed_by FROM facts WHERE id = 'CAND'`) != "ai" {
					t.Errorf("confirm 修订 %d 条，confirmed_by 该是 ai", revs)
				}
			} else if revs != 0 {
				t.Errorf("记账模式不写 confirm 修订：%d", revs)
			}
			// 再来一批：提升了的进已知事实、不在候选块里；没提升的还在候选块里
			r.say(ownerA, "今天天气不错。", func(e *Event) { e.At = evDay(23) })
			r.reply("no", "")
			r.run()
			prompt := r.gen.last().prompt
			known := section(prompt, "已知事实：\n", "\n\n听说过、还没确认的")
			cands := candidateIDs(prompt)
			if auto && (!strings.Contains(known, "CAND｜fact｜主人喜欢喝茶") || len(cands) != 0) {
				t.Errorf("提升了：该进已知事实、离开候选块：已知 %q 候选 %v", known, cands)
			}
			if !auto && (strings.Contains(known, "CAND") || len(cands) != 1 || cands[0] != "CAND") {
				t.Errorf("没提升：还在候选块里：已知 %q 候选 %v", known, cands)
			}
		})
	}
}

// 谁说的才算本人：从聊天里的说话人身份一路算到提升。候选在 QQ 群里，每一行是一天里的一句话。
// 预期是手写的：主人的事要主人本人（ActorKind）说过，某人的事要他本人（号对得上）说过，她自己的事、没认出是谁的事永远不提升；
// 名片叫"主人""阿萍"的别人不算本人
func TestEvidenceRulesFromChatToStatus(t *testing.T) {
	t.Parallel()
	intended(t, "summary-evidence-confirm")
	type line struct {
		a   actor
		day int
	}
	spoofOwner := spoofA                                  // 群名片叫"主人"的群友（qq:60009）
	spoofPing := actor{"human", "qq:60099", "阿萍", "peer"} // 群名片抄了阿萍的陌生人
	others := []actor{pingA, boA, spoofOwner}             // 三个不同的非主人
	same := func(a actor, days ...int) (ls []line) {
		for _, d := range days {
			ls = append(ls, line{a, d})
		}
		return
	}
	cases := []struct {
		name    string
		subject string
		text    string
		lines   []line
		want    bool
	}{
		{"主人的事：主人本人三天各说一次", "owner", "主人下周搬家", same(ownerA, 1, 2, 3), true},
		{"主人的事：名片叫主人的群友说三天（认的是类型不是名字）", "owner", "主人下周搬家", same(spoofOwner, 1, 2, 3), false},
		{"主人的事：三个不同的群友各说一次，没有主人本人", "owner", "主人下周搬家", []line{{others[0], 1}, {others[1], 2}, {others[2], 3}}, false},
		{"主人的事：主人一次加两个不同的群友", "owner", "主人下周搬家", []line{{ownerA, 1}, {pingA, 2}, {boA, 3}}, true},
		{"阿萍的事：阿萍本人三天各说一次", "qq:60001", "阿萍下周搬家", same(pingA, 1, 2, 3), true},
		{"阿萍的事：主人三天说（别人转述，同一个人）", "qq:60001", "阿萍下周搬家", same(ownerA, 1, 2, 3), false},
		{"阿萍的事：阿萍一次、主人一次、阿波一次", "qq:60001", "阿萍下周搬家", []line{{pingA, 1}, {ownerA, 2}, {boA, 3}}, true},
		{"阿萍的事：阿萍一次、阿波说两天", "qq:60001", "阿萍下周搬家", []line{{pingA, 1}, {boA, 2}, {boA, 3}}, false},
		{"阿萍的事：名片抄了阿萍的陌生人说三天（号对不上）", "qq:60001", "阿萍下周搬家", same(spoofPing, 1, 2, 3), false},
		{"她自己的事：真人说三天（真人说「你喜欢猫」不是本人）", "self", "铃音喜欢猫", same(pingA, 1, 2, 3), false},
		{"她自己的事：主人说三天", "self", "铃音喜欢猫", same(ownerA, 1, 2, 3), false},
		{"没认出是谁的事：阿萍说三天", "", "邻居家的猫很凶", same(pingA, 1, 2, 3), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, evGroup, rigOpts{
				seed: func(db *sql.DB) { seedCand(t, db, "CAND", c.text, c.subject, "conv:"+evGroup, 1) },
				mod:  autoConfirm,
			})
			for i, l := range c.lines {
				seq := r.say(l.a, "对，就是这样。", func(e *Event) { e.At = evDay(l.day) }).Seq
				r.reply("no", "", evidenceJSON("CAND", seq))
				res := r.run()
				if res.Counts[CountOpEvidence] != 1 {
					t.Fatalf("第 %d 批：证据没交给写入层：%v", i+1, res.Counts)
				}
				if status := str(t, r.m, `SELECT status FROM facts WHERE id = 'CAND'`); status != "candidate" && i < len(c.lines)-1 {
					t.Fatalf("第 %d 批（共 %d 批）就提升了", i+1, len(c.lines))
				}
			}
			got := str(t, r.m, `SELECT status FROM facts WHERE id = 'CAND'`) == "active"
			if got != c.want {
				t.Errorf("提升了 %v，应该是 %v", got, c.want)
			}
			if rows := r.evidenceOf("CAND"); len(rows) != len(c.lines) {
				t.Errorf("每一天一行证据：%d 行 / %d 批", len(rows), len(c.lines))
			}
		})
	}
}
