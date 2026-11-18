//go:build windows

package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/dbguard"
)

// 改名回滚（复核第 2 轮建议 ①）：调用前是 0 字节主文件加一个打开着的 -wal。
// Windows 上改名被占用的 -wal 会失败；CreateIfEmpty 要软退 create_failed，主文件和 -wal 都留在原名，没有 .empty-* 残留。
func TestCreateIfEmptyKeepsEmptyFilesWhenWALRenameFails(t *testing.T) {
	data, file, rel := createFixture(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wal, err := os.OpenFile(file+"-wal", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close() // 句柄开着，改名 -wal 那一步才会失败

	created, rep, err := CreateIfEmpty(context.Background(), data, file, "", rel)
	if err == nil || created {
		t.Fatalf("改名 -wal 失败时该软退：created=%v err=%v", created, err)
	}
	var unavailable *dbguard.Unavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != dbguard.ReasonCreateFailed {
		t.Fatalf("失败原因是 %v，要 create_failed", err)
	}
	if rep.Action != dbguard.ActionCreate {
		t.Fatalf("护栏判定是 %v，要按空库新建", rep.Action)
	}
	for _, name := range []string{file, file + "-wal"} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("改名失败后 %s 不在原名：%v", name, err)
		}
	}
	if left, _ := filepath.Glob(file + ".empty-*"); len(left) != 0 {
		t.Errorf("失败后留下旁名文件：%v", left)
	}
}
