package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 固定“取得目录句柄→父目录换路→发布”的顺序，不能靠碰巧找不到外部临时名来挡越界。
func TestCheckpointRestoreParentReplaced(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace", true: "delete"}[remove], func(t *testing.T) {
			work := t.TempDir()
			outside := t.TempDir()
			parentPath := filepath.Join(work, "parent")
			require(t, os.Mkdir(parentPath, 0700))
			require(t, os.WriteFile(filepath.Join(parentPath, "a"), []byte("agent"), 0600))
			require(t, os.WriteFile(filepath.Join(outside, "a"), []byte("neighbor"), 0600))
			const id = "fixture-publication"
			externalTemp := filepath.Join(outside, ".sumika-checkpoint-"+id+".tmp")
			require(t, os.WriteFile(externalTemp, []byte("external-temp"), 0600))
			workRoot, _, err := openWorkspace(work)
			require(t, err)
			defer workRoot.Close()
			var parent restoreParent
			defer parent.close()
			root, name, err := parent.resolve(workRoot, filepath.Join("parent", "a"))
			require(t, err)
			moved := filepath.Join(work, "moved")
			require(t, os.Rename(parentPath, moved))
			if runtime.GOOS == "windows" {
				if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", parentPath, outside).CombinedOutput(); err != nil {
					t.Fatalf("创建临时联接：%v %s", err, output)
				}
			} else {
				require(t, os.Symlink(outside, parentPath))
			}
			state := FileState{Exists: !remove, Mode: 0600}
			err = restoreFile(root, parent.file, name, state, []byte("restored"), id, nil)
			if !errors.Is(err, ErrLink) {
				t.Fatalf("父目录换路未拒绝：%v", err)
			}
			checkFile(t, filepath.Join(outside, "a"), []byte("neighbor"))
			checkFile(t, externalTemp, []byte("external-temp"))
			checkFile(t, filepath.Join(moved, "a"), []byte("agent"))
			entries, err := os.ReadDir(moved)
			require(t, err)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".sumika-checkpoint-") {
					t.Fatalf("失败发布遗留临时文件：%s", entry.Name())
				}
			}
		})
	}
}
