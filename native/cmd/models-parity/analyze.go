package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// Row 是报告里的一行：一个比较项、量到的数、门槛、结论。输出（终端和 JSON）里只有计数和统计，没有样本文字。
type Row struct {
	Stage   string `json:"stage"`
	What    string `json:"what"`
	Value   string `json:"value"`
	Gate    string `json:"gate,omitempty"`
	Verdict string `json:"verdict"`
}

// 结论的几种。"没量够"、"待定"和"没跑"都不是通过。
const (
	vPass   = "通过"
	vFail   = "不过"
	vThin   = "没量够"
	vWait   = "待定" // 低于规格下限、但已登记在案并锁住实测值（±pinLock）、等主人定的项
	vInfo   = "记录"
	vNotRun = "没跑"
)

// pinnedCosines：id 不同的已登记差异里，余弦低于规格第五节 0.95 下限的那些样本，锁在实测值 ±pinLock（和 GM3 的 pinnedDiffCosine 同一个办法）。
// 低于下限的不是"登记了就行"（规格），所以它们的结论是"待定"，不是"通过"；锁住的目的是以后分词或引擎任何一处变了，这个数一动就红。
//
//go:embed testdata/pinned-cosines.json
var pinnedJSON []byte

const pinLock = 1e-3

type pinEntry struct {
	Cats []string `json:"cats"`
	Cos  float64  `json:"cos"`
}

func loadPins() map[string]pinEntry {
	var f struct {
		Pins map[string]pinEntry `json:"pins"`
	}
	if err := json.Unmarshal(pinnedJSON, &f); err != nil {
		panic("testdata/pinned-cosines.json 解析不了：" + err.Error())
	}
	return f.Pins
}

// regVerdict：漂了或没登记就不过；都锁住但有低于下限的，是待定；都 ≥ 下限才是通过。
func regVerdict(bad, anyPinnedLow bool) string {
	switch {
	case bad:
		return vFail
	case anyPinnedLow:
		return vWait
	}
	return vPass
}

func verdictOf(ok bool) string {
	if ok {
		return vPass
	}
	return vFail
}

// 内存各点的上限（规格第五节"内存"一行），MiB。
const (
	ortRSSMiB       = 383 // GM2 实测加载 gte 后 371～383 MB
	tokenizerMiB    = 22  // GM1 量出的分词器常驻
	rssSlackMiB     = 50  // 规格写的余量
	rssLoadedLimit  = ortRSSMiB + tokenizerMiB + rssSlackMiB
	speedP50Ratio   = 1.1 // 单句和重排每对：Go 的 p50 不超过 Python 的 1.1 倍
	speedP95Limit   = 60  // 单句 p95，毫秒
	speedBatchLimit = 3400
	coldLoadLimit   = 2000
)

