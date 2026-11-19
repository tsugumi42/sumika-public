package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// owner_said 保护（规格-记忆质量"remember 写的事实怎么保护"）：remember 写的事实标 owner_said，
// 整理（后台总结）取代、撤销、合并改期限、提升候选时取代都动不了；remember 自己（ByRememberAI）和主人本人动得了。
// 下面的预期都是手写的表，不调被测的 protectedFrom。名字、号码、会话都是编的

type osShape struct {
	name      string
	status    string
	confirmed any  // nil 是 NULL（导入的）
	expired   bool // 有效期已过（状态仍是 active）
	fake      bool // 库里没有这个编号
	// 手写的预期：这个形状的有效事实，整理动得了吗、remember（ByRememberAI）动得了吗。不是有效的两边都动不了
	summaryMay, rememberMay bool
}

func (s osShape) active() bool { return !s.fake && s.status == "active" }

var osShapes = []osShape{
	{name: "owner_said 有效", status: "active", confirmed: ConfirmedOwnerSaid, rememberMay: true},
	{name: "ai 有效", status: "active", confirmed: "ai", summaryMay: true, rememberMay: true},
	{name: "owner 有效", status: "active", confirmed: "owner"},
	{name: "导入的 NULL", status: "active", confirmed: nil},
	{name: "导入的空串", status: "active", confirmed: ""},
	{name: "owner_said 已过期", status: "active", confirmed: ConfirmedOwnerSaid, expired: true, rememberMay: true},
	{name: "owner_said 大小写不对", status: "active", confirmed: "Owner_Said"},
	{name: "owner_said 已撤销", status: "revoked", confirmed: ConfirmedOwnerSaid},
	{name: "owner_said 被取代", status: "superseded", confirmed: ConfirmedOwnerSaid},
	{name: "编号不存在", fake: true},
}

const osFakeID = "NOSUCHFACTID"

// seedOS 往库里放一条这个形状的事实（旧的、有期限的现状事实），返回编号。不存在的形状只给一个假编号
func seedOS(t *testing.T, m *Memory, sh osShape, text string) string {
	t.Helper()
	if sh.fake {
		return osFakeID
	}
	id := seedShape(t, m, protectShape{name: sh.name, status: sh.status, confirmed: sh.confirmed}, text)
	until := toolsNow.Add(-time.Hour)
	if !sh.expired {
		until = toolsNow.Add(48 * time.Hour)
	}
	if _, err := m.w.db.Exec(`UPDATE facts SET temporal = 'temporary', valid_to = ?, event_at = ? WHERE id = ?`, until.UnixMilli(), toolsNow.Add(-24*time.Hour).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func osMay(sh osShape, by string) bool {
	if !sh.active() {
		return false
	}
	if by == ByRememberAI {
		return sh.rememberMay
	}
	return sh.summaryMay
}

// 列：整理直接取代、remember 取代（动作层）
func TestOwnerSaidSupersedeAction(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		for _, by := range []string{BySummary, ByRememberAI} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, _ := newWritable(t, nil)
				id := seedOS(t, m, sh, "主人住在北京")
				row0 := ""
				if !sh.fake {
					row0 = rowOf(t, m, id)
				}
				want := supersedeGone
				switch {
				case !sh.active():
				case osMay(sh, by):
					want = supersedeDone
				default:
					want = supersedeProtected
				}
				got, counts := supersedeDirect(t, m, by, id, "NEWER")
				if got != want {
					t.Fatalf("结局 %v，该是 %v", got, want)
				}
				if sh.fake {
					return
				}
				switch want {
				case supersedeDone:
					if r := rowOf(t, m, id); !strings.HasPrefix(r, "superseded|NEWER|") || len(counts) != 0 {
						t.Fatalf("该被取代：%s %v", r, counts)
					}
				case supersedeProtected:
					if r := rowOf(t, m, id); r != row0 || counts[CountSupersedeProtected] != 1 {
						t.Fatalf("受保护的一个字节都不该变、该数 supersede_protected：%s → %s %v", row0, r, counts)
					}
				default:
					if r := rowOf(t, m, id); r != row0 || len(counts) != 0 {
						t.Fatalf("不是有效的：不动不数：%s → %s %v", row0, r, counts)
					}
				}
			})
		}
	}
}

