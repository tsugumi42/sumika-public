package memview

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/memory"
	"sumika-native/internal/memory/memtest"
)

// 记忆页读服务的测试夹具：临时目录里编的记忆库（Bun 形状的旧库由 memtest 造，再跑真的迁移）和编的事件来源。
// 名字、号码、会话全是编的短串；绝不碰真实的 data/（规格-记忆页 第八节）

var now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// db 是一个测试用的记忆库：file 是路径，conn 是造数据用的读写连接
type db struct {
	t    *testing.T
	file string
	conn *sql.DB
}

func newDB(t *testing.T, migrate bool) *db {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	memtest.BunShaped(t, file)
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if migrate {
		if err := memory.Migrate(context.Background(), file); err != nil {
			t.Fatal(err)
		}
	}
	d := &db{t: t, file: file, conn: conn}
	t.Cleanup(func() { conn.Close() })
	return d
}

func (d *db) fact(id, status, text, subject, conv, scope, by string, created int64) {
	d.t.Helper()
	if _, err := d.conn.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, origin, conversation, scope, speech_act)
		VALUES (?, 'fact', ?, ?, 'owner', '[]', ?, ?, 'test', ?, ?, 'assert')`, id, text, nullable(subject), status, created, nullable(conv), scope); err != nil {
		d.t.Fatal(err)
	}
	if by != "" {
		d.conn.Exec(`UPDATE facts SET confirmed_by = ? WHERE id = ?`, by, id)
	}
	d.conn.Exec(`DELETE FROM item_scopes WHERE ref = ?`, "f:"+id)
	d.conn.Exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`, "f:"+id, scope)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// events 是编的事件来源：按（会话, seq）给事件；fail 非空时所有读都报错
type events struct {
	mu   sync.Mutex
	evs  []memory.Event
	fail error
	asks []string
}

func (e *events) Events(ctx context.Context, conv string, afterSeq int64, limit int) ([]memory.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asks = append(e.asks, fmt.Sprintf("%s>%d", conv, afterSeq))
	if e.fail != nil {
		return nil, e.fail
	}
	var out []memory.Event
	for _, ev := range e.evs {
		if ev.Conversation == conv && ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (e *events) Revoked(context.Context, string, []int64) (map[int64]bool, error) { return nil, nil }
func (e *events) LastSaid(context.Context, string) (memory.Event, bool, error) {
	return memory.Event{}, false, nil
}
func (e *events) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return nil, nil
}
func (e *events) Conversations(context.Context) ([]memory.ConvHead, error) { return nil, nil }

func newService(t *testing.T, chars []Character, opt Options) *Service {
	t.Helper()
	if opt.Now == nil {
		opt.Now = func() time.Time { return now }
	}
	s := New(chars, opt)
	t.Cleanup(s.Close)
	return s
}

func opts() memory.BrowserOptions {
	return memory.BrowserOptions{Now: func() time.Time { return now }}
}