type analysis struct {
	o      *options
	pl     *plan
	tok    *tokenResult
	goRuns []*sideRun
	pyRuns []*sideRun
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

func pctile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

// pickVerdict：文字的分词判定。分词那段没跑（没给 -python）就当相同，向量比较照最严的门槛。
func (a *analysis) pickVerdict(text string) verdict {
	if v, ok := a.tok.single(text); ok {
		return v
	}
	return verdict{Same: true}
}

func (a *analysis) pairVerdict(q, d string) verdict {
	if v, ok := a.tok.Pair[[2]string{q, d}]; ok {
		return v
	}
	return verdict{Same: true}
}

// vectors 给某一遍的某个样本取单位向量（和客户端存向量前 embed.Unit 一样）；没有返回 nil。
func unitOf(run *sideRun, i int) []float32 {
	if run == nil || i >= len(run.Embed) || run.Embed[i].Status != 200 || len(run.Embed[i].Vec) == 0 {
		return nil
	}
	return embed.Unit(run.Embed[i].Vec)
}

func (a *analysis) all() []Row {
	var rows []Row
	rows = append(rows, a.embedRows()...)
	rows = append(rows, a.statusRows()...)
	rows = append(rows, a.noiseRows()...)
	rows = append(rows, a.rerankRows()...)
	rows = append(rows, a.simRows()...)
	rows = append(rows, a.speedRows()...)
	rows = append(rows, a.memoryRows()...)
	return rows
}

func (a *analysis) firstRuns() (g, p *sideRun) {
	if len(a.goRuns) > 0 {
		g = a.goRuns[0]
	}
	if len(a.pyRuns) > 0 {
		p = a.pyRuns[0]
	}
	return
}

// ---- 嵌入向量 ----

func (a *analysis) embedRows() []Row {
	g, p := a.firstRuns()
	if g == nil || p == nil {
		return []Row{{Stage: "嵌入", What: "Go 的向量对 Python 的", Value: "缺一边的数据", Verdict: vNotRun}}
	}
	type bucket struct {
		n, bad, sameTok int
		maxDiff, minCos float64
		bads            []string
	}
	strict := &bucket{minCos: 1}
	reg := &bucket{minCos: 1}
	pins := loadPins()
	var regAll, regDrift, regLowNew []string // 所有登记差异的余弦清单、锁的值漂了的、低于下限又没登记锁的
	regPinned := 0                           // 低于下限、已锁住、没漂的
	missing, tokMismatch, tokBug := 0, 0, 0
	var missingL, tokL []string
	for i, s := range a.pl.Texts {
		if s.Skip != "" {
			continue
		}
		ge, pe := g.Embed[i], p.Embed[i]
		if ge.Status != 200 || pe.Status != 200 || len(ge.Vec) == 0 || len(pe.Vec) == 0 {
			missing++
			missingL = append(missingL, fmt.Sprintf("%s(Go %d/Py %d)", s.label(), ge.Status, pe.Status))
			continue
		}
		v := a.pickVerdict(s.Text)
		d, cos, err := vecDiff(embed.Unit(ge.Vec), embed.Unit(pe.Vec))
		if err != nil {
			missing++
			missingL = append(missingL, s.label()+"："+err.Error())
			continue
		}
		if ge.Tokens != 0 && pe.Tokens != 0 && ge.Tokens != pe.Tokens && v.Same {
			tokMismatch++
			tokL = append(tokL, fmt.Sprintf("%s(Go %d/Py %d)", s.label(), ge.Tokens, pe.Tokens))
		}
		switch {
		case v.Bug != "":
			tokBug++
		case v.explained():
			reg.n++
			reg.minCos = min(reg.minCos, cos)
			reg.maxDiff = max(reg.maxDiff, d)
			key := fmt.Sprintf("%s#%d", s.Set, s.Idx)
			regAll = append(regAll, fmt.Sprintf("%s %s=%.4f", key, strings.Join(v.Cats, "+"), cos))
			pin, pinned := pins[key]
			switch {
			case pinned && math.Abs(cos-pin.Cos) > pinLock:
				regDrift = append(regDrift, fmt.Sprintf("%s(锁 %.4f 现 %.4f)", s.label(), pin.Cos, cos))
			case cos < registeredCos && pinned:
				regPinned++
			case cos < registeredCos:
				regLowNew = append(regLowNew, fmt.Sprintf("%s(%.4f)", s.label(), cos))
			}
		default:
			strict.n++
			strict.minCos = min(strict.minCos, cos)
			strict.maxDiff = max(strict.maxDiff, d)
			if d > vecMaxDiff || cos < vecCosGate {
				strict.bad++
				strict.bads = append(strict.bads, fmt.Sprintf("%s(差%.2e 余弦%.9f)", s.label(), d, cos))
			}
		}
	}
	return []Row{
		{Stage: "嵌入", What: "id 相同的样本：第二层（L2 归一化后的 float32）", Gate: fmt.Sprintf("最大分量差 ≤ %.0e，余弦 ≥ 1−1e-6", vecMaxDiff),
			Value:   fmt.Sprintf("%d 个样本，最大分量差 %.3e，最小余弦 %.9f，超门槛 %d%s", strict.n, strict.maxDiff, strict.minCos, strict.bad, listOrEmpty(strict.bads)),
			Verdict: verdictOf(strict.bad == 0 && strict.n > 0)},
		{Stage: "嵌入", What: "id 不同的登记差异（T1、T5、S1）：余弦", Gate: fmt.Sprintf("余弦 ≥ %.2f；低于的要在 pinned-cosines.json 里锁住（±%.0e），结论是待定，不是通过", registeredCos, pinLock),
			Value: fmt.Sprintf("%d 个样本，最小余弦 %.6f；低于下限且锁住没漂 %d 个；锁的值漂了 %d 个%s；低于下限又没登记锁 %d 个%s",
				reg.n, reg.minCos, regPinned, len(regDrift), listOrEmpty(regDrift), len(regLowNew), listOrEmpty(regLowNew)),
			Verdict: regVerdict(len(regDrift)+len(regLowNew) > 0, regPinned > 0)},
		{Stage: "嵌入", What: "登记差异逐个的余弦（写进 pinned-cosines.json 用的，样本编号 + 类别 = 余弦）", Value: strings.Join(regAll, "；"), Verdict: vInfo},
		{Stage: "嵌入", What: "分词有未解释差异的样本（不比向量）", Gate: "0", Value: fmt.Sprintf("%d 个", tokBug), Verdict: verdictOf(tokBug == 0)},
		{Stage: "嵌入", What: "一边没出向量（状态不是 200 或向量坏了）", Gate: "0", Value: fmt.Sprintf("%d 个%s", missing, listOrEmpty(missingL)), Verdict: verdictOf(missing == 0)},
		{Stage: "嵌入", What: "id 相同的单条请求：usage 的 token 数一致", Gate: "0 个不一致", Value: fmt.Sprintf("%d 个不一致%s", tokMismatch, listOrEmpty(tokL)), Verdict: verdictOf(tokMismatch == 0)},
	}
}

// ---- 状态码 ----

func (a *analysis) statusRows() []Row {
	g, p := a.firstRuns()
	if g == nil || p == nil {
		return []Row{{Stage: "状态码", What: "两边状态码一致", Value: "缺一边的数据", Verdict: vNotRun}}
	}
	n, bad := 0, []string{}
	chk := func(label string, gs, ps int) {
		n++
		if gs != ps {
			bad = append(bad, fmt.Sprintf("%s(Go %d/Py %d)", label, gs, ps))
		}
	}
	for i, s := range a.pl.Texts {
		if s.Skip == skipBlank {
			chk(s.label(), g.Embed[i].Status, p.Embed[i].Status)
		}
	}
	for i := range a.pl.Zoo {
		chk(fmt.Sprintf("zoo#%d", i), g.Zoo[i], p.Zoo[i])
	}
	for i, pr := range a.pl.Pairs {
		chk(pr.label(), g.Pairs[i].Status, p.Pairs[i].Status)
	}
	for i, gr := range a.pl.Groups {
		chk(fmt.Sprintf("%s#%d", gr.Set, gr.Idx), g.Groups[i].Status, p.Groups[i].Status)
	}
	return []Row{{Stage: "状态码", What: "空白动物园、单独的空白输入、重排的对和组：两边状态码一致", Gate: "全部一致",
		Value: fmt.Sprintf("%d/%d 一致%s", n-len(bad), n, listOrEmpty(bad)), Verdict: verdictOf(len(bad) == 0 && n > 0)}}
}

// ---- 噪声底 ----

// runDiff 是同一个引擎两遍之间的最大分量差（对每个 200 的样本）。
func runDiff(pl *plan, x, y *sideRun) (maxDiff float64, n int) {
	for i, s := range pl.Texts {
		if s.Skip != "" {
			continue
		}
		a, b := unitOf(x, i), unitOf(y, i)
		if a == nil || b == nil {
			continue
		}
		if d, _, err := vecDiff(a, b); err == nil {
			maxDiff = max(maxDiff, d)
			n++
		}
	}
	return
}

func (a *analysis) noiseRows() []Row {
	var rows []Row
	for _, side := range []struct {
		name string
		runs []*sideRun
	}{{"Python", a.pyRuns}, {"Go", a.goRuns}} {
		if len(side.runs) < 2 {
			rows = append(rows, Row{Stage: "噪声底", What: side.name + " 自对拍（同参数，每遍重新起进程）", Value: fmt.Sprintf("只有 %d 遍，没法比", len(side.runs)), Verdict: vNotRun})
			continue
		}
		worst, total := 0.0, 0
		for k := 1; k < len(side.runs); k++ {
			d, n := runDiff(a.pl, side.runs[0], side.runs[k])
			worst, total = max(worst, d), n
		}
		// 噪声底超过门槛，门槛就没有意义了：不是门槛的问题就是环境有问题，要先查清
		rows = append(rows, Row{Stage: "噪声底", What: side.name + " 自对拍（同参数，每遍重新起进程）：N", Gate: fmt.Sprintf("N ≤ %.0e（门槛不许低于噪声底）", vecMaxDiff),
			Value: fmt.Sprintf("%d 遍，每遍 %d 个样本，最大分量差 N = %.3e", len(side.runs), total, worst), Verdict: verdictOf(worst <= vecMaxDiff)})
	}
	return rows
}

// ---- 重排 ----

func (a *analysis) rerankRows() []Row {
	g, p := a.firstRuns()
	if g == nil || p == nil {
		return []Row{{Stage: "重排", What: "Go 的分数对 Python 的", Value: "缺一边的数据", Verdict: vNotRun}}
	}
	var scoreD, logitD []float64
	regN, missing := 0, 0
	var badL []string
	cmp := func(label string, gs, ps float64, v verdict) {
		if v.Bug != "" {
			badL = append(badL, label+"(分词有未解释差异)")
			return
		}
		if v.explained() {
			regN++
			return
		}
		scoreD = append(scoreD, math.Abs(gs-ps))
		gl, ok1 := deriveLogit(gs)
		pl, ok2 := deriveLogit(ps)
		if ok1 && ok2 {
			logitD = append(logitD, math.Abs(gl-pl))
		}
	}
	for i, pr := range a.pl.Pairs {
		gr, pr2 := g.Pairs[i], p.Pairs[i]
		if gr.Status != 200 || pr2.Status != 200 {
			if gr.Status != pr2.Status {
				missing++
			}
			continue
		}
		cmp(pr.label(), gr.Score, pr2.Score, a.pairVerdict(pr.Q, pr.D))
	}
	rankSame, rankN := 0, 0
	var rankBad []string
	for i, gr := range a.pl.Groups {
		gg, pg := g.Groups[i], p.Groups[i]
		if gg.Status != 200 || pg.Status != 200 || len(gg.Scores) != len(pg.Scores) {
			if gg.Status != pg.Status {
				missing++
			}
			continue
		}
		explained := false
		for di, d := range gr.Docs {
			v := a.pairVerdict(gr.Q, d)
			explained = explained || !v.Same
			cmp(fmt.Sprintf("%s#%d/%d", gr.Set, gr.Idx, di), gg.Scores[di], pg.Scores[di], v)
		}
		if explained { // 有分词不同的文档，名次不比（分数已按各自的类别记在上面）
			continue
		}
		rankN++
		if slices.Equal(rankOrder(gg.Scores), rankOrder(pg.Scores)) {
			rankSame++
		} else {
			rankBad = append(rankBad, fmt.Sprintf("%s#%d", gr.Set, gr.Idx))
		}
	}
	ds, dl := describe(scoreD), describe(logitD)
	return []Row{
		{Stage: "重排", What: "分数（sigmoid 之后）", Gate: fmt.Sprintf("差 ≤ %.0e", scoreMaxDiff),
			Value:   fmt.Sprintf("%d 个，最大差 %.3e，中位 %.3e；分词有登记差异没比的 %d 个；状态不同 %d 个%s", ds.N, ds.Max, ds.Median, regN, missing, listOrEmpty(badL)),
			Verdict: verdictOf(ds.N > 0 && ds.Max <= scoreMaxDiff && missing == 0 && len(badL) == 0)},
		{Stage: "重排", What: "logit（由分数反推，|logit| ≤ 12 的才算）", Gate: fmt.Sprintf("差 ≤ %.0e", logitMaxDiff),
			Value: fmt.Sprintf("%d 个，最大差 %.3e", dl.N, dl.Max), Verdict: verdictOf(dl.N > 0 && dl.Max <= logitMaxDiff)},
		{Stage: "重排", What: "每个查询的排序完全相同（同分按下标）", Gate: "全部相同",
			Value: fmt.Sprintf("%d/%d 组相同%s", rankSame, rankN, listOrEmpty(rankBad)), Verdict: verdictOf(rankN > 0 && rankSame == rankN)},
	}
}

// rankOrder：按分数从高到低的下标，同分按下标。
func rankOrder(scores []float64) []int {
	idx := make([]int, len(scores))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool { return scores[idx[x]] > scores[idx[y]] })
	return idx
}

