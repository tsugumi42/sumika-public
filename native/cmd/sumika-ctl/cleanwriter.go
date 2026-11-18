package main

import (
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"

	"sumika-native/internal/ctlproto"
)

// cleanWriter 是给人看的输出的最后一道关：不管哪个字段、哪个分支打出来，终端控制序列（ESC 开头的清屏、改标题、写剪贴板）、
// 回车和别的控制字符都换掉，只留我们自己写的换行和制表符。服务端出去前已经过一遍（internal/ctlserver），这里不依赖它：
// 以后加新字段、加新分支也不会漏。--json 不走这里（json.Marshal 会把控制字符转成 \u00XX）
type cleanWriter struct{ w io.Writer }

func (c cleanWriter) Write(p []byte) (int, error) {
	parts := bytes.Split(p, []byte("\n"))
	for i, part := range parts {
		parts[i] = []byte(ctlproto.Clean(string(part)))
	}
	if _, err := c.w.Write(bytes.Join(parts, []byte("\n"))); err != nil {
		return 0, err
	}
	return len(p), nil // 换掉的字符长度可能变，但对调用方来说这一批都写完了
}

// jsonSafe 给 --json 的输出再加一道：encoding/json 不转义 DEL（0x7f）和 C1 控制字符（0x80～0x9f，8 位终端会当控制码），
// 也不改 RawMessage 里的非法 UTF-8。这两样在 JSON 里只可能出现在字符串内部，换成转义写法后内容不变、仍是合法 JSON
func jsonSafe(b []byte) []byte {
	if utf8.Valid(b) && !bytes.ContainsFunc(b, isRawControl) {
		return b
	}
	bs := string([]byte{92, 'u'}) // 反斜杠加 u
	var out bytes.Buffer
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		switch {
		case r == utf8.RuneError && size == 1:
			out.WriteString(bs + "fffd")
		case isRawControl(r):
			out.WriteString(bs + fmt.Sprintf("%04x", r))
		default:
			out.Write(b[:size])
		}
		b = b[size:]
	}
	return out.Bytes()
}

func isRawControl(r rune) bool { return r == 0x7f || (r >= 0x80 && r <= 0x9f) }
