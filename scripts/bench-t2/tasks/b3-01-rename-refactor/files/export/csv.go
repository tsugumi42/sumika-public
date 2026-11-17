// Package export 把库存导出成别的格式。
package export

import (
	"encoding/csv"
	"io"
	"strconv"

	"example.com/shelf/config"
	"example.com/shelf/store"
)

// WriteCSV 把全部物品写成 CSV：sku,name,qty,price。价格按 cfg.Currency 加前缀。
// 数量取自 store.GetQty；设置类型见 config.Cfg。
func WriteCSV(w io.Writer, s *store.Store, cfg config.Cfg) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"sku", "name", "qty", "price"}); err != nil {
		return err
	}
	for _, it := range s.Items() {
		price := cfg.Currency + " " + strconv.Itoa(it.PriceCents)
		if err := cw.Write([]string{it.SKU, it.Name, strconv.Itoa(s.GetQty(it.SKU)), price}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
