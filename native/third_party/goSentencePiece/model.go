package sentencepiece

import "math"

// PieceType represents the type of a vocabulary piece as defined by the
// SentencePiece model specification. Each piece in the vocabulary has a type
// that determines how it is handled during encoding.
type PieceType int32

const (
	// PieceNormal is a regular vocabulary piece learned during training.
	PieceNormal PieceType = 1
	// PieceUnknown represents the unknown token used for out-of-vocabulary inputs.
	PieceUnknown PieceType = 2
	// PieceControl represents control tokens such as BOS and EOS.
	PieceControl PieceType = 3
	// PieceUserDefined represents user-defined tokens that always take priority.
	PieceUserDefined PieceType = 4
	// PieceUnused represents unused/reserved vocabulary slots.
	PieceUnused PieceType = 5
	// PieceByte represents a byte-level fallback token (e.g., "<0x41>").
	PieceByte PieceType = 6
)

// Piece represents a single entry in the SentencePiece vocabulary, including
// its string representation, log-probability score, and type.
type Piece struct {
	Piece string
	Score float64
	Type  PieceType
}

// Model holds the loaded Unigram vocabulary and a byte trie for prefix
// matching during tokenization.
type Model struct {
	pieces []Piece
	unkID  int

	// Byte-level trie for vocab prefix matching (matches reference traverse behavior).
	vocabTrie *ByteTrie
	// Precomputed scores.
	maxScoreVal float64
	minScoreVal float64
}

// computeMinMaxScores sets the minimum and maximum score over the normal pieces
// with a non-zero score (what unigram.go needs for the unknown-character penalty
// and the bonus of user-defined pieces).
func (m *Model) computeMinMaxScores() {
	m.minScoreVal = math.MaxFloat64
	m.maxScoreVal = -math.MaxFloat64
	for _, p := range m.pieces {
		if p.Type == PieceNormal && p.Score != 0 {
			if p.Score < m.minScoreVal {
				m.minScoreVal = p.Score
			}
			if p.Score > m.maxScoreVal {
				m.maxScoreVal = p.Score
			}
		}
	}
}
