package linktrunc

import "unicode"

// Display 是合并转发卡片外面几行字（标题、预览、摘要、外显）的写法（规格-链接发送改版 B10，用户 2026-11-13）：
// 这些字段按显示宽度算（limit 个全角字，半角字母数字算半个），超出时同样截在最近的标点后，补“……”，不另起一行。
// 省略号占的宽度算在 limit 里，截完不会超；截两遍和截一遍一样。
func Display(s string, limit int) string {
	max := limit * 2
	if displayWidth(s) <= max {
		return s
	}
	room := max - displayWidth(Ellipsis)
	at, w, runes := 0, 0, 0
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > room {
			break
		}
		w += rw
		runes++
		at = i + len(string(r))
	}
	return ellipsize(s[:cutPoint(s, at, window(runes))])
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth：拉丁字母、数字、常见符号算 1，中日韩文字、全角符号、中文引号和省略号、表情算 2，控制字符不占宽。
func runeWidth(r rune) int {
	switch {
	case unicode.IsControl(r):
		return 0
	case r < 0x2000, r > 0x206F && r < 0x2E80:
		return 1
	}
	return 2
}
