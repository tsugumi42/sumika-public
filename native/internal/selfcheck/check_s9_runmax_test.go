package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// “做太久”的阈值跟 backup.runMaxHours 走（取 runMax + 1 小时）：runMaxHours 调到 12 小时，做了 8 小时还不算卡住；做了 14 小时才算
func TestBackupFreshRunningLimitFollowsRunMax(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	eval := func(runMax, since time.Duration) selfcheck.Result {
		env := envAt(t0, selfcheck.NewCounters())
		env.Backup = func() selfcheck.BackupState {
			return selfcheck.BackupState{Enabled: true, Checked: true, Running: true, RunningSince: t0.Add(-since), RunMax: runMax, Started: t0.Add(-100 * time.Hour)}
		}
		return c.Eval(context.Background(), env)
	}
	if r := eval(12*time.Hour, 8*time.Hour); r.State != selfcheck.Unknown {
		t.Fatalf("runMax 12 小时，做了 8 小时还在正常范围：%+v", r)
	}
	if r := eval(12*time.Hour, 14*time.Hour); r.State != selfcheck.Bad {
		t.Fatalf("超过 runMax + 1 小时：%+v", r)
	}
	if r := eval(2*time.Hour, 4*time.Hour); r.State != selfcheck.Bad {
		t.Fatalf("runMax 调小以后阈值也跟着小：%+v", r)
	}
	if r := eval(72*time.Hour, 20*time.Hour); r.State != selfcheck.Unknown {
		t.Fatalf("runMax 72 小时，做了 20 小时还没到 26 小时的封顶：%+v", r)
	}
	if r := eval(72*time.Hour, 27*time.Hour); r.State != selfcheck.Bad {
		t.Fatalf("runMax 调得再大，做了 26 小时以上也该报（阈值取 min(runMax+1h, 26h)）：%+v", r)
	}
	if r := eval(0, 8*time.Hour); r.State != selfcheck.Bad {
		t.Fatalf("不知道 runMax 时按 7 小时：%+v", r)
	}
}
