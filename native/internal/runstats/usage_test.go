package runstats

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/kernel"
	"sumika-native/internal/turnbudget"
)

// combo 是一种 (purpose, stage) 写法，want 是测试自己写死的预期类别（不调用 TaskOf，免得拿答案对答案）
type combo struct{ purpose, stage, want string }

var combos = []combo{
	// Go 内核写的行：purpose 有值
	{"foreground", "reply", ctlproto.TaskForeground},
	{"foreground", "reply-retry", ctlproto.TaskForeground},
	{"child", "child", ctlproto.TaskChild},
	{"advisor", "advisor", ctlproto.TaskAdvisor},
	{"verify", "verify", ctlproto.TaskAdvisor},
	{"compact", "compact", ctlproto.TaskCompact},
	{"memory", "memory", ctlproto.TaskMemory},
	{"guard", "guard", ctlproto.TaskGuard},
	{"goal_judge", "goal_judge", ctlproto.TaskGoal},
	// Bun 时代和导入的历史行：purpose 为 NULL，只有 stage
	{"", "reply", ctlproto.TaskForeground},
	{"", "reply-retry", ctlproto.TaskForeground},
	{"", "summarize", ctlproto.TaskMemory},
	{"", "learn-expression", ctlproto.TaskMemory},
	{"", "review-expression", ctlproto.TaskMemory},
	{"", "vision", ctlproto.TaskVision},
	{"", "guard", ctlproto.TaskGuard},
	// 认不出的：各一行，不落进别的类
	{"", "weird-stage", ctlproto.TaskUnknown},
	{"mystery", "reply", ctlproto.TaskUnknown},
}

var timestamps = []int64{
	ms(2026, 10, 31, 23, 59, 59, 999), // 周六，上一周
	ms(2026, 11, 1, 0, 0, 0, 0),       // 周日零点
	ms(2026, 11, 2, 0, 0, 0, 0),       // 周一零点：新的一周，也是默认 7 天的起点
	ms(2026, 11, 6, 12, 0, 0, 0),
	ms(2026, 11, 7, 23, 59, 59, 999), // 昨天最后一毫秒
	ms(2026, 11, 8, 0, 0, 0, 0),      // 今天第一毫秒
	ms(2026, 11, 8, 11, 59, 0, 0),
	ms(2026, 11, 9, 0, 0, 0, 0), // 明天零点：默认范围不含
}

// buildFixture 灌 18 种写法 × 2 个模型 × 8 个时间点 = 288 行，token、耗时、出错、没拿到用量按序号变化
func buildFixture() []fx {
	var rows []fx
	n := 0
	for _, c := range combos {
		for mi, model := range []string{"m/alpha", "m/beta"} {
			for _, at := range timestamps {
				n++
				r := fx{purpose: c.purpose, stage: c.stage, model: model, at: at,
					input: int64(10 + n), cacheRead: int64(100 * (n % 5)), output: int64(5 + n%7), ms: i64(int64(1000 + n))}
				if c.purpose == "" { // Bun 行：这几列是 NULL
					if n%3 == 0 {
						r.cacheWrite, r.reasoning = nil, nil
					}
				} else {
					r.cacheWrite, r.reasoning = i64(int64(n%4)*7), i64(int64(n%3))
					r.seen, r.incomplete = i64(1), i64(0)
				}
				if n%2 == 0 && c.purpose == "" {
					r.cacheWrite, r.reasoning = i64(3), i64(2)
				}
				if n%7 == 0 { // 出错：照样带 token（真花了），耗时不算
					r.err = "boom"
				}
				if n%11 == 0 && c.purpose != "" { // 供应商没回用量
					r.seen, r.incomplete = i64(0), i64(0)
					r.input, r.cacheRead, r.output = 0, 0, 0
				}
				if n%13 == 0 && c.purpose != "" {
					r.incomplete = i64(1)
				}
				if n%17 == 0 {
					r.ms = nil // 没记耗时
				}
				_ = mi
				rows = append(rows, r)
			}
		}
	}
	return rows
}

