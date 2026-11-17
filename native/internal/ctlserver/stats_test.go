package ctlserver

import (
	"context"
	"testing"

	"sumika-native/internal/ctlproto"
)

type statsFake struct {
	fakeBackend
	got []ctlproto.StatsArgs
	rep ctlproto.UsageReport
	err error
}

func (f *statsFake) Usage(_ context.Context, a ctlproto.StatsArgs) (ctlproto.UsageReport, error) {
	f.rec("stats-usage")
	f.got = append(f.got, a)
	return f.rep, f.err
}

func sampleReport() ctlproto.UsageReport {
	return ctlproto.UsageReport{
		Totals: ctlproto.UsageStats{Runs: 3},
		Tasks:  []ctlproto.UsageTask{{Key: "foreground", Models: []ctlproto.UsageModel{{Model: "m"}}}},
		Models: []ctlproto.UsageModel{{Model: "m"}},
		Days:   []ctlproto.UsageBucket{{Date: "2026-11-08"}},
		Weeks:  []ctlproto.UsageBucket{{Date: "2026-11-02"}},
	}
}

// 后端收到的是校验过的参数；回复按 --by、--full 裁过；stats 是只读命令，不写"改状态"的日志
func TestStatsUsageShapesReply(t *testing.T) {
	f := &statsFake{rep: sampleReport()}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Days: 3, By: "day"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.UsageReport](t, r)
	if v.By != "day" || len(v.Days) != 1 || v.Tasks != nil || v.Models != nil || v.Weeks != nil || v.Totals.Runs != 3 {
		t.Errorf("--by day 应只留 Days：%+v", v)
	}
	if len(f.got) != 1 || f.got[0].Days != 3 {
		t.Errorf("后端收到的参数不对：%+v", f.got)
	}
	// 不带 stats 字段：默认按类别、不带模型
	r = ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"usage"}})
	v = dataOf[ctlproto.UsageReport](t, r)
	if v.By != "task" || len(v.Tasks) != 1 || v.Tasks[0].Models != nil || v.Days != nil {
		t.Errorf("默认应按类别、不带模型：%+v", v)
	}
	if len(logs) != 0 {
		t.Errorf("只读命令不该写日志：%v", logs)
	}
}

// 演示模式回 demo、后端没接统计回 failed、后端的带码错误原样传回；都不是 ok
func TestStatsUsageModes(t *testing.T) {
	req := ctlproto.Request{Cmd: "stats", Args: []string{"usage"}}

	f := &statsFake{rep: sampleReport()}
	name, _ := start(t, f, Options{Demo: true})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, &f.fakeBackend)

	plain := &fakeBackend{}
	name, _ = start(t, plain, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接统计：%+v", r)
	}

	boom := &statsFake{err: ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")}
	name, _ = start(t, boom, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeTimeout || ctlproto.ExitCode(r) != ctlproto.ExitFailed {
		t.Errorf("后端超时：%+v", r)
	}
}

// 形状表：这一层把关，被拒的一格都不碰后端
func TestStatsShapeTable(t *testing.T) {
	rows := []ctlproto.Request{
		{Cmd: "stats"},
		{Cmd: "stats", Args: []string{"resources", "x"}},
		{Cmd: "stats", Args: []string{"resources"}, Stats: &ctlproto.StatsArgs{Days: 1}},
		{Cmd: "stats", Args: []string{"resources"}, Stats: &ctlproto.StatsArgs{Full: true}},
		{Cmd: "stats", Args: []string{"resources"}, N: 5},
		{Cmd: "stats", Args: []string{"RESOURCES"}},
		{Cmd: "stats", Args: []string{"USAGE"}},
		{Cmd: "stats", Args: []string{"usage", "x"}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Days: 1, All: true}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Days: -1}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Days: ctlproto.StatsMaxDays + 1}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Year: 1}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{By: "hour"}},
		{Cmd: "stats", Args: []string{"usage"}, Wait: "5s"},
		{Cmd: "stats", Args: []string{"usage"}, N: 5},
		{Cmd: "stats", Args: []string{"usage"}, Text: "你好"},
		{Cmd: "status", Stats: &ctlproto.StatsArgs{Days: 3}},
		{Cmd: "chat", Text: "你好", Stats: &ctlproto.StatsArgs{All: true}},
	}
	f := &statsFake{}
	name, _ := start(t, f, Options{})
	for i, req := range rows {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(r) != ctlproto.ExitDenied {
			t.Errorf("第 %d 格 %+v：应被拒（invalid），得到 %+v", i, req, r)
		}
	}
	// 请求里的 stats 字段里夹带不认识的字段：整个请求被拒
	if r := raw(t, name, []byte(`{"v":1,"cmd":"stats","args":["usage"],"stats":{"days":3,"sql":"select 1"}}`)); r.OK || r.Code != ctlproto.CodeInvalid {
		t.Errorf("夹带 sql 字段：%+v", r)
	}
	wantCalls(t, &f.fakeBackend)
}

