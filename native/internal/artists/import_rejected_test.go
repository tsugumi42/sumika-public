package artists

import "testing"

// 确认页导出的 rejected（用户标“不是”的）也要记进表（用户 2026-11-13）：没有行就新增“不是”，
// 待确认、生效的改成“不是”并记 Prev，撤回能退回去；同一个号两边都有时以“是”为准。

func rejectFile(yes, no []string) []byte {
	part := func(ids []string) string {
		out := ""
		for i, id := range ids {
			if i > 0 {
				out += ","
			}
			out += `{"id":"` + id + `","screen_name":"user_` + id + `","name":"编的名字"}`
		}
		return out
	}
	return []byte(`{"version":3,"confirmedArtists":[` + part(yes) + `],"rejected":[` + part(no) + `]}`)
}

func TestImportRejectedAddsDeclined(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	rep, err := s.Import(rejectFile(nil, []string{"8001", "8002"}))
	if err != nil || rep.Declined != 2 || rep.Batch == "" {
		t.Fatalf("没有行的“不是”应当新增两行并生成批次：%+v %v", rep, err)
	}
	for _, id := range []string{"8001", "8002"} {
		e, ok := s.Lookup(id)
		if !ok || e.Status != StatusDeclined || e.Source != SourceImport || e.Handle != "user_"+id {
			t.Fatalf("%s 应当记成“不是”（来源 import）：%+v", id, e)
		}
		if s.Confirmed(id) {
			t.Fatalf("%s 不该算画师", id)
		}
	}
	// 按需判断碰到这两个号不再提（已有行一律不动）
	if st, err := s.AddAuto("8001", "user_8001", "编的名字", judged(9)); err != nil || st != "" {
		if e, _ := s.Lookup("8001"); e.Status != StatusDeclined {
			t.Fatalf("按需判断不该把用户说“不是”的改掉：%q %v %+v", st, err, e)
		}
	}
	snap := snapshot(t, dir)
	rep, err = s.Import(rejectFile(nil, []string{"8001"}))
	if err != nil || rep.Batch != "" || rep.AlreadyDeclined != 1 || snapshot(t, dir) != snap {
		t.Fatalf("本来就是“不是”的再导一次不该有变化：%+v %v", rep, err)
	}
}

func TestImportRejectedOverridesPendingAndConfirmedWithUndo(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	if _, err := s.AddAuto("8003", "user_8003", "编的名字", judged(9)); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Lookup("8003"); e.Status != StatusPending {
		t.Fatalf("前提：应当是待确认：%+v", e)
	}
	if _, err := s.Import(rejectFile([]string{"8004"}, nil)); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Import(rejectFile(nil, []string{"8003", "8004"}))
	if err != nil || rep.Declined != 2 {
		t.Fatalf("待确认和生效的都应当改成“不是”：%+v %v", rep, err)
	}
	if s.Confirmed("8004") {
		t.Fatal("用户这次说“不是”，8004 不该再算画师")
	}
	if _, err := s.Undo(rep.Batch); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Lookup("8003"); e.Status != StatusPending {
		t.Fatalf("撤回后 8003 应当回到待确认：%+v", e)
	}
	if !s.Confirmed("8004") {
		t.Fatal("撤回后 8004 应当回到生效")
	}
}

func TestImportRejectedEdgeCases(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	rep, err := s.Import(rejectFile([]string{"8005"}, []string{"8005", "0", "abc"}))
	if err != nil || rep.Added != 1 || rep.Declined != 0 || rep.Malformed != 2 {
		t.Fatalf("两边都有以“是”为准，坏号计数：%+v %v", rep, err)
	}
	if !s.Confirmed("8005") {
		t.Fatal("两边都有的号应当生效")
	}
	// 以前导入的“不是”，后来又在确认页标“是”：改成生效（导入的决定可以被后来的导入改）
	if _, err := s.Import(rejectFile(nil, []string{"8006"})); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.Import(rejectFile([]string{"8006"}, nil)); err != nil || rep.Upgraded != 1 || !s.Confirmed("8006") {
		t.Fatalf("导入的“不是”再导入“是”应当改成生效：%+v %v", rep, err)
	}
	// 旧格式（没有 rejected 字段）照旧能导
	if rep, err := s.Import([]byte(`{"confirmedArtists":[{"id":"8007","screen_name":"user_8007"}]}`)); err != nil || rep.Added != 1 {
		t.Fatalf("没有 rejected 的旧文件照旧能导：%+v %v", rep, err)
	}
}
