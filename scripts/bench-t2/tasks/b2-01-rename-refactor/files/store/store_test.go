package store

import "testing"

func TestQuantity(t *testing.T) {
	s := New()
	s.Add(Item{SKU: "X-1", Name: "x", Qty: 7})
	if got := s.GetQty("X-1"); got != 7 {
		t.Errorf("GetQty = %d，想要 7", got)
	}
	if got := s.GetQty("none"); got != 0 {
		t.Errorf("没有的物品应该是 0，拿到 %d", got)
	}
}

func TestItemsSorted(t *testing.T) {
	s := New()
	s.Add(Item{SKU: "B"})
	s.Add(Item{SKU: "A"})
	s.Add(Item{SKU: "C"})
	got := s.Items()
	if len(got) != 3 || got[0].SKU != "A" || got[2].SKU != "C" {
		t.Errorf("没排好序：%+v", got)
	}
}

func TestAddOverwrites(t *testing.T) {
	s := New()
	s.Add(Item{SKU: "A", Qty: 1})
	s.Add(Item{SKU: "A", Qty: 2})
	if len(s.Items()) != 1 || s.GetQty("A") != 2 {
		t.Error("同一个 SKU 应该被覆盖")
	}
}
