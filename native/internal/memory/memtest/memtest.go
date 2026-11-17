// Package memtest 造记忆库测试夹具：Bun 形状的旧库和当前结构的空库。
//
// 只给测试用，非测试代码不许 import（archcheck 的 TestMemtestOnlyImportedByTests 钉着）。
// 建表语句只用 internal/memory.BaseSchema，不在这里抄第二份。
package memtest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"sumika-native/internal/memory"
)

// BunShaped 在 path 造一个 Bun 版形状的记忆库：只有基础五表，没有 goose 表、没有写入侧的表。
// 父目录不存在就建。要测迁移的夹具拿它当迁移前的库。
func BunShaped(t testing.TB, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memory.BaseSchema); err != nil {
		db.Close()
		t.Fatalf("造 Bun 形状的记忆库 %s：%v", path, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关 Bun 形状的记忆库 %s：%v", path, err)
	}
}

// Fresh 在 <data>/go/characters/<角色>/memory.db 走生产新建入口 memory.CreateIfEmpty，建当前结构的空库。
// path 已经在 <data> 下并按角色目录摆好；建不出来（路径不对、已有用户表）直接让测试失败。
func Fresh(t testing.TB, data, path string) {
	t.Helper()
	rel, err := filepath.Rel(data, path)
	if err != nil {
		t.Fatalf("算 %s 相对数据目录的路径：%v", path, err)
	}
	created, _, err := memory.CreateIfEmpty(context.Background(), data, path, filepath.Join(data, "go", "upgrade-snapshots"), filepath.ToSlash(rel))
	if err != nil {
		t.Fatalf("新建记忆库 %s：%v", path, err)
	}
	if !created {
		t.Fatalf("%s 不是空库，没有新建", path)
	}
}
