package notice

// 独立复核（2026-11-19）提出的几条：详情待补、列表和详情各自的时限、按事件分开列、记录文件的并发读写、goroutine 里 panic。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/devtools/selfbuild"
)

// ---- 1. 详情没取到：待补，不打、不记成已提醒，缓存只给短 TTL，下次补上再提醒 ----

func TestPartialDetailHeldThenCompleted(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	fg.viewFailTimes = 1 // 第一次问详情失败
	var warns []string
	mk := func(now time.Time) Options {
		o := cached(dir, fg, "s1", now)
		o.Warn = func(m string) { warns = append(warns, m) }
		return o
	}
	if out := collect(t, mk(ciNow)); out != "" {
		t.Fatalf("详情没取到的运行这次不该打：%q", out)
	}
	if e := cacheEntryOf(t, dir, "o/r"); !e.OK || !e.Partial || e.Runs[0].Detail != "" || e.Runs[0].Fails != 1 {
		t.Fatalf("缓存该标成待补：%+v", e)
	}
	if _, err := os.Stat(filepath.Join(dir, recordName)); err == nil {
		t.Fatalf("没打也没有报告，不该写记录（运行编号不能记成已提醒）：%v", readSeen(t, dir))
	}
	// 20 秒后（短 TTL 30 秒内）：用缓存，不调 gh
	if out := collect(t, mk(ciNow.Add(20*time.Second))); out != "" || fg.refreshes() != 1 || fg.count("run", "view") != 1 {
		t.Fatalf("短 TTL 内不该再调 gh：out=%q calls=%v", out, fg.calls)
	}
	// 31 秒后：重新查，只补这一条的详情，补上了就打并记成已提醒
	out := collect(t, mk(ciNow.Add(31*time.Second)))
	if !strings.Contains(out, "/runs/111") || !strings.Contains(out, "test (windows-latest) 的「go test -short（全部包）」") {
		t.Fatalf("补上详情后该打：%q", out)
	}
	if fg.refreshes() != 2 || fg.count("run", "view") != 2 {
		t.Fatalf("第二轮只补详情：%v", fg.calls)
	}
	if e := cacheEntryOf(t, dir, "o/r"); e.Partial || e.Runs[0].Detail == "" {
		t.Fatalf("补齐后缓存回到正常 TTL：%+v", e)
	}
	if got := readSeen(t, dir)["s1"].CI; len(got) != 1 || got[0] != 111 {
		t.Fatalf("补上后才记成已提醒：%v", got)
	}
	if out := collect(t, mk(ciNow.Add(40*time.Second))); out != "" {
		t.Fatalf("已提醒过不再打：%q", out)
	}
}

func TestPartialDetailAlreadyKnownIsReusedNotRefetched(t *testing.T) {
	dir, fg, _ := newCase(t)
	fg.runs = []fakeRun{failedRun(111, "ci", "push", "main", time.Hour), failedRun(222, "ci", "push", "main", 2*time.Hour)}
	fg.jobs[111], fg.jobs[222] = jobsJSON, jobsJSON
	fg.viewFailTimes = 1 // 两条里有一条失败（并行，哪条不定）
	collect(t, cached(dir, fg, "s1", ciNow))
	before := fg.count("run", "view")
	collect(t, cached(dir, fg, "s2", ciNow.Add(31*time.Second)))
	if got := fg.count("run", "view") - before; got != 1 {
		t.Fatalf("第二轮只该补缺的那一条，实际问了 %d 次详情：%v", got, fg.calls)
	}
}

