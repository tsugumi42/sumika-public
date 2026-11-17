package channel

// 拆条（规格-渠道中间件 第十节 10.2，规格-发送前拆条，ADR-129、ADR-163）：她在 QQ 里的一次回复，按标点再拆成几条连着发。纯函数，不调模型。
// 搬自 daemon/qq/split.ts 的「主参数」一套（splitRound、planMessage、scan、findCuts），参数不改：改参数要重跑离线实验的表，不凭感觉调。
// 随机数（mulberry32、fnv）也搬了，按 UTF-16 单元算 fnv，同一句话在 Go 和 Bun 里拆法一样（testdata/asent.json 逐条对）。
// 位置按字（rune）算：Bun 里 emoji 占两个位置，这里一个，不会切在 emoji 中间。

import (
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"sumika-native/internal/textclean"
)

// ───────────────────────── 度量 ─────────────────────────

var (
	urlRE   = regexp.MustCompile(`https?://[^` + jsWS + `]+`)
	wsAnyRE = regexp.MustCompile(`[` + jsWS + `]+`)
)

// bodyOf 去掉网址、开头的 @ 几个人，剩下的正文
func bodyOf(s string) string {
	s = urlRE.ReplaceAllString(s, " ")
	// /^\s*(@\S+\s*)+/：开头可以有空白，然后一个或多个「@ 加一串非空白，后面可以有空白」；一个都对不上就什么也不去
	r := []rune(s)
	i := 0
	for i < len(r) && isWS(r[i]) {
		i++
	}
	matched := false
	for i < len(r) && r[i] == '@' {
		j := i + 1
		for j < len(r) && !isWS(r[j]) {
			j++
		}
		if j == i+1 {
			break
		}
		for j < len(r) && isWS(r[j]) {
			j++
		}
		matched = true
		i = j
	}
	if matched {
		return textclean.Trim(string(r[i:]))
	}
	return textclean.Trim(s)
}

// charsOf 是正文去掉空白后的字数
func charsOf(s string) int { return utf8.RuneCountInString(wsAnyRE.ReplaceAllString(bodyOf(s), "")) }

// ───────────────────────── 随机数 ─────────────────────────

func mulberry32(a uint32) func() float64 {
	return func() float64 {
		a += 0x6d2b79f5
		t := (a ^ (a >> 15)) * (1 | a)
		t = (t + (t^(t>>7))*(61|t)) ^ t
		return float64((t^(t>>14))&0xffffffff) / 4294967296
	}
}

// fnv 是按 UTF-16 单元算的 FNV-1a（和 Bun 的 fnv 一样）
func fnv(s string) uint32 {
	h := uint32(2166136261)
	for _, u := range utf16.Encode([]rune(s)) {
		h ^= uint32(u)
		h *= 16777619
	}
	return h
}

// ───────────────────────── 主参数 ─────────────────────────

type ruleParams struct {
	L0                  float64 // 每条「想要」的长度（词数）；每条的代价 = (词数 / L0)²：越长越贵，所以长的拆、短的不拆
	maxPieces           int     // 一轮最多几条
	costStrong          float64 // 切口代价：断句标点（。！？～——……换行）后面切
	costComma           float64 // 逗号、分号后面切（备用切口；顿号「、」是并列，不当切口）
	costCommaInQuestion float64 // 逗号落在「以 ？！ 结尾的句子」里面，额外加的代价
	costSpace           float64 // 汉字之间的空格（当逗号用）
	minPiece            int     // 切出来少于这么多词的并回去（除了单独的语气词）
	longTol             int     // 拆完还有一条不短于这么多词、又没法再安全地切 → 标「交给模型」
	riskMargin          float64 // 危险切口如果能让总代价再降这么多以上才标「交给模型」
	prob                float64 // 规则认为该拆的，按这个概率真的拆
	seed                uint32
	flagWhole           bool // 标了「交给模型」的整条不拆（主人说的：只统计，不调模型）
	allowComma          bool
	allowSpace          bool
}

var mainRule = ruleParams{
	L0: 10, maxPieces: 3, costStrong: 0.4, costComma: 0.8, costCommaInQuestion: 0.6, costSpace: 1.0,
	minPiece: 4, longTol: 20, riskMargin: 0.3, prob: 0.7, seed: 1, flagWhole: true, allowComma: true, allowSpace: true,
}

// 语气词单独成条可以（≤3 字也不并回去）
const interjChars = "诶欸嗯哈啊哇噗唔哦噢呃嘿呜哎唉额咦嘻哼"