// 列：整理撤销、remember 撤销（动作层；工具层没有撤销入口）
func TestOwnerSaidRevokeAction(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		for _, by := range []string{BySummary, ByRememberAI} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, _ := newWritable(t, nil)
				id := seedOS(t, m, sh, "主人讨厌香菜")
				row0 := ""
				if !sh.fake {
					row0 = rowOf(t, m, id)
				}
				res := mustApply(t, m, Batch{By: by, Ops: []Op{RevokeOp{ID: id}}})
				o := res.Outcomes[0]
				switch {
				case osMay(sh, by):
					if o.Result != ResultRevoked || res.Counts[CountRevoked] != 1 || !strings.HasPrefix(rowOf(t, m, id), "revoked|") {
						t.Fatalf("该撤销成：%+v %v", o, res.Counts)
					}
				case sh.active():
					if o.Result != ResultNoop || res.Counts[CountRevokeProtected] != 1 || len(res.Counts) != 1 || rowOf(t, m, id) != row0 {
						t.Fatalf("该被保护挡下、一个字节不变：%+v %v", o, res.Counts)
					}
				default:
					if o.Result != ResultNoop || res.Counts[CountRevokeNoop] != 1 || len(res.Counts) != 1 || (!sh.fake && rowOf(t, m, id) != row0) {
						t.Fatalf("不是有效的：该是 revoke_noop、不动：%+v %v", o, res.Counts)
					}
				}
			})
		}
	}
}

// 列：整理候选提升时取代。候选写入时记下想取代谁；三次证据攒够、开着自动提升，旧的是整理动得了的才取代，
// 受保护的这条候选不提升（confirm_blocked_protected），旧的不是有效的照常提升
func TestOwnerSaidBlocksPromotionReplace(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritableWith(t, autoConfirm)
			id := seedOS(t, m, sh, "主人住在北京")
			row0 := ""
			if !sh.fake {
				row0 = rowOf(t, m, id)
			}
			wanting := candidate("主人搬到了上海", "owner", "owner")
			wanting.Supersedes = id
			repl := idOf(mustApply(t, m, summary(wanting)).Outcomes[0].Ref)
			var all []Applied
			for d := 1; d <= 3; d++ {
				all = append(all, mustApply(t, m, summary(cev{cGroupB, d, "owner", true}.viaEvidence(repl))))
			}
			blocked := sumCounts(all, CountConfirmBlockedProtected)
			switch {
			case osMay(sh, BySummary):
				if statusOf(t, m, repl) != "active" || !strings.HasPrefix(rowOf(t, m, id), "superseded|"+repl+"|") || blocked != 0 {
					t.Fatalf("整理动得了的：该取代并提升：新 %s 旧 %s 挡下 %d", statusOf(t, m, repl), rowOf(t, m, id), blocked)
				}
			case sh.active():
				if statusOf(t, m, repl) != "candidate" || rowOf(t, m, id) != row0 || blocked == 0 {
					t.Fatalf("受保护的：候选不提升、旧的一个字节不变、计 confirm_blocked_protected：新 %s 旧 %s 挡下 %d", statusOf(t, m, repl), rowOf(t, m, id), blocked)
				}
			default:
				if statusOf(t, m, repl) != "active" || blocked != 0 || (!sh.fake && rowOf(t, m, id) != row0) {
					t.Fatalf("旧的不是有效的：照常提升、旧的不动：新 %s 挡下 %d", statusOf(t, m, repl), blocked)
				}
			}
		})
	}
}

// 列：remember 取代（真实工具路径：改口、主人再说一遍换了说法）
func TestOwnerSaidRememberReplaceViaTool(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := openTools(t, newSQLEvents(t))
			id := seedOS(t, m, sh, "主人住在北京")
			code := "ZZZZZZ"
			row0 := ""
			if !sh.fake {
				code, row0 = id[len(id)-6:], rowOf(t, m, id)
			}
			w0 := writes(t, m)
			res := remembered(t, m, ownerHere, RememberArgs{Kind: "fact", Text: "主人搬到了上海", Replaces: code})
			switch {
			case osMay(sh, ByRememberAI):
				if res.IsError || !strings.Contains(res.Text, "主人住在北京") || !strings.HasPrefix(rowOf(t, m, id), "superseded|") {
					t.Fatalf("remember 改口该成：%+v %s", res, rowOf(t, m, id))
				}
				if got := str(t, m, `SELECT confirmed_by FROM facts WHERE text = '主人搬到了上海'`); got != ConfirmedOwnerSaid {
					t.Fatalf("新写的也标 owner_said：%q", got)
				}
			case sh.active():
				if !res.IsError || !strings.Contains(res.Text, "保护") || rowOf(t, m, id) != row0 || writes(t, m) != w0 {
					t.Fatalf("受保护的：回明确未修改、一个字节不写：%+v", res)
				}
			default:
				if !res.IsError || writes(t, m) != w0 || (!sh.fake && rowOf(t, m, id) != row0) {
					t.Fatalf("不是有效的：整批作废：%+v", res)
				}
			}
		})
	}
}

