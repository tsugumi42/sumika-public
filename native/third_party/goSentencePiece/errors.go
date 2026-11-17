package sentencepiece

import "errors"

// Sentinel errors returned by the tokenizer.
var (
	// ErrInvalidModel is returned when the tokenizer.json cannot be parsed.
	ErrInvalidModel = errors.New("sentencepiece: invalid model")

	// ErrUnsupportedModel is returned when the tokenizer.json uses something
	// this fork does not implement (a model type other than Unigram,
	// byte_fallback, non-special added tokens, another pre-tokenizer).
	ErrUnsupportedModel = errors.New("sentencepiece: unsupported model type")
)
