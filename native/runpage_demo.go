package main

// 运行页的演示数据：-demo 里看用量、存储两个标签用（数据全是编的，不碰任何库），也给量元素数、帧耗时的测试造"十个角色加一年"的数据。
// 同一份"每天 × 类别 × 模型"的流水按范围现算汇总，所以切时间范围时数字会跟着变，和真实读服务的行为一致。

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/runstats"
)

type demoUsageRec struct {
	day   time.Time // 本地零点
	task  string
	model string
	st    ctlproto.UsageStats
}

type demoStats struct {
	mu      sync.Mutex
	now     func() time.Time
	recs    []demoUsageRec
	store   ctlproto.StorageReport
	scanEnd time.Time // 演示的重新统计到这个时刻结束；零值 = 没在统计
	scanAt  time.Time
}

var demoModels = []string{"deepseek-v4-1-flash", "agnes-2.5-flash", "claude-sonnet-5-5"}

// newDemoStats 是 -demo 用的小份数据：最近 40 天、六个角色
func newDemoStats() *demoStats { return buildDemoStats(6, 40, time.Now) }

// buildDemoStats 造 days 天、chars 个角色的数据。角色只体现在存储的记忆库、角色卡分行上：用量的读数没有角色这一维
func buildDemoStats(chars, days int, now func() time.Time) *demoStats {
	d := &demoStats{now: now}
	rng := rand.New(rand.NewSource(11))
	today := dayStart(now())
	weights := map[string]int{
		ctlproto.TaskForeground: 40, ctlproto.TaskMemory: 12, ctlproto.TaskGuard: 6,
		ctlproto.TaskCompact: 3, ctlproto.TaskAdvisor: 2, ctlproto.TaskVision: 0,
	}
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -i)
		for _, m := range runstats.Tasks {
			w := weights[m.Key] * (1 + chars/3)
			if w == 0 && !(m.Key == ctlproto.TaskVision && i > days-20) {
				continue
			}
			if m.Key == ctlproto.TaskVision {
				w = 5 // 识图：只有历史行，Go 侧没有调用方
			}
			for mi, model := range demoModels {
				if rng.Intn(3) == 0 && mi > 0 {
					continue
				}
				runs := int64(w/(mi+1)) + int64(rng.Intn(5))
				if runs == 0 {
					continue
				}
				out := runs * int64(200+rng.Intn(400))
				uncached := runs * int64(300+rng.Intn(900))
				read := runs * int64(2000+rng.Intn(6000))
				st := ctlproto.UsageStats{Runs: runs, Failed: int64(rng.Intn(3)) * runs / 40, OKRuns: runs}
				st.OKMs = runs * int64(900+rng.Intn(5000))
				st.Uncached, st.CacheRead, st.CacheWrite = uncached, read, runs*int64(rng.Intn(200))
				st.Input = st.Uncached + st.CacheRead + st.CacheWrite
				st.Output, st.Reasoning = out, out/int64(2+rng.Intn(3))
				if i%17 == 3 {
					st.NoUsage = 1
				}
				d.recs = append(d.recs, demoUsageRec{day: day, task: m.Key, model: model, st: st})
			}
		}
	}
	d.store = demoStorage(chars, now())
	return d
}

func dayStart(t time.Time) time.Time {
	y, m, dd := t.In(time.Local).Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
}

func addStats(a *ctlproto.UsageStats, b ctlproto.UsageStats) {
	a.Runs += b.Runs
	a.Failed += b.Failed
	a.NoUsage += b.NoUsage
	a.Input += b.Input
	a.Uncached += b.Uncached
	a.CacheRead += b.CacheRead
	a.CacheWrite += b.CacheWrite
	a.Output += b.Output
	a.Reasoning += b.Reasoning
	a.OKRuns += b.OKRuns
	a.OKMs += b.OKMs
}

func finishStats(s *ctlproto.UsageStats) {
	if s.OKRuns > 0 {
		s.AvgMs = s.OKMs / s.OKRuns
	}
	if s.Input > 0 {
		s.Hit = float64(s.CacheRead) / float64(s.Input)
	}
}

