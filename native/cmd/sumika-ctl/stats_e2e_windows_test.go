package main

// stats usage 端到端（规格-运行页 第十二节第 1 步验收④）：命令行 → 带测试名的真命名管道 → internal/ctlserver 的校验 →
// 真的 runstats.Service 读临时 trace.db，再走回来变成退出码和输出。库只在 t.TempDir() 里，不碰真实 data/。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/resstat"
	"sumika-native/internal/runmetrics"
	"sumika-native/internal/runstats"
	"sumika-native/internal/store"
)

// statsBackend 在 stubBackend 上加统计：用真的 runstats.Service（固定时区和"现在"），可以让它回一个指定的错误
type statsBackend struct {
	stubBackend
	file  string
	zone  *time.Location
	now   time.Time
	smu   sync.Mutex
	asked []ctlproto.StatsArgs
	fail  error
}

func (b *statsBackend) Usage(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.UsageReport, error) {
	b.smu.Lock()
	b.asked = append(b.asked, a)
	fail := b.fail
	b.smu.Unlock()
	if fail != nil {
		return ctlproto.UsageReport{}, fail
	}
	svc, err := runstats.OpenFile(b.file, runstats.Options{Loc: b.zone, Now: func() time.Time { return b.now }})
	if err != nil {
		return ctlproto.UsageReport{}, err
	}
	defer svc.Close()
	rng, err := svc.RangeOf(a)
	if err != nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	return svc.Usage(ctx, rng)
}

// Resources 用真的采样器（本进程）：隔一小会儿采两次，和桌面程序里的命令行后端同一个做法
func (b *statsBackend) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	b.smu.Lock()
	fail := b.fail
	b.smu.Unlock()
	if fail != nil {
		return ctlproto.ResourcesReport{}, fail
	}
	return resstat.New(resstat.Options{Queues: &runmetrics.Registry{}}).SampleWarm(ctx, false)
}

