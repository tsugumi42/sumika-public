package engine

import (
	"fmt"
	"sort"
)

// Registry 是登记表：这一版 sumika-models 认识的模型。只放对拍过的两个（gte 嵌入、bge 重排），别的 id 回 404（规格 1、2.5）。
type Registry struct {
	specs map[string]Spec
	ids   []string // 登记的顺序
}

// NewRegistry 逐条校验再收进来；重复的 id 是错误。
func NewRegistry(specs ...Spec) (*Registry, error) {
	r := &Registry{specs: make(map[string]Spec, len(specs))}
	for _, s := range specs {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if _, dup := r.specs[s.ID]; dup {
			return nil, fmt.Errorf("登记表里 %s 登记了两次", s.ID)
		}
		r.specs[s.ID] = s
		r.ids = append(r.ids, s.ID)
	}
	if len(r.ids) == 0 {
		return nil, fmt.Errorf("登记表是空的")
	}
	return r, nil
}

// Get 按 id 取一条，返回的是拷贝。
func (r *Registry) Get(id string) (Spec, bool) {
	s, ok := r.specs[id]
	if ok {
		s.AllowedDimensions = append([]int(nil), s.AllowedDimensions...)
	}
	return s, ok
}

// IDs 是登记的全部 id（登记的顺序）。
func (r *Registry) IDs() []string { return append([]string(nil), r.ids...) }

// SortedIDs 是按字母排好的全部 id，给"可用 id 列表"这类要稳定输出的地方用。
func (r *Registry) SortedIDs() []string {
	out := r.IDs()
	sort.Strings(out)
	return out
}

// 登记表里的哈希（规格 2.3，2026-11-01 在本机算的）。
const (
	gteOnnxSHA256      = "ab2bd164ebd8ca9003dc49a981b611e849b5d326f504c8873ba76e07fa6c0082"
	gteTokenizerSHA256 = "3a56def25aa40facc030ea8b0b87f3688e4b3c39eb8b45d5702b3a1300fe2a20"
	bgeOnnxSHA256      = "912fc1215c2dbff6499700534bd8d31253af01573861abbfc43afd1fab6cce5d"
	bgeTokenizerSHA256 = "8bf8afbfd11306bd872018c53bfdf2e160a56f8edbcf49933324404791c148d3"
)

// BuiltinSpecs 是 gte 和 bge 两条登记，字段值和 tools/local-models/models.toml 里同名的两条一致
// （TestRegistryMatchesPython 对着 Python 导出的参照逐字段比，models.toml 改了会红）。
//
// 临时：规格 GM3 写的是读 cmd/sumika-models/models.toml（go:embed，依赖 github.com/BurntSushi/toml），
// 但那个库不在本机模块缓存里，夜里不联网，先把这两条写成 Go 值。库进了缓存以后：加 models.toml、
// 用它解析出同样的两条，删掉这个函数；TestRegistryMatchesPython 不用改。已在 docs/待办.md 挂条。
func BuiltinSpecs() []Spec {
	return []Spec{
		{
			ID:              "gte-multilingual-base",
			Kind:            KindEmbed,
			Dir:             "_待分类/gte-multilingual-base",
			Onnx:            "model_quantized.onnx",
			DownloadURL:     "https://huggingface.co/Alibaba-NLP/gte-multilingual-base",
			Dimension:       768,
			Pooling:         PoolingCLS,
			Normalize:       true,
			MaxTokens:       8192,
			Matryoshka:      true,
			MinDimensions:   128,
			Truncation:      TruncationLongestFirst,
			TokenizerSHA256: gteTokenizerSHA256,
			OnnxSHA256:      gteOnnxSHA256,
			// 内存估计（规格 2.5 池一行）：分词器常驻 +21.8 MB（GM1 TestTokenizerMemory）；一行 2048 token 把工作集顶高
			// 690 MiB（GM2 验收 9：372 → 1062）、池里的真模型测试复量 692 MiB（data/dev/test-runs/GM4/measure-gte.txt），取 700。
			TokenizerResidentMiB: 22,
			ActivationMiB:        700,
		},
		{
			ID:              "bge-reranker-v2-m3",
			Kind:            KindRerank,
			Dir:             "_待分类/bge-reranker-v2-m3",
			Onnx:            "model_quantized.onnx",
			Dimension:       0,
			Pooling:         PoolingLogit,
			Normalize:       false,
			MaxTokens:       1024,
			MinDimensions:   DefaultMinDimensions, // 不是 matryoshka，用不到；写成 Python 解出来的缺省值，方便逐字段比
			Truncation:      TruncationOnlySecond,
			TokenizerSHA256: bgeTokenizerSHA256,
			OnnxSHA256:      bgeOnnxSHA256,
			// 分词器常驻 +21.7 MB（GM1）；一对 1024 token（bge 最长的一行）把工作集顶高 218 MiB（池里的真模型测试，
			// 前向期间每 2 毫秒采样取最大，data/dev/test-runs/GM4/measure-bge.txt；第一次用进程峰值计数器量到的上界是 240），取 250。
			TokenizerResidentMiB: 22,
			ActivationMiB:        250,
		},
	}
}

// BuiltinRegistry 是 BuiltinSpecs 收成的登记表。
func BuiltinRegistry() (*Registry, error) { return NewRegistry(BuiltinSpecs()...) }
