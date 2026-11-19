package channel

// 改写（规格-渠道中间件 第十节 10.1 第 2 步、ADR-110、ADR-114）：去句尾句号，和拆条（split.go）合起来就是 Bun 的 asSentQQ。
// 搬自 daemon/plugins/qq.ts 的 splitMessage、pairDepth、dropOnce、dropTrailingPeriods、asSentQQ，行为一样；
// testdata/asent.json 是 Bun 版对一批编的句子的输出（sumika-bun 仓库的 scripts/go-golden-asent.ts 生成），rewrite_test.go 逐条对。
// 和 Bun 的差别只有一处：位置按字（rune）算，Bun 按 UTF-16 单元算，含 emoji（四字节）的句子 Bun 里一个 emoji 占两个位置，
// 这里占一个，所以不会切在 emoji 中间（规格 10.2：拆条和截短按字符边界切）。
//
// 一条话「改写」的结果就是之后库、历史、发送用的那一个字（三处一致，ADR-114）。改写是幂等的：改过的再改一遍不变。

import (
	"regexp"
	"strings"

	"sumika-native/internal/textclean"
)

// jsWS 是 JS 的 \s 在 Go 正则字符类里的写法（Go 的 \s 只认 ASCII 空白，JS 的还认不换行空格、全角空格等）
const jsWS = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var blankLineRE = regexp.MustCompile(`\n[` + jsWS + `]*\n`)

// chunkMax：QQ 单条消息的长度上限按保守的 1500 字算
const chunkMax = 1500

// splitMessage 按空行拆成几条；单段太长再按换行、句号拆（Bun 的 splitMessage）
func splitMessage(text string) []string {
	var out []string
	for _, p := range blankLineRE.Split(text, -1) {
		p = textclean.Trim(p)
		if p == "" {
			continue
		}
		r := []rune(p)
		for len(r) > chunkMax {
			nl := lastIndexRune(r, '\n', chunkMax)
			cut := max(nl, lastIndexRune(r, '。', chunkMax)+1)
			at := chunkMax
			if cut > chunkMax/2 {
				at = cut
			}
			if head := textclean.Trim(string(r[:at])); head != "" {
				out = append(out, head)
			}
			r = []rune(textclean.Trim(string(r[at:])))
		}
		if len(r) > 0 {
			out = append(out, string(r))
		}
	}
	return out
}

