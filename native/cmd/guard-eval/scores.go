package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// 不花钱的守卫比对（规格-Go本地模型 GM7 验收 5）：-method S 只算嵌入相似度，不调确认模型，也不记 1500 次预算的账（本机嵌入不花钱）。
// 同一份数据集、同一轮次，两种引擎（-engine python / go）各跑一份，再用 -compare-scores 比：逐样本的"拦 / 不拦"决定相同，
// 每个样本的最大相似度差不超过第五节的 D（1e-5），门槛附近的情况另报。

const (
	// scoreD 是第五节"下游判断"的向量门槛 D（起始值，我定的）：所有对里相似度的最大差不许超过它；每个翻转到门槛的距离也不许超过实际量到的差
	scoreD = 1e-5
	// nearBand 是"门槛附近"的宽度（±0.01），附近不足 nearMin 对就写"这一边没量够"，不能当通过
	nearBand = 0.01
	nearMin  = 50
	// productionThreshold 是线上守卫的门槛（memory.GuardThreshold）：决定相同与否看的就是它
	productionThreshold = memory.GuardThreshold
)

// guardModel 是守卫门槛标定过的嵌入模型，评测只量它（和 memory.GuardCalibrated 一致）
const guardModel = "gte-multilingual-base"

// PartScore 是样本里一段（已放行的前几段加上本段）的相似度证据
type PartScore struct {
	Literal   bool               `json:"literal"`              // 字面先命中：线上不再算相似度（Score 是 -1）
	Score     float64            `json:"score"`                // 全部禁看条目的最大相似度；没有禁看条目或字面先命中时是 -1
	PerMemory map[string]float64 `json:"per_memory,omitempty"` // 每条禁看条目的最大相似度（条目 id → 分）
}

// ScoreRow 是 -method S 一个样本的结果。同一份数据集同一轮次下，两种引擎的行一一对应（按样本 id 和段下标）
type ScoreRow struct {
	ID     string      `json:"id"`
	Engine string      `json:"engine"`
	Model  string      `json:"model"`
	Parts  []PartScore `json:"parts"`
}

func scorePath(o options) string {
	return filepath.Join(o.data, fmt.Sprintf("round%d-S-%s.jsonl", o.round, o.engine))
}

// startEmbedding 起这次用的嵌入：python 起 -python -server 的 server.py 在 11450（原来的做法）；
// go 用 embed.Local 起 sumika-models，命令行和后台一模一样（embed.LocalConfig）。stop 里等子进程退出（Local 杀进程树）
func startEmbedding(o options) (emb embed.Typed, stop func(), err error) {
	switch o.engine {
	case "python":
		stop, err := startServer(o)
		if err != nil {
			return nil, nil, err
		}
		return embed.Connect("http://127.0.0.1:11450/v1", guardModel, 2*time.Minute), stop, nil
	case "go":
		if o.modelsExe == "" || o.modelsRoot == "" {
			return nil, nil, errors.New("-engine go 要给 -models-exe 和 -models-root（-ort-library 不给就取 sumika-models.exe 旁边的 onnxruntime.dll）")
		}
		cfg := embed.LocalConfig{Backend: embed.BackendGoModels, Model: guardModel, MaxLineTokens: embed.GoModelsMaxLineTokens}
		for _, p := range []struct {
			val string
			to  *string
		}{{o.modelsExe, &cfg.Exe}, {o.modelsRoot, &cfg.ModelsRoot}, {o.ortLibrary, &cfg.OrtLibrary}} {
			if p.val == "" {
				continue
			}
			abs, err := filepath.Abs(p.val)
			if err != nil {
				return nil, nil, err
			}
			*p.to = abs
		}
		l := embed.NewLocal(cfg, func(f string, a ...any) { fmt.Printf("sumika-models："+f+"\n", a...) })
		l.Start()
		if !l.Wait(180 * time.Second) {
			l.Stop()
			s, why := l.Status()
			return nil, nil, fmt.Errorf("sumika-models 没起来：%s %s", s, why)
		}
		fmt.Println("sumika-models started（embed.Local，和后台同一条命令行）")
		return l, func() { l.Stop(); fmt.Println("sumika-models stopped") }, nil
	}
	return nil, nil, fmt.Errorf("-engine 只有 python、go：%q", o.engine)
}

// scoreSample 算一个样本每一段的证据。每一段的文本是前面全部段加上本段（和 decide 里全放行时一样；-method S 不问确认模型，没有"被拦下的段"）
func scoreSample(ctx context.Context, emb embed.Typed, ds Dataset, s Sample, vectors map[string][]float32) ([]PartScore, error) {
	denied := forbidden(ds, s)
	out := make([]PartScore, 0, len(s.Parts))
	for i := range s.Parts {
		text := strings.Join(s.Parts[:i+1], "\n")
		ps := PartScore{Score: -1}
		ps.Literal = literal(text, denied)
		if len(denied) > 0 && !ps.Literal {
			vs, err := emb.EmbedTyped(ctx, embed.TypeQuery, memory.OutboundSentences(text))
			if err != nil {
				return nil, err
			}
			ps.PerMemory, ps.Score, err = memScores(vs, denied, vectors)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, ps)
	}
	return out, nil
}

