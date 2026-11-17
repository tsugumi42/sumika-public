package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 清单 ⑧：事务提交了，向量还没算好，召回走全文照样找得到；算好以后向量也在
func TestVectorsComputedAfterCommit(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{gate: make(chan struct{}), seen: make(chan struct{}, 1)}
	m, _ := newWritable(t, e)
	ctx := context.Background()
	res := mustApply(t, m, summary(ownerFact("主人最近在学吉他")))
	ref := res.Added[0]

	<-e.seen // 后台已经在算了，卡在嵌入上
	hits := m.Recall(ctx, ownerHere, Query{Text: "学吉他", Mode: ModeExplicit}).Hits
	if len(hits) != 1 || hits[0].Ref != ref || hits[0].Lexical == 0 || hits[0].Vector != 0 {
		t.Fatalf("向量没好的时候全文应该找得到：%+v", hits)
	}
	if miss, _ := m.index.MissingVectors(m.Items()); miss != 1 {
		t.Fatalf("此刻应该还缺 1 条向量：%d", miss)
	}
	close(e.gate)
	waitVectors(t, m)
	if v := m.index.Vectors()[ref]; len(v) != 2 {
		t.Fatalf("向量应该补上了：%v", v)
	}
	if miss, _ := m.index.MissingVectors(m.Items()); miss != 0 || m.EmbeddingDegraded() {
		t.Fatalf("补齐以后不缺、不降级：缺 %d，降级 %v", miss, m.EmbeddingDegraded())
	}
}

// B18：向量算失败不拖垮、不回滚；降级期间写入只排队不逐条去试；恢复以后一次补齐。日志里没有原文
func TestVectorFailureDoesNotRollBackAndIsBackfilled(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{fail: errors.New("上游炸了，请求体里带着：很私密的正文XYZ")}
	m, sink := newWritable(t, e)
	ctx := context.Background()

	res := mustApply(t, m, summary(ownerFact("很私密的正文XYZ")))
	waitVectors(t, m)
	if factStatus(t, m, "很私密的正文XYZ") != "active" || len(m.Items()) != 1 {
		t.Fatal("向量算失败不该影响记忆本身")
	}
	if !m.EmbeddingDegraded() {
		t.Fatal("没算上应该标降级")
	}
	log := sink.String()
	if !strings.Contains(log, "向量没算成") || !strings.Contains(log, "还缺 1 条") {
		t.Fatalf("日志该说没算成、还缺几条：%q", log)
	}
	if strings.Contains(log, "XYZ") || strings.Contains(log, "上游炸了") || strings.Contains(log, "console:") {
		t.Fatalf("日志里不该有原文、嵌入服务回的错误、会话 id：%q", log)
	}
	calls := e.callCount()

	// 降级期间再写：写得进去，不再去试嵌入
	mustApply(t, m, summary(ownerFact("降级期间写的第二条")))
	waitVectors(t, m)
	if e.callCount() != calls {
		t.Fatalf("降级期间写入不该逐条去试嵌入：调用从 %d 次变成 %d 次", calls, e.callCount())
	}
	if miss, _ := m.index.MissingVectors(m.Items()); miss != 2 {
		t.Fatalf("两条都该排在补齐队列里：缺 %d", miss)
	}
	// 召回照用全文
	if hits := m.Recall(ctx, ownerHere, Query{Text: "降级期间", Mode: ModeExplicit}).Hits; len(hits) != 1 {
		t.Fatalf("降级期间全文照样找得到：%+v", hits)
	}

	// 嵌入服务恢复，补齐一次
	e.set(func(e *scriptedEmbed) { e.fail = nil })
	missing, err := m.BackfillVectors(ctx)
	if err != nil || missing != 0 || m.EmbeddingDegraded() {
		t.Fatalf("补齐以后应该齐了、不降级：缺 %d，%v，降级 %v", missing, err, m.EmbeddingDegraded())
	}
	if len(m.index.Vectors()) != 2 || m.index.Vectors()[res.Added[0]] == nil {
		t.Fatalf("两条向量都该在：%d", len(m.index.Vectors()))
	}
	if !strings.Contains(sink.String(), "向量补齐了") {
		t.Fatalf("恢复时该记一笔：%q", sink.String())
	}
	// 齐了以后的写入恢复为后台直接算
	before := e.callCount()
	mustApply(t, m, summary(ownerFact("恢复以后写的")))
	waitVectors(t, m)
	if e.callCount() == before || m.EmbeddingDegraded() {
		t.Fatalf("恢复以后写入该照常算向量：调用 %d → %d，降级 %v", before, e.callCount(), m.EmbeddingDegraded())
	}
}

