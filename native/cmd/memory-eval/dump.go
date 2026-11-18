package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"sync"

	"sumika-native/internal/embed"
	"sumika-native/internal/embedinputs"
	"sumika-native/internal/memory"
)

// dumpRecorder 是 -dump-embed-inputs 用的假嵌入器和假重排：记下"这次评测实际会送给嵌入和重排的字符串"（规格-Go本地模型 第五节），
// 不连任何模型，向量是文字的哈希（只要维度固定、能归一化，召回的流程就照常走完，才能走到每一处会嵌入的地方）。
// 记在 evalEmbedder 的里面一层：evalEmbedder 已经按 Open 之前 / 之后分出条目（document）和查询（query），
// 这里再按 SetPhase 把查询分成考题和真实群消息。条目的句子不另外建模：生产里守卫嵌入的就是条目文本经 memory.OutboundSentences 切出来的。
type dumpRecorder struct {
	mu     sync.Mutex
	phase  string
	file   embedinputs.File
	seen   map[string]map[string]bool // 列表名 → 已记过的文本
	rerank int                        // 最多记多少次重排调用
}

const (
	phaseExam  = "queries"
	phaseGroup = "group"
	dumpDim    = 8
)

func newDumpRecorder(rerankCap int) *dumpRecorder {
	return &dumpRecorder{phase: phaseExam, seen: map[string]map[string]bool{}, rerank: rerankCap}
}

// SetPhase：之后的查询记进哪个列表（phaseExam、phaseGroup）。
func (r *dumpRecorder) SetPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
}

func (r *dumpRecorder) add(list *[]string, name, text string) {
	if r.seen[name] == nil {
		r.seen[name] = map[string]bool{}
	}
	if r.seen[name][text] {
		return
	}
	r.seen[name][text] = true
	*list = append(*list, text)
}

// File 返回记下的内容，并补上守卫的句子。
func (r *dumpRecorder) File() embedinputs.File {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.file
	f.Version = embedinputs.Version
	f.Sentences = nil
	seen := map[string]bool{}
	for _, it := range f.Items {
		for _, s := range memory.OutboundSentences(it) {
			if !seen[s] {
				seen[s] = true
				f.Sentences = append(f.Sentences, s)
			}
		}
	}
	return f
}

// Embedder 是假嵌入器（要实现 embed.Typed，evalEmbedder 才会按 query / document 分开调）。
func (r *dumpRecorder) Embedder() embed.Embedder { return dumpEmbedder{r} }

type dumpEmbedder struct{ r *dumpRecorder }

func (dumpEmbedder) Model() string { return "dump-embed-inputs" }

func (e dumpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.EmbedTyped(ctx, embed.TypeDocument, texts)
}

func (e dumpEmbedder) EmbedTyped(_ context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	r := e.r
	r.mu.Lock()
	for _, t := range texts {
		switch {
		case kind == embed.TypeDocument:
			r.add(&r.file.Items, "items", t)
		case r.phase == phaseGroup:
			r.add(&r.file.Group, "group", t)
		default:
			r.add(&r.file.Queries, "queries", t)
		}
	}
	r.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeVector(t)
	}
	return out, nil
}

// fakeVector：文字的哈希摊成 dumpDim 维单位向量。同一段文字永远同一个向量，空串也不是零向量。
func fakeVector(text string) []float32 {
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()
	v := make([]float32, dumpDim)
	var n float64
	for i := range v {
		seed = seed*6364136223846793005 + 1442695040888963407
		x := float64(int64(seed>>11))/float64(1<<52) - 1 // 约 [-1, 1)
		v[i] = float32(x)
		n += x * x
	}
	if n == 0 {
		v[0], n = 1, 1
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

// Reranker 是假重排：记下前 rerankCap 次调用，原样返回顺序。
func (r *dumpRecorder) Reranker() memory.Reranker { return dumpReranker{r} }

type dumpReranker struct{ r *dumpRecorder }

func (d dumpReranker) Rerank(_ context.Context, query string, docs []string) ([]int, []float64, error) {
	r := d.r
	r.mu.Lock()
	if len(r.file.Rerank) < r.rerank {
		r.file.Rerank = append(r.file.Rerank, embedinputs.RerankInput{Query: query, Docs: append([]string(nil), docs...)})
	}
	r.mu.Unlock()
	order := make([]int, len(docs))
	scores := make([]float64, len(docs))
	for i := range order {
		order[i], scores[i] = i, float64(len(docs)-i)
	}
	return order, scores, nil
}

// maxDumpRerank：最多记多少次重排调用（规格第五节"100 个查询 × 召回的 40 条候选"）。
const maxDumpRerank = 100

// finishDump 把记下的写成文件，只在终端打各列表的条数（不打任何文字）。写文件前再过一次路径检查。
func finishDump(rec *dumpRecorder, path string, groupMsgs int) {
	f := rec.File()
	if err := embedinputs.Write(path, f); err != nil {
		fail(err.Error())
	}
	fmt.Fprintf(os.Stderr, "已写 %s：条目 %d、考题查询 %d、群消息查询 %d（抽了 %d 条群消息）、守卫句子 %d、重排调用 %d 次。文件里是真实原文，用完请删掉。\n",
		path, len(f.Items), len(f.Queries), len(f.Group), groupMsgs, len(f.Sentences), len(f.Rerank))
}
