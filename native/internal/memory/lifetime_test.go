package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryLifetimePersistsAndExpires(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, temporal, until string
		expires               bool
	}{
		{"stable", "stable", "", false}, {"legacy-default", "", "", true},
		{"temporary", "temporary", "", true}, {"explicit-offset", "temporary", "2026-10-26T21:00:00+08:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := newSQLEvents(t)
			events.add(ownerSays(tlMain, "我这周赶稿", 0))
			file := migratedDB(t, nil)
			opt := Options{Writable: true, Character: "suzune", Events: events, Now: func() time.Time { return toolsNow }}
			m, err := Open(context.Background(), file, nil, nil, opt)
			if err != nil {
				t.Fatal(err)
			}
			r, err := m.Remember(context.Background(), ownerHere, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人这周赶稿", SourceQuote: "我这周赶稿", Temporal: tc.temporal, ValidUntil: tc.until})
			if err != nil || r.IsError {
				m.Close()
				t.Fatalf("write: %+v %v", r, err)
			}
			m.Close()
			m, err = Open(context.Background(), file, nil, nil, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			items := m.Items()
			if len(items) != 1 || items[0].ConfirmedBy != ConfirmedOwnerSaid || items[0].Expired(toolsNow) {
				t.Fatalf("reopen: %+v", items)
			}
			if items[0].Expired(toolsNow.Add(8*24*time.Hour)) != tc.expires {
				t.Fatalf("expiry: %+v", items[0])
			}
			rcl := m.Recall(context.Background(), ownerHere, Query{Text: "赶稿", Mode: ModeExplicit, Now: toolsNow.Add(8 * 24 * time.Hour)})
			if tc.expires && len(rcl.Hits) != 0 {
				t.Fatalf("expired recall: %+v", rcl)
			}
			if factStatus(t, m, "主人这周赶稿") != "active" {
				t.Fatal("expiry must not imply completion/deletion")
			}
		})
	}
}

func TestMemoryLifetimeAndProtectedWritesRejectAtomically(t *testing.T) {
	t.Parallel()
	events := newSQLEvents(t)
	events.add(ownerSays(tlMain, "我喜欢茶", 0))
	m, _ := openTools(t, events)
	for _, tc := range []struct{ temporal, until string }{{"bad", ""}, {"temporary", "tomorrow"}, {"temporary", "2026-10-26T21:00:00"}, {"temporary", "2026-10-25T21:00:00+08:00"}, {"stable", "2026-10-27T21:00:00+08:00"}} {
		r, err := m.Remember(context.Background(), ownerHere, RememberArgs{InputSeqs: []int64{1}, Kind: "preference", Text: "主人喜欢茶", SourceQuote: "我喜欢茶", Temporal: tc.temporal, ValidUntil: tc.until})
		if err != nil || !r.IsError || writes(t, m) != [4]int{} {
			t.Fatalf("bad lifetime wrote: %+v %v", r, err)
		}
	}
	for _, sh := range protectShapes {
		if !sh.active() || sh.aiMayTouch {
			continue
		}
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			ev := newSQLEvents(t)
			ev.add(ownerSays(tlMain, "我喜欢茶", 0))
			mm, _ := openTools(t, ev)
			id := seedShape(t, mm, sh, "主人喜欢咖啡")
			before := writes(t, mm)
			r, err := mm.Remember(context.Background(), ownerHere, RememberArgs{InputSeqs: []int64{1}, Kind: "preference", Text: "主人喜欢茶", SourceQuote: "我喜欢茶", Temporal: "stable", Replaces: id[len(id)-6:]})
			if err != nil || !r.IsError || writes(t, mm) != before {
				t.Fatalf("protected write: %+v %v", r, err)
			}
		})
	}
}

func TestRememberCannotRecreateSharedRevocationAsExclusive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := newSQLEvents(t)
	events.add(ownerSays(tlMain, "我喜欢茶", 0))
	pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner.db"), nil, nil, Options{Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Writable: true, Character: "suzune", Shared: pool, Events: events, Now: func() time.Time { return toolsNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old := mustApply(t, pool, summary(aiFact("主人喜欢茶"))).Outcomes[0]
	mustApply(t, pool, summary(RevokeOp{ID: idOf(old.Ref)}))
	yes := true
	for _, only := range []*bool{nil, &yes} {
		r, err := m.Remember(ctx, ownerHere, RememberArgs{InputSeqs: []int64{1}, Kind: "preference", Text: "主人喜欢茶", SourceQuote: "我喜欢茶", OnlyHer: only, Temporal: "stable"})
		if err != nil || !r.IsError || count(t, m, `SELECT count(*) FROM facts`) != 0 {
			t.Fatalf("recreated: %+v %v", r, err)
		}
	}
}