func (d *demoStats) RangeOf(a ctlproto.StatsArgs) (ctlproto.UsageRange, error) {
	return runstats.New(nil, runstats.Options{Now: d.now}).RangeOf(a)
}

// Quality 演示数据里没有质量统计：返回空报告，不编造失败记录（质量标签是第 6 步）
func (d *demoStats) Quality(_ context.Context, _ ctlproto.UsageRange, _ ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	return ctlproto.QualityReport{}, nil
}

// Spans 演示数据里没有耗时记录：空报告。
func (d *demoStats) Spans(_ context.Context, _ ctlproto.UsageRange, _ ctlproto.StatsArgs) (ctlproto.SpansReport, error) {
	return ctlproto.SpansReport{}, nil
}

// Rebuild 演示数据里没有重建记录：空报告。
func (d *demoStats) Rebuild(_ context.Context, _ ctlproto.UsageRange) (ctlproto.RebuildReport, error) {
	return ctlproto.RebuildReport{}, nil
}

// ConvTurns 演示数据里没有请求记录：空报告。
func (d *demoStats) ConvTurns(_ context.Context, _ string, _ int) (ctlproto.ConvTurnsReport, error) {
	return ctlproto.ConvTurnsReport{}, nil
}

// RequestDetail 演示数据里没有请求记录。
func (d *demoStats) RequestDetail(_ context.Context, _ string) (ctlproto.RequestDetail, error) {
	return ctlproto.RequestDetail{}, runstats.ErrNoRequest
}

func (d *demoStats) Usage(_ context.Context, r ctlproto.UsageRange) (ctlproto.UsageReport, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rep := ctlproto.UsageReport{Range: r, Source: runstats.SourceRaw}
	tasks := map[string]*ctlproto.UsageTask{}
	models := map[string]*ctlproto.UsageModel{}
	days := map[string]*ctlproto.UsageBucket{}
	weeks := map[string]*ctlproto.UsageBucket{}
	for _, rec := range d.recs {
		ms := rec.day.UnixMilli()
		if ms < r.FromMS || ms >= r.ToMS {
			continue
		}
		addStats(&rep.Totals, rec.st)
		t := tasks[rec.task]
		if t == nil {
			t = &ctlproto.UsageTask{Key: rec.task}
			tasks[rec.task] = t
		}
		addStats(&t.UsageStats, rec.st)
		found := false
		for i := range t.Models {
			if t.Models[i].Model == rec.model {
				addStats(&t.Models[i].UsageStats, rec.st)
				found = true
			}
		}
		if !found {
			t.Models = append(t.Models, ctlproto.UsageModel{Model: rec.model, UsageStats: rec.st})
		}
		m := models[rec.model]
		if m == nil {
			m = &ctlproto.UsageModel{Model: rec.model}
			models[rec.model] = m
		}
		addStats(&m.UsageStats, rec.st)
		key := rec.day.Format("2006-01-02")
		b := days[key]
		if b == nil {
			b = &ctlproto.UsageBucket{Date: key}
			days[key] = b
		}
		addStats(&b.UsageStats, rec.st)
		wd := rec.day.AddDate(0, 0, -((int(rec.day.Weekday()) + 6) % 7))
		wk := wd.Format("2006-01-02")
		w := weeks[wk]
		if w == nil {
			w = &ctlproto.UsageBucket{Date: wk}
			weeks[wk] = w
		}
		addStats(&w.UsageStats, rec.st)
	}
	finishStats(&rep.Totals)
	for _, meta := range runstats.Tasks {
		t := tasks[meta.Key]
		if t == nil {
			t = &ctlproto.UsageTask{Key: meta.Key}
		}
		t.Label, t.Wired = meta.Label, meta.Wired
		finishStats(&t.UsageStats)
		for i := range t.Models {
			finishStats(&t.Models[i].UsageStats)
		}
		sort.Slice(t.Models, func(i, j int) bool { return t.Models[i].Input > t.Models[j].Input })
		rep.Tasks = append(rep.Tasks, *t)
	}
	for _, m := range models {
		finishStats(&m.UsageStats)
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool { return rep.Models[i].Input > rep.Models[j].Input })
	for _, b := range days {
		finishStats(&b.UsageStats)
		rep.Days = append(rep.Days, *b)
	}
	for _, b := range weeks {
		finishStats(&b.UsageStats)
		rep.Weeks = append(rep.Weeks, *b)
	}
	sort.Slice(rep.Days, func(i, j int) bool { return rep.Days[i].Date < rep.Days[j].Date })
	sort.Slice(rep.Weeks, func(i, j int) bool { return rep.Weeks[i].Date < rep.Weeks[j].Date })
	return rep, nil
}

