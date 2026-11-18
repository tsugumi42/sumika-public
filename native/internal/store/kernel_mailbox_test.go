package store

import (
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"testing"
)

func TestTaskMailboxAcceptanceAndRecovery(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	repo := EcosystemStore{DB: set.TraceStore().Trace}
	owner := kernel.Scope{CharacterID: "fixture", Conversation: "fixture-private", ScopeID: "fixture-owner", AuthorityID: "fixture-grant", AuthorityRevision: 1}
	record := TaskRecord{ID: "fixture-task", ParentRun: "fixture-parent", ToolID: "fixture-tool", Digest: "fixture-digest", Owner: owner, RootID: "fixture-root", State: "running", Payload: `{}`}
	if _, created, err := repo.CreateTask(t.Context(), record); err != nil || !created {
		t.Fatal(created, err)
	}
	record.State = "completed"
	record.Result = `{"TaskID":"fixture-task","Status":"completed"}`
	if err = repo.FinishTask(t.Context(), record, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		pending, err := repo.PendingTasks(t.Context(), owner)
		if err != nil || len(pending) != 1 || pending[0].ID != record.ID {
			t.Fatal("接纳前读取丢邮箱", pending, err)
		}
	}
	other := owner
	other.Conversation = "unrelated-conversation"
	if err = repo.CommitTasks(t.Context(), other, "fixture-receiving", []string{record.ID}); err == nil {
		t.Fatal("其他域确认了邮箱")
	}
	for range 2 {
		if err = repo.CommitTasks(t.Context(), owner, "fixture-receiving", []string{record.ID}); err != nil {
			t.Fatal("同一接纳重试不幂等", err)
		}
	}
	if pending, err := repo.PendingTasks(t.Context(), owner); err != nil || len(pending) != 0 {
		t.Fatal("已接纳结果再次投递", pending, err)
	}
	record.ID = "interrupted-task"
	record.ParentRun = "another-parent"
	record.ToolID = "another-tool"
	record.State = "running"
	record.Result = ""
	if _, _, err = repo.CreateTask(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err = repo.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.Task(t.Context(), record.ID)
	if err != nil || recovered.State != "interrupted" {
		t.Fatal("恢复冒充完成", recovered, err)
	}
	t.Log("pending_reads=2 accepted_deliveries=1 cross_owner_commits=0 restart_state=interrupted")
}