// oracle 是测试自己的算法：直接对 fx 累加，和 SQL 加 Go 折叠的结果比
type oracle struct {
	runs, failed, noUsage, uncached, cacheRead, cacheWrite, output, reasoning, okRuns, okMs int64
}

func (o *oracle) add(r fx) {
	o.runs++
	if r.err != "" {
		o.failed++
	}
	seen := r.seen == nil || *r.seen != 0
	if !seen || (r.incomplete != nil && *r.incomplete == 1) {
		o.noUsage++
	}
	o.uncached += r.input
	o.cacheRead += r.cacheRead
	if r.cacheWrite != nil {
		o.cacheWrite += *r.cacheWrite
	}
	o.output += r.output
	if r.reasoning != nil {
		o.reasoning += *r.reasoning
	}
	if r.err == "" && r.ms != nil {
		o.okRuns++
		o.okMs += *r.ms
	}
}

func (o oracle) check(t *testing.T, what string, got ctlproto.UsageStats) {
	t.Helper()
	in := o.uncached + o.cacheRead + o.cacheWrite
	switch {
	case got.Runs != o.runs, got.Failed != o.failed, got.NoUsage != o.noUsage,
		got.Uncached != o.uncached, got.CacheRead != o.cacheRead, got.CacheWrite != o.cacheWrite,
		got.Input != in, got.Output != o.output, got.Reasoning != o.reasoning,
		got.OKRuns != o.okRuns, got.OKMs != o.okMs:
		t.Errorf("%s：\n 得到 %+v\n 预期 %+v（总输入 %d）", what, got, o, in)
	}
	if o.okRuns > 0 && got.AvgMs != o.okMs/o.okRuns {
		t.Errorf("%s：平均耗时 %d，预期 %d", what, got.AvgMs, o.okMs/o.okRuns)
	}
	if in > 0 {
		if want := float64(o.cacheRead) / float64(in); math.Abs(got.Hit-want) > 1e-9 {
			t.Errorf("%s：命中率 %v，预期 %v", what, got.Hit, want)
		}
	} else if got.Hit != 0 {
		t.Errorf("%s：总输入为 0 时命中率应是 0，得到 %v", what, got.Hit)
	}
}

func localDate(at int64) string { return time.UnixMilli(at).In(zone).Format("2006-01-02") }

