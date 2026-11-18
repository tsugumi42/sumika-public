package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/pyspace"
)

// smallSpec 是 gte 的缩小版：维度 8、截断下限 4，其余（max_tokens 8192、字符上限 32768）和 gte 一样，
// 让长输入的测试不用造几十 MB 的假张量。
func smallSpec() Spec {
	s := mustBuiltin(nil, "gte-multilingual-base")
	s.ID = "small-embed"
	s.Dimension = 8
	s.MinDimensions = 4
	return s
}

func mustBuiltin(t testing.TB, id string) Spec {
	if t != nil {
		t.Helper()
	}
	for _, s := range BuiltinSpecs() {
		if s.ID == id {
			return s
		}
	}
	panic(fmt.Sprintf("BuiltinSpecs 里没有 %s", id))
}

func ptr[T any](v T) *T { return &v }

func readTestdata(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s：%v", name, err)
	}
}

// ---- GM3-9：登记表 ----

type refSpec struct {
	Kind              string `json:"kind"`
	Dir               string `json:"dir"`
	Onnx              string `json:"onnx"`
	Dimension         int    `json:"dimension"`
	Pooling           string `json:"pooling"`
	Normalize         bool   `json:"normalize"`
	MaxTokens         int    `json:"max_tokens"`
	Matryoshka        bool   `json:"matryoshka"`
	MinDimensions     int    `json:"min_dimensions"`
	AllowedDimensions []int  `json:"allowed_dimensions"`
	Truncation        string `json:"truncation"`
	QueryPrefix       string `json:"query_prefix"`
	DocumentPrefix    string `json:"document_prefix"`
	QueryTask         string `json:"query_task"`
}

// 参照是 Python 版（tools/local-models，ADR-389 已删）用 parity_ref.py rules 从 Python 自己的 load_specs 导出的快照
// （缺省值已补上，见 testdata/README.md）。Python 的表不在了，这份参照钉住的就是当时的字段值：改 BuiltinSpecs 要对上它。
func TestRegistryMatchesPython(t *testing.T) {
	var ref struct {
		Models map[string]refSpec `json:"models"`
	}
	readTestdata(t, "ref-registry.json", &ref)

	reg, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.Models) != len(reg.IDs()) {
		t.Fatalf("参照里有 %d 条，Go 的登记表有 %d 条（%v）", len(ref.Models), len(reg.IDs()), reg.IDs())
	}
	for id, want := range ref.Models {
		got, ok := reg.Get(id)
		if !ok {
			t.Errorf("Go 的登记表里没有 %s", id)
			continue
		}
		if want.AllowedDimensions == nil {
			want.AllowedDimensions = []int{}
		}
		gotRef := refSpec{
			Kind: string(got.Kind), Dir: got.Dir, Onnx: got.Onnx, Dimension: got.Dimension, Pooling: got.Pooling,
			Normalize: got.Normalize, MaxTokens: got.MaxTokens, Matryoshka: got.Matryoshka, MinDimensions: got.MinDimensions,
			AllowedDimensions: append([]int{}, got.AllowedDimensions...), Truncation: got.Truncation,
			QueryPrefix: got.QueryPrefix, DocumentPrefix: got.DocumentPrefix, QueryTask: got.QueryTask,
		}
		if !reflect.DeepEqual(gotRef, want) {
			t.Errorf("%s 和 Python 的登记不一致：\n  Go     %+v\n  Python %+v", id, gotRef, want)
		}
	}
}