func (d *demoStats) Storage(context.Context) (ctlproto.StorageReport, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rep := d.store
	if !d.scanEnd.IsZero() {
		if d.now().Before(d.scanEnd) {
			rep.Scan = ctlproto.StorageScan{Running: true, Entries: int64(d.now().Sub(d.scanAt) / time.Millisecond * 30), StartMS: d.scanAt.UnixMilli()}
		} else {
			d.scanEnd = time.Time{}
			d.store.ScannedAtMS, d.store.ScanMs, d.store.NeverScanned = d.now().UnixMilli(), 2100, false
			rep = d.store
		}
	}
	return rep, nil
}

func (d *demoStats) Rescan(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scanAt = d.now()
	d.scanEnd = d.scanAt.Add(2 * time.Second)
	return nil
}

func (d *demoStats) CancelScan() {
	d.mu.Lock()
	d.scanEnd = time.Time{}
	d.mu.Unlock()
}

func demoStorage(chars int, now time.Time) ctlproto.StorageReport {
	const mb, gb = int64(1) << 20, int64(1) << 30
	at := now.Add(-3 * time.Hour).UnixMilli()
	var memItems, cardItems []ctlproto.StorageItem
	for i := 0; i < chars; i++ {
		memItems = append(memItems, ctlproto.StorageItem{Name: fmt.Sprintf("角色%02d", i+1), Bytes: int64(2+i) * mb, Files: 3})
		cardItems = append(cardItems, ctlproto.StorageItem{Name: fmt.Sprintf("角色%02d", i+1), Bytes: int64(1+i%3) * mb, Files: 12})
	}
	sum := func(items []ctlproto.StorageItem) (b, f int64) {
		for _, it := range items {
			b += it.Bytes
			f += it.Files
		}
		return
	}
	mb1, mf := sum(memItems)
	cb, cf := sum(cardItems)
	cat := func(key, label string, bytes, files int64, rule string, auto bool, src string) ctlproto.StorageCategory {
		return ctlproto.StorageCategory{Key: key, Label: label, Bytes: bytes, Files: files, Rule: rule, Auto: auto, Source: src, AsOfMS: at, Approx: src != ctlproto.SourceLive}
	}
	cats := []ctlproto.StorageCategory{
		cat(ctlproto.StoreMedia, "媒体", 13*gb, 14000, "正文没有自动清理（被聊天记录引用）；写到一半的临时文件超过 1 小时的，在收图启动时清", false, ctlproto.SourceDelta),
		cat(ctlproto.StoreLegacyMedia, "旧版媒体", 0, 0, "没有自动清理（Bun 退役后的遗留，只读展示）", false, ctlproto.SourceNone),
		cat(ctlproto.StoreThumbs, "缩略图", 100*mb, 3200, "没有定期清理，可重建；写同名缩略图前顺手清超过 24 小时的 .partial", false, ctlproto.SourceSnapshot),
		cat(ctlproto.StoreChat, "聊天库", 112*mb, 4, "没有自动清理", false, ctlproto.SourceLive),
		cat(ctlproto.StoreTrace, "运行记录", 26*mb, 3, "没有自动清理", false, ctlproto.SourceLive),
		cat(ctlproto.StoreMemory, "角色记忆库", mb1, mf, "没有自动清理（开写入前的备份也不删旧的）", false, ctlproto.SourceLive),
		cat(ctlproto.StoreWork, "工作结果、检查点", 3*mb, 40, "无引用结果超过配置天数删、预留超过 24 小时回收；检查点按保留 30 天、最多 100 个、512 MB 裁", true, ctlproto.SourceLive),
		cat(ctlproto.StoreBackups, "备份", 200*mb, 18, "没有自动清理", false, ctlproto.SourceLive),
		cat(ctlproto.StoreLogs, "日志", 600<<10, 40, "没有自动清理（每天一个文件，没有轮转和删旧）", false, ctlproto.SourceLive),
		cat(ctlproto.StoreCards, "角色卡", cb, cf, "不清理", false, ctlproto.SourceLive),
		cat(ctlproto.StoreDev, "开发数据", 1500*mb, 24000, "没有自动清理", false, ctlproto.SourceSnapshot),
		cat(ctlproto.StoreModels, "本地模型", 4*gb, 12, "只显示大小，不清理", false, ctlproto.SourceSnapshot),
	}
	for i := range cats {
		switch cats[i].Key {
		case ctlproto.StoreMemory:
			cats[i].Items = memItems
		case ctlproto.StoreCards:
			cats[i].Items = cardItems
		case ctlproto.StoreChat:
			cats[i].Items = []ctlproto.StorageItem{{Name: "local", Bytes: 20 * mb, Files: 1}, {Name: "qq", Bytes: 92 * mb, Files: 3}}
		}
	}
	cats[1].Absent = true
	var total int64
	for _, c := range cats {
		total += c.Bytes
	}
	return ctlproto.StorageReport{Total: total, ScannedAtMS: at, ScanMs: 3700, DeltaBytes: 180 * mb, Live: true, Categories: cats}
}

