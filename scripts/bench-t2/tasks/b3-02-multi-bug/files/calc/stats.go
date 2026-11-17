package calc

import "sort"

// Average 返回平均值，向下取整；totals 为空时返回 0。
func Average(totals []int) int {
	sum := 0
	for _, v := range totals {
		sum += v
	}
	return sum / len(totals)
}

// Median 返回中位数；个数是偶数时取中间两个的平均（向下取整）；为空返回 0。不改动传入的切片。
func Median(totals []int) int {
	if len(totals) == 0 {
		return 0
	}
	s := append([]int(nil), totals...)
	sort.Ints(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
