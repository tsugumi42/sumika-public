// Package report 把库存打印成人看的文字。
package report

import (
	"fmt"
	"io"

	"example.com/shelf/config"
	"example.com/shelf/store"
)

// PrintLowStock 把数量小于 cfg.LowStockThreshold 的物品写进 w，一行一件。
// 数量用 store.GetQty 取，和别处保持一样的口径。
func PrintLowStock(w io.Writer, s *store.Store, cfg config.Cfg) {
	n := 0
	for _, it := range s.Items() {
		q := s.GetQty(it.SKU)
		if q < cfg.LowStockThreshold {
			fmt.Fprintf(w, "low: %s %s x%d\n", it.SKU, it.Name, q)
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "low: none")
	}
}