type resourcesFake struct {
	fakeBackend
	rep ctlproto.ResourcesReport
	err error
}

func (f *resourcesFake) Resources(context.Context) (ctlproto.ResourcesReport, error) {
	f.rec("stats-resources")
	return f.rep, f.err
}

// stats resources：后端的报告原样出去（数值为空的字段仍是 null，不被写成 0）；演示回 demo；没接回 failed；后端的带码错误原样传回
func TestStatsResources(t *testing.T) {
	req := ctlproto.Request{Cmd: "stats", Args: []string{"resources"}}
	cpu := 1.5
	f := &resourcesFake{rep: ctlproto.ResourcesReport{Cores: 8, MemoryKind: ctlproto.MemPrivate,
		Processes: []ctlproto.ProcessRow{{PID: 1, Name: "编造.exe", Category: ctlproto.ProcMain, CPU: &cpu}, {PID: 2, Parent: 1, Depth: 1, Name: "没读到.exe", Category: ctlproto.ProcUnregistered}},
		Queues:    []ctlproto.QueueRow{{Key: "backfill", Label: "补录"}}}}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, req)
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.ResourcesReport](t, r)
	if len(v.Processes) != 2 || v.Processes[0].CPU == nil || *v.Processes[0].CPU != 1.5 || v.Processes[1].CPU != nil || v.Processes[1].Memory != nil {
		t.Errorf("空的数值该保持 null：%+v", v.Processes)
	}
	if v.Queues[0].Wired || v.Queues[0].Backlog != nil {
		t.Errorf("没接入的队列不该被写成 0：%+v", v.Queues[0])
	}
	if len(logs) != 0 {
		t.Errorf("只读命令不该写日志：%v", logs)
	}
	wantCalls(t, &f.fakeBackend, "stats-resources")

	d := &resourcesFake{}
	name, _ = start(t, d, Options{Demo: true})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, &d.fakeBackend)

	plain := &fakeBackend{}
	name, _ = start(t, plain, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接资源：%+v", r)
	}

	boom := &resourcesFake{err: ctlproto.Errorf(ctlproto.CodeTimeout, "采样超时")}
	name, _ = start(t, boom, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeTimeout {
		t.Errorf("后端超时：%+v", r)
	}
}

type rebuildFake struct {
	fakeBackend
	rep  ctlproto.RebuildReport
	err  error
	args ctlproto.StatsArgs
}

func (f *rebuildFake) Rebuild(_ context.Context, a ctlproto.StatsArgs) (ctlproto.RebuildReport, error) {
	f.rec("stats-rebuild")
	f.args = a
	return f.rep, f.err
}

// stats rebuild：只认 --days / --all；别的选项、位置参数一律在服务端被拒，不碰后端；后端的报告原样出去；演示回 demo；没接回 failed
func TestStatsRebuild(t *testing.T) {
	f := &rebuildFake{rep: ctlproto.RebuildReport{Places: []ctlproto.RebuildRow{{Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 3, Counted: 3, DroppedAvg: 4}}}}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Days: 3}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	if v := dataOf[ctlproto.RebuildReport](t, r); len(v.Places) != 1 || v.Places[0].Rebuilds != 3 || f.args.Days != 3 {
		t.Errorf("报告原样出去、范围传给后端：%+v %+v", v, f.args)
	}
	if len(logs) != 0 {
		t.Errorf("只读命令不该写日志：%v", logs)
	}
	wantCalls(t, &f.fakeBackend, "stats-rebuild")

	bad := []ctlproto.Request{
		{Cmd: "stats", Args: []string{"rebuild", "extra"}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Year: 2026}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{By: "day"}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Full: true}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Task: "foreground"}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Turn: "last"}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Days: 3, All: true}},
		{Cmd: "stats", Args: []string{"rebuild"}, Stats: &ctlproto.StatsArgs{Days: -1}},
	}
	g := &rebuildFake{}
	name, _ = start(t, g, Options{})
	for i, req := range bad {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 格 %+v：应被拒（invalid），得到 %+v", i, req, r)
		}
	}
	wantCalls(t, &g.fakeBackend)

	d := &rebuildFake{}
	name, _ = start(t, d, Options{Demo: true})
	if r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"rebuild"}}); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, &d.fakeBackend)

	plain := &fakeBackend{}
	name, _ = start(t, plain, Options{})
	if r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"rebuild"}}); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接重建留痕：%+v", r)
	}
	boom := &rebuildFake{err: ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")}
	name, _ = start(t, boom, Options{})
	if r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"rebuild"}}); r.OK || r.Code != ctlproto.CodeTimeout {
		t.Errorf("后端超时：%+v", r)
	}
}
