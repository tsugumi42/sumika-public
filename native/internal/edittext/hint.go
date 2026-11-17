package edittext

// 编辑未命中时给模型的提示（规格-用户钩子 第七节，审查 D4）。
// 参考：opencode v2 的 edit 工具 missHint（先查 newText 是否已在文件里、按"忽略空白后逐行相等"投票定位、只显示命中范围）、
// Hermes 的 fuzzy_match 相似行回退和"→ 是 Tab、· 是空格"的可视化。
// 这里只算文字，不读文件；要不要给由调用方定（受保护的文件不给，审批预览不给）。原报错的字不变，提示只追加在后面。

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// MissError 是"找不到"或"不唯一"：Error() 和以前一模一样，调用方要提示时用 errors.As 取出来
type MissError struct {
	Msg   string
	Index int // 第几项编辑（0 起）
	Count int // 0 是找不到；大于 1 是出现了几次
}

func (e *MissError) Error() string { return e.Msg }

const (
	hintMaxLines       = 12    // 显示原文最多几行
	hintMaxLineRunes   = 200   // 每行最多几个字
	hintMaxPositions   = 8     // 不唯一时最多列几处
	hintMinNewTextLen  = 20    // newText 去空白后至少这么长，才拿它判断"已经改过了"
	hintFuzzyMaxLines  = 20000 // 相似度这步只对不太大的文件做
	hintFuzzyMaxBytes  = 2 << 20
	hintFuzzyThreshold = 0.6
	hintFuzzyPickLines = 5 // 拿 oldText 最长的几行去比
)

// Hint 返回追加在报错后面的提示；没有可说的就是空。content 是文件原文（换行不限），edits 是这次的全部编辑
func Hint(content string, edits []Pair, miss *MissError) string {
	if miss == nil || miss.Index < 0 || miss.Index >= len(edits) {
		return ""
	}
	content = normalizeToLF(strings.TrimPrefix(content, "\uFEFF"))
	e := Pair{normalizeToLF(edits[miss.Index].OldText), normalizeToLF(edits[miss.Index].NewText)}
	if miss.Count > 1 {
		return duplicateHint(content, e.OldText)
	}
	return missHint(content, e)
}