func TestRegistryRejects(t *testing.T) {
	good := func() Spec { return mustBuiltin(t, "gte-multilingual-base") }
	goodRerank := func() Spec { return mustBuiltin(t, "bge-reranker-v2-m3") }
	cases := []struct {
		name string
		spec func() Spec
		want string // 错误里要有的字
	}{
		{"没有 id", func() Spec { s := good(); s.ID = ""; return s }, "没有 id"},
		{"kind 不认识", func() Spec { s := good(); s.Kind = "chat"; return s }, "kind"},
		{"池化 last 没实现", func() Spec { s := good(); s.Pooling = "last"; return s }, "没实现"},
		{"池化 mean 没实现", func() Spec { s := good(); s.Pooling = "mean"; return s }, "没实现"},
		{"池化 model 没实现", func() Spec { s := good(); s.Pooling = "model"; return s }, "没实现"},
		{"池化不认识", func() Spec { s := good(); s.Pooling = "max"; return s }, "不认识的 pooling"},
		{"空池化不当成 cls", func() Spec { s := good(); s.Pooling = ""; return s }, "不认识的 pooling"},
		{"嵌入用 logit", func() Spec { s := good(); s.Pooling = PoolingLogit; return s }, "必须是 cls"},
		{"重排用 cls", func() Spec { s := goodRerank(); s.Pooling = PoolingCLS; return s }, "必须是 logit"},
		{"嵌入的截断是 only_second", func() Spec { s := good(); s.Truncation = TruncationOnlySecond; return s }, "truncation"},
		{"重排的截断是 longest_first", func() Spec { s := goodRerank(); s.Truncation = TruncationLongestFirst; return s }, "truncation"},
		{"截断不认识", func() Spec { s := good(); s.Truncation = ""; return s }, "truncation"},
		{"嵌入维度 0", func() Spec { s := good(); s.Dimension = 0; return s }, "嵌入维度必须大于 0"},
		{"嵌入维度负数", func() Spec { s := good(); s.Dimension = -1; return s }, "嵌入维度必须大于 0"},
		{"max_tokens 太小", func() Spec { s := good(); s.MaxTokens = 7; return s }, "max_tokens 至少 8"},
		{"min_dimensions 0", func() Spec { s := good(); s.MinDimensions = 0; return s }, "min_dimensions"},
		{"min_dimensions 超过维度", func() Spec { s := good(); s.MinDimensions = 769; return s }, "min_dimensions"},
		{"allowed 里有越界值", func() Spec { s := good(); s.AllowedDimensions = []int{128, 769}; return s }, "allowed_dimensions"},
		{"allowed 里有 0", func() Spec { s := good(); s.AllowedDimensions = []int{0}; return s }, "allowed_dimensions"},
		{"写了 allowed 没写 matryoshka", func() Spec {
			s := good()
			s.Matryoshka = false
			s.AllowedDimensions = []int{128}
			return s
		}, "matryoshka"},
		{"dir 是绝对路径", func() Spec { s := good(); s.Dir = "C:/models/x"; return s }, "相对路径"},
		{"dir 以斜杠开头", func() Spec { s := good(); s.Dir = "/models/x"; return s }, "相对路径"},
		{"dir 含 ..", func() Spec { s := good(); s.Dir = "a/../../b"; return s }, "相对路径"},
		{"dir 含反斜杠的 ..", func() Spec { s := good(); s.Dir = `a\..\b`; return s }, "相对路径"},
		{"onnx 为空", func() Spec { s := good(); s.Onnx = ""; return s }, "相对路径"},
		{"tokenizer 哈希为空", func() Spec { s := good(); s.TokenizerSHA256 = ""; return s }, "tokenizer_sha256"},
		{"tokenizer 哈希是大写", func() Spec { s := good(); s.TokenizerSHA256 = strings.ToUpper(s.TokenizerSHA256); return s }, "tokenizer_sha256"},
		{"onnx 哈希太短", func() Spec { s := good(); s.OnnxSHA256 = s.OnnxSHA256[:63]; return s }, "onnx_sha256"},
		{"分词器常驻是负数", func() Spec { s := good(); s.TokenizerResidentMiB = -1; return s }, "内存估计"},
		{"激活余量是负数", func() Spec { s := good(); s.ActivationMiB = -1; return s }, "内存估计"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.spec()
			err := s.Validate()
			if err == nil {
				t.Fatalf("应该被拒绝：%+v", s)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误里没有 %q：%v", c.want, err)
			}
			if _, err := NewRegistry(s); err == nil {
				t.Fatal("NewRegistry 也应该拒绝")
			}
		})
	}

	t.Run("一次列出所有问题", func(t *testing.T) {
		s := good()
		s.Pooling = "mean"
		s.Dimension = 0
		s.OnnxSHA256 = ""
		err := s.Validate()
		for _, want := range []string{"没实现", "嵌入维度", "onnx_sha256"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("错误里该同时有 %q：%v", want, err)
			}
		}
	})
	t.Run("重复的 id", func(t *testing.T) {
		if _, err := NewRegistry(good(), good()); err == nil || !strings.Contains(err.Error(), "两次") {
			t.Fatalf("重复登记该被拒绝：%v", err)
		}
	})
	t.Run("空表", func(t *testing.T) {
		if _, err := NewRegistry(); err == nil {
			t.Fatal("空登记表该被拒绝")
		}
	})
	t.Run("内置的两条是好的", func(t *testing.T) {
		reg, err := BuiltinRegistry()
		if err != nil {
			t.Fatal(err)
		}
		if got := reg.IDs(); !reflect.DeepEqual(got, []string{"gte-multilingual-base", "bge-reranker-v2-m3"}) {
			t.Fatalf("登记的 id：%v", got)
		}
		if _, ok := reg.Get("qwen3-embedding-0.6b"); ok {
			t.Fatal("Python 独有的模型不该在 Go 的登记表里（请求它回 404）")
		}
	})
	t.Run("内置的两条填了内存估计", func(t *testing.T) {
		// 池靠这两项估计占多少内存：只算 onnx 字节，gte 差好几倍（上游分词器单独就 1.8 GB），--max-bytes 形同虚设
		for _, s := range BuiltinSpecs() {
			if s.TokenizerResidentMiB <= 0 || s.ActivationMiB <= 0 {
				t.Errorf("%s 没填内存估计：分词器常驻 %d MiB，激活余量 %d MiB", s.ID, s.TokenizerResidentMiB, s.ActivationMiB)
			}
		}
	})
	t.Run("Get 返回拷贝", func(t *testing.T) {
		s := good()
		s.AllowedDimensions = []int{128, 256}
		reg, err := NewRegistry(s)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := reg.Get(s.ID)
		a.AllowedDimensions[0] = 999
		b, _ := reg.Get(s.ID)
		if b.AllowedDimensions[0] != 128 {
			t.Fatal("改返回值的切片不该动到登记表")
		}
	})
}

