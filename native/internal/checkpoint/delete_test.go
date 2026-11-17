package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"sumika-native/internal/kernel"
)

func TestCheckpointDeletionBarrierFailure(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.capture(t, "new", "agent")
	plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
	require(t, err)
	sentinel := errors.New("injected target-volume flush failure")
	original := publishDeletion
	t.Cleanup(func() { publishDeletion = original })
	reached := false
	publishDeletion = func(root *os.Root, from, to string) error {
		reached = true
		checkFile(t, filepath.Join(f.work, from), []byte("agent"))
		require(t, original(root, from, to))
		return sentinel
	}
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
	if !reached || !errors.Is(err, sentinel) || inverse.Status != "partial" || len(inverse.Done) != 0 {
		t.Fatalf("删除屏障失败却提交成功：reached=%v rec=%+v err=%v", reached, inverse, err)
	}
	publishDeletion = original
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	if records[0].ID != inverse.ID || records[0].Status != "partial" {
		t.Fatalf("冷开丢失失败结局：%+v", records)
	}
	recovery, err := cold.Preview(t.Context(), f.owner, inverse.ID, "recovery")
	require(t, err)
	_, err = cold.Restore(t.Context(), f.owner, f.in(), inverse.ID, "recovery", recovery.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "new"), []byte("agent"))
}

func TestCheckpointDeletionCrash(t *testing.T) {
	if path := os.Getenv("CKPT_DELETE_RECORD"); path != "" {
		body, err := os.ReadFile(path)
		require(t, err)
		var rec Record
		require(t, json.Unmarshal(body, &rec))
		s, err := New(filepath.Dir(filepath.Dir(path)), Options{Authorize: func(context.Context, kernel.Scope, string) error { return nil }})
		require(t, err)
		original := publishDeletion
		publishDeletion = func(root *os.Root, from, to string) error {
			require(t, original(root, from, to))
			os.Exit(74)
			return nil
		}
		plan, err := s.Preview(t.Context(), rec.Owner, rec.ID, "step")
		require(t, err)
		in := rec.Invocation
		in.RunID, in.ToolID = "fixture-restore", "fixture-delete"
		_, err = s.Restore(t.Context(), rec.Owner, in, rec.ID, "step", plan.Version)
		t.Fatalf("未到达删除中断点：%v", err)
	}
	f := newFixture(t, nil)
	rec := f.capture(t, "new", "agent")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointDeletionCrash$")
	cmd.Env = append(os.Environ(), "CKPT_DELETE_RECORD="+filepath.Join(f.data, rec.ID, "record.json"))
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 74 {
		t.Fatalf("未硬退出：%v %s", err, output)
	}
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	inverse := records[0]
	if inverse.InverseOf != rec.ID || inverse.Status != "interrupted" || len(inverse.Done) != 0 {
		t.Fatalf("删除后硬退出丢失逆记录：%+v", inverse)
	}
	if _, err := os.Stat(filepath.Join(f.work, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原名未提交删除：%v", err)
	}
	plan, err := cold.Preview(t.Context(), f.owner, inverse.ID, "recovery")
	require(t, err)
	_, err = cold.Restore(t.Context(), f.owner, f.in(), inverse.ID, "recovery", plan.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "new"), []byte("agent"))
}