func TestPartialDetailGivesUpAfterThreeFailures(t *testing.T) {
	dir, fg, _ := newCase(t)
	oneFailure(fg)
	fg.viewAlwaysFail = true
	outs := []string{
		collect(t, cached(dir, fg, "s1", ciNow)),
		collect(t, cached(dir, fg, "s1", ciNow.Add(31*time.Second))),
		collect(t, cached(dir, fg, "s1", ciNow.Add(62*time.Second))),
	}
	if outs[0] != "" || outs[1] != "" {
		t.Fatalf("前两次详情失败都该先留着：%q", outs[:2])
	}
	if !strings.Contains(outs[2], noDetail+" https://github.com/o/r/actions/runs/111") {
		t.Fatalf("连续失败 3 次后写“没取到”放行：%q", outs[2])
	}
}

func TestListAndDetailHaveSeparateDeadlines(t *testing.T) {
	_, fg, opts := newCase(t)
	oneFailure(fg)
	o := opts("s1")
	o.CI.Timeout, o.CI.DetailTimeout = time.Minute, 5*time.Minute
	collect(t, o)
	list, view := fg.deadlines["list"], fg.deadlines["view"]
	if list.IsZero() || view.IsZero() || view.Sub(list) < 3*time.Minute {
		t.Fatalf("详情的时限该是自己的 5 分钟，不和列表共用 1 分钟：list=%v view=%v", list, view)
	}
}

// ---- 4. 按 push 和 schedule 各列一次 ----

func TestListsAreSplitByEventAndMerged(t *testing.T) {
	_, fg, opts := newCase(t)
	fg.byEvent = map[string][]fakeRun{
		"push":     {failedRun(1, "ci", "push", "main", time.Hour)},
		"schedule": {failedRun(2, "nightly", "schedule", "main", 2*time.Hour)},
	}
	fg.jobs[1], fg.jobs[2] = jobsJSON, jobsJSON
	out := collect(t, opts("s1"))
	if !strings.Contains(out, "/runs/1") || !strings.Contains(out, "/runs/2") {
		t.Fatalf("两种事件的失败都该有：%q", out)
	}
	if strings.Index(out, "/runs/1") > strings.Index(out, "/runs/2") {
		t.Fatalf("合并后按时间新的在前：%q", out)
	}
	if fg.count("run", "list") != 2 {
		t.Fatalf("应该列两次：%v", fg.calls)
	}
}

func TestOneListFailingMakesWholeQueryFail(t *testing.T) {
	dir, fg, _ := newCase(t)
	fg.byEvent = map[string][]fakeRun{"push": {failedRun(1, "ci", "push", "main", time.Hour)}}
	fg.jobs[1] = jobsJSON
	fg.listErrByEvent = map[string]error{"schedule": errors.New("schedule 那次失败")}
	if out := collect(t, cached(dir, fg, "s1", ciNow)); out != "" {
		t.Fatalf("有一次列表失败就整个当失败，不拿一半的结果当全部：%q", out)
	}
	if e := cacheEntryOf(t, dir, "o/r"); e.OK || e.FailAt == "" {
		t.Fatalf("不能写成成功缓存：%+v", e)
	}
}

// ---- 6. goroutine 里 panic 不崩，当作这次查询失败 ----

func TestPanicInGhIsTreatedAsFailure(t *testing.T) {
	for name, set := range map[string]func(*fakeGH){
		"列表里 panic": func(f *fakeGH) { f.listPanic = true },
		"详情里 panic": func(f *fakeGH) { f.viewPanic = true },
	} {
		dir, fg, _ := newCase(t)
		write(t, dir, "复盘-2026-11-19.md", "## 给主会话\n- 报告那段照常\n")
		oneFailure(fg)
		set(fg)
		var warns []string
		o := cached(dir, fg, "s1", ciNow)
		o.Warn = func(m string) { warns = append(warns, m) }
		out, err := Collect(o)
		if err != nil || !strings.Contains(out, "报告那段照常") || strings.Contains(out, "CI 结果") || len(warns) != 1 {
			t.Errorf("%s：out=%q err=%v warns=%v", name, out, err, warns)
		}
		if e := cacheEntryOf(t, dir, "o/r"); e.OK || e.FailAt == "" {
			t.Errorf("%s：不能写成成功缓存：%+v", name, e)
		}
	}
}

