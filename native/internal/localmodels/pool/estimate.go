package pool

import "sumika-native/internal/localmodels/engine"

// 估计公式里的常数。
const (
	// onnxOverheadNum / onnxOverheadDen：onnx 文件字节 × 1.3（server.py 的 estimated_bytes）。
	onnxOverheadNum = 13
	onnxOverheadDen = 10

	// activationRefTokens：激活余量按这个长度的一行量的（GM2：gte 2048 token 一行把工作集从 372 顶到 1062 MiB）。
	// 一行更长时激活内存随长度的平方涨（实测 gte 比刚加载完多占的：2048 token 690 MiB、4096 token 2151 MiB、
	// 8192 token 7376 MiB），所以超过它按 (L/2048)² 放大：4096 估 2800、8192 估 11200，比实测高约 30% 和 52%，宁可高估。
	activationRefTokens = 2048
)

const mib = uint64(1) << 20

// EstimateBytes 是加载前估计一个模型要占多少字节：onnx 文件字节 × 1.3 + 分词器常驻 + 激活余量（规格 2.5 池一行）。
// rowTokens 是这个模型一行最多会前向多少个 token：嵌入 = min(登记表的 max_tokens, --max-line-tokens)，重排 = 登记表的 max_tokens。
// 起草时只算 onnx × 1.3，gte 是 422 MiB，同一个进程里分词器加会话实际有 2191 MiB（上游分词器的词表树就 1.8 GB），
// --max-bytes 4GiB 形同虚设；激活那一项随行长度的平方涨，不算进去就挡不住 --max-line-tokens 放太宽。
func EstimateBytes(spec engine.Spec, onnxBytes int64, rowTokens int) uint64 {
	if onnxBytes < 0 {
		onnxBytes = 0
	}
	act := uint64(spec.ActivationMiB) * mib
	if rowTokens > activationRefTokens {
		l := uint64(rowTokens)
		act = act * l * l / (activationRefTokens * activationRefTokens)
	}
	return uint64(onnxBytes)*onnxOverheadNum/onnxOverheadDen + uint64(spec.TokenizerResidentMiB)*mib + act
}
