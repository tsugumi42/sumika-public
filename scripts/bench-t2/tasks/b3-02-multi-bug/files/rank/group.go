// Package rank 按客户汇总，列出消费最多的几位。
package rank

import (
	"strings"

	"example.com/ledger/calc"
	"example.com/ledger/parse"
)

// SumByCustomer 把每位客户的应付金额加起来。客户名不分大小写、去掉首尾空白后合并，结果的键是小写的名字。
func SumByCustomer(orders []parse.Order, taxBps int) map[string]int {
	sums := map[string]int{}
	for _, o := range orders {
		name := strings.ToLower(strings.TrimSpace(o.Customer))
		sums[name] += calc.OrderTotal(o, taxBps)
	}
	return sums
}
