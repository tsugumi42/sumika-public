package parse

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Order 是一笔订单。Amount 的单位是分。
type Order struct {
	Date        time.Time
	Customer    string
	Amount      int
	DiscountPct int
}

// ParseLine 解析“日期,客户,金额,折扣百分比”一行。客户名去掉首尾空白，不能为空；折扣在 0 到 100 之间。
func ParseLine(line string) (Order, error) {
	parts := strings.Split(line, ",")
	if len(parts) != 4 {
		return Order{}, fmt.Errorf("要 4 列，拿到 %d 列", len(parts))
	}
	date, err := ParseDate(parts[0])
	if err != nil {
		return Order{}, err
	}
	customer := strings.TrimSpace(parts[1])
	if customer == "" {
		return Order{}, fmt.Errorf("客户名是空的")
	}
	amount, err := ParseAmount(parts[2])
	if err != nil {
		return Order{}, err
	}
	pct, err := strconv.Atoi(strings.TrimSpace(parts[3]))
	if err != nil || pct < 0 || pct > 100 {
		return Order{}, fmt.Errorf("折扣 %q 不对", parts[3])
	}
	return Order{Date: date, Customer: customer, Amount: amount, DiscountPct: pct}, nil
}

// ParseAll 解析整个文件：空行和 # 开头的行跳过，解析不了的行数一次坏行。
func ParseAll(r io.Reader) (orders []Order, bad int) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		o, err := ParseLine(line)
		if err != nil {
			bad++
			continue
		}
		orders = append(orders, o)
	}
	return orders, bad
}
