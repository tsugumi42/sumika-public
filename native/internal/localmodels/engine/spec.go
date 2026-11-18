package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// 数字都照 tools/local-models/server.py（规格-Go本地模型 2.5 的表、GM3"具体参数"）。
const (
	MaxBatchSize   = 64    // 一次请求最多多少条
	MaxBatchTokens = 32768 // 一批最多多少 token（全命中缓存也照查）
	CacheSize      = 256   // 每个模型的缓存条数
	CharsPerToken  = 4     // 单条字符数上限 = max_tokens × 这个倍数，在分词之前拦

	// DefaultMinDimensions 是 matryoshka 截断的缺省下限，和 server.py 的 MIN_DIMENSIONS 一样。
	DefaultMinDimensions = 32
	MinMaxTokens         = 8 // server.py：max_tokens 至少 8；也是 --max-line-tokens 的下限（sumika-models 启动时先查，免得到第一次加载才报错）
)

// Kind 是模型的用途。
type Kind string

const (
	KindEmbed  Kind = "embed"
	KindRerank Kind = "rerank"
)

// 池化和截断的取值。server.py 还认 last / mean / model，这一版只实现 cls（嵌入）和 logit（重排）：
// 登记表里写了别的就拒绝加载，不按模型名 if，也不悄悄当 cls 用（规格 2.5"池化"一行）。
const (
	PoolingCLS   = "cls"
	PoolingLogit = "logit"

	TruncationLongestFirst = "longest_first" // 嵌入：单条，先把内容留到 max−2 再补 <s> 和 </s>
	TruncationOnlySecond   = "only_second"   // 重排：一对，只截文档，查询不截
)

// 这一版没实现、但 server.py 认识的池化：给个说得清的错误，不当成"不认识"。
var unimplementedPoolings = map[string]bool{"last": true, "mean": true, "model": true}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Spec 是登记表里的一个模型（字段名和 server.py 的 ModelSpec、models.toml 一一对应）。
type Spec struct {
	ID                string
	Kind              Kind
	Dir               string // 模型目录，相对模型根目录
	Onnx              string // 相对 Dir 的 ONNX 文件
	Dimension         int    // 向量维度；重排写 0
	Pooling           string
	Normalize         bool
	MaxTokens         int
	Matryoshka        bool
	MinDimensions     int
	AllowedDimensions []int
	Truncation        string
	QueryPrefix       string
	DocumentPrefix    string
	QueryTask         string

	// Go 版比 Python 多的两项：文件的 SHA-256（小写十六进制），加载时必须核对（规格 2.3"校验的时机"）。
	TokenizerSHA256 string
	OnnxSHA256      string

	// DownloadURL 是这个模型官方发布页（缺模型时设置页“打开下载页”用它）；空 = 没登记地址。
	// 不在 models.toml 里，Validate 不校验，和 Python 的对拍也不比这一项。
	DownloadURL string

	// 模型池估计占多少内存用的两项（规格 2.5 池一行；单位 MiB，量出来的数向上取整）。估计 = onnx 文件字节 × 1.3
	// + 分词器常驻 + 激活余量，只算 onnx 的话 gte 差好几倍（上游分词器单独就 1.8 GB）。0 表示没量过，Validate 不拒绝
	// （手写的测试登记不用填），但内置的两条必须有值（TestBuiltinHasMemoryProfile）。
	//
	// TokenizerResidentMiB：分词器加载完常驻的工作集增量（GM1 的 TestTokenizerMemory）。
	// ActivationMiB：一行前向把进程工作集顶高多少，量在这个模型自己最长的一行上——嵌入是 2048 token
	// （生产传 --max-line-tokens 2048；更长的行池按平方放大，见 pool.EstimateBytes），重排是 max_tokens 那么长的一对。
	TokenizerResidentMiB int
	ActivationMiB        int
}