// ---- 3. 记录文件：并发读写不丢、读不了不覆盖、改名失败重试、锁取不到不硬写 ----

func TestConcurrentWritersLoseNoRecord(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "复盘-2026-11-19.md", "## 给主会话\n- 大家都该看到\n")
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Collect(Options{Dir: dir, SessionID: fmt.Sprintf("会话%02d", i), Now: ciNow})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Collect 报错：%v", err)
		}
	}
	if got := readSeen(t, dir); len(got) != n {
		t.Fatalf("%d 个会话同时写，记录里只剩 %d 个", n, len(got))
	}
}

func TestLoadSeenDistinguishesMissingCorruptAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	if s, err := loadSeen(filepath.Join(dir, "没有.json")); err != nil || len(s.keys) != 0 {
		t.Fatalf("不存在是空的：%v %v", s, err)
	}
	write(t, dir, "坏.json", "{坏的")
	if s, err := loadSeen(filepath.Join(dir, "坏.json")); err != nil || len(s.keys) != 0 {
		t.Fatalf("写坏了是空的：%v %v", s, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "是目录.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := loadSeen(filepath.Join(dir, "是目录.json")); err == nil || s != nil {
		t.Fatalf("读不了（不是不存在）要报错，不能当空的去覆盖写：%v %v", s, err)
	}
}

func TestCollectDoesNotOverwriteUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "复盘-2026-11-19.md", "## 给主会话\n- x\n")
	if err := os.Mkdir(filepath.Join(dir, recordName), 0o755); err != nil { // 记录文件的位置被占成了目录：读不了
		t.Fatal(err)
	}
	out, err := Collect(Options{Dir: dir, SessionID: "s1", Now: ciNow})
	if err == nil || out != "" {
		t.Fatalf("读不了记录要报错、不打：out=%q err=%v", out, err)
	}
	if st, e := os.Stat(filepath.Join(dir, recordName)); e != nil || !st.IsDir() {
		t.Fatal("读不了的记录不该被覆盖")
	}
}

func TestWriteAtomicRetriesRename(t *testing.T) {
	dir := t.TempDir()
	old := renameFile
	defer func() { renameFile = old }()
	fails := 3
	calls := 0
	renameFile = func(a, b string) error {
		calls++
		if fails > 0 {
			fails--
			return errors.New("Access is denied.")
		}
		return os.Rename(a, b)
	}
	target := filepath.Join(dir, "x.json")
	if err := writeAtomic(target, []byte("{}")); err != nil || calls != 4 {
		t.Fatalf("前 3 次被拒该重试到成功：err=%v calls=%d", err, calls)
	}
	if b, _ := os.ReadFile(target); string(b) != "{}" {
		t.Fatalf("内容不对：%q", b)
	}
	renameFile = func(a, b string) error { return errors.New("一直被拒") }
	if err := writeAtomic(target, []byte("新")); err == nil {
		t.Fatal("一直失败要报错")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp.*")); len(left) != 0 {
		t.Fatalf("失败后不该留临时文件：%v", left)
	}
}

func TestCollectErrorsWhenLockCannotBeTaken(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "复盘-2026-11-19.md", "## 给主会话\n- x\n")
	release, err := selfbuild.AcquireLock(filepath.Join(dir, lockName), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := lockWait
	lockWait = 150 * time.Millisecond
	defer func() { lockWait = old }()
	out, err := Collect(Options{Dir: dir, SessionID: "s1", Now: ciNow})
	if err == nil || out != "" {
		t.Fatalf("别人占着锁、等不到：要报错、不打、不硬写：out=%q err=%v", out, err)
	}
	if _, e := os.Stat(filepath.Join(dir, recordName)); e == nil {
		t.Fatal("没拿到锁不该写记录")
	}
}
