package tokenizer

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 已知差异的类别（testdata/categories.json，规格-Go本地模型 2.4"已解释怎么判"）：对拍时一个不一致的样本，把它含有的类别
// 的字符全部去掉以后，Python 和 Go 对去掉之后的文本的 id 必须完全一致，才算"已解释"；归不到类别的不一致就是真的分词 bug。
// 单测（parity_test.go）和对拍命令（cmd/models-parity）用同一份判法，所以放在这里，不各写一份。

// Category 是一个类别：对哪些模型生效、为什么、"去掉哪些字符算去掉"。
type Category struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Why    string   `json:"why"`
	Remove struct {
		Ranges  [][2]rune `json:"ranges"`
		Strings []string  `json:"strings"`
		// NFKC：Strings 在 NFKC 之后的文本里找（normalizer 会把全角 ＜s＞ 折成 <s>）。找到了才算命中，
		// 去掉以后返回的是 NFKC 之后的文本；没找到原样返回，不因为做了 NFKC 就算改动
		NFKC bool `json:"nfkc"`
	} `json:"remove"`
}

// ParseCategories 解析 categories.json（类别名 → 类别）。
func ParseCategories(raw []byte) (map[string]Category, error) {
	var cats map[string]Category
	if err := json.Unmarshal(raw, &cats); err != nil {
		return nil, fmt.Errorf("解析类别表：%w", err)
	}
	return cats, nil
}

// Strip 把类别的字符从文本里去掉：区间里的码点，和列出的字符串（去到不再出现为止：去掉 <s> 可能让两边拼出新的 <s>，例如 <<s>s>；
// parity_ref.py 的 remove_category 同样的规则）。没有可去的就原样返回。
func (c Category) Strip(text string) string {
	if len(c.Remove.Ranges) > 0 {
		text = strings.Map(func(r rune) rune {
			for _, span := range c.Remove.Ranges {
				if r >= span[0] && r <= span[1] {
					return -1
				}
			}
			return r
		}, text)
	}
	if len(c.Remove.Strings) == 0 {
		return text
	}
	t := text
	if c.Remove.NFKC {
		t = norm.NFKC.String(text)
	}
	if !slices.ContainsFunc(c.Remove.Strings, func(s string) bool { return strings.Contains(t, s) }) {
		return text
	}
	for {
		before := t
		for _, s := range c.Remove.Strings {
			t = strings.ReplaceAll(t, s, "")
		}
		if t == before {
			return t
		}
	}
}

// PresentCategories 返回对这个模型生效、且文本里含有其字符的类别名（按名字排序），和全部去掉以后的文本
func PresentCategories(cats map[string]Category, model string, texts ...string) (names []string, rest []string) {
	var all []string
	for n := range cats {
		all = append(all, n)
	}
	sort.Strings(all)
	rest = slices.Clone(texts)
	for _, n := range all {
		c := cats[n]
		if !slices.Contains(c.Models, model) {
			continue
		}
		hit := false
		for _, s := range texts {
			if c.Strip(s) != s {
				hit = true
			}
		}
		if !hit {
			continue
		}
		names = append(names, n)
	}
	for _, n := range names {
		for i := range rest {
			rest[i] = cats[n].Strip(rest[i])
		}
	}
	return names, rest
}