// SyncVectors 对 ErrUnavailable 返回 nil（index.go），补齐不能靠它的返回值判断做完了没有
func TestEmbedUnavailableIsDegradedNotDone(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{down: true}
	m, sink := newWritable(t, e)
	mustApply(t, m, summary(ownerFact("嵌入服务还没起来时写的")))
	waitVectors(t, m)
	if !m.EmbeddingDegraded() || !strings.Contains(sink.String(), "嵌入不可用") {
		t.Fatalf("嵌入不可用应该标降级并说清原因：%v %q", m.EmbeddingDegraded(), sink.String())
	}
	if missing, err := m.BackfillVectors(context.Background()); err != nil || missing != 1 {
		t.Fatalf("还是不可用：补齐不报错，但要如实说还缺 1 条：%d %v", missing, err)
	}
	e.set(func(e *scriptedEmbed) { e.down = false })
	if missing, _ := m.BackfillVectors(context.Background()); missing != 0 || m.EmbeddingDegraded() {
		t.Fatalf("恢复以后：缺 %d，降级 %v", missing, m.EmbeddingDegraded())
	}
}

// 打开时已经缺向量（上一回没补完）：一开始就标降级，写入不去试，等 BackfillVectors
func TestOpenWithMissingVectorsIsDegraded(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, func(db *sql.DB) {
		db.Exec(`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F1', 'fact', '上回没算向量的事实', 'private', 'active', 1, 'owner')`)
	})
	e := &scriptedEmbed{down: true}
	m, _ := openW(t, file, e)
	if !m.EmbeddingDegraded() {
		t.Fatal("打开时就缺向量应该是降级")
	}
	calls := e.callCount()
	mustApply(t, m, summary(ownerFact("新写的")))
	waitVectors(t, m)
	if e.callCount() != calls {
		t.Fatal("降级期间写入不该去试嵌入")
	}
	e.set(func(e *scriptedEmbed) { e.down = false })
	if missing, _ := m.BackfillVectors(context.Background()); missing != 0 {
		t.Fatalf("补齐后缺 %d", missing)
	}
}

// 清单 ④：向量算到一半停掉。Close 取消在途的嵌入、等协程退出，不 panic，之后什么都写不进
func TestCloseCancelsEmbeddingInFlight(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{gate: make(chan struct{}), seen: make(chan struct{}, 1)} // gate 不放：嵌入一直卡着，只认 ctx
	m, _ := newWritable(t, e)
	mustApply(t, m, summary(ownerFact("向量算到一半")))
	<-e.seen
	done := make(chan error, 1)
	go func() { done <- m.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close 应该取消在途的嵌入并退出，3 秒还没返回")
	}
	if _, err := m.apply(context.Background(), summary(ownerFact("停掉以后"))); !errors.Is(err, ErrClosed) {
		t.Fatalf("停掉以后写入应该是 ErrClosed：%v", err)
	}
	// 停掉以后踢一下补齐也不会起协程
	m.kickVectors(false)
	m.vecMu.Lock()
	running := m.vecRunning
	m.vecMu.Unlock()
	if running {
		t.Fatal("停掉以后不该再起补齐协程")
	}
}

// 召回、读条目、写、补向量同时来（go test -race 下才看得出锁有没有用；去掉锁 -race 报红）
func TestConcurrentRecallAndWrites(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{}
	m, _ := newWritable(t, e)
	ctx := context.Background()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var recalls atomic.Int64
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, h := range m.Recall(ctx, ownerHere, Query{Text: "喜欢吉他", Mode: ModeExplicit}).Hits {
					if h.Ref == "" || h.Text == "" {
						t.Errorf("召回给出了空条目：%+v", h)
					}
				}
				_ = m.Items()
				recalls.Add(1)
			}
		}()
	}
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for i := 0; i < 25; i++ {
			if _, err := m.apply(ctx, summary(ownerFact("主人喜欢吉他的第"+string(rune('A'+i))+"个说法"))); err != nil {
				t.Errorf("apply：%v", err)
			}
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < 10; i++ {
			if _, err := m.BackfillVectors(ctx); err != nil {
				t.Errorf("BackfillVectors：%v", err)
			}
		}
	}()
	writers.Wait()
	close(stop)
	readers.Wait()
	waitVectors(t, m)
	if n := len(m.Items()); n != 25 {
		t.Fatalf("25 条都该在：%d", n)
	}
	if miss, _ := m.BackfillVectors(ctx); miss != 0 {
		t.Fatalf("补齐以后不缺：%d", miss)
	}
	if recalls.Load() == 0 {
		t.Fatal("读的协程一次都没跑到")
	}
}

