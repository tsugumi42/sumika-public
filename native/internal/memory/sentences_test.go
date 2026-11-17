package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed"
)

// warmEmbed 是句向量预热测试的假嵌入：模型名、状态可以中途改；句向量（TypeDocument）每次调用记下条数、可以慢、可以先失败几次；
// 补 memory_vectors 走的普通 Embed 和守卫查询（TypeQuery）分开计
type warmEmbed struct {
	mu      sync.Mutex
	model   string
	status  embed.Status
	delay   time.Duration
	fail    int   // 接下来这么多次句向量调用返回错误
	batches []int // 每次句向量调用的条数
	queries int
	plain   int
}

func newWarmEmbed(model string) *warmEmbed {
	return &warmEmbed{model: model, status: embed.StatusReady}
}

func (e *warmEmbed) Model() string { e.mu.Lock(); defer e.mu.Unlock(); return e.model }
func (e *warmEmbed) Status() (embed.Status, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, ""
}
func (e *warmEmbed) set(f func(*warmEmbed)) { e.mu.Lock(); defer e.mu.Unlock(); f(e) }
func (e *warmEmbed) batchSizes() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.batches...)
}

func warmVecs(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{1, float32(len(t)%5) + 0.5}
	}
	return out
}

func (e *warmEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.plain++
	e.mu.Unlock()
	return warmVecs(texts), nil
}

func (e *warmEmbed) EmbedTyped(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	e.mu.Lock()
	if kind == embed.TypeQuery {
		e.queries++
		e.mu.Unlock()
		return warmVecs(texts), nil
	}
	e.batches = append(e.batches, len(texts))
	delay, failing := e.delay, e.fail > 0
	if failing {
		e.fail--
	}
	e.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failing {
		return nil, errors.New("编的嵌入错误")
	}
	return warmVecs(texts), nil
}

// manySentences：n 个事实，每个 per 句（用「。」断句），句句不同
func manySentences(t *testing.T, n, per int) string {
	t.Helper()
	var facts [][]string
	for i := 1; i <= n; i++ {
		var b strings.Builder
		for j := 1; j <= per; j++ {
			fmt.Fprintf(&b, "关于吉他的第%d条第%d句话。", i, j)
		}
		facts = append(facts, []string{fmt.Sprint(i), "fact", "active", b.String(), "owner", "owner"})
	}
	return bunDb(t, facts, nil)
}

