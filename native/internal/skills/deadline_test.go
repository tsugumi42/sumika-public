package skills

import (
	"context"
	"errors"
	"sumika-native/internal/ecosystem"
	"testing"
	"time"
)

func TestSkillScanHonorsDeadline(t *testing.T) {
	_, a := skillAccess(t, "skill:fixture")
	c, err := New(Options{Enabled: true, Roots: []Root{{ID: "fixture", Path: t.TempDir()}}, ScanTimeoutMs: 1, Approve: func(ctx context.Context, _ ecosystem.Access, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("配置的短扫描期限未传到读取审批")
		}
		<-ctx.Done()
		return ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(t.Context(), a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("扫描期限未生效", err)
	}
}