func mondayOf(at int64) string {
	d := time.UnixMilli(at).In(zone)
	return time.Date(d.Year(), d.Month(), d.Day()-((int(d.Weekday())+6)%7), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// 同一份夹具、同一套预期，三种来源各跑一遍：直接查原始行（没做过 SyncRollup）、汇总表（灌完再重建）、
// 汇总表（先建好汇总表，灌的时候由触发器累加）。三条路的结果必须都等于测试自己算的预期
func TestUsageTotals(t *testing.T) {
	for _, mode := range []string{"raw", "rebuild", "trigger"} {
		t.Run(mode, func(t *testing.T) { usageTotals(t, mode) })
	}
}

func usageTotals(t *testing.T, mode string) {
	db, file := tempTrace(t)
	rows := buildFixture()
	if len(rows) < 250 {
		t.Fatalf("夹具只有 %d 行，要几百行", len(rows))
	}
	wantSource := SourceRaw
	if mode == "trigger" {
		syncRollup(t, db)
	}
	insertRows(t, db, rows)
	if mode == "rebuild" {
		syncRollup(t, db)
	}
	if mode != "raw" {
		wantSource = SourceRollup
	}
	svc := openService(t, file)

	for _, tc := range []struct {
		name string
		args ctlproto.StatsArgs
		in   func(at int64) bool
	}{
		{"默认 7 天", ctlproto.StatsArgs{}, func(at int64) bool { return at >= timestamps[2] && at < timestamps[7] }},
		{"今天", ctlproto.StatsArgs{Days: 1}, func(at int64) bool { return at >= timestamps[5] && at < timestamps[7] }},
		{"2 天", ctlproto.StatsArgs{Days: 2}, func(at int64) bool { return at >= timestamps[4] && at < timestamps[7] }},
		{"全部", ctlproto.StatsArgs{All: true}, func(int64) bool { return true }},
		{"2026 年", ctlproto.StatsArgs{Year: 2026}, func(int64) bool { return true }},
		{"2025 年", ctlproto.StatsArgs{Year: 2025}, func(int64) bool { return false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rng, err := svc.RangeOf(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := svc.Usage(context.Background(), rng)
			if err != nil {
				t.Fatal(err)
			}
			var total oracle
			byTask := map[string]*oracle{}
			byModel := map[string]*oracle{}
			byDay := map[string]*oracle{}
			byWeek := map[string]*oracle{}
			byUnknown := map[string]*oracle{}
			get := func(m map[string]*oracle, k string) *oracle {
				if m[k] == nil {
					m[k] = &oracle{}
				}
				return m[k]
			}
			for _, r := range rows {
				if !tc.in(r.at) {
					continue
				}
				want := ""
				for _, c := range combos {
					if c.purpose == r.purpose && c.stage == r.stage {
						want = c.want
					}
				}
				total.add(r)
				get(byTask, want).add(r)
				get(byModel, r.model).add(r)
				get(byDay, localDate(r.at)).add(r)
				get(byWeek, mondayOf(r.at)).add(r)
				if want == ctlproto.TaskUnknown {
					raw := r.stage
					if r.purpose != "" {
						raw = r.purpose + "/" + r.stage
					}
					get(byUnknown, raw).add(r)
				}
			}
			if rep.Source != wantSource {
				t.Errorf("来源是 %q，预期 %q", rep.Source, wantSource)
			}
			total.check(t, "总计", rep.Totals)

			// 固定类别：每一类都在表里，顺序固定；没有数据的是 0 行
			if len(rep.Tasks) < len(Tasks) {
				t.Fatalf("类别表只有 %d 行", len(rep.Tasks))
			}
			for i, meta := range Tasks {
				row := rep.Tasks[i]
				if row.Key != meta.Key || row.Label != meta.Label || row.Wired != meta.Wired {
					t.Errorf("第 %d 行应是 %+v，得到 %s/%s/%v", i, meta, row.Key, row.Label, row.Wired)
				}
				o := byTask[meta.Key]
				if o == nil {
					o = &oracle{}
				}
				o.check(t, "类别 "+meta.Key, row.UsageStats)
				// 类别下按模型的合计要等于类别合计
				var sum int64
				for _, m := range row.Models {
					sum += m.Runs
				}
				if sum != row.Runs {
					t.Errorf("类别 %s 下各模型次数加起来 %d，类别是 %d", meta.Key, sum, row.Runs)
				}
			}
			// 未知值：每种原文单列一行，带原文，数量和预期一致
			var unknownRows int
			for _, row := range rep.Tasks[len(Tasks):] {
				if row.Key != ctlproto.TaskUnknown || row.Raw == "" {
					t.Errorf("类别表末尾只该有带原文的 unknown 行，得到 %+v", row)
					continue
				}
				unknownRows++
				o := byUnknown[row.Raw]
				if o == nil {
					t.Errorf("多出一行未知 %q", row.Raw)
					continue
				}
				o.check(t, "未知 "+row.Raw, row.UsageStats)
			}
			if unknownRows != len(byUnknown) {
				t.Errorf("未知行 %d 行，预期 %d 种", unknownRows, len(byUnknown))
			}

			// 按模型
			if len(rep.Models) != len(byModel) {
				t.Errorf("模型 %d 个，预期 %d", len(rep.Models), len(byModel))
			}
			for _, m := range rep.Models {
				if o := byModel[m.Model]; o == nil {
					t.Errorf("多出模型 %s", m.Model)
				} else {
					o.check(t, "模型 "+m.Model, m.UsageStats)
				}
			}
			// 按天、按周：本地时区，升序，没有记录的日子不列
			checkBuckets(t, "天", rep.Days, byDay)
			checkBuckets(t, "周", rep.Weeks, byWeek)
		})
	}
}

func checkBuckets(t *testing.T, what string, got []ctlproto.UsageBucket, want map[string]*oracle) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("按%s %d 行，预期 %d 行", what, len(got), len(want))
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Date < got[j].Date }) {
		t.Errorf("按%s 没有按日期升序", what)
	}
	for _, b := range got {
		o := want[b.Date]
		if o == nil {
			t.Errorf("按%s 多出 %s", what, b.Date)
			continue
		}
		o.check(t, what+" "+b.Date, b.UsageStats)
	}
}

