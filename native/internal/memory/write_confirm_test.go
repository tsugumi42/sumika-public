package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// 待确认攒够证据变有效（规格-记忆写入上线 W3b）：计次规则、种类、记账模式、想取代旧事实的候选、范围不动、并发。
// 下面每一格的预期都是照规格的"形状 × 入口"表手写的，不调被测的 tally / confirmByEvidence。名字、号码、会话都是编的

// newWritableWith：造一个迁移过的空库，以可写方式打开，Options 由 mod 改
func newWritableWith(t *testing.T, mod func(*Options)) (*Memory, *logSink) {
	t.Helper()
	sink := &logSink{}
	opt := Options{Writable: true, Character: "suzune", Logf: sink.logf}
	if mod != nil {
		mod(&opt)
	}
	m, err := Open(context.Background(), migratedDB(t, nil), nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, sink
}

func autoConfirm(o *Options) { o.AutoConfirm = true }

// cev 是一次证据：在哪个会话、哪一天、谁说的、是不是本人说的
type cev struct {
	conv    string
	day     int
	speaker string
	self    bool
}

func (e cev) evidence() Evidence {
	return Evidence{Conversation: e.conv, At: evDay(e.day), Seq: int64(e.day), Speaker: e.speaker, BySubject: e.self}
}

// 两个入口：evidence 动作，和同文合并（整理又写了一遍同一句，带这次的证据）
func (e cev) viaEvidence(id string) Op { return EvidenceOp{FactID: id, Evidence: e.evidence()} }
func (e cev) viaMerge(text, subject string) Op {
	f := candidate(text, subject, "public") // 范围同候选自己：合并不另加范围
	ev := e.evidence()
	f.Evidence = &ev
	return f
}

const (
	cGroupA = "qq:group:5001001"
	cGroupB = "qq:group:5002002"
	cDM     = "qq:private:20001:7001"
	cSelf   = "qq:60001" // 候选讲的那个人（阿萍）
	cOther1 = "qq:60002"
	cOther2 = "qq:60003"
	cOther3 = "qq:60004"
)

// 在库里放一条范围是 public 的候选（public 对谁都看得见，这一组测试只管计次规则，可见性在 write_hidden_test.go）
func seedPublicCandidate(t *testing.T, m *Memory, kind string) string {
	t.Helper()
	f := candidate(hiddenText, cSelf, "public")
	f.Kind = kind
	return idOf(mustApply(t, m, summary(f)).Outcomes[0].Ref)
}

func statusOf(t *testing.T, m *Memory, id string) string {
	t.Helper()
	return str(t, m, `SELECT status FROM facts WHERE id = ?`, id)
}

func sumCounts(all []Applied, key string) (n int) {
	for _, a := range all {
		n += a.Counts[key]
	}
	return n
}

// 形状 × 入口：每一格证据怎么攒，预期提升不提升。最后一次证据分别走 evidence 动作和同文合并两个入口，结果必须一样；
// 提升只能发生在最后一次证据上（前面的每一步都还是待确认）
func TestConfirmByEvidenceMatrix(t *testing.T) {
	t.Parallel()
	intended(t, "summary-evidence-confirm")
	type tc struct {
		name string
		kind string // 空 = fact
		mod  func(*Options)
		evs  []cev
		want bool // 开着自动提升时最后提升了；记账模式下对应的是数到一次 would_confirm
	}
	self := func(conv string, day int) cev { return cev{conv, day, cSelf, true} }
	other := func(who string, conv string, day int) cev { return cev{conv, day, who, false} }
	cases := []tc{
		{name: "本人三天各一次", evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}, want: true},
		{name: "本人一次加两个不同的别人", evs: []cev{self(cGroupA, 1), other(cOther1, cGroupA, 2), other(cOther2, cGroupA, 3)}, want: true},
		{name: "本人两次加一个别人", evs: []cev{self(cGroupA, 1), self(cGroupA, 2), other(cOther1, cGroupA, 3)}, want: true},
		{name: "三个不同的群各一次（同一天）", evs: []cev{self(cGroupA, 1), self(cGroupB, 1), self(cDM, 1)}, want: true},
		{name: "差一次", evs: []cev{self(cGroupA, 1), self(cGroupA, 2)}},
		{name: "同一天同一个会话说了五遍：算一次", evs: []cev{self(cGroupA, 1), self(cGroupA, 1), self(cGroupA, 1), self(cGroupA, 1), self(cGroupA, 1)}},
		{name: "三次都是别人转述", evs: []cev{other(cOther1, cGroupA, 1), other(cOther2, cGroupA, 2), other(cOther3, cGroupA, 3)}},
		{name: "同一个别人隔三天各转述一遍", evs: []cev{other(cOther1, cGroupA, 1), other(cOther1, cGroupA, 2), other(cOther1, cGroupA, 3)}},
		{name: "一次本人加同一个别人转述两天：本人 1 加转述 1 才 2 次", evs: []cev{self(cGroupA, 1), other(cOther1, cGroupA, 2), other(cOther1, cGroupA, 3)}},
		{name: "一次本人加同一个别人转述四天：还是 2 次", evs: []cev{self(cGroupA, 1), other(cOther1, cGroupA, 2), other(cOther1, cGroupA, 3), other(cOther1, cGroupA, 4), other(cOther1, cGroupA, 5)}},
		{name: "说话人的写法大小写、空白不同：还是同一个人", evs: []cev{self(cGroupA, 1), other("qq:60002", cGroupA, 2), other(" QQ:60002 ", cGroupA, 3)}},
		{name: "转述的没记说话人：认不出是谁，不算", evs: []cev{self(cGroupA, 1), other("", cGroupA, 2), other("", cGroupA, 3)}},
		{name: "没记说话人的不算，其余两个不同的别人才算", evs: []cev{self(cGroupA, 1), other("", cGroupA, 2), other(cOther1, cGroupA, 3), other(cOther2, cGroupA, 4)}, want: true},
		{name: "promise 默认不自动提升", kind: "promise", evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}},
		{name: "boundary 默认不自动提升", kind: "boundary", evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}},
		{name: "preference 默认可以", kind: "preference", evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}, want: true},
		{name: "配置里加了 promise 就可以", kind: "promise", mod: func(o *Options) { o.ConfirmKinds = []string{"fact", "promise"} },
			evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}, want: true},
		{name: "配置成空列表：什么都不自动提升", mod: func(o *Options) { o.ConfirmKinds = []string{} },
			evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}},
		{name: "配置里没有 fact", mod: func(o *Options) { o.ConfirmKinds = []string{"preference"} },
			evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}},
		{name: "配置里写了不认识的种类：忽略，fact 也不因此提升", mod: func(o *Options) { o.ConfirmKinds = []string{"mystery"} },
			evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3)}},
		{name: "要 2 次：本人加一个别人", mod: func(o *Options) { o.ConfirmOccasions = 2 }, evs: []cev{self(cGroupA, 1), other(cOther1, cGroupA, 2)}, want: true},
		{name: "要 2 次：只有一次", mod: func(o *Options) { o.ConfirmOccasions = 2 }, evs: []cev{self(cGroupA, 1)}},
		{name: "配成 1 也按 2 次算：一次不够", mod: func(o *Options) { o.ConfirmOccasions = 1 }, evs: []cev{self(cGroupA, 1)}},
		{name: "配成 1 也按 2 次算：两次够", mod: func(o *Options) { o.ConfirmOccasions = 1 }, evs: []cev{self(cGroupA, 1), self(cGroupA, 2)}, want: true},
		{name: "配成负数：用默认的 3 次", mod: func(o *Options) { o.ConfirmOccasions = -4 }, evs: []cev{self(cGroupA, 1), self(cGroupA, 2)}},
		{name: "要 5 次：四次不够", mod: func(o *Options) { o.ConfirmOccasions = 5 }, evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3), self(cGroupA, 4)}},
		{name: "要 5 次：五次够", mod: func(o *Options) { o.ConfirmOccasions = 5 }, evs: []cev{self(cGroupA, 1), self(cGroupA, 2), self(cGroupA, 3), self(cGroupA, 4), self(cGroupA, 5)}, want: true},
	}
	entries := []struct {
		name string
		last func(e cev, id string) Op
	}{
		{"最后一次走 evidence 动作", func(e cev, id string) Op { return e.viaEvidence(id) }},
		{"最后一次走同文合并", func(e cev, _ string) Op { return e.viaMerge(hiddenText, cSelf) }},
	}
	for _, c := range cases {
		for _, en := range entries {
			for _, auto := range []bool{true, false} {
				name := c.name + "／" + en.name
				if !auto {
					name += "／记账模式"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					m, _ := newWritableWith(t, func(o *Options) {
						o.AutoConfirm = auto
						if c.mod != nil {
							c.mod(o)
						}
					})
					kind := c.kind
					if kind == "" {
						kind = "fact"
					}
					id := seedPublicCandidate(t, m, kind)
					scopes0 := scopesText(t, m, id)
					var all []Applied
					for i, e := range c.evs {
						op := e.viaEvidence(id)
						if i == len(c.evs)-1 {
							op = en.last(e, id)
						}
						all = append(all, mustApply(t, m, summary(op)))
						if i < len(c.evs)-1 && statusOf(t, m, id) != "candidate" {
							t.Fatalf("第 %d 次证据（共 %d 次）就提升了", i+1, len(c.evs))
						}
					}
					wantActive := c.want && auto
					if got := statusOf(t, m, id); (got == "active") != wantActive {
						t.Errorf("状态 %s，应该是 %v", got, map[bool]string{true: "active", false: "candidate"}[wantActive])
					}
					confirmed := sumCounts(all, CountConfirmed)
					would := sumCounts(all, CountWouldConfirm)
					revs := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id)
					switch {
					case wantActive:
						if confirmed != 1 || would != 0 || revs != 1 {
							t.Errorf("提升了：confirmed=%d would_confirm=%d confirm 修订 %d", confirmed, would, revs)
						}
						if got := str(t, m, `SELECT coalesce(confirmed_by, '-') || '|' || coalesce(speech_act, '-') FROM facts WHERE id = ?`, id); !strings.HasPrefix(got, "ai|") || strings.HasSuffix(got, "|-") {
							t.Errorf("confirmed_by 该是 ai、speech_act 补上：%q", got)
						}
						if by := str(t, m, `SELECT by FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id); by != BySummary {
							t.Errorf("confirm 修订的 by %q", by)
						}
					case c.want && !auto:
						if confirmed != 0 || would != 1 || revs != 0 {
							t.Errorf("记账模式：只数 would_confirm 一次、一个字节不动：confirmed=%d would_confirm=%d confirm 修订 %d", confirmed, would, revs)
						}
					default:
						if confirmed != 0 || would != 0 || revs != 0 {
							t.Errorf("不该提升也不该记账：confirmed=%d would_confirm=%d confirm 修订 %d", confirmed, would, revs)
						}
					}
					if got := scopesText(t, m, id); got != scopes0 {
						t.Errorf("范围一个字不动：%q → %q", scopes0, got)
					}
					// 修订只增不改，记账模式不往里写"如果开了会怎样"：总数只有创建时那一条，加上提升时的 confirm
					if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ?`, id); n != 1+revs {
						t.Errorf("修订总数 %d，应该是创建那条加 %d 条 confirm", n, revs)
					}
					if !wantActive {
						if got := str(t, m, `SELECT coalesce(confirmed_by, '-') FROM facts WHERE id = ?`, id); got != "-" {
							t.Errorf("没提升的不该有 confirmed_by：%q", got)
						}
					}
				})
			}
		}
	}
}

