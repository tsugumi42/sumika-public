package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 取代保护（规格-记忆写入上线 W2）：整理（AI）只能取代、撤销 confirmed_by = 'ai' 的有效事实；
// 整理写的候选不在写入时取代任何有效事实（想取代谁记在 add 修订的 wants_supersede，提升时才定，W3b）；
// 主人再说一遍（remember 同文）把有效事实提成 owner。
// 下面的"预期"都是手写的表，不调被测的 protectedFromAI。名字、号码、会话都是编的

// protectShape 是被动的那条事实的形状（confirmed_by 的各种写法，加上各种状态）
type protectShape struct {
	name      string
	status    string
	confirmed any // nil 是 NULL（Bun 导入的：迁移 0001 只加列、没回填）
	// aiMayTouch 是手写的预期：整理动得了吗。只有 active 且 confirmed_by 恰好是 'ai'（AI 自己攒够证据提升的）
	aiMayTouch bool
}

func (s protectShape) active() bool { return s.status == "active" }

var protectShapes = []protectShape{
	{"导入的 NULL", "active", nil, false},
	{"空串", "active", "", false},
	{"ai", "active", "ai", true},
	{"owner", "active", "owner", false},
	{"不认识的值 import", "active", "import", false},
	{"大小写不对的 AI", "active", "AI", false},
	{"待确认", "candidate", nil, false},
	{"被取代", "superseded", "ai", false},
	{"撤销", "revoked", "ai", false},
	{"AI 没通过", "rejected", nil, false},
}

