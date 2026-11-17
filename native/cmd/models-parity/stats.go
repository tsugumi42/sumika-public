package main

import (
	"errors"
	"math"
	"sort"
)

// 门槛（规格-Go本地模型 第五节）。写成常量并随输出打印；改它们要改规格，不是为了让命令变绿。
const (
	vecMaxDiff      = 1e-6     // 嵌入向量最大分量差（id 相同，同一个 dll、同参数）
	vecDiffCeiling  = 1e-5     // 门槛不许松过这个（松过了是代码有问题）
	logitMaxDiff    = 1e-5     // 重排 logit 差（由分数反推，精度见 deriveLogit）
	logitCeiling    = 1e-4     // 同上，logit 的天花板
	scoreMaxDiff    = 1e-6     // 重排分数（sigmoid 之后）差
	registeredCos   = 0.95     // id 不同的登记差异：余弦下限
	simD            = 1e-5     // 相似度最大差 D 的起始值
	simDCeiling     = 3e-5     // 768 维每个分量差 1e-6 的最坏点积差，不许松过
	nearBand        = 0.01     // "门槛附近"的宽度
	nearMinPairs    = 50       // 每个门槛附近至少这么多对，不够写"这一边没量够"
	logitReliableAt = 12.0     // |logit| 超过它，sigmoid 在 float64 里饱和，反推的 logit 不可信
	vecCosGate      = 1 - 1e-6 // 余弦 ≥ 0.999999（写成 1 − 1e-6，免得字面值的浮点表示差一点点）
)

// vecDiff 是两个向量的最大分量差和余弦，全用 float64 算（输入是 float32）。长度不同、含 NaN / Inf、全零都是错误。
func vecDiff(a, b []float32) (maxDiff, cos float64, err error) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, 0, errors.New("向量长度对不上或为空")
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
			return 0, 0, errors.New("向量里有 NaN 或 Inf")
		}
		maxDiff = max(maxDiff, math.Abs(x-y))
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, 0, errors.New("全零向量")
	}
	return maxDiff, dot / math.Sqrt(na*nb), nil
}

// deriveLogit：HTTP 只回 sigmoid 之后的分数，logit 要反推：ln(s / (1−s))。
// |logit| 很大时 sigmoid 在 float64 里饱和（1−s 取整成 0 或 s 取整成 1），反推出来不可信，返回 ok=false，那一对不进 logit 这一行。
func deriveLogit(score float64) (logit float64, ok bool) {
	if !(score > 0 && score < 1) {
		return 0, false
	}
	l := math.Log(score / (1 - score))
	if math.IsInf(l, 0) || math.IsNaN(l) || math.Abs(l) > logitReliableAt {
		return 0, false
	}
	return l, true
}

// Dist 是一组非负数的描述：最大、中位、p99。
type Dist struct {
	N      int     `json:"n"`
	Max    float64 `json:"max"`
	Median float64 `json:"median"`
	P99    float64 `json:"p99"`
}

func describe(xs []float64) Dist {
	if len(xs) == 0 {
		return Dist{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return Dist{N: len(s), Max: s[len(s)-1], Median: at(0.5), P99: at(0.99)}
}

// FlipInput 是一个门槛上的一批对：A、B 是同一批对的两列分数（A = 被测的一列，B = 参照），Reaches 是生产里那一档的比较函数
// （memory.GuardReaches 等，命令自己不写比较）。EffectiveThreshold 是这个函数实际在比的值：守卫把 0.70 转成 float32 再比，
// 它比 0.70 小一点（float64(float32(0.70)) = 0.69999998…），算"到门槛的距离"要用它，不然恰好跨在 float32(0.70) 两侧的一对会被冤枉。
type FlipInput struct {
	Name               string
	Threshold          float64
	EffectiveThreshold float64
	Reaches            func(score float32) bool
	A, B               []float32
}

// FlipReport：规格第五节"下游判断"一行的四个数。
type FlipReport struct {
	Name      string  `json:"name"`
	Threshold float64 `json:"threshold"`
	Pairs     int     `json:"pairs"`
	Diff      Dist    `json:"diff"`      // |A−B|（float64）；Max 就是 D
	Flips     int     `json:"flips"`     // 两列过不过门槛的判断不同的对数
	FlipDist  float64 `json:"flipDist"`  // 所有翻转里"到门槛的距离"的最大值
	WrongFlip int     `json:"wrongFlip"` // 翻转的距离 > D 的个数：不该有，有就是比较函数写错了
	Near      int     `json:"near"`      // 参照列分数在门槛 ±0.01 内的对数
	MinMargin float64 `json:"minMargin"` // 两列里离门槛最近的 |分数 − 门槛|
	NotEnough bool    `json:"notEnough"` // Near < 50：这一边没量够，不能当通过
}

// AnalyzeFlips 算 FlipReport。A、B 长度不同返回错误。
// 翻转（Reaches 对两列的判断不同）的"到门槛的距离"取两列里离有效门槛更远的那个；A、B 差 ≤ D 的两个数跨在门槛两侧时，
// 两个距离都 ≤ D，所以距离 > D 说明比较函数和声明的门槛对不上（或不单调）。
func AnalyzeFlips(in FlipInput) (FlipReport, error) {
	if len(in.A) != len(in.B) {
		return FlipReport{}, errors.New("两列分数的长度不同")
	}
	if in.Reaches == nil {
		return FlipReport{}, errors.New("没有比较函数")
	}
	r := FlipReport{Name: in.Name, Threshold: in.Threshold, Pairs: len(in.A), MinMargin: math.Inf(1)}
	diffs := make([]float64, len(in.A))
	for i := range in.A {
		diffs[i] = math.Abs(float64(in.A[i]) - float64(in.B[i]))
	}
	r.Diff = describe(diffs)
	for i := range in.A {
		a, b := float64(in.A[i]), float64(in.B[i])
		da, db := math.Abs(a-in.EffectiveThreshold), math.Abs(b-in.EffectiveThreshold)
		r.MinMargin = min(r.MinMargin, da, db)
		if db <= nearBand {
			r.Near++
		}
		if in.Reaches(in.A[i]) == in.Reaches(in.B[i]) {
			continue
		}
		r.Flips++
		dist := max(da, db)
		r.FlipDist = max(r.FlipDist, dist)
		if dist > r.Diff.Max {
			r.WrongFlip++
		}
	}
	if len(in.A) == 0 {
		r.MinMargin = 0
	}
	r.NotEnough = r.Near < nearMinPairs
	return r, nil
}
