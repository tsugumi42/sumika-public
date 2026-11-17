package web

import (
	"slices"
	"sync"
)

// Order 是 auto 时搜索和抓网页各家的先后。设置页拖完顺序会立刻写进这里，不用重启；对 nil 安全。
type Order struct {
	mu            sync.RWMutex
	search, fetch []string
}

func NewOrder(search, fetch []string) *Order {
	return &Order{search: slices.Clone(search), fetch: slices.Clone(fetch)}
}

func (o *Order) Search() []string {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return slices.Clone(o.search)
}

func (o *Order) Fetch() []string {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return slices.Clone(o.fetch)
}

func (o *Order) SetSearch(ids []string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.search = slices.Clone(ids)
	o.mu.Unlock()
}

func (o *Order) SetFetch(ids []string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.fetch = slices.Clone(ids)
	o.mu.Unlock()
}
