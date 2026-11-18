package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// 向量在事务提交以后异步算（规格-记忆-v2 13.2.4 第 9 步）：算失败只记日志、不回滚（B18），没算上的之后补齐。
// 补齐靠自己数缺的（Index.MissingVectors），不信 SyncVectors 的返回值：嵌入不可用时它返回 nil。
// 降级标志 embedding_degraded（借 MaiBot）：有向量没补上就是降级，降级期间写入只排队、不逐条去试嵌入服务，等 BackfillVectors 来补。
// 补向量只写旁边的派生索引（memory-v2.db），不碰事实库：所以只读的记忆也补（嵌入比记忆晚就绪，见 EmbeddingReady），
// 这一组状态因此挂在 Memory 上，不挂在 writer 上

// vecState 是补向量这一侧的状态，只读、可写两种模式都有
type vecState struct {
	// bgCtx 在 Close 时取消，bg 等后台协程退出；vecMu 管下面三个，Close 之后不再起新的
	bgCtx                           context.Context
	cancel                          context.CancelFunc
	bg                              sync.WaitGroup
	vecMu                           sync.Mutex
	vecRunning, vecAgain, vecClosed bool
	// vecSync：一趟补齐从取条目快照到定降级标志，整个过程只许一个人在里面（后台协程和外面调的 BackfillVectors 都排队）
	vecSync  sync.Mutex
	degraded atomic.Bool // embedding_degraded：有向量没补上。降级期间写入只排队，不逐条去试（借 MaiBot）
	// 句向量预热（sentences.go）：warmModel 是预热做完的那个模型名（atomic.Value 里是 string），GuardLevel 读；
	// 下面三个归 vecMu 管：重试的定时器、连续失败了几次、重试间隔（零用默认的 30 秒起翻倍，测试里缩短）
	warmModel atomic.Value
	warmTimer *time.Timer
	warmFails int
	warmRetry time.Duration
}

func (v *vecState) init() {
	v.bgCtx, v.cancel = context.WithCancel(context.Background())
}

// stopVectors 停掉补向量这一侧：置关闭（之后不再起新的）→ 取消在途的 → 等协程退出。可以重复调，只有第一次返回真
func (m *Memory) stopVectors() (first bool) {
	m.vecMu.Lock()
	first = !m.vecClosed
	m.vecClosed = true
	if m.warmTimer != nil {
		m.warmTimer.Stop() // 已经触发的那一次进 kickVectors 会看到 vecClosed 直接回去
		m.warmTimer = nil
	}
	m.vecMu.Unlock()
	if !first {
		return false
	}
	m.cancel()
	m.bg.Wait()
	return true
}

// EmbeddingDegraded：有条目还没有现行向量（嵌入不可用、出错，或者还没来得及算）
func (m *Memory) EmbeddingDegraded() bool {
	return m.degraded.Load()
}

// EmbeddingReady 是宿主在嵌入就绪（含挂了又恢复）时调的：马上排一次补向量，不等它做完，所以可以在嵌入的状态回调里直接调。
// 只读的记忆也补（只写旁边的 memory-v2.db，不碰事实库）；已经有一趟在跑就并进去。
// 不看降级标志：降级正是"嵌入不在"时留下的，恢复的这一次必须补。关了以后调什么也不做
func (m *Memory) EmbeddingReady() {
	m.kickVectors(true)
}

// BackfillVectors 现在就补一次，不管降级与否。返回补完以后还缺几条，0 表示齐了（降级标志同时清掉）。
// 嵌入服务恢复了、或者定时来补的时候调；没配嵌入返回 0
func (m *Memory) BackfillVectors(ctx context.Context) (missing int, err error) {
	if _, err := m.writer(); err != nil {
		return 0, err
	}
	// Close 时一起取消：补齐协程之外调进来的这一路也不能在库关了以后还往里写
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(m.bgCtx, cancel)()
	return m.syncVectors(ctx)
}

// kickVectors 排一次补齐：已经有一趟在跑就并进去（再多跑一圈）。写完以后排的（force 为假）降级期间不试；
// 嵌入就绪通知排的（force 为真）不看降级
func (m *Memory) kickVectors(force bool) {
	if m.embed == nil || (!force && m.degraded.Load()) {
		return
	}
	m.vecMu.Lock()
	if m.vecClosed {
		m.vecMu.Unlock()
		return
	}
	if m.vecRunning {
		m.vecAgain = true
		m.vecMu.Unlock()
		return
	}
	m.vecRunning = true
	m.bg.Add(1) // 在 vecMu 里加：Close 先置 vecClosed 再 Wait，不会和 Add 撞
	m.vecMu.Unlock()
	go func() {
		defer m.bg.Done()
		for {
			m.syncVectors(m.bgCtx)
			m.warmSentences(m.bgCtx) // 向量补完接着补句向量：新条目写入以后的这一趟也走这里
			m.vecMu.Lock()
			if !m.vecAgain || m.vecClosed {
				m.vecRunning = false
				m.vecMu.Unlock()
				return
			}
			m.vecAgain = false
			m.vecMu.Unlock()
		}
	}()
}

// syncVectors 补一次：算缺的，再数还缺几条，据此定降级标志。日志只写数和原因码。
// 条目快照要在 vecSync 里面取，不能先取再排队：SyncVectors 会把"不在这份快照里"的向量当成多余的删掉，
// 排在后面的那一趟如果拿着排队之前的旧快照，就会把前一趟刚给新条目算好的向量删掉，数缺几条时又用同一份旧快照数，
// 数出 0、把降级标志清了（13.2.4 第 9 步要求自己数缺的，非零就留着）
func (m *Memory) syncVectors(ctx context.Context) (missing int, err error) {
	if m.embed == nil {
		return 0, nil
	}
	m.vecSync.Lock()
	defer m.vecSync.Unlock()
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err() // 停掉了（排队的时候 Close 来了也走这里）：不算失败，不动标志
		}
		items, _, ver := m.rawSnapshot()
		var computed int
		computed, err = m.index.SyncVectors(ctx, items, m.embed, VectorBatch)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if computed > 0 {
			m.logf("记忆[%s]：向量补齐 %d 条（模型 %s）", m.opt.Character, computed, m.embed.Model())
		}
		cur, _, curVer := m.rawSnapshot()
		if err == nil && curVer != ver {
			continue // 算的时候又落了库，这份快照旧了：再来一趟，先不定标志
		}
		var merr error
		missing, merr = m.index.MissingVectors(cur) // 数此刻的条目，不数更早的
		if err == nil {
			err = merr
		}
		switch {
		case err != nil:
			m.degraded.Store(true)
			m.logf("记忆[%s]：向量没算成（嵌入出错），还缺 %d 条，已排进补齐", m.opt.Character, missing)
		case missing > 0:
			m.degraded.Store(true)
			m.logf("记忆[%s]：向量没算成（嵌入不可用），还缺 %d 条，已排进补齐", m.opt.Character, missing)
		default:
			if m.degraded.Swap(false) {
				m.logf("记忆[%s]：向量补齐了", m.opt.Character)
			}
		}
		// 标志定好以后又落了库：那一条的 kickVectors 看到的可能还是旧标志（降级就不排），不再来一趟它就没人补
		if _, _, v := m.rawSnapshot(); err != nil || v == curVer {
			return missing, err
		}
	}
}

// noteVectors：Open 时同步过一遍向量以后，定一下降级标志
func (m *Memory) noteVectors(items []Item) {
	missing, err := m.index.MissingVectors(items)
	m.degraded.Store(err != nil || missing > 0)
}