// 零点边界：今天的第一毫秒算今天，昨天的最后一毫秒算昨天；周日算上一周，周一零点是新的一周
func TestUsageMidnightAndWeekBoundaries(t *testing.T) {
	db, file := tempTrace(t)
	var rows []fx
	for _, at := range timestamps[:7] {
		rows = append(rows, fx{purpose: "foreground", stage: "reply", model: "m", at: at, input: 1, output: 1, ms: i64(10), seen: i64(1), incomplete: i64(0)})
	}
	insertRows(t, db, rows)
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{All: true})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	days := map[string]int64{}
	for _, d := range rep.Days {
		days[d.Date] = d.Runs
	}
	wantDays := map[string]int64{"2026-10-31": 1, "2026-11-01": 1, "2026-11-02": 1, "2026-11-06": 1, "2026-11-07": 1, "2026-11-08": 2}
	for k, v := range wantDays {
		if days[k] != v {
			t.Errorf("%s 应有 %d 次，得到 %d（全部：%v）", k, v, days[k], days)
		}
	}
	weeks := map[string]int64{}
	for _, w := range rep.Weeks {
		weeks[w.Date] = w.Runs
	}
	// 10-26 周：10-31（周六）、11-01（周日）；11-02 周：其余 5 条
	if weeks["2026-10-26"] != 2 || weeks["2026-11-02"] != 5 || len(weeks) != 2 {
		t.Errorf("周分组不对：%v", weeks)
	}
}

// 每个合法 purpose 和每个历史 stage 都有归属，未知值单列（验收②）；用内核的常量和 turnbudget 的合法表遍历，
// 新加一个合法值而没人改 TaskOf，这里就红
func TestTaskOfCoversEveryValidPurposeAndHistoricalStage(t *testing.T) {
	kinds := []string{kernel.KindForeground, kernel.KindChild, kernel.KindAdvisor, kernel.KindVerify, kernel.KindCompact, kernel.KindMemory, kernel.KindGuard, kernel.KindGoalJudge}
	for _, k := range kinds {
		if !turnbudget.ValidInvocation(kernel.Invocation{RootID: "r", RunID: "r", Kind: k}) {
			t.Errorf("%q 不是合法的 Invocation 类型：测试表和 ValidInvocation 对不上", k)
		}
		if got := TaskOf(k, k); got == ctlproto.TaskUnknown {
			t.Errorf("合法 purpose %q 没有归属，落进了 unknown", k)
		}
		if got := TaskOf(k, "anything"); got == ctlproto.TaskUnknown {
			t.Errorf("purpose %q 配别的 stage 也要按 purpose 归类", k)
		}
	}
	// ValidInvocation 接受的值不能比上面的表多：多了一个就说明有新的合法 purpose 没进来
	for _, k := range []string{"x", "", "Foreground", "goal"} {
		if turnbudget.ValidInvocation(kernel.Invocation{RootID: "r", RunID: "r", Kind: k}) {
			t.Errorf("%q 竟然是合法 purpose：TaskOf 的表要跟着改", k)
		}
	}
	for _, s := range []string{"vision", "reply", "reply-retry", "summarize", "learn-expression", "review-expression", "guard"} {
		if got := TaskOf("", s); got == ctlproto.TaskUnknown {
			t.Errorf("历史 stage %q 没有归属", s)
		}
	}
	// 未知值：不归任何类，purpose 不认识时不回头用 stage 猜
	for _, c := range [][2]string{{"", "weird"}, {"", ""}, {"mystery", "reply"}, {"mystery", ""}, {"Foreground", "reply"}, {" foreground", "reply"}} {
		if got := TaskOf(c[0], c[1]); got != ctlproto.TaskUnknown {
			t.Errorf("TaskOf(%q, %q) = %q，应是 unknown", c[0], c[1], got)
		}
	}
	// 归属的类别都在类别表里
	keys := map[string]bool{}
	for _, m := range Tasks {
		keys[m.Key] = true
	}
	for _, m := range []map[string]string{purposeTask, stageTask} {
		for k, v := range m {
			if !keys[v] {
				t.Errorf("%q 归到 %q，类别表里没有它", k, v)
			}
		}
	}
}

