package tokenizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	sentencepiece "sumika-native/third_party/goSentencePiece"
)

// fork 的回归基线：样本（cases.json、probes.json）和一批固定种子的随机串，id 存在 testdata/golden-ids-*.json。
// 这批 golden 最初是在拷进来的上游原样代码上跑出来的（第 1 个提交），紧凑词表树（第 2 步）、删 BPE（第 3 步）、
// normalizer 重写（第 6 步）每一步都和它逐个相同，证明那几次改动没有改变分词结果；
// 第 7 步把 Viterbi 的累加从 float32 改成 float64（和 Python 的 tokenizers 一致），真的 gte 的随机串因此有几条变了，
// golden 在那个提交里重新生成，旧的在 git 历史里。之后改 fork 的任何代码，id 不许悄悄变。
// 三份输入：迷你词表（编的，不要真文件，gte / bge 两种 pre_tokenizer 各一份）和真的 gte 原文件（SUMIKA_TEST_MODELS_ROOT）。
// 真的 bge 不在其中：上游原样把它的 Sequence 形式的 normalizer 整个忽略了，fork 现在按文件执行（Strip 和 Replace），
// 结果有意不同；bge 的对照是和 Python 的逐 id 对拍（ref-ids-bge.json）。

const (
	modelsRootEnv = "SUMIKA_TEST_MODELS_ROOT"
	randomSeed    = 20261102
	randomCount   = 50000
)

// 登记表里的模型目录（规格 2.3）和 tokenizer.json 的哈希（2026-11-01 算的）
var realTokenizers = map[string]struct{ dir, sha256 string }{
	"gte": {"_待分类/gte-multilingual-base", "3a56def25aa40facc030ea8b0b87f3688e4b3c39eb8b45d5702b3a1300fe2a20"},
	"bge": {"_待分类/bge-reranker-v2-m3", "8bf8afbfd11306bd872018c53bfdf2e160a56f8edbcf49933324404791c148d3"},
}

func realTokenizerJSON(t testing.TB, name string) []byte {
	t.Helper()
	root := os.Getenv(modelsRootEnv)
	if root == "" {
		t.Skipf("没设 %s（模型根目录，如 <模型根>），跳过用真 tokenizer.json 的测试", modelsRootEnv)
	}
	info := realTokenizers[name]
	raw, err := os.ReadFile(filepath.Join(root, info.dir, "tokenizer.json"))
	if err != nil {
		t.Fatalf("读 %s 的 tokenizer.json：%v", name, err)
	}
	if got := sha256Hex(raw); got != info.sha256 {
		t.Fatalf("%s 的 tokenizer.json 哈希是 %s，登记的是 %s", name, got, info.sha256)
	}
	return raw
}

// encodeRaw 用 fork 的 Encode 取内容 id（不带特殊符号）；panic 记成 [-1]，免得一条坏输入拖垮整批
func encodeRaw(tk *sentencepiece.Tokenizer, text string) (ids []int) {
	defer func() {
		if r := recover(); r != nil {
			ids = []int{-1}
		}
	}()
	return tk.Encode(text)
}

type goldenFile struct {
	Source          string  `json:"source"`
	TokenizerSHA256 string  `json:"tokenizer_sha256"`
	Cases           [][]int `json:"cases"`
	Probes          [][]int `json:"probes"`
	Random          struct {
		Seed   uint64 `json:"seed"`
		N      int    `json:"n"`
		Tokens int    `json:"tokens"`
		SHA256 string `json:"sha256"`
	} `json:"random"`
}

func encodeAll(tk *sentencepiece.Tokenizer, texts []string) [][]int {
	rows := make([][]int, len(texts))
	for i, s := range texts {
		rows[i] = encodeRaw(tk, s)
	}
	return rows
}

func buildGolden(tk *sentencepiece.Tokenizer, raw []byte, source string, cases, probes []string) goldenFile {
	g := goldenFile{Source: source, TokenizerSHA256: sha256Hex(raw)}
	g.Cases = encodeAll(tk, cases)
	g.Probes = encodeAll(tk, probes)
	g.Random.Seed, g.Random.N = randomSeed, randomCount
	g.Random.SHA256, g.Random.Tokens = idsDigest(encodeAll(tk, randomStrings(randomSeed, randomCount)))
	return g
}