// seedShape 往库里放一条这个形状的事实。FactOp 的校验不收不认识的 confirmed_by，所以先写成 ai 有效的，再直接改库
func seedShape(t *testing.T, m *Memory, sh protectShape, text string) string {
	t.Helper()
	id := idOf(mustApply(t, m, summary(aiFact(text))).Outcomes[0].Ref)
	if _, err := m.w.db.Exec(`UPDATE facts SET status = ?, confirmed_by = ? WHERE id = ?`, sh.status, sh.confirmed, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// rowOf 是一条事实此刻的样子：状态、被谁取代、谁放行的、修订几条、范围。"库里一个字节没变"就比它
func rowOf(t *testing.T, m *Memory, id string) string {
	t.Helper()
	return str(t, m, `SELECT status || '|' || coalesce(superseded_by, '-') || '|' || coalesce(confirmed_by, '<NULL>') || '|'
		|| (SELECT count(*) FROM fact_revisions WHERE fact_id = facts.id) || '|'
		|| coalesce((SELECT group_concat(scope) FROM (SELECT scope FROM item_scopes WHERE ref = 'f:' || facts.id ORDER BY scope)), '')
		FROM facts WHERE id = ?`, id)
}

// supersedeDirect 在一个事务里直接调"取代"这个动作（提升候选时 W3b 会这样调）。by 是 Batch.By。返回结局和这一次数到的计数
func supersedeDirect(t *testing.T, m *Memory, by, oldID, newID string) (supersedeResult, map[string]int) {
	t.Helper()
	ctx := context.Background()
	unlock, err := m.w.lock(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	tx, err := m.w.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &txRun{w: m.w, tx: tx, b: Batch{By: by}, now: m.w.now().UnixMilli(), counts: map[string]int{}, dups: map[string]map[string][]*dupRow{}}
	_, res, err := tr.supersede(ctx, oldID, newID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m.w.needReload = true // 库在锁里改了，内存里的条目下次写入时先补刷新
	return res, tr.counts
}

func candidateWanting(text, target string) FactOp {
	c := candidate(text, "owner", "owner")
	c.Supersedes = target
	return c
}

// ①：整理写的候选带 supersedes，写入时不取代任何有效事实——不论旧的是什么形状（连 ai 放行的也不），旧的一个字节不变，只多一条带 wants_supersede 的新候选
func TestCandidateNeverSupersedesAtInsert(t *testing.T) {
	t.Parallel()
	intended(t, "summary-supersede-deferred-whitelist")
	ops := map[string]func(text, target string) FactOp{
		"候选": candidateWanting,
		"整理写的有效事实（只有测试里有）": func(text, target string) FactOp { f := aiFact(text); f.Supersedes = target; return f },
	}
	for _, sh := range protectShapes {
		for opName, mk := range ops {
			t.Run(sh.name+"／"+opName, func(t *testing.T) {
				t.Parallel()
				m, _ := newWritable(t, nil)
				id := seedShape(t, m, sh, "主人住在北京")
				row0 := rowOf(t, m, id)
				facts, revs, scopes, evid := count(t, m, `SELECT count(*) FROM facts`), count(t, m, `SELECT count(*) FROM fact_revisions`),
					count(t, m, `SELECT count(*) FROM item_scopes`), count(t, m, `SELECT count(*) FROM fact_evidence`)

				res := mustApply(t, m, summary(mk("主人搬到了上海", id)))
				o := res.Outcomes[0]
				if o.Result != ResultAdded || o.Status != "candidate" || o.Replaced != nil {
					t.Fatalf("新的该是待确认、没取代任何东西：%+v", o)
				}
				if res.Counts[CountSupersedeDeferred] != 1 || res.Counts[CountSuperseded] != 0 || res.Counts[CountSupersedeProtected] != 0 || res.Counts[CountSupersedeLost] != 0 {
					t.Fatalf("计数该只有 supersede_deferred：%v", res.Counts)
				}
				if got := rowOf(t, m, id); got != row0 {
					t.Fatalf("旧的一个字节都不该变：\n前 %s\n后 %s", row0, got)
				}
				if got := [4]int{count(t, m, `SELECT count(*) FROM facts`), count(t, m, `SELECT count(*) FROM fact_revisions`),
					count(t, m, `SELECT count(*) FROM item_scopes`), count(t, m, `SELECT count(*) FROM fact_evidence`)}; got != [4]int{facts + 1, revs + 1, scopes + 1, evid} {
					t.Fatalf("只该多出新候选的一行、一条 add 修订、一个范围：%v → %v", [4]int{facts, revs, scopes, evid}, got)
				}
				add := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'add'`, idOf(o.Ref)))
				if add["wants_supersede"] != id || add["status"] != "candidate" {
					t.Fatalf("add 修订里该记想取代谁：%v", add)
				}
				if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE op = 'supersede'`); n != 0 {
					t.Fatalf("没有任何 supersede 修订：%d 条", n)
				}
				if got := str(t, m, `SELECT coalesce(confirmed_by, '-') || '|' || coalesce(speech_act, '-') FROM facts WHERE id = ?`, idOf(o.Ref)); got != "-|-" {
					t.Fatalf("新的不带放行和 speech_act：%q", got)
				}
			})
		}
	}
}

// ①'、③：取代这个动作本身（整理提升候选时会调）和 remember 带 replaces。整理只取代 confirmed_by = 'ai' 的有效事实；主人自己可以取代任何有效事实
func TestSupersedeProtectionMatrix(t *testing.T) {
	t.Parallel()
	intended(t, "summary-supersede-deferred-whitelist")
	for _, sh := range protectShapes {
		for _, by := range []string{BySummary, ByRemember} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, sink := newWritable(t, nil)
				id := seedShape(t, m, sh, "主人住在北京")
				row0 := rowOf(t, m, id)
				want := supersedeGone
				switch {
				case !sh.active():
				case by == ByRemember || sh.aiMayTouch:
					want = supersedeDone
				default:
					want = supersedeProtected
				}
				got, counts := supersedeDirect(t, m, by, id, "NEWER")
				if got != want {
					t.Fatalf("结局 %v，该是 %v", got, want)
				}
				switch want {
				case supersedeDone:
					if r := rowOf(t, m, id); !strings.HasPrefix(r, "superseded|NEWER|") {
						t.Fatalf("该被取代：%s", r)
					}
					if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'supersede'`, id); n != 1 {
						t.Fatalf("该记一条 supersede 修订：%d", n)
					}
					if len(counts) != 0 {
						t.Fatalf("取代成了不数保护：%v", counts)
					}
				case supersedeProtected:
					if r := rowOf(t, m, id); r != row0 {
						t.Fatalf("受保护的一个字节都不该变：\n前 %s\n后 %s", row0, r)
					}
					if counts[CountSupersedeProtected] != 1 || len(counts) != 1 {
						t.Fatalf("该数 supersede_protected：%v", counts)
					}
					log := sink.String()
					if !strings.Contains(log, "f:"+strings.ToUpper(id[len(id)-6:])) || !strings.Contains(log, "protected") || strings.Contains(log, "主人住在北京") {
						t.Fatalf("日志该有短码和原因码、不带正文：%q", log)
					}
				default:
					if r := rowOf(t, m, id); r != row0 {
						t.Fatalf("不是有效的：不动，也不记修订：\n前 %s\n后 %s", row0, r)
					}
					if len(counts) != 0 {
						t.Fatalf("没有可取代的不数保护：%v", counts)
					}
				}
			})
		}
		// ③ remember 带 replaces（写入层：ownerActs）：任何有效的都能换，不是有效的整批作废
		t.Run(sh.name+"／remember 带 replaces", func(t *testing.T) {
			t.Parallel()
			m, _ := newWritable(t, nil)
			id := seedShape(t, m, sh, "主人住在北京")
			f := ownerFact("主人搬到了上海")
			f.Supersedes = id
			sizes, row0 := tableSizes(t, m), rowOf(t, m, id)
			res, err := m.apply(context.Background(), remember(f))
			if sh.active() {
				if err != nil || res.Outcomes[0].Replaced == nil || res.Outcomes[0].Replaced.Text != "主人住在北京" || !strings.HasPrefix(rowOf(t, m, id), "superseded|") {
					t.Fatalf("主人自己该取代得了：%v %+v", err, res.Outcomes)
				}
				return
			}
			if !errors.Is(err, ErrReplaceGone) || tableSizes(t, m) != sizes || rowOf(t, m, id) != row0 {
				t.Fatalf("不是有效的该整批作废：%v，%s → %s", err, sizes, tableSizes(t, m))
			}
		})
	}
}

// ②：整理的 revoke。整理只撤销 confirmed_by = 'ai' 的有效事实，受保护的计 revoke_protected（不是 revoke_noop）；不是有效的计 revoke_noop；主人自己可以撤销任何有效的
func TestRevokeProtectionMatrix(t *testing.T) {
	t.Parallel()
	intended(t, "summary-supersede-deferred-whitelist")
	for _, sh := range protectShapes {
		for _, by := range []string{BySummary, ByRemember} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, sink := newWritable(t, nil)
				id := seedShape(t, m, sh, "主人讨厌香菜")
				row0 := rowOf(t, m, id)
				revoked, protected := sh.active() && (by == ByRemember || sh.aiMayTouch), sh.active() && by == BySummary && !sh.aiMayTouch

				res := mustApply(t, m, Batch{By: by, Ops: []Op{RevokeOp{ID: id}}})
				o := res.Outcomes[0]
				switch {
				case revoked:
					if o.Result != ResultRevoked || o.Status != "revoked" || res.Counts[CountRevoked] != 1 || len(res.Counts) != 1 {
						t.Fatalf("该撤销成：%+v %v", o, res.Counts)
					}
					if r := rowOf(t, m, id); !strings.HasPrefix(r, "revoked|") || count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'revoke'`, id) != 1 {
						t.Fatalf("该是撤销、记一条 revoke 修订：%s", r)
					}
				case protected:
					if o.Result != ResultNoop || res.Counts[CountRevokeProtected] != 1 || len(res.Counts) != 1 {
						t.Fatalf("该被保护挡下：%+v %v", o, res.Counts)
					}
					if r := rowOf(t, m, id); r != row0 {
						t.Fatalf("受保护的一个字节都不该变：\n前 %s\n后 %s", row0, r)
					}
					log := sink.String()
					if !strings.Contains(log, "f:"+strings.ToUpper(id[len(id)-6:])) || !strings.Contains(log, "protected") || strings.Contains(log, "主人讨厌香菜") {
						t.Fatalf("日志该有短码和原因码、不带正文：%q", log)
					}
				default:
					if o.Result != ResultNoop || res.Counts[CountRevokeNoop] != 1 || len(res.Counts) != 1 {
						t.Fatalf("不是有效的：该是 revoke_noop：%+v %v", o, res.Counts)
					}
					if r := rowOf(t, m, id); r != row0 {
						t.Fatalf("不是有效的：不动，也不记修订：\n前 %s\n后 %s", row0, r)
					}
				}
			})
		}
	}
}

