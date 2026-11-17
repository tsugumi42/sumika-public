// Package sqlitesnap 给一个 SQLite 库取一份一致的快照（VACUUM INTO），并查副本能不能用。备份（internal/backup）和各个持有库连接的一方共用。
//
// 为什么另开一条连接：各库都是单连接池（SetMaxOpenConns(1)），在池里那条连接上跑 VACUUM INTO，整个后台对这个库的读写都得等着；
// archive 模式的连接还带着 query_only，它连 VACUUM INTO 写另一个文件也拒绝（memory/backup.go 的先例）。所以从已经开着的连接问出库文件在哪，
// 另开一条只读、不带 query_only 的连接取快照；WAL 模式下读者不挡写者，快照对应事务开始的那一刻。
package sqlitesnap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Into 用持有方已经开着的连接 db 取快照写到 dst（必须是还不存在的新文件）。
// 库文件从连接自己问出来（pragma_database_list），不由调用方另拼路径；内存库（没有文件，测试用）直接在这条连接上做。
func Into(ctx context.Context, db *sql.DB, dst string) error {
	if db == nil {
		return errors.New("没有可用的库连接")
	}
	file, err := fileOf(ctx, db)
	if err != nil {
		return err
	}
	if file == "" {
		return vacuumInto(ctx, db, dst)
	}
	return IntoFile(ctx, file, dst)
}

// IntoFile 另开一条只读连接，对库文件 src 取快照写到 dst。没有登记持有方的库（兜底）也走这里
func IntoFile(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("没有 %s：%w", src, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s 已经存在，快照只写新文件", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	db, err := openSource(src)
	if err != nil {
		return err
	}
	defer db.Close()
	return vacuumInto(ctx, db, dst)
}

func vacuumInto(ctx context.Context, db *sql.DB, dst string) error {
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("取快照：%w", err)
	}
	return nil
}

func fileOf(ctx context.Context, db *sql.DB) (string, error) {
	var file sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil {
		return "", fmt.Errorf("问库文件在哪：%w", err)
	}
	return file.String, nil
}

func uriPath(file string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
}

// openSource：只读（mode=ro，库文件写不了），不带 query_only（它连 VACUUM INTO 写别的文件也拒绝）。单连接、等锁最多 5 秒
func openSource(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+uriPath(file)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%w", file, err)
	}
	return db, nil
}

// Report 是对一个副本的检查结果
type Report struct {
	Integrity string           // integrity_check 的结果；"ok" 才算通过，否则是它报的前几行
	Tables    map[string]int64 // 每个普通表的行数；虚表（可能缺模块）不数，行数查不出的表记 -1
}

// OK：副本通过了 integrity_check
func (r Report) OK() bool { return r.Integrity == "ok" }

// Inspect 打开副本 file 查一遍：PRAGMA integrity_check，数每个普通表的行数。
// 用 mode=ro&immutable=1 打开：不拿锁、不产生 -wal 和 -shm（副本是刚写完的，不会再有别的写者）。
func Inspect(ctx context.Context, file string) (Report, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	db, err := sql.Open("sqlite", "file:"+uriPath(file)+"?"+q.Encode())
	if err != nil {
		return Report{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return Report{}, fmt.Errorf("integrity_check：%w", err)
	}
	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return Report{}, err
		}
		if len(lines) < 5 {
			lines = append(lines, s)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, err
	}
	rows.Close()
	rep := Report{Integrity: strings.Join(lines, "；"), Tables: map[string]int64{}}
	// 普通表：虚表的 sql 以 CREATE VIRTUAL TABLE 开头（缺模块时连查都查不了）；内部表 sqlite_ 开头不数
	trows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' AND upper(substr(ltrim(sql), 1, 20)) NOT LIKE 'CREATE VIRTUAL%' ORDER BY name`)
	if err != nil {
		return Report{}, fmt.Errorf("列表：%w", err)
	}
	var names []string
	for trows.Next() {
		var n string
		if err := trows.Scan(&n); err != nil {
			trows.Close()
			return Report{}, err
		}
		names = append(names, n)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return Report{}, err
	}
	sort.Strings(names)
	for _, n := range names {
		var c int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(n, `"`, `""`)+`"`).Scan(&c); err != nil {
			rep.Tables[n] = -1
			continue
		}
		rep.Tables[n] = c
	}
	return rep, nil
}
