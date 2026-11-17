package calc

import "example.com/ledger/parse"

// OrderTotal 返回一笔订单的应付金额：金额 - 折扣，再加上对折后金额算的税。
func OrderTotal(o parse.Order, taxBps int) int {
	net := o.Amount - Discount(o.Amount, o.DiscountPct)
	return net + Tax(net, taxBps)
}