var (
	interjRE    = regexp.MustCompile(`^[` + interjChars + `]{1,4}$`)
	stripTailRE = regexp.MustCompile(`[` + jsWS + `，,。、；;！!？?～~…—.]+$`)
)

func stripTail(s string) string { return textclean.Trim(stripTailRE.ReplaceAllString(s, "")) }
func isInterj(s string) bool    { return interjRE.MatchString(stripTail(s)) }

// 危险切口：左边这段以这些结尾 = 话还没说完
var (
	leftBadEnd = regexp.MustCompile(`(的话|如果|要是|假如|假设|既然|因为|由于|虽然|尽管|即使|就算|哪怕|只要|除非|不管|无论|但是|可是|不过|然后|所以|而且|并且|或者|还是|以及|和|跟|与|把|被|对于|给|让|是不是|有没有|能不能|要不要|会不会|怎么|为什么|那么|这么)$`)
	// 左边这段里有从句连词 = 左边是个从句，后面的主句还没来
	leftSubAny = regexp.MustCompile(`(如果|要是|假如|假设|既然|因为|由于|虽然|尽管|即使|就算|哪怕|只要|除非|不管|无论|万一|一旦|与其|不仅|不但)`)
	// 右边这段以这些开头 = 是前面的附属成分，或者是接着前面主语的谓语（话题和评论被拆开）
	rightBadStart = regexp.MustCompile(`^(的话|的$|吧|吗|呢|嘛|而已|罢了|似的|来着|为止|之类|什么的|一样|还是|或者|要么|要不然|否则|以及|确实|其实|简直|真是|倒是|总算|才|也要|也得)`)
	rightHeadRE   = regexp.MustCompile(`^[` + jsWS + `“‘「『（(《【\["']+`)
	tailEndRE     = regexp.MustCompile(`[` + jsWS + `」』”）)》】\]"']+$`)
)

type cut struct {
	a, b       int    // 标点串 [a, b)：a 是标点串开始（左边这段到这里为止，不含标点），b 是结束（含后面的空白），右边这段从 b 开始
	kind       string // strong / comma / space
	tail       string // 要留在左边末尾的标点（？！～——……）
	uBefore    int    // 这个位置之前累计的词数
	inQuestion bool
	risky      bool
	period     bool
}

func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// extPict 是 Unicode 的 Extended_Pictographic（emoji 一族）的主要区间；Go 的标准库没有这张表
var extPict = [][2]rune{
	{0xA9, 0xA9}, {0xAE, 0xAE}, {0x203C, 0x203C}, {0x2049, 0x2049}, {0x2122, 0x2122}, {0x2139, 0x2139}, {0x2194, 0x2199}, {0x21A9, 0x21AA},
	{0x231A, 0x231B}, {0x2328, 0x2328}, {0x2388, 0x2388}, {0x23CF, 0x23CF}, {0x23E9, 0x23F3}, {0x23F8, 0x23FA}, {0x24C2, 0x24C2},
	{0x25AA, 0x25AB}, {0x25B6, 0x25B6}, {0x25C0, 0x25C0}, {0x25FB, 0x25FE}, {0x2600, 0x2605}, {0x2607, 0x2612}, {0x2614, 0x2685},
	{0x2690, 0x2705}, {0x2708, 0x2712}, {0x2714, 0x2714}, {0x2716, 0x2716}, {0x271D, 0x271D}, {0x2721, 0x2721}, {0x2728, 0x2728},
	{0x2733, 0x2734}, {0x2744, 0x2744}, {0x2747, 0x2747}, {0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757},
	{0x2763, 0x2767}, {0x2795, 0x2797}, {0x27A1, 0x27A1}, {0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2934, 0x2935}, {0x2B05, 0x2B07},
	{0x2B1B, 0x2B1C}, {0x2B50, 0x2B50}, {0x2B55, 0x2B55}, {0x3030, 0x3030}, {0x303D, 0x303D}, {0x3297, 0x3297}, {0x3299, 0x3299},
	{0x1F000, 0x1F0FF}, {0x1F10D, 0x1F10F}, {0x1F12F, 0x1F12F}, {0x1F16C, 0x1F171}, {0x1F17E, 0x1F17F}, {0x1F18E, 0x1F18E},
	{0x1F191, 0x1F19A}, {0x1F1AD, 0x1F1E5}, {0x1F201, 0x1F20F}, {0x1F21A, 0x1F21A}, {0x1F22F, 0x1F22F}, {0x1F232, 0x1F23A},
	{0x1F23C, 0x1F23F}, {0x1F249, 0x1F3FA}, {0x1F400, 0x1F53D}, {0x1F546, 0x1F64F}, {0x1F680, 0x1F6FF}, {0x1F774, 0x1F77F},
	{0x1F7D5, 0x1F7FF}, {0x1F80C, 0x1F80F}, {0x1F848, 0x1F84F}, {0x1F85A, 0x1F85F}, {0x1F888, 0x1F88F}, {0x1F8AE, 0x1F8FF},
	{0x1F90C, 0x1F93A}, {0x1F93C, 0x1F945}, {0x1F947, 0x1FAFF}, {0x1FC00, 0x1FFFD},
}

