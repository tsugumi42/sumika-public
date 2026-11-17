package sentencepiece

// Encoder normalizes the input and runs the Unigram Viterbi search over it.
//
// Encoder is safe for concurrent use by multiple goroutines.
type Encoder struct {
	model      *Model
	normalizer *Normalizer

	// preTokenize replaces the default normalization pipeline's single
	// segment: it receives the normalized text and returns pre-tokenized
	// segments that are each encoded independently (HuggingFace's Metaspace
	// pre-tokenizers).
	preTokenize func(string) []string
}

func newEncoder(model *Model, normalizer *Normalizer) *Encoder {
	return &Encoder{model: model, normalizer: normalizer}
}

// Encode normalizes and tokenizes the input text, returning the resulting
// token IDs. It returns nil for empty input.
func (e *Encoder) Encode(text string) []int {
	if len(text) == 0 {
		return nil
	}

	pieces := e.mergeConsecutiveUnk(e.encodeSegment(text))

	ids := make([]int, len(pieces))
	for i, p := range pieces {
		ids[i] = p.id
	}

	return ids
}

// encodeSegment normalizes first, then optionally pre-tokenizes into segments.
func (e *Encoder) encodeSegment(text string) []encodedPiece {
	normalized := e.normalizer.Normalize(text)

	if e.preTokenize != nil {
		segments := e.preTokenize(normalized)
		var all []encodedPiece
		for _, seg := range segments {
			all = append(all, e.model.encodeUnigram(seg)...)
		}
		return all
	}

	return e.model.encodeUnigram(normalized)
}

// mergeConsecutiveUnk merges consecutive UNK pieces into a single piece.
// This matches the C++ reference behavior (sentencepiece_processor.cc:609-625)
// which merges "continuous run of unknown pieces" when byte_fallback is disabled
// (the only mode this fork supports).
func (e *Encoder) mergeConsecutiveUnk(pieces []encodedPiece) []encodedPiece {
	merged := pieces[:0]
	for _, p := range pieces {
		if p.id == e.model.unkID && len(merged) > 0 && merged[len(merged)-1].id == e.model.unkID {
			continue
		}
		merged = append(merged, p)
	}
	return merged
}
