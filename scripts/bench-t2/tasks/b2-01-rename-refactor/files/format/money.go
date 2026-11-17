// Package format 放显示用的小函数。
package format

import "fmt"

// Money 把分显示成“CNY 12.50”这样。
func Money(cents int, currency string) string {
	return fmt.Sprintf("%s %d.%02d", currency, cents/100, cents%100)
}
