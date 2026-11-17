package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// 一份备份做得太久：像是卡住了，记 bad；正常做的（时间没到）仍是 unknown
func TestBackupFreshRunningTooLong(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	eval := func(since time.Duration) selfcheck.Result {
		env := envAt(t0, selfcheck.NewCounters())
		env.Backup = func() selfcheck.BackupState {
			return selfcheck.BackupState{Enabled: true, Checked: true, Running: true, RunningSince: t0.Add(-since), Started: t0.Add(-100 * time.Hour)}
		}
		return c.Eval(context.Background(), env)
	}
	if r := eval(30 * time.Minute); r.State != selfcheck.Unknown {
		t.Fatalf("才做了半小时：%+v", r)
	}
	if r := eval(8 * time.Hour); r.State != selfcheck.Bad {
		t.Fatalf("做了 8 小时，该记卡住：%+v", r)
	}
}

// 到点了但有角色在回复，先等：不当异常（和调度那边“最多等到 36 小时”配合）
func TestBackupFreshDeferredIsNotBad(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	env := envAt(t0, selfcheck.NewCounters())
	env.Backup = func() selfcheck.BackupState {
		return selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-30 * time.Hour), Started: t0.Add(-100 * time.Hour), Deferred: true}
	}
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Unknown {
		t.Fatalf("先等：%+v", r)
	}
}
