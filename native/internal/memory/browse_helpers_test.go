package memory

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// 记忆页只读查询的测试夹具：全在 t.TempDir() 里编，名字、号码、会话都是编的短串；绝不碰真实的 data/（规格-记忆页 第八节）

var browseNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// bf 是一条要放进库的事实；没写的字段用默认（有效、事实、范围 owner、创建时间 browseNow 前一小时）
type bf struct {
	id, kind, status, text, subject, conv, scope string
	scopes                                       []string // 范围组（迁移过的库才写 item_scopes）；空 = 只放 scope
	by                                           any      // confirmed_by：nil = NULL（导入的）
	hasBy                                        bool     // 是否写 confirmed_by（迁移过的库才有这一列）
	created                                      int64
	learned                                      int64
	seqs                                         string
	temporal                                     string
	validTo                                      any
	superseded                                   string
}

type bfx struct {
	t        *testing.T
	db       *sql.DB
	tx       *sql.Tx // bulk 里有值：放行的语句都走这个事务，一次提交
	file     string
	migrated bool
}

// newBrowseDB 造一个库：migrated 为假是 Bun 版没迁移的样子
func newBrowseDB(t *testing.T, migrated bool) *bfx {
	t.Helper()
	var file string
	if migrated {
		file = migratedDB(t, nil)
	} else {
		file = newBunDB(t, nil)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return &bfx{t: t, db: db, file: file, migrated: migrated}
}

// exec 执行一条放行用的语句：在 bulk 里走事务，不在就直接提交
func (x *bfx) exec(q string, args ...any) (sql.Result, error) {
	if x.tx != nil {
		return x.tx.Exec(q, args...)
	}
	return x.db.Exec(q, args...)
}

// bulk 把 fn 里放行的一大批语句包进一个事务（每条单独提交要创建删除一次日志文件，几百条就是几秒）
func (x *bfx) bulk(fn func()) {
	x.t.Helper()
	tx, err := x.db.Begin()
	if err != nil {
		x.t.Fatal(err)
	}
	x.tx = tx
	defer func() { x.tx = nil }()
	fn()
	if err := tx.Commit(); err != nil {
		x.t.Fatal(err)
	}
}

func (x *bfx) put(f bf) {
	x.t.Helper()
	if f.kind == "" {
		f.kind = "fact"
	}
	if f.status == "" {
		f.status = "active"
	}
	if f.text == "" {
		f.text = "编造的一条 " + f.id
	}
	if f.scope == "" {
		f.scope = "owner"
	}
	if f.created == 0 {
		f.created = browseNow.Add(-time.Hour).UnixMilli()
	}
	if f.seqs == "" {
		f.seqs = "[]"
	}
	if _, err := x.exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, superseded_by, created_at, origin, conversation, scope,
		learned_at, valid_to, temporal, speech_act) VALUES (?, ?, ?, ?, 'owner', ?, ?, ?, ?, 'test', ?, ?, ?, ?, ?, 'assert')`,
		f.id, f.kind, f.text, nullableText(f.subject), f.seqs, f.status, nullableText(f.superseded), f.created, nullableText(f.conv), f.scope,
		f.learned, f.validTo, nullableText(f.temporal)); err != nil {
		x.t.Fatalf("放事实 %s：%v", f.id, err)
	}
	if x.migrated {
		if f.hasBy {
			if _, err := x.exec(`UPDATE facts SET confirmed_by = ? WHERE id = ?`, f.by, f.id); err != nil {
				x.t.Fatal(err)
			}
		}
		scopes := f.scopes
		if len(scopes) == 0 {
			scopes = []string{f.scope}
		}
		// 迁移时给每条事实都灌过一行 item_scopes；这里放进去的新事实照样要有，否则就是"迁移后 Bun 又写的"那种
		x.exec(`DELETE FROM item_scopes WHERE ref = ?`, "f:"+f.id)
		for _, s := range scopes {
			if _, err := x.exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "f:"+f.id, s); err != nil {
				x.t.Fatal(err)
			}
		}
	}
}

// byOwner、byAI 是 bf 里写 confirmed_by 的简写
func byOwner(f bf) bf { f.by, f.hasBy = "owner", true; return f }
func byAI(f bf) bf    { f.by, f.hasBy = "ai", true; return f }

// episode 放一个片段
func (x *bfx) episode(id, conv, text, scope, status string, created int64, from, to int64) {
	x.t.Helper()
	if created == 0 {
		created = browseNow.Add(-time.Hour).UnixMilli()
	}
	if _, err := x.exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope, origin, status) VALUES (?, ?, ?, 'owner', ?, ?, ?, ?, 'test', ?)`,
		id, conv, text, from, to, created, scope, status); err != nil {
		x.t.Fatalf("放片段 %s：%v", id, err)
	}
	if x.migrated {
		x.exec(`DELETE FROM item_scopes WHERE ref = ?`, "e:"+id)
		if _, err := x.exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "e:"+id, scope); err != nil {
			x.t.Fatal(err)
		}
	}
}

func (x *bfx) evidence(fact, day, conv string, seq int64, speaker string, by int, at int64) {
	x.t.Helper()
	if _, err := x.exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, verdict, at) VALUES (?, ?, ?, ?, ?, ?, 'ok', ?)`,
		fact, day, conv, seq, nullableText(speaker), by, at); err != nil {
		x.t.Fatal(err)
	}
}

func (x *bfx) revision(fact, op string, at int64, by, reason, before, after string) {
	x.t.Helper()
	var err error
	if x.migrated {
		_, err = x.exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason, before_json, after_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fact, op, at, by, nullableText(reason), nullableText(before), nullableText(after))
	} else {
		_, err = x.exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES (?, ?, ?, ?, ?)`, fact, op, at, by, nullableText(reason))
	}
	if err != nil {
		x.t.Fatal(err)
	}
}

func (x *bfx) browser(opt BrowserOptions) *Browser {
	x.t.Helper()
	if opt.Now == nil {
		opt.Now = func() time.Time { return browseNow }
	}
	b, err := OpenBrowser(x.file, opt)
	if err != nil {
		x.t.Fatal(err)
	}
	x.t.Cleanup(func() { b.Close() })
	return b
}

// browseIDs 取一页的 ref（去掉前缀），断言里好比
func browseIDs(rows []BrowseRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = refID(r.Ref)
	}
	return out
}

func idsFmt(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("f%03d", i)
	}
	return out
}
