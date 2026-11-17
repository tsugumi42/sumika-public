package memory

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pressly/goose/v3"

	"sumika-native/internal/dbguard"
)

// 角色库写入侧的表（规格-记忆-v2 13.2.12 的 W2）。迁移嵌在程序里用 goose，做法同 internal/store；
// 迁移前检查已有基础表和列（baseColumns），缺少时不继续。

//go:embed migrations
var migrationsFS embed.FS

// ErrNeedsMigration：以可写方式打开的库还没有写入侧的表。Open 自己不迁移；调用 Migrate 会改库，调用方负责先备份。
var ErrNeedsMigration = errors.New("角色库还没有写入侧的表：先关掉 Sumika，再跑 memory-migrate -data <数据目录>（会先给每个库拍快照）")

// baseColumns 是写入侧要求已有的基础表和列（facts、episodes 等）；缺少时不继续迁移。
var baseColumns = map[string][]string{
	"facts": {"id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation",
		"audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal"},
	"episodes":       {"id", "conversation", "text", "visibility", "from_seq", "to_seq", "created_at", "scope", "origin", "audience_at_origin", "event_at", "learned_at", "status"},
	"fact_revisions": {"id", "fact_id", "op", "at", "by", "reason"},
	"summary_state":  {"conversation", "last_seq", "updated_at"},
	"memory_jobs":    {"conversation", "from_seq", "to_seq", "version", "done_at"},
}

// writeSideColumns 是迁移加出来的表和列（每个迁移文件加了东西，这里同一个提交里跟着加）；以可写方式打开时查它们都在
var writeSideColumns = map[string][]string{
	"facts":          {"confirmed_by", "only_her", "learned_by", "whisper_json"},
	"fact_revisions": {"before_json", "after_json"},
	"memory_jobs":    {"counts_json"},
	"summary_state":  {"failures", "retry_at"},
	"item_scopes":    {"ref", "scope"},
	"fact_evidence":  {"fact_id", "day", "conversation", "seq", "speaker", "by_subject", "verdict", "at"},
	"memory_meta":    {"key", "value"},
	"shared_outbox":  {"id", "payload"},
}

// Migrate 给已有基础表和列的 memory.db 加上写入侧的表。重跑不变，文件必须已经存在（不新建）。
// 直接跑 goose、不拍迁移前快照：只给测试夹具和模板用；生产入口（memory-migrate）走 MigrateGuarded。
func Migrate(ctx context.Context, file string) error {
	if err := mustBeFile(file); err != nil {
		return err
	}
	db, err := openWritable(file)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkColumns(ctx, db, baseColumns); err != nil {
		return fmt.Errorf("%s 不像 Bun 版的 memory.db（不迁移，没改动）：%w", filepath.Base(file), err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	pv, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return err
	}
	if _, err := pv.Up(ctx); err != nil {
		return fmt.Errorf("迁移 %s：%w", filepath.Base(file), err)
	}
	return nil
}

// MigrateGuarded 给一个已有的角色记忆库走护栏（规格-库版本与升级护栏 第 8 条）：有待跑迁移时先拍快照再迁，
// 失败把库换回快照并返回错误（错误里带 -failed 快照路径）；不新建库，库不像 Bun 版 memory.db 时不迁移、不改动。
// snapshotDir 和 relName 由调用方给（memory-migrate 传 <数据目录>/go/upgrade-snapshots 和库相对数据目录的路径），
// 每个角色的库都叫 memory.db，只有 relName 能让快照互相区分。
func MigrateGuarded(ctx context.Context, file, snapshotDir, relName string) (dbguard.Report, error) {
	var rep dbguard.Report
	if err := mustBeFile(file); err != nil {
		return rep, err
	}
	if err := checkMemoryDBShape(ctx, file, false); err != nil {
		return rep, err
	}
	tracks, err := memoryTracks()
	if err != nil {
		return rep, err
	}
	db, rep, err := dbguard.Open(ctx, file, tracks, dbguard.Options{
		SnapshotDir: snapshotDir,
		RelName:     relName,
		OpenDB: func(file string, fresh bool) (*sql.DB, error) {
			if fresh {
				return nil, fmt.Errorf("%s：角色记忆库不新建", filepath.Base(file))
			}
			return openWritable(file)
		},
	})
	if db != nil {
		db.Close()
	}
	return rep, err
}

// checkMemoryDBShape 只读看一眼已有的库，确认它像 Bun 版 memory.db（baseColumns 里的表和列都在）。
// allowEmpty 时一张用户表都没有的空库（也没有 sumika_schema）不算错，留给护栏按新建处理；
// 不像就返回错误，调用方不拍快照、不改文件。
func checkMemoryDBShape(ctx context.Context, file string, allowEmpty bool) error {
	pre, err := openBackupSource(file)
	if err != nil {
		return err
	}
	defer pre.Close()
	if allowEmpty {
		var users int
		if err := pre.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'sumika_schema'`).Scan(&users); err != nil {
			return fmt.Errorf("看 %s 里有什么表：%w", filepath.Base(file), err)
		}
		if users == 0 {
			return nil
		}
	}
	if err := checkColumns(ctx, pre, baseColumns); err != nil {
		return fmt.Errorf("%s 不像 Bun 版的 memory.db（不迁移，没改动）：%w", filepath.Base(file), err)
	}
	return nil
}

// schemaReady 查写入侧的表和列是不是都在；不在返回 ErrNeedsMigration。只查不写（goose 自己查版本会建它的表，这里不用它）
func schemaReady(ctx context.Context, db *sql.DB) error {
	if err := checkColumns(ctx, db, baseColumns); err != nil {
		return fmt.Errorf("库的表对不上：%w", err)
	}
	if err := checkColumns(ctx, db, writeSideColumns); err != nil {
		return fmt.Errorf("%w（%v）", ErrNeedsMigration, err)
	}
	return nil
}

// checkColumns 查 want 里的表和列是不是都在，缺的列在错误里（"表.列"）
func checkColumns(ctx context.Context, db *sql.DB, want map[string][]string) error {
	var missing []string
	for table, cols := range want {
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			have[c] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(have) == 0 {
			missing = append(missing, table)
			continue
		}
		for _, c := range cols {
			if !have[c] {
				missing = append(missing, table+"."+c)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("缺 %s", strings.Join(missing, "、"))
	}
	return nil
}

func mustBeFile(file string) error {
	fi, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("没有 %s（写入侧不新建记忆库，库得先存在）：%w", filepath.Base(file), err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s 是目录", filepath.Base(file))
	}
	return nil
}

// openWritable 以可写方式打开一个已经存在的记忆库：mode=rw 不新建（modernc 默认"不存在就建"，B17 的教训：别在别处悄悄建出库）；
// 写事务一开始就拿写锁（_txlock=immediate），不先读后写再升级，省得两个写的撞上 SQLITE_BUSY；一个库一个连接，同 internal/store。
// 不改 journal_mode：库原来是什么样（Bun 建的是 WAL）就什么样
func openWritable(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	db, err := sql.Open("sqlite", "file:"+p+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%w", file, err)
	}
	return db, nil
}
