package sentencepiece

import (
	"unicode/utf8"
)

const unkPenalty = 10.0

// encodedPiece represents a single piece in the encoding result.
type encodedPiece struct {
	id int
}

// bestPathNode stores Viterbi DP state for each byte position.
type bestPathNode struct {
	id            int     // vocab piece ID (-1 = uninitialized)
	bestPathScore float64 // cumulative score to reach this position
	startsAt      int     // byte position where the best token starts
}

// encodeUnigram performs Viterbi-based Unigram tokenization.
// This follows the exact SentencePiece Viterbi implementation from unigram_model.cc.
func (m *Model) encodeUnigram(normalized string) []encodedPiece {
	if len(normalized) == 0 {
		return nil
	}

	dp := m.viterbiForward(normalized)
	return m.viterbiBacktrack(normalized, dp)
}

// viterbiForward runs the forward Viterbi DP pass over the normalized string,
// returning the best-path DP array indexed by byte position.
func (m *Model) viterbiForward(normalized string) []bestPathNode {
	size := len(normalized)
	unkScore := m.minScoreVal - unkPenalty

	dp := make([]bestPathNode, size+1)
	for i := range dp {
		dp[i].id = -1
	}

	startsAt := 0
	for startsAt < size {
		bestScoreHere := dp[startsAt].bestPathScore
		mblen := m.charLen(normalized, startsAt, size)

		hasSingleNode := m.trieSearch(normalized, startsAt, size, mblen, bestScoreHere, dp)

		if !hasSingleNode {
			m.handleFallback(startsAt, mblen, size, unkScore, bestScoreHere, dp)
		}

		startsAt += mblen
	}

	return dp
}

// charLen returns the UTF-8 byte length of the character at position pos,
// clamped to the remaining input size.
func (m *Model) charLen(normalized string, pos, size int) int {
	_, mblen := utf8.DecodeRuneInString(normalized[pos:])
	if mblen == 0 {
		mblen = 1
	}
	if pos+mblen > size {
		mblen = size - pos
	}
	return mblen
}

// trieSearch traverses the vocab trie byte-by-byte from startsAt, updating
// the DP array for each matching piece. Returns true if any match covers
// exactly one Unicode character (hasSingleNode).
func (m *Model) trieSearch(normalized string, startsAt, size, mblen int, bestScoreHere float64, dp []bestPathNode) bool {
	hasSingleNode := false
	var node int32 // the root
	keyPos := startsAt

	for keyPos < size {
		var ret int
		node, ret = m.vocabTrie.Traverse(node, normalized[keyPos])
		keyPos++

		if ret == -2 {
			break
		}
		if ret < 0 {
			continue
		}

		length := keyPos - startsAt
		score := m.pieceScore(ret, length)
		candidateScore := score + bestScoreHere
		target := &dp[keyPos]

		if target.id == -1 || candidateScore > target.bestPathScore {
			target.bestPathScore = candidateScore
			target.startsAt = startsAt
			target.id = ret
		}

		if !hasSingleNode && length == mblen {
			hasSingleNode = true
		}
	}

	return hasSingleNode
}

// pieceScore returns the score for a piece, with user-defined pieces receiving
// a bonus proportional to their length.
func (m *Model) pieceScore(pieceID, length int) float64 {
	if m.pieces[pieceID].Type == PieceUserDefined {
		return float64(length)*m.maxScoreVal - 0.1
	}
	return m.pieces[pieceID].Score
}

// handleFallback handles the case where no vocab piece covers a single
// Unicode character at the current position: the character becomes UNK.
func (m *Model) handleFallback(startsAt, mblen, size int, unkScore, bestScoreHere float64, dp []bestPathNode) {
	endPos := startsAt + mblen
	if endPos > size {
		return
	}

	candidateScore := unkScore + bestScoreHere
	target := &dp[endPos]
	if target.id == -1 || candidateScore > target.bestPathScore {
		target.bestPathScore = candidateScore
		target.startsAt = startsAt
		target.id = m.unkID
	}
}

// viterbiBacktrack recovers the optimal segmentation by walking backwards
// through the DP array.
func (m *Model) viterbiBacktrack(normalized string, dp []bestPathNode) []encodedPiece {
	size := len(normalized)
	if dp[size].id == -1 {
		return []encodedPiece{{id: m.unkID}}
	}

	var result []encodedPiece
	endsAt := size
	for endsAt > 0 {
		node := dp[endsAt]
		result = append(result, encodedPiece{id: node.id})
		endsAt = node.startsAt
	}

	// Reverse to get forward order.
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}
