package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// unloadSettle：卸载以后等多久再量内存（ORT 的会话释放慢）。测试里设成 0。
var unloadSettle = 3 * time.Second

const (
	gteID = "gte-multilingual-base"
	bgeID = "bge-reranker-v2-m3"

	batchSize     = 16  // 一批嵌入送几条
	speedBatch    = 64  // "64 条长短不一一批"
	longTokens    = 256 // 超过这么多 token 的样本单独一条一条送，放在最后（内存第 ② 个数在它们之后量）
	warmupText    = "预热用的一句话，不在样本里"
	warmupQuery   = "预热用的查询"
	warmupDoc     = "预热用的文档"
	requestBudget = 15 * time.Minute
)

// f32s 在 JSON 里写成 base64（小端 float32）：-ref-out / -ref-in 的参照文件里有几千个 768 维向量，写成十进制文本又大又有取整问题。
type f32s []float32

func (v f32s) MarshalJSON() ([]byte, error) {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(b))
}

func (v *f32s) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if s == "" {
		*v = nil
		return nil
	}
	out, err := decodeF32(s)
	*v = out
	return err
}

type embedRec struct {
	Status int     `json:"s"`
	Tokens int     `json:"t,omitempty"`
	Vec    f32s    `json:"v,omitempty"`
	MS     float64 `json:"ms,omitempty"` // 单句计时（Short 的样本一条一条送时的耗时）
}

type pairRec struct {
	Status int     `json:"s"`
	Score  float64 `json:"score,omitempty"`
	MS     float64 `json:"ms,omitempty"`
}

type groupRec struct {
	Status int       `json:"s"`
	Scores []float64 `json:"scores,omitempty"`
}

type rssPoint struct {
	Label string `json:"label"`
	Bytes uint64 `json:"bytes"`
}

// sideRun 是一份服务（Go 或 Python）的一遍：每个样本一条记录，下标和 plan 对齐。不含任何样本文字。
type sideRun struct {
	Engine        string              `json:"engine"`
	Pass          int                 `json:"pass"`
	PlanHash      string              `json:"planHash"`
	ReadyMS       int64               `json:"readyMs"`      // 起进程到 /health 200
	FirstEmbedMS  float64             `json:"firstEmbedMs"` // 第一次嵌入请求（含加载 gte）
	FirstRerankMS float64             `json:"firstRerankMs"`
	Load          map[string]loadInfo `json:"load,omitempty"` // Go 版日志里的 LOADED 行
	Embed         []embedRec          `json:"embed"`
	Pairs         []pairRec           `json:"pairs"`
	Groups        []groupRec          `json:"groups"`
	Zoo           []int               `json:"zoo"`
	Batch64MS     float64             `json:"batch64Ms"`
	Batch64N      int                 `json:"batch64N"`
	RSS           []rssPoint          `json:"rss,omitempty"`
	Errors        []string            `json:"errors,omitempty"` // 没送成的（网络错误之类），不是状态码
}

func (r *sideRun) fail(format string, a ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
}

