package sentencepiece

import (
	"cmp"
	"encoding/binary"
	"slices"
	"strings"
)

// ByteTrie is a read-only byte-level trie used for vocabulary prefix matching
// during Viterbi tokenization. It is stored as three flat arrays in
// breadth-first order (fork change; upstream allocated a [256]*ByteTrie
// children table per node, about 2 KB each, which came to 1.8 GB for a
// 250k-piece vocabulary): the children of node n are the nodes
// first[n] .. first[n+1]-1, contiguous and sorted by label, so one step is a
// binary search over at most 256 bytes. Node 0 is the root.
type ByteTrie struct {
	first []int32 // first[n] is the first child of node n; first[n+1] is one past its last
	label []byte  // the byte on the edge into each node (unused for the root)
	value []int32 // the piece ID at each node, -1 if the node is not a piece
}

// Traverse advances one byte from node (0 is the root) and returns the child
// node and what is there:
//
//	(-1, -2) if no transition exists
//	(child, -1) if the transition exists but no piece ends there
//	(child, id) the piece ID at the child
//
// Same contract as the upstream Traverse, with node numbers instead of pointers.
func (t *ByteTrie) Traverse(node int32, b byte) (int32, int) {
	lo, end := t.first[node], t.first[node+1]
	hi := end
	for lo < hi {
		mid := int32(uint32(lo+hi) >> 1)
		if t.label[mid] < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= end || t.label[lo] != b {
		return -1, -2
	}
	return lo, int(t.value[lo])
}

// trieBuilder collects (piece, id) pairs and builds the ByteTrie in one go.
// Empty pieces are ignored; when the same piece is added twice the later one
// wins (what Insert did upstream).
type trieBuilder struct {
	keys []string
	ids  []int32
}

func (b *trieBuilder) Add(piece string, id int) {
	b.keys = append(b.keys, piece)
	b.ids = append(b.ids, int32(id))
}

// Build sorts the pieces by bytes and numbers the nodes breadth first: a node
// is a run of sorted pieces that share a prefix, and its children are the runs
// that agree on the next byte.
func (b *trieBuilder) Build() *ByteTrie {
	order := make([]int32, 0, len(b.keys))
	for i, k := range b.keys {
		if k != "" {
			order = append(order, int32(i))
		}
	}
	slices.SortFunc(order, func(x, y int32) int {
		if c := strings.Compare(b.keys[x], b.keys[y]); c != 0 {
			return c
		}
		return cmp.Compare(x, y)
	})

	t := &ByteTrie{first: []int32{1}, label: []byte{0}, value: []int32{-1}}
	lo, hi, depth := []int32{0}, []int32{int32(len(order))}, []int32{0}
	for n := 0; n < len(lo); n++ {
		l, h, d := lo[n], hi[n], int(depth[n])
		// A piece exactly equal to the prefix sorts first; duplicates are adjacent, the last one wins.
		for l < h && len(b.keys[order[l]]) == d {
			t.value[n] = b.ids[order[l]]
			l++
		}
		for l < h {
			c := b.keys[order[l]][d]
			r := l + 1
			for r < h && b.keys[order[r]][d] == c {
				r++
			}
			lo, hi, depth = append(lo, l), append(hi, r), append(depth, int32(d+1))
			t.label, t.value = append(t.label, c), append(t.value, -1)
			l = r
		}
		t.first = append(t.first, int32(len(lo)))
	}
	return t
}

// dartsArray is the Darts-clone double-array trie that SentencePiece's
// precompiled charsmap uses for Unicode normalization: NFKC and custom
// character mapping rules in a compact array of uint32 units.
type dartsArray struct {
	units []uint32
}

// newDartsArray reads the raw binary trie data extracted from the precompiled
// charsmap: a sequence of little-endian uint32 units.
func newDartsArray(data []byte) *dartsArray {
	numUnits := len(data) / 4
	units := make([]uint32, numUnits)
	for i := 0; i < numUnits; i++ {
		units[i] = binary.LittleEndian.Uint32(data[i*4:])
	}
	return &dartsArray{units: units}
}

// dartsOffset extracts the offset from a Darts unit value.
// offset = (unit >> 10) << ((unit & 0x200) >> 6)
func dartsOffset(unit uint32) uint32 {
	return (unit >> 10) << ((unit & 0x200) >> 6)
}

// dartsHasLeaf returns whether this node has a leaf (terminal value).
func dartsHasLeaf(unit uint32) bool {
	return ((unit >> 8) & 1) == 1
}

// dartsValue extracts the value from a leaf unit.
func dartsValue(unit uint32) int {
	return int(unit & 0x7FFFFFFF)
}

// dartsLabel extracts the label (check byte) from a unit.
func dartsLabel(unit uint32) uint32 {
	return unit & ((1 << 31) | 0xFF)
}

// longestMatch follows the exact Darts-clone commonPrefixSearch algorithm over
// key and returns the longest prefix of key that is in the trie: the offset
// into the replacement table and the number of key bytes it covers. length is
// 0 when no prefix matches. (Upstream returned every prefix match as a slice
// and the caller took the last one; this fork asks for the longest directly.)
func (d *dartsArray) longestMatch(key string) (value, length int) {
	n := len(d.units)
	if n == 0 {
		return 0, 0
	}

	// Start at root (position 0).
	nodePos := 0
	unit := d.units[nodePos]
	// XOR with root's offset.
	nodePos ^= int(dartsOffset(unit))

	for i := 0; i < len(key); i++ {
		b := uint32(key[i])

		// XOR with key byte.
		p := nodePos ^ int(b)
		if p < 0 || p >= n {
			break
		}

		unit = d.units[p]
		// Check label.
		if dartsLabel(unit) != b {
			break
		}

		// XOR with this unit's offset.
		nodePos = p ^ int(dartsOffset(unit))

		// Check for leaf.
		if dartsHasLeaf(unit) {
			if nodePos >= 0 && nodePos < n {
				value, length = dartsValue(d.units[nodePos]), i+1
			}
		}
	}

	return value, length
}
