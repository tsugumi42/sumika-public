package dbguard

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
	"syscall"

	_ "modernc.org/sqlite"
)

// SchemaRow 是 sumika_schema 里一条迁移轨的行。
type SchemaRow struct {
	Track      string
	Version    int64
	MinReader  int64
	MigratedBy string
	MigratedAt string
}

// TrackState 是探查结果里一条轨的样子。
// HasGooseTable 为假时 Applied 是 0（没有迁移文件的轨是它的固定版本号）；Schema 为 nil 表示版本表缺这一行。
type TrackState struct {
	Name          string
	Table         string
	HasGooseTable bool
	Applied       int64
	Schema        *SchemaRow
}

// State 是一个库文件的只读探查结果。Exists 为假时不打开文件，也不建任何东西。
// SchemaRows 是 sumika_schema 的全部行（没有这张表时为 nil），换回后拿它和迁移前比。
// UserTables 是 sqlite_master 里 type='table'、名字不以 sqlite_ 开头、也不是 sumika_schema 的表，按名字排好；
// 决定这是不是“认不出的库”（规格第 3 条 3b）用它。只有 sumika_schema 的库按“一张表都没有”处理。
type State struct {
	File       string
	Exists     bool
	HasSchema  bool
	SchemaRows map[string]SchemaRow
	UserTables []string
	Tracks     []TrackState
}

// Probe 只读看一眼库文件：有没有 sumika_schema、每条轨的 goose 已应用最大号、版本表里的行。
// 文件不存在返回 Exists=false；存在时用 mode=ro + query_only(1) 打开，不建表、不写库、不用 goose Provider。
func Probe(ctx context.Context, file string, tracks []Track) (State, error) {
	if _, err := os.Stat(file); err != nil {
		// 路径中间某一级是文件时，Windows 报“路径不存在”，Linux 报 ENOTDIR；两边都是“这个库不存在”
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return State{File: file}, nil
		}
		return State{File: file}, fmt.Errorf("看 %s 在不在：%w", file, err)
	}
	db, err := openReadOnly(file)
	if err != nil {
		return State{File: file}, err
	}
	defer db.Close()
	return probeConn(ctx, file, db, tracks)
}

// probeConn 在一个已经打开的只读连接上读探查结果；换回后的验证用 immutable 连接走这里。
func probeConn(ctx context.Context, file string, db *sql.DB, tracks []Track) (State, error) {
	st := State{File: file, Exists: true}
	names, userTables, err := tableNames(ctx, db)
	if err != nil {
		return st, err
	}
	st.HasSchema = names[SchemaTable]
	st.UserTables = userTables

	schema := map[string]*SchemaRow{}
	if st.HasSchema {
		rows, err := db.QueryContext(ctx, `SELECT track, version, min_reader, migrated_by, migrated_at FROM `+quoteIdent(SchemaTable))
		if err != nil {
			return st, fmt.Errorf("读 %s：%w", SchemaTable, err)
		}
		st.SchemaRows = map[string]SchemaRow{}
		for rows.Next() {
			var r SchemaRow
			var version, minReader any
			if err := rows.Scan(&r.Track, &version, &minReader, &r.MigratedBy, &r.MigratedAt); err != nil {
				rows.Close()
				return st, fmt.Errorf("读 %s：%w", SchemaTable, err)
			}
			if r.Version, err = integerColumn(version, "version"); err != nil {
				rows.Close()
				return st, fmt.Errorf("读 %s：%w", SchemaTable, err)
			}
			if r.MinReader, err = integerColumn(minReader, "min_reader"); err != nil {
				rows.Close()
				return st, fmt.Errorf("读 %s：%w", SchemaTable, err)
			}
			st.SchemaRows[r.Track] = r
			row := r
			schema[r.Track] = &row
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return st, fmt.Errorf("读 %s：%w", SchemaTable, err)
		}
	}

	st.Tracks = make([]TrackState, 0, len(tracks))
	for _, t := range tracks {
		ts := TrackState{Name: t.Name, Table: t.table(), Schema: schema[t.Name]}
		if t.FS == nil {
			// 没有迁移文件的轨（memory-index）：库里的版本只能从版本表读；没有行就按程序现在的号推断。
			ts.Applied = t.Version
			if ts.Schema != nil {
				ts.Applied = ts.Schema.Version
			}
			st.Tracks = append(st.Tracks, ts)
			continue
		}
		ts.HasGooseTable = names[ts.Table]
		if ts.HasGooseTable {
			var raw any
			if err := db.QueryRowContext(ctx, `SELECT coalesce(max(version_id), 0) FROM `+quoteIdent(ts.Table)+` WHERE is_applied`).Scan(&raw); err != nil {
				return st, fmt.Errorf("读迁移记录 %s：%w", ts.Table, err)
			}
			// 按 any 读再判整数，和 sumika_schema 的 integerColumn 走一套：文本版本号只说哪列不是整数，不回显值。
			applied, err := integerColumn(raw, "version_id")
			if err != nil {
				return st, fmt.Errorf("读迁移记录 %s：%w", ts.Table, err)
			}
			ts.Applied = applied
		}
		st.Tracks = append(st.Tracks, ts)
	}
	return st, nil
}

// openReadOnly：只读连接，带 query_only 第二道保险；单连接、等锁 5 秒，和 store 的只读打开一致。
func openReadOnly(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	db, err := sql.Open("sqlite", "file:"+p+"?"+q.Encode())
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

// openImmutable：mode=ro&immutable=1。换回后的验证用它：不拿锁、不建 -wal/-shm，刚换回的主文件是唯一权威。
func openImmutable(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("immutable", "1")
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

// uriPath 和 store / sqlitesnap 的写法一致：file: 地址里转义路径里的特殊字符，Windows 路径换正斜杠。
func uriPath(file string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
}

func tableNames(ctx context.Context, db *sql.DB) (map[string]bool, []string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, nil, fmt.Errorf("读 sqlite_master：%w", err)
	}
	defer rows.Close()
	all := map[string]bool{}
	var user []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, nil, err
		}
		all[name] = true
		if !strings.HasPrefix(name, "sqlite_") && name != SchemaTable {
			user = append(user, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(user)
	return all, user, nil
}

// integerColumn 把版本表里的整数列读成 int64。类型不对时只说哪一列不是整数，
// 不回显库里的值（规格第 3 条 3c）：探查失败的错误会进日志和自检。
func integerColumn(v any, column string) (int64, error) {
	if n, ok := v.(int64); ok {
		return n, nil
	}
	return 0, fmt.Errorf("版本表的 %s 不是整数", column)
}

// quoteIdent 把表名写成 SQLite 的带引号标识符。表名是程序里的常量，这里只防将来有人传进奇怪字符。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
