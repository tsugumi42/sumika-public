// Package jsdiff 照 npm 的 diff 包（jsdiff 8.0.4）的 diffLines，和 pi 的 generateDiffString（core/tools/edit-diff.js）：
// 审批卡片上改文件的预览、edit 给界面的 diff 都是它算的。Myers 差分算法本身有很多种等价的写法，同样长的改法里挑哪一种
// 取决于实现细节（先试插入还是删除、碰到边以后不再往外扩），所以这里照 jsdiff 的 base.js 一步步搬，不用 Go 现成的差分库
package jsdiff

import (
	"strconv"
	"strings"
)

// Part 是差分的一段：没变的、加的、删的（jsdiff 的 change object）
type Part struct {
	Value   string
	Count   int
	Added   bool
	Removed bool
}

type component struct {
	count          int
	added, removed bool
	prev           *component
}

type path struct {
	oldPos int
	last   *component
}

// tokenize 照 line.js 的 tokenize（没有选项时）：按 \n 或 \r\n 切，换行跟着前面那一行；字符串以换行结尾时最后不多出一个空行
func tokenize(s string) []string {
	var parts []string // 照 value.split(/(\n|\r\n)/)：内容和分隔符交替
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\n':
			parts = append(parts, s[start:i], "\n")
			start = i + 1
		case s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n':
			parts = append(parts, s[start:i], "\r\n")
			start = i + 2
			i++
		}
	}
	parts = append(parts, s[start:])
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var lines []string
	for i, p := range parts {
		if i%2 == 1 {
			lines[len(lines)-1] += p
		} else {
			lines = append(lines, p)
		}
	}
	// removeEmpty
	out := lines[:0]
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func addToPath(p *path, added, removed bool, oldPosInc int) *path {
	if last := p.last; last != nil && last.added == added && last.removed == removed {
		return &path{oldPos: p.oldPos + oldPosInc, last: &component{count: last.count + 1, added: added, removed: removed, prev: last.prev}}
	}
	return &path{oldPos: p.oldPos + oldPosInc, last: &component{count: 1, added: added, removed: removed, prev: p.last}}
}

func extractCommon(p *path, newT, oldT []string, diagonal int) int {
	oldPos := p.oldPos
	newPos := oldPos - diagonal
	common := 0
	for newPos+1 < len(newT) && oldPos+1 < len(oldT) && oldT[oldPos+1] == newT[newPos+1] {
		newPos++
		oldPos++
		common++
	}
	if common > 0 {
		p.last = &component{count: common, prev: p.last}
	}
	p.oldPos = oldPos
	return newPos
}

func buildValues(last *component, newT, oldT []string) []Part {
	var comps []*component
	for c := last; c != nil; c = c.prev {
		comps = append(comps, c)
	}
	parts := make([]Part, 0, len(comps))
	newPos, oldPos := 0, 0
	for i := len(comps) - 1; i >= 0; i-- {
		c := comps[i]
		p := Part{Count: c.count, Added: c.added, Removed: c.removed}
		if !c.removed {
			p.Value = strings.Join(newT[newPos:newPos+c.count], "")
			newPos += c.count
			if !c.added {
				oldPos += c.count
			}
		} else {
			p.Value = strings.Join(oldT[oldPos:oldPos+c.count], "")
			oldPos += c.count
		}
		parts = append(parts, p)
	}
	return parts
}

// maxEditLength 是最多算到多少行改动（增删各算一行），照 jsdiff 的同名选项。Myers 的耗时是改动行数的平方：
// 整体替换一个 6000 行的文件要算 12000 步，空闲时 7 秒、低优先级跑在忙机器上超过一分钟，工具已经超时了它还占着一个核算到底。
// 4096 步（约 2000 行整段替换）空闲时不到 1 秒；差分给人看，改到这么多行时本来也只显示开头（详情上限 64 KB）。
const maxEditLength = 4096

// DiffLines 照 jsdiff 的 diffLines(old, new)；改动超过 maxEditLength 时不再找最短改法（jsdiff 这时什么都不返回，
// 这里还要给人看），公共的头尾之外整段算作先删后加。
func DiffLines(oldStr, newStr string) []Part {
	oldT, newT := tokenize(oldStr), tokenize(newStr)
	if parts, ok := myers(oldT, newT, maxEditLength); ok {
		return parts
	}
	return wholesale(oldT, newT)
}

// wholesale 是改动太多时的差分：公共的头尾留着，中间整段先删后加。
func wholesale(oldT, newT []string) []Part {
	head := 0
	for head < len(oldT) && head < len(newT) && oldT[head] == newT[head] {
		head++
	}
	tail := 0
	for tail < len(oldT)-head && tail < len(newT)-head && oldT[len(oldT)-1-tail] == newT[len(newT)-1-tail] {
		tail++
	}
	var parts []Part
	add := func(lines []string, added, removed bool) {
		if len(lines) > 0 {
			parts = append(parts, Part{Value: strings.Join(lines, ""), Count: len(lines), Added: added, Removed: removed})
		}
	}
	add(oldT[:head], false, false)
	add(oldT[head:len(oldT)-tail], false, true)
	add(newT[head:len(newT)-tail], true, false)
	add(oldT[len(oldT)-tail:], false, false)
	return parts
}

