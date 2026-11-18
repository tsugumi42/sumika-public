package selfcheck_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// S12：每周抽验超过 10 天没有通过、或最近一次失败 → bad（带下一步）；没开、没读过、正在做、没有备份可抽、刚开机 → 不报警
func TestBackupDrillStale(t *testing.T) {
	c := findCheck(t, "backup.drill_stale", selfcheck.DefaultThresholds())
	started := t0.Add(-100 * 24 * time.Hour) // 进程早就开着
	day := 24 * time.Hour
	base := selfcheck.DrillState{Enabled: true, Checked: true, HaveBackup: true, Started: started}
	pass := func(ago time.Duration) selfcheck.DrillState {
		s := base
		s.Have, s.LastOK, s.LastAt, s.HavePass, s.LastPassAt, s.LastBackup = true, true, t0.Add(-ago), true, t0.Add(-ago), "每天-x"
		return s
	}
	failedAfterPass := pass(3 * day)
	failedAfterPass.LastOK, failedAfterPass.LastAt, failedAfterPass.LastProblem = false, t0.Add(-time.Hour), `config/sumika.yml 和清单对不上：E:\秘密\备份 disk is full`
	failedNeverPassed := base
	failedNeverPassed.Have, failedNeverPassed.LastAt, failedNeverPassed.LastProblem = true, t0.Add(-time.Hour), "x"
	running := base
	running.Running, running.RunningSince = true, t0.Add(-time.Minute)
	runningLong := base
	runningLong.Running, runningLong.RunningSince, runningLong.RunMax = true, t0.Add(-8*time.Hour), 6*time.Hour
	noBackup := base
	noBackup.HaveBackup = false
	justStarted := base
	justStarted.Started = t0.Add(-5 * time.Minute)
	notChecked := base
	notChecked.Checked = false
	disabled := base
	disabled.Enabled = false
	oldPassWithNewProcess := pass(11 * day)
	oldPassWithNewProcess.Started = t0.Add(-5 * time.Minute) // 关机很久再开机：记录是旧的，但进程刚起来
	cases := []struct {
		name  string
		st    selfcheck.DrillState
		state selfcheck.State
	}{
		{"没开每周抽验（每日备份关着）", disabled, selfcheck.Ok},
		{"调度还没读过记录", notChecked, selfcheck.Unknown},
		{"正在抽验", running, selfcheck.Unknown},
		{"抽验做了 8 小时（上限 6 小时）还没完", runningLong, selfcheck.Bad},
		{"3 天前通过", pass(3 * day), selfcheck.Ok},
		{"10 天前通过（阈值边上）", pass(10 * day), selfcheck.Ok},
		{"11 天前通过，进程开了很久", pass(11 * day), selfcheck.Bad},
		{"11 天前通过，进程刚起来 5 分钟（关机很久再开机，前几分钟不报警）", oldPassWithNewProcess, selfcheck.Unknown},
		{"最近一次失败，之前 3 天前通过过", failedAfterPass, selfcheck.Bad},
		{"最近一次失败，从没通过过", failedNeverPassed, selfcheck.Bad},
		{"从没抽验过，进程开了很久，有备份可抽", base, selfcheck.Bad},
		{"从没抽验过，没有备份可抽", noBackup, selfcheck.Unknown},
		{"从没抽验过，进程刚起来 5 分钟", justStarted, selfcheck.Unknown},
	}
	for _, tc := range cases {
		st := tc.st
		env := envAt(t0, selfcheck.NewCounters())
		env.Drill = func() selfcheck.DrillState { return st }
		r := c.Eval(context.Background(), env)
		if r.State != tc.state {
			t.Errorf("%s：想要 %s，实际 %+v", tc.name, tc.state, r)
		}
		if r.State == selfcheck.Bad && !strings.Contains(r.Next, "sumika-ctl backup verify") {
			t.Errorf("%s：异常该在 Next 里带下一步：%+v", tc.name, r)
		}
	}
}

func TestBackupDrillStaleNotWired(t *testing.T) {
	c := findCheck(t, "backup.drill_stale", selfcheck.DefaultThresholds())
	if r := c.Eval(context.Background(), envAt(t0, selfcheck.NewCounters())); r.State != selfcheck.Unknown {
		t.Fatalf("没接抽验状态不许记成 ok：%+v", r)
	}
}

// 失败原因只记类别，不带路径、不带文件名原文
func TestBackupDrillStaleExampleHasNoPath(t *testing.T) {
	c := findCheck(t, "backup.drill_stale", selfcheck.DefaultThresholds())
	env := envAt(t0, selfcheck.NewCounters())
	env.Drill = func() selfcheck.DrillState {
		return selfcheck.DrillState{Enabled: true, Checked: true, HaveBackup: true, Started: t0.Add(-time.Hour), Have: true, LastAt: t0.Add(-time.Minute),
			LastBackup: "每天-20261115-0530", LastProblem: `复制 config/a.yml 失败：write E:\秘密\抽验: database or disk is full`}
	}
	r := c.Eval(context.Background(), env)
	if r.State != selfcheck.Bad || r.Example["errorClass"] != selfcheck.StoreFull {
		t.Fatalf("%+v", r)
	}
	for k, v := range r.Example {
		if v == "" || len(v) > 40 || strings.ContainsAny(v, `\/`) {
			t.Errorf("例子 %s 不该放原文：%q", k, v)
		}
	}
}

// 阈值可调（Thresholds.DrillStale）：改了它，同一份状态的判定会变
func TestBackupDrillStaleThresholdIsRead(t *testing.T) {
	th := selfcheck.DefaultThresholds()
	st := selfcheck.DrillState{Enabled: true, Checked: true, HaveBackup: true, Started: t0.Add(-100 * 24 * time.Hour), Have: true, LastOK: true,
		LastAt: t0.Add(-5 * 24 * time.Hour), HavePass: true, LastPassAt: t0.Add(-5 * 24 * time.Hour)}
	eval := func(th selfcheck.Thresholds) selfcheck.State {
		env := envAt(t0, selfcheck.NewCounters())
		env.Drill = func() selfcheck.DrillState { return st }
		return findCheck(t, "backup.drill_stale", th).Eval(context.Background(), env).State
	}
	if got := eval(th); got != selfcheck.Ok {
		t.Fatalf("默认 10 天：5 天前通过该是 ok：%v", got)
	}
	th.DrillStale = 3 * 24 * time.Hour
	if got := eval(th); got != selfcheck.Bad {
		t.Fatalf("阈值改成 3 天：5 天前通过该是 bad：%v", got)
	}
}
