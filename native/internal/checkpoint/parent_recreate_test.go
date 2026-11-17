package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointRestoreDeletedParent(t *testing.T) {
	f := newFixture(t, nil)
	name := filepath.Join("nested", "deeper", "原文.bin")
	path := filepath.Join(f.work, name)
	original := []byte{0, 255, '\r', '\n'}
	require(t, os.MkdirAll(filepath.Dir(path), 0700))
	require(t, os.WriteFile(path, original, 0600))
	record, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.Remove(filepath.Dir(path)); err != nil {
			return err
		}
		return os.Remove(filepath.Join(f.work, "nested"))
	})
	require(t, err)
	plan, err := f.s.Preview(t.Context(), f.owner, record.ID, "step")
	require(t, err)
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), record.ID, "step", plan.Version)
	require(t, err)
	checkFile(t, path, original)
	if len(inverse.Done) != 1 || inverse.Done[0] != name {
		t.Fatalf("恢复删除目录下的原文件漏记结局：%+v", inverse.Done)
	}
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	undo, err := cold.Preview(t.Context(), f.owner, inverse.ID, "step")
	require(t, err)
	_, err = cold.Restore(t.Context(), f.owner, f.in(), inverse.ID, "step", undo.Version)
	require(t, err)
	if body, err := os.ReadFile(path); !os.IsNotExist(err) {
		t.Fatalf("冷恢复的撤销没有删除原来不存在的文件：body=%q err=%v", bytes.TrimSpace(body), err)
	}
}