// myers 是 jsdiff base.js 的主循环；改动超过 limit 行时返回 false。
func myers(oldT, newT []string, limit int) ([]Part, bool) {
	oldLen, newLen := len(oldT), len(newT)
	maxEdit := min(newLen+oldLen, limit)
	best := map[int]*path{0: {oldPos: -1}}
	newPos := extractCommon(best[0], newT, oldT, 0)
	if best[0].oldPos+1 >= oldLen && newPos+1 >= newLen {
		return buildValues(best[0].last, newT, oldT), true
	}
	const inf = 1 << 62
	minDiag, maxDiag := -inf, inf
	for editLength := 1; editLength <= maxEdit; editLength++ {
		for d := max(minDiag, -editLength); d <= min(maxDiag, editLength); d += 2 {
			removePath, addPath := best[d-1], best[d+1]
			if removePath != nil {
				best[d-1] = nil
			}
			canAdd := false
			if addPath != nil {
				addNewPos := addPath.oldPos - d
				canAdd = 0 <= addNewPos && addNewPos < newLen
			}
			canRemove := removePath != nil && removePath.oldPos+1 < oldLen
			if !canAdd && !canRemove {
				best[d] = nil
				continue
			}
			var base *path
			if !canRemove || (canAdd && removePath.oldPos < addPath.oldPos) {
				base = addToPath(addPath, true, false, 0)
			} else {
				base = addToPath(removePath, false, true, 1)
			}
			newPos = extractCommon(base, newT, oldT, d)
			if base.oldPos+1 >= oldLen && newPos+1 >= newLen {
				return buildValues(base.last, newT, oldT), true
			}
			best[d] = base
			if base.oldPos+1 >= oldLen {
				maxDiag = min(maxDiag, d-1)
			}
			if newPos+1 >= newLen {
				minDiag = max(minDiag, d+1)
			}
		}
	}
	return nil, false
}

func pad(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) < width {
		s = strings.Repeat(" ", width-len(s)) + s
	}
	return s
}

// GenerateDiffString 照 pi 的 generateDiffString：带行号的差分，改动前后各留 context 行，再远的写 "..."；
// 第二个返回值是新文件里第一处改动的行号（没有改动时 has 为假）
func GenerateDiffString(oldContent, newContent string, context int) (diff string, firstChanged int, has bool) {
	parts := DiffLines(oldContent, newContent)
	var out []string
	width := len(strconv.Itoa(max(strings.Count(oldContent, "\n")+1, strings.Count(newContent, "\n")+1)))
	oldNum, newNum := 1, 1
	lastWasChange := false
	blank := strings.Repeat(" ", width)
	ctx := func(line string) {
		out = append(out, " "+pad(oldNum, width)+" "+line)
		oldNum++
		newNum++
	}
	for i, p := range parts {
		raw := strings.Split(p.Value, "\n")
		if raw[len(raw)-1] == "" {
			raw = raw[:len(raw)-1]
		}
		if p.Added || p.Removed {
			if !has {
				firstChanged, has = newNum, true
			}
			for _, line := range raw {
				if p.Added {
					out = append(out, "+"+pad(newNum, width)+" "+line)
					newNum++
				} else {
					out = append(out, "-"+pad(oldNum, width)+" "+line)
					oldNum++
				}
			}
			lastWasChange = true
			continue
		}
		nextIsChange := i < len(parts)-1 && (parts[i+1].Added || parts[i+1].Removed)
		switch {
		case lastWasChange && nextIsChange:
			if len(raw) <= context*2 {
				for _, l := range raw {
					ctx(l)
				}
			} else {
				lead, trail := raw[:context], raw[len(raw)-context:]
				skipped := len(raw) - len(lead) - len(trail)
				for _, l := range lead {
					ctx(l)
				}
				out = append(out, " "+blank+" ...")
				oldNum += skipped
				newNum += skipped
				for _, l := range trail {
					ctx(l)
				}
			}
		case lastWasChange:
			shown := raw[:min(context, len(raw))]
			skipped := len(raw) - len(shown)
			for _, l := range shown {
				ctx(l)
			}
			if skipped > 0 {
				out = append(out, " "+blank+" ...")
				oldNum += skipped
				newNum += skipped
			}
		case nextIsChange:
			skipped := max(0, len(raw)-context)
			if skipped > 0 {
				out = append(out, " "+blank+" ...")
				oldNum += skipped
				newNum += skipped
			}
			for _, l := range raw[skipped:] {
				ctx(l)
			}
		default:
			oldNum += len(raw)
			newNum += len(raw)
		}
		lastWasChange = false
	}
	return strings.Join(out, "\n"), firstChanged, has
}
