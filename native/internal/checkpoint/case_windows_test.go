package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCheckpointWindowsCaseAliases(t *testing.T) {
	f := newFixture(t, nil)
	require(t, os.WriteFile(filepath.Join(f.work, "A.txt"), []byte("original"), 0600))
	first := f.capture(t, "A.txt", "one")
	f.capture(t, "a.txt", "two")
	plan, err := f.s.Preview(t.Context(), f.owner, first.ID, "step")
	require(t, err)
	if len(plan.Files) != 1 {
		t.Fatalf("大小写别名没有合并：%+v", plan)
	}
	_, err = f.s.Restore(t.Context(), f.owner, f.in(), first.ID, "step", plan.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "A.txt"), []byte("original"))
}

func TestCheckpointWindowsCaseOnlyRename(t *testing.T) {
	f := newFixture(t, nil)
	require(t, os.WriteFile(filepath.Join(f.work, "A.txt"), []byte("original"), 0600))
	rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error { return os.Rename(filepath.Join(f.work, "A.txt"), filepath.Join(f.work, "a.txt")) })
	require(t, err)
	plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
	require(t, err)
	if len(plan.Files) != 1 {
		t.Fatalf("仅大小写改名丢失：%+v", plan)
	}
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
	require(t, err)
	entries, err := os.ReadDir(f.work)
	require(t, err)
	if len(entries) != 1 || entries[0].Name() != "A.txt" {
		t.Fatalf("原始文件名未恢复：%v", entries)
	}
	undo, err := f.s.Preview(t.Context(), f.owner, inverse.ID, "step")
	require(t, err)
	_, err = f.s.Restore(t.Context(), f.owner, f.in(), inverse.ID, "step", undo.Version)
	require(t, err)
	entries, err = os.ReadDir(f.work)
	require(t, err)
	if len(entries) != 1 || entries[0].Name() != "a.txt" {
		t.Fatalf("撤销恢复未保留改名：%v", entries)
	}
}

func TestCheckpointWindowsCaseSensitiveDirectory(t *testing.T) {
	f := newFixture(t, nil)
	name, err := windows.UTF16PtrFromString(f.work)
	require(t, err)
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	require(t, err)
	flags := uint32(windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR)
	err = windows.NtSetInformationFile(handle, &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&flags)), 4, windows.FileCaseSensitiveInformation)
	require(t, err)
	require(t, windows.CloseHandle(handle))
	require(t, os.WriteFile(filepath.Join(f.work, "A.txt"), []byte("upper"), 0600))
	require(t, os.WriteFile(filepath.Join(f.work, "a.txt"), []byte("lower"), 0600))
	first := f.capture(t, "A.txt", "upper-edit")
	f.capture(t, "a.txt", "lower-edit")
	plan, err := f.s.Preview(t.Context(), f.owner, first.ID, "step")
	require(t, err)
	if len(plan.Files) != 2 {
		t.Fatalf("敏感目录文件被误合并：%+v", plan)
	}
	_, err = f.s.Restore(t.Context(), f.owner, f.in(), first.ID, "step", plan.Version)
	require(t, err)
	checkFile(t, filepath.Join(f.work, "A.txt"), []byte("upper"))
	checkFile(t, filepath.Join(f.work, "a.txt"), []byte("lower"))
}
