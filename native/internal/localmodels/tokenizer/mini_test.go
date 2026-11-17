package tokenizer

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
)

// 迷你词表：编的 Unigram tokenizer.json（几百个词条），不要真模型文件就能在 go test 里跑。
// 版式照真的 gte / bge 的 tokenizer.json（五个 added_tokens、Metaspace、TemplateProcessing），
// 但没有 Precompiled 的字符映射表，分数取 0.25 的整数倍让"同分"的情形常出现。
// 生成方式固定（不用随机数），testdata/mini-*.json 是它的输出，go test -update 重写。

type hfAdded struct {
	ID         int    `json:"id"`
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	Lstrip     bool   `json:"lstrip"`
	Rstrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
	Special    bool   `json:"special"`
}

type hfModel struct {
	Type         string  `json:"type"`
	UnkID        int     `json:"unk_id"`
	Vocab        [][]any `json:"vocab"`
	ByteFallback bool    `json:"byte_fallback"`
}

type hfTokenizer struct {
	Version       string    `json:"version"`
	Truncation    any       `json:"truncation"`
	Padding       any       `json:"padding"`
	AddedTokens   []hfAdded `json:"added_tokens"`
	Normalizer    any       `json:"normalizer"`
	PreTokenizer  any       `json:"pre_tokenizer"`
	PostProcessor any       `json:"post_processor"`
	Decoder       any       `json:"decoder"`
	Model         hfModel   `json:"model"`
}

func miniPieces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(ps ...string) {
		for _, p := range ps {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add("▁")
	for c := byte(' ' + 1); c < 127; c++ {
		add(string(rune(c)))
	}
	for c := byte('a'); c <= 'z'; c++ {
		add("▁" + string(rune(c)))
	}
	add("the", "ing", "tion", "er", "ed", "hel", "lo", "wor", "ld", "bug", "onnx", "runtime", "model", "quantized", "hello", "world",
		"▁the", "▁hello", "▁world", "▁model", "▁in", "▁of", "▁and", "▁is", "▁to", "▁a",
		"▁bug", "▁onnx", "▁runtime", "CGO", "ENABLED", "▁CGO", "_ENABLED", "▁▁", "▁▁▁")
	han := []rune("的一是不了人我在有他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看文无开手十用主行方又如前所本见经头面公同三已老从动两长知民样现分将外但身些与高意进把法此实回二理美点月明其种声全工话儿者向情部正名定女问力机给等几很业最间新什写相关晚上吃火锅推荐店吗昨改角色卡感觉话式然世界再见")
	words := []string{"今天", "晚上", "火锅", "推荐", "角色", "什么", "自然", "方式", "你好", "世界", "再见", "感觉", "昨天", "一下", "那个", "可以", "没有", "已经", "我们", "他们",
		"今天晚上", "你好世界", "角色卡", "说话", "为什么", "怎么样", "好开心", "学习", "时间", "问题", "功能", "实现", "文档", "示例", "代码", "配置", "错误", "是不是"}
	for _, r := range han {
		add(string(r))
	}
	for _, r := range []rune("今天晚上想吃火锅你有什么推荐的店吗我昨天把那个角色卡改了一下感觉说话方式自然多了") {
		add(string(r))
	}
	add(words...)
	for _, w := range []string{"今天", "你好", "我", "你", "的", "是", "这个", "那个"} {
		add("▁" + w)
	}
	add("，", "。", "？", "！", "：", "；", "（", "）", "“", "”", "～", "…", "……", "\u3000")
	for _, r := range []rune("こんにちはせかいありがとうカタナー") {
		add(string(r))
	}
	for _, r := range []rune("안녕하세요세계오늘날씨가좋네요") {
		add(string(r))
	}
	add("\U0001F600", "\U0001F602", "\U0001F44D", "❤", "\ufe0f", "\u200d", "\u0301", "é", "ä", "ñ")
	return out
}

// miniScore 取 0.25 的整数倍：由词条本身决定，同一个词条分数永远一样。故意让不同切法的总分常常相等，测同分的取舍
func miniScore(p string) float64 {
	h := fnv.New32a()
	h.Write([]byte(p))
	n := len([]rune(strings.TrimPrefix(p, "▁")))
	base := -4.0 * float64(max(n, 1))       // 每个字 -4：长词条和拆开的短词条的和很接近，常常同分
	return base + 0.25*float64(h.Sum32()%3) // 只有三档（0、0.25、0.5），同分很常见
}

func miniTokenizerJSON(flavor string) []byte {
	return marshalMini(miniTokenizer(flavor))
}

func marshalMini(tj hfTokenizer) []byte {
	raw, err := json.MarshalIndent(tj, "", " ")
	if err != nil {
		panic(fmt.Sprintf("生成迷你词表：%v", err))
	}
	return append(raw, '\n')
}

func miniTokenizer(flavor string) hfTokenizer {
	special := func(id int, content string) hfAdded {
		return hfAdded{ID: id, Content: content, Special: true}
	}
	pieces := miniPieces()
	vocab := [][]any{{"<s>", 0.0}, {"<pad>", 0.0}, {"</s>", 0.0}, {"<unk>", 0.0}}
	for _, p := range pieces {
		vocab = append(vocab, []any{p, miniScore(p)})
	}
	maskID := len(vocab)
	vocab = append(vocab, []any{"<mask>", 0.0})
	added := []hfAdded{special(0, "<s>"), special(1, "<pad>"), special(2, "</s>"), special(3, "<unk>"), special(maskID, "<mask>")}
	added[4].Lstrip = true

	meta := map[string]any{"type": "Metaspace", "replacement": "▁", "prepend_scheme": "always", "split": true}
	var pre any = meta
	if flavor == "gte" {
		pre = map[string]any{"type": "Sequence", "pretokenizers": []any{map[string]any{"type": "WhitespaceSplit"}, meta}}
	}
	seqA := map[string]any{"Sequence": map[string]any{"id": "A", "type_id": 0}}
	seqB := map[string]any{"Sequence": map[string]any{"id": "B", "type_id": 0}}
	sp := func(id string) map[string]any {
		return map[string]any{"SpecialToken": map[string]any{"id": id, "type_id": 0}}
	}
	post := map[string]any{
		"type":   "TemplateProcessing",
		"single": []any{sp("<s>"), seqA, sp("</s>")},
		"pair":   []any{sp("<s>"), seqA, sp("</s>"), sp("</s>"), seqB, sp("</s>")},
		"special_tokens": map[string]any{
			"</s>": map[string]any{"id": "</s>", "ids": []int{2}, "tokens": []string{"</s>"}},
			"<s>":  map[string]any{"id": "<s>", "ids": []int{0}, "tokens": []string{"<s>"}},
		},
	}
	return hfTokenizer{
		Version:       "1.0",
		AddedTokens:   added,
		PreTokenizer:  pre,
		PostProcessor: post,
		Decoder:       meta,
		Model:         hfModel{Type: "Unigram", UnkID: 3, Vocab: vocab},
	}
}