// ---- 前缀和 dimensions 的规则（server.py 的 prefix()、dimensions_error()）----

func TestSpecPrefix(t *testing.T) {
	s := smallSpec()
	s.QueryPrefix = "Instruct: {task}\nQuery: "
	s.DocumentPrefix = "Doc: "
	s.QueryTask = "找相关段落"
	for _, c := range []struct {
		it   InputType
		task string
		want string
	}{
		{InputQuery, "", "Instruct: 找相关段落\nQuery: "},
		{InputQuery, "换个任务", "Instruct: 换个任务\nQuery: "},
		{InputDocument, "", "Doc: "},
		{InputDocument, "任务句对库存侧没用", "Doc: "},
		{"", "", "Doc: "}, // 零值 = document
	} {
		if got := s.Prefix(c.it, c.task); got != c.want {
			t.Errorf("Prefix(%q, %q) = %q，想要 %q", c.it, c.task, got, c.want)
		}
	}
	s.QueryPrefix, s.DocumentPrefix = "", ""
	if s.Prefix(InputQuery, "x") != "" || s.Prefix(InputDocument, "x") != "" {
		t.Error("模板为空就不加前缀")
	}
}

func TestSpecDimensionsError(t *testing.T) {
	g := mustBuiltin(t, "gte-multilingual-base")
	for d, ok := range map[int]bool{-1: false, 0: false, 1: false, 64: false, 127: false, 128: true, 129: true, 767: true, 768: true, 769: false} {
		if got := g.DimensionsError(d) == ""; got != ok {
			t.Errorf("gte dimensions=%d：合法=%v，想要 %v", d, got, ok)
		}
	}
	b := mustBuiltin(t, "bge-reranker-v2-m3")
	if b.DimensionsError(128) == "" {
		t.Error("不支持 matryoshka 的模型不能截维度")
	}
	w := smallSpec()
	w.AllowedDimensions = []int{4, 8}
	for d, ok := range map[int]bool{4: true, 8: true, 5: false, 6: false} {
		if got := w.DimensionsError(d) == ""; got != ok {
			t.Errorf("白名单 dimensions=%d：合法=%v，想要 %v", d, got, ok)
		}
	}
}

