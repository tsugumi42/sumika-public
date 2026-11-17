package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/embedinputs"
	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/memory"
)

// 编的语料：326 条记忆式条目、100 条查询、10 个重排的查询（各 20 个候选）。全是编的，没有真实人名和聊天（testdata/corpus.json）。
//
//go:embed testdata/corpus.json
var corpusJSON []byte

// 样本的来源（Set）。真实样本的名字带 "real-" 前缀，输出里只写来源和下标，不写文字。
const (
	setCases       = "cases"        // tokenizer/testdata/cases.json：调研的 42 句加边界样本
	setZoo         = "zoo"          // 夹在字母之间的每种空白字符（a<c>b）
	setCorpusItems = "corpus-items" // 编的语料：条目
	setCorpusQ     = "corpus-queries"
	setCorpusSent  = "corpus-sentences"
	setRealItems   = "real-items"
	setRealQ       = "real-queries"
	setRealGroup   = "real-group"
	setRealSent    = "real-sentences"
)

// skip 的原因。
const (
	skipOverLimit = "over-limit" // 分词后超过 Go 的单行上限：两边都不送（Python 那边会做 8192 token 的前向，要单独跑，见 -long）
	skipBlank     = "blank"      // 按 Python 的空白集合是空的：只比状态码（两边都该 400）
)

// tokenCounter 是数 token 的东西（*tokenizer.Tokenizer）；测试里换成假的。
type tokenCounter interface {
	Count(text string) (int, error)
}

type sample struct {
	Text   string
	Set    string
	Idx    int    // 在 Set 里的下标
	Cat    string // 编的样本的类别（cases 有）
	Type   string // input_type：document / query
	Skip   string
	Tokens int      // Go 分词器数的 token 数（含两个特殊符号），数不出为 0
	Short  bool     // 单句计时用：非跳过、≤ 64 token 的 cases
	Also   []string // 同一段文字在别的来源里也出现过（同一段只送一次，但相似度要按每个来源的角色各算：条目也是守卫的句子）
}

func (s sample) label() string {
	if s.Cat != "" {
		return fmt.Sprintf("%s#%d(%s)", s.Set, s.Idx, s.Cat)
	}
	return fmt.Sprintf("%s#%d", s.Set, s.Idx)
}

type pairSample struct {
	Q, D string
	Name string // 编的对有名字；真实的没有
	Set  string
	Idx  int
}

func (p pairSample) label() string {
	if p.Name != "" {
		return fmt.Sprintf("%s#%d(%s)", p.Set, p.Idx, p.Name)
	}
	return fmt.Sprintf("%s#%d", p.Set, p.Idx)
}

type groupSample struct {
	Q    string
	Docs []string
	Set  string
	Idx  int
}

type plan struct {
	Texts  []sample
	index  map[string]int // 文本 → Texts 下标（同一段文字只记一次，先到的来源算数）
	Pairs  []pairSample   // 一对一对问的重排（tokenizer/testdata/pairs.json）
	Groups []groupSample  // 一个查询对一组候选，问一次，比排序（编的语料 + 真实）
	Zoo    []string       // 单独一个空白 / 控制字符的输入，只比状态码
	Hash   string         // 样本集的哈希：-ref-in 的参照和这次的样本对得上才用
	Real   bool
}

// sourceFiles 是编的样本放在哪（-testdata 指到 native/internal/localmodels）。
type sourceFiles struct{ root string }

func (s sourceFiles) path(parts ...string) string {
	return filepath.Join(append([]string{s.root}, parts...)...)
}

type caseEntry struct {
	Cat   string `json:"cat"`
	Text  string `json:"text"`
	Unit  string `json:"unit"`
	Count int    `json:"count"`
}

func (c caseEntry) expand() string {
	if c.Unit != "" {
		return strings.Repeat(c.Unit, c.Count)
	}
	return c.Text
}

type pairEntry struct {
	Name string    `json:"name"`
	Q    caseEntry `json:"q"`
	D    caseEntry `json:"d"`
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("解析 %s：%w", filepath.Base(path), err)
	}
	return nil
}

func (s sourceFiles) cases() ([]caseEntry, error) {
	var out []caseEntry
	return out, readJSON(s.path("tokenizer", "testdata", "cases.json"), &out)
}

func (s sourceFiles) probes() ([]string, error) {
	var in []struct {
		Text string `json:"text"`
	}
	if err := readJSON(s.path("tokenizer", "testdata", "probes.json"), &in); err != nil {
		return nil, err
	}
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = p.Text
	}
	return out, nil
}

func (s sourceFiles) pairs() ([]pairEntry, error) {
	var in struct {
		Pairs []pairEntry `json:"pairs"`
	}
	return in.Pairs, readJSON(s.path("tokenizer", "testdata", "pairs.json"), &in)
}

func (s sourceFiles) whitespace() ([]rune, error) {
	var in struct {
		IsSpace []int `json:"isspace"`
	}
	if err := readJSON(s.path("engine", "testdata", "ref-whitespace.json"), &in); err != nil {
		return nil, err
	}
	out := make([]rune, len(in.IsSpace))
	for i, cp := range in.IsSpace {
		out[i] = rune(cp)
	}
	return out, nil
}

// isPySpace：Python 的 str.strip() 会剥掉的字符（和 isspace 同一个集合，engine/testdata/ref-whitespace.json）。
func isPySpace(set map[rune]bool, s string) bool {
	for _, r := range s {
		if !set[r] {
			return false
		}
	}
	return true
}

