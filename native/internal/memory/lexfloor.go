package memory

import (
	"math"
	"strings"
)

// 全文那一路的相关度下限（规格-记忆 第七节第 3 步）。
//
// 全文表按两字重叠着切，查询用 OR 连起来：问句里"是什么""的""周五"随便一个词撞上，就能拿到名次，
// 而 RRF 只看名次、不看分数，所以库里没有相关内容时，也会把撞上几个常用字的无关条目排进结果。
// 这里在 RRF 之前给全文命中加一道"词的稀有度"下限，口径取自 BM25 的 IDF：
//  1. 问句先按虚词、代词、助词、语气词切成内容段（同去停用词），单字段不算；内容段里的两字词、英文数字词是查询词；
//  2. 每个查询词的稀有度 = ln(N/df) / ln(N)：N 是库里的条数，df 是含这个词的条数，只出现一条是 1，每条都有是 0。
//     按 ln(N) 归一化，库大了不用改阈值；
//  3. 一条全文命中的分 = 它含有的查询词的稀有度之和（每个词算一次）；低于 LexicalFloor 的不进 RRF。
//     "周五""晚上"这类每隔几条就有的词单独撞上不够，需要有一个罕见词，或者几个词一起撞上；
//  4. 库里不到 minDocsForFloor 条时统计没有意义，不设下限；问句里没有查询词（"然后呢"）也不设。
//
// 为什么不用"问句被盖住的比例"：问句里常含要问的那一项（"专业""名字"），记忆里本来就没有，
// 合理命中的覆盖率也低；稀有度看的是撞上的词有多特别，不受问句长短影响。
//
// 向量那一路有自己的绝对下限 SimilarityFloor（余弦 0.6）。两路各过各的线，一条记忆有一路过线就进候选；
// RRF 本身不变。人名别名走 aliasRefs（subject 对上的事实直接进候选），不靠这里。
const (
	LexicalFloor    = 0.6
	minDocsForFloor = 30
)

// lexStop 是不承载内容的单字：虚词、代词、助词、语气词、常见的泛动词和量词、否定副词
const lexStop = "的了是么什吗呢吧啊哪有我你他她它这那在和与也都就还要会能可把被让给对为嘛哦呀谁几怎之而及或且但并从向用以于不没别很最好再又个些点着得地说过去来"

// queryTerms 把问句切成查询词：内容段里的两字词（重叠着切）和英文数字词，去重
func queryTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	var run []rune
	cur := scriptOther
	flush := func() {
		switch {
		case cur == scriptLatin && len(run) > 0:
			add(string(run))
		case len(run) >= 2:
			for i := 0; i+2 <= len(run); i++ {
				add(string(run[i : i+2]))
			}
		}
		run = run[:0]
	}
	for _, r := range q {
		r = fold(r)
		s := scriptOf(r)
		if s == scriptOther || (s != scriptLatin && strings.ContainsRune(lexStop, r)) {
			flush()
			cur = scriptOther
			continue
		}
		if s != cur {
			flush()
			cur = s
		}
		run = append(run, r)
	}
	flush()
	return out
}

// lexFloor 是一次召回用的下限：每个查询词的稀有度
type lexFloor struct {
	rarity map[string]float64
}

// newLexFloor 查库里的词频；不需要下限（库太小、没有查询词、查询出错）时返回 nil
func (m *Memory) newLexFloor(q string) *lexFloor {
	terms := queryTerms(q)
	if len(terms) == 0 || m.index == nil {
		return nil
	}
	var n int
	if m.index.db.QueryRow(`SELECT count(DISTINCT ref) FROM memory_bigram`).Scan(&n) != nil || n < minDocsForFloor {
		return nil
	}
	f := &lexFloor{rarity: map[string]float64{}}
	for _, t := range terms {
		var df int
		match := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		if m.index.db.QueryRow(`SELECT count(DISTINCT ref) FROM memory_bigram WHERE memory_bigram MATCH ?`, match).Scan(&df) != nil {
			return nil
		}
		if df > 0 {
			f.rarity[t] = math.Max(0, math.Log(float64(n)/float64(df))/math.Log(float64(n)))
		}
	}
	return f
}

// relevant 判断一条条目的文字是否过线
func (f *lexFloor) relevant(text string) bool {
	if f == nil {
		return true
	}
	sum := 0.0
	for _, b := range Bigrams(text) {
		sum += f.rarity[b]
	}
	return sum >= LexicalFloor
}
