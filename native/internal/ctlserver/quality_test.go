package ctlserver

import (
	"context"
	"testing"

	"sumika-native/internal/ctlproto"
)

type qualityFake struct {
	fakeBackend
	got []ctlproto.StatsArgs
	rep ctlproto.QualityReport
	err error
}

func (f *qualityFake) Quality(_ context.Context, a ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	f.rec("stats-quality")
	f.got = append(f.got, a)
	return f.rep, f.err
}

// 后端收到的是校验过的参数，回复原样交回；只读命令不写日志
func TestStatsQualityPassesArgs(t *testing.T) {
	f := &qualityFake{rep: ctlproto.QualityReport{Totals: ctlproto.QualityStats{Runs: 4}}}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"quality"}, Stats: &ctlproto.StatsArgs{Days: 3, Task: "send", Failures: 5}})
	if !r.OK || dataOf[ctlproto.QualityReport](t, r).Totals.Runs != 4 {
		t.Fatalf("%+v", r)
	}
	if len(f.got) != 1 || f.got[0].Days != 3 || f.got[0].Task != "send" || f.got[0].Failures != 5 {
		t.Errorf("后端收到的参数不对：%+v", f.got)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "stats", Args: []string{"quality"}}); !r.OK {
		t.Errorf("不带参数也能查：%+v", r)
	}
	if len(logs) != 0 {
		t.Errorf("只读命令不该写日志：%v", logs)
	}
}

// 演示模式回 demo、后端没接回 failed、后端的带码错误原样传回
func TestStatsQualityModes(t *testing.T) {
	req := ctlproto.Request{Cmd: "stats", Args: []string{"quality"}}
	f := &qualityFake{}
	name, _ := start(t, f, Options{Demo: true})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, &f.fakeBackend)
	name, _ = start(t, &fakeBackend{}, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接质量：%+v", r)
	}
	boom := &qualityFake{err: ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")}
	name, _ = start(t, boom, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeTimeout {
		t.Errorf("后端超时：%+v", r)
	}
}

// 形状表：这一层把关，被拒的一格都不碰后端；usage、storage 不收质量的参数
func TestStatsQualityShapeTable(t *testing.T) {
	q := func(a ctlproto.StatsArgs) ctlproto.Request {
		return ctlproto.Request{Cmd: "stats", Args: []string{"quality"}, Stats: &a}
	}
	rows := []ctlproto.Request{
		{Cmd: "stats", Args: []string{"quality", "x"}},
		q(ctlproto.StatsArgs{Year: 2026}),
		q(ctlproto.StatsArgs{By: "task"}),
		q(ctlproto.StatsArgs{Full: true}),
		q(ctlproto.StatsArgs{Days: 1, All: true}),
		q(ctlproto.StatsArgs{Days: -1}),
		q(ctlproto.StatsArgs{Task: "nope"}),
		q(ctlproto.StatsArgs{Task: " "}),
		q(ctlproto.StatsArgs{Failures: -1}),
		q(ctlproto.StatsArgs{Failures: ctlproto.StatsMaxFailures + 1}),
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Task: "send"}},
		{Cmd: "stats", Args: []string{"usage"}, Stats: &ctlproto.StatsArgs{Failures: 5}},
		{Cmd: "stats", Args: []string{"storage"}, Stats: &ctlproto.StatsArgs{Task: "send"}},
		{Cmd: "status", Stats: &ctlproto.StatsArgs{Failures: 5}},
	}
	f := &qualityFake{}
	name, _ := start(t, f, Options{})
	for i, req := range rows {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 格 %+v：应被拒（invalid），得到 %+v", i, req, r)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 每个认识的 --task 键都放行；认不出的被拒
func TestValidQualityTask(t *testing.T) {
	for _, k := range []string{"foreground", "memory", "guard", "vision", "compact", "child", "advisor", "goal", "loop", "unknown", "send", "link", "media", "backfill", "memory_pass", "storage_clean"} {
		if !ctlproto.ValidQualityTask(k) {
			t.Errorf("%s 应放行", k)
		}
	}
	for _, k := range []string{"", "Send", "reply", "model"} {
		if ctlproto.ValidQualityTask(k) {
			t.Errorf("%q 不该放行", k)
		}
	}
}
