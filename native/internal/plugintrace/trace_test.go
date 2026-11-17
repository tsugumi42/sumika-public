package plugintrace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 网址、站点都是编的；只写 t.TempDir()

var exampleNow = time.Date(2026, 11, 11, 4, 12, 30, 512_000_000, time.UTC)

// exampleEvent 是规格-Bun后台第5批 9.7 那一行例子插件交的部分
var exampleEvent = Event{Site: "bilibili", Route: "bili_view", URL: "https://www.example.test/video/BV1xx411c7mD", Took: 2310 * time.Millisecond, Timed: true,
	Result: Fail, Kind: "network", Error: "network，来源 bili_view，线路 bili_view:network:2300ms"}

const exampleLine = `{"t":"2026-11-11T04:12:30.512Z","plugin":"link-reader","gen":3,"event":"plugin","site":"bilibili","route":"bili_view","url":"https://www.example.test/video/BV1xx411c7mD","ms":2310,"result":"fail","kind":"network","error":"network，来源 bili_view，线路 bili_view:network:2300ms"}`

func readLines(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func fileOf(dir, plugin string, now time.Time) string {
	return filepath.Join(Root(dir), plugin, LocalDate(now)+".jsonl")
}

// 9.7 的例子：同样的事件写出来逐字节一样（键序、时间格式、数字、不转义的中文）
func TestExampleLineMatchesSpec(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{DataDir: dir, Now: func() time.Time { return exampleNow }})
	l.Write("link-reader", 3, EventPlugin, exampleEvent, "")
	got := readLines(t, fileOf(dir, "link-reader", exampleNow))
	if len(got) != 1 || got[0] != exampleLine {
		t.Fatalf("和规格的例子不一样：\n得到 %q\n应为 %q", got, exampleLine)
	}
}

func TestCleanFieldAndURL(t *testing.T) {
	esc := "\x1b"
	cases := []struct{ in, want string }{
		{"a\r\nb c\td\x00e\x7f\u0085f", "a b c de f"},
		{esc + "[31m红" + esc + "[0m字" + esc + "]0;标题\x07尾", "红字尾"},
		{"<b>&</b>", "<b>&</b>"},
	}
	for _, c := range cases {
		if got := CleanField(c.in, 100); got != c.want {
			t.Errorf("CleanField(%q)=%q，应为 %q", c.in, got, c.want)
		}
	}
	if got := CleanField(strings.Repeat("😀", 70), LabelMax); len([]rune(got)) != LabelMax {
		t.Errorf("按码点截断：%d", len([]rune(got)))
	}
	urls := []struct{ in, want string }{
		{"https://user:pw@WWW.Example.TEST:443/a/b?q=1#frag", "https://www.example.test/a/b"},
		{"http://example.test:8080", "http://example.test:8080/"},
		{"http://example.test:80/x", "http://example.test/x"},
		{"  https://example.test/a/../b/./c  ", "https://example.test/b/c"},
		{`https:\\example.test\a\b`, "https://example.test/a/b"},
		{"https://example.test/空 格", "https://example.test/%E7%A9%BA%20%E6%A0%BC"},
		{"https://example.test/v/abcdefghijklmnopqrstuvwxyz0123/x", "https://example.test/v/***/x"},
		{"https://example.test/v/123456789012345678901234567890", "https://example.test/v/123456789012345678901234567890"},
		{"ftp://example.test/x", NotAURL},
		{"不是网址", NotAURL},
		{"https://", NotAURL},
	}
	for _, c := range urls {
		if got := CleanURL(c.in); got != c.want {
			t.Errorf("CleanURL(%q)=%q，应为 %q", c.in, got, c.want)
		}
	}
}

