// Package store 打开 Go 的聊天库和运行记录库（规格-Go后台 四点五；做法照 Crush：纯 Go 的 modernc.org/sqlite、迁移嵌进程序用 goose）。
//
// 三种打开方式：
//   - OpenReadOnly：Bun 后台管着的真实库，只读（mode=ro），只检查表和列是不是认识的，绝不补表补列；
//   - OpenFresh：Go 自己建的库（测试的临时库、全新安装），用 migrations/ 里的迁移建表：0001 是 Bun 版各模块建的表的镜像，
//     0002 起是 Go 自己加的表（只加表、不改 0001 里的旧表）；
//   - OpenGo：分平台的一套库（ADR-199）：每个平台一个 go/chat/<平台>.db，跑 migrations/chat 加上平台自己的迁移目录；
//     运行记录 go/trace.db 共用一个。
//
// PRAGMA 照 daemon/util/db.ts：WAL、journal_size_limit 4 MB、busy_timeout 5000、foreign_keys，全新文件先设 auto_vacuum = INCREMENTAL。
package store

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
	"sync"
	"sync/atomic"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"sumika-native/internal/paths"
)

//go:embed migrations
var migrations embed.FS

// mirrorVersion 是"Bun 版的镜像"那份迁移（0001_baseline.sql）的版本号。CheckSchema 拿它当基线：Bun 管着的真实库只有这一份里的表，
// 0002 起 Go 自己加的表（channel_msgs……）真实库里没有，不能要求有（要求了，对着真实库的 core-check 就永远报缺表）
const mirrorVersion = 1

// Store 是一个聊天库和一个运行记录库
type Store struct {
	Events *sql.DB
	Trace  *sql.DB

	// shared 表示这是一组库里的一员（OpenGo 开的）：几个成员共用同一个 trace，平台库也在 Set 手上，
	// 所以这种 Store 的 Close 什么都不关，只能由 Set.Close 关
	shared bool
	// outcomeOwner 指向共用 trace 的结局屏障；独立库用自己。
	outcomeOwner   *Store
	outcomeMu      sync.Mutex
	outcomesSealed bool
	changeMu       sync.Mutex
	changeSubs     map[*ChangeSub]bool
	// readOnly：别的进程写这个库，本进程只读打开（foreign.go）；extMu 以下是 data_version 轮询的上次读数
	readOnly   bool
	extMu      sync.Mutex
	extSeen    bool
	extVersion int64
	// foreignChannels（只有共用的 trace 那一份有）：由别的进程管的渠道，启动收拾不碰它们的在途行
	foreignChannels []string
}

// uriPath 把文件路径转成 SQLite URI 里的路径部分：% # ? 必须转义。
// 不转义时目录名带 # 会被 SQLite 当成 URI 片段截断，后面的 mode=ro 一起丢掉，
// 而 modernc 打开时总带着"可写、不存在就建"（conn.go newConn），只读就全靠 mode=ro
// URIPath 把文件路径写成 modernc 认的 file: 地址里的路径：换成正斜杠，转义 % # ?
func URIPath(file string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
}

// testFastSync 为真时，之后打开的库不再每次提交都等磁盘落盘（synchronous=OFF）。只给测试用，见 UseFastSyncForTests。
var testFastSync atomic.Bool

// UseFastSyncForTests 让这个进程之后打开的库用 synchronous=OFF，只给测试包的 init 调用，生产代码不调。
// 原因：每条测试都新建库、跑一遍迁移、再按条提交，默认的 FULL 每次提交都要等一次磁盘，
// 在忙的机器上一个测试能花十几秒；测试库是临时目录，断电丢数据无所谓，提交的可见性和回滚语义不变。
func UseFastSyncForTests() { testFastSync.Store(true) }

func dsn(file string, readOnly bool) string {
	q := url.Values{}
	if testFastSync.Load() && !readOnly {
		q.Add("_pragma", "synchronous(OFF)")
	}
	if readOnly {
		q.Set("mode", "ro")
		// 第二道保险：万一 mode=ro 没生效，这条连接也拒绝写
		q.Add("_pragma", "query_only(1)")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// modernc 认 file: 开头的地址；Windows 路径要换成正斜杠
	return "file:" + URIPath(file) + "?" + q.Encode()
}

func open(file string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(file, readOnly))
	if err != nil {
		return nil, err
	}
	// 一个库一个连接：SQLite 写只能一个一个来；读多了以后再按需要放开（Crush 也是一个连接，见拆解笔记一·5）
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%w", file, err)
	}
	return db, nil
}

// OpenReadOnlyDB 只读打开一个 SQLite 库，不检查表结构；给只取部分表的启动快照用。
// 路径转义、mode=ro 和 query_only 与 OpenReadOnly 保持一致。
func OpenReadOnlyDB(file string) (*sql.DB, error) {
	return open(file, true)
}