// ---- 相似度差和翻转 ----

func (a *analysis) simRows() []Row {
	g, p := a.firstRuns()
	if g == nil || p == nil {
		return []Row{{Stage: "相似度", What: "相似度差和翻转", Value: "缺一边的数据", Verdict: vNotRun}}
	}
	var rows []Row
	for _, set := range []struct {
		name                  string
		items, queries, sents []string
	}{
		{"编的语料", []string{setCorpusItems}, []string{setCorpusQ}, []string{setCorpusSent}},
		{"真实样本", []string{setRealItems}, []string{setRealQ, setRealGroup}, []string{setRealSent}},
	} {
		items, queries, sents := a.simIndexes(set.items), a.simIndexes(set.queries), a.simIndexes(set.sents)
		if len(items) == 0 {
			if set.name == "真实样本" && !a.pl.Real {
				continue
			}
			rows = append(rows, Row{Stage: "相似度", What: set.name, Value: "没有可比的条目", Verdict: vNotRun})
			continue
		}
		type threshold struct {
			simThreshold
			first, second []int
			triangle      bool // 同一批里两两配（i<j），不配自己
		}
		ths := []threshold{
			{simRecall, queries, items, false},
			{simGuard, sents, sents, true},
			{simDup, items, items, true},
		}
		gu, pu := map[int][]float32{}, map[int][]float32{} // 每个样本的单位向量只算一次
		for _, idxs := range [][]int{queries, items, sents} {
			for _, i := range idxs {
				gu[i], pu[i] = unitOf(g, i), unitOf(p, i)
			}
		}
		for _, th := range ths {
			var gg, pp, gp []float32
			for x, i := range th.first {
				for y, j := range th.second {
					if th.triangle && y <= x {
						continue
					}
					gi, pi, gj, pj := gu[i], pu[i], gu[j], pu[j]
					gg = append(gg, embed.Dot(gi, gj)) // Go 的查询 × Go 的库
					pp = append(pp, embed.Dot(pi, pj)) // Python × Python（参照）
					gp = append(gp, embed.Dot(gi, pj)) // Go 的查询 × Python 的库（开发评测时库副本常是 Python 算的向量）
				}
			}
			for _, col := range []struct {
				name string
				a    []float32
			}{{"Go×Go 对 Python×Python", gg}, {"Go 查询×Python 库 对 Python×Python", gp}} {
				rep, err := AnalyzeFlips(FlipInput{Name: th.name, Threshold: th.t, EffectiveThreshold: th.eff, Reaches: th.reaches, A: col.a, B: pp})
				if err != nil {
					rows = append(rows, Row{Stage: "相似度", What: th.name, Value: err.Error(), Verdict: vFail})
					continue
				}
				rows = append(rows, simRow(set.name, col.name, rep))
			}
		}
	}
	return rows
}

