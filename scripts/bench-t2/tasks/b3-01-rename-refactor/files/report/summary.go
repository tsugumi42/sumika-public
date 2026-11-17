package report

import (
	"fmt"

	"example.com/shelf/config"
	"example.com/shelf/format"
	"example.com/shelf/store"
)

// PrintSummary 把汇总直接打印到标准输出：种类数、总件数、总价值、每页件数。
func PrintSummary(s *store.Store, cfg config.Cfg) {
	items := s.Items()
	total, value := 0, 0
	for _, it := range items {
		q := s.GetQty(it.SKU)
		total += q
		value += q * it.PriceCents
	}
	pages := (len(items) + cfg.PageSize - 1) / cfg.PageSize
	fmt.Printf("items: %d, total qty: %d\n", len(items), total)
	fmt.Printf("value: %s\n", format.Money(value, cfg.Currency))
	fmt.Printf("pages: %d (page size %d)\n", pages, cfg.PageSize)
}
