package parse

import (
	"fmt"
	"strings"
	"time"
)

// ParseDate 解析“2026-03-01”这样的日期；不存在的日子（如 2 月 30 日）报错。
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("日期 %q 不对：%w", s, err)
	}
	return t, nil
}
