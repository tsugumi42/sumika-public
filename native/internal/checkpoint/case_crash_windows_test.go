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

func TestCheckpointWindowsCaseOnlyCrash(t *testing.T) {
	if path := os.Getenv("CKPT_CASE_RECORD"); path != "" {
		body, err := os.ReadFile(path)
		require(t, err)
		var rec Record
		require(t, json.Unmarshal(body, &rec))
		s, err := New(filepath.Dir(filepath.Dir(path)), Options{Authorize: func(context.Context, kernel.Scope, string) error { return nil }})
		require(t, err)
		original := publishReplacement
		publishReplacement = func(root *os.Root, from, to string, mode os.FileMode) error {
			require(t, original(root, from, to, mode))
			os.Exit(76)
			return nil
		}
		plan, err := s.Preview(t.Context(), rec.Owner, rec.ID, "step")
		require(t, err)
		in := rec.Invocation
		in.RunID, in.ToolID = "fixture-case-restore", "fixture-case"
		_, err = s.Restore(t.Context(), rec.Owner, in, rec.ID, "step", plan.Version)
		t.Fatalf("未到达大小写发布中断点：%v", err)
	}
	f := newFixture(t, nil)
	require(t, os.WriteFile(filepath.Join(f.work, "A.txt"), []byte("same\r\n"), 0600))
	rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
		return os.Rename(filepath.Join(f.work, "A.txt"), filepath.Join(f.work, "a.txt"))
	})
	require(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointWindowsCaseOnlyCrash$")
	cmd.Env = append(os.Environ(), "CKPT_CASE_RECORD="+filepath.Join(f.data, rec.ID, "record.json"))
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 76 {
		t.Fatalf("未硬退出：%v %s", err, output)
	}
	checkOnlyName(t, f.work, "A.txt")
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	inverse := records[0]
	if inverse.InverseOf != rec.ID || inverse.Status != "interrupted" || !inverse.Before["a.txt"].Exists || !inverse.After["A.txt"].Exists {
		t.Fatalf("逆记录未保留两边实际名称：%+v", inverse)
	}
	checkOnlyName(t, f.work, "A.txt")
	plan, err := cold.Preview(t.Context(), f.owner, inverse.ID, "recovery")
	require(t, err)
	if len(plan.Files) != 1 || plan.Files[0].Path != "a.txt" || plan.Files[0].CurrentPath != "A.txt" {
		t.Fatalf("中断恢复丢失实际名称差异：%+v", plan)
	}
	_, err = cold.Restore(t.Context(), f.owner, f.in(), inverse.ID, "recovery", plan.Version)
	require(t, err)
	checkOnlyName(t, f.work, "a.txt")
	checkFile(t, filepath.Join(f.work, "a.txt"), []byte("same\r\n"))
}

func checkOnlyName(t *testing.T, dir, want string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require(t, err)
	if len(entries) != 1 || entries[0].Name() != want {
		t.Fatalf("目录项名称不符：want=%q entries=%v", want, entries)
	}
}
