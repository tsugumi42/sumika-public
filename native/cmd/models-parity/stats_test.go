package main

import (
	"math"
	"testing"

	"sumika-native/internal/memory"
)

func TestVecDiff(t *testing.T) {
	d, cos, err := vecDiff([]float32{1, 0, 0}, []float32{1, 0, 0})
	if err != nil || d != 0 || cos != 1 {
		t.Fatalf("相同向量：%v %v %v", d, cos, err)
	}
	d, cos, err = vecDiff([]float32{1, 0}, []float32{0, 1})
	if err != nil || d != 1 || math.Abs(cos) > 1e-12 {
		t.Fatalf("正交向量：%v %v %v", d, cos, err)
	}
	d, _, _ = vecDiff([]float32{0.5, 0.5}, []float32{0.5, 0.5000005})
	if want := float64(float32(0.5000005)) - 0.5; d != want {
		t.Errorf("最大分量差该是 %v，得到 %v", want, d)
	}
	for name, c := range map[string][2][]float32{
		"长度不同": {{1, 2}, {1}},
		"空":    {{}, {}},
		"NaN":  {{float32(math.NaN()), 1}, {1, 1}},
		"Inf":  {{1, 1}, {float32(math.Inf(1)), 1}},
		"全零":   {{0, 0}, {1, 0}},
	} {
		if _, _, err := vecDiff(c[0], c[1]); err == nil {
			t.Errorf("%s 该报错", name)
		}
	}
}

func TestDeriveLogit(t *testing.T) {
	for _, x := range []float64{-8, -1.5, 0, 0.3, 4.2, 11.5} {
		s := 1 / (1 + math.Exp(-x))
		got, ok := deriveLogit(s)
		if !ok || math.Abs(got-x) > 1e-9 {
			t.Errorf("logit %v：反推得到 %v %v", x, got, ok)
		}
	}
	for _, s := range []float64{0, 1, -0.1, 1.5, math.NaN(), 1 - 1e-16, 1e-300} {
		if _, ok := deriveLogit(s); ok {
			t.Errorf("分数 %v 反推不可信，该返回 ok=false", s)
		}
	}
}

// 翻转数用生产的比较函数数（规格 GM6 验收 2 的 TestFlipUsesProductionComparisons）：
// 守卫那档，点积恰好等于 float32(0.70) 的一对必须判"命中"；把守卫的比较改成 float64(score) >= 0.70，这一条必须由绿变红
// （改坏检查：把 memory.GuardReaches 改成 float64 比，这个测试红）。召回档（0.6）、近重复档（0.92）各造一对恰好等于常量的。
func TestFlipUsesProductionComparisons(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		exact     float32 // 恰好等于 float32(门槛) 的分数
		reaches   func(float32) bool
		effective float64
	}{
		{"守卫", memory.GuardThreshold, float32(memory.GuardThreshold), func(s float32) bool { return memory.GuardReaches(s, memory.GuardThreshold) }, float64(float32(memory.GuardThreshold))},
		{"召回", memory.SimilarityFloor, float32(memory.SimilarityFloor), memory.RecallReaches, memory.SimilarityFloor},
		{"近重复", memory.DupSimilarity, float32(memory.DupSimilarity), memory.NearDuplicateReaches, memory.DupSimilarity},
	}
	for _, c := range cases {
		below := math.Nextafter32(c.exact, 0)
		// 一列在门槛上、一列低一格：必须数成 1 个翻转（噪声内）：恰好等于门槛的那一列判命中，低一格的判没命中
		rep, err := AnalyzeFlips(FlipInput{Name: c.name, Threshold: c.threshold, EffectiveThreshold: c.effective, Reaches: c.reaches,
			A: []float32{c.exact}, B: []float32{below}})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Flips != 1 || rep.WrongFlip != 0 {
			t.Errorf("%s：恰好等于门槛的一对和低一格的一对应该是 1 个翻转（噪声内），得到 flips=%d wrong=%d", c.name, rep.Flips, rep.WrongFlip)
		}
		// 两列都恰好在门槛上：都判命中，0 翻转
		rep, _ = AnalyzeFlips(FlipInput{Name: c.name, Threshold: c.threshold, EffectiveThreshold: c.effective, Reaches: c.reaches,
			A: []float32{c.exact}, B: []float32{c.exact}})
		if rep.Flips != 0 {
			t.Errorf("%s：两列都恰好等于门槛，却数出 %d 个翻转", c.name, rep.Flips)
		}
		if !c.reaches(c.exact) || c.reaches(below) {
			t.Errorf("%s：生产的比较函数对恰好等于门槛该判命中、对低一格该判没命中", c.name)
		}
	}
	// 守卫那档的前提：float32(0.70) 比 0.70 小一点，所以有效门槛要用 float64(float32(0.70))，不是 0.70
	if !(float64(float32(memory.GuardThreshold)) < memory.GuardThreshold) {
		t.Fatal("前提变了：float32(0.70) 不再小于 0.70")
	}
}