// 验收⑤：旧代码没有 TaskOf 时 vision 行会落进"未知"。这条测试钉住：vision 历史行必须在"识图"类里、
// 不在未知里，而且识图标着 Go 侧未接入（有历史数据但 Wired 为假）
func TestVisionHistoryIsNotUnknown(t *testing.T) {
	db, file := tempTrace(t)
	var rows []fx
	for i := 0; i < 5; i++ {
		rows = append(rows, fx{stage: "vision", model: "m/v", at: ms(2026, 11, 3, 10, i, 0, 0), input: 100, output: 20, ms: i64(500)})
	}
	insertRows(t, db, rows)
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range rep.Tasks {
		switch task.Key {
		case ctlproto.TaskVision:
			if task.Runs != 5 || task.Wired || task.Input != 500 {
				t.Errorf("识图应有 5 次历史、Wired 为假，得到 %+v", task)
			}
		case ctlproto.TaskUnknown:
			t.Errorf("vision 行落进了未知：%+v", task)
		}
	}
}

// 预留类别没有记录就是 0 次 + Wired 为假，调用方据此写"未接入"，不写 0 次失败
func TestReservedTasksAreMarkedUnwired(t *testing.T) {
	_, file := tempTrace(t)
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Runs != 0 || len(rep.Days) != 0 || len(rep.Weeks) != 0 || len(rep.Models) != 0 {
		t.Errorf("空库不该有数据：%+v", rep)
	}
	wired := map[string]bool{}
	for _, task := range rep.Tasks {
		wired[task.Key] = task.Wired
		if task.Runs != 0 {
			t.Errorf("空库的 %s 有 %d 次", task.Key, task.Runs)
		}
	}
	for _, k := range []string{ctlproto.TaskVision, ctlproto.TaskChild, ctlproto.TaskGoal, ctlproto.TaskLoop} {
		if w, ok := wired[k]; !ok || w {
			t.Errorf("%s 应在表里且 Wired 为假", k)
		}
	}
	for _, k := range []string{ctlproto.TaskForeground, ctlproto.TaskMemory, ctlproto.TaskGuard, ctlproto.TaskCompact, ctlproto.TaskAdvisor} {
		if !wired[k] {
			t.Errorf("%s 应是已接入", k)
		}
	}
}

