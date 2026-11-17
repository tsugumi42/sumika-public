package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed"
)

// newBunDB 在临时目录里造一个 Bun 版样子的 memory.db，seed 往里放几行；返回文件路径
func newBunDB(t *testing.T, seed func(db *sql.DB)) string {
	t.Helper()
	buildDBTemplates(t)
	file := filepath.Join(t.TempDir(), "memory.db")
	if tplBunDB != nil {
		if err := os.WriteFile(file, tplBunDB, 0o644); err != nil {
			t.Fatal(err)
		}
		if seed != nil {
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			seed(db)
		}
		return file
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(BaseSchema); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(db)
	}
	return file
}

// migratedDB：Bun 样子的库，已经迁移过写入侧的表，旁边的 memory-v2.db 也是建好的空表。
// 没有 seed 时整个直接拷模板；有 seed 时先拷表结构、放行、再迁移（旁边的文件同样预先放好）
func migratedDB(t *testing.T, seed func(db *sql.DB)) string {
	t.Helper()
	buildDBTemplates(t)
	if seed == nil && tplMigratedDB != nil {
		file := filepath.Join(t.TempDir(), "memory.db")
		if err := os.WriteFile(file, tplMigratedDB, 0o644); err != nil {
			t.Fatal(err)
		}
		placeSidecar(t, file)
		return file
	}
	file := newBunDB(t, seed)
	if err := Migrate(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	placeSidecar(t, file)
	return file
}

// placeSidecar 在库旁边放一份建好表的 memory-v2.db，省掉 Open 时现建
func placeSidecar(t *testing.T, file string) {
	t.Helper()
	if tplSidecarDB == nil {
		return
	}
	if err := os.WriteFile(SidecarPath(file), tplSidecarDB, 0o644); err != nil {
		t.Fatal(err)
	}
}

// logSink 收日志，测试里查"日志里没有原文"
type logSink struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.text, format+"\n", args...)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// openW 以可写方式打开一个迁移过的库副本。embed 可以是 nil。返回的 Memory 在测试结束时关掉
func openW(t *testing.T, file string, e embed.Embedder) (*Memory, *logSink) {
	t.Helper()
	sink := &logSink{}
	m, err := Open(context.Background(), file, e, nil, Options{Writable: true, Character: "suzune", Logf: sink.logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, sink
}

// newWritable：造一个迁移过的空库并以可写方式打开
func newWritable(t *testing.T, e embed.Embedder) (*Memory, *logSink) {
	t.Helper()
	return openW(t, migratedDB(t, nil), e)
}

// 数一个查询的行数 / 取一个值
func count(t *testing.T, m *Memory, query string, args ...any) (n int) {
	t.Helper()
	if err := m.w.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s：%v", query, err)
	}
	return n
}

func str(t *testing.T, m *Memory, query string, args ...any) (s string) {
	t.Helper()
	var v sql.NullString
	if err := m.w.db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s：%v", query, err)
	}
	return v.String
}

// factStatus：按正文找一条事实的状态（正文在一个库里不重复时用）
func factStatus(t *testing.T, m *Memory, text string) string {
	t.Helper()
	return str(t, m, `SELECT status FROM facts WHERE text = ?`, text)
}

func idOf(ref string) string { return strings.TrimPrefix(strings.TrimPrefix(ref, "f:"), "e:") }

// 常用的几个 Op
func ownerFact(text string) FactOp {
	return FactOp{Kind: "fact", Text: text, Subject: "owner", Status: "active", ConfirmedBy: "owner", Scopes: []string{"owner"}}
}

func candidate(text, subject string, scopes ...string) FactOp {
	return FactOp{Kind: "fact", Text: text, Subject: subject, Status: "candidate", Scopes: scopes}
}

func mustApply(t *testing.T, m *Memory, b Batch) Applied {
	t.Helper()
	res, err := m.apply(context.Background(), b)
	if err != nil {
		t.Fatalf("apply：%v", err)
	}
	return res
}

// testLockWait：测试里等写锁的上限。等的是"别的写放锁"这个事件，上限只是防死锁的兜底，不是时间断言；
// 不用生产的 5 秒：几个写同时排队时机器一忙，排在后面的就等超了，报"记忆库正忙"（2026-11-14 合并队列红过）。
// 要测"等不到锁就放弃"的测试自己写 Wait
const testLockWait = 2 * time.Minute

func summary(ops ...Op) Batch { return Batch{By: BySummary, Ops: ops, Wait: testLockWait} }
func remember(ops ...Op) Batch {
	return Batch{By: ByRemember, Ops: ops, OnDup: DupReport, Wait: testLockWait}
}

// fileHash：一个文件整个的指纹，查"没有被改过"
func fileHash(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func jsonMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q 不是 JSON：%v", s, err)
	}
	return m
}

// scriptedEmbed 是可以控制的嵌入：能让它不可用、出错、卡住（卡住时认 ctx）
type scriptedEmbed struct {
	mu    sync.Mutex
	calls int
	down  bool
	fail  error
	gate  chan struct{} // 不是 nil 时 Embed 先等它关掉（或 ctx 取消）
	seen  chan struct{} // 不是 nil 时 Embed 一进来就往里发一个信号
}

func (e *scriptedEmbed) Model() string { return "scripted" }

func (e *scriptedEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	down, fail, gate, seen := e.down, e.fail, e.gate, e.seen
	e.mu.Unlock()
	if seen != nil {
		select {
		case seen <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if down {
		return nil, embed.ErrUnavailable
	}
	if fail != nil {
		return nil, fail
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len([]rune(t))), 1}
	}
	return out, nil
}

func (e *scriptedEmbed) set(f func(*scriptedEmbed)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

func (e *scriptedEmbed) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// waitVectors 等后台补向量的协程这一趟跑完
func waitVectors(t *testing.T, m *Memory) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.vecMu.Lock()
		idle := !m.vecRunning
		m.vecMu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("后台补向量 5 秒还没跑完")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
