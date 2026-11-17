package freeapi

import (
	"fmt"
	"time"
)

// 给页面看的快照和人手动的几个操作（置顶、关闭、清退避）。人工状态和自动状态分开存：手动关的绝不被探测复活

// ProviderView 是页面上一行
type ProviderView struct {
	ID, Name, Category  string
	Grey                bool
	Network             string
	Regions             []string
	State               State
	Until               time.Time // 退避、暂停到几点
	Reason              Kind      // 需处理的原因
	Pinned, Off         bool
	LastCheck           time.Time
	LastKind            Kind
	Success24h          float64 // 没有样本是 -1
	MedianMS            int
	UsedToday, DailyCap int
}

// CategoryView 是一类
type CategoryView struct {
	ID, Name  string
	Providers []ProviderView
}

// Snapshot 是现在的全貌，按注册表的顺序
func (e *Engine) Snapshot() []CategoryView {
	s := e.o.Settings()
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := make([]CategoryView, 0, len(e.cat.Categories))
	for _, c := range e.cat.Categories {
		cv := CategoryView{ID: c.ID, Name: c.Name}
		for _, p := range e.cat.Providers {
			if p.Category != c.ID {
				continue
			}
			route := routeOf(p, s)
			st, until := e.stateOf(p, route, now)
			day, _ := e.caps(p, s)
			v := ProviderView{ID: p.ID, Name: p.Name, Category: p.Category, Grey: p.Grey, Network: p.Network, Regions: p.Regions,
				State: st, Until: until, Pinned: e.st.Manual[p.ID].Pin, Off: e.st.Manual[p.ID].Off, Success24h: -1,
				UsedToday: e.used(provDayKey(p.ID), e.dayKey(now)), DailyCap: day}
			if ps := e.st.Providers[stKey(p.ID, route)]; ps != nil {
				v.Reason = ps.Attention
				if ps.LastCheck > 0 {
					v.LastCheck, v.LastKind = time.UnixMilli(ps.LastCheck), ps.LastKind
				}
				v.Success24h = plainRate(ps.Samples, now)
				v.MedianMS = medianMS(ps.Samples, now)
			}
			if st == StateOK && v.UsedToday >= day {
				v.State = StateQuota
			}
			cv.Providers = append(cv.Providers, v)
		}
		out = append(out, cv)
	}
	return out
}

// Events 是最近的状态变化，新的在后
func (e *Engine) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.st.Events...)
}

func (e *Engine) known(id string) (Provider, error) {
	p, ok := e.byID[id]
	if !ok {
		return Provider{}, fmt.Errorf("没有这个接口")
	}
	return p, nil
}

// SetPinned 置顶：同一类别只留一个置顶。置顶只是排在最前，挂了照样回退
func (e *Engine) SetPinned(id string, pin bool) error {
	p, err := e.known(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if pin {
		for _, o := range e.cat.Providers {
			if o.Category == p.Category && o.ID != id {
				m := e.st.Manual[o.ID]
				m.Pin = false
				e.setManual(o.ID, m)
			}
		}
	}
	m := e.st.Manual[id]
	m.Pin = pin
	e.setManual(id, m)
	e.event(id, map[bool]string{true: "被置顶", false: "取消置顶"}[pin])
	e.save()
	return nil
}

// SetOff 手动关闭或打开。手动关的探测不碰，也不会被自动恢复
func (e *Engine) SetOff(id string, off bool) error {
	if _, err := e.known(id); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.st.Manual[id]
	m.Off = off
	e.setManual(id, m)
	e.event(id, map[bool]string{true: "被手动关闭", false: "被手动打开"}[off])
	e.save()
	return nil
}

func (e *Engine) setManual(id string, m manualState) {
	if m == (manualState{}) {
		delete(e.st.Manual, id)
		return
	}
	e.st.Manual[id] = m
}

// ClearBackoff 清掉退避和暂停（不碰“需处理”，那只能靠检测成功解除）
func (e *Engine) ClearBackoff(id string) error {
	p, err := e.known(id)
	if err != nil {
		return err
	}
	s := e.o.Settings()
	e.mu.Lock()
	defer e.mu.Unlock()
	if ps := e.st.Providers[stKey(id, routeOf(p, s))]; ps != nil {
		ps.Streak, ps.BackoffN, ps.OpenUntil, ps.PausedUntil = 0, 0, 0, 0
	}
	e.event(id, "手动清除退避")
	e.save()
	return nil
}