func isPicto(r rune) bool {
	lo, hi := 0, len(extPict)
	for lo < hi {
		m := (lo + hi) / 2
		switch {
		case r < extPict[m][0]:
			hi = m
		case r > extPict[m][1]:
			lo = m + 1
		default:
			return true
		}
	}
	return false
}

var (
	wordRE     = regexp.MustCompile(`^[￥$]?[A-Za-z0-9]+(?::\d+)*(?:\d*[.,]\d+)*(?:['’\-][A-Za-z0-9]+)*[%％]?`)
	bracketRE  = regexp.MustCompile("^(?:\\[[^\\[\\]\\n]*\\]|［[^［］\\n]*］|`[^`\\n]*`)")
	pairRES    = pairREs()
	mentionBad = "，,。！？!?：:、；;"
)

func pairREs() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range []string{`“[^“”]*”`, `‘[^‘’]*’`, `「[^「」]*」`, `『[^『』]*』`, `《[^《》]*》`, `【[^【】]*】`, `（[^（）]*）`, `\([^()]*\)`, `"[^"\n]*"`} {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}

// text 是一段文字的字（rune）数组加一张「字的位置 → 字节位置」表，正则在字节串上跑、结果换回字的位置
type text struct {
	s   string
	r   []rune
	off []int // off[i] 是第 i 个字的字节位置，off[len(r)] 是总长
}

func newText(s string) text {
	t := text{s: s, r: []rune(s)}
	t.off = make([]int, len(t.r)+1)
	b := 0
	for i, c := range t.r {
		t.off[i] = b
		b += utf8.RuneLen(c)
	}
	t.off[len(t.r)] = b
	return t
}