// 翻转的"到门槛的距离"和最大差 D 的关系（规格 GM6 验收 3a）：
// 距离 ≤ D 的翻转是噪声内（两列差 D、跨在门槛两侧，距离当然 ≤ D）；距离 > D 说明比较函数和声明的门槛对不上，要报"写错了"。
// 门槛 ±0.01 内不足 50 对时 NotEnough 为真（输出"这一边没量够"，不能当通过）。
func TestFlipsReportMargins(t *testing.T) {
	const thr = 0.5
	ge := func(s float32) bool { return float64(s) >= thr }
	mk := func(a, b []float32, reaches func(float32) bool) FlipReport {
		t.Helper()
		r, err := AnalyzeFlips(FlipInput{Name: "x", Threshold: thr, EffectiveThreshold: thr, Reaches: reaches, A: a, B: b})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// B 在门槛上方 D/2，A 在下方 D/2，两列差 D：距离 D/2 ≤ D，噪声内
	D := float32(1e-4)
	r := mk([]float32{thr - D/2, thr + D/2}, []float32{thr + D/2, thr + D/2}, ge)
	if r.Flips != 1 || r.WrongFlip != 0 || math.Abs(r.FlipDist-float64(D)/2) > 1e-7 {
		t.Errorf("D/2 的翻转该判噪声内：%+v", r)
	}
	// 声明的门槛是 0.5，比较函数实际在比 0.5 + 2D（用错了门槛）；第二对把 D 撑到 2e-6，
	// 第一对只差 2e-6、却跨在函数的门槛两侧，到声明门槛的距离 ≈ 2D = 2e-4 > D：比较函数写错了
	wrong := func(s float32) bool { return float64(s) >= thr+float64(2*D) }
	r = mk([]float32{thr + 2*D - 1e-6, thr}, []float32{thr + 2*D + 1e-6, thr}, wrong)
	if r.Flips != 1 || r.WrongFlip != 1 || r.FlipDist < float64(2*D)-1e-6 {
		t.Errorf("2D 的翻转该判比较函数写错了：%+v", r)
	}
	// 门槛 ±0.01 内的对数：49 对不够，50 对够
	pairs := func(n int) ([]float32, []float32) {
		a, b := make([]float32, n+3), make([]float32, n+3)
		for i := 0; i < n; i++ {
			a[i], b[i] = thr+0.005, thr+0.005
		}
		for i := n; i < n+3; i++ { // 远离门槛的不算
			a[i], b[i] = 0.9, 0.9
		}
		return a, b
	}
	a, b := pairs(49)
	if r = mk(a, b, ge); r.Near != 49 || !r.NotEnough {
		t.Errorf("49 对该判没量够：%+v", r)
	}
	a, b = pairs(50)
	if r = mk(a, b, ge); r.Near != 50 || r.NotEnough {
		t.Errorf("50 对够了：%+v", r)
	}
	// 另报离门槛最近的距离
	r = mk([]float32{0.4, 0.5003}, []float32{0.4, 0.5004}, ge)
	if math.Abs(r.MinMargin-0.0003) > 1e-6 {
		t.Errorf("MinMargin 该是 0.0003：%v", r.MinMargin)
	}
	// 错误输入
	if _, err := AnalyzeFlips(FlipInput{Reaches: ge, A: []float32{1}, B: nil}); err == nil {
		t.Error("两列长度不同该报错")
	}
	if _, err := AnalyzeFlips(FlipInput{A: []float32{1}, B: []float32{1}}); err == nil {
		t.Error("没有比较函数该报错")
	}
	if r = mk(nil, nil, ge); r.Pairs != 0 || !r.NotEnough || r.MinMargin != 0 {
		t.Errorf("空输入：%+v", r)
	}
}

func TestDescribe(t *testing.T) {
	d := describe([]float64{3, 1, 2, 5, 4})
	if d.N != 5 || d.Max != 5 || d.Median != 3 || d.P99 != 5 {
		t.Errorf("%+v", d)
	}
	if d := describe(nil); d.N != 0 || d.Max != 0 {
		t.Errorf("空：%+v", d)
	}
}

// 门槛不许松过天花板（规格第五节：向量不松过 1e-5、logit 不松过 1e-4）。两个"天花板"常量原来只写进 JSON 报告，
// 没有任何代码断言门槛不超过它们，有人把 vecMaxDiff 改成 1e-3 也没有东西拦。门槛是量完才许收紧、不许为了变绿放宽的。
// 改坏检查：把任何一个门槛改到天花板之上，这条红
func TestGatesStayBelowCeilings(t *testing.T) {
	for _, c := range []struct {
		name          string
		gate, ceiling float64
	}{
		{"嵌入向量最大分量差", vecMaxDiff, vecDiffCeiling},
		{"重排 logit 差", logitMaxDiff, logitCeiling},
		{"相似度最大差 D", simD, simDCeiling},
	} {
		if !(c.gate > 0 && c.gate <= c.ceiling) {
			t.Errorf("%s 的门槛 %g 超过了天花板 %g（或不是正数）：放宽门槛说明代码有问题，不是门槛的问题", c.name, c.gate, c.ceiling)
		}
	}
	if scoreMaxDiff > logitMaxDiff {
		t.Errorf("分数门槛 %g 不该比 logit 门槛 %g 松", scoreMaxDiff, logitMaxDiff)
	}
}
