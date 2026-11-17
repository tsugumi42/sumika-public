package selfcheck_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// 最近一份好的备份跳过了媒体（链接、读不了）：仍是 ok，但备注里看得到跳过数
func TestBackupFreshShowsSkippedMedia(t *testing.T) {
	c := findCheck(t, "backup.daily_stale", selfcheck.DefaultThresholds())
	env := envAt(t0, selfcheck.NewCounters())
	env.Backup = func() selfcheck.BackupState {
		return selfcheck.BackupState{Enabled: true, Checked: true, HaveGood: true, LastGood: t0.Add(-2 * time.Hour), Started: t0.Add(-50 * time.Hour), LastSkipped: 3}
	}
	r := c.Eval(context.Background(), env)
	if r.State != selfcheck.Ok || !strings.Contains(r.Note, "3 个") {
		t.Fatalf("%+v", r)
	}
}
