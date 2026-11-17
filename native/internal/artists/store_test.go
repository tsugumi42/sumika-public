package artists

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 测试里的账号 ID、名字都是编的。
const (
	idA = "1001"
	idB = "1002"
	idC = "1003"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) tick(d time.Duration) {
	c.t = c.t.Add(d)
}

func newStore(t *testing.T, opt Options) (*Store, string, *clock) {
	t.Helper()
	dir := t.TempDir()
	ck := &clock{t: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	if opt.Now == nil {
		opt.Now = ck.now
	}
	s, err := NewBook(dir, opt).Platform("x")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir, ck
}

func reopen(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewBook(dir, Options{}).Platform("x")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func judged(score float64) Judgement { return Judgement{Score: score, Clues: []string{"自称绘师"}} }

func TestValidIDTable(t *testing.T) {
	cases := []struct {
		platform, id string
		want         bool
	}{
		{"x", "1", true},
		{"x", "1234567890123456789", true},
		{"x", "12345678901234567890", true}, // 20 位
		{"x", "123456789012345678901", false},
		{"x", "", false},
		{"x", "0", false},
		{"x", "000", false},
		{"x", "0123", false}, // 前导零不认：X 的 ID 没有前导零，规范化会改变键
		{"x", "12a", false},
		{"x", " 12", false},
		{"x", "12 ", false},
		{"x", "-12", false},
		{"x", "１２３", false}, // 全角数字
		{"x", "12\n", false},
		{"pixiv", "123", false}, // 平台没做
		{"", "123", false},
	}
	for _, c := range cases {
		if got := ValidID(c.platform, c.id); got != c.want {
			t.Errorf("ValidID(%q,%q)=%v，想要 %v", c.platform, c.id, got, c.want)
		}
	}
}

func TestPlatformNotDone(t *testing.T) {
	b := NewBook(t.TempDir(), Options{})
	for _, p := range []string{"pixiv", "bluesky", "misskey", "", "../x"} {
		if _, err := b.Platform(p); !errors.Is(err, ErrPlatform) {
			t.Errorf("平台 %q 应该报 ErrPlatform，得到 %v", p, err)
		}
	}
}

func TestEmptyStoreNothingConfirmedAndNoFile(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	if s.Confirmed(idA) {
		t.Fatal("空表里不该有生效的画师")
	}
	if _, ok := s.Lookup(idA); ok {
		t.Fatal("空表里不该有行")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("没改过表不该写文件")
	}
}

func TestAddAutoThenReload(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	st, err := s.AddAuto(idA, "paper_cloud", "纸云", judged(4))
	if err != nil || st != StatusPending {
		t.Fatalf("得到 %q %v，想要 pending", st, err)
	}
	st, err = s.AddAuto(idB, "someone", "某人", judged(1))
	if err != nil || st != StatusDeclined {
		t.Fatalf("得到 %q %v，想要 declined", st, err)
	}
	if s.Confirmed(idA) || s.Confirmed(idB) {
		t.Fatal("按需判断的结果不能直接生效")
	}
	// 同一账号只判断一次：已有行不再动
	st, err = s.AddAuto(idA, "renamed", "改名", judged(0))
	if err != nil || st != "" {
		t.Fatalf("已有行再判断应该什么都不做，得到 %q %v", st, err)
	}
	e, _ := s.Lookup(idA)
	if e.Handle != "paper_cloud" || e.Status != StatusPending || e.Score != 4 || len(e.Clues) != 1 || e.Source != SourceAuto {
		t.Fatalf("行被改了：%+v", e)
	}
	// 重载后内容相同
	s2 := reopen(t, dir)
	for _, id := range []string{idA, idB} {
		a, _ := s.Lookup(id)
		b, ok := s2.Lookup(id)
		if !ok || a.Status != b.Status || a.Handle != b.Handle || !a.At.Equal(b.At) || a.Score != b.Score {
			t.Fatalf("重载后 %s 不一样：%+v / %+v", id, a, b)
		}
	}
}

func TestAddAutoBadIDWritesNothing(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	for _, id := range []string{"", "0", "abc", "01"} {
		if _, err := s.AddAuto(id, "h", "n", judged(9)); !errors.Is(err, ErrBadID) {
			t.Errorf("ID %q 应该报格式错，得到 %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("全是坏 ID 不该写文件")
	}
}

func TestAddAutoCleansExternalText(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	evil := "名\r\n字\x00‮" + strings.Repeat("长", 200)
	if _, err := s.AddAuto(idA, "h\nandle", evil, judged(5)); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Lookup(idA)
	if strings.ContainsAny(e.Handle+e.Name, "\r\n\x00‮") {
		t.Fatalf("控制字符没清掉：%q %q", e.Handle, e.Name)
	}
	if n := len([]rune(e.Name)); n > textMax {
		t.Fatalf("名字没截：%d 字", n)
	}
}

func TestPendingCapFallsToDeclined(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	for i := 0; i < MaxPending; i++ {
		id := "9" + strconv.Itoa(1000000+i)
		if st, err := s.AddAuto(id, "", "", judged(5)); err != nil || st != StatusPending {
			t.Fatalf("第 %d 个：%q %v", i, st, err)
		}
	}
	st, err := s.AddAuto("5000", "", "", judged(5))
	if err != nil || st != StatusDeclined {
		t.Fatalf("待确认满了应该落 declined，得到 %q %v", st, err)
	}
	if n := s.Counts()[StatusPending]; n != MaxPending {
		t.Fatalf("待确认行数 %d", n)
	}
}

func TestEntriesCap(t *testing.T) {
	var logs []string
	s, _, _ := newStore(t, Options{Logf: func(f string, a ...any) { logs = append(logs, f) }})
	// 直接塞满再试，免得写 20000 次盘
	m := make(map[string]Entry, MaxEntries)
	for i := 0; i < MaxEntries; i++ {
		m["1"+strings.Repeat("0", 8)+pad(i)] = Entry{Status: StatusDeclined, Source: SourceAuto}
	}
	s.entries = m
	st, err := s.AddAuto("777", "", "", judged(9))
	if err != nil || st != "" {
		t.Fatalf("满了不该新增：%q %v", st, err)
	}
	if _, ok := s.Lookup("777"); ok {
		t.Fatal("满了还是加进去了")
	}
	if len(logs) == 0 {
		t.Fatal("满了要写一行日志")
	}
}

func pad(i int) string {
	d := []byte("00000")
	for k := 4; k >= 0 && i > 0; k-- {
		d[k] = byte('0' + i%10)
		i /= 10
	}
	return string(d)
}

func TestWriteFailureLeavesFileAndMemoryUnchanged(t *testing.T) {
	failing := false
	s, dir, _ := newStore(t, Options{Rename: func(o, n string) error {
		if failing {
			return errors.New("模拟改名失败")
		}
		return os.Rename(o, n)
	}})
	if _, err := s.AddAuto(idA, "a", "A", judged(4)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "x.json"))
	failing = true
	if _, err := s.AddAuto(idB, "b", "B", judged(4)); err == nil {
		t.Fatal("改名失败应该报错")
	}
	if _, ok := s.Lookup(idB); ok {
		t.Fatal("写盘失败，内存里却多了一行")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "x.json"))
	if string(before) != string(after) {
		t.Fatal("写盘失败不该动原文件")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*"))
	if len(left) != 0 {
		t.Fatalf("留下了临时文件：%v", left)
	}
}

func TestCorruptFileRenamedAndStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte("{坏的"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs int
	s, err := NewBook(dir, Options{Logf: func(string, ...any) { logs++ }}).Platform("x")
	if err != nil {
		t.Fatal(err)
	}
	if s.Confirmed(idA) || len(s.Counts()) != 0 {
		t.Fatal("坏文件应该按空表")
	}
	bad, _ := filepath.Glob(filepath.Join(dir, "x.json.bad-*"))
	if len(bad) != 1 || logs == 0 {
		t.Fatalf("坏文件要改名保留并写日志：%v %d", bad, logs)
	}
	if b, _ := os.ReadFile(bad[0]); string(b) != "{坏的" {
		t.Fatal("保留的坏文件内容被改了")
	}
	// 之后可以正常写
	if _, err := s.AddAuto(idA, "", "", judged(5)); err != nil {
		t.Fatal(err)
	}
}

func TestWrongVersionOrPlatformIsCorrupt(t *testing.T) {
	for name, body := range map[string]string{
		"版本":  `{"version":2,"platform":"x","entries":{}}`,
		"平台":  `{"version":1,"platform":"pixiv","entries":{}}`,
		"坏ID": `{"version":1,"platform":"x","entries":{"abc":{"status":"confirmed"}}}`,
		"坏状态": `{"version":1,"platform":"x","entries":{"1001":{"status":"vip"}}}`,
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "x.json"), []byte(body), 0o644)
		s, _ := NewBook(dir, Options{}).Platform("x")
		if s.Confirmed("1001") {
			t.Errorf("%s：不该认", name)
		}
		if bad, _ := filepath.Glob(filepath.Join(dir, "x.json.bad-*")); len(bad) != 1 {
			t.Errorf("%s：该改名保留", name)
		}
	}
}

func TestCorruptAndRenameFailsDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.json"), []byte("{坏的"), 0o644)
	s, _ := NewBook(dir, Options{Rename: func(string, string) error { return errors.New("不让改") }}).Platform("x")
	if _, err := s.AddAuto(idA, "", "", judged(5)); err == nil {
		t.Fatal("坏文件又改不了名，不该覆盖它")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x.json")); string(b) != "{坏的" {
		t.Fatal("坏文件被覆盖了")
	}
}

func TestConfirmRemoveStateMachine(t *testing.T) {
	s, dir, ck := newStore(t, Options{})
	s.AddAuto(idA, "a", "A", judged(5)) // pending
	s.AddAuto(idB, "b", "B", judged(0)) // declined(auto)
	ck.tick(time.Hour)

	rep, err := s.Confirm([]string{idA, idB})
	if err != nil || rep.Confirmed != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, id := range []string{idA, idB} {
		e, _ := s.Lookup(id)
		if e.Status != StatusConfirmed || e.Source != SourceManual || e.Batch != "" || e.Prev != nil {
			t.Fatalf("确认后的行不对：%+v", e)
		}
	}
	if !s.Confirmed(idA) {
		t.Fatal("确认后应该生效")
	}
	// 再确认：已经生效，不报错、不变
	rep, err = s.Confirm([]string{idA})
	if err != nil || rep.Confirmed != 0 || rep.Already != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	// 表里没有的：整批不动
	if _, err := s.Confirm([]string{idA, idC}); err == nil {
		t.Fatal("表里没有的账号应该报错")
	}
	if _, ok := s.Lookup(idC); ok {
		t.Fatal("confirm 不能凭空新增")
	}

	ck.tick(time.Hour)
	rr, err := s.Remove([]string{idA})
	if err != nil || rr.Removed != 1 {
		t.Fatalf("%+v %v", rr, err)
	}
	if s.Confirmed(idA) {
		t.Fatal("移除后不该生效")
	}
	e, _ := s.Lookup(idA)
	if e.Status != StatusDeclined || e.Source != SourceManual {
		t.Fatalf("%+v", e)
	}
	// 移除后不会被按需判断重新提：已有行
	if st, _ := s.AddAuto(idA, "a", "A", judged(9)); st != "" {
		t.Fatal("手动移除的账号不该被再次发现")
	}
	// 移除一批里有一个不在表里：整批不动
	if _, err := s.Remove([]string{idB, idC}); err == nil {
		t.Fatal("表里没有的账号应该报错")
	}
	if !s.Confirmed(idB) {
		t.Fatal("整批报错时 idB 不该被动")
	}
	// 重载后一致
	s2 := reopen(t, dir)
	if s2.Confirmed(idA) || !s2.Confirmed(idB) {
		t.Fatal("重载后状态不对")
	}
}

func TestCommandIDValidation(t *testing.T) {
	s, _, _ := newStore(t, Options{})
	s.AddAuto(idA, "", "", judged(5))
	if _, err := s.Confirm(nil); err == nil {
		t.Fatal("没写 ID 应该报错")
	}
	if _, err := s.Confirm([]string{"abc"}); !errors.Is(err, ErrBadID) {
		t.Fatalf("坏 ID：%v", err)
	}
	many := make([]string, MaxIDsPerCommand+1)
	for i := range many {
		many[i] = "1" + pad(i)
	}
	if _, err := s.Remove(many); err == nil {
		t.Fatal("超过 200 个应该拒绝")
	}
	// 重复的 ID 只算一次
	rep, err := s.Confirm([]string{idA, idA})
	if err != nil || rep.Confirmed != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestListOrderFilterLimit(t *testing.T) {
	s, _, ck := newStore(t, Options{})
	s.AddAuto(idA, "", "", judged(5))
	ck.tick(time.Minute)
	s.AddAuto(idB, "", "", judged(6))
	ck.tick(time.Minute)
	s.AddAuto(idC, "", "", judged(0))
	rows := s.List(StatusPending, 0)
	if len(rows) != 2 || rows[0].ID != idB || rows[1].ID != idA {
		t.Fatalf("应该新到旧：%+v", rows)
	}
	if rows := s.List("all", 2); len(rows) != 2 || rows[0].ID != idC {
		t.Fatalf("%+v", rows)
	}
	if rows := s.List(StatusConfirmed, 0); len(rows) != 0 {
		t.Fatal("没有生效的")
	}
}

func TestConcurrentObserveAndLookup(t *testing.T) {
	s, dir, _ := newStore(t, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 25; k++ {
				id := "2" + pad(i*100+k)
				s.AddAuto(id, "h", "n", judged(5))
				s.Confirmed(id)
				s.List("all", 5)
			}
		}()
	}
	wg.Wait()
	if n := s.Counts()[StatusPending]; n != 200 {
		t.Fatalf("应该 200 行，得到 %d", n)
	}
	if n := reopen(t, dir).Counts()[StatusPending]; n != 200 {
		t.Fatalf("重载后 %d 行", n)
	}
}
