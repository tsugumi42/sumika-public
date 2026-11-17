package report

import (
	"bytes"
	"strings"
	"testing"

	"example.com/shelf/config"
	"example.com/shelf/store"
)

func TestLowStockListsOnlyLow(t *testing.T) {
	s := store.New()
	s.Add(store.Item{SKU: "A", Name: "a", Qty: 2})
	s.Add(store.Item{SKU: "B", Name: "b", Qty: 9})
	var buf bytes.Buffer
	PrintLowStock(&buf, s, config.Cfg{LowStockThreshold: 5})
	out := buf.String()
	if !strings.Contains(out, "low: A a x2") || strings.Contains(out, "B") {
		t.Errorf("输出不对：%q", out)
	}
}

func TestLowStockNone(t *testing.T) {
	var buf bytes.Buffer
	PrintLowStock(&buf, store.New(), config.Cfg{LowStockThreshold: 5})
	if strings.TrimSpace(buf.String()) != "low: none" {
		t.Errorf("没有缺货时应该写 low: none，拿到 %q", buf.String())
	}
}

func TestThresholdIsExclusive(t *testing.T) {
	s := store.New()
	s.Add(store.Item{SKU: "A", Name: "a", Qty: 5})
	var buf bytes.Buffer
	PrintLowStock(&buf, s, config.Cfg{LowStockThreshold: 5})
	if strings.Contains(buf.String(), "A") {
		t.Error("数量等于门槛不算缺货")
	}
}