// ⑤：整理又说起同一句（同文合并）：只加一次证据，状态和谁放行的不变，也不因为合并受保护或解除保护
func TestSummaryMergeKeepsStanding(t *testing.T) {
	t.Parallel()
	for _, sh := range protectShapes {
		if !sh.active() {
			continue
		}
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritable(t, nil)
			id := seedShape(t, m, sh, "主人喜欢喝茶")
			row0 := rowOf(t, m, id)
			dup := candidate("主人，喜欢喝茶。", "owner", "owner")
			dup.Evidence = &Evidence{Conversation: GA, Speaker: "owner", BySubject: false}
			res := mustApply(t, m, summary(dup))
			if res.Outcomes[0].Result != ResultMerged || res.Counts[CountEvidenceAdded] != 1 || res.Counts[CountSupersedeDeferred] != 0 {
				t.Fatalf("该合并并加一次证据：%+v %v", res.Outcomes[0], res.Counts)
			}
			if got := rowOf(t, m, id); got != row0 {
				t.Fatalf("合并不改状态、谁放行的、范围、修订：\n前 %s\n后 %s", row0, got)
			}
		})
	}
}

// 计数键进 memory_jobs.counts_json（W6 的报告要数）；撤销被挡下和取代被延后是两个键
func TestCountsRecordProtected(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	imported := seedShape(t, m, protectShapes[0], "主人住在北京")
	owned := seedShape(t, m, protectShapes[3], "主人不吃香菜")
	res := mustApply(t, m, Batch{By: BySummary, Job: &Job{Conversation: "console:z", From: 1, To: 2, Version: 1},
		Ops: []Op{RevokeOp{ID: imported}, RevokeOp{ID: owned}, candidateWanting("主人搬到了上海", imported)}})
	if res.Counts[CountRevokeProtected] != 2 || res.Counts[CountSupersedeDeferred] != 1 {
		t.Fatalf("计数：%v", res.Counts)
	}
	raw := jsonMap(t, str(t, m, `SELECT counts_json FROM memory_jobs WHERE conversation = 'console:z'`))
	if raw[CountRevokeProtected] != float64(2) || raw[CountSupersedeDeferred] != float64(1) {
		t.Fatalf("memory_jobs.counts_json 里该有这两个键：%v", raw)
	}
}

