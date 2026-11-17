// 命令 shelf 打印演示数据的汇总和缺货清单。
package main

import (
	"fmt"
	"os"

	"example.com/shelf/config"
	"example.com/shelf/export"
	"example.com/shelf/report"
	"example.com/shelf/store"
)

func main() {
	// 设置文件路径从第一个参数取，不给就只用默认值（见 config.LoadCfg）。
	path := ""
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	cfg, err := config.LoadCfg(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读设置失败：", err)
		os.Exit(1)
	}
	s := store.Seed()
	report.PrintSummary(s, cfg)
	report.PrintLowStock(os.Stdout, s, cfg)
	fmt.Printf("sku A-100 qty: %d\n", s.GetQty("A-100"))
	if err := export.WriteCSV(os.Stdout, s, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "导出失败：", err)
		os.Exit(1)
	}
}
