package selfcheck

import (
	"context"
	"testing"
	"time"
)

// EvalNow 只算点名的检查、按 ID 返回，不写文件也不碰 Runner 的状态。
func TestEvalNowOnlyNamed(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	env := &Env{
		Now: now,
		Backup: func() BackupState {
			return BackupState{Enabled: true, Checked: true, Started: now.Add(-100 * time.Hour)}
		},
		Embed: func() EmbedState {
			return EmbedState{Enabled: true, Backend: "go-models", Status: "down", Reason: "找不到模型文件"}
		},
	}
	out := EvalNow(context.Background(), env, DefaultThresholds(), "backup.daily_stale", "embed.unavailable")
	if len(out) != 2 {
		t.Fatalf("要点名的两条结果，实际 %+v", out)
	}
	if r := out["backup.daily_stale"]; r.State != Bad || r.Next == "" {
		t.Errorf("S9 应照常判 bad 并带下一步：%+v", r)
	}
	if r := out["embed.unavailable"]; r.State != Bad || r.Note == "" {
		t.Errorf("S13 应照常判 bad：%+v", r)
	}
	if got := EvalNow(context.Background(), env, DefaultThresholds(), "no.such.check"); len(got) != 0 {
		t.Errorf("没点名的检查不该算：%+v", got)
	}
}

// 没接状态时 EvalNow 也照自检的规则记 unknown，不当成正常。
func TestEvalNowUnknownWithoutProvider(t *testing.T) {
	env := &Env{Now: time.Now()}
	out := EvalNow(context.Background(), env, DefaultThresholds(), "mcp.quarantined", "kernel.faulted")
	for _, id := range []string{"mcp.quarantined", "kernel.faulted"} {
		if got := out[id].State; got != Unknown {
			t.Errorf("%s 没接状态应是 unknown，实际 %s", id, got)
		}
	}
}