// ④：主人再说一遍就是放行。remember 遇到同文的有效事实，confirmed_by 不是 owner、而且 remember 查到的最近一句话是主人本人说的，就提成 owner：
// 记一条 confirm 修订，范围一个字不动；之后整理再想取代、撤销它都被挡下。查不到原话、是群友说的、身份字段对不上，只回"已经记着了"，不动
func TestRememberSameTextNeverClaimsOwnerConfirmation(t *testing.T) {
	t.Parallel()
	intended(t, "remember-same-text-confirms-active")
	const text = "主人养了一只猫"
	other := Event{Seq: 9, Conversation: tlOther, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "别的会话里的话", Seen: true}
	saids := map[string]struct {
		setup  func(ev *sqlEvents)
		raises bool // 手写的预期：这一句算"主人本人说的"吗
	}{
		"主人本人说的最近一句": {func(ev *sqlEvents) { ev.add(ownerSays(tlMain, "我养了一只猫", time.Minute)) }, true},
		"查不到原话":      {func(ev *sqlEvents) {}, false},
		"最近一句是群友说的": {func(ev *sqlEvents) {
			ev.add(ownerSays(tlMain, "我养了一只猫", 2*time.Minute))
			ev.add(peerSays(tlMain, "10001", "阿福", "对，他养了一只猫", time.Minute))
		}, false},
		"名字叫主人的群友（ActorKind 不是 owner）": {func(ev *sqlEvents) {
			ev.said = func(conv string) (Event, bool, error) {
				return Event{Seq: 9, Conversation: conv, At: evAt(time.Minute), ActorKind: "human", ActorID: "qq:10001", ActorName: "主人", Origin: "owner", Kind: "message", Text: "我养了一只猫", Seen: true}, true, nil
			}
		}, false},
		"查到的是别的会话的话": {func(ev *sqlEvents) {
			ev.said = func(conv string) (Event, bool, error) { return other, true, nil }
		}, false},
	}
	for _, sh := range protectShapes {
		if !sh.active() {
			continue
		}
		for name, said := range saids {
			t.Run(sh.name+"／"+name, func(t *testing.T) {
				t.Parallel()
				ev := newSQLEvents(t)
				said.setup(ev)
				m, _ := openTools(t, ev)
				id := seedShape(t, m, sh, text)
				scopes0 := str(t, m, `SELECT group_concat(scope) FROM item_scopes WHERE ref = ?`, "f:"+id)
				facts0 := count(t, m, `SELECT count(*) FROM facts`)

				res := remembered(t, m, ownerHere, RememberArgs{Kind: "fact", Text: text})
				if said.raises && res != (ToolResult{Text: "这件事已经记着了。"}) {
					t.Fatalf("成功文字：%+v", res)
				}
				wantBy, wantRevs := sh.confirmed, 0
				if sh.confirmed == "ai" && !res.IsError {
					// 来源绑定了主人本人的原话（"查不到原话"这一格由测试辅助函数补了一句主人原话）：同文的 ai 事实升成 owner_said、记一条 confirm 修订；
					// 没绑上来源的几格 remember 直接拒写（IsError），ai 不变
					wantBy, wantRevs = ConfirmedOwnerSaid, 1
				}
				gotBy := str(t, m, `SELECT coalesce(confirmed_by, '<NULL>') FROM facts WHERE id = ?`, id)
				if w, _ := wantBy.(string); (wantBy == nil && gotBy != "<NULL>") || (wantBy != nil && gotBy != w) {
					t.Fatalf("confirmed_by 现在是 %q，预期 %v", gotBy, wantBy)
				}
				if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id); n != wantRevs {
					t.Fatalf("confirm 修订 %d 条，预期 %d", n, wantRevs)
				}
				if got := str(t, m, `SELECT group_concat(scope) FROM item_scopes WHERE ref = ?`, "f:"+id); got != scopes0 {
					t.Fatalf("范围一个字不动：%q → %q", scopes0, got)
				}
				if st := str(t, m, `SELECT status FROM facts WHERE id = ?`, id); st != "active" || count(t, m, `SELECT count(*) FROM facts`) != facts0 {
					t.Fatalf("还是同一条有效的事实，不新写：%s", st)
				}

			})
		}
	}
}

// 写入层自己也不信调用方：OwnerSaid 只在 remember 的批里起作用，整理的批里设了也不放行
func TestOwnerSaidOnlyCountsInRememberBatches(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	id := seedShape(t, m, protectShapes[0], "主人养了一只猫")
	row0 := rowOf(t, m, id)
	f := ownerFact("主人养了一只猫")
	f.OwnerSaid = true
	res := mustApply(t, m, Batch{By: BySummary, OnDup: DupReport, Ops: []Op{f}})
	if res.Outcomes[0].Result != ResultExists || res.Counts[CountOwnerConfirmed] != 0 || rowOf(t, m, id) != row0 {
		t.Fatalf("整理的批里 OwnerSaid 不起作用：%+v %v", res.Outcomes[0], res.Counts)
	}
	res = mustApply(t, m, remember(f))
	if res.Counts[CountOwnerConfirmed] != 1 || !strings.Contains(rowOf(t, m, id), "|owner|") {
		t.Fatalf("remember 的批里该放行：%v", res.Counts)
	}
}
