package memory

import (
	"context"
	"fmt"
	"time"

	"sumika-native/internal/embed"
)

// 句向量预热（规格-记忆写入上线 W1a）：换模型当天 memory_sentences 里全是旧模型的行，守卫每次判断都得在 2 秒里现算，
// 算不完就掉到只比原文的退路——这段时间发送却已经放行，是自己造成的降级。所以嵌入就绪以后，后台先把守卫能用的全部条目
// （不按某个听众的 denied 取：谁来问都够用）的句向量补齐；补齐之前 GuardLevel 报 literal-pending，发送留在试运行（outbound.go）。
// 补向量时每批各拿一次 sentenceGate、批与批之间放开，守卫的调用最多等一批；整段不持锁。

// sentenceKey 是 memory_sentences 的一行（主键）
type sentenceKey struct {
	ref string
	idx int
}

// missingSentences 数这批条目还缺哪些句向量（按条目顺序、句序），以及库里已经没用了的行（条目不在这批里、句序超出现在的句数）。
// 缺的 = 没有这一行，或者模型、指纹不对，或者向量字节数不对；库里模型不同的旧行不算 stale，它们会被新行按主键覆盖。
// 只读 length(vec)，不把向量读出来：几千行的扫描不该读几千个 3 KB
func (x *Index) missingSentences(ctx context.Context, items []Item, model string) (todo []sentenceRow, stale []sentenceKey, err error) {
	type wanted struct {
		hash      string
		sentences []string
	}
	want := make(map[string]wanted, len(items))
	for _, it := range items {
		hash, ss := sentenceWant(it)
		want[it.Ref] = wanted{hash, ss}
	}
	rows, err := x.db.QueryContext(ctx, `SELECT ref, idx, model, content_hash, length(vec) FROM memory_sentences`)
	if err != nil {
		return nil, nil, fmt.Errorf("读句向量：%w", err)
	}
	have := map[sentenceKey]bool{}
	for rows.Next() {
		var ref, m, hash string
		var idx, n int
		if err := rows.Scan(&ref, &idx, &m, &hash, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		w, ok := want[ref]
		switch {
		case !ok || idx < 0 || idx >= len(w.sentences):
			stale = append(stale, sentenceKey{ref, idx})
		case m == model && hash == w.hash && n > 0 && n%4 == 0:
			have[sentenceKey{ref, idx}] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for _, it := range items {
		w := want[it.Ref]
		for i, s := range w.sentences {
			if !have[sentenceKey{it.Ref, i}] {
				todo = append(todo, sentenceRow{ref: it.Ref, idx: i, hash: w.hash, text: s})
			}
		}
	}
	return todo, stale, nil
}

// warmSentences 把这批条目缺的句向量补齐，每 batch 句一批；全部补齐以后顺手删掉没用了的行（旧模型的、已经不在的条目的）。
// 返回补了几句。ctx 取消或嵌入出错就停在这一批，已经写进去的留着，下一趟接着补
func (x *Index) warmSentences(ctx context.Context, items []Item, e embed.Embedder, batch int) (filled int, err error) {
	todo, stale, err := x.missingSentences(ctx, items, e.Model())
	if err != nil {
		return 0, err
	}
	for start := 0; start < len(todo); start += batch {
		part := todo[start:min(start+batch, len(todo))]
		if err := x.fillSentences(ctx, e, part); err != nil {
			return filled, err
		}
		filled += len(part)
	}
	for _, k := range stale {
		if _, err := x.db.ExecContext(ctx, `DELETE FROM memory_sentences WHERE ref = ? AND idx = ?`, k.ref, k.idx); err != nil {
			return filled, err
		}
	}
	return filled, nil
}

// fillSentences 算并写一批：拿 sentenceGate（等的时候认取消）→ 嵌入 → 写库 → 放锁
func (x *Index) fillSentences(ctx context.Context, e embed.Embedder, part []sentenceRow) error {
	select {
	case x.sentenceGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-x.sentenceGate }()
	texts := make([]string, len(part))
	for i, r := range part {
		texts[i] = r.text
	}
	vs, err := outboundEmbed(ctx, e, embed.TypeDocument, texts)
	if err != nil {
		return err
	}
	return x.storeSentences(ctx, e.Model(), part, vs)
}

// guardableItems 是守卫能用的全部条目：预热补它们的句向量，GuardLevel 数它们缺不缺
func guardableItems(items []Item) []Item {
	all, _ := withPrevious(items)
	out := make([]Item, 0, len(all))
	for _, it := range all {
		if guardable(it) {
			out = append(out, it)
		}
	}
	return out
}

const (
	warmRetryFirst = 30 * time.Second // 预热没做完（嵌入出错）后第一次重试等多久；之后翻倍，最长 warmRetryMax
	warmRetryMax   = 10 * time.Minute
)

// warmSentences 补齐当前模型的句向量（kickVectors 的协程在补完 memory_vectors 之后调；写入之后的那一趟也走这里）。
// 嵌入没就绪就不试（就绪时宿主会调 EmbeddingReady 再排一趟）；出错、没补完：记一行日志，过一阵自己再排一趟，间隔翻倍，不靠谁碰巧再通知
func (m *Memory) warmSentences(ctx context.Context) {
	if m.embed == nil || m.embed.Model() == "" || ctx.Err() != nil {
		return
	}
	if local, ok := m.embed.(interface{ Status() (embed.Status, string) }); ok {
		if status, _ := local.Status(); status != embed.StatusReady {
			return
		}
	}
	items, _, _ := m.rawSnapshot()
	filled, err := m.index.warmSentences(ctx, guardableItems(items), m.embed, VectorBatch)
	if ctx.Err() != nil {
		return // 关了或取消了：不算失败
	}
	m.vecMu.Lock()
	if m.warmTimer != nil {
		m.warmTimer.Stop()
		m.warmTimer = nil
	}
	if err != nil {
		delay := min(warmRetryFirst<<min(m.warmFails, 8), warmRetryMax)
		if m.warmRetry > 0 {
			delay = m.warmRetry // 测试里缩短
		}
		m.warmFails++
		if !m.vecClosed {
			m.warmTimer = time.AfterFunc(delay, func() { m.kickVectors(true) })
		}
		m.vecMu.Unlock()
		m.logf("记忆[%s]：句向量预热没做完（嵌入出错），已补 %d 句，%d 秒后再试", m.opt.Character, filled, int(delay/time.Second))
		return
	}
	m.warmFails = 0
	m.vecMu.Unlock()
	m.warmModel.Store(m.embed.Model())
	if filled > 0 {
		m.logf("记忆[%s]：句向量预热完成（模型 %s），补了 %d 句", m.opt.Character, m.embed.Model(), filled)
	}
}

// sentencesWarm 说当前模型的句向量预热做完了没有：守卫能用的全部条目的句向量都在库里。
// 做完一次就按模型名记住，之后不再查库——写入新条目以后的句向量由写入后的那一趟补和守卫按需补，不让每次 remember 都把发送钳回试运行。
// 没记住之前每次现数（几毫秒）：重启后库里本来就是齐的，不用等后台那一趟跑完才能 full
func (m *Memory) sentencesWarm(ctx context.Context) (bool, error) {
	if m.opt.Shared != nil {
		warm, err := m.opt.Shared.sentencesWarm(ctx)
		if !warm || err != nil {
			return warm, err
		}
	}
	model := m.embed.Model()
	if done, _ := m.warmModel.Load().(string); done == model {
		return true, nil
	}
	items, _, _ := m.rawSnapshot()
	todo, _, err := m.index.missingSentences(ctx, guardableItems(items), model)
	if err != nil {
		return false, err
	}
	if len(todo) > 0 {
		return false, nil
	}
	m.warmModel.Store(model)
	return true, nil
}