func scopesText(t *testing.T, m *Memory, id string) string {
	t.Helper()
	return str(t, m, `SELECT coalesce(group_concat(scope, ','), '') FROM (SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope)`, "f:"+id)
}

// 新写的候选自带一次证据，不在创建它的这一批里变有效；配成要 1 次也一样（至少 2 次）
func TestNewCandidateWithItsOwnEvidenceIsNotPromoted(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, func(o *Options) { o.AutoConfirm, o.ConfirmOccasions = true, 1 })
	f := candidate(hiddenText, cSelf, "public")
	ev := cev{cGroupA, 1, cSelf, true}.evidence()
	f.Evidence = &ev
	res := mustApply(t, m, summary(f))
	id := idOf(res.Outcomes[0].Ref)
	if got := statusOf(t, m, id); got != "candidate" || res.Counts[CountConfirmed] != 0 || res.Counts[CountWouldConfirm] != 0 {
		t.Fatalf("新写的候选还是待确认：%s %v", got, res.Counts)
	}
	if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); n != 1 {
		t.Fatalf("它自带的那一次证据照记：%d", n)
	}
}

// 只动待确认的：已经是有效、撤销、被取代、AI 没通过的，哪怕证据行摆在那儿也不动、不记账。直接调提升这个动作（启动扫描和以后别的入口会这样调），形状表复用取代保护那张
func TestConfirmOnlyTouchesCandidates(t *testing.T) {
	t.Parallel()
	for _, sh := range protectShapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritableWith(t, autoConfirm)
			id := seedShape(t, m, sh, "测试用的事实 "+sh.name)
			for d := 1; d <= 3; d++ {
				if _, err := m.w.db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES (?, ?, ?, ?, 'qq:60001', 1, 1)`, id, fmt.Sprintf("2026-10-%02d", d), cGroupA, d); err != nil {
					t.Fatal(err)
				}
			}
			before := rowOf(t, m, id)
			confirmed, counts := confirmDirect(t, m, id)
			isCandidate := sh.status == "candidate"
			if confirmed != isCandidate {
				t.Errorf("提升了 %v，应该是 %v", confirmed, isCandidate)
			}
			if !isCandidate {
				if got := rowOf(t, m, id); got != before || len(counts) != 0 {
					t.Errorf("不是待确认的：一个字节不动、不记账：\n前 %s\n后 %s 计数 %v", before, got, counts)
				}
			}
		})
	}
}

// confirmDirect 在一个事务里直接调"按证据提升"这个动作，返回提升了没有和这一次数到的计数
func confirmDirect(t *testing.T, m *Memory, id string) (bool, map[string]int) {
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
	tr := &txRun{w: m.w, tx: tx, b: Batch{By: BySummary}, now: m.w.now().UnixMilli(), counts: map[string]int{}, dups: map[string]map[string][]*dupRow{}}
	ok, err := tr.confirmByEvidence(ctx, id)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m.w.needReload = true
	return ok, tr.counts
}

// 想取代旧事实的候选（W2 记在 add 修订的 wants_supersede 里）：提升时旧的还是 ai 放行的有效事实 → 同一个事务里取代它再提升；
// 旧的受保护 → 这条不提升；旧的已经不是有效的 → 照常提升、不取代。记账模式不取代任何东西，受保护的不算"会提升"
func TestConfirmWithWantsSupersede(t *testing.T) {
	t.Parallel()
	type want struct {
		newActive  bool // 新候选提升了
		oldChanged bool // 旧的被取代了
		blocked    bool // 旧的受保护挡下
	}
	byShape := map[string]want{}
	for _, sh := range protectShapes {
		w := want{newActive: true}
		switch {
		case sh.status == "active" && sh.aiMayTouch:
			w.oldChanged = true
		case sh.status == "active": // 导入的、空串、owner、不认识的值：受保护
			w = want{blocked: true}
		}
		byShape[sh.name] = w
	}
	shapes := append(append([]protectShape{}, protectShapes...), protectShape{name: "编号不存在"})
	byShape["编号不存在"] = want{newActive: true}
	for _, sh := range shapes {
		for _, auto := range []bool{true, false} {
			name := sh.name
			if !auto {
				name += "／记账模式"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				m, sink := newWritableWith(t, func(o *Options) { o.AutoConfirm = auto })
				target := "NOSUCH"
				if sh.status != "" {
					target = seedShape(t, m, sh, "主人住在北京")
				}
				old0 := ""
				if sh.status != "" {
					old0 = rowOf(t, m, target)
				}
				id := idOf(mustApply(t, m, summary(candidateWanting("主人搬到了上海", target))).Outcomes[0].Ref)
				var all []Applied
				for d := 1; d <= 3; d++ {
					all = append(all, mustApply(t, m, summary(cev{"console:a", d, "owner", true}.viaEvidence(id))))
				}
				w := byShape[sh.name]
				if auto {
					if got := statusOf(t, m, id); (got == "active") != w.newActive {
						t.Errorf("新候选状态 %s，提升该是 %v", got, w.newActive)
					}
				} else if got := statusOf(t, m, id); got != "candidate" {
					t.Errorf("记账模式新候选不该变：%s", got)
				}
				oldNow := ""
				if sh.status != "" {
					oldNow = rowOf(t, m, target)
				}
				switch {
				case auto && w.oldChanged:
					if str(t, m, `SELECT status || '|' || coalesce(superseded_by, '-') FROM facts WHERE id = ?`, target) != "superseded|"+id {
						t.Errorf("旧的该被新的取代：%s", oldNow)
					}
					if sumCounts(all, CountSuperseded) != 1 || sumCounts(all, CountConfirmed) != 1 {
						t.Errorf("该数 superseded=1 confirmed=1")
					}
					if after := jsonMap(t, str(t, m, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id)); after["superseded"] != target {
						t.Errorf("confirm 修订里该记取代了谁：%v", after)
					}
				case sh.status != "" && oldNow != old0:
					t.Errorf("旧的一个字节都不该变：\n前 %s\n后 %s", old0, oldNow)
				}
				blocked := sumCounts(all, CountConfirmBlockedProtected)
				switch {
				case w.blocked && blocked == 0:
					t.Errorf("旧的受保护：该数 confirm_blocked_protected")
				case !w.blocked && blocked != 0:
					t.Errorf("不该数 confirm_blocked_protected：%d", blocked)
				}
				if w.blocked && auto && sumCounts(all, CountSupersedeProtected) == 0 {
					t.Errorf("真提升时被挡下，取代那一步该数 supersede_protected")
				}
				if !auto {
					if sumCounts(all, CountSupersedeProtected) != 0 || strings.Contains(sink.String(), "整理想取代") {
						t.Errorf("记账模式不去试取代：不该数 supersede_protected、不该写那行日志：%q", sink.String())
					}
					if wouldN := sumCounts(all, CountWouldConfirm); (wouldN != 0) != !w.blocked {
						t.Errorf("记账模式：受保护的不算会提升，其余算：would_confirm=%d", wouldN)
					}
				}
			})
		}
	}
}

// 提升不改来源范围，也不改四种场合的召回资格；唯一变化是待确认的“听说”变成有效。
func TestConfirmKeepsScopesAndRecallEligibility(t *testing.T) {
	t.Parallel()
	for _, sh := range hiddenShapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritableWith(t, autoConfirm)
			id := idOf(mustApply(t, m, summary(candidate(hiddenText, cSelf, sh.scopes...))).Outcomes[0].Ref)
			scopes0 := scopesText(t, m, id)
			seenBy := func() (hearsay, plain []string) {
				for _, sc := range hiddenScenes {
					for _, h := range m.Recall(context.Background(), sc.a, Query{Text: hiddenText, Mode: ModeExplicit}).Hits {
						if h.Ref != "f:"+id {
							continue
						}
						if h.Hearsay {
							hearsay = append(hearsay, sc.name)
						} else {
							plain = append(plain, sc.name)
						}
					}
				}
				return
			}
			var want []string
			for _, sc := range hiddenScenes {
				want = append(want, sc.name)
			}
			hearsay, plain := seenBy()
			if !slices.Equal(hearsay, want) || len(plain) != 0 {
				t.Fatalf("提升前：每种场合都应标听说 %v，实际听说 %v、有效 %v", want, hearsay, plain)
			}
			for d := 1; d <= 3; d++ {
				mustApply(t, m, summary(cev{cGroupA, d, cSelf, true}.viaEvidence(id)))
			}
			if got := statusOf(t, m, id); got != "active" {
				t.Fatalf("该提升了：%s", got)
			}
			if got := scopesText(t, m, id); got != scopes0 {
				t.Errorf("范围一个字不动：%q → %q", scopes0, got)
			}
			hearsay, plain = seenBy()
			if !slices.Equal(plain, want) || len(hearsay) != 0 {
				t.Errorf("提升后：召回场合仍为 %v，只去掉听说；实际听说 %v、有效 %v", want, hearsay, plain)
			}
		})
	}
}

// AI 提升以后主人再说一遍：提成 owner；之后别的候选想取代它、攒够证据也被挡下（W2 的保护）
func TestRememberAfterAIConfirm(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, autoConfirm)
	id := seedPublicCandidate(t, m, "fact")
	for d := 1; d <= 3; d++ {
		mustApply(t, m, summary(cev{cGroupA, d, cSelf, true}.viaEvidence(id)))
	}
	if got := str(t, m, `SELECT status || '|' || confirmed_by FROM facts WHERE id = ?`, id); got != "active|ai" {
		t.Fatalf("先是 AI 提升的：%s", got)
	}
	same := FactOp{Kind: "fact", Text: hiddenText, Subject: cSelf, Status: "active", ConfirmedBy: "owner", Scopes: []string{"owner"}, OwnerSaid: true}
	res := mustApply(t, m, remember(same))
	if res.Counts[CountOwnerConfirmed] != 1 {
		t.Fatalf("主人本人再说一遍该放行：%v", res.Counts)
	}
	if got := str(t, m, `SELECT status || '|' || confirmed_by FROM facts WHERE id = ?`, id); got != "active|owner" {
		t.Fatalf("该提成 owner：%s", got)
	}
	// 别的候选（同一个主体：整理的 supersedes 要和旧事实是同一个人）想取代它，证据攒够了也不提升
	wanting := candidate("阿萍已经搬走了", cSelf, "owner")
	wanting.Supersedes = id
	repl := idOf(mustApply(t, m, summary(wanting)).Outcomes[0].Ref)
	var all []Applied
	for d := 1; d <= 3; d++ {
		all = append(all, mustApply(t, m, summary(cev{cGroupB, d, "owner", true}.viaEvidence(repl))))
	}
	if statusOf(t, m, repl) != "candidate" || statusOf(t, m, id) != "active" || sumCounts(all, CountConfirmBlockedProtected) == 0 {
		t.Fatalf("主人放行过的，AI 凑出来的三次证据压不过去：新 %s 旧 %s 计数 %d", statusOf(t, m, repl), statusOf(t, m, id), sumCounts(all, CountConfirmBlockedProtected))
	}
}

// 边界 ⑬：几个会话几乎同时给同一条候选记第三次证据——写锁串起来，事务里重新数，只有一个提升；后到的看到已经是有效的，不记、不再提升
func TestConfirmConcurrentThirdEvidence(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, autoConfirm)
	id := seedPublicCandidate(t, m, "fact")
	mustApply(t, m, summary(cev{cGroupA, 1, cSelf, true}.viaEvidence(id)))
	mustApply(t, m, summary(cev{cGroupA, 2, cSelf, true}.viaEvidence(id)))
	const n = 8
	var wg sync.WaitGroup
	results := make([]Applied, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.apply(context.Background(), summary(cev{fmt.Sprintf("qq:group:60%02d", i), 3, cSelf, true}.viaEvidence(id)))
			if err != nil {
				t.Errorf("apply：%v", err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	if got := sumCounts(results, CountConfirmed); got != 1 {
		t.Errorf("只有一个批次提升：%d", got)
	}
	if revs := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm'`, id); revs != 1 {
		t.Errorf("confirm 修订该只有一条：%d", revs)
	}
	if rows := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); rows != 3 || sumCounts(results, CountEvidenceGone) != n-1 {
		t.Errorf("提升以后到的不再记证据（evidence_gone）：证据行 %d，evidence_gone %d", rows, sumCounts(results, CountEvidenceGone))
	}
	if statusOf(t, m, id) != "active" {
		t.Errorf("该是有效的")
	}
}

