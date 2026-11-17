package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckpointRenameAfterFinalCheck(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(work, "parent")
	require(t, os.Mkdir(path, 0700))
	require(t, os.WriteFile(filepath.Join(path, "source.tmp"), []byte("restored"), 0600))
	require(t, os.WriteFile(filepath.Join(path, "a"), []byte("agent"), 0600))
	require(t, os.WriteFile(filepath.Join(outside, "source.tmp"), []byte("external-temp"), 0600))
	require(t, os.WriteFile(filepath.Join(outside, "a"), []byte("neighbor"), 0600))
	workRoot, _, err := openWorkspace(work)
	require(t, err)
	defer workRoot.Close()
	root, err := workRoot.OpenRoot("parent")
	require(t, err)
	defer root.Close()
	parent, err := root.Open(".")
	require(t, err)
	defer parent.Close()
	_, err = checkHandle(parent, path)
	require(t, err)
	require(t, checkParents(root, "a"))
	// 最后一次目录核验后固定换路；源文件随后也按同一目录句柄打开，不重新解析父路径。
	moved := filepath.Join(work, "moved")
	require(t, os.Rename(path, moved))
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", path, outside).CombinedOutput(); err != nil {
		t.Fatalf("创建联接：%v %s", err, out)
	}
	source, err := openRenameFile(parent, "source.tmp")
	require(t, err)
	defer source.Close()
	require(t, commitRename(parent, source, "a"))
	require(t, source.Close())
	checkFile(t, filepath.Join(outside, "a"), []byte("neighbor"))
	checkFile(t, filepath.Join(outside, "source.tmp"), []byte("external-temp"))
	checkFile(t, filepath.Join(moved, "a"), []byte("restored"))
}