// 列：remember 同文再说一遍刷新期限；整理的同文合并（带本人证据）不能刷新
func TestOwnerSaidRefreshLifetimeOnlyForRemember(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		if sh.fake {
			continue
		}
		for _, by := range []string{ByRememberAI, BySummary} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, _ := openTools(t, newSQLEvents(t))
				id := seedOS(t, m, sh, "主人本周赶稿")
				validTo := func() string { return str(t, m, `SELECT coalesce(valid_to, -1) FROM facts WHERE id = ?`, id) }
				before := validTo()
				until := toolsNow.Add(72 * time.Hour).UnixMilli()
				f := aiFact("主人本周赶稿")
				f.Temporal, f.ValidTo, f.EventAt = "temporary", &until, toolsNow.UnixMilli()
				onDup := DupMerge
				if by == ByRememberAI {
					f.ConfirmedBy, onDup = ConfirmedOwnerSaid, DupReport
				} else {
					f.Evidence = &Evidence{Conversation: tlMain, At: f.EventAt, Seq: 1, Speaker: "owner", BySubject: true}
				}
				mustApply(t, m, Batch{By: by, OnDup: onDup, Ops: []Op{f}})
				refreshed := validTo() != before
				if want := osMay(sh, by); refreshed != want {
					t.Fatalf("期限刷新了=%v，该是 %v（前 %s 后 %s）", refreshed, want, before, validTo())
				}
			})
		}
	}
}

// 校验：remember 的批只收 owner_said，别的批不许写 owner_said
func TestOwnerSaidValidate(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	for _, tc := range []struct {
		by, confirmed string
		ok            bool
	}{
		{ByRememberAI, ConfirmedOwnerSaid, true}, {ByRememberAI, "ai", false}, {ByRememberAI, "owner", false}, {ByRememberAI, "", false},
		{BySummary, ConfirmedOwnerSaid, false}, {ByRemember, ConfirmedOwnerSaid, false}, {BySummary, "ai", true}, {ByRemember, "owner", true},
	} {
		f := aiFact("主人喜欢" + tc.by + tc.confirmed)
		f.ConfirmedBy = tc.confirmed
		_, err := m.apply(t.Context(), Batch{By: tc.by, OnDup: DupReport, Ops: []Op{f}})
		if (err == nil) != tc.ok {
			t.Errorf("By=%s confirmed_by=%q：err=%v，该 ok=%v", tc.by, tc.confirmed, err, tc.ok)
		}
	}
}

// 端到端：模型调 remember 记下，之后整理想撤销、想用候选顶掉它，都被挡下；remember 自己改口仍行
func TestOwnerSaidEndToEndFromTool(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, func(o *Options) { autoConfirm(o); o.Events = newSQLEvents(t) })
	if res := remembered(t, m, ownerHere, RememberArgs{Kind: "preference", Text: "主人喜欢黑咖啡"}); res.IsError {
		t.Fatalf("%+v", res)
	}
	id := str(t, m, `SELECT id FROM facts WHERE text = '主人喜欢黑咖啡'`)
	if got := str(t, m, `SELECT confirmed_by FROM facts WHERE id = ?`, id); got != ConfirmedOwnerSaid {
		t.Fatalf("confirmed_by=%q", got)
	}
	res := mustApply(t, m, Batch{By: BySummary, Ops: []Op{RevokeOp{ID: id}}})
	if res.Counts[CountRevokeProtected] != 1 || statusOf(t, m, id) != "active" {
		t.Fatalf("整理撤销该被挡下：%v %s", res.Counts, statusOf(t, m, id))
	}
	wanting := candidate("主人改喝茶了", "owner", "owner")
	wanting.Supersedes = id
	repl := idOf(mustApply(t, m, summary(wanting)).Outcomes[0].Ref)
	for d := 1; d <= 3; d++ {
		mustApply(t, m, summary(cev{cGroupB, d, "owner", true}.viaEvidence(repl)))
	}
	if statusOf(t, m, repl) != "candidate" || statusOf(t, m, id) != "active" {
		t.Fatalf("三次证据压不过去：新 %s 旧 %s", statusOf(t, m, repl), statusOf(t, m, id))
	}
	if res := remembered(t, m, ownerHere, RememberArgs{Kind: "preference", Text: "主人改喝拿铁了", Replaces: id[len(id)-6:]}); res.IsError || statusOf(t, m, id) != "superseded" {
		t.Fatalf("remember 自己改口该行：%+v %s", res, statusOf(t, m, id))
	}
}

