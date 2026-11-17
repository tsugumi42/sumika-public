// 命令 ledger 读 data/orders.csv，打印订单数、坏行数、平均应付金额、中位数和前三名客户。
package main

import (
	"fmt"
	"os"

	"example.com/ledger/calc"
	"example.com/ledger/parse"
	"example.com/ledger/rank"
)

const taxBps = 700

func main() {
	f, err := os.Open("data/orders.csv")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	orders, bad := parse.ParseAll(f)
	totals := make([]int, 0, len(orders))
	for _, o := range orders {
		totals = append(totals, calc.OrderTotal(o, taxBps))
	}
	fmt.Printf("orders: %d, bad lines: %d\n", len(orders), bad)
	fmt.Printf("average: %d, median: %d\n", calc.Average(totals), calc.Median(totals))
	for i, e := range rank.TopCustomers(rank.SumByCustomer(orders, taxBps), 10) {
		fmt.Printf("%d. %s %d\n", i+1, e.Customer, e.Total)
	}
}
