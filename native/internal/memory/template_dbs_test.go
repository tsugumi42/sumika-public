package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// 测试里每条都要新建库：建五张表、迁移、再建旁边的 memory-v2.db，每一步都是真写文件（Windows 上被杀毒拖慢），
// 一个包几百条加起来占了 CPU 的一半（规范-测试耗时）。这三份“刚建好的库”在测试进程里只造一次，之后各条测试直接拷字节；
// 要预先放行（seed）的还是先拷表结构、放行、再迁移，顺序和原来一样。哪一步造不出来就退回逐条自己建。
var (
	tplOnce                 sync.Once
	tplBunDB, tplMigratedDB []byte
	tplSidecarDB            []byte
)

func buildDBTemplates(t *testing.T) {
	tplOnce.Do(func() {
		dir := t.TempDir()
		file := filepath.Join(dir, "memory.db")
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
		if err != nil {
			return
		}
		_, err = db.Exec(BaseSchema)
		db.Close()
		if err != nil {
			return
		}
		bun, ok := readLoneFile(file)
		if !ok {
			return
		}
		if err := Migrate(context.Background(), file); err != nil {
			return
		}
		mig, ok := readLoneFile(file)
		if !ok {
			return
		}
		side := filepath.Join(dir, "side", "memory-v2.db")
		if os.MkdirAll(filepath.Dir(side), 0o755) != nil {
			return
		}
		x, err := OpenIndex(context.Background(), side)
		if err != nil {
			return
		}
		x.Close()
		sd, ok := readLoneFile(side)
		if !ok {
			return
		}
		tplBunDB, tplMigratedDB, tplSidecarDB = bun, mig, sd
	})
}

// readLoneFile 读库文件的全部字节；旁边还留着 -wal / -journal 就说明没收干净，不当模板
func readLoneFile(file string) ([]byte, bool) {
	for _, suffix := range []string{"-wal", "-journal", "-shm"} {
		if _, err := os.Stat(file + suffix); err == nil {
			return nil, false
		}
	}
	b, err := os.ReadFile(file)
	return b, err == nil && len(b) > 0
}
