package jsstr

import "unicode/utf8"

// Decoder 是 WHATWG 的 UTF-8 解码器（JS 的 TextDecoder 用的就是它）：坏的字节换成 U+FFFD，
// 一段不完整的多字节序列（"最长的合法前缀"）只换一个 U+FFFD。Go 的 utf8.DecodeRune 是一个字节一个 U+FFFD，不一样
type Decoder struct {
	cp           rune
	need, seen   int
	lower, upper byte
}

func NewDecoder() *Decoder { return &Decoder{lower: 0x80, upper: 0xBF} }

func (d *Decoder) reset() { d.cp, d.need, d.seen, d.lower, d.upper = 0, 0, 0, 0x80, 0xBF }

// Decode 接着解一段字节；最后不完整的序列留到下一段（stream: true）
func (d *Decoder) Decode(b []byte) string {
	var out []byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if d.need == 0 {
			switch {
			case c <= 0x7F:
				out = append(out, c)
			case c >= 0xC2 && c <= 0xDF:
				d.need, d.cp = 1, rune(c&0x1F)
			case c >= 0xE0 && c <= 0xEF:
				if c == 0xE0 {
					d.lower = 0xA0
				} else if c == 0xED {
					d.upper = 0x9F
				}
				d.need, d.cp = 2, rune(c&0x0F)
			case c >= 0xF0 && c <= 0xF4:
				if c == 0xF0 {
					d.lower = 0x90
				} else if c == 0xF4 {
					d.upper = 0x8F
				}
				d.need, d.cp = 3, rune(c&0x07)
			default:
				out = utf8.AppendRune(out, utf8.RuneError)
			}
			continue
		}
		if c < d.lower || c > d.upper {
			// 这个字节不接着前面的：前面那段换成一个 U+FFFD，这个字节重新当开头看
			d.reset()
			out = utf8.AppendRune(out, utf8.RuneError)
			i--
			continue
		}
		d.lower, d.upper = 0x80, 0xBF
		d.cp = d.cp<<6 | rune(c&0x3F)
		d.seen++
		if d.seen == d.need {
			out = utf8.AppendRune(out, d.cp)
			d.reset()
		}
	}
	return string(out)
}

// Flush 是最后一次 decode()：还没解完的序列换成一个 U+FFFD
func (d *Decoder) Flush() string {
	if d.need > 0 {
		d.reset()
		return string(utf8.RuneError)
	}
	return ""
}

// DecodeUTF8 像 new TextDecoder().decode(b)、Buffer 的 toString('utf8')：整段一次解完
func DecodeUTF8(b []byte) string {
	d := NewDecoder()
	return d.Decode(b) + d.Flush()
}
