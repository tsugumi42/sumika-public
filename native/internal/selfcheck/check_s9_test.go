package selfcheck_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// S9：每天自动备份开着，却超过 26 小时没有校验通过的每天备份 → bad；刚开机、正在做、调度还没看过 → unknown，不报警
func TestBackupFresh(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	started := t0.Add(-100 * time.Hour) // 进程早就开着
	cases := []struct {
		name  string
		st    selfcheck.BackupState
		state selfcheck.State
	}{
		{"没开每日备份", selfcheck.BackupState{Enabled: false}, selfcheck.Ok},
		{"调度还没看过目录", selfcheck.BackupState{Enabled: true, Started: started}, selfcheck.Unknown},
		{"正在做", selfcheck.BackupState{Enabled: true, Checked: true, Running: true, Started: started}, selfcheck.Unknown},
		{"5 小时前做过", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-5 * time.Hour), Started: started}, selfcheck.Ok},
		{"25 小时前做过", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-25 * time.Hour), Started: started}, selfcheck.Ok},
		{"27 小时前做过，进程开了很久", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-27 * time.Hour), Started: started}, selfcheck.Bad},
		{"从没有好的，进程开了很久", selfcheck.BackupState{Enabled: true, Checked: true, Started: started}, selfcheck.Bad},
		{"27 小时前做过，刚开机 5 分钟（关机几天再开机）", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-27 * time.Hour), Started: t0.Add(-5 * time.Minute)}, selfcheck.Unknown},
		{"27 小时前做过，刚开机，但刚才试过并失败", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-27 * time.Hour), Started: t0.Add(-5 * time.Minute), FailedAt: t0.Add(-time.Minute), LastFailure: "database or disk is full"}, selfcheck.Bad},
		{"失败是在最近一份好的之前", selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-2 * time.Hour), Started: started, FailedAt: t0.Add(-3 * time.Hour), LastFailure: "x"}, selfcheck.Ok},
	}
	for _, tc := range cases {
		st := tc.st
		env := envAt(t0, selfcheck.NewCounters())
		env.Backup = func() selfcheck.BackupState { return st }
		r := c.Eval(context.Background(), env)
		if r.State != tc.state {
			t.Errorf("%s：想要 %s，实际 %+v", tc.name, tc.state, r)
		}
		if r.State == selfcheck.Bad && !strings.Contains(r.Next, "sumika-ctl backup now") {
			t.Errorf("%s：bad 要在 Next 里带下一步：%+v", tc.name, r)
		}
	}
}

func TestBackupFreshNotWired(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	if r := c.Eval(context.Background(), envAt(t0, selfcheck.NewCounters())); r.State != selfcheck.Unknown {
		t.Fatalf("没接备份状态不许记成 ok：%+v", r)
	}
}

// 失败原因只记类别，不带路径
func TestBackupFreshExampleHasNoPath(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	env := envAt(t0, selfcheck.NewCounters())
	env.Backup = func() selfcheck.BackupState {
		return selfcheck.BackupState{Enabled: true, Checked: true, Started: t0.Add(-time.Hour), FailedAt: t0.Add(-time.Minute), LastFailure: `备份目录不可写：open E:\秘密\备份: database or disk is full`}
	}
	r := c.Eval(context.Background(), env)
	if r.State != selfcheck.Bad || r.Example["errorClass"] != selfcheck.StoreFull {
		t.Fatalf("%+v", r)
	}
	for k, v := range r.Example {
		if v == "" || len(v) > 40 {
			t.Errorf("例子 %s 不该放原文：%q", k, v)
		}
	}
}
