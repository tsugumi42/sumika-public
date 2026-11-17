// Package store 存货架上的物品。
package store

import "sort"

// Item 是一种物品。
type Item struct {
	SKU        string
	Name       string
	Qty        int
	PriceCents int
}

// Store 按 SKU 存物品。
type Store struct {
	items map[string]Item
}

// New 返回一个空的 Store。
func New() *Store {
	return &Store{items: map[string]Item{}}
}

// Add 放入一种物品；SKU 已有就整个覆盖。
func (s *Store) Add(it Item) {
	s.items[it.SKU] = it
}

// GetQty 返回 SKU 对应物品的数量，没有这个物品返回 0。
func (s *Store) GetQty(sku string) int {
	return s.items[sku].Qty
}

// Items 按 SKU 排好序返回全部物品。
func (s *Store) Items() []Item {
	out := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out
}
