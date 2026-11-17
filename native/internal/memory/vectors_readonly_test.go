package memory

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed"
)

// statusEmbed 是会说"我就绪了没有"的假嵌入（像本机嵌入子进程）：Open 看到没就绪就跳过启动时的向量同步。
// 向量按"意思"给方向（吉他和琴是一个方向），这样查询和条目没有共用的字，召回只能靠向量那一路找到
type statusEmbed struct {
	mu     sync.Mutex
	status embed.Status
	down   bool          // 状态说就绪了，但算的时候又不可用（刚好又挂了）
	gate   chan struct{} // 不是 nil 时 Embed 先等它关掉（或 ctx 取消）
	seen   chan struct{} // 不是 nil 时 Embed 一进来就往里发一个信号
}

var statusGroups = [][]string{{"吉他", "琴"}, {"搬家", "换房"}, {"考试", "测验"}, {"猫", "喵"}}

func (e *statusEmbed) Model() string { return "status-fake" }

func (e *statusEmbed) Status() (embed.Status, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, ""
}

func (e *statusEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	status, down, gate, seen := e.status, e.down, e.gate, e.seen
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
	if down || status != embed.StatusReady {
		return nil, embed.ErrUnavailable
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(statusGroups)+1)
		v[len(statusGroups)] = 0.3
		for g, words := range statusGroups {
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

func (e *statusEmbed) set(f func(*statusEmbed)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

// 只读库里三条有效事实；查询"练琴练得怎么样了"和第一条没有共用的字（全文那一路找不到，只有向量找得到）
func readOnlyFixture(t *testing.T) string {
	t.Helper()
	return bunDb(t, [][]string{
		{"1", "fact", "active", "主人最近在学吉他，每天练半小时", "owner", "owner"},
		{"2", "fact", "active", "主人下周要搬家", "owner", "owner"},
		{"3", "fact", "active", "主人考试前提醒他早睡", "owner", "owner"},
	}, nil)
}

const readOnlyQuery = "练琴练得怎么样了"

func openRO(t *testing.T, file string, e *statusEmbed) (*Memory, *logSink) {
	t.Helper()
	sink := &logSink{}
	m, err := Open(context.Background(), file, e, nil, Options{Character: "suzune", Logf: sink.logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, sink
}

func recallHere(m *Memory) []Hit {
	return m.Recall(context.Background(), ownerHere, Query{Text: readOnlyQuery, Mode: ModeExplicit}).Hits
}

// waitFilled 等派生索引里的向量数到 n（补齐在后台协程里，没有别的可等）
func waitFilled(t *testing.T, m *Memory, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(m.index.Vectors()) != n {
		if time.Now().After(deadline) {
			t.Fatalf("3 秒后向量还是 %d 条，应该是 %d 条", len(m.index.Vectors()), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// sidecarVectorRows 数旁边的文件（memory-v2.db）里真正落盘的向量行数，不看内存里的表
func sidecarVectorRows(t *testing.T, file string) int {
	t.Helper()
	x, err := OpenIndex(context.Background(), SidecarPath(file))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	var rows int
	if err := x.db.QueryRow(`SELECT count(*) FROM memory_vectors`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// 嵌入比记忆晚就绪：Open 时跳过向量同步，之后嵌入就绪、宿主通知一声，只读的记忆也要把向量补齐，召回才走得到向量那一路。
// 补的只是旁边的派生索引，事实库一个字节都不动
func TestReadOnlyFillsVectorsWhenEmbedBecomesReady(t *testing.T) {
	t.Parallel()
	file := readOnlyFixture(t)
	e := &statusEmbed{status: embed.StatusStarting}
	m, sink := openRO(t, file, e)
	before := fileHash(t, file)

	if n := len(m.index.Vectors()); n != 0 {
		t.Fatalf("嵌入还没就绪时不该有向量：%d", n)
	}
	if hits := recallHere(m); len(hits) != 0 {
		t.Fatalf("没有向量时，和条目没有共用字的查询不该找到东西：%+v", hits)
	}

	e.set(func(e *statusEmbed) { e.status = embed.StatusReady })
	m.EmbeddingReady()
	waitFilled(t, m, 3)
	waitVectors(t, m)

	if miss, _ := m.index.MissingVectors(m.Items()); miss != 0 || m.EmbeddingDegraded() {
		t.Fatalf("补齐以后不缺、不降级：缺 %d，降级 %v", miss, m.EmbeddingDegraded())
	}
	hits := recallHere(m)
	if len(hits) != 1 || hits[0].Ref != "f:1" || hits[0].Vector == 0 || hits[0].Lexical != 0 {
		t.Fatalf("补齐以后该由向量那一路找到吉他那条：%+v", hits)
	}
	if fileHash(t, file) != before {
		t.Fatal("补向量动了事实库（memory.db）")
	}
	if rows := sidecarVectorRows(t, file); rows != 3 {
		t.Fatalf("旁边的文件里该有 3 行向量：%d", rows)
	}
	log := sink.String()
	if !strings.Contains(log, "向量补齐 3 条（模型 status-fake）") {
		t.Fatalf("日志该记一笔补了几条：%q", log)
	}
	if strings.Contains(log, "吉他") || strings.Contains(log, "console:") {
		t.Fatalf("日志里不该有原文和会话 id：%q", log)
	}
}

// 状态说就绪了、算的时候又不可用（刚好又挂了）：补不上，降级；之后再就绪时必须补一次。
// 这一次通知不能被"降级期间不试"拦掉（那是给写入用的）
func TestReadOnlyRefillsAfterEmbedFlaps(t *testing.T) {
	t.Parallel()
	file := readOnlyFixture(t)
	e := &statusEmbed{status: embed.StatusReady, down: true}
	m, _ := openRO(t, file, e) // 打开时"就绪"但算不了：同步了一遍没算上，标降级
	if !m.EmbeddingDegraded() || len(m.index.Vectors()) != 0 {
		t.Fatalf("打开时就缺向量应该是降级、向量表空：降级 %v，%d 条", m.EmbeddingDegraded(), len(m.index.Vectors()))
	}

	m.EmbeddingReady() // 通知了，但还是不可用
	waitVectors(t, m)
	if !m.EmbeddingDegraded() || len(m.index.Vectors()) != 0 {
		t.Fatal("仍不可用：保持降级、向量表还是空")
	}

	e.set(func(e *statusEmbed) { e.down = false })
	m.EmbeddingReady() // 恢复了：降级中也要补
	waitFilled(t, m, 3)
	waitVectors(t, m)
	if m.EmbeddingDegraded() {
		t.Fatal("补齐以后该清掉降级")
	}
	if hits := recallHere(m); len(hits) != 1 || hits[0].Vector == 0 {
		t.Fatalf("补齐以后召回该走向量：%+v", hits)
	}

	// 又挂了再恢复：已有的向量都在，通知一声也不会把它们清掉
	e.set(func(e *statusEmbed) { e.down = true })
	m.EmbeddingReady()
	waitVectors(t, m)
	e.set(func(e *statusEmbed) { e.down = false })
	m.EmbeddingReady()
	waitVectors(t, m)
	if n := len(m.index.Vectors()); n != 3 || m.EmbeddingDegraded() {
		t.Fatalf("挂了再恢复以后向量还是 3 条、不降级：%d 条，降级 %v", n, m.EmbeddingDegraded())
	}
}

// Close 要把在途的补向量叫停、等它退出，之后通知也不再起协程；迟到的补向量一条都写不进旁边的文件
func TestReadOnlyCloseStopsFillInFlight(t *testing.T) {
	t.Parallel()
	file := readOnlyFixture(t)
	e := &statusEmbed{status: embed.StatusStarting, gate: make(chan struct{}), seen: make(chan struct{}, 1)}
	m, _ := openRO(t, file, e)
	e.set(func(e *statusEmbed) { e.status = embed.StatusReady })
	m.EmbeddingReady()
	select { // 补向量已经卡在嵌入上（gate 不放，只认 ctx）
	case <-e.seen:
	case <-time.After(3 * time.Second):
		t.Fatal("通知以后 3 秒内没有开始补向量")
	}

	done := make(chan error, 1)
	go func() { done <- m.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close 应该取消在途的补向量并退出，3 秒还没返回")
	}
	m.EmbeddingReady() // 关了以后再通知：不起协程
	m.vecMu.Lock()
	running := m.vecRunning
	m.vecMu.Unlock()
	if running {
		t.Fatal("关了以后不该再起补向量的协程")
	}
	close(e.gate) // 嵌入这时才回来：也没有人会再去写
	time.Sleep(50 * time.Millisecond)
	if rows := sidecarVectorRows(t, file); rows != 0 {
		t.Fatalf("关了以后迟到的向量不该写进旁边的文件：%d 行", rows)
	}
	if err := m.Close(); err != nil { // 可以重复调
		t.Fatalf("重复 Close：%v", err)
	}
}

// 可写的一侧：嵌入恢复，补向量卡在嵌入上时，其中一条被撤销（忘掉）、条目表换了（撤销不排补向量，没有别人替它再来一趟）；
// 放行以后，被忘掉那条的向量不能留在表里或复活
func TestLateFillDoesNotResurrectForgottenVector(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{down: true, seen: make(chan struct{}, 1)}
	m, _ := newWritable(t, e)
	a := mustApply(t, m, summary(aiFact("主人在学吉他"))).Added[0]
	b := mustApply(t, m, summary(aiFact("主人下周搬家"))).Added[0]
	waitVectors(t, m)
	if !m.EmbeddingDegraded() || len(m.index.Vectors()) != 0 {
		t.Fatal("嵌入不可用时两条都没有向量、标降级")
	}
	select { // 上面几趟留下的信号清掉，下面等的是这一趟
	case <-e.seen:
	default:
	}

	gate := make(chan struct{})
	e.set(func(e *scriptedEmbed) { e.down = false; e.gate = gate }) // 恢复了，但嵌入卡着
	m.EmbeddingReady()
	select {
	case <-e.seen: // 补向量手里是两条的快照
	case <-time.After(3 * time.Second):
		t.Fatal("通知以后 3 秒内没有开始补向量")
	}
	mustApply(t, m, summary(RevokeOp{ID: idOf(a)})) // 忘掉第一条：条目表换了
	close(gate)
	waitVectors(t, m)

	v := m.index.Vectors()
	if v[a] != nil || v[b] == nil || len(v) != 1 {
		t.Fatalf("被忘掉的向量不该留着或复活，另一条该在：%d 条，忘掉的在 %v，另一条在 %v", len(v), v[a] != nil, v[b] != nil)
	}
	if miss, _ := m.index.MissingVectors(m.Items()); miss != 0 || m.EmbeddingDegraded() {
		t.Fatalf("安静下来以后不缺、不降级：缺 %d，降级 %v", miss, m.EmbeddingDegraded())
	}
	if rows := indexVectorRows(t, m); rows != 1 {
		t.Fatalf("旁边的文件里也只该剩 1 行：%d", rows)
	}
}

// indexVectorRows：同 sidecarVectorRows，已经打开着的记忆直接问它的索引库
func indexVectorRows(t *testing.T, m *Memory) int {
	t.Helper()
	var rows int
	if err := m.index.db.QueryRow(`SELECT count(*) FROM memory_vectors`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
