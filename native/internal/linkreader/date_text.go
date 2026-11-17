package linkreader

import (
	"fmt"
	"regexp"
	"strconv"
)

var cnDateText = regexp.MustCompile(`^\s*(\d{4})\s*年\s*(\d{1,2})\s*月(?:\s*(\d{1,2})\s*日)?\s*$`)

// isoDateText 把平台给的中文日期（“2011 年 4 月 18 日”“2025年11月16日”“2027年05月”）写成统一的
// 2011-04-18、2027-05。别的写法（“即将推出”“Q3 2026”“2026 年第三季度”）和不合法的月日原样返回，不猜。
// 只有日期的文字不转成 time.Time：时间类型一旦进了卡片，会被补上凭空的钟点。
func isoDateText(s string) string {
	m := cnDateText.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	if month < 1 || month > 12 {
		return s
	}
	if m[3] == "" {
		return fmt.Sprintf("%04d-%02d", year, month)
	}
	day, _ := strconv.Atoi(m[3])
	if day < 1 || day > 31 {
		return s
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func isoDateField(f Field[string]) Field[string] {
	if f.State == "available" {
		f.Value = isoDateText(f.Value)
	}
	return f
}
