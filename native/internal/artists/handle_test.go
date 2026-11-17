package artists

import "testing"

// ADR-372：vx 线路没有数字 ID 时按 @用户名 找确认过的画师；有 ID 的结果顺手更新存的用户名。

func importHandles(t *testing.T, s *Store, pairs ...string) {
	t.Helper()
	body := `{"exportedAt":"2026-11-13T00:00:00Z","confirmedArtists":[`
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			body += ","
		}
		body += `{"id":"` + pairs[i] + `","screen_name":"` + pairs[i+1] + `","name":"编的名字"}`
	}
	body += `]}`
	if _, err := s.Import([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmedHandleMatching(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	importHandles(t, s, "7001", "Paper_A", "7002", "paper_b")
	for _, h := range []string{"Paper_A", "paper_a", "@PAPER_A", " paper_a "} {
		if id, ok := s.ConfirmedHandle(h); !ok || id != "7001" {
			t.Errorf("%q 应该认成 7001：%q %v", h, id, ok)
		}
	}
	for _, h := range []string{"", "@", "paper", "paper_a_x", "someone"} {
		if id, ok := s.ConfirmedHandle(h); ok {
			t.Errorf("%q 不该认出画师：%q", h, id)
		}
	}
}

func TestConfirmedHandleIgnoresPendingAndDeclined(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	if _, err := s.AddAuto("7003", "pend_x", "编的名字", judged(9)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAuto("7004", "decl_x", "编的名字", judged(0)); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pend_x", "decl_x"} {
		if _, ok := s.ConfirmedHandle(h); ok {
			t.Errorf("%s 没确认过，不该按用户名命中", h)
		}
	}
}

func TestConfirmedHandleAmbiguousIsNoMatch(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	importHandles(t, s, "7005", "dup_name", "7006", "DUP_NAME")
	if id, ok := s.ConfirmedHandle("dup_name"); ok {
		t.Fatalf("两位确认过的画师存着同一个用户名，宁可漏认：%q", id)
	}
}

func TestRefreshHandleRenamesAndFreesOldName(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	importHandles(t, s, "7001", "paper_a", "7002", "paper_b")
	before, _ := s.Lookup("7001")
	if changed, err := s.RefreshHandle("7001", "@renamed_a"); err != nil || !changed {
		t.Fatalf("改名应该写进去：%v %v", changed, err)
	}
	if _, ok := s.ConfirmedHandle("paper_a"); ok {
		t.Fatal("旧用户名不该再命中")
	}
	if id, ok := s.ConfirmedHandle("renamed_a"); !ok || id != "7001" {
		t.Fatalf("新用户名应该命中 7001：%q %v", id, ok)
	}
	after, _ := s.Lookup("7001")
	if after.Status != before.Status || after.Batch != before.Batch || after.Source != before.Source || after.Handle != "renamed_a" {
		t.Fatalf("只改用户名，状态、来源、导入批次都不动：%+v → %+v", before, after)
	}
	// 7002 后来用上了 7001 以前的名字：7001 那行不会被它的旧名字误认
	if _, err := s.RefreshHandle("7002", "renamed_a"); err != nil {
		t.Fatal(err)
	}
	if id, ok := s.ConfirmedHandle("renamed_a"); !ok || id != "7002" {
		t.Fatalf("用户名同一时间只属于一个人，应该认成 7002：%q %v", id, ok)
	}
	if e, _ := s.Lookup("7001"); e.Handle != "" {
		t.Fatalf("7001 存的旧名字要清掉：%q", e.Handle)
	}
	// 重新打开文件也是这样
	if id, ok := reopen(t, dir).ConfirmedHandle("renamed_a"); !ok || id != "7002" {
		t.Fatalf("改名要落盘：%q %v", id, ok)
	}
}

func TestRefreshHandleNoopCases(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	importHandles(t, s, "7001", "Paper_A")
	snap := snapshot(t, dir)
	for _, c := range []struct{ id, h string }{{"7001", "paper_a"}, {"7001", "@PAPER_A"}, {"7009", "someone"}, {"0", "x"}, {"7001", ""}, {"7001", "@"}} {
		if changed, err := s.RefreshHandle(c.id, c.h); err != nil || changed {
			t.Errorf("%+v 不该改动：%v %v", c, changed, err)
		}
	}
	if snapshot(t, dir) != snap {
		t.Fatal("什么都没改就不该写文件")
	}
	if _, ok := s.Lookup("7009"); ok {
		t.Fatal("表里没有的作者不该被刷新出一行")
	}
}