func TestLifetimeRefreshOnSameText(t *testing.T) {
	t.Parallel()
	for _, by := range []string{ByRememberAI, BySummary} {
		t.Run(by, func(t *testing.T) {
			t.Parallel()
			m, _ := openTools(t, newSQLEvents(t))
			oldUntil := toolsNow.Add(-time.Hour).UnixMilli()
			f := aiFact("主人本周赶稿")
			f.Temporal = "temporary"
			f.ValidTo = &oldUntil
			f.EventAt = toolsNow.Add(-24 * time.Hour).UnixMilli()
			id := idOf(mustApply(t, m, summary(f)).Outcomes[0].Ref)
			until := toolsNow.Add(24 * time.Hour).UnixMilli()
			if by == ByRememberAI {
				f.ConfirmedBy = ConfirmedOwnerSaid // remember 的批只收这个标记；上面整理写的种子仍是 ai，刷新期限两种都要行
			}
			f.ValidTo = &until
			f.EventAt = toolsNow.UnixMilli()
			f.Evidence = &Evidence{Conversation: tlMain, At: f.EventAt, Seq: 1, Speaker: "owner", BySubject: true}
			onDup := DupMerge
			if by == ByRememberAI {
				onDup = DupReport
			}
			mustApply(t, m, Batch{By: by, OnDup: onDup, Ops: []Op{f}})
			var actual int64
			if err := m.w.db.QueryRow(`SELECT valid_to FROM facts WHERE id=?`, id).Scan(&actual); err != nil || actual != until {
				t.Fatalf("renew: %d %v", actual, err)
			}
			if m.Items()[0].Expired(toolsNow) || count(t, m, `SELECT count(*) FROM facts`) != 1 {
				t.Fatal("same fact should be recallable again")
			}
			f.EventAt += 2000
			mustApply(t, m, Batch{By: by, OnDup: onDup, Ops: []Op{f}}) // 同期限的更新仍推进来源时间
			f.EventAt -= 1000
			f.ValidTo = &oldUntil
			mustApply(t, m, Batch{By: by, OnDup: onDup, Ops: []Op{f}})
			if m.Items()[0].Expired(toolsNow) {
				t.Fatal("older source shortened renewal")
			}
		})
	}
	for _, legacy := range []string{"now", "plan", "past", "stopped"} {
		temporal, until, err := memoryLifetime("fact", legacy, "", toolsNow)
		if err != nil || temporal != "temporary" || until == nil {
			t.Fatalf("legacy permanent: %s", legacy)
		}
	}
}

func TestExpiredDuplicateCannotReplaceLiveFact(t *testing.T) {
	t.Parallel()
	for _, confirmed := range []string{"owner", "ai"} {
		t.Run(confirmed, func(t *testing.T) {
			t.Parallel()
			ev := newSQLEvents(t)
			ev.add(ownerSays(tlMain, "主人喜欢茶", 0))
			m, _ := openTools(t, ev)
			expiry := toolsNow.Add(-time.Hour).UnixMilli()
			old := aiFact("主人喜欢茶")
			old.ConfirmedBy = confirmed
			old.Temporal = "temporary"
			old.ValidTo = &expiry
			old.EventAt = toolsNow.Add(time.Hour).UnixMilli()
			target := mustApply(t, m, summary(aiFact("主人喜欢咖啡"), old)).Outcomes[0]
			before := writes(t, m)
			id := idOf(target.Ref)
			r, err := m.Remember(context.Background(), ownerHere, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人喜欢茶", SourceQuote: "主人喜欢茶", Temporal: "stable", Replaces: id[len(id)-6:]})
			if err != nil || !r.IsError || writes(t, m) != before || factStatus(t, m, "主人喜欢咖啡") != "active" {
				t.Fatalf("expired replaced: %+v %v", r, err)
			}
		})
	}
}

// 没写时态：边界、承诺、偏好默认长期，别的默认暂时；写了的照写的来
func TestLifetimeDefaultByKind(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]string{"boundary": "stable", "promise": "stable", "preference": "stable", "fact": "temporary"} {
		got, until, err := memoryLifetime(kind, "", "", toolsNow)
		if err != nil || got != want || (want == "stable") != (until == nil) {
			t.Errorf("%s 没写时态：得 %q until=%v err=%v，要 %q", kind, got, until, err, want)
		}
	}
	if got, _, _ := memoryLifetime("boundary", "temporary", "", toolsNow); got != "temporary" {
		t.Errorf("边界明写 temporary 要照写的：%q", got)
	}
	if got, _, _ := memoryLifetime("promise", "plan", "", toolsNow); got != "temporary" {
		t.Errorf("承诺写 plan 仍按暂时：%q", got)
	}
}
