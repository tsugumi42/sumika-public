package memory

import (
	"context"
	"fmt"

	"sumika-native/internal/embed"
)

// VectorBatch 是补向量（memory_vectors、memory_sentences）一批最多算几条（规格-记忆写入上线 W1a：起步 8）。
// 旧桥（Python）整个请求持一把锁，一批 64 条要 3.4 秒，而守卫的嵌入时限是 2 秒、召回是 3 秒，换模型后重算的那段时间
// 每次撞上大批都超时、走 embed-down 退路；8 条一批每批不到半秒，取消的粒度也细。最终数以 GM7 的实测为准
const VectorBatch = 8

// guardable：守卫比的是哪些条目——有效的，和待确认的事实。checkOutbound 挑被禁止的条目、句向量预热挑要预热的条目，
// 都用它，两处不会各写一份
func guardable(it Item) bool {
	return it.Status == "active" || (it.Type == "fact" && it.Status == "candidate")
}

// withPrevious 是守卫要保护的全部条目：快照里的，加上它们带着的旧版本（Item.Previous）。
// 旧版本会随新值一起注入给她（召回不分场合），说出去和新值一样要拦；状态记成 active 才过得了 guardable。
// 旧版本的范围包含新值的范围（fillPrevious 保证），所以不会比新值拦得更宽。parent 是旧版本 ref → 带着它的新值 ref，共用池的归属靠它认
func withPrevious(items []Item) (all []Item, parent map[string]string) {
	all = make([]Item, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		seen[it.Ref] = true
	}
	for _, it := range items {
		all = append(all, it)
		for _, p := range it.Previous {
			if seen[p.Ref] {
				continue
			}
			seen[p.Ref] = true
			if parent == nil {
				parent = map[string]string{}
			}
			parent[p.Ref] = it.Ref
			p.Status, p.Previous = "active", nil
			all = append(all, p)
		}
	}
	return all, parent
}

// sentenceWant 是一条条目的句向量该是什么样：指纹和切出来的句子。指纹带 VectorPrompt，改正文、换提示词版本都会让旧行作废
func sentenceWant(it Item) (hash string, sentences []string) {
	return contentHash(VectorPrompt + "\x00" + it.Text), OutboundSentences(it.Text)
}

// sentenceRow 是一句要写进 memory_sentences 的话
type sentenceRow struct {
	ref  string
	idx  int
	hash string
	text string
}

// storeSentences 把一批句向量写进 memory_sentences（一个事务；已有的覆盖）。vs 和 rows 一一对应
func (x *Index) storeSentences(ctx context.Context, model string, rows []sentenceRow, vs [][]float32) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for i, key := range rows {
		_, err = tx.ExecContext(ctx, `INSERT INTO memory_sentences (ref, idx, model, content_hash, vec) VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(ref, idx) DO UPDATE SET model=excluded.model, content_hash=excluded.content_hash, vec=excluded.vec`,
			key.ref, key.idx, model, key.hash, encodeVec(vs[i]))
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// outboundVectors 只读现行模型、现行正文的句向量；缺的在同一个嵌入时限内补齐。
// 缓存留在派生库，原文仍只在条目快照里；改正文、换模型都不会混用旧向量。
// dim 是现行模型查询向量的维度；缓存里字节被截断、维度对不上的也当作没有，重算并覆盖，不让一行坏缓存长期把守卫压在退路上。
// 这是按需补：预热（sentences.go）没做完、或者刚写入的条目还没补上时，守卫自己在 2 秒里补它这一次要比的那些
func (x *Index) outboundVectors(ctx context.Context, items []Item, e embed.Embedder, dim int) (map[string][][]float32, error) {
	// 等另一个补齐者时也认整体取消。
	select {
	case x.sentenceGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-x.sentenceGate }()
	type wanted struct {
		hash      string
		sentences []string
	}
	want := make(map[string]wanted, len(items))
	out := make(map[string][][]float32, len(items))
	for _, it := range items {
		hash, ss := sentenceWant(it)
		want[it.Ref] = wanted{hash, ss}
		out[it.Ref] = make([][]float32, len(ss))
	}
	rows, err := x.db.QueryContext(ctx, `SELECT ref, idx, model, content_hash, vec FROM memory_sentences`)
	if err != nil {
		return nil, fmt.Errorf("读守卫句向量：%w", err)
	}
	for rows.Next() {
		var ref, model, hash string
		var idx int
		var blob []byte
		if err := rows.Scan(&ref, &idx, &model, &hash, &blob); err != nil {
			rows.Close()
			return nil, err
		}
		w, ok := want[ref]
		if ok && model == e.Model() && hash == w.hash && idx >= 0 && idx < len(w.sentences) {
			// 缓存里的向量坏了（字节被截断、维度不对、含 NaN / Inf、全零）就当没有，下面重算并覆盖；不然点积是 NaN，会被当成"不像"放行。
			if u, valid := outboundUnit(decodeVec(blob)); valid && len(blob)%4 == 0 && len(u) == dim {
				out[ref][idx] = u
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var todo []sentenceRow
	var texts []string
	for _, it := range items {
		for i, s := range want[it.Ref].sentences {
			if len(out[it.Ref][i]) == 0 {
				todo = append(todo, sentenceRow{ref: it.Ref, idx: i, hash: want[it.Ref].hash, text: s})
				texts = append(texts, s)
			}
		}
	}
	for start := 0; start < len(todo); start += VectorBatch {
		end := min(start+VectorBatch, len(todo))
		vs, err := outboundEmbed(ctx, e, embed.TypeDocument, texts[start:end])
		if err != nil {
			return nil, err
		}
		if err := x.storeSentences(ctx, e.Model(), todo[start:end], vs); err != nil {
			return nil, err
		}
		for i, key := range todo[start:end] {
			out[key.ref][key.idx] = vs[i]
		}
	}
	for _, vs := range out {
		for _, v := range vs {
			if len(v) != dim {
				return nil, errOutboundEmbedding
			}
		}
	}
	return out, nil
}
