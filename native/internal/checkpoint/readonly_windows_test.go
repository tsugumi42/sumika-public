package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckpointWindowsReadonlyRestore(t *testing.T) {
	f := newFixture(t, nil)
	path := filepath.Join(f.work, "readonly.txt")
	original := []byte("readonly\r\n\x00original")
	require(t, os.WriteFile(path, original, 0600))
	require(t, os.Chmod(path, 0400))
	t.Cleanup(func() { os.Chmod(path, 0600) })
	rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Remove-Item -LiteralPath $env:CKPT_READONLY_PATH -Force")
		cmd.Env = append(os.Environ(), "CKPT_READONLY_PATH="+path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("删除只读夹具失败：%v %s", err, out)
		}
		return err
	})
	require(t, err)
	if rec.Before["readonly.txt"].Mode&0200 != 0 {
		t.Fatal("未捕获只读属性")
	}
	plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
	require(t, err)
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
	require(t, err)
	if inverse.Status != "ready" {
		t.Fatalf("恢复未完成：%+v", inverse)
	}
	checkFile(t, path, original)
	ptr, err := windows.UTF16PtrFromString(path)
	require(t, err)
	attrs, err := windows.GetFileAttributes(ptr)
	require(t, err)
	if attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		t.Fatalf("未恢复只读属性：%x", attrs)
	}
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	if records[0].Status != "ready" {
		t.Fatalf("冷开丢失恢复结局：%+v", records)
	}
}
