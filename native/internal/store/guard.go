package store

// 开库走升级护栏（规格-库版本与升级护栏 第 3 条）：迁移前拍快照、失败换回、太新拒开、写版本表。
// 一个库文件一次 dbguard.Open，平台库的 chat 轨和平台轨在同一次调用里，第二条失败时第一条也一起换回。
// 这里只定义四条迁移轨和传进去的依赖；判定、快照、换回都在 internal/dbguard。

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
)

// migrationFS 是迁移轨的迁移文件来源。生产就是内嵌的 migrations；只有测试会换成会失败的版本（失败注入），
// 生产代码不读环境变量、不加开关。替换的测试不能 t.Parallel：并行测试都在它恢复之后才跑。
var migrationFS fs.FS = migrations

// track 造一条迁移轨：迁移文件已经 Sub 到目录，goose 表名和护栏起点号照现有登记。
func track(name, dir, table string) (dbguard.Track, error) {
	sub, err := fs.Sub(migrationFS, dir)
	if err != nil {
		return dbguard.Track{}, fmt.Errorf("迁移轨 %s 找不到目录 %s：%w", name, dir, err)
	}
	floor, ok := dbguard.Floors[name]
	if !ok {
		return dbguard.Track{}, fmt.Errorf("迁移轨 %s 没有护栏起点号", name)
	}
	return dbguard.Track{Name: name, FS: sub, Table: table, Floor: floor}, nil
}

// platformTracks 是一个平台库的全部迁移轨：通用 chat，后面是这个平台自己的轨（有迁移目录才有）。
// 顺序就是迁移顺序（规格第 3 条：平台库先 chat 后平台轨）。
func platformTracks(platform string) ([]dbguard.Track, error) {
	chat, err := track("chat", "migrations/chat", "")
	if err != nil {
		return nil, err
	}
	tracks := []dbguard.Track{chat}
	if hasMigrations(platform) {
		own, err := track(platform, "migrations/"+platform, "goose_db_version_"+platform)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, own)
	}
	return tracks, nil
}

// traceTracks 是共用运行记录库的轨。
func traceTracks() ([]dbguard.Track, error) {
	t, err := track("trace", "migrations/trace", "")
	if err != nil {
		return nil, err
	}
	return []dbguard.Track{t}, nil
}

// TraceTracks 是 trace 库的迁移轨，给只读旁路（存储统计）探查用；只读调用方自己只 Inspect，不迁移。
func TraceTracks() ([]dbguard.Track, error) { return traceTracks() }

// tracksTemplateKey 是测试模板的键。模板只在测试程序里用（testFastSync），生产不读。
func tracksTemplateKey(tracks []dbguard.Track) string {
	names := make([]string, len(tracks))
	for i, t := range tracks {
		names[i] = t.Name
	}
	return "tracks:" + strings.Join(names, ",")
}

// openGuarded 打开（没有就建）一个库文件，判定、快照、迁移、换回、写版本表都交给 dbguard。
// 错误原样返回，调用方用 errors.As 能取到 *dbguard.Unavailable；整体报错、不启动的行为由调用方决定。
func openGuarded(file string, tracks []dbguard.Track, snapshotDir, relName string) (*sql.DB, dbguard.Report, error) {
	key := tracksTemplateKey(tracks)
	db, rep, err := dbguard.Open(context.Background(), file, tracks, dbguard.Options{
		SnapshotDir: snapshotDir,
		RelName:     relName,
		OpenDB:      func(file string, fresh bool) (*sql.DB, error) { return openGuardedDB(file, fresh, key) },
	})
	if err != nil {
		return nil, rep, err
	}
	// 模板要在护栏写完版本表之后才存：拷模板建出来的库也带着 sumika_schema（规格第 3 条第 1 步、坑 16）。
	if rep.Action == dbguard.ActionCreate && testFastSync.Load() {
		saveTemplate(db, file, key)
	}
	return db, rep, nil
}

// openGuardedDB 是 dbguard 拿的可写连接：用现有的 open 和 PRAGMA。
// fresh 时先设 auto_vacuum 再建表；每次打开都重设 WAL 和 journal_size_limit（换回后的主文件头是回滚日志模式）。
// 测试程序里有模板时，fresh 直接把已经迁移好的字节写进文件，goose 会看到全部已应用。
func openGuardedDB(file string, fresh bool, templateKey string) (*sql.DB, error) {
	if fresh {
		if tpl := templateBytes(templateKey); tpl != nil {
			if err := os.WriteFile(file, tpl, 0o644); err != nil {
				return nil, err
			}
			db, err := open(file, false)
			if err != nil {
				return nil, err
			}
			if err := applyJournalPRAGMAs(db); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		}
	}
	db, err := open(file, false)
	if err != nil {
		return nil, err
	}
	if fresh {
		if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil { // 必须在建任何表之前
			db.Close()
			return nil, err
		}
	}
	if err := applyJournalPRAGMAs(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func applyJournalPRAGMAs(db *sql.DB) error {
	for _, q := range []string{"PRAGMA journal_mode = WAL", "PRAGMA journal_size_limit = 4194304"} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// relToData 是库相对数据目录的路径，起快照文件名用（每个库都要能互相区分）。
func relToData(p paths.Paths, file string) string {
	rel, err := filepath.Rel(p.Root, file)
	if err != nil {
		return filepath.Base(file)
	}
	return filepath.ToSlash(rel)
}

// fileSnapshotDir 是数据目录外的工具入口（OpenChatFile、OpenTraceFile）放快照的地方：
// 库文件所在目录下的 upgrade-snapshots，不能让临时库的快照落进真实数据目录。
func fileSnapshotDir(file string) string {
	return filepath.Join(filepath.Dir(file), "upgrade-snapshots")
}