// 库里已有的证据行（Bun 导入的、手工补的）说话人可能是空串、空白或没记：认不出是谁，不能当成一个人，一条都不算
func TestConfirmIgnoresEvidenceRowsWithBlankSpeakers(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, autoConfirm)
	id := seedPublicCandidate(t, m, "fact")
	insert := func(day string, speaker any, by int) {
		t.Helper()
		if _, err := m.w.db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES (?, ?, ?, 1, ?, ?, 1)`, id, day, cGroupA, speaker, by); err != nil {
			t.Fatal(err)
		}
	}
	insert("2026-10-01", cSelf, 1)
	insert("2026-10-02", "", 0)
	insert("2026-10-03", "   ", 0)
	insert("2026-10-04", nil, 0)
	insert("2026-10-05", cOther1, 0)
	// 本人 1 次 + 一个认得出的别人 = 2；三行认不出是谁的转述一条都不算（算的话是 3，会提升）
	if ok, counts := confirmDirect(t, m, id); ok || len(counts) != 0 || statusOf(t, m, id) != "candidate" {
		t.Fatalf("认不出是谁的转述不能当一个人：%v %v", ok, counts)
	}
	insert("2026-10-06", cOther2, 0)
	if ok, _ := confirmDirect(t, m, id); !ok || statusOf(t, m, id) != "active" {
		t.Fatalf("1 次本人加两个认得出的不同的人，该提升")
	}
}

// openWith 以可写方式再打开一个已有的库副本（重启）
func openWith(t *testing.T, file string, mod func(*Options)) *Memory {
	t.Helper()
	opt := Options{Writable: true, Character: "suzune", Logf: (&logSink{}).logf}
	if mod != nil {
		mod(&opt)
	}
	m, err := Open(context.Background(), file, nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

// 启动扫描（规格 W3b 验收 4）：记账模式下攒够证据的候选，打开自动提升的那次启动补提升；每条一个事务；种类不允许的、证据行数不够的不动；
// 想取代的旧事实受保护的不提升；再扫一遍什么都不变；没打开自动提升什么都不做；只读返回 ErrNotWritable
func TestAutoConfirmSweep(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, nil)
	m1 := openWith(t, file, nil) // 记账模式
	ownerOld := seedShape(t, m1, protectShapes[3], "主人住在北京")
	aiOld := seedShape(t, m1, protectShapes[2], "主人住在杭州")
	plain := func(text, kind string) string {
		// owner 范围：默认名单（只有 owner-private）下启动补提升碰得了的范围；public、群、别人的私聊的见 TestAutoConfirmSweepOnlyTouchesCandidatesInAllowedPlaces
		f := candidate(text, cSelf, "owner")
		f.Kind = kind
		return idOf(mustApply(t, m1, summary(f)).Outcomes[0].Ref)
	}
	evidence := func(id string, n int) {
		for d := 1; d <= n; d++ {
			mustApply(t, m1, summary(cev{cGroupA, d, cSelf, true}.viaEvidence(id)))
		}
	}
	a := plain("阿萍下周搬家", "fact")
	b := plain("阿萍在学做饭", "fact")
	c := plain("阿萍答应周末来", "promise")
	d := idOf(mustApply(t, m1, summary(candidateWanting("主人搬到了上海", ownerOld))).Outcomes[0].Ref)
	e := idOf(mustApply(t, m1, summary(candidateWanting("主人搬到了广州", aiOld))).Outcomes[0].Ref)
	evidence(a, 3)
	evidence(b, 2)
	evidence(c, 3)
	evidence(d, 3)
	evidence(e, 3)
	all := []string{a, b, c, d, e, ownerOld, aiOld}
	snapshot := func(m *Memory) map[string]string {
		out := map[string]string{}
		for _, id := range all {
			out[id] = rowOf(t, m, id)
		}
		return out
	}
	scopes := map[string]string{}
	for _, id := range all {
		scopes[id] = scopesText(t, m1, id)
	}
	for _, id := range []string{a, b, c, d, e} {
		if statusOf(t, m1, id) != "candidate" {
			t.Fatalf("记账模式下都还是待确认：%s", id)
		}
	}

	// 没打开自动提升：什么都不做
	before := snapshot(m1)
	if r, err := m1.ConfirmSweep(context.Background()); err != nil || r != (ConfirmSweepResult{}) {
		t.Fatalf("没打开自动提升：%+v %v", r, err)
	}
	for id, row := range snapshot(m1) {
		if row != before[id] {
			t.Errorf("没打开自动提升不该动 %s：%s → %s", id, before[id], row)
		}
	}
	m1.Close()

	// 重启，这回打开了
	m2 := openWith(t, file, autoConfirm)
	before = snapshot(m2)
	r, err := m2.ConfirmSweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 看了 a、d、e 三条（b 证据行不够、c 的种类默认不自动提升，连事务都不进）；a、e 提升，d 想取代的旧事实受保护
	if r != (ConfirmSweepResult{Checked: 3, Confirmed: 2, Blocked: 1}) {
		t.Fatalf("启动扫描的账：%+v", r)
	}
	for _, id := range []string{a, e} {
		if got := str(t, m2, `SELECT status || '|' || confirmed_by FROM facts WHERE id = ?`, id); got != "active|ai" {
			t.Errorf("%s 该提升：%s", id, got)
		}
		if n := count(t, m2, `SELECT count(*) FROM fact_revisions WHERE fact_id = ? AND op = 'confirm' AND by = 'summary'`, id); n != 1 {
			t.Errorf("%s 该有一条 confirm 修订：%d", id, n)
		}
	}
	if str(t, m2, `SELECT status || '|' || coalesce(superseded_by, '-') FROM facts WHERE id = ?`, aiOld) != "superseded|"+e {
		t.Errorf("e 想取代的旧事实是 ai 放行的，该被它取代")
	}
	for _, id := range []string{b, c, d, ownerOld} {
		if got := rowOf(t, m2, id); got != before[id] {
			t.Errorf("%s 不该动：%s → %s", id, before[id], got)
		}
	}
	for _, id := range all {
		if got := scopesText(t, m2, id); got != scopes[id] {
			t.Errorf("%s 的范围一个字不动：%q → %q", id, scopes[id], got)
		}
	}
	// 内存里的条目刷新了：a 现在召回出来不再标「听说」
	hits := m2.Recall(context.Background(), ownerRoom, Query{Text: "阿萍下周搬家", Mode: ModeExplicit}).Hits
	if len(hits) == 0 || hits[0].Ref != "f:"+a || hits[0].Hearsay {
		t.Errorf("提升之后召回里该是有效的：%+v", hits)
	}

	// 再扫一遍：只有 d 还在（还是被挡），别的都不再进事务；什么都不变
	after := snapshot(m2)
	r, err = m2.ConfirmSweep(context.Background())
	if err != nil || r != (ConfirmSweepResult{Checked: 1, Blocked: 1}) {
		t.Fatalf("第二遍：%+v %v", r, err)
	}
	for id, row := range snapshot(m2) {
		if row != after[id] {
			t.Errorf("第二遍不该改 %s：%s → %s", id, after[id], row)
		}
	}

	// 只读打开：写入口都是 ErrNotWritable
	ro, err := Open(context.Background(), file, nil, nil, Options{AutoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.ConfirmSweep(context.Background()); !errors.Is(err, ErrNotWritable) {
		t.Errorf("只读该返回 ErrNotWritable：%v", err)
	}
}

// 提升动作只有整理能给，编号必须写；不合规整批不写。对不存在的编号不报错，只是什么都没提升
func TestConfirmOpValidation(t *testing.T) {
	t.Parallel()
	m, _ := newWritableWith(t, autoConfirm)
	id := seedPublicCandidate(t, m, "fact")
	for d := 1; d <= 2; d++ {
		mustApply(t, m, summary(cev{cGroupA, d, cSelf, true}.viaEvidence(id)))
	}
	for _, c := range []struct {
		name string
		b    Batch
	}{
		{"remember 带提升动作", Batch{By: ByRemember, OnDup: DupReport, Ops: []Op{ConfirmOp{FactID: id}}}},
		{"没写编号", summary(ConfirmOp{})},
		{"好的后面跟着坏的：整批不写", summary(cev{cGroupA, 3, cSelf, true}.viaEvidence(id), ConfirmOp{})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := m.apply(context.Background(), c.b); err == nil {
				t.Fatal("该报错")
			}
			if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); n != 2 {
				t.Errorf("证据行不该变：%d", n)
			}
			if statusOf(t, m, id) != "candidate" {
				t.Errorf("不该提升")
			}
		})
	}
	res := mustApply(t, m, summary(ConfirmOp{FactID: "NOSUCH"}))
	if o := res.Outcomes[0]; o.Result != ResultNoop || o.Confirmed || len(res.Counts) != 0 {
		t.Errorf("不存在的编号：%+v %v", o, res.Counts)
	}
}

// 启动补提升只碰范围落在放行场合里的候选（复核后补）：导入的旧候选里有范围是群、别人的私聊的，证据行数够也不能在启动这一刻悄悄变成有效。
// 预期是手写的表：每个范围 × 每份名单，哪些候选会被碰；不放行的候选一个字节不变，只数 Skipped
func TestAutoConfirmSweepOnlyTouchesCandidatesInAllowedPlaces(t *testing.T) {
	t.Parallel()
	const (
		cLocal   = "console:suzune"
		cLocalGp = "console:group:g1"       // 本地群聊（会话 id 的写法同 write_places_test.go 的 TestConvPlaceShapes）
		cPrivate = "qq:private:20001:60001" // 平台私聊（认不出对方是谁：范围退到这个会话本身）
	)
	type cand struct {
		name   string
		scopes []string
	}
	cands := []cand{
		{"owner", []string{"owner"}},
		{"群", []string{"conv:" + cGroupA}},
		{"平台私聊", []string{"conv:" + cPrivate}},
		{"本地私聊", []string{"conv:" + cLocal}},
		{"本地群聊", []string{"conv:" + cLocalGp}},
		{"某人的私聊", []string{"person:qq:20003"}},
		{"public", []string{"public"}},
		{"owner 加群", []string{"owner", "conv:" + cGroupA}},
		{"陌生渠道", []string{"conv:telegram:private:1:2"}},
		{"形状不对的群", []string{"conv:qq:group:abc"}},
	}
	for _, tc := range []struct {
		name   string
		places []string
		want   []string // 手写：会被碰的候选
	}{
		{"默认名单（只有 owner-private）", nil, []string{"owner"}},
		{"owner-private 加平台群", []string{WritePlaceOwnerPrivate, WritePlacePlatformGroup}, []string{"owner", "群", "owner 加群"}},
		{"只有本地私聊", []string{WritePlaceLocalPrivate}, []string{"owner", "本地私聊"}},
		{"只有本地群聊", []string{WritePlaceLocalGroup}, []string{"owner", "本地群聊"}},
		{"所有人的 QQ 私聊", []string{WritePlacePlatformPrivate}, []string{"owner", "平台私聊", "某人的私聊"}},
		{"名单是空的（什么场合都不整理）", []string{}, nil},
		{"全部场合", AllWritePlaces(), []string{"owner", "群", "平台私聊", "本地私聊", "本地群聊", "某人的私聊", "owner 加群"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file := migratedDB(t, nil)
			m1 := openWith(t, file, nil) // 记账模式：证据记下了，状态不变
			ids := map[string]string{}
			for i, c := range cands {
				f := candidate("阿萍的第"+string(rune('A'+i))+"件事", cSelf, c.scopes...)
				id := idOf(mustApply(t, m1, summary(f)).Outcomes[0].Ref)
				ids[c.name] = id
				for d := 1; d <= 3; d++ {
					mustApply(t, m1, summary(cev{cGroupA, d, cSelf, true}.viaEvidence(id)))
				}
				if statusOf(t, m1, id) != "candidate" {
					t.Fatalf("记账模式下 %s 还是待确认", c.name)
				}
			}
			m1.Close()
			m2 := openWith(t, file, func(o *Options) { o.AutoConfirm, o.WritePlaces = true, tc.places })
			before := map[string]string{}
			for name, id := range ids {
				before[name] = rowOf(t, m2, id)
			}
			r, err := m2.ConfirmSweep(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			touched := 0
			for _, c := range cands {
				id := ids[c.name]
				want := slices.Contains(tc.want, c.name)
				if got := statusOf(t, m2, id) == "active"; got != want {
					t.Errorf("%s：提升了 %v，该 %v", c.name, got, want)
				}
				if want {
					touched++
				} else if rowOf(t, m2, id) != before[c.name] {
					t.Errorf("%s 不在放行的场合里，一个字节都不该动：%s → %s", c.name, before[c.name], rowOf(t, m2, id))
				}
			}
			if r.Confirmed != touched || r.Checked != touched || r.Skipped != len(cands)-touched {
				t.Errorf("账：%+v，该 看了=提升=%d、没碰=%d", r, touched, len(cands)-touched)
			}
		})
	}
}
