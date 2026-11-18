package tokenspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// node 是保留键顺序的 JSON 值：读进来、改 `$value`、原样写回时键的顺序不变，令牌文件的 diff 才看得懂。
type node struct {
	kind    byte // 'o' 对象、'a' 数组、's' 字符串、'n' 数字、'b' 布尔、'z' null
	members []member
	items   []*node
	str     string
	num     string // 数字的原文，写回时不改写法
	b       bool
}

type member struct {
	key string
	val *node
}

func parseJSON(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := readNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON 末尾还有多余内容")
	}
	return n, nil
}

func readNode(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &node{kind: 'o'}
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				if seen[k] {
					return nil, fmt.Errorf("同一个对象里键 %q 出现两次", k)
				}
				seen[k] = true
				v, err := readNode(dec)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, member{k, v})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		case '[':
			n := &node{kind: 'a'}
			for dec.More() {
				v, err := readNode(dec)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		}
	case string:
		return &node{kind: 's', str: t}, nil
	case json.Number:
		return &node{kind: 'n', num: string(t)}, nil
	case bool:
		return &node{kind: 'b', b: t}, nil
	case nil:
		return &node{kind: 'z'}, nil
	}
	return nil, fmt.Errorf("读不懂的 JSON 记号 %v", tok)
}

func (n *node) get(key string) *node {
	if n == nil || n.kind != 'o' {
		return nil
	}
	for _, m := range n.members {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

func (n *node) set(key string, v *node) {
	for i, m := range n.members {
		if m.key == key {
			n.members[i].val = v
			return
		}
	}
	n.members = append(n.members, member{key, v})
}

func (n *node) float() (float64, bool) {
	if n == nil || n.kind != 'n' {
		return 0, false
	}
	f, err := strconv.ParseFloat(n.num, 64)
	return f, err == nil
}

func numNode(f float64) *node {
	return &node{kind: 'n', num: strconv.FormatFloat(f, 'f', -1, 64)}
}

func strNode(s string) *node { return &node{kind: 's', str: s} }

// encode 按两格缩进写回；只有数字的数组写成一行。
func (n *node) encode() []byte {
	var b strings.Builder
	n.write(&b, 0)
	b.WriteByte('\n')
	return []byte(b.String())
}

func (n *node) write(b *strings.Builder, depth int) {
	pad := strings.Repeat("  ", depth+1)
	switch n.kind {
	case 'o':
		if len(n.members) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, m := range n.members {
			b.WriteString(pad)
			k, _ := json.Marshal(m.key)
			b.Write(k)
			b.WriteString(": ")
			m.val.write(b, depth+1)
			if i < len(n.members)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteByte('}')
	case 'a':
		flat := true
		for _, it := range n.items {
			if it.kind == 'o' || it.kind == 'a' {
				flat = false
			}
		}
		if flat {
			b.WriteByte('[')
			for i, it := range n.items {
				if i > 0 {
					b.WriteString(", ")
				}
				it.write(b, depth)
			}
			b.WriteByte(']')
			return
		}
		b.WriteString("[\n")
		for i, it := range n.items {
			b.WriteString(pad)
			it.write(b, depth+1)
			if i < len(n.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteByte(']')
	case 's':
		s, _ := json.Marshal(n.str)
		b.Write(s)
	case 'n':
		b.WriteString(n.num)
	case 'b':
		b.WriteString(strconv.FormatBool(n.b))
	default:
		b.WriteString("null")
	}
}