// Resources 给演示模式一份固定的假数据：几行进程、运行时、队列，页面不是空的。数字是编的，只为了让界面有东西看
func (d *demoStats) Resources(context.Context) (ctlproto.ResourcesReport, error) {
	f := func(v float64) *float64 { return &v }
	n := func(v int64) *int64 { return &v }
	mb := int64(1 << 20)
	return ctlproto.ResourcesReport{
		SampledAtMS: d.now().UnixMilli(), IntervalMS: 2000, Cores: 16, MemoryKind: ctlproto.MemPrivate,
		GPU: ctlproto.GPUStatus{Available: true},
		Processes: []ctlproto.ProcessRow{
			{PID: 4100, Name: "sumika.exe", Category: ctlproto.ProcMain, CPU: f(1.8), Memory: n(310 * mb), Commit: n(420 * mb), GPU: f(2.1), GPUMemory: n(180 * mb)},
			{PID: 4200, Parent: 4100, Depth: 1, Name: "sumika-models.exe", Category: ctlproto.ProcModels, Label: "本地模型", CPU: f(0.4), Memory: n(540 * mb), Commit: n(600 * mb), GPU: f(0), GPUMemory: n(0)},
			{PID: 4300, Parent: 4100, Depth: 1, Name: "pwsh.exe", Category: ctlproto.ProcAnalyzer, Label: "命令分析", CPU: f(0), Memory: n(70 * mb), Commit: n(90 * mb)},
			{PID: 4400, Parent: 4100, Depth: 1, Name: "node.exe", Category: ctlproto.ProcMCP, Label: "演示服务", CPU: f(0.1), Memory: n(95 * mb), Commit: n(130 * mb)},
		},
		Runtime: ctlproto.RuntimeStats{Total: 180 * mb, HeapObj: 90 * mb, Stacks: 6 * mb, HeapLive: 80 * mb, Goroutines: 210, GCCycles: 1520, GCCPU: f(0.012), PauseP99Ns: n(420000), Other: n(130 * mb)},
		Queues: []ctlproto.QueueRow{
			{Key: "model-slots", Label: "模型请求槽位", Wired: true, Backlog: n(0), Running: n(1), Capacity: n(4), OldestWaitMs: n(0), State: ctlproto.QueueBusy},
			{Key: "backfill", Label: "补录"},
			{Key: "vision", Label: "识图"},
		},
	}, nil
}
