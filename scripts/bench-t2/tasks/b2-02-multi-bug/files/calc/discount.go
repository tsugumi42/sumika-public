// Package calc 算钱：折扣、税、应付金额和统计。金额都是分。
package calc

// Discount 返回 amount 打 pct% 折扣省下的钱，向下取整到分。
func Discount(amount, pct int) int {
	return amount * pct / 100
}
