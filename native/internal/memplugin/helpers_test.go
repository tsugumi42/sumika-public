package memplugin

// 测试用的小东西：临时目录里造一个 Bun 样子的 memory.db、按关键词给向量的假嵌入、收日志的小盒子。
// 名字、群号、事实都是编的，绝不碰真实的 data/

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
	"sumika-native/internal/turn"
)

// 测试里用的两个群，和群里的一句话、主人的私事
const (
	groupA = "qq:group:30001"
	groupB = "qq:group:30002"
)

// 这一组记忆的正文。断言只认这些独一无二的句子，不认关键词（问话里自己带着关键词）
const (
	textGuitar   = "主人最近在学吉他，每天练半小时"
	textGame     = "群里说好周五一起开黑"
	textMove     = "群友小A说下周搬家" // 待确认
	textCat      = "群友小B家的猫很胖"
	pinnedPublic = "不在群里提主人的私事"
	pinnedOwner  = "主人考试前要提醒他早睡"
)

// seedRow：id, kind, status, text, subject, scope
type seedRow = [6]string

// standardFacts 是大多数测试共用的一组：主人的私事（owner）、一个群的事（含一条待确认）、公开的约定、主人才能看的约定
func standardFacts() []seedRow {
	return []seedRow{
		{"1", "fact", "active", textGuitar, "owner", "owner"},
		{"2", "fact", "active", textGame, "qq:60001", "conv:" + groupA},
		{"3", "promise", "active", pinnedPublic, "owner", "public"},
		{"4", "boundary", "active", pinnedOwner, "owner", "owner"},
		{"5", "fact", "active", textCat, "qq:60002", "conv:" + groupB},
		{"6", "fact", "candidate", textMove, "qq:60001", "conv:" + groupA},
	}
}

// bunDB 在临时目录里造一个只有 loadItems 读到的列的 memory.db（照 memory 包 recall_test.go 的 bunDb）；created 是所有条目的创建时间
func bunDB(t *testing.T, created time.Time, facts []seedRow) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建插件测试要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ms := created.UnixMilli()
	for i, f := range facts {
		// 创建时间错开一点，固定要点的先后（新的在前）有确定的次序
		if _, err := db.Exec(`INSERT INTO facts VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, NULL, NULL)`, f[0], f[1], f[2], f[3], f[4], f[5], ms-int64(i)*1000, ms-int64(i)*1000); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

// openMemory 打开一个角色的记忆（只读，没有写入侧）；e 为空就只用全文
func openMemory(t *testing.T, created time.Time, facts []seedRow, e *fakeEmbed) *memory.Memory {
	t.Helper()
	var emb embed.Embedder
	if e != nil {
		emb = e
	}
	m, err := memory.Open(context.Background(), bunDB(t, created, facts), emb, nil, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

// fakeEmbed 按关键词给向量，记下每次被问了哪几句；hold 不为空时每次调用先卡在它上面（模拟本机嵌入很慢）。
// 打开记忆时补向量也会走它，所以 reset 之后才记召回里的调用
type fakeEmbed struct {
	mu      sync.Mutex
	calls   [][]string
	entered chan struct{} // 每次进来发一个信号（缓冲的）
	hold    func(ctx context.Context) error
}

func (*fakeEmbed) Model() string { return "fake" }

func (e *fakeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls = append(e.calls, append([]string(nil), texts...))
	hold := e.hold
	e.mu.Unlock()
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		default:
		}
	}
	if hold != nil {
		if err := hold(ctx); err != nil {
			return nil, err
		}
	}
	keys := []string{"吉他", "搬家", "考试", "猫", "开黑"}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(keys)+1)
		v[len(keys)] = 0.3
		for k, key := range keys {
			if strings.Contains(t, key) {
				v[k] = 1
			}
		}
		out[i] = v
	}
	return out, nil
}

func (e *fakeEmbed) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = nil
	for e.entered != nil {
		select {
		case <-e.entered:
		default:
			return
		}
	}
}

func (e *fakeEmbed) asked() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.calls...)
}

func (e *fakeEmbed) setHold(h func(ctx context.Context) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hold = h
}

// logSink 收日志，查"日志里没有原文和会话 id"
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

// 几种听众，档都按装配处该有的做法用 ScopeKey 算
var (
	consoleAud = audience.Console("console:suzu")
	ownerInA   = audience.Audience{Channel: "qq", Conversation: groupA, Scene: "group", PersonIDs: []string{"owner"}, OwnerAsked: true}
	friendInA  = audience.Audience{Channel: "qq", Conversation: groupA, Scene: "group", PersonIDs: []string{"qq:60001"}}
)

// in 拼一次拼参考信息的入参；档按听众算（主窗口用 ConsoleScope）
func in(character string, a audience.Audience, text string, state map[string]any) turn.ContextIn {
	scope := memory.ScopeKey(a)
	if a.Channel == "console" {
		scope = turn.ConsoleScope
	}
	return turn.ContextIn{CharacterID: character, Conversation: a.Conversation, Scope: scope, Audience: a, Addressed: true, Text: text, State: state}
}

// only 让所有角色都用同一份记忆
func only(m *memory.Memory) Memories {
	return func(string) *memory.Memory { return m }
}

// newLife 建一个插件并开始一次生命周期（不经内核，直接调钩子）；测试收尾时停掉
func newLife(t *testing.T, mems Memories, mutate func(*Options)) (*life, *logSink) {
	t.Helper()
	logs := &logSink{}
	opt := Options{Memories: mems, Logf: logs.logf, Now: func() time.Time { return fixedNow }}
	if mutate != nil {
		mutate(&opt)
	}
	lf := New(opt).start()
	t.Cleanup(lf.stop)
	return lf, logs
}

// 测试里的"现在"：条目都建在这个时刻，所以时间标记是"今天"
var fixedNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func joined(lines []string) string { return strings.Join(lines, "\n") }

// count 是 sub 在 lines 里出现了几次
func count(lines []string, sub string) int { return strings.Count(joined(lines), sub) }