// collect 对一份已经起来的服务把整个清单送一遍，结果记在 sideRun 里。严格一个请求接一个请求（一次只跑一份，也不叠着发）。
// 每段文字在这一遍里只问一次，服务的缓存帮不上忙，单句计时因此是冷的。
func collect(ctx context.Context, p *serverProc, pl *plan, pass int) (*sideRun, error) {
	run := &sideRun{Engine: p.l.Engine, Pass: pass, PlanHash: pl.Hash,
		Embed: make([]embedRec, len(pl.Texts)), Pairs: make([]pairRec, len(pl.Pairs)),
		Groups: make([]groupRec, len(pl.Groups)), Zoo: make([]int, len(pl.Zoo))}
	var err error
	if run.ReadyMS, err = p.waitReady(ctx, 3*time.Minute); err != nil {
		return nil, err
	}
	cli := p.cli
	rss := func(label string) {
		if p.l.Engine != "go" {
			return // Python 版的 /health 没有 rss_bytes
		}
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if h, _, err := cli.health(hctx); err == nil {
			run.RSS = append(run.RSS, rssPoint{label, h.RSSBytes})
		}
	}
	rss("ready")

	// 预热：第一次嵌入含加载 gte。这一句不在样本里。
	warm, err := cli.embed(ctx, gteID, []string{warmupText}, typeDoc)
	if err != nil || warm.Status != 200 {
		return nil, fmt.Errorf("%s 第一次嵌入失败：状态 %d %v\n%s", p.l.Engine, warm.Status, err, p.log.tail(6))
	}
	run.FirstEmbedMS = warm.MS
	rss("gte-loaded")

	// 单独一个空白 / 控制字符：只比状态码
	for i, z := range pl.Zoo {
		r, err := cli.embed(ctx, gteID, []string{z}, typeDoc)
		if err != nil {
			run.fail("zoo#%d：%v", i, err)
		}
		run.Zoo[i] = r.Status
	}
	record := func(idxs []int, res embedResult) {
		for k, idx := range idxs {
			rec := embedRec{Status: res.Status}
			if res.Status == 200 {
				if len(idxs) == 1 { // usage 是整批的总数，只有单条请求才是这一条的 token 数
					rec.Tokens = res.Tokens
				}
				rec.Vec = res.Vecs[k]
			}
			run.Embed[idx] = rec
		}
	}
	// 按空白算空的样本：只比状态码，一条一条送
	var shorts, longs, speed []int
	rest := map[string][]int{}
	inSpeed := map[int]bool{}
	for i, s := range pl.Texts {
		if s.Set == setCorpusItems && len(speed) < speedBatch && s.Skip == "" {
			speed = append(speed, i)
			inSpeed[i] = true
		}
	}
	for i, s := range pl.Texts {
		switch {
		case s.Skip == skipOverLimit:
		case s.Skip == skipBlank:
			r, err := cli.embed(ctx, gteID, []string{s.Text}, s.Type)
			if err != nil {
				run.fail("%s：%v", s.label(), err)
			}
			run.Embed[i] = embedRec{Status: r.Status}
		case inSpeed[i]:
		case s.Short:
			shorts = append(shorts, i)
		case s.Tokens > longTokens:
			longs = append(longs, i)
		default:
			rest[s.Type] = append(rest[s.Type], i)
		}
	}
	// 64 条长短不一的一批（速度表里的一行；向量也记下来）
	if len(speed) > 0 {
		texts := make([]string, len(speed))
		for k, i := range speed {
			texts[k] = pl.Texts[i].Text
		}
		r, err := cli.embed(ctx, gteID, texts, typeDoc)
		if err != nil || r.Status != 200 {
			run.fail("64 条一批：状态 %d %v", r.Status, err)
		} else {
			run.Batch64MS, run.Batch64N = r.MS, len(speed)
			record(speed, r)
		}
	}
	// 短句一条一条送：每条的耗时是 p50 / p95 的样本
	for _, i := range shorts {
		r, err := cli.embed(ctx, gteID, []string{pl.Texts[i].Text}, pl.Texts[i].Type)
		if err != nil || r.Status != 200 {
			run.fail("%s：状态 %d %v", pl.Texts[i].label(), r.Status, err)
			continue
		}
		record([]int{i}, r)
		run.Embed[i].MS = r.MS
	}
	// 其余的按 input_type 分批
	for _, typ := range []string{typeDoc, typeQuery} {
		idxs := rest[typ]
		for start := 0; start < len(idxs); start += batchSize {
			chunk := idxs[start:min(start+batchSize, len(idxs))]
			texts := make([]string, len(chunk))
			for k, i := range chunk {
				texts[k] = pl.Texts[i].Text
			}
			r, err := cli.embed(ctx, gteID, texts, typ)
			if err != nil || r.Status != 200 {
				run.fail("一批 %d 条（%s 起）：状态 %d %v", len(chunk), pl.Texts[chunk[0]].label(), r.Status, err)
				continue
			}
			record(chunk, r)
		}
	}
	// 长的单独一条一条送，按 token 从少到多
	sort.SliceStable(longs, func(a, b int) bool { return pl.Texts[longs[a]].Tokens < pl.Texts[longs[b]].Tokens })
	for _, i := range longs {
		r, err := cli.embed(ctx, gteID, []string{pl.Texts[i].Text}, pl.Texts[i].Type)
		if err != nil || r.Status != 200 {
			run.fail("%s：状态 %d %v", pl.Texts[i].label(), r.Status, err)
			continue
		}
		record([]int{i}, r)
	}
	rss("after-long")

	// 重排：先一次预热（含加载 bge），再一对一对、一组一组
	wr, err := cli.rerank(ctx, bgeID, warmupQuery, []string{warmupDoc})
	if err != nil || wr.Status != 200 {
		run.fail("重排第一次：状态 %d %v", wr.Status, err)
	}
	run.FirstRerankMS = wr.MS
	for i, pr := range pl.Pairs {
		r, err := cli.rerank(ctx, bgeID, pr.Q, []string{pr.D})
		if err != nil {
			run.fail("%s：%v", pr.label(), err)
		}
		run.Pairs[i] = pairRec{Status: r.Status, MS: r.MS}
		if r.Status == 200 {
			run.Pairs[i].Score = r.Scores[0]
		}
	}
	for i, g := range pl.Groups {
		r, err := cli.rerank(ctx, bgeID, g.Q, g.Docs)
		if err != nil {
			run.fail("%s#%d：%v", g.Set, g.Idx, err)
		}
		run.Groups[i] = groupRec{Status: r.Status, Scores: r.Scores}
	}

	// 两个模型都卸载（同时清缓存）：回落后的内存是第 ③ 个数。ORT 的会话释放慢，等一会儿再量
	for _, id := range []string{gteID, bgeID} {
		if st, err := cli.unload(ctx, id); err != nil || st != 200 {
			run.fail("卸载 %s：状态 %d %v", id, st, err)
		}
	}
	time.Sleep(unloadSettle)
	rss("after-unload")
	if p.l.Engine == "go" {
		run.Load = parseLoaded(p.log.snapshot())
	}
	return run, nil
}
