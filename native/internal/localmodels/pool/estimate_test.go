package pool

import (
	"testing"

	"sumika-native/internal/localmodels/engine"
)

func TestEstimateBytes(t *testing.T) {
	gte := embedSpec("gte")
	gte.TokenizerResidentMiB, gte.ActivationMiB = 22, 700
	onnx := int64(340 * mib) // gte 量化图 324.6 MiB 的整数近似

	cases := []struct {
		name string
		row  int
		want uint64
	}{
		{"生产的 2048 token 一行", 2048, 442 + 22 + 700},
		{"更短的行不缩小余量（余量是按 2048 量的，宁可高估）", 512, 442 + 22 + 700},
		{"4096：激活按平方放大 4 倍（估 2800，实测 2151）", 4096, 442 + 22 + 700*4},
		{"8192：放大 16 倍（估 11200，实测 7376，高估）", 8192, 442 + 22 + 700*16},
	}
	for _, c := range cases {
		if got := EstimateBytes(gte, onnx, c.row) / mib; got != c.want {
			t.Errorf("%s：得 %d MiB，该是 %d MiB", c.name, got, c.want)
		}
	}

	t.Run("不只算 onnx", func(t *testing.T) {
		got := EstimateBytes(gte, onnx, 2048)
		onnxOnly := uint64(onnx) * 13 / 10
		if got < onnxOnly+700*mib {
			t.Fatalf("估计 %d MiB 比 onnx×1.3 加激活余量还低：只算 onnx 就是起草时那个形同虚设的预算", got/mib)
		}
	})
	t.Run("重排：行不会超过 max_tokens，余量不放大", func(t *testing.T) {
		bge := rerankSpec("bge")
		bge.TokenizerResidentMiB, bge.ActivationMiB = 22, 300
		if got := EstimateBytes(bge, 544*int64(mib), 1024) / mib; got != 544*13/10+22+300 {
			t.Fatalf("%d", got)
		}
	})
	t.Run("没量过的登记项只剩 onnx × 1.3", func(t *testing.T) {
		if got := EstimateBytes(embedSpec("x"), 100*int64(mib), 8192); got != 130*mib {
			t.Fatalf("%d", got/mib)
		}
	})
	t.Run("文件大小是负数当 0", func(t *testing.T) {
		if got := EstimateBytes(engine.Spec{}, -5, 100); got != 0 {
			t.Fatal(got)
		}
	})
}
