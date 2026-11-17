//go:build windows

package dbguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// 换回时改名第一次被拒（Windows 上杀毒、索引程序开着文件的样子）：照 secrets 的重试逻辑
// 等一下就成功，库照样换回原样。
func TestOpenRestoreRetriesAccessDeniedRename(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	o := testOptions(file, new(int))
	denied := 0
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") && denied == 0 {
			denied++
			return &os.LinkError{Op: "rename", Old: old, New: new, Err: windows.ERROR_ACCESS_DENIED}
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
		t.Fatalf("Reason = %s，要 migrate_failed（重试后应当换回成功）：%v", un.Reason, err)
	}
	if denied != 1 {
		t.Fatalf("只拒了一次，实际改名被拒 %d 次", denied)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("重试换回后和迁移前不相等")
	}
	if left := sidecars(t, file); len(left) != 0 {
		t.Fatalf("重试换回后还有旁文件：%v", left)
	}
}
