// Package parse 把文本变成订单。
package parse

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseAmount 把“7”“12.5”“19.99”“0.05”这样的金额转成分。
// 小数最多两位；整数部分必须是非负整数；其余写法报错。
func ParseAmount(s string) (int, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.Atoi(whole)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("金额 %q 的整数部分不对", s)
	}
	f := 0
	if frac != "" {
		if len(frac) > 2 {
			return 0, fmt.Errorf("金额 %q 的小数超过两位", s)
		}
		f, err = strconv.Atoi(frac)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("金额 %q 的小数部分不对", s)
		}
	}
	return w*100 + f, nil
}