// simThreshold 是一个门槛：名字、声明的值、比较函数实际在比的值（守卫把 0.70 转成 float32 再比，它比 0.70 小一点）、生产的比较函数。
// 比较函数全是 memory 包里生产用的那几个，这里不写第二份（TestSimThresholdsMatchProduction 钉住声明和函数对得上）。
type simThreshold struct {
	name    string
	t, eff  float64
	reaches func(score float32) bool
}

var (
	simRecall = simThreshold{"召回底线", memory.SimilarityFloor, memory.SimilarityFloor, memory.RecallReaches}
	simGuard  = simThreshold{"守卫", memory.GuardThreshold, float64(float32(memory.GuardThreshold)), func(s float32) bool { return memory.GuardReaches(s, memory.GuardThreshold) }}
	simDup    = simThreshold{"近重复", memory.DupSimilarity, memory.DupSimilarity, memory.NearDuplicateReaches}
)

// simIndexes：某些来源里两边都出了向量、分词没有任何差异的样本下标（有登记差异的不进，它们的相似度本来就会不一样）。
func (a *analysis) simIndexes(sets []string) []int {
	g, p := a.firstRuns()
	var out []int
	for _, i := range a.pl.set(sets...) {
		if unitOf(g, i) != nil && unitOf(p, i) != nil && a.pickVerdict(a.pl.Texts[i].Text).Same {
			out = append(out, i)
		}
	}
	return out
}