// 列：remember 同文再说一遍时，只把 ai 有效事实升成 owner_said（记一条 confirm 修订、计数）；owner、导入的、已撤销的不动；整理的同文合并从不升
func TestOwnerSaidUpgradeOnSameText(t *testing.T) {
	t.Parallel()
	for _, sh := range osShapes {
		if sh.fake {
			continue
		}
		for _, by := range []string{ByRememberAI, BySummary} {
			t.Run(sh.name+"／"+by, func(t *testing.T) {
				t.Parallel()
				m, _ := openTools(t, newSQLEvents(t))
				id := seedOS(t, m, sh, "主人养了一只猫")
				row0 := rowOf(t, m, id)
				scopes0 := str(t, m, `SELECT group_concat(scope) FROM item_scopes WHERE ref = ?`, "f:"+id)
				f := aiFact("主人养了一只猫")
				f.Temporal = "stable"
				onDup := DupMerge
				if by == ByRememberAI {
					// 来源是更晚的主人原话：已过期的同文也靠它刷新期限、不被当成过期拒写
					f.ConfirmedBy, onDup, f.EventAt = ConfirmedOwnerSaid, DupReport, toolsNow.UnixMilli()
				} else {
					f.Evidence = &Evidence{Conversation: tlMain, At: toolsNow.UnixMilli(), Seq: 1, Speaker: "owner", BySubject: true}
				}
				res := mustApply(t, m, Batch{By: by, OnDup: onDup, Ops: []Op{f}})
				confirmedNow := str(t, m, `SELECT coalesce(confirmed_by, '<NULL>') FROM facts WHERE id = ?`, id)
				confirms := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id)
				upgrade := by == ByRememberAI && sh.active() && sh.confirmed == "ai"
				if upgrade {
					if confirmedNow != ConfirmedOwnerSaid || confirms != 1 || res.Counts[CountOwnerSaidUpgraded] != 1 {
						t.Fatalf("该升成 owner_said、记一条 confirm 修订、计数：%s %d %v", confirmedNow, confirms, res.Counts)
					}
					rev := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id))
					if rev["confirmed_by"] != ConfirmedOwnerSaid {
						t.Fatalf("修订里该记新值：%v", rev)
					}
					if got := str(t, m, `SELECT group_concat(scope) FROM item_scopes WHERE ref = ?`, "f:"+id); got != scopes0 {
						t.Fatalf("范围不动：%q → %q", scopes0, got)
					}
					// 升了以后整理撤销、取代都动不了
					r := mustApply(t, m, Batch{By: BySummary, Ops: []Op{RevokeOp{ID: id}}})
					if r.Counts[CountRevokeProtected] != 1 || statusOf(t, m, id) != "active" {
						t.Fatalf("升了以后整理该动不了：%v %s", r.Counts, statusOf(t, m, id))
					}
					return
				}
				if res.Counts[CountOwnerSaidUpgraded] != 0 || confirms != 0 {
					t.Fatalf("不该升：计数 %v，confirm 修订 %d", res.Counts, confirms)
				}
				if sh.active() && by == ByRememberAI && confirmedNow != fmt.Sprint(orNull(sh.confirmed)) {
					t.Fatalf("confirmed_by 不该变：%s", confirmedNow)
				}
				if sh.active() && rowOf(t, m, id) != row0 && by == ByRememberAI && sh.confirmed != ConfirmedOwnerSaid {
					// owner_said 同文会刷新期限、但不记 confirm；这里只查 owner、导入的整行不变（期限刷新只给 ai、owner_said）
					t.Fatalf("整行不该变：\n前 %s\n后 %s", row0, rowOf(t, m, id))
				}
			})
		}
	}
}

func orNull(v any) any {
	if v == nil {
		return "<NULL>"
	}
	return v
}
