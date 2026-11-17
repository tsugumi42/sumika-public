package sentencepiece

// pieceIsByte reports whether the given piece string is a byte-fallback token
// matching the pattern "<0xHH>". Such pieces are classified as PieceByte and
// stay out of the vocabulary trie (byte_fallback itself is not supported).
func pieceIsByte(piece string) bool {
	if len(piece) != 6 {
		return false
	}
	return piece[0] == '<' && piece[1] == '0' && piece[2] == 'x' && piece[5] == '>'
}