func simRow(set, col string, r FlipReport) Row {
	verdict := vPass
	switch {
	case r.Diff.Max > simDCeiling || r.Diff.Max > simD || r.WrongFlip > 0:
		verdict = vFail
	case r.NotEnough:
		verdict = vThin
	}
	note := ""
	if r.NotEnough {
		note = fmt.Sprintf("；门槛 ±%.2f 内只有 %d 对（< %d），这一边没量够", nearBand, r.Near, nearMinPairs)
	}
	return Row{Stage: "相似度", What: fmt.Sprintf("%s · %s · 门槛 %.2f · %s", set, r.Name, r.Threshold, col),
		Gate: fmt.Sprintf("D ≤ %.0e（不许松过 %.0e），翻转的距离都 ≤ D", simD, simDCeiling),
		Value: fmt.Sprintf("%d 对，D = %.3e（中位 %.2e，p99 %.2e），翻转 %d 个，翻转到门槛的最大距离 %.2e，距离 > D 的 %d 个，±%.2f 内 %d 对，离门槛最近 %.2e%s",
			r.Pairs, r.Diff.Max, r.Diff.Median, r.Diff.P99, r.Flips, r.FlipDist, r.WrongFlip, nearBand, r.Near, r.MinMargin, note),
		Verdict: verdict}
}