// NULL 和失败的口径：usage_seen 为 NULL 当拿到了；usage_seen 为 0 或 incomplete 为 1 计入"没拿到用量"；
// 出错的请求 token 照算、耗时不算；没记耗时的成功请求不进平均
func TestUsageNullAndFailureSemantics(t *testing.T) {
	db, file := tempTrace(t)
	at := ms(2026, 11, 8, 9, 0, 0, 0)
	insertRows(t, db, []fx{
		{stage: "reply", model: "m", at: at, input: 10, cacheRead: 90, output: 5, ms: i64(100)},                                                                                   // Bun 行：全 NULL
		{purpose: "foreground", stage: "reply", model: "m", at: at, input: 20, cacheWrite: i64(30), output: 6, reasoning: i64(4), ms: i64(300), seen: i64(1), incomplete: i64(0)}, // 正常 Go 行
		{purpose: "foreground", stage: "reply", model: "m", at: at, err: "boom", input: 7, output: 1, ms: i64(9999), seen: i64(1), incomplete: i64(0)},                            // 出错但有用量
		{purpose: "foreground", stage: "reply", model: "m", at: at, err: "boom", ms: i64(50), seen: i64(0), incomplete: i64(0)},                                                   // 出错且没拿到用量
		{purpose: "foreground", stage: "reply", model: "m", at: at, input: 1, output: 1, seen: i64(1), incomplete: i64(1)},                                                        // 不完整，没记耗时
	})
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{Days: 1})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Tasks[0]
	want := ctlproto.UsageStats{
		Runs: 5, Failed: 2, NoUsage: 2,
		UsageTokens: ctlproto.UsageTokens{Input: (10 + 20 + 7 + 1) + 90 + 30, Uncached: 10 + 20 + 7 + 1, CacheRead: 90, CacheWrite: 30, Output: 5 + 6 + 1 + 1, Reasoning: 4},
		OKRuns:      2, OKMs: 400, AvgMs: 200, Hit: 90.0 / 158.0,
	}
	if got.UsageStats != want {
		t.Errorf("得到 %+v\n预期 %+v", got.UsageStats, want)
	}
}

func TestRangeOf(t *testing.T) {
	_, file := tempTrace(t)
	svc := openService(t, file) // 现在是 2026-11-08 12:00，周日
	for _, tc := range []struct {
		args     ctlproto.StatsArgs
		from, to int64
		kind     string
	}{
		{ctlproto.StatsArgs{}, ms(2026, 11, 2, 0, 0, 0, 0), ms(2026, 11, 9, 0, 0, 0, 0), "days"},
		{ctlproto.StatsArgs{Days: 1}, ms(2026, 11, 8, 0, 0, 0, 0), ms(2026, 11, 9, 0, 0, 0, 0), "days"},
		{ctlproto.StatsArgs{Days: 30}, ms(2026, 10, 10, 0, 0, 0, 0), ms(2026, 11, 9, 0, 0, 0, 0), "days"},
		{ctlproto.StatsArgs{Year: 2026}, ms(2026, 1, 1, 0, 0, 0, 0), ms(2027, 1, 1, 0, 0, 0, 0), "year"},
		{ctlproto.StatsArgs{All: true}, 0, math.MaxInt64, "all"},
	} {
		r, err := svc.RangeOf(tc.args)
		if err != nil || r.FromMS != tc.from || r.ToMS != tc.to || r.Kind != tc.kind || r.Label == "" || r.Zone == "" {
			t.Errorf("%+v → %+v, %v；预期 %d..%d %s", tc.args, r, err, tc.from, tc.to, tc.kind)
		}
	}
	for _, bad := range []ctlproto.StatsArgs{
		{Days: 1, All: true}, {Days: 1, Year: 2026}, {Year: 2026, All: true}, {Days: -1}, {Days: ctlproto.StatsMaxDays + 1},
		{Year: 1999}, {Year: 2201}, {By: "hour"}, {By: "TASK"},
	} {
		if _, err := svc.RangeOf(bad); err == nil {
			t.Errorf("%+v 应被拒绝", bad)
		}
	}
	if _, err := svc.Usage(context.Background(), ctlproto.UsageRange{FromMS: 5, ToMS: 5}); err == nil {
		t.Error("空范围应被拒绝")
	}
	if _, err := svc.Usage(context.Background(), ctlproto.UsageRange{FromMS: -1, ToMS: 5}); err == nil {
		t.Error("负的起点应被拒绝")
	}
}