// Validate 把 server.py load_specs 的校验搬过来，再加 Go 版自己的几条（只实现 cls 和 logit、截断方式要和用途配对、哈希格式）。
// 一次列出所有问题，不在第一个就停。
func (s Spec) Validate() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if s.ID == "" {
		add("没有 id")
	}
	switch s.Kind {
	case KindEmbed, KindRerank:
	default:
		add("kind 只能是 embed 或 rerank，现在是 %q", s.Kind)
	}
	switch {
	case unimplementedPoolings[s.Pooling]:
		add("pooling %q 这一版没实现（只有 %s 和 %s）", s.Pooling, PoolingCLS, PoolingLogit)
	case s.Pooling != PoolingCLS && s.Pooling != PoolingLogit:
		add("不认识的 pooling %q（这一版只有 %s 和 %s）", s.Pooling, PoolingCLS, PoolingLogit)
	case s.Kind == KindEmbed && s.Pooling != PoolingCLS:
		add("嵌入模型的 pooling 必须是 %s（%q 是重排用的）", PoolingCLS, s.Pooling)
	case s.Kind == KindRerank && s.Pooling != PoolingLogit:
		add("重排模型的 pooling 必须是 %s", PoolingLogit)
	}
	switch s.Truncation {
	case TruncationLongestFirst:
		if s.Kind == KindRerank {
			add("重排模型的 truncation 必须是 %s", TruncationOnlySecond)
		}
	case TruncationOnlySecond:
		if s.Kind == KindEmbed {
			add("嵌入模型的 truncation 必须是 %s", TruncationLongestFirst)
		}
	default:
		add("truncation 只能是 %s 或 %s，现在是 %q", TruncationLongestFirst, TruncationOnlySecond, s.Truncation)
	}
	for name, v := range map[string]string{"dir": s.Dir, "onnx": s.Onnx} {
		if !safeRelPath(v) {
			add("%s %q 必须是模型根目录下的相对路径（非空、不是绝对路径、不含 ..）", name, v)
		}
	}
	if s.Kind == KindEmbed && s.Dimension <= 0 {
		add("嵌入维度必须大于 0")
	}
	if s.MaxTokens < MinMaxTokens {
		add("max_tokens 至少 %d", MinMaxTokens)
	}
	if s.Matryoshka {
		if s.MinDimensions < 1 || s.MinDimensions > s.Dimension {
			add("min_dimensions 要在 [1, %d]", s.Dimension)
		}
		for _, v := range s.AllowedDimensions {
			if v < 1 || v > s.Dimension {
				add("allowed_dimensions 里只能有 [1, %d] 的值", s.Dimension)
				break
			}
		}
	} else if len(s.AllowedDimensions) > 0 {
		add("写了 allowed_dimensions 就要写 matryoshka = true")
	}
	if s.TokenizerResidentMiB < 0 || s.ActivationMiB < 0 {
		add("内存估计的两项（分词器常驻、激活余量）不能是负数：%d、%d", s.TokenizerResidentMiB, s.ActivationMiB)
	}
	if !hex64.MatchString(s.TokenizerSHA256) {
		add("tokenizer_sha256 要是 64 位小写十六进制")
	}
	if !hex64.MatchString(s.OnnxSHA256) {
		add("onnx_sha256 要是 64 位小写十六进制")
	}
	if len(p) == 0 {
		return nil
	}
	id := s.ID
	if id == "" {
		id = "（没有 id）"
	}
	return fmt.Errorf("登记表里 %s 有问题：%s", id, strings.Join(p, "；"))
}

func safeRelPath(v string) bool {
	if v == "" || filepath.IsAbs(v) || strings.HasPrefix(v, "/") || strings.HasPrefix(v, `\`) || filepath.VolumeName(v) != "" {
		return false
	}
	for _, seg := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

// CharLimit 是单条字符串允许的字符数（按 rune 数，分词之前拦）。
func (s Spec) CharLimit() int { return s.MaxTokens * CharsPerToken }

// Prefix 是加在文本前面的前缀：查询侧和库存侧各一个模板，{task} 换成请求里的任务句，没有就用 QueryTask。
// 模板为空就不加前缀（gte 和 bge 都是空）。
func (s Spec) Prefix(it InputType, task string) string {
	tmpl := s.DocumentPrefix
	if it == InputQuery {
		tmpl = s.QueryPrefix
	}
	if tmpl == "" {
		return ""
	}
	if task == "" {
		task = s.QueryTask
	}
	return strings.ReplaceAll(tmpl, "{task}", task)
}

// DimensionsError 在请求的 dimensions 不合模型卡时返回一句原因，合法返回空串。
func (s Spec) DimensionsError(d int) string {
	if !s.Matryoshka {
		return fmt.Sprintf("Model '%s' does not support dimensions truncation", s.ID)
	}
	if len(s.AllowedDimensions) > 0 {
		for _, v := range s.AllowedDimensions {
			if v == d {
				return ""
			}
		}
		parts := make([]string, len(s.AllowedDimensions))
		for i, v := range s.AllowedDimensions {
			parts[i] = fmt.Sprint(v)
		}
		return fmt.Sprintf("dimensions must be one of [%s] for '%s'", strings.Join(parts, ", "), s.ID)
	}
	if d < s.MinDimensions || d > s.Dimension {
		return fmt.Sprintf("dimensions must be within [%d, %d] for '%s'", s.MinDimensions, s.Dimension, s.ID)
	}
	return ""
}

// InputType 是嵌入的输入是查询还是库存里的文本，决定用哪个前缀。零值就是 document（和 server.py 的缺省一样）。
type InputType string

const (
	InputDocument InputType = "document"
	InputQuery    InputType = "query"
)
