package tokenizer

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	sentencepiece "sumika-native/third_party/goSentencePiece"
)

// fork 不实现的 tokenizer.json 版式要在加载时报错，不能悄悄加载成功、给出不对的 id（PROVENANCE「删掉用不到的」）。
func TestForkRejectsUnsupportedLayouts(t *testing.T) {
	meta := func(scheme string, split bool) map[string]any {
		return map[string]any{"type": "Metaspace", "replacement": "▁", "prepend_scheme": scheme, "split": split}
	}
	cases := []struct {
		name   string
		mutate func(*hfTokenizer)
	}{
		{"model 不是 Unigram", func(tj *hfTokenizer) { tj.Model.Type = "BPE" }},
		{"字节回退", func(tj *hfTokenizer) { tj.Model.ByteFallback = true }},
		{"不是 special 的 added token", func(tj *hfTokenizer) { tj.AddedTokens[1].Special = false }},
		{"pre_tokenizer 没有", func(tj *hfTokenizer) { tj.PreTokenizer = nil }},
		{"Metaspace 不加前缀", func(tj *hfTokenizer) { tj.PreTokenizer = meta("never", true) }},
		{"Metaspace 不按下划线切", func(tj *hfTokenizer) { tj.PreTokenizer = meta("always", false) }},
		{"pre_tokenizer 是别的东西", func(tj *hfTokenizer) { tj.PreTokenizer = map[string]any{"type": "ByteLevel"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tj := miniTokenizer("bge")
			c.mutate(&tj)
			_, err := sentencepiece.NewTokenizerFromJSONReader(bytes.NewReader(marshalMini(tj)))
			if !errors.Is(err, sentencepiece.ErrUnsupportedModel) {
				t.Fatalf("该报 ErrUnsupportedModel，得到 %v", err)
			}
		})
	}
	// 对照：没改的迷你词表（两种 pre_tokenizer）能加载，不是 JSON 报 ErrInvalidModel
	for _, flavor := range []string{"gte", "bge"} {
		if _, err := sentencepiece.NewTokenizerFromJSONReader(bytes.NewReader(miniTokenizerJSON(flavor))); err != nil {
			t.Fatalf("没改的迷你词表（%s）该能加载：%v", flavor, err)
		}
	}
	if _, err := sentencepiece.NewTokenizerFromJSONReader(strings.NewReader("{")); !errors.Is(err, sentencepiece.ErrInvalidModel) {
		t.Fatalf("不是 JSON 该报 ErrInvalidModel，得到 %v", err)
	}
}

// Viterbi 的累加用 float64（和 Python 的 tokenizers 一致）：两种切法的总分只差 1e-9，float32 里它们相等、先找到的"ab"赢，
// float64 里分开切的总分更高。词条分数和总分都选得 float32 能精确表示其中一个，另一个差在舍入误差之内。
func TestViterbiFloat64Ties(t *testing.T) {
	tj := miniTokenizer("bge")
	tj.Model.Vocab = [][]any{
		{"<s>", 0.0}, {"<pad>", 0.0}, {"</s>", 0.0}, {"<unk>", 0.0},
		{"▁", -2.0}, {"a", -3.5}, {"b", -4.25}, {"ab", -7.750000001},
	}
	tj.AddedTokens = tj.AddedTokens[:4]
	sp, err := sentencepiece.NewTokenizerFromJSONReader(bytes.NewReader(marshalMini(tj)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sp.Encode("ab"), []int{4, 5, 6}; !slices.Equal(got, want) {
		t.Fatalf("分开切（总分 -9.75）该比 ab 一个词条（-9.750000001）高：得到 %v，该 %v", got, want)
	}
}