func TestShape(t *testing.T) {
	db, file := tempTrace(t)
	insertRows(t, db, buildFixture())
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{All: true})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	task := rep.Shape("", false)
	if task.By != ctlproto.ByTask || len(task.Tasks) == 0 || task.Models != nil || task.Days != nil || task.Weeks != nil {
		t.Errorf("默认应只有类别：%+v", task.By)
	}
	for _, tk := range task.Tasks {
		if tk.Models != nil {
			t.Fatalf("不加 --full 类别下不该有模型：%s", tk.Key)
		}
	}
	if rep.Tasks[0].Models == nil {
		t.Error("Shape 不该改动原报告（它共用切片）")
	}
	full := rep.Shape(ctlproto.ByTask, true)
	if full.Tasks[0].Models == nil {
		t.Error("--full 应带模型")
	}
	if m := rep.Shape(ctlproto.ByModel, false); m.Models == nil || m.Tasks != nil || m.Days != nil {
		t.Error("--by model 只给 Models")
	}
	if d := rep.Shape(ctlproto.ByDay, false); d.Days == nil || d.Tasks != nil || d.Weeks != nil {
		t.Error("--by day 只给 Days")
	}
	if w := rep.Shape(ctlproto.ByWeek, false); w.Weeks == nil || w.Tasks != nil || w.Days != nil {
		t.Error("--by week 只给 Weeks")
	}
	for _, s := range []*ctlproto.UsageReport{&task, &full} {
		if s.Totals.Runs != rep.Totals.Runs {
			t.Error("Totals 总是在")
		}
	}
}

// 统计连接是只读的：INSERT 必须失败（规格第二节第 5 条）
func TestServiceConnectionIsReadOnly(t *testing.T) {
	_, file := tempTrace(t)
	svc := openService(t, file)
	_, err := svc.db.Exec(insertSQL, "x", "reply", "m", 1, 1, 1, 1, 1, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("只读连接竟然写进去了")
	}
}

// 单次查询超时：返回 ErrTimeout，不是别的错误
func TestUsageTimeout(t *testing.T) {
	db, file := tempTrace(t)
	insertRows(t, db, buildFixture())
	svc, err := OpenFile(file, Options{Loc: zone})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{All: true})
	// 期限已经过了：结果确定。给 1 纳秒的时限不行，定时器还没触发，小数据的查询可能已经查完
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := svc.Usage(expired, rng); !errors.Is(err, ErrTimeout) {
		t.Errorf("应是 ErrTimeout，得到 %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc2 := openService(t, file)
	if _, err := svc2.Usage(ctx, rng); err == nil || errors.Is(err, ErrTimeout) {
		t.Errorf("调用方取消不该报成超时：%v", err)
	}
}

// 迁移 0012 的两个索引存在，而且按时间范围的统计和"最近失败"查询真的走它们
func TestIndexesExistAndAreUsed(t *testing.T) {
	db, _ := tempTrace(t)
	insertRows(t, db, buildFixture())
	plan := func(q string, args ...any) string {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		return strings.Join(out, "; ")
	}
	if p := plan(usageSQL, 0, dayMS, 1, 2); !strings.Contains(p, "ix_runs_started") {
		t.Errorf("用量查询没走 ix_runs_started：%s", p)
	}
	if p := plan(`SELECT id, started_at FROM model_runs WHERE error IS NOT NULL AND started_at >= ? ORDER BY started_at DESC LIMIT 50`, 0); !strings.Contains(p, "ix_runs_failed") {
		t.Errorf("最近失败查询没走 ix_runs_failed：%s", p)
	}
}
