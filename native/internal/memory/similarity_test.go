package memory

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"strings"
	"testing"
)

// 三处比较的边界（规格-Go本地模型 第五节"翻转"一行；models-parity 里原来的翻转测试随命令删除，ADR-389），
// 这里钉住被调用的函数自己）：点积恰好等于 float32(常量) 的得分，三处都要判"够"。
func TestComparisonsAtTheConstants(t *testing.T) {
	t.Parallel()
	// 前提：float32(0.70) 比 0.70 小一点。守卫要是按 float64 比，恰好等于 float32(0.70) 的得分会被放过——LESSONS 里出过一次。
	if !(float64(float32(GuardThreshold)) < GuardThreshold) {
		t.Fatal("前提变了：float32(0.70) 不再小于 0.70，这条测试要重新设计")
	}
	atGuard := float32(GuardThreshold)
	if !GuardReaches(atGuard, GuardThreshold) {
		t.Error("守卫：得分恰好等于 float32(0.70) 却判不够（按 float64 比了？）")
	}
	if GuardReaches(math.Nextafter32(atGuard, 0), GuardThreshold) {
		t.Error("守卫：比 float32(0.70) 小一格的得分判成了够")
	}
	if !GuardReaches(float32(0.5), 0.5) || GuardReaches(float32(0.49), 0.5) {
		t.Error("守卫：门槛是传进来的，不是写死 0.70")
	}
	// 召回和近重复的"等号"改成严格大于，这里分辨不出，也不需要：float32 里没有一个值恰好等于双精度的 0.6 和 0.92，两种写法对任何 float32 得分结果一样。
	// 能错的是用错常量、改成 float32 以外的类型，下面的两个方向（恰好等于常量的 float32、低一格的）管这个。
	atFloor := float32(SimilarityFloor)
	if !RecallReaches(atFloor) {
		t.Error("召回：得分恰好等于 float32(0.6) 却判不够")
	}
	if RecallReaches(math.Nextafter32(atFloor, 0)) {
		t.Error("召回：比 float32(0.6) 小一格的得分判成了够")
	}
	atDup := float32(DupSimilarity)
	if !NearDuplicateReaches(atDup) {
		t.Error("近重复：得分恰好等于 float32(0.92) 却判不够")
	}
	if NearDuplicateReaches(math.Nextafter32(atDup, 0)) {
		t.Error("近重复：比 float32(0.92) 小一格的得分判成了够")
	}
}

func TestGuardScore(t *testing.T) {
	t.Parallel()
	a, b, c := []float32{1, 0}, []float32{0, 1}, []float32{0.6, 0.8}
	cases := []struct {
		name    string
		qs, ds  [][]float32
		want    float32
		wantErr error
	}{
		{"没有可比的是 -1", [][]float32{a}, nil, -1, nil},
		{"没有查询也是 -1", nil, [][]float32{a}, -1, nil},
		{"取全部配对里最大的", [][]float32{a, b}, [][]float32{a, c}, 1, nil},
		{"全是负分就返回最大的那个负数，不是 -1", [][]float32{{-1, 0}}, [][]float32{{0.6, 0.8}, {0.8, -0.6}}, -0.6, nil},
		{"维度对不上", [][]float32{a}, [][]float32{{1, 0, 0}}, 0, ErrGuardDimension},
		{"后一对维度对不上也报", [][]float32{a}, [][]float32{a, {1}}, 0, ErrGuardDimension},
		{"NaN", [][]float32{{float32(math.NaN()), 0}}, [][]float32{a}, 0, ErrGuardInvalid},
		{"Inf", [][]float32{{float32(math.Inf(1)), 0}}, [][]float32{a}, 0, ErrGuardInvalid},
	}
	for _, c := range cases {
		got, err := GuardScore(c.qs, c.ds)
		if !errors.Is(err, c.wantErr) || (c.wantErr == nil && got != c.want) {
			t.Errorf("%s：得到 (%v, %v)，想要 (%v, %v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

// 生产里读这几个门槛的地方只许调 similarity.go 的函数，不许再自己写一次 `>=`：读源码，找拿门槛常量去比的二元表达式。
func TestProductionUsesSharedComparisons(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]bool{"SimilarityFloor": true, "DupSimilarity": true, "dupSimilarity": true, "GuardThreshold": true}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "similarity.go" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			switch be.Op {
			case token.GEQ, token.GTR, token.LSS, token.LEQ:
			default:
				return true
			}
			for _, side := range []ast.Expr{be.X, be.Y} {
				ast.Inspect(side, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && bad[id.Name] {
						t.Errorf("%s：自己拿 %s 做了比较，改调 similarity.go 的 GuardReaches / RecallReaches / NearDuplicateReaches", name, id.Name)
					}
					return true
				})
			}
			return true
		})
	}
}