func openWarm(t *testing.T, file string, e *warmEmbed) (*Memory, *logSink) {
	t.Helper()
	sink := &logSink{}
	m, err := Open(context.Background(), file, e, nil, Options{
		Character: "suzune", Logf: sink.logf, Gen: &outboundFakeGen{reply: `{"leaked":"no"}`}, Model: func() string { return "编的后台模型" },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, sink
}

func waitGuard(t *testing.T, m *Memory, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.GuardLevel() != want {
		if time.Now().After(deadline) {
			t.Fatalf("5 秒后守卫能力还是 %q，要 %q", m.GuardLevel(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func sentenceRows(t *testing.T, m *Memory, where string, args ...any) int {
	t.Helper()
	var n int
	if err := m.index.db.QueryRow(`SELECT count(*) FROM memory_sentences `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// W1a 验收 2：句向量没补完 → literal-pending（发送由 qqSendMode 钳在试运行）；补完 → full。
// 换模型当天旧模型的句向量全部作废：GuardLevel 立刻降成 literal-pending，补完以后回 full，旧模型的行顺手删掉；
// 写入新条目不会把已经 full 的守卫又降下来（新条目的句向量由写入后那一趟和守卫按需补）
func TestGuardLevelNeedsWarmup(t *testing.T) {
	t.Parallel()
	m, e, _, _ := outboundRig(t, "主人把备用电池收在蓝柜底层。他怕黑。")
	ctx := context.Background()
	if got := m.GuardLevel(); got != "literal-pending" {
		t.Fatalf("句向量还没补：%q", got)
	}
	m.warmSentences(ctx)
	if got, n := m.GuardLevel(), sentenceRows(t, m, ""); got != "full" || n != 2 {
		t.Fatalf("补完以后：%q，%d 行（要 full、2 行）", got, n)
	}
	// 换模型
	e.model = "编的新嵌入"
	if got := m.GuardLevel(); got != "literal-pending" {
		t.Fatalf("换了模型，旧模型的句向量不能算：%q", got)
	}
	m.warmSentences(ctx)
	if got := m.GuardLevel(); got != "full" {
		t.Fatalf("新模型补完以后：%q", got)
	}
	if old := sentenceRows(t, m, "WHERE model != ?", "编的新嵌入"); old != 0 {
		t.Fatalf("旧模型的行该顺手清掉：还剩 %d 行", old)
	}
	// 写入新条目（这里直接换条目快照）：已经 full 的不降，守卫按需补它
	m.mu.Lock()
	m.items = append(m.items[:len(m.items):len(m.items)], Item{Ref: "f:101", Type: "fact", Status: "active", Text: "新写进来的一条。", Scopes: []string{"owner"}})
	m.mu.Unlock()
	if got := m.GuardLevel(); got != "full" {
		t.Fatalf("写入新条目不该把守卫降成 %q", got)
	}
	// 嵌入掉线：照旧 literal-pending（这一条原来就有）
	m.embed = guardStatusEmbed{e, embed.StatusDown}
	if got := m.GuardLevel(); got != "literal-pending" {
		t.Fatalf("嵌入掉线：%q", got)
	}
}

// 预热范围是守卫能用的全部条目，不按听众：有效的和待确认的事实都补，撤销的、被取代的（不会读进来）和待确认的片段不补；
// 改了正文的条目旧行作废、不在了的条目的行被清掉
func TestWarmupScopeAndStaleRows(t *testing.T) {
	t.Parallel()
	m, _, _, _ := outboundRig(t, "第一句。第二句。")
	m.mu.Lock()
	m.items = []Item{
		{Ref: "f:1", Type: "fact", Status: "active", Text: "一号第一句。一号第二句。", Scopes: []string{"owner"}},
		{Ref: "f:2", Type: "fact", Status: "candidate", Text: "待确认的事实。", Scopes: []string{"conv:qq:group:1"}},
		{Ref: "e:3", Type: "episode", Status: "active", Text: "一段有效的片段。", Scopes: []string{"owner"}},
		{Ref: "e:4", Type: "episode", Status: "candidate", Text: "待确认的片段不在守卫范围。", Scopes: []string{"owner"}},
	}
	m.mu.Unlock()
	ctx := context.Background()
	m.warmSentences(ctx)
	if n := sentenceRows(t, m, "WHERE ref = 'e:4'"); n != 0 {
		t.Fatalf("待确认的片段不是守卫的范围：%d 行", n)
	}
	if n := sentenceRows(t, m, ""); n != 4 { // f:1 两句 + f:2 一句 + e:3 一句
		t.Fatalf("该有 4 行：%d", n)
	}
	// 一号改了第二句，只剩一句；二号没了
	m.mu.Lock()
	m.items = []Item{
		{Ref: "f:1", Type: "fact", Status: "active", Text: "一号第一句。", Scopes: []string{"owner"}},
		{Ref: "e:3", Type: "episode", Status: "active", Text: "一段有效的片段。", Scopes: []string{"owner"}},
	}
	m.mu.Unlock()
	m.warmSentences(ctx)
	if n, gone := sentenceRows(t, m, ""), sentenceRows(t, m, "WHERE ref = 'f:2' OR (ref = 'f:1' AND idx = 1)"); n != 2 || gone != 0 {
		t.Fatalf("多出来的行该清掉：共 %d 行，该清的还剩 %d 行", n, gone)
	}
}

// W1a 验收 2：预热分批（一批最多 VectorBatch=8 句）、批与批之间放开 sentenceGate（守卫等锁不超过一批）；
// 补完以后第一次守卫判断不再现算句向量，走 normal 路线
func TestSentenceWarmup(t *testing.T) {
	t.Parallel()
	file := manySentences(t, 10, 8) // 80 句
	e := newWarmEmbed("预热测试嵌入")
	m, sink := openWarm(t, file, e)
	if got := m.GuardLevel(); got != "literal-pending" {
		t.Fatalf("还没预热：%q", got)
	}
	e.set(func(e *warmEmbed) { e.delay = 100 * time.Millisecond })
	t0 := time.Now()
	m.EmbeddingReady()
	// 预热进行中，另一个人要用 sentenceGate（守卫按需补就是这样）：最多等一批，不是等整段
	for len(e.batchSizes()) < 2 {
		if time.Since(t0) > 3*time.Second {
			t.Fatal("预热没开始")
		}
		time.Sleep(5 * time.Millisecond)
	}
	w0 := time.Now()
	m.index.sentenceGate <- struct{}{}
	waited := time.Since(w0)
	<-m.index.sentenceGate
	if waited > 450*time.Millisecond {
		t.Fatalf("预热整段持着 sentenceGate：等了 %v（一批 100 毫秒，一共 10 批）", waited)
	}
	waitGuard(t, m, "full")
	total := time.Since(t0)
	sizes := e.batchSizes()
	sum := 0
	for _, n := range sizes {
		if n > VectorBatch {
			t.Fatalf("一批不该超过 %d 句：%v", VectorBatch, sizes)
		}
		sum += n
	}
	if sum != 80 || len(sizes) != 10 || total < 900*time.Millisecond {
		t.Fatalf("80 句该分 10 批补完：%v（共 %d 句，用了 %v）", sizes, sum, total)
	}
	if n := sentenceRows(t, m, ""); n != 80 {
		t.Fatalf("库里该有 80 行：%d", n)
	}
	// 补完以后第一次守卫判断：句向量都现成，只算查询，走 normal
	before := len(e.batchSizes())
	got, err := m.CheckOutbound(context.Background(), "他今天心情不错。", Audience{Channel: "qq", Conversation: "qq:100:group:200", Scene: "group"})
	if err != nil || m.GuardRoute() != "normal" || len(e.batchSizes()) != before {
		t.Fatalf("补完以后第一次判断该走 normal 且不现算句向量：%v %v %s，句向量调用 %d → %d", got, err, m.GuardRoute(), before, len(e.batchSizes()))
	}
	if !strings.Contains(sink.String(), "句向量预热完成（模型 预热测试嵌入），补了 80 句") {
		t.Fatalf("日志该记一笔预热完成：%s", sink.String())
	}
	if strings.Contains(sink.String(), "关于吉他") {
		t.Fatal("日志不该有记忆原文")
	}
}

// 嵌入出错：预热停在那一批，已经写进去的留着；过一阵自己再排一趟（不靠谁再通知），间隔翻倍；关记忆时定时器一起收掉
func TestWarmupRetriesAndStops(t *testing.T) {
	t.Parallel()
	t.Run("出错后自己重试", func(t *testing.T) {
		t.Parallel()
		file := manySentences(t, 3, 8) // 24 句，3 批
		e := newWarmEmbed("预热测试嵌入")
		m, sink := openWarm(t, file, e)
		m.vecMu.Lock()
		m.warmRetry = 20 * time.Millisecond
		m.vecMu.Unlock()
		e.set(func(e *warmEmbed) { e.fail = 2 }) // 前两次句向量调用失败：第一趟停在第一批，第二趟又停在第一批，第三趟补完
		m.EmbeddingReady()                       // 只通知这一次；后面的两趟是预热自己排的
		waitGuard(t, m, "full")
		if n := len(e.batchSizes()); n != 2+3 {
			t.Fatalf("两次失败加三批成功共 5 次句向量调用：%d 次 %v", n, e.batchSizes())
		}
		if !strings.Contains(sink.String(), "句向量预热没做完") {
			t.Fatalf("失败该记一行日志：%s", sink.String())
		}
	})
	t.Run("关记忆收掉重试的定时器", func(t *testing.T) {
		t.Parallel()
		e := newWarmEmbed("预热测试嵌入")
		e.set(func(e *warmEmbed) { e.fail = 1000 })
		sink := &logSink{}
		m, err := Open(context.Background(), manySentences(t, 1, 3), e, nil, Options{Character: "suzune", Logf: sink.logf})
		if err != nil {
			t.Fatal(err)
		}
		m.vecMu.Lock()
		m.warmRetry = time.Hour
		m.vecMu.Unlock()
		m.EmbeddingReady()
		deadline := time.Now().Add(3 * time.Second)
		for {
			m.vecMu.Lock()
			armed := m.warmTimer != nil
			m.vecMu.Unlock()
			if armed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("预热失败后没有排下一趟")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		m.vecMu.Lock()
		left := m.warmTimer
		m.vecMu.Unlock()
		if left != nil {
			t.Fatal("关了以后重试的定时器还在")
		}
	})
	t.Run("关记忆时取消正在补的", func(t *testing.T) {
		t.Parallel()
		e := newWarmEmbed("预热测试嵌入")
		e.set(func(e *warmEmbed) { e.delay = time.Hour })
		sink := &logSink{}
		m, err := Open(context.Background(), manySentences(t, 1, 3), e, nil, Options{Character: "suzune", Logf: sink.logf})
		if err != nil {
			t.Fatal(err)
		}
		m.EmbeddingReady()
		for len(e.batchSizes()) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		done := make(chan struct{})
		go func() { m.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("关记忆被卡住的预热拖住了")
		}
	})
}

// W1a 验收 4：库里是模型 A 的向量，换成模型 B：补齐完成前召回只用全文，完成后一条都不缺；
// 日志记一行「向量补齐 N 条（模型 B）」
func TestModelSwitchRecomputes(t *testing.T) {
	t.Parallel()
	file := readOnlyFixture(t)
	// 先用模型 A 把向量补齐
	a := &statusEmbed{status: embed.StatusReady}
	ma, _ := openRO(t, file, a)
	waitFilled(t, ma, 3)
	if err := ma.Close(); err != nil {
		t.Fatal(err)
	}
	// 同一份库换成 B：开的时候嵌入还没就绪（Open 不同步向量），此时库里全是 A 的向量
	b := &switchEmbed{statusEmbed: &statusEmbed{status: embed.StatusStarting}}
	sink := &logSink{}
	m, err := Open(context.Background(), file, b, nil, Options{Character: "suzune", Logf: sink.logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if hits := recallHere(m); len(hits) != 0 {
		t.Fatalf("补齐完成前，没有共用字的查询只能靠全文，不该有结果（不能拿 A 的向量混用）：%+v", hits)
	}
	b.statusEmbed.set(func(e *statusEmbed) { e.status = embed.StatusReady })
	m.EmbeddingReady()
	waitFilled(t, m, 3)
	waitVectors(t, m)
	if miss, _ := m.index.MissingVectors(m.Items()); miss != 0 || m.EmbeddingDegraded() {
		t.Fatalf("补齐完成以后不缺、不降级：缺 %d，降级 %v", miss, m.EmbeddingDegraded())
	}
	if hits := recallHere(m); len(hits) != 1 || hits[0].Vector == 0 {
		t.Fatalf("补齐以后该由新模型的向量找到：%+v", hits)
	}
	if n := strings.Count(sink.String(), "向量补齐 3 条（模型 模型B）"); n != 1 {
		t.Fatalf("日志该有一行「向量补齐 3 条（模型 模型B）」：%d 行\n%s", n, sink.String())
	}
}

// switchEmbed 是模型名叫「模型B」的 statusEmbed（statusEmbed 自己的模型名写死是 status-fake，当作模型 A）
type switchEmbed struct{ *statusEmbed }

func (s *switchEmbed) Model() string { return "模型B" }

// W1a：写入以后的句向量和向量一起排（同一个 kick），不留给下一次守卫判断在 2 秒里现算。
// 此前 SyncVectors 只管 memory_vectors、不碰 memory_sentences（规格里这一条是「待核」，核出来是没有，这里接上）
func TestWriteKicksSentenceWarmup(t *testing.T) {
	t.Parallel()
	e := newWarmEmbed("预热测试嵌入")
	m, _ := newWritable(t, e)
	res := mustApply(t, m, summary(ownerFact("主人最近在学吉他。每天练半小时。")))
	waitVectors(t, m)
	if len(res.Added) != 1 {
		t.Fatalf("该写进一条：%+v", res)
	}
	if n := sentenceRows(t, m, "WHERE ref = ?", res.Added[0]); n != 2 {
		t.Fatalf("写入以后那一趟该把这条的两句也补上：%d 行", n)
	}
	if sizes := e.batchSizes(); len(sizes) != 1 || sizes[0] != 2 {
		t.Fatalf("两句一批补完：%v", sizes)
	}
	// 再写一条：只补新的，不重算旧的
	mustApply(t, m, summary(ownerFact("主人下周要搬家。")))
	waitVectors(t, m)
	if sizes := e.batchSizes(); len(sizes) != 2 || sizes[1] != 1 {
		t.Fatalf("第二次写入只该补新的一句：%v", sizes)
	}
}