// runScores 是 -method S：起引擎、预热库存句向量、逐样本算证据、追加写进 round<N>-S-<引擎>.jsonl（已有的行数必须等于 -start，同 A / B / C）
func runScores(o options, ds Dataset) error {
	path := scorePath(o)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s 已经有了：要重跑请换 -round 或自己删掉", path)
	}
	emb, stop, err := startEmbedding(o)
	if err != nil {
		return err
	}
	defer stop()
	ctx := context.Background()
	vectors, err := memoryVectors(ctx, o, ds, emb, false)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, s := range ds.Samples {
		parts, err := scoreSample(ctx, emb, ds, s, vectors)
		if err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		if err := json.NewEncoder(f).Encode(ScoreRow{ID: s.ID, Engine: o.engine, Model: guardModel, Parts: parts}); err != nil {
			return err
		}
	}
	fmt.Printf("engine=%s samples=%d -> %s\n", o.engine, len(ds.Samples), path)
	return f.Sync()
}

func readScoreRows(path string) ([]ScoreRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ScoreRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 16*1024*1024)
	for sc.Scan() {
		var r ScoreRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s：%w", path, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// decisionOf 是线上守卫在这个门槛上的第一道决定：字面先命中就拦；否则相似度够了才去问确认模型（"候选"）。
// 比较用 reaches，也就是线上的 memory.GuardReaches（float32 比）
func decisionOf(p PartScore, threshold float64) bool {
	return p.Literal || (p.Score >= 0 && reaches(p.Score, threshold))
}

// ScoreDiff 是两份结果的比较
type ScoreDiff struct {
	Samples, Parts, Compared int                // 样本数、段数、两边都算了相似度的段数
	MaxD                     float64            // 全部段最大相似度的最大差（float64）
	MaxDPerMemory            float64            // 每条条目分数的最大差
	Mismatch                 []string           // 结构对不上：样本、段数、字面命中、有没有相似度
	Flips                    []Flip             // 在 0.30～0.90 各门槛上两边决定不同的
	Near                     map[string]int     // 每个门槛 ±0.01 内的段数（以 A 为准）
	MinDist                  map[string]float64 // 每个门槛上 min |分数 − 门槛|
}

// Flip 是一个翻转：两边在这个门槛上的决定不同
type Flip struct {
	Sample    string
	Part      int
	Threshold float64
	A, B      float64
	Dist      float64 // 到门槛的距离（取两边中近的）
}

func thresholds() []float64 {
	var out []float64
	for step := 30; step <= 90; step += 5 {
		out = append(out, float64(step)/100)
	}
	return out
}

func thresholdKey(t float64) string { return fmt.Sprintf("%.2f", t) }

// diffScores 比两份结果：行按样本 id 和段下标一一对上才算数（对不上的写进 Mismatch，不悄悄跳过）
func diffScores(a, b []ScoreRow) ScoreDiff {
	d := ScoreDiff{Near: map[string]int{}, MinDist: map[string]float64{}}
	for _, t := range thresholds() {
		d.MinDist[thresholdKey(t)] = math.Inf(1)
	}
	if len(a) != len(b) {
		d.Mismatch = append(d.Mismatch, fmt.Sprintf("样本数不同：%d 对 %d", len(a), len(b)))
	}
	for i := 0; i < min(len(a), len(b)); i++ {
		ra, rb := a[i], b[i]
		if ra.ID != rb.ID || len(ra.Parts) != len(rb.Parts) {
			d.Mismatch = append(d.Mismatch, fmt.Sprintf("第 %d 行对不上：%s（%d 段）对 %s（%d 段）", i+1, ra.ID, len(ra.Parts), rb.ID, len(rb.Parts)))
			continue
		}
		d.Samples++
		for j := range ra.Parts {
			pa, pb := ra.Parts[j], rb.Parts[j]
			d.Parts++
			if pa.Literal != pb.Literal || (pa.Score < 0) != (pb.Score < 0) {
				d.Mismatch = append(d.Mismatch, fmt.Sprintf("%s 第 %d 段：字面或有无相似度不同（字面 %t/%t，分 %.6f/%.6f）", ra.ID, j+1, pa.Literal, pb.Literal, pa.Score, pb.Score))
				continue
			}
			if pa.Score >= 0 {
				d.Compared++
				d.MaxD = max(d.MaxD, math.Abs(pa.Score-pb.Score))
				for id, sa := range pa.PerMemory {
					if sb, ok := pb.PerMemory[id]; ok {
						d.MaxDPerMemory = max(d.MaxDPerMemory, math.Abs(sa-sb))
					} else {
						d.Mismatch = append(d.Mismatch, fmt.Sprintf("%s 第 %d 段：条目 %s 只有一边有分", ra.ID, j+1, id))
					}
				}
			}
			for _, t := range thresholds() {
				key := thresholdKey(t)
				if pa.Score >= 0 {
					dist := math.Min(math.Abs(pa.Score-t), math.Abs(pb.Score-t))
					d.MinDist[key] = math.Min(d.MinDist[key], dist)
					if math.Abs(pa.Score-t) <= nearBand {
						d.Near[key]++
					}
				}
				if decisionOf(pa, t) != decisionOf(pb, t) {
					d.Flips = append(d.Flips, Flip{Sample: ra.ID, Part: j + 1, Threshold: t, A: pa.Score, B: pb.Score, Dist: math.Min(math.Abs(pa.Score-t), math.Abs(pb.Score-t))})
				}
			}
		}
	}
	return d
}

// verdict 把 ScoreDiff 变成通过与否和文字：结构对不上、D 超过 scoreD、线上门槛 0.70 上有翻转、任何翻转到门槛的距离大于实际量到的 D（说明比较写错了），都不通过。
// 门槛附近不足 50 对不算不通过，但写明"这一边没量够"，不能当作验证过
func (d ScoreDiff) verdict() (ok bool, text string) {
	var b strings.Builder
	ok = true
	fail := func(f string, a ...any) { ok = false; fmt.Fprintf(&b, "不通过："+f+"\n", a...) }
	fmt.Fprintf(&b, "样本 %d，段 %d（两边都算了相似度的 %d 段）\n", d.Samples, d.Parts, d.Compared)
	for _, m := range d.Mismatch {
		fail("%s", m)
	}
	fmt.Fprintf(&b, "最大相似度差 D = %.3e（门槛 %.0e）；逐条目分数最大差 %.3e\n", d.MaxD, scoreD, d.MaxDPerMemory)
	if d.MaxD > scoreD {
		fail("D %.3e 超过 %.0e", d.MaxD, scoreD)
	}
	fmt.Fprintf(&b, "|门槛|±%.2f 内的段数|离门槛最近|翻转数|\n|---|---|---|---|\n", nearBand)
	perT := map[string]int{}
	for _, f := range d.Flips {
		perT[thresholdKey(f.Threshold)]++
		if f.Dist > d.MaxD {
			fail("%s 第 %d 段在门槛 %.2f 上翻转，但到门槛的距离 %.3e 大于 D %.3e：比较函数或数据对不上", f.Sample, f.Part, f.Threshold, f.Dist, d.MaxD)
		}
	}
	keys := make([]string, 0, len(d.MinDist))
	for k := range d.MinDist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		md := "—"
		if !math.IsInf(d.MinDist[k], 1) {
			md = fmt.Sprintf("%.3e", d.MinDist[k])
		}
		fmt.Fprintf(&b, "|%s|%d|%s|%d|\n", k, d.Near[k], md, perT[k])
	}
	prod := thresholdKey(productionThreshold)
	if n := perT[prod]; n > 0 {
		// 到门槛的距离都在 D 以内（上面不满足的已经算不通过）：是数值噪声让分数落在门槛两边，列出来，不算不通过（第五节"下游判断"②）
		fmt.Fprintf(&b, "线上门槛 %s 上有 %d 个样本段两边决定不同，到门槛的距离都不超过 D：\n", prod, n)
		for _, f := range d.Flips {
			if thresholdKey(f.Threshold) == prod {
				fmt.Fprintf(&b, "  %s 第 %d 段：A %.9f，B %.9f，到门槛 %.3e\n", f.Sample, f.Part, f.A, f.B, f.Dist)
			}
		}
	}
	if d.Near[prod] < nearMin {
		fmt.Fprintf(&b, "线上门槛 %s 附近（±%.2f）只有 %d 段，不足 %d：这一边没量够，不能当作验证过\n", prod, nearBand, d.Near[prod], nearMin)
	}
	if ok {
		b.WriteString("两份结果在每个样本每一段上拦 / 不拦决定相同，相似度差在 D 以内\n")
	}
	return ok, b.String()
}

// compareScoreFiles 读两份 -method S 的结果比一遍，返回要打印的文字；不通过时同时返回错误（命令退出码 1）
func compareScoreFiles(pathA, pathB string) (string, error) {
	a, err := readScoreRows(pathA)
	if err != nil {
		return "", err
	}
	b, err := readScoreRows(pathB)
	if err != nil {
		return "", err
	}
	if len(a) == 0 || len(b) == 0 {
		return "", errors.New("有一份结果是空的")
	}
	ok, text := diffScores(a, b).verdict()
	head := fmt.Sprintf("A = %s（%s）\nB = %s（%s）\n\n", filepath.Base(pathA), a[0].Engine, filepath.Base(pathB), b[0].Engine)
	if !ok {
		return head + text, errors.New("两份结果对不上")
	}
	return head + text, nil
}