// 向量表在补齐时被整张换掉，召回同时在读它：读的人拿到的那一张不会在手里变（go test -race 下，去掉 Index 的锁会报红）
func TestIndexVectorsReadWhileSyncing(t *testing.T) {
	t.Parallel()
	x, err := OpenIndex(context.Background(), filepath.Join(t.TempDir(), "memory-v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	e := &scriptedEmbed{}
	var items []Item
	for i := 0; i < 20; i++ {
		items = append(items, Item{Ref: fmt.Sprintf("f:%02d", i), Text: fmt.Sprintf("第 %d 条", i), Scopes: []string{"owner"}})
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				v := x.Vectors()
				n := len(v)
				for range v { // 遍历拿到的这一张：换表的人不会改它，不会报「并发读写 map」
				}
				if len(v) != n {
					t.Error("拿到手的向量表被改了")
				}
			}
		}()
	}
	for round := 1; round <= 40; round++ {
		if _, err := x.SyncVectors(context.Background(), items[:round%len(items)+1], e, 7); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
}

// 补齐不只在后台协程里调：别处调 BackfillVectors 卡在嵌入上时，Close 也要把它叫停，不能在库关了以后还往里写
func TestCloseCancelsBackfillCalledFromOutside(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{down: true, seen: make(chan struct{}, 1)}
	m, _ := newWritable(t, e)
	mustApply(t, m, summary(ownerFact("补齐时卡住的事实")))
	waitVectors(t, m)
	select { // 上一趟留下的信号清掉，下面等的是这一趟
	case <-e.seen:
	default:
	}
	e.set(func(e *scriptedEmbed) { e.down = false; e.gate = make(chan struct{}) }) // 恢复了，但嵌入卡着
	done := make(chan error, 1)
	go func() { _, err := m.BackfillVectors(context.Background()); done <- err }()
	<-e.seen
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("被 Close 叫停的补齐应该返回取消：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close 之后补齐还卡着")
	}
	<-closed
}

// 复核（W2）：外面调的 BackfillVectors 先取条目快照、再排队等锁。排队的时候后台协程已经给新写的条目算好了向量，
// 轮到它时拿着旧快照，把新条目的向量当多余的删掉，数缺几条又用同一份旧快照，数出 0、把降级标志清了。
// 这个交错靠调度，一轮出不来（旧代码约每六轮出一次），所以跑够多轮。断言不在结尾再补一次齐来兜底（旧测试就是这样盖住了它）
func TestBackfillQueuedBehindSyncDoesNotDropNewVectors(t *testing.T) {
	// 不加 t.Parallel()：下面把整个进程的 GOMAXPROCS 压到 1，并行跑会拖慢同时在跑的所有测试，也让它自己的 3 秒等待在满载时误红
	// 只留一个 P：解锁时被叫醒的排队者要等放锁的协程让出才跑，后台协程趁机先把锁抢回去，交错就稳定出现
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	e := &scriptedEmbed{seen: make(chan struct{}, 1)}
	m, _ := newWritable(t, e)
	ctx := context.Background()
	for round := 0; round < 30; round++ {
		select { // 上一轮留下的信号清掉，下面等的是这一轮
		case <-e.seen:
		default:
		}
		gate := make(chan struct{})
		e.set(func(e *scriptedEmbed) { e.gate = gate })
		mustApply(t, m, summary(ownerFact(fmt.Sprintf("round%d alpha", round))))
		select { // 后台协程卡在嵌入上，手里拿着补齐的锁
		case <-e.seen:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 轮：后台没有去算新写的向量（上一轮留下了降级标志？）", round)
		}
		done := make(chan struct{})
		go func() { defer close(done); m.BackfillVectors(ctx) }()
		time.Sleep(5 * time.Millisecond)                                        // 让外面这一趟取好快照、排上队
		mustApply(t, m, summary(ownerFact(fmt.Sprintf("round%d beta", round)))) // 排队期间又写进一条
		close(gate)
		<-done
		waitVectors(t, m)
		// 嵌入一直是好的，安静下来以后每一条都该有向量、也不该降级（缺着还清了降级标志是最坏的，下一轮的写入就没人补了）
		if miss, _ := m.index.MissingVectors(m.Items()); miss != 0 || m.EmbeddingDegraded() {
			t.Fatalf("第 %d 轮：安静下来以后向量该齐、不降级：缺 %d，降级 %v", round, miss, m.EmbeddingDegraded())
		}
	}
}

// 邻居：嵌入一直不可用时，补齐照旧如实说还缺几条、保持降级；途中又写进一条也算进去
func TestBackfillCountsEntriesWrittenWhileDown(t *testing.T) {
	t.Parallel()
	e := &scriptedEmbed{down: true}
	m, _ := newWritable(t, e)
	mustApply(t, m, summary(ownerFact("嵌入没起来时写的甲")))
	waitVectors(t, m)
	mustApply(t, m, summary(ownerFact("嵌入没起来时写的乙")))
	waitVectors(t, m)
	if missing, err := m.BackfillVectors(context.Background()); err != nil || missing != 2 || !m.EmbeddingDegraded() {
		t.Fatalf("还是不可用：缺 2 条、保持降级：缺 %d，%v，降级 %v", missing, err, m.EmbeddingDegraded())
	}
}
