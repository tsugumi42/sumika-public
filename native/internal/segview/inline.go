// Package segview contains presentation-only message layout shared by desktop views.
// It does not own backend credentials, UI state or rendering resources.
package segview

import (
	"strings"
	"unicode"

	"sumika-native/internal/linkreader"
)

// Run is an inline piece; mentions are bold, faces retain their fallback text.
type Run struct {
	Text string
	Bold bool
	Face *FaceRef
	Link *LinkRef
}

// FaceRef identifies a platform face and its original kind (face or superface).
type FaceRef struct {
	ID   string
	Kind string
}

// LinkRef retains the displayed, opened and cleaned forms of a link.
type LinkRef struct{ Raw, Open, Clean string }

// CleanText removes literal object replacement characters reserved for inline faces.
func CleanText(s string) string { return strings.ReplaceAll(s, "￼", "") }

// FaceFallback supplies text before the face name table is available.
func FaceFallback(id, kind string) string {
	switch {
	case id == "":
		return "[表情]"
	case kind == "superface":
		return "[超级表情" + id + "]"
	}
	return "[表情" + id + "]"
}

// LinkRuns splits links using the remaining budget shared across one message.
func LinkRuns(text string, budget *int) []Run {
	if *budget == 0 || !(strings.Contains(text, "://") || strings.Contains(text, "：／／")) {
		return []Run{{Text: text}}
	}
	found := linkreader.FindURLs(text)
	if len(found) == 0 {
		return []Run{{Text: text}}
	}
	var out []Run
	end := 0
	for _, f := range found[:min(len(found), *budget)] {
		if f.Start > end {
			out = append(out, Run{Text: text[end:f.Start]})
		}
		out = append(out, Run{Text: f.Raw, Link: &LinkRef{Raw: f.Raw, Open: f.Open, Clean: f.Clean}})
		end = f.End
		*budget--
	}
	if end < len(text) {
		out = append(out, Run{Text: text[end:]})
	}
	return out
}

// TrimRuns trims edge whitespace in place without splitting a face or copying runs.
// An entirely blank sequence returns nil.
func TrimRuns(rs []Run) []Run {
	for len(rs) > 0 {
		if rs[0].Face != nil {
			break
		}
		t := strings.TrimLeftFunc(rs[0].Text, unicode.IsSpace)
		if t != "" {
			rs[0].Text = t
			break
		}
		rs = rs[1:]
	}
	for len(rs) > 0 {
		last := rs[len(rs)-1]
		if last.Face != nil {
			break
		}
		t := strings.TrimRightFunc(last.Text, unicode.IsSpace)
		if t != "" {
			rs[len(rs)-1].Text = t
			break
		}
		rs = rs[:len(rs)-1]
	}
	if len(rs) == 0 {
		return nil
	}
	return rs
}
