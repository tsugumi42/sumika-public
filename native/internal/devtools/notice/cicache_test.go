package notice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 带缓存的 CI 选项：now 是这一次钩子运行的“现在”。
func cached(dir string, fg *fakeGH, session string, now time.Time) Options {
	return Options{Dir: dir, SessionID: session, Prompt: "你好", Now: now,
		CI: &CIOptions{Repo: "o/r", Gh: fg.fn, Timeout: 2 * time.Second, CacheTTL: 5 * time.Minute, FailBackoff: time.Minute}}
}

func cacheEntryOf(t *testing.T, dir, repo string) *cacheEntry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		t.Fatalf("没有缓存文件：%v", err)
	}
	var c cacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("缓存不是合法 JSON：%v\n%s", err, b)
	}
	return c[repo]
}

func oneFailure(fg *fakeGH) {
	fg.runs = []fakeRun{failedRun(111, "ci", "push", "main", time.Hour)}
	fg.jobs[111] = jobsJSON
}

func TestCacheWithinTTLDoesNotCallGh(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	if out := collect(t, cached(dir, fg, "s1", ciNow)); !strings.Contains(out, "/runs/111") {
		t.Fatalf("第一次真查并打出：%q", out)
	}
	if fg.refreshes() != 1 || fg.count("run", "view") != 1 {
		t.Fatalf("第一次该查 1 次列表和 1 次详情：%v", fg.calls)
	}
	// 4 分 59 秒后，另一个会话：用缓存，不调 gh，内容一样（含 job 详情）
	out := collect(t, cached(dir, fg, "s2", ciNow.Add(5*time.Minute-time.Second)))
	if !strings.Contains(out, "/runs/111") || !strings.Contains(out, "test (windows-latest) 的「go test -short（全部包）」") {
		t.Fatalf("缓存里该有这条和 job 详情：%q", out)
	}
	if fg.refreshes() != 1 || fg.count("run", "view") != 1 {
		t.Fatalf("5 分钟内不该再调 gh：%v", fg.calls)
	}
	// 同会话再来：照样按会话去重，也不调 gh
	if out := collect(t, cached(dir, fg, "s1", ciNow.Add(time.Minute))); out != "" {
		t.Fatalf("同会话不再提醒：%q", out)
	}
	if len(fg.calls) != 3 {
		t.Fatalf("gh 调用总数应还是 3（两个列表加一个详情）：%v", fg.calls)
	}
	if got := readSeen(t, dir); len(got["s1"].CI) != 1 || len(got["s2"].CI) != 1 {
		t.Fatalf("去重记录仍在 .已提醒.json：%+v", got)
	}
}

func TestCacheExpiredCallsGhAgain(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	collect(t, cached(dir, fg, "s1", ciNow))
	// 过期后出现新的失败：真查，并打出新的
	fg.runs = append(fg.runs, failedRun(222, "nightly", "schedule", "main", 10*time.Minute))
	fg.jobs[222] = jobsJSON
	out := collect(t, cached(dir, fg, "s1", ciNow.Add(5*time.Minute)))
	if fg.refreshes() != 2 {
		t.Fatalf("刚好 5 分钟就算过期，要真查：%v", fg.calls)
	}
	if !strings.Contains(out, "/runs/222") || strings.Contains(out, "/runs/111") {
		t.Fatalf("过期后查到新的失败：%q", out)
	}
	if e := cacheEntryOf(t, dir, "o/r"); !e.OK || len(e.Runs) != 2 || e.At != ciNow.Add(5*time.Minute).UTC().Format(timeFormat) {
		t.Fatalf("缓存该更新：%+v", e)
	}
}

func TestCacheNewFailureWithinTTLWaitsForExpiry(t *testing.T) {
	// 说明性的：缓存期内新出的失败要等缓存过期才看得到（5 分钟的代价，主人要的取舍）
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	collect(t, cached(dir, fg, "s1", ciNow))
	fg.runs = append(fg.runs, failedRun(222, "ci", "push", "main", time.Minute))
	if out := collect(t, cached(dir, fg, "s1", ciNow.Add(time.Minute))); out != "" {
		t.Fatalf("缓存期内不该看到新失败：%q", out)
	}
}

func TestCacheCorruptIsTreatedAsMissing(t *testing.T) {
	good := ciNow.Format(timeFormat)
	for name, content := range map[string]string{
		"乱码":       "{坏的",
		"数组":       "[]",
		"null":     "null",
		"空文件":      "",
		"时间坏了":     `{"o/r":{"at":"昨天","ok":true,"runs":[]}}`,
		"时间缺失":     `{"o/r":{"ok":true,"runs":[]}}`,
		"没标成功":     `{"o/r":{"at":"` + good + `","ok":false,"runs":[]}}`,
		"字段类型错":    `{"o/r":{"at":5,"ok":"是","runs":"没有"}}`,
		"别的仓库的缓存":  `{"x/y":{"at":"` + good + `","ok":true,"runs":[]}}`,
		"时间在遥远的将来": `{"o/r":{"at":"2099-01-01T00:00:00.000Z","ok":true,"runs":[]}}`,
	} {
		dir, fg, _ := newCase(t)
		oneFailure(fg)
		write(t, dir, cacheName, content)
		out := collect(t, cached(dir, fg, "s1", ciNow))
		if !strings.Contains(out, "/runs/111") || fg.refreshes() != 1 {
			t.Errorf("%s：缓存坏了该真查一次：out=%q calls=%v", name, out, fg.calls)
		}
		if e := cacheEntryOf(t, dir, "o/r"); e == nil || !e.OK || len(e.Runs) != 1 {
			t.Errorf("%s：真查后缓存该被修好：%+v", name, e)
		}
	}
}

