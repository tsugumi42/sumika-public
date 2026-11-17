package artists

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 导入测试用的文件内容：账号、名字都是编的。
func file(ids ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"exportedAt":"2026-11-13T00:00:00Z","confirmedArtists":[`)
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"` + id + `","screen_name":"user_` + id + `","name":"名字` + id + `","suggested":"画师"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "x.json"))
	if err != nil {
		return ""
	}
	return string(b)
}

func TestImportAddsConfirmedWithBatch(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	rep, err := s.Import(file(idA, idB))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 2 || rep.Batch != "imp-20261113100000" {
		t.Fatalf("%+v", rep)
	}
	e, _ := s.Lookup(idA)
	if e.Status != StatusConfirmed || e.Source != SourceImport || e.Batch != rep.Batch || e.Prev != nil || e.Handle != "user_"+idA || e.Name != "名字"+idA {
		t.Fatalf("%+v", e)
	}
	if !s.Confirmed(idA) || !reopen(t, dir).Confirmed(idB) {
		t.Fatal("导入后应该生效并且重载后还在")
	}
}

func TestImportIsIdempotentAndMakesNoEmptyBatch(t *testing.T) {
	s, dir, ck := newStore(t, Options{})
	if _, err := s.Import(file(idA, idB)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)
	ck.tick(time.Hour)
	rep, err := s.Import(file(idA, idB))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Batch != "" || rep.Already != 2 || rep.Added+rep.Upgraded != 0 {
		t.Fatalf("同一个文件再跑应该没有变化：%+v", rep)
	}
	if snapshot(t, dir) != before {
		t.Fatal("没有变化却改了文件")
	}
}

func TestImportUpgradesPendingAndDeclinedAuto(t *testing.T) {
	s, _, ck := newStore(t, Options{})
	s.AddAuto(idA, "a", "A", judged(5)) // pending
	s.AddAuto(idB, "b", "B", judged(0)) // declined(auto)
	ck.tick(time.Hour)
	rep, err := s.Import(file(idA, idB, idC))
	if err != nil || rep.Upgraded != 2 || rep.Added != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	e, _ := s.Lookup(idA)
	if e.Prev == nil || e.Prev.Status != StatusPending || e.Prev.Source != SourceAuto || e.Prev.Score != 5 {
		t.Fatalf("要记下改动前的整行：%+v", e.Prev)
	}
}

func TestImportSkipsManualDecline(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	s.Import(file(idA))
	s.Remove([]string{idA}) // 用户手动说“不是”
	rep, err := s.Import(file(idA, idB))
	if err != nil || rep.Skipped != 1 || rep.Added != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if s.Confirmed(idA) {
		t.Fatal("手动移除的账号不该被旧文件复活")
	}
}

func TestImportMalformedRowsCountedNotWritten(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	rep, err := s.Import(file(idA, "", "0", "abc", "012", idA+`\n`))
	if err != nil || rep.Added != 1 || rep.Malformed != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
	if n := s.Counts()[StatusConfirmed]; n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestImportRejectsWrongShapes(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	for name, body := range map[string]string{
		"不是JSON":  "hello",
		"空":       "",
		"数组":      `[1,2]`,
		"没有字段":    `{"exportedAt":"x"}`,
		"字段是null": `{"confirmedArtists":null}`,
		"字段类型错":   `{"confirmedArtists":"x"}`,
		"行类型错":    `{"confirmedArtists":[1]}`,
	} {
		if _, err := s.Import([]byte(body)); err == nil {
			t.Errorf("%s：应该报错", name)
		}
	}
	if snapshot(t, dir) != "" {
		t.Fatal("错误的文件不该写表")
	}
	// 空数组合法，但没有变化
	rep, err := s.Import([]byte(`{"confirmedArtists":[]}`))
	if err != nil || rep.Batch != "" {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestUndoRestoresExactlyBeforeImport(t *testing.T) {
	s, dir, ck := newStore(t, Options{})
	s.AddAuto(idA, "a", "A", judged(5)) // pending
	s.AddAuto(idB, "b", "B", judged(0)) // declined(auto)
	before := snapshot(t, dir)
	ck.tick(time.Hour)
	rep, err := s.Import(file(idA, idB, idC))
	if err != nil {
		t.Fatal(err)
	}
	ur, err := s.Undo(rep.Batch)
	if err != nil || ur.Restored != 2 || ur.Deleted != 1 {
		t.Fatalf("%+v %v", ur, err)
	}
	if got := snapshot(t, dir); got != before {
		t.Fatalf("撤回后表和导入前不一样：\n%s\n%s", before, got)
	}
}

func TestUndoLeavesLaterManualChangesAlone(t *testing.T) {
	s, _, ck := newStore(t, Options{})
	rep, _ := s.Import(file(idA, idB, idC))
	ck.tick(time.Hour)
	s.Remove([]string{idA}) // 导入后手动说不是：批次清空
	s.AddAuto("4000", "", "", judged(5))
	ur, err := s.Undo(rep.Batch)
	if err != nil || ur.Deleted != 2 || ur.Restored != 0 {
		t.Fatalf("%+v %v", ur, err)
	}
	if e, ok := s.Lookup(idA); !ok || e.Status != StatusDeclined || e.Source != SourceManual {
		t.Fatalf("手动改过的行不该被撤回动到：%+v", e)
	}
	if _, ok := s.Lookup("4000"); !ok {
		t.Fatal("按需发现的行不该被撤回动到")
	}
	if s.Confirmed(idB) || s.Confirmed(idC) {
		t.Fatal("这批新增的应该删掉")
	}
	if _, err := s.Undo(rep.Batch); err == nil {
		t.Fatal("同一批撤回第二次应该报错")
	}
}

func TestUndoDoesNotTouchOtherBatches(t *testing.T) {
	s, _, ck := newStore(t, Options{})
	r1, _ := s.Import(file(idA))
	ck.tick(2 * time.Second)
	r2, _ := s.Import(file(idB))
	if r1.Batch == r2.Batch {
		t.Fatal("两次有变化的导入批次号不该相同")
	}
	s.Undo(r2.Batch)
	if !s.Confirmed(idA) || s.Confirmed(idB) {
		t.Fatal("只该撤回第二批")
	}
}

func TestBatchSuffixWhenSameSecond(t *testing.T) {
	s, _, _ := newStore(t, Options{}) // 时钟不动
	r1, _ := s.Import(file(idA))
	r2, _ := s.Import(file(idB))
	if r1.Batch == "" || r2.Batch == "" || r1.Batch == r2.Batch {
		t.Fatalf("同一秒两个批次要区分：%q %q", r1.Batch, r2.Batch)
	}
}

func TestUndoBatchFormat(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	for _, b := range []string{"", "imp-", "x", "imp-abc", "imp-1; DROP", strings.Repeat("imp-1", 20), "../imp-1"} {
		if _, err := s.Undo(b); err == nil {
			t.Errorf("批次号 %q 应该报错", b)
		}
	}
	if _, err := s.Undo("imp-20200101000000"); err == nil {
		t.Error("不存在的批次应该报错")
	}
}

func TestImportRowCap(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	m := make(map[string]Entry, MaxEntries)
	for i := 0; i < MaxEntries; i++ {
		m["1"+pad(i)+"00000"] = Entry{Status: StatusDeclined, Source: SourceAuto}
	}
	s.entries = m
	rep, err := s.Import(file(idA))
	if err != nil || rep.Added != 0 || rep.Skipped != 1 || rep.Batch != "" {
		t.Fatalf("表满了不该新增：%+v %v", rep, err)
	}
}

func TestReadImportFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "artists-confirmed.json")
	os.WriteFile(good, file(idA), 0o644)
	if b, err := ReadImportFile(good); err != nil || len(b) == 0 {
		t.Fatalf("%v", err)
	}
	if _, err := ReadImportFile("artists-confirmed.json"); err == nil {
		t.Error("相对路径应该拒绝")
	}
	if _, err := ReadImportFile(filepath.Join(dir, "没有.json")); err == nil {
		t.Error("不存在的文件应该报错")
	}
	if _, err := ReadImportFile(dir); err == nil {
		t.Error("目录应该拒绝")
	}
	big := filepath.Join(dir, "big.json")
	f, _ := os.Create(big)
	f.Truncate(MaxImportBytes + 1)
	f.Close()
	if _, err := ReadImportFile(big); err == nil {
		t.Error("超大文件应该拒绝")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err == nil {
		if _, err := ReadImportFile(link); err == nil {
			t.Error("符号链接应该拒绝")
		}
	} // Windows 没有权限建符号链接时跳过这一格
	// 没有路径信息泄露到错误里
	_, err := ReadImportFile(filepath.Join(dir, "没有.json"))
	if err != nil && strings.Contains(err.Error(), dir) {
		t.Error("错误文字里不该带路径")
	}
}