// ---- 判空：Python 的 str.isspace() ----

// 参照是 Python 3.12（Unicode 15.0）对全部 0x110000 个码点跑 isspace() 的结果。
func TestPySpaceMatchesPython(t *testing.T) {
	var ref struct {
		IsSpace []int `json:"isspace"`
	}
	readTestdata(t, "ref-whitespace.json", &ref)
	want := map[rune]bool{}
	for _, cp := range ref.IsSpace {
		want[rune(cp)] = true
	}
	if len(want) != 29 {
		t.Fatalf("参照里有 %d 个空白码点，Python 3.12 该是 29 个：参照被改过？", len(want))
	}
	var wrong []string
	for r := rune(0); r <= 0x10FFFF; r++ {
		if pyspace.IsSpace(r) != want[r] {
			wrong = append(wrong, string(r))
			if len(wrong) == 10 {
				break
			}
		}
	}
	if len(wrong) > 0 {
		t.Fatalf("和 Python 的 isspace 不一致的码点（最多列 10 个）：%U", []rune(strings.Join(wrong, "")))
	}
	// 几个容易判错的：Go 的 unicode.IsSpace 没有 U+001C～U+001F；零宽字符、U+180E、U+FEFF 都不是空白
	for _, r := range []rune{0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x3000} {
		if !pyspace.IsSpace(r) {
			t.Errorf("%U 该是空白", r)
		}
	}
	for _, r := range []rune{0x200b, 0x200c, 0x200d, 0x2060, 0x180e, 0xfeff, 0x00, 0x7f, 0xfffd} {
		if pyspace.IsSpace(r) {
			t.Errorf("%U 不该是空白", r)
		}
	}
}

func TestIsBlank(t *testing.T) {
	for s, want := range map[string]bool{
		"":                   true,
		" ":                  true,
		"\t\r\n":             true,
		"\x1c\x1d\x1e\x1f":   true,
		"\u3000\u00a0\u0085": true,
		"\u200b":             false, // 只含零宽字符不是空：Python 照常前向
		"\u200b\u200b":       false,
		" \u200b ":           false,
		"a":                  false,
		" a ":                false,
		"\ufeff":             false,
		"\x00":               false,
		"\xff\xfe":           false, // 非法 UTF-8 按 U+FFFD 算，不是空白
	} {
		if got := pyspace.IsBlank(s); got != want {
			t.Errorf("pyspace.IsBlank(%q) = %v，想要 %v", s, got, want)
		}
	}
}

// 盘符判断要与本机系统无关：Linux 上 filepath.IsAbs / VolumeName 不认 C:/models/x，靠 hasDriveLetter 拦住
func TestHasDriveLetter(t *testing.T) {
	for v, want := range map[string]bool{
		"C:/models/x": true, `c:\models`: true, "Z:x": true, "d:": true,
		"models/x": false, "ab:c": false, "1:x": false, ":x": false, "a": false, "": false, "a/b:c": false,
	} {
		if got := hasDriveLetter(v); got != want {
			t.Errorf("hasDriveLetter(%q) = %v，要 %v", v, got, want)
		}
		if want && safeRelPath(v) {
			t.Errorf("safeRelPath(%q) 应当拒绝带盘符的路径", v)
		}
	}
}