// ---- 速度 ----

func (a *analysis) speedRows() []Row {
	if len(a.goRuns) == 0 || len(a.pyRuns) == 0 {
		return []Row{{Stage: "速度", What: "Go 对 Python", Value: "缺一边的数据", Verdict: vNotRun}}
	}
	singles := func(runs []*sideRun) (all []float64) {
		for _, r := range runs {
			for i, s := range a.pl.Texts {
				if s.Short && r.Embed[i].Status == 200 && r.Embed[i].MS > 0 {
					all = append(all, r.Embed[i].MS)
				}
			}
		}
		return
	}
	pairMS := func(runs []*sideRun) (all []float64) {
		for _, r := range runs {
			for _, pr := range r.Pairs {
				if pr.Status == 200 && pr.MS > 0 {
					all = append(all, pr.MS)
				}
			}
		}
		return
	}
	per := func(runs []*sideRun, f func(*sideRun) float64) (xs []float64) {
		for _, r := range runs {
			if v := f(r); v > 0 {
				xs = append(xs, v)
			}
		}
		return
	}
	gs, ps := singles(a.goRuns), singles(a.pyRuns)
	g50, p50, g95 := pctile(gs, .5), pctile(ps, .5), pctile(gs, .95)
	var rows []Row
	ratio := 0.0
	if p50 > 0 {
		ratio = g50 / p50
	}
	rows = append(rows, Row{Stage: "速度", What: fmt.Sprintf("单句（≤ 64 token，%d 句 × %d 遍，Go/Python 各一遍一遍交替）p50", len(gs)/max(len(a.goRuns), 1), len(a.goRuns)),
		Gate: fmt.Sprintf("Go 的 p50 ≤ Python 的 %.1f 倍", speedP50Ratio), Value: fmt.Sprintf("Go %.1f ms，Python %.1f ms，Go/Python = %.3f", g50, p50, ratio), Verdict: verdictOf(p50 > 0 && g50 > 0 && ratio <= speedP50Ratio)})
	rows = append(rows, Row{Stage: "速度", What: "单句 p95（Go）", Gate: fmt.Sprintf("≤ %d ms", speedP95Limit), Value: fmt.Sprintf("%.1f ms（Python %.1f ms）", g95, pctile(ps, .95)), Verdict: verdictOf(g95 > 0 && g95 <= speedP95Limit)})
	gb, pb := medianOf(per(a.goRuns, func(r *sideRun) float64 { return r.Batch64MS })), medianOf(per(a.pyRuns, func(r *sideRun) float64 { return r.Batch64MS }))
	rows = append(rows, Row{Stage: "速度", What: "64 条长短不一的一批", Gate: fmt.Sprintf("Go ≤ %d ms", speedBatchLimit), Value: fmt.Sprintf("Go %.0f ms，Python %.0f ms", gb, pb), Verdict: verdictOf(gb > 0 && gb <= speedBatchLimit)})
	var cold, coldHash []float64
	var loadedLines []string
	for _, r := range a.goRuns {
		for _, id := range []string{gteID, bgeID} {
			if l, ok := r.Load[id]; ok {
				loadedLines = append(loadedLines, fmt.Sprintf("第 %d 遍 %s load_ms=%d hash_ms=%d", r.Pass, id, l.LoadMS, l.HashMS))
			}
		}
		if l, ok := r.Load[gteID]; ok {
			cold = append(cold, float64(l.LoadMS-l.HashMS))
			coldHash = append(coldHash, float64(l.LoadMS))
		}
	}
	cm := medianOf(cold)
	rows = append(rows, Row{Stage: "速度", What: "gte 冷加载（Go 日志的 LOADED：load_ms 减 hash_ms，不含 onnx 首次哈希；gte 的 onnx 哈希在启动检查里算过了，所以它的 hash_ms 是 0）", Gate: fmt.Sprintf("≤ %d ms", coldLoadLimit),
		Value:   fmt.Sprintf("%.0f ms（LOADED 的 load_ms %.0f ms）；起进程到就绪（Go 含启动检查里的哈希）Go %.0f ms，Python %.0f ms；第一次嵌入请求 Go %.0f ms，Python %.0f ms", cm, medianOf(coldHash), medianOf(per(a.goRuns, func(r *sideRun) float64 { return float64(r.ReadyMS) })), medianOf(per(a.pyRuns, func(r *sideRun) float64 { return float64(r.ReadyMS) })), medianOf(per(a.goRuns, func(r *sideRun) float64 { return r.FirstEmbedMS })), medianOf(per(a.pyRuns, func(r *sideRun) float64 { return r.FirstEmbedMS }))),
		Verdict: verdictOf(len(cold) > 0 && cm > 0 && cm <= coldLoadLimit)}) // 0 不算：没读到 LOADED 行的数不能当成"很快"
	rows = append(rows, Row{Stage: "速度", What: "Go 日志里的 LOADED 行（load_ms、hash_ms）", Value: strings.Join(loadedLines, "；"), Verdict: vInfo})
	gp, pp := pairMS(a.goRuns), pairMS(a.pyRuns)
	rr := 0.0
	if pctile(pp, .5) > 0 {
		rr = pctile(gp, .5) / pctile(pp, .5)
	}
	rows = append(rows, Row{Stage: "速度", What: "重排每对（一对一请求）p50", Gate: fmt.Sprintf("Go ≤ Python 的 %.1f 倍", speedP50Ratio),
		Value: fmt.Sprintf("Go %.1f ms，Python %.1f ms，Go/Python = %.3f（%d 对 × %d 遍）", pctile(gp, .5), pctile(pp, .5), rr, len(a.pl.Pairs), len(a.goRuns)), Verdict: verdictOf(rr > 0 && rr <= speedP50Ratio)})
	return rows
}