// lastIndexRune 是 JS 的 lastIndexOf(c, from)：从 from（含）往前找，找不到 -1
func lastIndexRune(r []rune, c rune, from int) int {
	for i := min(from, len(r)-1); i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// 成对的引号、括号（全角、半角圆括号算一种）：左边的、右边的
var quotePairs = [][2]string{{"「", "」"}, {"『", "』"}, {"“", "”"}, {"（(", "）)"}, {"《", "》"}, {"【", "】"}}

var openKind, closeKind = func() (map[rune]int, map[rune]int) {
	o, c := map[rune]int{}, map[rune]int{}
	for t, p := range quotePairs {
		for _, ch := range p[0] {
			o[ch] = t
		}
		for _, ch := range p[1] {
			c[ch] = t
		}
	}
	return o, c
}()

// pairDepth：每个位置在几层配上了的引号、括号里面（>0 就是在里面）。右边的找最近一个同类的左边的配；后面没有配上的左边的不算
// （颜文字"好难过 :("、"我说（小声"照常去后面几行的句号），配不上的右边的也不算
func pairDepth(r []rune) []int {
	d := make([]int, len(r)+1)
	type open struct{ at, kind int }
	var stack []open
	for i, ch := range r {
		if k, ok := openKind[ch]; ok {
			stack = append(stack, open{i, k})
			continue
		}
		k, ok := closeKind[ch]
		if !ok {
			continue
		}
		j := len(stack) - 1
		for j >= 0 && stack[j].kind != k {
			j--
		}
		if j < 0 {
			continue
		}
		d[stack[j].at+1]++
		d[i]--
		stack = stack[:j] // 中间没合上的一起丢掉
	}
	for i := 1; i <= len(r); i++ {
		d[i] += d[i-1]
	}
	return d
}

func isWS(r rune) bool { return textclean.IsJSSpace(r) }

// endPeriod 找一段文字末尾单独的一个"。"：前面得是一个不是空白、不是"。"的字；返回「要从哪里砍掉」（空白的起点）和「。」的位置。
// 对应 Bun 的 END_PERIOD = /(?<=[^\s。])\s*。\s*$/
func endPeriod(r []rune) (cutAt, periodAt int, ok bool) {
	e := len(r)
	for e > 0 && isWS(r[e-1]) {
		e--
	}
	if e == 0 || r[e-1] != '。' {
		return 0, 0, false
	}
	k := e - 1
	j := k
	for j > 0 && isWS(r[j-1]) {
		j--
	}
	if j == 0 || r[j-1] == '。' {
		return 0, 0, false
	}
	return j, k, true
}

// dropOnce 去一遍：整条末尾（含最后一行只有"。"的："好\n。"）、每行行尾，"。"在配上了的引号括号里的不动（inside 给的是这一条里的位置）。
// 先砍整条的尾巴、再砍每行的尾巴，都只砍尾巴，前面的位置不变，inside 一直对得上
func dropOnce(chunk string, inside func(i int) bool) string {
	r := []rune(chunk)
	if cut, at, ok := endPeriod(r); ok && !inside(at) {
		r = r[:cut]
	}
	var out strings.Builder
	pos := 0
	for pos <= len(r) {
		// 一行：到下一个 \n（前面带 \r 的，\r\n 是一个换行）为止
		end := pos
		for end < len(r) && r[end] != '\n' {
			end++
		}
		line, sepStart := r[pos:end], end
		if end < len(r) && end > pos && r[end-1] == '\r' {
			line, sepStart = r[pos:end-1], end-1
		}
		if cut, at, ok := endPeriod(line); ok && !inside(pos+at) {
			line = line[:cut]
		}
		out.WriteString(string(line))
		if end >= len(r) {
			break
		}
		out.WriteString(string(r[sepStart : end+1]))
		pos = end + 1
	}
	return out.String()
}

// dropPeriodsOnce 整段去一遍：按 splitMessage 拆开，引号括号按整段配（空行隔开的"他说「好。\n\n我知道了。」"前一条也在引号里），每一条 dropOnce
func dropPeriodsOnce(text string) string {
	parts := splitMessage(text)
	d := pairDepth([]rune(strings.Join(parts, "\n")))
	at := 0
	changed := false
	chunks := make([]string, len(parts))
	for i, p := range parts {
		base := at
		at += len([]rune(p)) + 1
		chunks[i] = dropOnce(p, func(k int) bool { return d[base+k] > 0 })
		if chunks[i] != p {
			changed = true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(chunks, "\n\n")
}

// dropTrailingPeriods 是她的一整段话去句尾句号：按 splitMessage 拆开、每一条每行去行尾句号，有去掉的就用空行连起来，没有就原样返回（不动空白）。
// 去到不再变为止
func dropTrailingPeriods(text string) string {
	for cur := text; ; {
		next := dropPeriodsOnce(cur)
		if next == cur {
			return cur
		}
		cur = next
	}
}

// asSentQQ 是她的一整段话发到 QQ 是什么样（规格-发送前拆条 + 去句尾句号）：先去句尾句号；空行分开的每一条分别交给规则（splitRound）定拆不拆，
// 整段最多 3 条，拆了用空行把几条接起来（出口再按空行拆开连着发）。模型自己用单个换行排的多行气泡不动。
// 库、历史、发出去的都是这一个结果；再过一遍不变（规则一直跑到不再拆为止，每次拆都多一条、最多 3 条）
func asSentQQ(text string) string {
	t := dropTrailingPeriods(text)
	parts := splitMessage(t)
	for _, p := range parts {
		if strings.Contains(p, "\n") {
			return t
		}
	}
	cur := parts
	for i := 0; i < 3; i++ {
		r := splitRound(cur, mainRule)
		if r.Status != roundSplit {
			break
		}
		cur = r.Pieces
	}
	if len(cur) == len(parts) {
		return t
	}
	return dropTrailingPeriods(strings.Join(cur, "\n\n"))
}
