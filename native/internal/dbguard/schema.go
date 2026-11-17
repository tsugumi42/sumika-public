package dbguard

import (
	"context"
	"database/sql"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// WriteSchema 在一个事务里建 sumika_schema 并给每条轨 upsert 一行：
// version 从 goose 表读（没有迁移文件的轨用 Track.Version），min_reader 用 Track.MinReader()。
// 重复调用结果一样；migrated_at 取 now 的 UTC、RFC 3339。
//
// 调用方要在迁移全部成功之后调（第 3 条第 6 步），或者在没有待跑迁移、版本表缺行 / 落后时补写（第 5 步）。
func WriteSchema(ctx context.Context, db *sql.DB, tracks []Track, migratedBy string, now time.Time) error {
	if db == nil {
		return fmt.Errorf("没有可用的库连接")
	}
	if migratedBy == "" {
		migratedBy = "dev"
	}
	type row struct {
		track     string
		version   int64
		minReader int64
	}
	rows := make([]row, 0, len(tracks))
	for _, t := range tracks {
		minReader, err := t.MinReader()
		if err != nil {
			return err
		}
		rows = append(rows, row{track: t.Name, minReader: minReader})
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始写 %s：%w", SchemaTable, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+quoteIdent(SchemaTable)+` (
		track TEXT PRIMARY KEY,
		version INTEGER NOT NULL,
		min_reader INTEGER NOT NULL,
		migrated_by TEXT NOT NULL,
		migrated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("建 %s：%w", SchemaTable, err)
	}
	at := now.UTC().Format(time.RFC3339)
	for i, t := range tracks {
		version := t.Version
		if t.FS != nil {
			exists, err := tableExists(ctx, tx, t.table())
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("迁移轨 %s 的版本表 %s 不存在：先把迁移跑完再写 %s", t.Name, t.table(), SchemaTable)
			}
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(version_id), 0) FROM `+quoteIdent(t.table())+` WHERE is_applied`).Scan(&version); err != nil {
				return fmt.Errorf("读迁移记录 %s：%w", t.table(), err)
			}
		}
		rows[i].version = version
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+quoteIdent(SchemaTable)+`
			(track, version, min_reader, migrated_by, migrated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(track) DO UPDATE SET
				version = excluded.version,
				min_reader = excluded.min_reader,
				migrated_by = excluded.migrated_by,
				migrated_at = excluded.migrated_at`,
			rows[i].track, rows[i].version, rows[i].minReader, migratedBy, at); err != nil {
			return fmt.Errorf("写 %s 的 %s：%w", SchemaTable, rows[i].track, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 %s：%w", SchemaTable, err)
	}
	return nil
}

func tableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("查表 %s 在不在：%w", name, err)
	}
	return n > 0, nil
}

// MigratedBy 是写进 sumika_schema.migrated_by 的程序标识：构建的 vcs.revision 前 9 位，取不到写 dev。
func MigratedBy() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	for _, s := range bi.Settings {
		if s.Key != "vcs.revision" {
			continue
		}
		rev := strings.TrimSpace(s.Value)
		if len(rev) >= 9 {
			return rev[:9]
		}
		if rev != "" {
			return rev
		}
	}
	return "dev"
}
