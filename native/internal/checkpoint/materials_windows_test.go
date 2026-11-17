package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckpointMaterialCleanupRejectsJunction(t *testing.T) {
	f := newFixture(t, nil)
	nested := filepath.Join(f.work, "nested")
	require(t, os.Mkdir(nested, 0700))
	require(t, os.WriteFile(filepath.Join(nested, "a"), []byte("original"), 0600))
	rec := f.capture(t, filepath.Join("nested", "a"), "agent")
	plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
	require(t, err)
	inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
	require(t, err)
	name := materialName(inverse.ID+"-0", true)
	require(t, os.WriteFile(filepath.Join(nested, name), []byte("owned"), 0600))
	moved := filepath.Join(f.work, "moved")
	require(t, os.Rename(nested, moved))
	outside := t.TempDir()
	require(t, os.WriteFile(filepath.Join(outside, name), []byte("neighbor"), 0600))
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", nested, outside).CombinedOutput(); err != nil {
		t.Fatalf("创建联接：%v %s", err, out)
	}
	storage, err := os.OpenRoot(f.data)
	require(t, err)
	defer storage.Close()
	if err := cleanupRestoreMaterials(&inverse, storage); err == nil {
		t.Fatal("清理沿联接进入未登记目录")
	}
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	if len(records) != 2 {
		t.Fatalf("工作区换路阻断了存储冷打开：%+v", records)
	}
	checkFile(t, filepath.Join(outside, name), []byte("neighbor"))
	checkFile(t, filepath.Join(moved, name), []byte("owned"))
}

func TestCheckpointMaterialCleanupContinuesAfterMissing(t *testing.T) {
	f := newFixture(t, nil)
	for _, name := range []string{"a", "b"} {
		require(t, os.WriteFile(filepath.Join(f.work, name), []byte("original-"+name), 0600))
	}
	rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
		for _, name := range []string{"a", "b"} {
			if err := os.WriteFile(filepath.Join(f.work, name), []byte("agent-"+name), 0600); err != nil {
				return err
			}
		}
		return nil
	})
	require(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointRestoreTempCrash$")
	cmd.Env = append(os.Environ(), "CKPT_TEMP_RECORD="+filepath.Join(f.data, rec.ID, "record.json"), "CKPT_TEMP_STAGE=second")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 75 {
		t.Fatalf("未在第二项材料落盘后硬退出：%v %s", err, out)
	}
	records, err := f.s.List(t.Context(), f.owner)
	require(t, err)
	inverse := records[0]
	if inverse.InverseOf != rec.ID || inverse.Status != "interrupted" {
		t.Fatalf("未留下待恢复逆记录：%+v", inverse)
	}
	first := filepath.Join(f.work, materialName(inverse.ID+"-0", true))
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("首项临时名应该已发布：%v", err)
	}
	second := filepath.Join(f.work, materialName(inverse.ID+"-1", true))
	checkFile(t, second, []byte("original-b"))
	neighbor := filepath.Join(f.work, materialName(inverse.ID+"-1", false))
	require(t, os.WriteFile(neighbor, []byte("neighbor"), 0600))
	_, err = New(f.data, f.s.opts)
	require(t, err)
	if _, err := os.Stat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失首项阻断了后项真实残留回收：%v", err)
	}
	checkFile(t, filepath.Join(f.work, "a"), []byte("original-a"))
	checkFile(t, filepath.Join(f.work, "b"), []byte("agent-b"))
	checkFile(t, neighbor, []byte("neighbor"))
	parent, err := os.Open(f.work)
	require(t, err)
	defer parent.Close()
	path, err := windows.UTF16PtrFromString(neighbor)
	require(t, err)
	locked, err := windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require(t, err)
	defer windows.CloseHandle(locked)
	if _, err := openMaterialFile(parent, filepath.Base(neighbor)); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("占用错误被吞掉或误分类：%v", err)
	}
}
