package backup

import (
	"context"
	"os"
	"testing"

	"modernc.org/sqlite"
)

// realSync 存正式程序里 syncFile 的默认值：包级变量先于下面的 init 初始化，init 把 syncFile 换成不刷以后，这里还是原来的
var realSync = syncFile

// 测试里所有 SQLite 连接都不等落盘（synchronous=OFF）：每条测试都要建几个库、再取快照，默认每次提交都刷盘，
// 一个包 105 条测试 25 秒里大半是在等 FlushFileBuffers，机器忙时（合并队列里）涨到 69 秒（规范-测试耗时）。
// 测试库都在临时目录，断电丢数据无所谓，测的是逻辑。store 包给整个测试程序做了同样的事（store/fastsync_tests.go），
// 备份是叶子包不引它，这里自己登记一份；只在 backup 的测试程序里生效，正式程序不受影响
func init() {
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		_, err := conn.ExecContext(context.Background(), "PRAGMA synchronous=OFF", nil)
		return err
	})
	syncFile = func(*os.File) error { return nil } // 副本和清单也不刷盘；下面的测试单独数“该刷的地方都调了刷盘”
}

// 刷盘的默认值守护：正式程序里 syncFile 的默认值真的会刷盘，不是被改成了空操作。
// 判法：对一个已经关闭的文件调它必须返回错误（真的 Sync 会报“文件已关闭”，空操作返回 nil）；
// 对打开的文件调它也不能出错（不是随便返回个错误）
func TestDefaultSyncFileReallySyncs(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sync-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := realSync(f); err != nil {
		t.Fatalf("打开的文件刷盘不该出错：%v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := realSync(f); err == nil {
		t.Fatal("对已经关闭的文件刷盘该返回错误：默认的 syncFile 被换成了空操作？")
	}
}

// 测试里不刷盘，不代表正式程序不刷：每个复制的文件、写好的清单写完都要过 syncFile（副本、清单落盘以后才算数，断电不留半个“好备份”）。
// 数一数：一次备份里 syncFile 的调用不少于复制的文件数加清单（先写 partial 清单、最后改成 ok 的清单，至少两次）
func TestRunSyncsEveryCopiedFileAndManifest(t *testing.T) {
	var synced int
	old := syncFile
	syncFile = func(*os.File) error { synced++; return nil }
	t.Cleanup(func() { syncFile = old })
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	m, err := ReadManifest(res.Folder)
	if err != nil {
		t.Fatal(err)
	}
	copied := len(m.Files) - m.Linked - len(m.DBs) // 库是 sqlite 自己写出来的快照，不走复制，也不过 syncFile
	if copied == 0 || synced < copied+2 {
		t.Fatalf("复制了 %d 个普通文件，刷盘只调了 %d 次（至少 文件数 + 2 次清单）", copied, synced)
	}
}
