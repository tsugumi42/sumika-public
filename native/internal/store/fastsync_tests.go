package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// 测试程序里所有 SQLite 连接都不等落盘（synchronous=OFF）：每条测试都新建库、跑一遍迁移，默认每次提交都
// 要刷盘，占了开后台时间的大头（规范-测试耗时）。测试库在临时目录，断电丢数据无所谓，测的是逻辑不是掉电。
// 放在这里是因为各包都间接用到它，不用每个测试包各写一遍，也不会漏。正式程序的文件名不是 .test，不受影响。
func init() {
	if !inTestBinary(os.Args[0]) {
		return
	}
	testFastSync.Store(true)
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		_, err := conn.ExecContext(context.Background(), "PRAGMA synchronous=OFF", nil)
		return err
	})
}

// inTestBinary 看程序名：go test 编出来的是 xxx.test 或 xxx.test.exe。
func inTestBinary(arg0 string) bool {
	name := strings.ToLower(filepath.Base(arg0))
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".test.exe")
}
