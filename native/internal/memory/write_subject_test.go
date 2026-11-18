package memory

import (
	"testing"
)

// 整理的 supersedes 要和候选是同一个主体（复核后补）：取代是"同一个人的同一件事更新了"，
// 模型填错编号（或聊天里的提示注入）指到了别人名下的 ai 事实时，不记想取代谁，候选自己照常走。
// 两道防线：写入时（insertFact）不记 wants_supersede；就算记下了（直接改 add 修订模拟），提升时（confirmByEvidence）再核一次，不取代、照常提升。
// 主体都是编的；预期手写：只有 候选主体 == 旧事实主体 才取代，没有主体（别人的事，主体留空）只和没有主体的是同一个

var subjectShapes = []string{"owner", "self", "qq:20003", ""}

func subjectedAIFact(text, subject string) FactOp {
	f := aiFact(text)
	f.Subject = subject
	return f
}

func subjectedCandidate(text, subject, target string) FactOp {
	c := candidate(text, subject, "owner")
	c.Supersedes = target
	return c
}

func TestSupersedeNeedsTheSameSubject(t *testing.T) {
	t.Parallel()
	label := func(s string) string {
		if s == "" {
			return "无主体"
		}
		return s
	}
	for _, candSubject := range subjectShapes {
		for _, oldSubject := range subjectShapes {
			same := candSubject == oldSubject
			name := "候选 " + label(candSubject) + "／旧事实 " + label(oldSubject)

			// 第一道：写入时
			t.Run(name+"／写入时", func(t *testing.T) {
				t.Parallel()
				m, _ := newWritableWith(t, func(o *Options) { o.AutoConfirm = true })
				oldID := idOf(mustApply(t, m, summary(subjectedAIFact("主人住在北京", oldSubject))).Outcomes[0].Ref)
				old0 := rowOf(t, m, oldID)
				res := mustApply(t, m, summary(subjectedCandidate("主人搬到了上海", candSubject, oldID)))
				id := idOf(res.Outcomes[0].Ref)
				add := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'add'`, id))
				if same {
					if res.Counts[CountSupersedeDeferred] != 1 || res.Counts[CountSupersedeSubjectMismatch] != 0 || add["wants_supersede"] != oldID {
						t.Fatalf("主体一样：该记想取代谁、数 supersede_deferred：%v %v", res.Counts, add)
					}
				} else {
					if res.Counts[CountSupersedeSubjectMismatch] != 1 || res.Counts[CountSupersedeDeferred] != 0 {
						t.Fatalf("主体不一样：该数 supersede_subject_mismatch、不数 supersede_deferred：%v", res.Counts)
					}
					if _, has := add["wants_supersede"]; has {
						t.Fatalf("主体不一样：不该记想取代谁：%v", add)
					}
				}
				if res.Outcomes[0].Status != "candidate" || res.Outcomes[0].Replaced != nil {
					t.Fatalf("新的一律是待确认、写入时不取代任何东西：%+v", res.Outcomes[0])
				}
				if got := rowOf(t, m, oldID); got != old0 {
					t.Fatalf("写入时旧的一个字节都不该变：\n前 %s\n后 %s", old0, got)
				}
				// 攒够证据提升：主体一样才取代旧的；不一样旧的一个字节不变，候选自己照常提升
				var all []Applied
				for d := 1; d <= 3; d++ {
					all = append(all, mustApply(t, m, summary(cev{"console:a", d, "owner", true}.viaEvidence(id))))
				}
				if statusOf(t, m, id) != "active" {
					t.Fatalf("证据够了候选该提升：%s", statusOf(t, m, id))
				}
				if same {
					if got := str(t, m, `SELECT status || '|' || coalesce(superseded_by, '-') FROM facts WHERE id = ?`, oldID); got != "superseded|"+id {
						t.Fatalf("主体一样：旧的该被取代：%s", got)
					}
				} else if got := rowOf(t, m, oldID); got != old0 {
					t.Fatalf("主体不一样：旧的一个字节都不该变：\n前 %s\n后 %s", old0, got)
				}
				if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, oldID); (n == 1) != same {
					t.Fatalf("supersede 修订该有 %v，得到 %d 条", same, n)
				}
			})

			if same {
				continue
			}
			// 第二道：假设 wants_supersede 已经记下了（直接改 add 修订模拟），提升时还要再核一次
			for _, auto := range []bool{true, false} {
				mode := "／提升时"
				if !auto {
					mode = "／提升时／记账模式"
				}
				t.Run(name+mode, func(t *testing.T) {
					t.Parallel()
					m, _ := newWritableWith(t, func(o *Options) { o.AutoConfirm = auto })
					oldID := idOf(mustApply(t, m, summary(subjectedAIFact("主人住在北京", oldSubject))).Outcomes[0].Ref)
					id := idOf(mustApply(t, m, summary(subjectedCandidate("主人搬到了上海", candSubject, ""))).Outcomes[0].Ref)
					if _, err := m.w.db.Exec(`UPDATE fact_revisions SET after_json = json_set(after_json, '$.wants_supersede', ?) WHERE fact_id = ? AND op = 'add'`, oldID, id); err != nil {
						t.Fatal(err)
					}
					old0 := rowOf(t, m, oldID)
					var all []Applied
					for d := 1; d <= 3; d++ {
						all = append(all, mustApply(t, m, summary(cev{"console:a", d, "owner", true}.viaEvidence(id))))
					}
					if got := rowOf(t, m, oldID); got != old0 {
						t.Fatalf("旧的一个字节都不该变：\n前 %s\n后 %s", old0, got)
					}
					if auto && statusOf(t, m, id) != "active" {
						t.Fatalf("候选自己的证据够了，照常提升：%s", statusOf(t, m, id))
					}
					if !auto && statusOf(t, m, id) != "candidate" {
						t.Fatalf("记账模式不改状态：%s", statusOf(t, m, id))
					}
					if sumCounts(all, CountSupersedeSubjectMismatch) != 1 || sumCounts(all, CountSuperseded) != 0 || sumCounts(all, CountConfirmBlockedProtected) != 0 {
						t.Fatalf("该数 supersede_subject_mismatch 一次，不取代，也不算被保护挡下：%v", all)
					}
					if auto && sumCounts(all, CountConfirmed) != 1 || !auto && sumCounts(all, CountWouldConfirm) == 0 {
						t.Fatalf("提升（记账模式是 would_confirm）该照常发生：%v", all)
					}
				})
			}
		}
	}
}
