package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 子进程在包已删除、退役元数据仍在时硬退出，不能用 defer 模拟崩溃。
func TestCheckpointRetirementCrash(t *testing.T) {
	if data := os.Getenv("CKPT_RETIRE_DATA"); data != "" {
		root, err := os.OpenRoot(data)
		require(t, err)
		id := os.Getenv("CKPT_RETIRE_ID")
		require(t, retireRecord(root, id, func(root *os.Root, name string) error {
			require(t, root.Remove(filepath.Join(name, "ready.bin")))
			os.Exit(73)
			return nil
		}))
		t.Fatal("没有到达轮换中断点")
	}
	f := newFixture(t, nil)
	rec := f.capture(t, "a", "agent")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointRetirementCrash$")
	cmd.Env = append(os.Environ(), "CKPT_RETIRE_DATA="+f.data, "CKPT_RETIRE_ID="+rec.ID)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("未硬退出：%v %s", err, output)
	}
	cold, err := New(f.data, f.s.opts)
	require(t, err)
	records, err := cold.List(t.Context(), f.owner)
	require(t, err)
	if len(records) != 0 {
		t.Fatalf("退役记录仍可见：%+v", records)
	}
	if _, err := os.Stat(filepath.Join(f.data, rec.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("退役材料未回收：%v", err)
	}
	checkFile(t, filepath.Join(f.work, "a"), []byte("agent"))
	f.s = cold
	live := f.capture(t, "b", "next")
	require(t, os.Remove(filepath.Join(f.data, live.ID, live.Pack)))
	if _, err := New(f.data, f.s.opts); err == nil {
		t.Fatal("未退役的缺包损坏被当垃圾吞掉")
	}
	if _, err := os.Stat(filepath.Join(f.data, live.ID, "record.json")); err != nil {
		t.Fatalf("真实损坏证据被删除：%v", err)
	}
}