// writeGolden 每条样本的 id 占一行，改动时 diff 看得清
func writeGolden(path string, g goldenFile) error {
	var b bytes.Buffer
	rows := func(rs [][]int) string {
		var lines []string
		for _, r := range rs {
			if r == nil {
				r = []int{}
			}
			j, _ := json.Marshal(r)
			lines = append(lines, string(j))
		}
		return "[\n" + strings.Join(lines, ",\n") + "\n]"
	}
	head, _ := json.Marshal(map[string]string{"source": g.Source, "tokenizer_sha256": g.TokenizerSHA256})
	rnd, _ := json.Marshal(g.Random)
	fmt.Fprintf(&b, "{\n\"head\": %s,\n\"cases\": %s,\n\"probes\": %s,\n\"random\": %s\n}\n", head, rows(g.Cases), rows(g.Probes), rnd)
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// readGolden 读 writeGolden 写的文件
func readGolden(t testing.TB, path string) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 golden %s：%v（第一次要加 -update 生成）", path, err)
	}
	var f struct {
		Head   map[string]string `json:"head"`
		Cases  [][]int           `json:"cases"`
		Probes [][]int           `json:"probes"`
		Random json.RawMessage   `json:"random"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 golden %s：%v", path, err)
	}
	g := goldenFile{Source: f.Head["source"], TokenizerSHA256: f.Head["tokenizer_sha256"], Cases: f.Cases, Probes: f.Probes}
	if err := json.Unmarshal(f.Random, &g.Random); err != nil {
		t.Fatalf("解析 golden %s 的 random：%v", path, err)
	}
	return g
}

func diffRows(t *testing.T, what string, texts []string, got, want [][]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：样本 %d 条，golden %d 条（样本集合变了就要重新生成 golden）", what, len(got), len(want))
	}
	bad := 0
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			bad++
			if bad <= 5 {
				t.Errorf("%s 第 %d 条 %q：现在 %v，golden %v", what, i, texts[i], got[i], want[i])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%s：%d / %d 条和 golden 不同", what, bad, len(got))
	}
}

const goldenSource = "fork 的 Encode（不带特殊符号），Viterbi 用 float64；最初在 tggo/goSentencePiece v1.1.0 (d277d1da23cd) 原样上生成"

func TestEncodeGolden(t *testing.T) {
	sources := []struct {
		name string
		load func(testing.TB) []byte
	}{
		{"mini-gte", func(testing.TB) []byte { return miniTokenizerJSON("gte") }},
		{"mini-bge", func(testing.TB) []byte { return miniTokenizerJSON("bge") }},
		{"gte", func(tb testing.TB) []byte { return realTokenizerJSON(tb, "gte") }},
	}
	if *updateGolden {
		writeProbes(t)
	}
	cases, probes := loadCases(t), loadProbes(t)
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			raw := src.load(t)
			golden := filepath.Join("testdata", "golden-ids-"+src.name+".json")
			tk, err := sentencepiece.NewTokenizerFromJSONReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("加载：%v", err)
			}
			if *updateGolden {
				if err := writeGolden(golden, buildGolden(tk, raw, goldenSource, cases, probes)); err != nil {
					t.Fatal(err)
				}
				t.Logf("已重写 %s", golden)
				return
			}
			want := readGolden(t, golden)
			if want.TokenizerSHA256 != sha256Hex(raw) {
				t.Fatalf("golden 是在另一份 tokenizer.json（%s）上生成的，现在是 %s", want.TokenizerSHA256, sha256Hex(raw))
			}
			diffRows(t, "cases", cases, encodeAll(tk, cases), want.Cases)
			diffRows(t, "probes", probes, encodeAll(tk, probes), want.Probes)
			digest, tokens := idsDigest(encodeAll(tk, randomStrings(want.Random.Seed, want.Random.N)))
			if digest != want.Random.SHA256 || tokens != want.Random.Tokens {
				t.Errorf("%d 条随机串：摘要 %s（%d 个 id），golden %s（%d 个 id）", want.Random.N, digest, tokens, want.Random.SHA256, want.Random.Tokens)
			} else {
				t.Logf("%s：%d 条随机串 + %d 条样本 + %d 条探针，id 和 golden 逐个相同（共 %d 个随机串 id）", src.name, want.Random.N, len(cases), len(probes), tokens)
			}
		})
	}
}

// writeProbes 重写探针集（编的，生成方式固定）。不可见字符一律写成转义，diff 里看得见
func writeProbes(t *testing.T) {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(buildProbes()); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, r := range b.String() {
		if r > 0x7e && (unicode.IsSpace(r) || unicode.In(r, unicode.Cf, unicode.Mn, unicode.Zs) || r == 0x2581) {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	if err := os.WriteFile(filepath.Join("testdata", "probes.json"), []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
