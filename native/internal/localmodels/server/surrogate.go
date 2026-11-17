package server

// hasLoneSurrogate 看合法 JSON 里有没有孤立的代理对转义（\ud800～\udbff 后面不是 \udc00～\udfff，或者单独的 \udc00～\udfff）。
// Python 对这样的请求体回 422（实测，python-http.json 里"孤立代理对"那条）；Go 的 encoding/json 会悄悄换成 U+FFFD 照常算，
// 同一个坏输入两边结果不同，所以在解析前拦下。
//
// 调用前 body 已经是合法 JSON，所以反斜杠只会出现在字符串里、且每个都是一个转义的开头：逐个转义往前走，
// \u 就读四位十六进制，别的转义跳过被转义的那个字符（所以 \\ud800，即字面的反斜杠加 ud800，不会被误判）。
func hasLoneSurrogate(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		if i+1 >= len(body) {
			return false
		}
		if body[i+1] != 'u' {
			i++ // 跳过被转义的那个字符
			continue
		}
		v, ok := hex4(body, i+2)
		if !ok {
			return false // 合法 JSON 里不会有
		}
		i += 5 // 走到这个 \uXXXX 的最后一位
		switch {
		case v >= 0xD800 && v <= 0xDBFF: // 高位：后面必须紧跟一个低位转义
			if i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
				if lo, ok := hex4(body, i+3); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					i += 6
					continue
				}
			}
			return true
		case v >= 0xDC00 && v <= 0xDFFF: // 低位单独出现
			return true
		}
	}
	return false
}

// hex4 读 b[i:i+4] 这四个字节当十六进制数；不够四个或有不是十六进制的就 false。
func hex4(b []byte, i int) (rune, bool) {
	if i+4 > len(b) {
		return 0, false
	}
	var v rune
	for _, c := range b[i : i+4] {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}