func (t text) runeAt(byteOff int) int {
	lo, hi := 0, len(t.r)
	for lo < hi {
		m := (lo + hi) / 2
		if t.off[m] < byteOff {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

type scanned struct {
	unit   []uint8 // 词的第一个字记 1
	inPair []bool  // 在引号括号里面
	inAtom []bool  // 在整块不可切的记号里面（网址、@某人、[表情包]、行内代码）
}

// atomLen 是 i 处的整块记号有多长（字数），不是记号返回 0：网址、@某人、[…]、［…］、`…`
func (t text) atomLen(i int) int {
	r := t.r
	rest := t.s[t.off[i]:]
	switch r[i] {
	case 'h':
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			j := i + len("http://")
			if strings.HasPrefix(rest, "https://") {
				j++
			}
			k := j
			for k < len(r) && !isWS(r[k]) {
				k++
			}
			if k > j {
				return k - i
			}
		}
	case '@':
		k := i + 1
		for k < len(r) && !isWS(r[k]) && !strings.ContainsRune(mentionBad, r[k]) {
			k++
		}
		if k > i+1 {
			return k - i
		}
	case '[', '［', '`':
		if m := bracketRE.FindStringIndex(rest); m != nil && m[1] > 0 {
			return utf8.RuneCountInString(rest[:m[1]])
		}
	}
	return 0
}

// scan 扫一遍：每个字符的「词数」、哪些在引号括号里面、哪些在整块不可切的记号里面
func (t text) scan() scanned {
	n := len(t.r)
	sc := scanned{unit: make([]uint8, n), inPair: make([]bool, n), inAtom: make([]bool, n)}
	for _, re := range pairRES {
		for _, m := range re.FindAllStringIndex(t.s, -1) {
			for k := t.runeAt(m[0]); k < t.runeAt(m[1]); k++ {
				sc.inPair[k] = true
			}
		}
	}
	for i := 0; i < n; {
		if l := t.atomLen(i); l > 0 {
			sc.unit[i] = 1
			for k := i; k < i+l; k++ {
				sc.inAtom[k] = true
			}
			i += l
			continue
		}
		c := t.r[i]
		if isHan(c) {
			sc.unit[i] = 1
			i++
			continue
		}
		if isPicto(c) {
			sc.unit[i] = 1
			i++
			for i < n && (t.r[i] == 0x200d || t.r[i] == 0xfe0f) {
				i++
			}
			continue
		}
		if m := wordRE.FindStringIndex(t.s[t.off[i]:]); m != nil && m[1] > 0 {
			sc.unit[i] = 1
			i += utf8.RuneCountInString(t.s[t.off[i] : t.off[i]+m[1]])
			continue
		}
		i++
	}
	return sc
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func isAlnum(r rune) bool { return isDigit(r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// at 取第 i 个字，越界给 0
func (t text) at(i int) rune {
	if i < 0 || i >= len(t.r) {
		return 0
	}
	return t.r[i]
}

var dotsRE = regexp.MustCompile(`\.{2,}`)

func hasStrong(run string) bool {
	rr := []rune(dotsRE.ReplaceAllString(run, "…"))
	for i, c := range rr {
		if strings.ContainsRune("。！!？?～~…—\n", c) {
			return true
		}
		if c == '.' && !(i+1 < len(rr) && isDigit(rr[i+1])) {
			return true
		}
	}
	return false
}

func hasPeriod(run string) bool {
	rr := []rune(run)
	for i, c := range rr {
		if c == '。' || (c == '.' && !(i+1 < len(rr) && rr[i+1] == '.')) {
			return true
		}
	}
	return false
}

// findCuts 找所有候选切口，N 是总词数
func (t text) findCuts(p ruleParams) ([]cut, int) {
	sc := t.scan()
	n := len(t.r)
	pref := make([]int, n+1)
	for i := 0; i < n; i++ {
		pref[i+1] = pref[i] + int(sc.unit[i])
	}
	N := pref[n]
	var cuts []cut
	isP := func(i int) bool {
		if sc.inAtom[i] || sc.inPair[i] {
			return false
		}
		c := t.r[i]
		switch {
		case strings.ContainsRune("，,；;。！!？?～…\n", c):
			return true
		case c == '~':
			return !(isDigit(t.at(i-1)) && isDigit(t.at(i+1)))
		case c == '—':
			return t.at(i+1) == '—' || t.at(i-1) == '—'
		case c == '.':
			return !(isAlnum(t.at(i-1)) && isAlnum(t.at(i+1)))
		}
		return false
	}
	for i := 0; i < n; {
		c := t.r[i]
		if sc.inAtom[i] || sc.inPair[i] {
			i++
			continue
		}
		if isP(i) || (isWS(c) && c != '\n') {
			// 一串标点 + 空白连在一起算一个切口
			j, hasP, hasNl := i, false, false
			for j < n && !sc.inAtom[j] && !sc.inPair[j] && (isP(j) || isWS(t.r[j])) {
				if isP(j) {
					hasP = true
					if t.r[j] == '\n' {
						hasNl = true
					}
				}
				j++
			}
			run := string(t.r[i:j])
			if !hasP {
				// 只有空白：汉字和汉字之间的空格当逗号；英文数字之间不切
				a, b := t.at(i-1), t.at(j)
				if p.allowSpace && a != 0 && b != 0 && isHan(a) && isHan(b) {
					cuts = append(cuts, cut{a: i, b: j, kind: "space", uBefore: pref[i]})
				}
				i = j
				continue
			}
			kind := "comma"
			if hasNl || hasStrong(run) {
				kind = "strong"
			}
			// 留在左边末尾的：？！～——……（句号、逗号、分号、空白都不留）
			tailRun := dotsRE.ReplaceAllString(run, "…")
			var tail strings.Builder
			for _, ch := range tailRun {
				if strings.ContainsRune("？！!?～~…—", ch) {
					tail.WriteRune(ch)
				}
			}
			tl := dashRunRE.ReplaceAllStringFunc(tail.String(), func(m string) string {
				if utf8.RuneCountInString(m) >= 2 {
					return m
				}
				return ""
			})
			period := hasPeriod(run) && kind == "strong"
			if j < n && pref[n] > pref[j] && pref[i] > 0 {
				if kind == "strong" || (kind == "comma" && p.allowComma) {
					cuts = append(cuts, cut{a: i, b: j, kind: kind, tail: tl, uBefore: pref[i], period: period})
				}
			}
			i = j
			continue
		}
		i++
	}

	// 句子：两个「强切口」之间；句子以 ？！ 结尾 → 里面的逗号、空格算「在问句里」
	lastEnd := ""
	if trimmed := []rune(tailEndRE.ReplaceAllString(t.s, "")); len(trimmed) > 0 {
		lastEnd = string(trimmed[len(trimmed)-1])
	}
	msgEndsQ := strings.ContainsAny(lastEnd, "？?！!")
	start := 0
	for k := 0; k <= len(cuts); k++ {
		if k == len(cuts) || cuts[k].kind == "strong" {
			endsQ := msgEndsQ
			if k < len(cuts) {
				endsQ = strings.ContainsAny(cuts[k].tail, "？?！!")
			}
			if endsQ {
				for m := start; m < k; m++ {
					cuts[m].inQuestion = true
				}
			}
			start = k + 1
		}
	}
	// 危险切口（只看逗号、空格；断句标点后面切总是可以）
	for k := range cuts {
		c := &cuts[k]
		if c.kind == "strong" {
			continue
		}
		prevB := 0
		if k > 0 {
			prevB = cuts[k-1].b
		}
		leftGap := stripTail(string(t.r[prevB:c.a]))
		rightHead := rightHeadRE.ReplaceAllString(string(t.r[c.b:]), "")
		c.risky = leftBadEnd.MatchString(leftGap) || rightBadStart.MatchString(rightHead) || leftSubAny.MatchString(leftGap)
	}
	return cuts, N
}

var dashRunRE = regexp.MustCompile(`—+`)

type splitPlan struct {
	N       int
	chosen  []int
	pieces  []string
	flag    string // "" / long-nopunct / long / risk
	nCuts   int
	costBst float64
	costRsk float64
}

func pieceCost(n int, p ruleParams) float64 { return math.Pow(float64(n)/p.L0, 2) }

// planMessage 对一条话定「要不要拆、在哪拆」。不掷骰子；返回安全解和标记
func planMessage(msg string, p ruleParams) splitPlan {
	ts := strings.ReplaceAll(textclean.Trim(msg), "\r", "")
	t := newText(ts)
	cuts, N := t.findCuts(p)
	cost0 := pieceCost(N, p)
	cutCost := func(c cut) float64 {
		x := p.costSpace
		switch c.kind {
		case "strong":
			x = p.costStrong
		case "comma":
			x = p.costComma
		}
		if c.kind != "strong" && c.inQuestion {
			x += p.costCommaInQuestion
		}
		return x
	}
	piece := func(from, to int, tailCut *cut) string {
		s := string(t.r[from:to])
		if tailCut != nil {
			s += tailCut.tail
		}
		return textclean.Trim(s)
	}

	type best struct {
		cost float64
		idx  []int
	}
	search := func(allowRisky bool) best {
		var usable []int
		for k := range cuts {
			if allowRisky || !cuts[k].risky {
				usable = append(usable, k)
			}
		}
		bst := best{cost: cost0}
		var rec func(from int, chosen []int)
		rec = func(from int, chosen []int) {
			if len(chosen) > 0 {
				cost, ok, prevB, prevU := 0.0, true, 0, 0
				for q := 0; q <= len(chosen); q++ {
					endU, endA := N, len(t.r)
					var tc *cut
					if q < len(chosen) {
						tc = &cuts[chosen[q]]
						endU, endA = tc.uBefore, tc.a
					}
					l := endU - prevU
					if l < p.minPiece && !isInterj(piece(prevB, endA, tc)) {
						ok = false
						break
					}
					cost += pieceCost(l, p)
					if tc != nil {
						cost += cutCost(*tc)
						prevB, prevU = tc.b, tc.uBefore
					}
				}
				if ok && cost < bst.cost-1e-9 {
					bst = best{cost, append([]int(nil), chosen...)}
				}
			}
			if len(chosen) >= p.maxPieces-1 {
				return
			}
			for q := from; q < len(usable); q++ {
				rec(q+1, append(append([]int(nil), chosen...), usable[q]))
			}
		}
		rec(0, nil)
		return bst
	}
	safe := search(false)
	risky := safe
	for _, c := range cuts {
		if c.risky {
			risky = search(true)
			break
		}
	}

	var pieces []string
	prevB := 0
	var raw []string
	for _, k := range safe.idx {
		raw = append(raw, piece(prevB, cuts[k].a, &cuts[k]))
		prevB = cuts[k].b
	}
	raw = append(raw, textclean.Trim(string(t.r[prevB:])))
	for i, s := range raw {
		if i == len(raw)-1 {
			s = lastTrimRE.ReplaceAllString(s, "")
		} else {
			s = midTrimRE.ReplaceAllString(s, "")
		}
		if s != "" {
			pieces = append(pieces, s)
		}
	}

	flag := ""
	maxPiece := 0
	for _, s := range pieces {
		u := 0
		for _, x := range newText(s).scan().unit {
			u += int(x)
		}
		maxPiece = max(maxPiece, u)
	}
	switch {
	case N >= p.longTol && len(cuts) == 0:
		flag = "long-nopunct"
	case maxPiece >= p.longTol:
		flag = "long"
	case risky.cost+p.riskMargin < safe.cost:
		flag = "risk"
	}
	return splitPlan{N: N, chosen: safe.idx, pieces: pieces, flag: flag, nCuts: len(cuts), costBst: safe.cost, costRsk: risky.cost}
}

var (
	lastTrimRE = regexp.MustCompile(`[` + jsWS + `，,。、；;]+$`)
	midTrimRE  = regexp.MustCompile(`[` + jsWS + `。]+$`)
	newlinesRE = regexp.MustCompile(`\n+`)
)

// 一轮的结果。status：multi 本来就 ≥2 条、没有哪条被拆 / flag 交给模型 / nocut 规则认为不用拆 / skip 该拆但没掷中 / split 拆了
const (
	roundMulti = "multi"
	roundFlag  = "flag"
	roundNoCut = "nocut"
	roundSkip  = "skip"
	roundSplit = "split"
)

type roundResult struct {
	Pieces []string
	Status string
	Flag   string
}

// splitRound 是一轮（模型这一轮写的几条，已经过空行拆条）要不要再拆。每一条话分别过规则（模型自己分成两条、其中一条仍然很长的，也会再拆，ADR-129）；
// 整轮最多 maxPieces 条，已经分够的不再拆。随机用 seed ^ fnv(这条话)：同一条话、同一个种子，结果不依赖跑的顺序
func splitRound(msgs []string, p ruleParams) roundResult {
	var lines []string
	for _, m := range msgs {
		for _, x := range newlinesRE.Split(m, -1) {
			if x = textclean.Trim(x); x != "" {
				lines = append(lines, x)
			}
		}
	}
	if len(lines) == 0 {
		return roundResult{Status: roundNoCut}
	}
	single := len(lines) == 1
	var out []string
	status, flag := roundMulti, ""
	if single {
		status = roundNoCut
	}
	for i, m := range lines {
		// 这一条最多拆成几条：整轮上限减去已经定下的和后面还没处理的各算一条
		allowed := p.maxPieces - (len(out) + (len(lines) - i)) + 1
		if allowed < 2 {
			out = append(out, m)
			continue
		}
		// 整段只有一条、不到 30 字的一句话不拆（ADR-163）
		if single && charsOf(m) < 30 {
			out = append(out, m)
			continue
		}
		pp := p
		pp.maxPieces = allowed
		plan := planMessage(m, pp)
		// 只有一条话时，拆完还有一条太长的整条不拆（交给模型）；模型自己已经分成几条的，这一条照安全解拆（ADR-129）
		if plan.flag != "" && p.flagWhole && single {
			out = append(out, m)
			status, flag = roundFlag, plan.flag
			continue
		}
		if len(plan.chosen) == 0 {
			out = append(out, m)
			if single {
				status, flag = roundNoCut, plan.flag
				if plan.flag != "" {
					status = roundFlag
				}
			}
			continue
		}
		rng := mulberry32((p.seed ^ fnv(m)))
		// 掷骰子是为了把连发比例调到目标；很长的（≥ 30 字，或者模型已经分成几条、这一条 ≥ 20 字）规则认为该拆就拆，不掷骰子
		veryLong := charsOf(m) >= 30 || (!single && charsOf(m) >= 20)
		if !veryLong && rng() >= p.prob {
			out = append(out, m)
			if single {
				status, flag = roundSkip, plan.flag
			}
			continue
		}
		out = append(out, plan.pieces...)
		status = roundSplit
		if single {
			flag = plan.flag
		}
	}
	return roundResult{Pieces: out, Status: status, Flag: flag}
}
