package store

import (
	"database/sql"
	"os"
	"sync"
)

// 测试程序里每条测试都新建库、从头跑一遍迁移（goose 逐条建表，加起来 70 毫秒上下，Windows 上被杀毒拖得更慢），
// 一个包几百条就是十几秒（规范-测试耗时）。同一套迁移（目录加版本表名，或护栏的一组轨）在这个进程里只跑一次，
// 第一个建好的库做成模板，之后新建的库直接拷这份字节。正式程序不走这里：testFastSync 只有测试程序才开。
var (
	tplMu sync.Mutex
	tpls  = map[string][]byte{}
)

func templateBytes(key string) []byte {
	if !testFastSync.Load() {
		return nil
	}
	tplMu.Lock()
	defer tplMu.Unlock()
	return tpls[key]
}

// saveTemplate 在新建的库迁移完以后记下它的内容；失败就不记，之后的库照旧自己迁移。
func saveTemplate(db *sql.DB, file, key string) {
	if !testFastSync.Load() {
		return
	}
	tplMu.Lock()
	_, have := tpls[key]
	tplMu.Unlock()
	if have {
		return
	}
	// 把 WAL 里的内容并回主文件再读：之后拷出去的就是一个完整的库
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return
	}
	b, err := os.ReadFile(file)
	if err != nil || len(b) == 0 {
		return
	}
	tplMu.Lock()
	tpls[key] = b
	tplMu.Unlock()
}

func migratedTemplate(dir, table string) []byte {
	return templateBytes(dir + "|" + table)
}

func saveMigratedTemplate(db *sql.DB, file, dir, table string) {
	saveTemplate(db, file, dir+"|"+table)
}