// OpenReadOnly 只读打开 Bun 管着的真实库（所有平台一个 events.db），并检查表结构认不认识：对拍、导入用
func OpenReadOnly(p paths.Paths) (*Store, error) {
	s := &Store{}
	var err error
	for _, f := range []string{p.BunEventsDB(), p.BunTraceDB()} {
		if _, e := os.Stat(f); e != nil {
			return nil, fmt.Errorf("没有 %s：%w", f, e)
		}
	}
	if s.Events, err = open(p.BunEventsDB(), true); err != nil {
		return nil, err
	}
	if s.Trace, err = open(p.BunTraceDB(), true); err != nil {
		s.Events.Close()
		return nil, err
	}
	return s, nil
}

// OpenFresh 打开（没有就建）一对单库（shared/events.db、shared/trace.db，跑 Bun 镜像那一套迁移）。
// 只给测试和临时目录用：它写的位置就是 Bun 后台的库，生产一律走 OpenGo（ADR-199，规格-聊天记录按平台分库 第五节第 5 步）。
// 再拦一道：那里已经有一个不是 Go 建的库（没有 goose 的版本表，就是 Bun 的）就拒绝打开，免得在真数据目录上写 Bun 的库
func OpenFresh(p paths.Paths) (*Store, error) {
	for _, f := range []string{p.BunEventsDB(), p.BunTraceDB()} {
		if err := refuseBunDB(f); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(p.SharedDir(), 0o755); err != nil {
		return nil, err
	}
	s := &Store{}
	var err error
	if s.Events, err = openGo(p.BunEventsDB(), "migrations/events", ""); err != nil {
		return nil, err
	}
	if s.Trace, err = openGo(p.BunTraceDB(), "migrations/trace", ""); err != nil {
		s.Events.Close()
		return nil, err
	}
	return s, nil
}

// refuseBunDB：file 已经存在、又没有 goose 的版本表（Go 建的库都有），就是 Bun 后台的库，返回错误。
// 不存在、或者是 Go 自己建过的（测试里关了再开）都放行。只读打开看一眼，不改它
func refuseBunDB(file string) error {
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	db, err := open(file, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s 是 Bun 后台的库（没有 Go 的迁移记录）：OpenFresh 只给测试和临时目录用，生产用 OpenGo", file)
	}
	return nil
}

// openGo 打开（没有就建）一个 Go 自己的库，设 PRAGMA，跑 dir 里的迁移；table 不是空时用这个版本表（平台自己的迁移用，
// 和通用迁移的版本分开记，编号不会撞）
func openGo(file, dir, table string) (*sql.DB, error) {
	_, statErr := os.Stat(file)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if fresh {
		// 测试程序里：同一套迁移只跑一次，之后新建的库直接拷已经迁移好的那份（fresh_template_tests.go）
		if tpl := migratedTemplate(dir, table); tpl != nil {
			if err := os.WriteFile(file, tpl, 0o644); err != nil {
				return nil, err
			}
			fresh = false
			db, err := open(file, false)
			if err != nil {
				return nil, err
			}
			for _, q := range []string{"PRAGMA journal_mode = WAL", "PRAGMA journal_size_limit = 4194304"} {
				if _, err := db.Exec(q); err != nil {
					db.Close()
					return nil, err
				}
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
	for _, q := range []string{"PRAGMA journal_mode = WAL", "PRAGMA journal_size_limit = 4194304"} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrateWith(db, dir, 0, table); err != nil {
		db.Close()
		return nil, fmt.Errorf("迁移 %s：%w", file, err)
	}
	if fresh && testFastSync.Load() {
		saveMigratedTemplate(db, file, dir, table)
	}
	return db, nil
}

// migrate 跑 dir 里的迁移；upTo 大于 0 时只跑到这个版本（Baseline 用），0 是全部
func migrate(db *sql.DB, dir string, upTo int64) error {
	return migrateWith(db, dir, upTo, "")
}

// migrateWith 和 migrate 一样，table 不是空时把版本记在那张表里（goose 默认是 goose_db_version）
func migrateWith(db *sql.DB, dir string, upTo int64, table string) error {
	sub, err := fs.Sub(migrations, dir)
	if err != nil {
		return err
	}
	opts := []goose.ProviderOption{}
	if table != "" {
		opts = append(opts, goose.WithTableName(table))
	}
	pv, err := goose.NewProvider(goose.DialectSQLite3, db, sub, opts...)
	if err != nil {
		return err
	}
	if upTo > 0 {
		_, err = pv.UpTo(context.Background(), upTo)
	} else {
		_, err = pv.Up(context.Background())
	}
	return err
}

// Close 关掉两个库；一组库里的一员（OpenGo 开的）什么都不关：共用的 trace 只能由 Set.Close 关
func (s *Store) Close() {
	if s.shared {
		return
	}
	s.outcomeMu.Lock()
	s.outcomesSealed = true
	s.outcomeMu.Unlock()
	if s.Events != nil {
		s.Events.Close()
	}
	if s.Trace != nil {
		s.Trace.Close()
	}
}

// Columns 是一个库里每张表的列名（按表名、列名排好）
type Columns map[string][]string

// ColumnsOf 读一个库的表和列（不含 sqlite_ 开头的和 goose 自己的表）
func ColumnsOf(db *sql.DB) (Columns, error) {
	rows, err := db.Query("SELECT name, coalesce(sql, '') FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'")
	if err != nil {
		return nil, err
	}
	var tables []string
	virtual := map[string]bool{}
	for rows.Next() {
		var n, ddl string
		if err := rows.Scan(&n, &ddl); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
		virtual[n] = strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ddl)), "CREATE VIRTUAL TABLE")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := Columns{}
	for _, t := range tables {
		cr, err := db.Query(`SELECT name FROM pragma_table_info(?)`, t)
		if err != nil && virtual[t] {
			// 本驱动没有它的模块（Bun 记忆库的 vec0）：连列都读不了，只记"有这张表"，不当整库读不了
			out[t] = []string{}
			continue
		}
		if err != nil {
			return nil, err
		}
		for cr.Next() {
			var c string
			if err := cr.Scan(&c); err != nil {
				cr.Close()
				return nil, err
			}
			out[t] = append(out[t], c)
		}
		cr.Close()
		if err := cr.Err(); err != nil && virtual[t] {
			out[t] = []string{}
			continue
		} else if err != nil {
			return nil, err
		}
		sort.Strings(out[t])
	}
	return out, nil
}

// Missing 列出 want 里有、got 里没有的表和列（"表" 或 "表.列"）
func Missing(want, got Columns) []string {
	var out []string
	for t, cols := range want {
		have, ok := got[t]
		if !ok {
			out = append(out, t)
			continue
		}
		set := map[string]bool{}
		for _, c := range have {
			set[c] = true
		}
		for _, c := range cols {
			if !set[c] {
				out = append(out, t+"."+c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Baseline 是基线迁移（只到 mirrorVersion，即 Bun 版的镜像）建出来的表和列（在内存里建一份量出来），用来检查真实库认不认识。
// Go 自己后加的表不在里面
func Baseline() (events, trace Columns, err error) {
	for _, x := range []struct {
		dir string
		out *Columns
	}{{"migrations/events", &events}, {"migrations/trace", &trace}} {
		db, e := sql.Open("sqlite", "file::memory:")
		if e != nil {
			return nil, nil, e
		}
		db.SetMaxOpenConns(1)
		if e := migrate(db, x.dir, mirrorVersion); e != nil {
			db.Close()
			return nil, nil, e
		}
		c, e := ColumnsOf(db)
		db.Close()
		if e != nil {
			return nil, nil, e
		}
		*x.out = c
	}
	return events, trace, nil
}

// CheckSchema：真实库里缺了基线里的哪些表和列（缺了就说明 Go 版和这个库对不上，别往下走）。
// 真实库比基线多出来的不算错：那是 Bun 版以后加的，Go 版还没跟上，读的时候用不到
func (s *Store) CheckSchema() error {
	wantEv, wantTr, err := Baseline()
	if err != nil {
		return err
	}
	gotEv, err := ColumnsOf(s.Events)
	if err != nil {
		return err
	}
	gotTr, err := ColumnsOf(s.Trace)
	if err != nil {
		return err
	}
	var miss []string
	for _, m := range Missing(wantEv, gotEv) {
		miss = append(miss, "events."+m)
	}
	for _, m := range Missing(wantTr, gotTr) {
		miss = append(miss, "trace."+m)
	}
	if len(miss) > 0 {
		return fmt.Errorf("库里缺了 %d 处 Go 版要用的表或列：%s", len(miss), strings.Join(miss, "、"))
	}
	return nil
}

// Counts 是对拍用的几个数
type Counts struct {
	Events, Messages, Conversations, ModelRuns, Steps int
	PerConversation                                   map[string]int
}

// Count 数一遍（规格-Go后台 四点五 第 1 条：和 Bun 版各数一遍，要一样）。
// 每个库的几个数放在同一个读事务里，取自同一刻（Bun 后台开着时也不会前后对不上）
func (s *Store) Count() (Counts, error) {
	c := Counts{PerConversation: map[string]int{}}
	ctx := context.Background()
	ev, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return c, err
	}
	defer ev.Rollback()
	tr, err := s.Trace.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return c, err
	}
	defer tr.Rollback()
	for _, x := range []struct {
		tx  *sql.Tx
		q   string
		out *int
	}{
		{ev, "SELECT count(*) FROM events", &c.Events},
		{ev, "SELECT count(*) FROM events WHERE kind = 'message'", &c.Messages},
		{ev, "SELECT count(DISTINCT conversation) FROM events", &c.Conversations},
		{tr, "SELECT count(*) FROM model_runs", &c.ModelRuns},
		{tr, "SELECT count(*) FROM steps", &c.Steps},
	} {
		if err := x.tx.QueryRow(x.q).Scan(x.out); err != nil {
			return c, err
		}
	}
	rows, err := ev.Query("SELECT conversation, count(*) FROM events WHERE kind = 'message' GROUP BY conversation")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return c, err
		}
		c.PerConversation[k] = n
	}
	return c, rows.Err()
}
