package sentencepiece

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// tokenizerJSON is the part of a HuggingFace tokenizer.json this fork reads.
type tokenizerJSON struct {
	AddedTokens  []addedTokenJSON `json:"added_tokens"`
	Normalizer   json.RawMessage  `json:"normalizer"`
	PreTokenizer json.RawMessage  `json:"pre_tokenizer"`
	Model        modelJSON        `json:"model"`
}

type addedTokenJSON struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
	Special bool   `json:"special"`
}

type modelJSON struct {
	Type         string          `json:"type"`
	Vocab        json.RawMessage `json:"vocab"`
	UnkToken     string          `json:"unk_token"`
	UnkID        *int            `json:"unk_id"`
	ByteFallback bool            `json:"byte_fallback"`
}

func loadFromJSON(data []byte) (*Tokenizer, error) {
	var tj tokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidModel, err)
	}

	model, err := buildModelFromJSON(&tj)
	if err != nil {
		return nil, err
	}

	normalizer, err := parseNormalizer(tj.Normalizer)
	if err != nil {
		return nil, err
	}
	encoder := newEncoder(model, normalizer)

	// Configure pre-tokenization based on JSON pre_tokenizer type.
	kind, err := detectPreTokenizer(tj.PreTokenizer)
	if err != nil {
		return nil, err
	}
	switch kind {
	case preTokenizerMetaspace:
		encoder.preTokenize = metaspacePreTokenize
	case preTokenizerWhitespaceSplitMetaspace:
		encoder.preTokenize = whitespaceSplitMetaspacePreTokenize
	}

	return &Tokenizer{encoder: encoder}, nil
}

func buildModelFromJSON(tj *tokenizerJSON) (*Model, error) {
	if tj.Model.Type != "Unigram" {
		return nil, fmt.Errorf("%w: model type %q, only Unigram", ErrUnsupportedModel, tj.Model.Type)
	}
	if tj.Model.ByteFallback {
		return nil, fmt.Errorf("%w: byte_fallback", ErrUnsupportedModel)
	}
	// Added tokens that are not special would have to be matched in the raw
	// text before normalization; this fork does not do that, so refuse rather
	// than silently produce different IDs.
	for _, at := range tj.AddedTokens {
		if !at.Special {
			return nil, fmt.Errorf("%w: added token %q is not special", ErrUnsupportedModel, at.Content)
		}
	}

	pieces, err := parseUnigramVocab(tj.Model.Vocab)
	if err != nil {
		return nil, err
	}

	classifyJSONPieces(pieces, tj.AddedTokens)

	m := &Model{
		pieces: pieces,
		unkID:  resolveUnkID(tj, pieces),
	}

	var tb trieBuilder
	for i, p := range m.pieces {
		if p.Type == PieceNormal || p.Type == PieceUserDefined {
			tb.Add(p.Piece, i)
		}
	}
	m.vocabTrie = tb.Build()
	m.computeMinMaxScores()

	return m, nil
}

func parseUnigramVocab(raw json.RawMessage) ([]Piece, error) {
	var vocab [][2]json.RawMessage
	if err := json.Unmarshal(raw, &vocab); err != nil {
		return nil, fmt.Errorf("parse Unigram vocab: %w", err)
	}

	pieces := make([]Piece, len(vocab))
	for i, entry := range vocab {
		var token string
		var score float64
		if err := json.Unmarshal(entry[0], &token); err != nil {
			return nil, fmt.Errorf("parse vocab token: %w", err)
		}
		if err := json.Unmarshal(entry[1], &score); err != nil {
			return nil, fmt.Errorf("parse vocab score: %w", err)
		}
		pieces[i] = Piece{Piece: token, Score: score, Type: PieceNormal}
	}

	return pieces, nil
}

func classifyJSONPieces(pieces []Piece, addedTokens []addedTokenJSON) {
	addedByID := make(map[int]*addedTokenJSON, len(addedTokens))
	for i := range addedTokens {
		addedByID[addedTokens[i].ID] = &addedTokens[i]
	}

	for i := range pieces {
		at, isAdded := addedByID[i]
		switch {
		case isAdded && at.Content == "<unk>":
			pieces[i].Type = PieceUnknown
		case isAdded && at.Special:
			pieces[i].Type = PieceControl
		case isAdded && strings.HasPrefix(at.Content, "<unused"):
			pieces[i].Type = PieceUnused
		case pieceIsByte(pieces[i].Piece):
			pieces[i].Type = PieceByte
		}
	}
}

func resolveUnkID(tj *tokenizerJSON, pieces []Piece) int {
	if tj.Model.UnkID != nil {
		return *tj.Model.UnkID
	}
	if tj.Model.UnkToken != "" {
		for i, p := range pieces {
			if p.Piece == tj.Model.UnkToken {
				return i
			}
		}
	}
	return 0
}

// preTokenizerKind describes the type of pre-tokenizer detected.
type preTokenizerKind int

const (
	preTokenizerMetaspace                preTokenizerKind = iota + 1 // standalone Metaspace
	preTokenizerWhitespaceSplitMetaspace                             // Sequence(WhitespaceSplit, Metaspace)
)

type metaspaceJSON struct {
	Type          string `json:"type"`
	Replacement   string `json:"replacement"`
	PrependScheme string `json:"prepend_scheme"`
	Split         *bool  `json:"split"`
}

// ok reports whether this is the Metaspace the pre-tokenizer functions below
// implement: replacement ▁, always prepend, split on ▁.
func (m metaspaceJSON) ok() bool {
	return m.Type == "Metaspace" && m.Replacement == metaSpace && m.PrependScheme == "always" && m.Split != nil && *m.Split
}

