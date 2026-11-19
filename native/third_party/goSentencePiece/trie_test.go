package sentencepiece

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// naiveTrie 是对照：一个 map 加一遍扫描，和 ByteTrie 对同一批键、同一批查询必须给出一样的答案。
type naiveTrie struct {
	keys []string
	id   map[string]int
}

func newNaive() *naiveTrie { return &naiveTrie{id: map[string]int{}} }

func (n *naiveTrie) add(k string, id int) {
	if k == "" {
		return
	}
	n.keys = append(n.keys, k)
	n.id[k] = id // 后加的赢
}

// walk 返回走完 s 之后的 (有没有这条路, 词条 id 或 -1)
func (n *naiveTrie) walk(s string) (bool, int) {
	for _, k := range n.keys {
		if strings.HasPrefix(k, s) {
			if id, ok := n.id[s]; ok {
				return true, id
			}
			return true, -1
		}
	}
	return false, -1
}

func (t *ByteTrie) walk(s string) (bool, int) {
	var node int32
	ret := -1
	for i := 0; i < len(s); i++ {
		node, ret = t.Traverse(node, s[i])
		if ret == -2 {
			return false, -1
		}
	}
	return true, ret
}

func checkTrie(t *testing.T, name string, b *trieBuilder, n *naiveTrie, queries []string) {
	t.Helper()
	tr := b.Build()
	for _, q := range queries {
		wantOK, wantID := n.walk(q)
		gotOK, gotID := tr.walk(q)
		// 空串：根节点本身，没有走任何一步，walk 返回 (true, -1)；naive 对空串也是 (true, -1) 或有键时 true
		if q == "" {
			continue
		}
		if gotOK != wantOK || (wantOK && gotID != wantID) {
			t.Fatalf("%s：查 %q，ByteTrie 给 (%v, %d)，对照给 (%v, %d)", name, q, gotOK, gotID, wantOK, wantID)
		}
	}
}

func TestByteTrieMatchesNaive(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for round := 0; round < 200; round++ {
		var b trieBuilder
		n := newNaive()
		alphabet := []byte{0x00, 'a', 'b', 'c', 0x80, 0xe4, 0xff}
		var keys []string
		for i, count := 0, r.IntN(60); i < count; i++ {
			l := r.IntN(6) // 0 = 空串，应被忽略
			kb := make([]byte, l)
			for j := range kb {
				kb[j] = alphabet[r.IntN(len(alphabet))]
			}
			k := string(kb)
			id := r.IntN(1000)
			b.Add(k, id)
			n.add(k, id)
			keys = append(keys, k)
		}
		// 查询：每个键的每个前缀、每个前缀多一个字节、再加一些随机串
		var queries []string
		for _, k := range keys {
			for i := 0; i <= len(k); i++ {
				queries = append(queries, k[:i])
				for _, c := range alphabet {
					queries = append(queries, k[:i]+string([]byte{c}))
				}
			}
		}
		checkTrie(t, "随机键", &b, n, queries)
	}
}

func TestByteTrieEdges(t *testing.T) {
	// 根下 256 个分支，每个字节都是一个词条
	var b trieBuilder
	n := newNaive()
	for c := 0; c < 256; c++ {
		k := string([]byte{byte(c)})
		b.Add(k, c)
		n.add(k, c)
	}
	var q []string
	for c := 0; c < 256; c++ {
		q = append(q, string([]byte{byte(c)}), string([]byte{byte(c), 'x'}))
	}
	checkTrie(t, "256 个分支", &b, n, q)

	// 同一个词条加两次，后加的赢；互为前缀的词条各自保留 id
	var d trieBuilder
	dn := newNaive()
	for _, kv := range []struct {
		k  string
		id int
	}{{"ab", 1}, {"abc", 2}, {"ab", 3}, {"", 9}, {"a", 4}} {
		d.Add(kv.k, kv.id)
		dn.add(kv.k, kv.id)
	}
	checkTrie(t, "重复和前缀", &d, dn, []string{"a", "ab", "abc", "abcd", "b", "ba"})
	if _, id := d.Build().walk("ab"); id != 3 {
		t.Fatalf("重复的词条 ab 该取后加的 3，得到 %d", id)
	}

	// 没有任何词条：根下没有边
	var e trieBuilder
	e.Add("", 1)
	if ok, _ := e.Build().walk("a"); ok {
		t.Fatal("空树不该走得通")
	}
}
