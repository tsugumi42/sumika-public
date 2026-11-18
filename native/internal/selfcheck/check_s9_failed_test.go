package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// 失败判断排在“先等”前面：每天备份失败以后又碰上有角色在回复，S9 也要报 bad，不能被“先等”盖成 unknown
func TestBackupFreshFailedBeatsDeferred(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	env := envAt(t0, selfcheck.NewCounters())
	env.Backup = func() selfcheck.BackupState {
		return selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-30 * time.Hour), Started: t0.Add(-100 * time.Hour),
			FailedAt: t0.Add(-2 * time.Hour), LastFailure: "database or disk is full", Deferred: true}
	}
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Bad {
		t.Fatalf("失败过又碰上先等：该是 bad：%+v", r)
	}
}