func squeeze(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func meaningful(line string) bool {
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func missHint(content string, e Pair) string {
	// 1. newText 已经在文件里：多半是上一次其实改成功了
	if ns := squeeze(e.NewText); len([]rune(ns)) >= hintMinNewTextLen && strings.Contains(squeeze(content), ns) {
		return "提示：newText 的内容已经在这个文件里了，这处可能已经改过。先 read 看看现状再决定要不要改。"
	}
	lines := strings.Split(content, "\n")
	old := strings.Split(e.OldText, "\n")
	// 2. 忽略空白后逐行比对，投票定位
	if first, last, hit, ok := voteRange(lines, old); ok {
		var b strings.Builder
		fmt.Fprintf(&b, "提示：文件第 %d～%d 行和 oldText 逐行对得上，只是空白（缩进、行尾、空格和 Tab）不一样。文件里的原文是：\n", first+1, last+1)
		b.WriteString(excerpt(lines, first, last))
		if v := whitespaceDiff(lines, old, hit); v != "" {
			b.WriteString("\n" + v)
		}
		b.WriteString("\n照原文的空白重写 oldText（别加行号），或者把 oldText 缩到更短的一两行。")
		return b.String()
	}
	// 3. 差一个参数、记错一个词：按字二元组相似度找最像的行
	if len(content) <= hintFuzzyMaxBytes && len(lines) <= hintFuzzyMaxLines {
		if at, score, ok := similarLine(lines, old); ok {
			from, to := max(at-1, 0), min(at+1, len(lines)-1)
			return fmt.Sprintf("提示：文件里最接近 oldText 的是第 %d 行（相似度 %.2f），附近的原文是：\n%s\n看看是不是哪个词或参数记错了；原文逐字照抄。", at+1, score, excerpt(lines, from, to))
		}
	}
	return ""
}

// voteRange：oldText 里每个有字的行，和文件里"去掉全部空白后相等"的行投票给起点（文件行号 - oldText 行号）。
// 至少一半的行同意才算；返回文件里首末命中的行（含）和命中的 (oldText 行号 → 文件行号)
func voteRange(lines, old []string) (first, last int, hit map[int]int, ok bool) {
	index := map[string][]int{}
	for i, l := range lines {
		if meaningful(l) {
			s := squeeze(l)
			index[s] = append(index[s], i)
		}
	}
	need := 0
	votes := map[int]int{}
	for oi, l := range old {
		if !meaningful(l) {
			continue
		}
		need++
		for _, j := range index[squeeze(l)] {
			votes[j-oi]++
		}
	}
	if need == 0 {
		return 0, 0, nil, false
	}
	best, bestVotes := 0, 0
	for start, n := range votes {
		if n > bestVotes || (n == bestVotes && start < best) {
			best, bestVotes = start, n
		}
	}
	if bestVotes*2 < need {
		return 0, 0, nil, false
	}
	hit = map[int]int{}
	first, last = len(lines), -1
	for oi, l := range old {
		if !meaningful(l) {
			continue
		}
		j := best + oi
		if j >= 0 && j < len(lines) && squeeze(lines[j]) == squeeze(l) {
			hit[oi] = j
			first, last = min(first, j), max(last, j)
		}
	}
	return first, last, hit, last >= first
}

// excerpt 是 lines[from..to] 的原文，最多 hintMaxLines 行，每行最多 hintMaxLineRunes 个字；不加行号（加了模型会抄进 oldText）
func excerpt(lines []string, from, to int) string {
	var out []string
	for i := from; i <= to && i < len(lines); i++ {
		if len(out) == hintMaxLines {
			out = append(out, fmt.Sprintf("……（后面还有 %d 行没显示）", to-i+1))
			break
		}
		l := lines[i]
		if r := []rune(l); len(r) > hintMaxLineRunes {
			l = string(r[:hintMaxLineRunes]) + "…"
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// visible 把开头的空白画出来：→ 是 Tab，· 是空格
func visible(line string) string {
	body := strings.TrimLeft(line, " \t")
	lead := line[:len(line)-len(body)]
	lead = strings.ReplaceAll(strings.ReplaceAll(lead, "\t", "→"), " ", "·")
	return lead + body
}

// whitespaceDiff 找第一行"只差开头空白"的，把文件和 oldText 两边的开头空白画出来
func whitespaceDiff(lines, old []string, hit map[int]int) string {
	for oi := 0; oi < len(old); oi++ {
		j, ok := hit[oi]
		if !ok || lines[j] == old[oi] {
			continue
		}
		if strings.TrimLeft(lines[j], " \t") != strings.TrimLeft(old[oi], " \t") {
			continue
		}
		cut := func(s string) string {
			if r := []rune(s); len(r) > 80 {
				return string(r[:80]) + "…"
			}
			return s
		}
		return "开头空白对比（→ 是 Tab，· 是空格）：\n文件里：" + cut(visible(lines[j])) + "\noldText：" + cut(visible(old[oi]))
	}
	return ""
}

// bigrams 是字二元组的计数
func bigrams(s string) (map[[2]rune]int, int) {
	rs := []rune(strings.Join(strings.Fields(s), " "))
	m := map[[2]rune]int{}
	n := 0
	for i := 0; i+1 < len(rs); i++ {
		m[[2]rune{rs[i], rs[i+1]}]++
		n++
	}
	return m, n
}

func dice(a map[[2]rune]int, na int, b map[[2]rune]int, nb int) float64 {
	if na == 0 || nb == 0 {
		return 0
	}
	common := 0
	for k, x := range a {
		common += min(x, b[k])
	}
	return 2 * float64(common) / float64(na+nb)
}

// similarLine：取 oldText 最长的几行，和文件每行比相似度，不低于阈值的取最高，返回文件行号
func similarLine(lines, old []string) (at int, score float64, ok bool) {
	var picks []string
	for _, l := range old {
		if meaningful(l) && len([]rune(strings.TrimSpace(l))) >= 8 {
			picks = append(picks, strings.TrimSpace(l))
		}
	}
	for i := 1; i < len(picks); i++ { // 插入排序：长的在前（行数不多）
		for j := i; j > 0 && len([]rune(picks[j])) > len([]rune(picks[j-1])); j-- {
			picks[j], picks[j-1] = picks[j-1], picks[j]
		}
	}
	if len(picks) > hintFuzzyPickLines {
		picks = picks[:hintFuzzyPickLines]
	}
	type pickGrams struct {
		m map[[2]rune]int
		n int
	}
	grams := make([]pickGrams, len(picks))
	for i, p := range picks {
		grams[i].m, grams[i].n = bigrams(p)
	}
	best := -1.0
	for j, l := range lines {
		if !meaningful(l) {
			continue
		}
		lm, ln := bigrams(l)
		for _, g := range grams {
			if s := dice(g.m, g.n, lm, ln); s > best {
				best, at = s, j
			}
		}
	}
	if best < hintFuzzyThreshold {
		return 0, 0, false
	}
	return at, best, true
}

// duplicateHint：不唯一时列出每处的行号，好让模型加上下文
func duplicateHint(content, oldText string) string {
	fc, fo := normalizeForFuzzyMatch(content), normalizeForFuzzyMatch(oldText)
	if fo == "" {
		return ""
	}
	var rows []int
	from := 0
	for len(rows) < hintMaxPositions+1 {
		i := strings.Index(fc[from:], fo)
		if i < 0 {
			break
		}
		at := from + i
		rows = append(rows, strings.Count(fc[:at], "\n")+1)
		from = at + len(fo)
	}
	if len(rows) == 0 {
		return ""
	}
	more := ""
	if len(rows) > hintMaxPositions {
		rows, more = rows[:hintMaxPositions], "等"
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = fmt.Sprint(r)
	}
	return "提示：这段文字分别在第 " + strings.Join(parts, "、") + " 行" + more + "开头；在 oldText 里多带上前后一两行，让它只对上一处。"
}

// AsMiss 取出 MissError；没有就是 nil
func AsMiss(err error) *MissError {
	var m *MissError
	if errors.As(err, &m) {
		return m
	}
	return nil
}