func TestStatsUsageEndToEnd(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, zone)
	p := paths.Paths{Root: t.TempDir()}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := runstats.SyncRollup(context.Background(), st.Trace, zone, now); err != nil {
		t.Fatal(err)
	}
	// 七行：今天两行前台（一行出错）、昨天一行守卫、三天前一行识图（Bun 的历史行，purpose 为 NULL）、
	// 一行认不出的 stage、一行在 7 天之外
	insert := func(i int, purpose any, stage, model string, at time.Time, in, cache, out int, errText any) {
		t.Helper()
		if _, err := st.Trace.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, output_tokens, cache_read_tokens,
 sections_json, injected_json, error, purpose) VALUES (?, 'c', 'console:c', ?, ?, ?, 1500, ?, ?, ?, '[]', '[]', ?, ?)`,
			fmt.Sprintf("e%d", i), stage, model, at.UnixMilli(), in, out, cache, errText, purpose); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, "foreground", "reply", "m/a", now.Add(-time.Hour), 100, 900, 50, nil)
	insert(2, "foreground", "reply", "m/a", now.Add(-2*time.Hour), 10, 0, 5, "Request aborted")
	insert(3, "guard", "guard", "m/b", now.Add(-26*time.Hour), 20, 20, 10, nil)
	insert(4, nil, "vision", "m/v", now.Add(-72*time.Hour), 30, 0, 15, nil)
	insert(5, nil, "mystery-stage", "m/a", now.Add(-5*time.Hour), 1, 0, 1, nil)
	insert(6, "foreground", "reply", "m/a", now.Add(-10*24*time.Hour), 7, 0, 7, nil)

	name := testPipeName("stats")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &statsBackend{stubBackend: stubBackend{quit: make(chan struct{})}, file: p.BunTraceDB(), zone: zone, now: now}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}
	report := func(args ...string) ctlproto.UsageReport {
		t.Helper()
		code, out, errs := do(append([]string{"--json"}, args...)...)
		var r ctlproto.Reply
		var rep ctlproto.UsageReport
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.OK || json.Unmarshal(r.Data, &rep) != nil {
			t.Fatalf("%v：退出码 %d，输出 %q，错误 %q", args, code, out, errs)
		}
		return rep
	}

	// --json 可解析；默认最近 7 天：前台 2 行 + 守卫 1 + 识图 1 + 未知 1 = 5，第 6 行在范围外
	rep := report("stats", "usage")
	if rep.By != ctlproto.ByTask || rep.Totals.Runs != 5 || rep.Source != runstats.SourceRollup || rep.Range.Kind != "days" || len(rep.Tasks) < 10 {
		t.Fatalf("默认报告不对：%+v", rep)
	}
	byKey := map[string]ctlproto.UsageTask{}
	for _, task := range rep.Tasks {
		byKey[task.Key] = task
		if task.Models != nil {
			t.Errorf("不加 --full 类别 %s 下不该带模型", task.Key)
		}
	}
	fg := byKey[ctlproto.TaskForeground]
	if fg.Runs != 2 || fg.Failed != 1 || fg.Input != 100+900+10 || fg.CacheRead != 900 || fg.Output != 55 || fg.OKRuns != 1 || fg.AvgMs != 1500 {
		t.Errorf("前台合计不对：%+v", fg)
	}
	if v := byKey[ctlproto.TaskVision]; v.Runs != 1 || v.Wired {
		t.Errorf("识图历史行应在识图类里、标着未接入：%+v", v)
	}
	if byKey[ctlproto.TaskUnknown].Raw != "mystery-stage" || byKey[ctlproto.TaskUnknown].Runs != 1 {
		t.Errorf("认不出的 stage 应单列带原文：%+v", byKey[ctlproto.TaskUnknown])
	}
	if g := byKey[ctlproto.TaskGoal]; g.Runs != 0 || g.Wired {
		t.Errorf("/goal 应是未接入的预留行：%+v", g)
	}

	// 别的范围和分组
	if r := report("stats", "usage", "--days", "1"); r.Totals.Runs != 3 { // 今天 2 行 + 5 小时前的未知
		t.Errorf("--days 1：%d", r.Totals.Runs)
	}
	if r := report("stats", "usage", "--all"); r.Totals.Runs != 6 || r.Range.Kind != "all" {
		t.Errorf("--all：%+v", r.Totals)
	}
	if r := report("stats", "usage", "--year=2026"); r.Totals.Runs != 6 || r.Range.Kind != "year" {
		t.Errorf("--year：%+v", r.Totals)
	}
	if r := report("stats", "usage", "--year", "2025"); r.Totals.Runs != 0 {
		t.Errorf("--year 2025：%+v", r.Totals)
	}
	if r := report("stats", "usage", "--by", "model"); r.By != "model" || len(r.Models) != 3 || r.Tasks != nil || r.Days != nil {
		t.Errorf("--by model：%+v", r)
	}
	if r := report("stats", "usage", "--by=day"); len(r.Days) != 3 || r.Tasks != nil || r.Weeks != nil || r.Days[0].Date != "2026-11-05" {
		t.Errorf("--by day：%+v", r.Days)
	}
	if r := report("stats", "usage", "--by", "week", "--all"); len(r.Weeks) != 2 || r.Weeks[0].Date != "2026-10-26" || r.Weeks[1].Date != "2026-11-02" {
		t.Errorf("--by week：%+v", r.Weeks)
	}
	full := report("stats", "usage", "--full")
	var fgFull ctlproto.UsageTask
	for _, task := range full.Tasks {
		if task.Key == ctlproto.TaskForeground {
			fgFull = task
		}
	}
	if len(fgFull.Models) != 1 || fgFull.Models[0].Model != "m/a" {
		t.Errorf("--full 前台下应列出模型：%+v", fgFull)
	}

	// 给人看的输出：预留类别写"未接入"，历史行标出来，未知带引号的原文，不是 0
	code, out, errs := do("stats", "usage")
	for _, want := range []string{"用量（最近 7 天", "来源：汇总表", "前台回合  2 次", "输入 1,010（命中 89%）", "识图  1 次", "Go 侧未接入，只有历史记录", "子代理  未接入", "/loop  未接入", `未知："mystery-stage"  1 次`} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("文字输出缺 %q（退出码 %d，错误 %q）：\n%s", want, code, errs, out)
		}
	}

	// 形状表：全部被拒、退出码 3、后端一次都没调到
	b.smu.Lock()
	before := len(b.asked)
	b.smu.Unlock()
	for _, bad := range [][]string{
		{"stats"},
		{"stats", "resources", "extra"},
		{"stats", "resources", "--days", "3"},
		{"stats", "resources", "--full"},
		{"stats", "nonsense"},
		{"stats", "usage", "extra"},
		{"stats", "usage", "--days", "7", "--all"},
		{"stats", "usage", "--days", "7", "--year", "2026"},
		{"stats", "usage", "--year", "2026", "--all"},
		{"stats", "usage", "--days", "0"},
		{"stats", "usage", "--days", "-3"},
		{"stats", "usage", "--days", "99999"},
		{"stats", "usage", "--year", "1999"},
		{"stats", "usage", "--by", "hour"},
		{"stats", "usage", "--by", "TASK"},
		{"stats", "usage", "--by="},
		{"stats", "usage", "--days", "abc"},
		{"stats", "usage", "--days"},
		{"status", "--days", "3"},
		{"logs", "--all"},
		{"quit", "--full"},
	} {
		if code, _, errs := do(bad...); code != 3 {
			t.Errorf("%v 应被拒（退出码 3），得到 %d，错误 %q", bad, code, errs)
		}
	}
	b.smu.Lock()
	after := len(b.asked)
	b.smu.Unlock()
	if after != before {
		t.Errorf("被拒的请求不该走到后端：%d → %d", before, after)
	}

	// stats resources：真采样器采本进程，--json 可解析，文字输出有三块；CPU 隔了一小会儿采两次，所以不是空的
	{
		code, out, errs := do("--json", "stats", "resources")
		var r ctlproto.Reply
		var rep ctlproto.ResourcesReport
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.OK || json.Unmarshal(r.Data, &rep) != nil {
			t.Fatalf("stats resources --json：退出码 %d，输出 %q，错误 %q", code, out, errs)
		}
		if len(rep.Processes) == 0 || rep.Processes[0].Category != ctlproto.ProcMain || rep.Processes[0].CPU == nil || rep.IntervalMS <= 0 {
			t.Errorf("主程序那行该有 CPU（命令行自己采了两次）：%+v interval=%d", rep.Processes, rep.IntervalMS)
		}
		if len(rep.Queues) == 0 || rep.Queues[0].Wired {
			t.Errorf("没注册的队列该是未接入：%+v", rep.Queues)
		}
		code, text, errs := do("stats", "resources")
		for _, want := range []string{"资源（", "进程：", "主程序", "主程序内部：", "协程", "后台队列：", "模型请求槽位  未接入"} {
			if code != 0 || !strings.Contains(text, want) {
				t.Errorf("文字输出缺 %q（退出码 %d，错误 %q）：\n%s", want, code, errs, text)
			}
		}
	}

	// 后端的错误带着码传回来：超时是退出码 1、code timeout，不是 3
	b.smu.Lock()
	b.fail = ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")
	b.smu.Unlock()
	if code, out, _ := do("--json", "stats", "usage"); code != 1 || !strings.Contains(out, `"code":"timeout"`) {
		t.Errorf("后端超时：%d %q", code, out)
	}
}
