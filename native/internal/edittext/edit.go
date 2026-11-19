// Package edittext 计算文件编辑，不读写文件；审批预览与执行共用同一份结果和错误。
package edittext

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
)

// Pair 是一次字面替换；所有 OldText 都对着同一份原文定位。
type Pair struct{ OldText, NewText string }

// Parse 接收 tools.Prepare 整理后的参数。
func Parse(args jsjson.Value) ([]Pair, error) {
	list := args.Obj["edits"]
	if list.Kind != jsjson.Array || len(list.Arr) == 0 {
		return nil, errors.New("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	edits := make([]Pair, len(list.Arr))
	for i, e := range list.Arr {
		edits[i] = Pair{e.Obj["oldText"].Str, e.Obj["newText"].Str}
	}
	return edits, nil
}

// Change 同时保留用于编辑详情的 LF 正文和最终写入的原文件换行/BOM。
type Change struct {
	Before, After string
	Content       string
}

// Apply 返回完整编辑结果；任意一项失败时不返回部分修改。
// NewText 中的 $ 一律按字面写入，保证预览展示的替换与实际执行相同。
func Apply(content string, edits []Pair, path string) (Change, error) {
	bom := ""
	if strings.HasPrefix(content, "\uFEFF") {
		bom, content = "\uFEFF", content[len("\uFEFF"):]
	}
	ending := detectLineEnding(content)
	before, after, err := applyEdits(normalizeToLF(content), edits, path)
	if err != nil {
		return Change{}, err
	}
	return Change{Before: before, After: after, Content: bom + restoreLineEndings(after, ending)}, nil
}

func detectLineEnding(s string) string {
	crlf, lf := strings.Index(s, "\r\n"), strings.Index(s, "\n")
	if lf == -1 || crlf == -1 {
		return "\n"
	}
	if crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func normalizeToLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func restoreLineEndings(s, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

var (
	fuzzySingleQuotes = regexp.MustCompile("[\u2018\u2019\u201A\u201B]")
	fuzzyDoubleQuotes = regexp.MustCompile("[\u201C\u201D\u201E\u201F]")
	fuzzyDashes       = regexp.MustCompile("[\u2010\u2011\u2012\u2013\u2014\u2015\u2212]")
	fuzzySpaces       = regexp.MustCompile("[\u00A0\u2002-\u200A\u202F\u205F\u3000]")
)

// normalizeForFuzzyMatch 容忍排版差异：按 x/text 的 NFKC 表归一、去行尾空白，
// 再统一引号、横线和特殊空格；只把命中的行写回，避免改动无关正文。
func normalizeForFuzzyMatch(s string) string {
	lines := strings.Split(norm.NFKC.String(s), "\n")
	for i, l := range lines {
		lines[i] = jsstr.TrimEnd(l)
	}
	s = strings.Join(lines, "\n")
	s = fuzzySingleQuotes.ReplaceAllString(s, "'")
	s = fuzzyDoubleQuotes.ReplaceAllString(s, `"`)
	s = fuzzyDashes.ReplaceAllString(s, "-")
	return fuzzySpaces.ReplaceAllString(s, " ")
}

type found struct {
	ok       bool
	index, n int
	fuzzy    bool
}

func fuzzyFindText(content, oldText string) found {
	if i := strings.Index(content, oldText); i != -1 {
		return found{ok: true, index: i, n: len(oldText)}
	}
	fc, fo := normalizeForFuzzyMatch(content), normalizeForFuzzyMatch(oldText)
	if i := strings.Index(fc, fo); i != -1 {
		return found{ok: true, index: i, n: len(fo), fuzzy: true}
	}
	return found{}
}

type matched struct {
	editIndex, index, n int
	newText             string
}

// applyEdits 每项都对着同一份原文找，防止前项替换改变后项定位。
// 有一项需要模糊匹配时统一定位基准，写回仍保留未命中行。
func applyEdits(content string, edits []Pair, path string) (string, string, error) {
	total := len(edits)
	norms := make([]Pair, total)
	for i, e := range edits {
		norms[i] = Pair{normalizeToLF(e.OldText), normalizeToLF(e.NewText)}
	}
	for i, e := range norms {
		if e.OldText == "" {
			if total == 1 {
				return "", "", fmt.Errorf("oldText must not be empty in %s.", path)
			}
			return "", "", fmt.Errorf("edits[%d].oldText must not be empty in %s.", i, path)
		}
	}
	useFuzzy := false
	for _, e := range norms {
		if fuzzyFindText(content, e.OldText).fuzzy {
			useFuzzy = true
		}
	}
	replBase := content
	if useFuzzy {
		replBase = normalizeForFuzzyMatch(content)
	}
	var ms []matched
	for i, e := range norms {
		f := fuzzyFindText(replBase, e.OldText)
		if !f.ok {
			if total == 1 {
				return "", "", &MissError{Msg: fmt.Sprintf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path), Index: i}
			}
			return "", "", &MissError{Msg: fmt.Sprintf("Could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.", i, path), Index: i}
		}
		if n := strings.Count(normalizeForFuzzyMatch(replBase), normalizeForFuzzyMatch(e.OldText)); n > 1 {
			if total == 1 {
				return "", "", &MissError{Msg: fmt.Sprintf("Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", n, path), Index: i, Count: n}
			}
			return "", "", &MissError{Msg: fmt.Sprintf("Found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.", n, i, path), Index: i, Count: n}
		}
		ms = append(ms, matched{i, f.index, f.n, e.NewText})
	}
	sort.SliceStable(ms, func(a, b int) bool { return ms[a].index < ms[b].index })
	for i := 1; i < len(ms); i++ {
		if prev, cur := ms[i-1], ms[i]; prev.index+prev.n > cur.index {
			return "", "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.", prev.editIndex, cur.editIndex, path)
		}
	}
	var out string
	if useFuzzy {
		var err error
		if out, err = applyPreservingUnchangedLines(content, replBase, ms); err != nil {
			return "", "", err
		}
	} else {
		out = applyReplacements(replBase, ms, 0)
	}
	if out == content {
		if total == 1 {
			return "", "", fmt.Errorf("No changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.", path)
		}
		return "", "", fmt.Errorf("No changes made to %s. The replacements produced identical content.", path)
	}
	return content, out, nil
}

// applyReplacements 从后往前换，前面的偏移不受影响（ms 已经按位置排好）
func applyReplacements(s string, ms []matched, offset int) string {
	for i := len(ms) - 1; i >= 0; i-- {
		at := ms[i].index - offset
		s = s[:at] + ms[i].newText + s[at+ms[i].n:]
	}
	return s
}

// splitLinesWithEndings 保留行尾换行，确保模糊匹配只改命中的行。
func splitLinesWithEndings(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

type span struct{ start, end int }

func lineSpans(s string) []span {
	var out []span
	off := 0
	for _, l := range splitLinesWithEndings(s) {
		out = append(out, span{off, off + len(l)})
		off += len(l)
	}
	return out
}

func replacementLines(lines []span, m matched) (int, int, error) {
	start, end := m.index, m.index+m.n
	first := -1
	for i, l := range lines {
		if start >= l.start && start < l.end {
			first = i
			break
		}
	}
	if first == -1 {
		return 0, 0, errors.New("Replacement range is outside the base content.")
	}
	last := first
	for last < len(lines) && lines[last].end < end {
		last++
	}
	if last >= len(lines) {
		return 0, 0, errors.New("Replacement range is outside the base content.")
	}
	return first, last + 1, nil
}

// applyPreservingUnchangedLines 只写回命中行的归一结果，别的行保留原字节。
func applyPreservingUnchangedLines(original, base string, ms []matched) (string, error) {
	origLines, baseLines := splitLinesWithEndings(original), lineSpans(base)
	if len(origLines) != len(baseLines) {
		return "", errors.New("Cannot preserve unchanged lines because the base content has a different line count.")
	}
	type group struct {
		start, end int
		ms         []matched
	}
	var groups []*group
	for _, m := range ms {
		s, e, err := replacementLines(baseLines, m)
		if err != nil {
			return "", err
		}
		if n := len(groups); n > 0 && s < groups[n-1].end {
			groups[n-1].end = max(groups[n-1].end, e)
			groups[n-1].ms = append(groups[n-1].ms, m)
			continue
		}
		groups = append(groups, &group{s, e, []matched{m}})
	}
	var b strings.Builder
	at := 0
	for _, g := range groups {
		b.WriteString(strings.Join(origLines[at:g.start], ""))
		from, to := baseLines[g.start].start, baseLines[g.end-1].end
		b.WriteString(applyReplacements(base[from:to], g.ms, from))
		at = g.end
	}
	b.WriteString(strings.Join(origLines[at:], ""))
	return b.String(), nil
}
