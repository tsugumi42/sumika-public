// Package leftover 给要起整个后台的测试用的临时数据目录：删不掉时留下现场。
//
// Windows 上 Go 1.27 的 RemoveAll 按 POSIX 语义删，别人开着（允许删除共享）的文件名也会立刻消失；
// t.TempDir 清理时报“目录非空”，说明删的过程中目录里又冒出了新东西——测试结束后还有东西在往数据目录里写。
// CI 上 TestRunStopsCleanlyOnCancel、TestDeskLogFileKeepsHostAndBackendLines 各红过一次（2026-11-19），
// 本机各循环 30 遍复现不出来，日志里也看不出是谁在写，所以先把现场留下来（待办里有这一条）。
package leftover

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Dir 同 t.TempDir；在 t.TempDir 自己删目录之前先删一遍，删不掉就把剩下的条目和当时全部 goroutine 的栈写进测试日志
// （只在测试失败时打出来）。删不删得掉仍由 t.TempDir 的清理判，这里不放宽、不重试。
func Dir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { // 比 t.TempDir 的清理后登记，所以先跑
		err := os.RemoveAll(dir)
		if err == nil {
			return
		}
		var rest strings.Builder
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
			rel, _ := filepath.Rel(dir, p)
			if werr != nil {
				fmt.Fprintf(&rest, "  %s（%v）\n", rel, werr)
				return nil
			}
			if info, ierr := d.Info(); ierr == nil {
				fmt.Fprintf(&rest, "  %s %d 字节 %s\n", rel, info.Size(), info.ModTime().Format("15:04:05.000"))
			}
			return nil
		})
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Logf("临时数据目录删不掉：%v\n剩下的：\n%s这时的 goroutine：\n%s", err, rest.String(), buf[:n])
	})
	return dir
}
