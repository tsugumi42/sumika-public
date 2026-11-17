// Package sentencepiece is a trimmed fork of github.com/tggo/goSentencePiece
// v1.1.0 (see PROVENANCE.md): a pure Go Unigram tokenizer that reads a
// HuggingFace tokenizer.json and returns the content token IDs. Special
// tokens, truncation and sentence pairs are left to the caller.
package sentencepiece

import (
	"fmt"
	"io"
)

// Tokenizer is a loaded Unigram tokenizer. It is safe for concurrent use by
// multiple goroutines after creation.
type Tokenizer struct {
	encoder *Encoder
}

// NewTokenizerFromJSONReader loads a tokenizer from a reader containing
// HuggingFace tokenizer.json data.
func NewTokenizerFromJSONReader(r io.Reader) (*Tokenizer, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer data: %w", err)
	}
	return loadFromJSON(data)
}

// Encode normalizes and tokenizes the input text and returns the content token
// IDs (no special tokens). An empty input returns nil.
func (t *Tokenizer) Encode(text string) []int {
	return t.encoder.Encode(text)
}
