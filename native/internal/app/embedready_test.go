package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// 嵌入比记忆晚就绪（openMemories 紧跟在 openEmbedding 后面，嵌入子进程要十几秒）：
// 就绪的回调来了以后，每个已经打开的角色记忆都要把向量补齐，召回才走得到向量那一路（只读记忆也一样）

// readyLocal 是线程安全的假本机嵌入：向量按"意思"给方向，查询和条目没有共用的字，召回只能靠向量找到。
// 状态切换像真的那样走 OnStatus；broken 为真时状态是就绪、算的时候却不可用（刚好又挂了）
type readyLocal struct {
	mu       sync.Mutex
	cfg      embed.LocalConfig
	status   embed.Status
	broken   bool
	flipOnce bool // 第一次被问状态时先就绪（并通知），但答的还是"启动中"：就绪赶在 Open 看过状态之后、记忆挂上之前
	calls    int
}

var readyGroups = [][]string{{"电池", "充电"}, {"相机", "拍照"}}

func (e *readyLocal) Start()        { e.setStatus(embed.StatusStarting) }
func (e *readyLocal) Stop()         {}
func (e *readyLocal) Model() string { return e.cfg.Model }

func (e *readyLocal) Status() (embed.Status, string) {
	e.mu.Lock()
	flip := e.flipOnce
	e.flipOnce = false
	answer := e.status
	e.mu.Unlock()
	if flip {
		e.setStatus(embed.StatusReady) // 回调在这里同步跑完，记忆那会儿还没挂上
	}
	return answer, ""
}

func (e *readyLocal) MissingModel() *embed.MissingModel { return nil }

func (e *readyLocal) setStatus(s embed.Status) {
	e.mu.Lock()
	e.status = s
	on := e.cfg.OnStatus
	e.mu.Unlock()
	if on != nil {
		on(s)
	}
}

func (e *readyLocal) set(f func(*readyLocal)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

func (e *readyLocal) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	ok := e.status == embed.StatusReady && !e.broken
	e.mu.Unlock()
	if !ok {
		return nil, embed.ErrUnavailable
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(readyGroups)+1)
		v[len(readyGroups)] = 0.3
		for g, words := range readyGroups {
			for _, w := range words {
				if strings.Contains(t, w) {
					v[g] = 1
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

// secondCharacter 再造一个角色：拷一份卡、放一个自己的记忆库（临时目录里，不碰真实数据）
func secondCharacter(t *testing.T, e *testEnv, id, fact string) string {
	t.Helper()
	srcCard := filepath.Join(e.data, "characters", testChar, "card")
	dstCard := filepath.Join(e.data, "characters", id, "card")
	if err := os.MkdirAll(dstCard, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(srcCard, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstCard, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(e.data, "go", "characters", id, "memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建嵌入就绪检查要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO facts VALUES ('200', 'fact', 'active', ?, 'owner', 'owner', '', 1, 1, NULL, NULL)`, fact); err != nil {
		t.Fatal(err)
	}
	return file
}

var ownerAudience = memory.Audience{Channel: "console", Conversation: "console:a", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}

// vectorHit：用和条目没有共用字的话问一次，看有没有由向量那一路找到的条目
func vectorHit(a *App, id, query string) bool {
	m := a.memories[id]
	for _, h := range m.Recall(context.Background(), ownerAudience, memory.Query{Text: query, Mode: memory.ModeExplicit}).Hits {
		if h.Vector > 0 && h.Lexical == 0 {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("5 秒内没等到：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const (
	batteryQuery = "充电的东西放哪了" // 对应 example 角色记忆里"备用电池收在蓝柜底层"
	cameraQuery  = "拍照的设备呢"   // 对应第二个角色记忆里"相机收在红箱里"
)

func openWithEmbed(t *testing.T, e *testEnv, local **readyLocal, setup func(*readyLocal)) *App {
	t.Helper()
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		l := &readyLocal{cfg: cfg, status: embed.StatusOff}
		if setup != nil {
			setup(l)
		}
		*local = l
		return l
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// 晚就绪：打开时没有向量；嵌入就绪的回调来了以后，两个角色的记忆各自补齐，事实库一个字节都不动
func TestEmbedReadyFillsEveryCharactersVectors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	mem1 := guardFixture(t, e, true)
	mem2 := secondCharacter(t, e, "other", "主人把相机收在红箱里")
	before1, before2 := fileBytes(t, mem1), fileBytes(t, mem2)

	var local *readyLocal
	a := openWithEmbed(t, e, &local, nil)
	if len(a.memories) != 2 {
		t.Fatalf("该打开两个角色的记忆：%d", len(a.memories))
	}
	if vectorHit(a, testChar, batteryQuery) || vectorHit(a, "other", cameraQuery) {
		t.Fatal("嵌入还没就绪时不该有向量那一路")
	}

	local.setStatus(embed.StatusReady) // 像嵌入子进程起好了：OnStatus 回调
	waitUntil(t, "两个角色的记忆都补齐了向量", func() bool {
		return vectorHit(a, testChar, batteryQuery) && vectorHit(a, "other", cameraQuery)
	})
	if string(fileBytes(t, mem1)) != string(before1) || string(fileBytes(t, mem2)) != string(before2) {
		t.Fatal("补向量动了事实库（memory.db）")
	}
	for _, id := range []string{testChar, "other"} {
		if _, _, err := a.memories[id].SummaryState(context.Background(), "console:a"); err == nil {
			t.Fatalf("角色 %s 的记忆应该还是只读", id)
		}
	}
	e.logMu.Lock()
	defer e.logMu.Unlock()
	filled := 0
	for _, l := range e.logs {
		if strings.Contains(l, "向量补齐 1 条") {
			filled++
		}
		if strings.Contains(l, "电池") || strings.Contains(l, "相机") {
			t.Fatalf("日志里不该有记忆原文：%q", l)
		}
	}
	if filled != 2 {
		t.Fatalf("两个角色各记一笔补了几条：%d 笔", filled)
	}
}

// 就绪赶在 Open 看过状态之后、记忆挂上之前：回调那会儿还没有记忆可通知，挂上以后要自己再看一眼状态
func TestEmbedReadyBetweenOpenAndRegistration(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	guardFixture(t, e, true)
	var local *readyLocal
	a := openWithEmbed(t, e, &local, func(l *readyLocal) { l.flipOnce = true })
	waitUntil(t, "挂上以后补齐了向量", func() bool { return vectorHit(a, testChar, batteryQuery) })
}

// 就绪了但算的时候又不可用（降级），挂了再恢复：恢复的那次回调要再补一次
func TestEmbedFlapRefillsVectors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	guardFixture(t, e, true)
	var local *readyLocal
	a := openWithEmbed(t, e, &local, nil)

	local.set(func(l *readyLocal) { l.broken = true })
	local.setStatus(embed.StatusReady) // 就绪，但算不了
	waitUntil(t, "没算上，记忆标降级", func() bool { return a.memories[testChar].EmbeddingDegraded() })
	if vectorHit(a, testChar, batteryQuery) {
		t.Fatal("没算上不该有向量那一路")
	}

	local.setStatus(embed.StatusDown)
	local.set(func(l *readyLocal) { l.broken = false })
	local.setStatus(embed.StatusReady) // 恢复
	waitUntil(t, "恢复以后补齐了向量", func() bool { return vectorHit(a, testChar, batteryQuery) })
	waitUntil(t, "补齐以后清掉降级", func() bool { return !a.memories[testChar].EmbeddingDegraded() })
}

func fileBytes(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
