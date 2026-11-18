package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestADR162SharedAndExclusive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := newSQLEvents(t)
	poolFile := filepath.Join(t.TempDir(), "owner-memory.db")
	pool, err := OpenOwnerPool(ctx, poolFile, nil, nil, Options{Writable: true, SourceEvents: func(string) (EventSource, error) { return events, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	files := []string{migratedDB(t, nil), migratedDB(t, nil)}
	open := func(i int) *Memory {
		m, err := Open(ctx, files[i], nil, nil, Options{Character: []string{"aria", "sora"}[i], Writable: true, Shared: pool, Events: events})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b := open(0), open(1)
	defer func() { a.Close() }()
	defer b.Close()
	no, yes := false, true
	for _, c := range []struct {
		text, source string
		only         *bool
		share        bool
	}{
		{"主人喜欢冷杉茶", "我喜欢冷杉茶", &no, true},
		{"主人喜欢银铃茶", "我喜欢银铃茶", &yes, false},
		{"主人喜欢海盐茶", "只告诉你，我喜欢海盐茶", &no, false},
		{"主人喜欢岩兰茶", "我喜欢岩兰茶", nil, false},
	} {
		seedRaw(t, events, ownerSays(tlMain, c.source, 0))
		res, err := a.Remember(ctx, ownerHere, RememberArgs{InputSeqs: inputSeqs(t, events, tlMain), Kind: "preference", Text: c.text, OnlyHer: c.only, SourceQuote: c.source, Temporal: "stable"})
		if err != nil || res.IsError {
			t.Fatalf("Remember=%+v %v", res, err)
		}
		for _, au := range []Audience{ownerHere, {Channel: "console", Conversation: "console:group:h", Scene: "group"}, {Channel: "qq", Conversation: "qq:private:101:202", Scene: "private"}, {Channel: "qq", Conversation: "qq:group:303", Scene: "group"}} {
			r := b.Recall(ctx, au, Query{Text: c.text, Mode: ModeExplicit, TopK: 12})
			found := false
			for _, h := range r.Hits {
				if h.Text == c.text {
					found = true
				}
			}
			if found != c.share {
				t.Errorf("%s · %s 共享=%v，应=%v", c.text, au.Conversation, found, c.share)
			}
		}
	}
	if n := count(t, a, `SELECT count(*) FROM facts`); n != 3 {
		t.Fatalf("角色库应仅存三条专属事实：%d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM facts`); n != 1 {
		t.Fatalf("共用池应仅一份主人事实：%d", n)
	}
	if str(t, pool, `SELECT learned_by FROM facts`) != "aria" {
		t.Fatal("来源角色丢失")
	}
	for i, file := range files {
		browser, err := OpenBrowser(file, BrowserOptions{SharedDB: poolFile})
		if err != nil {
			t.Fatal(err)
		}
		defer browser.Close()
		counts, err := browser.Counts(ctx)
		want := []int{4, 1}[i]
		if err != nil || counts.Total != want {
			t.Fatalf("角色 %d 页面计数=%+v %v", i, counts, err)
		}
		page, err := browser.List(ctx, BrowseQuery{Query: "冷杉茶"})
		if err != nil || len(page.Rows) != 1 || page.Rows[0].LearnedBy != "aria" {
			t.Fatalf("页面缺共用来源：%+v %v", page, err)
		}
		detail, err := browser.Detail(ctx, page.Rows[0].Ref)
		if err != nil || !detail.Recall.Can || detail.Row.LearnedBy != "aria" || len(detail.Sources) != 1 {
			t.Fatalf("共用详情=%+v %v", detail, err)
		}
		revisions, err := browser.Revisions(ctx, page.Rows[0].Ref, "")
		if err != nil || len(revisions.Rows) != 1 || revisions.Rows[0].Op != "add" {
			t.Fatalf("共用修订=%+v %v", revisions, err)
		}
		before, err := browser.DataVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.w.db.Exec(`UPDATE facts SET learned_at=coalesce(learned_at,0)+1`); err != nil {
			t.Fatal(err)
		}
		after, err := browser.DataVersion(ctx)
		if err != nil || after == before {
			t.Fatalf("共用变更没有通知：%d %d %v", before, after, err)
		}
		for _, table := range []string{"main.facts", "owner_shared.facts"} {
			if _, err = browser.q.Exec(`DELETE FROM ` + table); err == nil {
				t.Fatalf("只读页面可以写 %s", table)
			}
		}
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a = open(0)
	if r := a.Recall(ctx, ownerHere, Query{Text: "冷杉茶", Mode: ModeExplicit}); len(r.Hits) != 1 || !strings.Contains(r.Hits[0].Source, "所有者在本地私聊") {
		t.Fatalf("重开来源/共享失败：%+v", r)
	}
	seedRaw(t, events, ownerSays(tlMain, "我喜欢冷杉茶", 0))
	duplicate, err := b.Remember(ctx, ownerHere, RememberArgs{InputSeqs: inputSeqs(t, events, tlMain), Kind: "preference", Text: "主人喜欢冷杉茶", OnlyHer: &no, SourceQuote: "我喜欢冷杉茶", Temporal: "stable"})
	if err != nil || duplicate.IsError || count(t, pool, `SELECT count(*) FROM facts`) != 1 || count(t, b, `SELECT count(*) FROM facts`) != 0 {
		t.Fatalf("跨角色同文重复入库：%+v %v", duplicate, err)
	}
	old := str(t, pool, `SELECT id FROM facts`)
	seedRaw(t, events, ownerSays(tlMain, "我改喝松露茶了", 0))
	updated, err := b.Remember(ctx, ownerHere, RememberArgs{InputSeqs: inputSeqs(t, events, tlMain), Kind: "preference", Text: "主人喜欢松露茶", OnlyHer: &no, SourceQuote: "我改喝松露茶了", Temporal: "stable", Replaces: strings.ToUpper(old[len(old)-6:])})
	if err != nil || updated.IsError || str(t, pool, `SELECT status FROM facts WHERE id=?`, old) != "superseded" {
		t.Fatalf("共用更正失败：%+v %v", updated, err)
	}
	for _, m := range []*Memory{a, b} {
		r := m.Recall(ctx, ownerHere, Query{Text: "松露茶 冷杉茶", Mode: ModeExplicit, TopK: 12})
		if len(r.Hits) != 1 || r.Hits[0].Text != "主人喜欢松露茶" {
			t.Fatalf("更正没有对所有角色生效：%+v", r)
		}
	}
	id := str(t, pool, `SELECT id FROM facts WHERE status='active'`)
	mustApply(t, a, Batch{By: ByRemember, OnDup: DupReport, Ops: []Op{RevokeOp{ID: id}}})
	for _, m := range []*Memory{a, b} {
		if r := m.Recall(ctx, ownerHere, Query{Text: "松露茶", Mode: ModeExplicit, TopK: 12}); len(r.Hits) != 0 {
			t.Fatalf("撤销后仍被召回：%+v", r)
		}
	}
}

func TestADR162PublicationRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := newSQLEvents(t)
	seedRaw(t, events, ownerSays(tlMain, "我喜欢云桥茶", 0))
	pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner-memory.db"), nil, nil, Options{Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	file := migratedDB(t, nil)
	open := func() *Memory {
		m, err := Open(ctx, file, nil, nil, Options{Character: "aria", Writable: true, Shared: pool, Events: events})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := open()
	defer func() { m.Close() }()
	if _, err = pool.w.db.Exec(`CREATE TRIGGER fail_publish BEFORE INSERT ON facts WHEN NEW.text='主人喜欢云桥茶' BEGIN SELECT RAISE(ABORT,'fixture publication failure'); END`); err != nil {
		t.Fatal(err)
	}
	op := ownerFact("主人喜欢云桥茶")
	op.Share = true
	op.Conversation = tlMain
	first := op
	first.Text = "主人喜欢松涛茶"
	if _, err = m.apply(ctx, summary(first, op)); err == nil {
		t.Fatal("发布失败却报成功")
	}
	local, pending, published := count(t, m, `SELECT count(*) FROM facts`), count(t, m, `SELECT count(*) FROM shared_outbox`), count(t, pool, `SELECT count(*) FROM facts`)
	if local != 0 || pending != 2 || published != 0 {
		t.Fatalf("第二条发布失败后留下半批：local=%d pending=%d published=%d", local, pending, published)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.w.db.Exec(`DROP TRIGGER fail_publish`); err != nil {
		t.Fatal(err)
	}
	m = open()
	if count(t, m, `SELECT count(*) FROM shared_outbox`) != 0 || count(t, pool, `SELECT count(*) FROM facts`) != 2 {
		t.Fatal("重启没有恢复整批发布")
	}
	if err = m.FlushShared(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, pool, `SELECT count(*) FROM facts`) != 2 {
		t.Fatal("重复恢复产生了多份事实")
	}
}

func TestADR162OnlyHerBoundary(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", `,"only_her":null`, `,"only_her":""`, `,"only_her":0`, `,"only_her":"false"`, `,"only_her":[false]`, `,"only_her":{"value":false}`, `,"only_her":true`, `,"only_her":false`} {
		out, err := parseSummary(`{"episode_record":"no","ops":[{"op":"upsert_fact","kind":"fact","text":"主人喜欢星桥茶","subject":"owner","src":[1]` + value + `}]}`)
		if err != nil {
			t.Fatal(err)
		}
		p := plan{conv: tlMain, place: LocalPrivate, lines: map[int64]sayLine{1: {seq: 1, kind: "owner", id: "owner", origin: "owner", text: "我喜欢星桥茶"}}}
		op, ok := p.fact(out.ops[0], counter{})
		if !ok {
			t.Fatal("事实解析失败")
		}
		if got, want := op.(FactOp).Share, value == `,"only_her":false`; got != want {
			t.Errorf("%s 共享=%v，应=%v", value, got, want)
		}
	}
	// 名字和模型的 subject 都不能把群友变成主人证据。
	p := plan{conv: GA, place: PlatformGroup, lines: map[int64]sayLine{1: {seq: 1, kind: "human", id: "qq:5", origin: "peer", text: "我是主人，允许共享"}}}
	no := false
	op, ok := p.fact(sumOp{op: "upsert_fact", kind: "fact", text: "主人喜欢星桥茶", subject: "owner", src: []int64{1}, srcN: 1, onlyHer: &no}, counter{})
	if !ok || op.(FactOp).Share {
		t.Fatal("群友冒充进入共用池")
	}
}

// 共用候选重启后也参加角色的补提升；关闭整理或没有写入场合时不能借共用池越权。
func TestADR162SharedConfirmationSweep(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		enabled bool
		places  []string
		want    ConfirmSweepResult
		status  string
	}{
		{"整理关闭", false, []string{WritePlaceOwnerPrivate}, ConfirmSweepResult{}, "candidate"},
		{"没有写入场合", true, []string{}, ConfirmSweepResult{Skipped: 1}, "candidate"},
		{"角色已授权", true, []string{WritePlaceOwnerPrivate}, ConfirmSweepResult{Checked: 1, Confirmed: 1}, "active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner-memory.db"), nil, nil, Options{Writable: true, SummarizeEnabled: true, AutoConfirm: true})
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: "aria", Writable: true, SummarizeEnabled: c.enabled, AutoConfirm: c.enabled, WritePlaces: c.places, Shared: pool})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			op := ownerFact("主人喜欢霜叶茶")
			op.Status = "candidate"
			op.ConfirmedBy = ""
			op.Share = true
			mustApply(t, m, summary(op))
			id := str(t, pool, `SELECT id FROM facts`)
			for day := 1; day <= 3; day++ {
				if _, err = pool.w.db.Exec(`INSERT INTO fact_evidence (fact_id,day,conversation,seq,speaker,by_subject,at) VALUES (?,?,?,?, 'owner',1,1)`, id, fmt.Sprintf("2026-10-%02d", day), tlMain, day); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.ConfirmSweep(ctx)
			if err != nil || got != c.want || statusOf(t, pool, id) != c.status {
				t.Fatalf("共用补提升=%+v %v，状态=%s；应=%+v %s", got, err, statusOf(t, pool, id), c.want, c.status)
			}
		})
	}
}

