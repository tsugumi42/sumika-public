package sentencepiece

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const metaSpace = "▁" // U+2581 LOWER ONE EIGHTH BLOCK

// normKind is one step of a HuggingFace "Sequence" normalizer that runs after
// the Precompiled charsmap.
type normKind uint8

const (
	// normStrip removes leading and / or trailing white space (Strip).
	normStrip normKind = iota + 1
	// normCollapseSpaces replaces every run of two or more ASCII spaces with a
	// single ▁ (Replace with the regex " {2,}" and content "▁").
	normCollapseSpaces
)

type normStep struct {
	kind        normKind
	left, right bool // normStrip only
}

// Normalizer applies a tokenizer.json's normalizer to the input text:
// first the Precompiled charsmap (the Darts double-array NFKC and custom
// character rules), then the Strip / Replace steps of a Sequence in order.
// It is read-only after creation.
//
// Fork change: upstream also had the .model-file options (dummy prefix,
// extra-whitespace removal, whitespace escaping) which a tokenizer.json never
// sets, and executed only a lone Precompiled normalizer; a Sequence was
// silently ignored (bge-reranker-v2-m3 lost its charsmap that way).
type Normalizer struct {
	trie     *dartsArray
	replaced []byte // null-terminated replacement strings, concatenated
	steps    []normStep
}

// newNormalizer builds a Normalizer from the decoded charsmap (nil means no
// charsmap) and the steps that follow it.
func newNormalizer(charsmap []byte, steps []normStep) (*Normalizer, error) {
	n := &Normalizer{steps: steps}
	if len(charsmap) == 0 {
		return n, nil
	}
	// Layout: little-endian uint32 size of the trie in bytes, the trie, then
	// the replacement strings.
	if len(charsmap) < 4 {
		return nil, fmt.Errorf("%w: precompiled_charsmap is shorter than 4 bytes", ErrInvalidModel)
	}
	trieSize := int(uint32(charsmap[0]) | uint32(charsmap[1])<<8 | uint32(charsmap[2])<<16 | uint32(charsmap[3])<<24)
	if trieSize < 0 || 4+trieSize > len(charsmap) {
		return nil, fmt.Errorf("%w: precompiled_charsmap trie size %d does not fit in %d bytes", ErrInvalidModel, trieSize, len(charsmap))
	}
	n.trie = newDartsArray(charsmap[4 : 4+trieSize])
	n.replaced = charsmap[4+trieSize:]
	return n, nil
}

// Normalize applies the charsmap and then the steps.
func (n *Normalizer) Normalize(input string) string {
	if len(input) == 0 {
		return ""
	}
	s := n.applyCharsmap(input)
	for _, st := range n.steps {
		switch st.kind {
		case normStrip:
			s = stripSpace(s, st.left, st.right)
		case normCollapseSpaces:
			s = collapseSpaces(s)
		}
	}
	return s
}

// applyCharsmap walks the input; at each position it takes the longest charsmap
// match and writes its replacement, or copies one character through unchanged
// (an invalid UTF-8 byte becomes U+FFFD). The charsmap already contains all
// NFKC mappings, so unmatched characters are not normalized again.
func (n *Normalizer) applyCharsmap(input string) string {
	var sb strings.Builder
	sb.Grow(len(input) + 3)
	for i := 0; i < len(input); {
		if n.trie != nil {
			if offset, length := n.trie.longestMatch(input[i:]); length > 0 && offset < len(n.replaced) {
				end := offset
				for end < len(n.replaced) && n.replaced[end] != 0 {
					end++
				}
				sb.Write(n.replaced[offset:end])
				i += length
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size <= 1 {
			sb.WriteString("�")
			i++
			continue
		}
		sb.WriteString(input[i : i+size])
		i += size
	}
	return sb.String()
}

// stripSpace removes white space (the Unicode White_Space property, what Rust's
// char::is_whitespace, HuggingFace's Strip, tests) from the chosen ends.
func stripSpace(s string, left, right bool) string {
	if left {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
	}
	if right {
		s = strings.TrimRightFunc(s, unicode.IsSpace)
	}
	return s
}

// collapseSpaces replaces every run of two or more ASCII spaces with one ▁.
// Single spaces stay: the Metaspace pre-tokenizer turns them into ▁ itself.
func collapseSpaces(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != ' ' {
			sb.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == ' ' {
			j++
		}
		if j-i >= 2 {
			sb.WriteString(metaSpace)
		} else {
			sb.WriteByte(' ')
		}
		i = j
	}
	return sb.String()
}
