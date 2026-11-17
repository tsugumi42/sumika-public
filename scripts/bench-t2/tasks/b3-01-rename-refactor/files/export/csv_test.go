package export

import (
	"bytes"
	"strings"
	"testing"

	"example.com/shelf/config"
	"example.com/shelf/store"
)

func TestWriteCSV(t *testing.T) {
	s := store.New()
	s.Add(store.Item{SKU: "A", Name: "a", Qty: 2, PriceCents: 150})
	var buf bytes.Buffer
	if err := WriteCSV(&buf, s, config.Cfg{Currency: "CNY"}); err != nil {
		t.Fatal(err)
	}
	want := "sku,name,qty,price\nA,a,2,CNY 150\n"
	if got := strings.ReplaceAll(buf.String(), "\r\n", "\n"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