// detectPreTokenizer inspects the pre_tokenizer JSON and returns its kind. Any
// shape other than the two this fork implements is an error: a tokenizer.json
// with another pre-tokenizer would otherwise load and silently tokenize wrong.
func detectPreTokenizer(raw json.RawMessage) (preTokenizerKind, error) {
	var pt struct {
		metaspaceJSON
		PreTokenizers []json.RawMessage `json:"pretokenizers"`
	}
	if err := json.Unmarshal(raw, &pt); err != nil {
		return 0, fmt.Errorf("%w: pre_tokenizer: %w", ErrUnsupportedModel, err)
	}

	if pt.metaspaceJSON.ok() {
		return preTokenizerMetaspace, nil
	}

	if pt.Type == "Sequence" && len(pt.PreTokenizers) == 2 {
		var first struct {
			Type string `json:"type"`
		}
		var second metaspaceJSON
		if json.Unmarshal(pt.PreTokenizers[0], &first) == nil && first.Type == "WhitespaceSplit" &&
			json.Unmarshal(pt.PreTokenizers[1], &second) == nil && second.ok() {
			return preTokenizerWhitespaceSplitMetaspace, nil
		}
	}

	return 0, fmt.Errorf("%w: pre_tokenizer is neither Metaspace nor Sequence(WhitespaceSplit, Metaspace)", ErrUnsupportedModel)
}

// metaspacePreTokenize implements the HuggingFace Metaspace pre-tokenizer:
// 1. Replace spaces with ▁
// 2. Prepend ▁ if text doesn't already start with ▁
// 3. Split on ▁ boundaries, keeping ▁ with the following segment
//
// The input text is already normalized (charsmap applied, spaces untouched).
func metaspacePreTokenize(text string) []string {
	if len(text) == 0 {
		return nil
	}

	// Replace spaces with ▁.
	text = strings.ReplaceAll(text, " ", metaSpace)

	// Prepend ▁ if text doesn't start with ▁.
	if !strings.HasPrefix(text, metaSpace) {
		text = metaSpace + text
	}

	// Split on ▁, keeping ▁ attached to the right segment.
	return splitKeepDelimiter(text, '▁')
}

// whitespaceSplitMetaspacePreTokenize implements the HuggingFace
// Sequence(WhitespaceSplit, Metaspace) pre-tokenizer:
// 1. Split on whitespace (dropping whitespace tokens)
// 2. Prepend ▁ to each word
func whitespaceSplitMetaspacePreTokenize(text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	segments := make([]string, len(words))
	for i, w := range words {
		segments[i] = metaSpace + w
	}
	return segments
}

// splitKeepDelimiter splits text on a delimiter rune, keeping the delimiter
// attached to the following segment. Empty segments are dropped.
func splitKeepDelimiter(text string, delim rune) []string {
	var segments []string
	start := 0

	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == delim && i > start {
			segments = append(segments, text[start:i])
			start = i
		}
		i += size
	}

	if start < len(text) {
		segments = append(segments, text[start:])
	}

	return segments
}

type normalizerJSON struct {
	Type                string `json:"type"`
	PrecompiledCharsmap string `json:"precompiled_charsmap"`
	StripLeft           *bool  `json:"strip_left"`
	StripRight          *bool  `json:"strip_right"`
	Pattern             struct {
		Regex string `json:"Regex"`
		Str   string `json:"String"`
	} `json:"pattern"`
	Content     string            `json:"content"`
	Normalizers []json.RawMessage `json:"normalizers"`
}

// parseNormalizer reads the tokenizer.json "normalizer": none, a lone
// Precompiled, or a Sequence that starts with Precompiled and goes on with
// Strip and the Replace(" {2,}" -> "▁") step. Anything else is an error, since
// ignoring it would silently change the token IDs.
func parseNormalizer(raw json.RawMessage) (*Normalizer, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return newNormalizer(nil, nil)
	}
	var top normalizerJSON
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("%w: normalizer: %w", ErrInvalidModel, err)
	}
	elems := []json.RawMessage{raw}
	if top.Type == "Sequence" {
		elems = top.Normalizers
	}
	var charsmap []byte
	var steps []normStep
	for i, e := range elems {
		var n normalizerJSON
		if err := json.Unmarshal(e, &n); err != nil {
			return nil, fmt.Errorf("%w: normalizer: %w", ErrInvalidModel, err)
		}
		switch n.Type {
		case "Precompiled":
			if i != 0 {
				return nil, fmt.Errorf("%w: Precompiled normalizer must be the first of the sequence", ErrUnsupportedModel)
			}
			data, err := base64.StdEncoding.DecodeString(n.PrecompiledCharsmap)
			if err != nil || len(data) == 0 {
				return nil, fmt.Errorf("%w: precompiled_charsmap is empty or not base64", ErrInvalidModel)
			}
			charsmap = data
		case "Strip":
			if n.StripLeft == nil || n.StripRight == nil {
				return nil, fmt.Errorf("%w: Strip without strip_left / strip_right", ErrUnsupportedModel)
			}
			steps = append(steps, normStep{kind: normStrip, left: *n.StripLeft, right: *n.StripRight})
		case "Replace":
			if n.Pattern.Regex != " {2,}" || n.Pattern.Str != "" || n.Content != metaSpace {
				return nil, fmt.Errorf("%w: only Replace(Regex \" {2,}\" -> %s) is implemented", ErrUnsupportedModel, metaSpace)
			}
			steps = append(steps, normStep{kind: normCollapseSpaces})
		default:
			return nil, fmt.Errorf("%w: normalizer %q", ErrUnsupportedModel, n.Type)
		}
	}
	return newNormalizer(charsmap, steps)
}
