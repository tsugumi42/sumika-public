package store

import (
	"context"
	"sumika-native/internal/paths"
	"testing"
)

func TestEcosystemJournalFailuresAndUnknown(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	repo := EcosystemStore{DB: set.TraceStore().Trace}
	defer set.Close()
	quota := []WebQuota{{Dimension: "fixture", Window: "root", Limit: 1, Amount: 1}}
	if err := repo.WebDispatch(t.Context(), "first", "fixture-owner", "fixture-root", 1000000, quota); err != nil {
		t.Fatal(err)
	}
	if err := repo.WebSettle(t.Context(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.WebDispatch(t.Context(), "second", "fixture-owner", "fixture-root", 1000000, quota); err == nil {
		t.Fatal("unknown 消费返还")
	}
	if err := repo.MCPDispatch(t.Context(), "stdio-call", "fixture-server", "binding", "stdio"); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPEnd(t.Context(), "stdio-call", false, MCPCauseError); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPTerminated(t.Context(), "stdio-call"); err != nil {
		t.Fatal(err)
	}
	blocked, err := repo.MCPBlocked(t.Context(), "fixture-server")
	if err != nil || blocked {
		t.Fatal("确认退出后隔离未解除", err)
	}
	var state string
	if err := repo.DB.QueryRowContext(context.Background(), `SELECT state FROM kernel_mcp_calls WHERE id='stdio-call'`).Scan(&state); err != nil || state != "terminated" {
		t.Fatal("终止被冒充成功", state, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.WebDispatch(t.Context(), "third", "fixture-owner", "fixture-root", 1000000, quota); err == nil {
		t.Fatal("数据库不可写仍准入")
	}
}