func medianOf(xs []float64) float64 { return pctile(xs, .5) }

// longestSent：这次实际送出去（没被跳过）的最长样本有多少 token（Go 分词器数的，含两个特殊符号）。
func (a *analysis) longestSent() int {
	n := 0
	for _, s := range a.pl.Texts {
		if s.Skip == "" {
			n = max(n, s.Tokens)
		}
	}
	return n
}

// ---- 内存（Go 的 /health 里的 rss_bytes）----

func (a *analysis) memoryRows() []Row {
	if len(a.goRuns) == 0 {
		return []Row{{Stage: "内存", What: "rss_bytes", Value: "没有 Go 的数据", Verdict: vNotRun}}
	}
	get := func(label string) (xs []float64) {
		for _, r := range a.goRuns {
			for _, pt := range r.RSS {
				if pt.Label == label {
					xs = append(xs, mib(pt.Bytes))
				}
			}
		}
		return
	}
	ready, loaded, afterLong, afterUnload := medianOf(get("ready")), medianOf(get("gte-loaded")), medianOf(get("after-long")), medianOf(get("after-unload"))
	if loaded == 0 {
		return []Row{{Stage: "内存", What: "rss_bytes", Value: "读不到", Verdict: vNotRun}}
	}
	return []Row{
		{Stage: "内存", What: "① 加载 gte 并跑过第一句以后", Gate: fmt.Sprintf("≤ %d MiB（ORT %d + 分词器 %d + 余量 %d，规格第五节的公式，没有放宽）", rssLoadedLimit, ortRSSMiB, tokenizerMiB, rssSlackMiB),
			Value: fmt.Sprintf("%.0f MiB（刚就绪 %.0f MiB）", loaded, ready), Verdict: verdictOf(loaded <= rssLoadedLimit)},
		{Stage: "内存", What: fmt.Sprintf("② 送完清单里最长的那些样本（单条 > %d token 的一条一条送；这次送出去最长的是 %d token）之后", longTokens, a.longestSent()),
			Gate:  fmt.Sprintf("≤ ① + %d MiB（清单里最长的一条接近生产的 2048 上限；预热 500 次变长请求加一万次小请求那一项归 cmd/sumika-models 的 TestRealExeSoak，GM5 验收 11）", rssSlackMiB),
			Value: fmt.Sprintf("%.0f MiB（比 ① 多 %.0f）", afterLong, afterLong-loaded), Verdict: verdictOf(afterLong <= loaded+rssSlackMiB)},
		{Stage: "内存", What: "③ gte 和 bge 都卸载以后", Gate: fmt.Sprintf("≤ 刚就绪 + %d MiB", rssSlackMiB),
			Value: fmt.Sprintf("%.0f MiB（刚就绪 %.0f MiB）", afterUnload, ready), Verdict: verdictOf(afterUnload > 0 && afterUnload <= ready+rssSlackMiB)},
		{Stage: "内存", What: "④ 预热 500 次变长请求后一万次小请求前后增长", Value: "不在这里量：cmd/sumika-models 的 TestRealExeSoak 管（GM5 验收 11）", Verdict: vInfo},
	}
}