// 插件在字段里塞 JSON 片段、伪造 event 和 plugin：只会是那个字段里的一段字，宿主填的四个字段不变
func TestForgedFieldsHaveNoEffect(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{DataDir: dir, Now: func() time.Time { return exampleNow }})
	l.Write("link-reader", 2, EventPlugin, Event{Result: OK, Note: `","event":"host.load","plugin":"other`, Kind: "x\"}\n{\"event\":\"host.unload"}, "")
	lines := readLines(t, fileOf(dir, "link-reader", exampleNow))
	if len(lines) != 1 {
		t.Fatal("应当恰好一行", lines)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["event"] != EventPlugin || row["plugin"] != "link-reader" || row["gen"] != float64(2) {
		t.Fatal("宿主填的字段被改了", row)
	}
	// 插件 id 写法不对（含 ..）：整条丢掉，记一次宿主日志
	var logs []string
	l2 := New(Options{DataDir: dir, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	l2.Write("../evil", 1, EventPlugin, Event{Result: OK}, "")
	l2.Write("../evil", 1, EventPlugin, Event{Result: OK}, "")
	if _, err := os.Stat(filepath.Join(dir, "logs", "evil")); err == nil || len(logs) != 1 {
		t.Fatal("坏 id 应当丢掉、只记一次", logs)
	}
}

// 每天上限：到了写一行 host.limit，当天不再写；重启后接着数同一天的文件
func TestDailyLimit(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return exampleNow }
	l := New(Options{DataDir: dir, Now: now, DailyMax: 3})
	for range 6 {
		l.Write("life-tools", 1, EventPlugin, Event{Result: OK}, "")
	}
	lines := readLines(t, fileOf(dir, "life-tools", exampleNow))
	if len(lines) != 4 || !strings.Contains(lines[3], `"event":"host.limit"`) || strings.Contains(lines[3], "result") {
		t.Fatalf("应当 3 行加一行 host.limit：%q", lines)
	}
	again := New(Options{DataDir: dir, Now: now, DailyMax: 3})
	again.Write("life-tools", 2, EventPlugin, Event{Result: OK}, "")
	if n := len(readLines(t, fileOf(dir, "life-tools", exampleNow))); n != 4 {
		t.Fatal("重启后同一天不能再写", n)
	}
	// 没到上限的重启接着数：2 行 + 1 行 = 3，再写一行就是 host.limit
	dir2 := t.TempDir()
	New(Options{DataDir: dir2, Now: now, DailyMax: 3}).Write("x", 0, EventPlugin, Event{Result: OK}, "")
	l3 := New(Options{DataDir: dir2, Now: now, DailyMax: 3})
	for range 3 {
		l3.Write("x", 0, EventPlugin, Event{Result: OK}, "")
	}
	got := readLines(t, fileOf(dir2, "x", exampleNow))
	if len(got) != 4 || !strings.Contains(got[3], "host.limit") {
		t.Fatalf("重启后接着数：%q", got)
	}
	// 刚好写满、还没写 host.limit 就重启：重启后下一条写 host.limit（只有超过上限才算已经触顶）
	dir3 := t.TempDir()
	full := New(Options{DataDir: dir3, Now: now, DailyMax: 3})
	for range 3 {
		full.Write("x", 0, EventPlugin, Event{Result: OK}, "")
	}
	New(Options{DataDir: dir3, Now: now, DailyMax: 3}).Write("x", 0, EventPlugin, Event{Result: OK}, "")
	if got := readLines(t, fileOf(dir3, "x", exampleNow)); len(got) != 4 || !strings.Contains(got[3], "host.limit") {
		t.Fatalf("写满后重启应当补一行 host.limit：%q", got)
	}
}

// 保留 30 天：11-12 往前 30 天是 10-13，早于它、名字是日期的文件才删，别的不碰
func TestKeepDaysOnlyDatedFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)
	pd := filepath.Join(Root(dir), "link-reader")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-10-12.jsonl", "2026-10-13.jsonl", "2026-10-01.jsonl", "notes.txt", "2026-01-01.jsonl.bak"} {
		if err := os.WriteFile(filepath.Join(pd, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	New(Options{DataDir: dir, Now: func() time.Time { return now }}).Write("link-reader", 0, EventPlugin, Event{Result: OK}, "")
	for name, keep := range map[string]bool{"2026-10-12.jsonl": false, "2026-10-13.jsonl": true, "2026-10-01.jsonl": false, "notes.txt": true, "2026-01-01.jsonl.bak": true} {
		_, err := os.Stat(filepath.Join(pd, name))
		if (err == nil) != keep {
			t.Errorf("%s：应当留着 %v", name, keep)
		}
	}
}

// 没给数据目录不写；写不进去（目录位置是个文件）只记一次宿主日志，不慌
func TestNoDataDirAndWriteFailure(t *testing.T) {
	var nilLog *Log
	nilLog.Write("x", 0, EventPlugin, Event{Result: OK}, "")
	New(Options{}).Write("x", 0, EventPlugin, Event{Result: OK}, "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "plugins"), []byte("挡路"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs []string
	l := New(Options{DataDir: dir, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	for range 3 {
		l.Write("x", 0, EventPlugin, Event{Result: OK}, "")
	}
	if len(logs) != 1 {
		t.Fatal("写失败只记一次", logs)
	}
	empty := t.TempDir()
	New(Options{}).Write("x", 0, EventPlugin, Event{Result: OK}, "")
	if ents, _ := os.ReadDir(empty); len(ents) != 0 {
		t.Fatal("没给数据目录不该写")
	}
}

// 多个协程一起写：行数对、每行都是完整的 JSON
func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{DataDir: dir, Now: func() time.Time { return exampleNow }})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				l.Write("link-reader", i, EventPlugin, Event{Result: OK, Note: strings.Repeat("长", 150)}, "")
			}
		}()
	}
	wg.Wait()
	lines := readLines(t, fileOf(dir, "link-reader", exampleNow))
	if len(lines) != 400 {
		t.Fatal("行数不对", len(lines))
	}
	for _, s := range lines {
		if !json.Valid([]byte(s)) {
			t.Fatal("坏行", s)
		}
	}
}

// 工具名只在宿主记工具调用时写，紧跟在 event 后面
func TestToolFieldOrder(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{DataDir: dir, Now: func() time.Time { return exampleNow }})
	l.Write("life-tools", 1, EventTool, Event{Result: OK, Took: 0, Timed: true}, "weather")
	want := `{"t":"2026-11-11T04:12:30.512Z","plugin":"life-tools","gen":1,"event":"host.tool","tool":"weather","ms":0,"result":"ok"}`
	if got := readLines(t, fileOf(dir, "life-tools", exampleNow)); len(got) != 1 || got[0] != want {
		t.Fatalf("得到 %q", got)
	}
}