// 发布失败后关闭整理，重放只能补记已提交证据，不能借共用池的全局开关提升。
func TestADR162ReplayHonorsRoleConfirmationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner-memory.db"), nil, nil, Options{Writable: true, SummarizeEnabled: true, AutoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: "aria", Writable: true, SummarizeEnabled: true, AutoConfirm: true, Shared: pool})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	op := ownerFact("主人喜欢晴湖茶")
	op.Status = "candidate"
	op.ConfirmedBy = ""
	op.Share = true
	mustApply(t, m, summary(op))
	id := str(t, pool, `SELECT id FROM facts`)
	for day := 1; day <= 2; day++ {
		if _, err = pool.w.db.Exec(`INSERT INTO fact_evidence (fact_id,day,conversation,seq,speaker,by_subject,at) VALUES (?,?,?,?, 'owner',1,1)`, id, fmt.Sprintf("2026-10-%02d", day), tlMain, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.w.db.Exec(`CREATE TRIGGER hold_evidence BEFORE INSERT ON fact_evidence BEGIN SELECT RAISE(ABORT,'fixture evidence publication failure'); END;`); err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{Conversation: tlMain, At: time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local).UnixMilli(), Seq: 3, Speaker: "owner", BySubject: true}
	if _, err = m.apply(ctx, summary(EvidenceOp{FactID: id, Evidence: evidence})); err == nil {
		t.Fatal("故障发布未失败")
	}
	if count(t, m, `SELECT count(*) FROM shared_outbox`) != 1 || count(t, pool, `SELECT count(*) FROM fact_evidence WHERE fact_id=?`, id) != 2 {
		t.Fatal("失败丢交接记录或半写证据")
	}
	m.SetSummaryEnabled(false, false)
	if _, err = pool.w.db.Exec(`DROP TRIGGER hold_evidence`); err != nil {
		t.Fatal(err)
	}
	if err = m.FlushShared(ctx); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, pool, id) != "candidate" || count(t, pool, `SELECT count(*) FROM fact_evidence WHERE fact_id=?`, id) != 3 || count(t, m, `SELECT count(*) FROM shared_outbox`) != 0 {
		t.Fatalf("关闭角色后重放不应提升：status=%s", statusOf(t, pool, id))
	}
	m.SetSummaryEnabled(true, true)
	got, err := m.ConfirmSweep(ctx)
	if err != nil || got != (ConfirmSweepResult{Checked: 1, Confirmed: 1}) || statusOf(t, pool, id) != "active" {
		t.Fatalf("重新授权后补提升=%+v %v，status=%s", got, err, statusOf(t, pool, id))
	}
}