// buildPlan 把编的样本和（有的话）真实样本排成这次要送的清单。gte 是用来数 token 数的 Go 分词器；lineLimit 是 Go 的单行上限，
// 超过的两边都不送。真实样本里的条目 / 查询 / 群消息 / 句子按各自的 input_type 记；同一段文字只记一次。
func buildPlan(src sourceFiles, gte tokenCounter, gteSpec engine.Spec, lineLimit int, real *embedinputs.File) (*plan, error) {
	cases, err := src.cases()
	if err != nil {
		return nil, err
	}
	spaces, err := src.whitespace()
	if err != nil {
		return nil, err
	}
	spaceSet := map[rune]bool{}
	for _, r := range spaces {
		spaceSet[r] = true
	}
	pairs, err := src.pairs()
	if err != nil {
		return nil, err
	}
	var corpus embedinputs.File
	if err := json.Unmarshal(corpusJSON, &corpus); err != nil {
		return nil, fmt.Errorf("解析内嵌的 corpus.json：%w", err)
	}
	for _, it := range corpus.Items { // 守卫的句子由条目切出来，和 memory-eval 导出时一样
		corpus.Sentences = appendUnique(corpus.Sentences, memory.OutboundSentences(it)...)
	}

	p := &plan{index: map[string]int{}, Real: real != nil}
	add := func(text, set string, idx int, cat, typ string) {
		if at, dup := p.index[text]; dup {
			if p.Texts[at].Set != set && !slices.Contains(p.Texts[at].Also, set) {
				p.Texts[at].Also = append(p.Texts[at].Also, set)
			}
			return
		}
		s := sample{Text: text, Set: set, Idx: idx, Cat: cat, Type: typ}
		switch {
		case isPySpace(spaceSet, text):
			s.Skip = skipBlank
		case utf8.RuneCountInString(text) > gteSpec.MaxTokens*engine.CharsPerToken:
			s.Skip = skipOverLimit
		default:
			n, err := gte.Count(text)
			if err != nil {
				s.Skip = skipOverLimit
			} else if s.Tokens = n; n > lineLimit {
				s.Skip = skipOverLimit
			}
		}
		s.Short = set == setCases && s.Skip == "" && s.Tokens > 0 && s.Tokens <= 64
		p.index[text] = len(p.Texts)
		p.Texts = append(p.Texts, s)
	}
	for i, c := range cases {
		add(c.expand(), setCases, i, c.Cat, typeDoc)
	}
	for i, r := range spaces {
		p.Zoo = append(p.Zoo, string(r))
		add("a"+string(r)+"b", setZoo, i, fmt.Sprintf("U+%04X", r), typeDoc)
	}
	for i, t := range corpus.Items {
		add(t, setCorpusItems, i, "", typeDoc)
	}
	for i, t := range corpus.Queries {
		add(t, setCorpusQ, i, "", typeQuery)
	}
	for i, t := range corpus.Sentences {
		add(t, setCorpusSent, i, "", typeDoc)
	}
	if real != nil {
		for i, t := range real.Items {
			add(t, setRealItems, i, "", typeDoc)
		}
		for i, t := range real.Queries {
			add(t, setRealQ, i, "", typeQuery)
		}
		for i, t := range real.Group {
			add(t, setRealGroup, i, "", typeQuery)
		}
		sents := real.Sentences
		if len(sents) == 0 {
			for _, it := range real.Items {
				sents = appendUnique(sents, memory.OutboundSentences(it)...)
			}
		}
		for i, t := range sents {
			add(t, setRealSent, i, "", typeDoc)
		}
	}
	for i, pe := range pairs {
		p.Pairs = append(p.Pairs, pairSample{Q: pe.Q.expand(), D: pe.D.expand(), Name: pe.Name, Set: "pairs", Idx: i})
	}
	for i, g := range corpus.Rerank {
		p.Groups = append(p.Groups, groupSample{Q: g.Query, Docs: g.Docs, Set: "corpus-rerank", Idx: i})
	}
	if real != nil {
		for i, g := range real.Rerank {
			p.Groups = append(p.Groups, groupSample{Q: g.Query, Docs: g.Docs, Set: "real-rerank", Idx: i})
		}
	}
	h := sha256.New()
	for _, s := range p.Texts {
		fmt.Fprintf(h, "t%d:%s:%s:%s:%s\x00", len(s.Text), s.Set, s.Type, s.Skip, s.Text)
	}
	for _, s := range p.Zoo {
		fmt.Fprintf(h, "z%d:%s\x00", len(s), s)
	}
	for _, q := range p.Pairs {
		fmt.Fprintf(h, "p%d:%s\x00%s\x00", len(q.Q), q.Q, q.D)
	}
	for _, g := range p.Groups {
		fmt.Fprintf(h, "g%d:%s\x00", len(g.Q), g.Q)
		for _, d := range g.Docs {
			fmt.Fprintf(h, "%d:%s\x00", len(d), d)
		}
	}
	p.Hash = hex.EncodeToString(h.Sum(nil))[:16]
	return p, nil
}

func appendUnique(dst []string, add ...string) []string {
	have := make(map[string]bool, len(dst))
	for _, s := range dst {
		have[s] = true
	}
	for _, s := range add {
		if !have[s] {
			have[s] = true
			dst = append(dst, s)
		}
	}
	return dst
}

// set 返回某个来源的样本下标。
func (p *plan) set(names ...string) []int {
	var out []int
	for i, s := range p.Texts {
		for _, n := range names {
			if (s.Set == n || slices.Contains(s.Also, n)) && s.Skip == "" {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

func (p *plan) counts() map[string]int {
	c := map[string]int{}
	for _, s := range p.Texts {
		c[s.Set]++
		if s.Skip != "" {
			c["skipped:"+s.Skip]++
		}
	}
	c["zoo-alone"] = len(p.Zoo)
	c["pairs"] = len(p.Pairs)
	c["groups"] = len(p.Groups)
	return c
}
