package calc

// Tax 返回 amount 的税，税率按万分比 bps 给（700 就是 7%）。四舍五入到分，0.5 进位。
func Tax(amount, bps int) int {
	return amount * bps / 10000
}