func TestCacheIsPerRepo(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	collect(t, cached(dir, fg, "s1", ciNow))
	o := cached(dir, fg, "s2", ciNow.Add(time.Minute))
	o.CI.Repo = "other/repo"
	if out := collect(t, o); !strings.Contains(out, "other/repo/actions/runs/111") {
		t.Fatalf("%q", out)
	}
	if fg.refreshes() != 2 {
		t.Fatalf("别的仓库不能用这份缓存：%v", fg.calls)
	}
	if cacheEntryOf(t, dir, "o/r") == nil || cacheEntryOf(t, dir, "other/repo") == nil {
		t.Fatal("两个仓库各一份")
	}
}

func TestCacheOffWhenTTLZero(t *testing.T) {
	dir, fg, opts := newCase(t)
	oneFailure(fg)
	collect(t, opts("s1"))
	collect(t, opts("s2"))
	if fg.refreshes() != 2 {
		t.Fatalf("没开缓存每次都真查：%v", fg.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheName)); err == nil {
		t.Fatal("没开缓存就不该写缓存文件")
	}
}

func TestCacheFailureNotCachedAsEmptySuccessAndBacksOff(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	fg.listErr = errors.New("gh 失败：没登录")
	var warns []string
	mk := func(session string, now time.Time) Options {
		o := cached(dir, fg, session, now)
		o.Warn = func(m string) { warns = append(warns, m) }
		return o
	}
	if out := collect(t, mk("s1", ciNow)); out != "" || len(warns) != 1 {
		t.Fatalf("失败：out=%q warns=%v", out, warns)
	}
	e := cacheEntryOf(t, dir, "o/r")
	if e.OK || len(e.Runs) != 0 || e.FailAt == "" {
		t.Fatalf("失败不能写成空列表的成功缓存，只记失败时间：%+v", e)
	}
	// 30 秒后：冷却中，不调 gh，也不再往 stderr 说
	if out := collect(t, mk("s1", ciNow.Add(30*time.Second))); out != "" {
		t.Fatalf("冷却中：%q", out)
	}
	if fg.refreshes() != 1 || len(warns) != 1 {
		t.Fatalf("1 分钟内不重试、不再说：calls=%v warns=%v", fg.calls, warns)
	}
	// 过了 1 分钟，gh 好了：再试并写成成功缓存
	fg.listErr = nil
	out := collect(t, mk("s1", ciNow.Add(61*time.Second)))
	if !strings.Contains(out, "/runs/111") || fg.refreshes() != 2 {
		t.Fatalf("冷却过了该再试：out=%q calls=%v", out, fg.calls)
	}
	if e := cacheEntryOf(t, dir, "o/r"); !e.OK || e.FailAt != "" || len(e.Runs) != 1 {
		t.Fatalf("成功后缓存该是新的：%+v", e)
	}
}

func TestCacheFailureAfterExpiredGoodCacheDoesNotUseStale(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	collect(t, cached(dir, fg, "s1", ciNow))
	fg.listErr = errors.New("gh 超时")
	if out := collect(t, cached(dir, fg, "s2", ciNow.Add(10*time.Minute))); out != "" {
		t.Fatalf("缓存过期后 gh 又失败，不能拿旧数据当新的：%q", out)
	}
	if e := cacheEntryOf(t, dir, "o/r"); e.FailAt == "" {
		t.Fatalf("该记下失败时间：%+v", e)
	}
}

func TestCacheHangTimesOutThenBacksOff(t *testing.T) {
	dir, _, _ := newCase(t)
	write(t, dir, "复盘-2026-11-19.md", "## 给主会话\n- 报告那段照常\n")
	var calls atomic.Int32
	hang := func(ctx context.Context, args ...string) ([]byte, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	mk := func(session string, now time.Time) Options {
		return Options{Dir: dir, SessionID: session, Now: now,
			CI: &CIOptions{Repo: "o/r", Gh: hang, Timeout: 100 * time.Millisecond, CacheTTL: 5 * time.Minute, FailBackoff: time.Minute}}
	}
	collect(t, mk("s1", ciNow))
	first := calls.Load() // 第一次：push 和 schedule 两个列表请求都挂到时限
	out := collect(t, mk("s2", ciNow.Add(10*time.Second)))
	if first != 2 || calls.Load() != first || !strings.Contains(out, "报告那段照常") {
		t.Fatalf("冷却中第二个会话不该再调 gh（只数调用次数，不量时间）：第一次 %d 个，之后共 %d 个，out=%q", first, calls.Load(), out)
	}
}

func TestRunUsesCache(t *testing.T) {
	dir := t.TempDir()
	fg := &fakeGH{jobs: map[int64]string{7: jobsJSON}}
	fg.runs = []fakeRun{{ID: 7, Workflow: "ci", Event: "push", Branch: "main", SHA: sha40, CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Conclusion: "failure"}}
	oldGh, oldOrigin := runGh, runOrigin
	defer func() { runGh, runOrigin = oldGh, oldOrigin }()
	runGh = fg.fn
	runOrigin = func(string) (string, error) { return "o/r", nil }
	for _, s := range []string{"a", "b", "c"} {
		_, out, _ := runHook(t, []string{"--reports", dir}, t.TempDir(), `{"session_id":"`+s+`"}`)
		if !strings.Contains(out, "/actions/runs/7") {
			t.Fatalf("会话 %s 该看到：%q", s, out)
		}
	}
	if fg.refreshes() != 1 {
		t.Fatalf("三个会话共用一份缓存，gh 列表只该查 1 次：%v", fg.calls)
	}
}
