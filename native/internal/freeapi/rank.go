package freeapi

import (
	"sort"
	"time"
)

// 打分权重：起步值，没有流量依据，以后看日志再调（规格-免费接口 第四节）
const (
	wSuccess = 0.5
	wLatency = 0.2
	wRegion  = 0.2
	wQuota   = 0.1
	// 同档里分数在最高分这个比例以内的随机取一个排前面，把流量分散开
	tieBuffer = 0.10
)

type candidate struct {
	p      Provider
	route  string
	tier   int
	score  float64
	pinned bool
	n      int // 有效样本数；0 = 没数据
}

// eligible 是区域对得上、有人实现、没被人关、不是“需处理”的提供方数（不看退避和额度），用来判“唯一候选”。调用时持锁
func (e *Engine) eligible(cat, region string, has func(string) bool, s Settings) int {
	n := 0
	for _, p := range e.cat.Providers {
		if p.Category != cat || !p.Serves(region) || !has(p.ID) || e.st.Manual[p.ID].Off {
			continue
		}
		if ps := e.st.Providers[stKey(p.ID, routeOf(p, s))]; ps != nil && ps.Attention != "" {
			continue
		}
		n++
	}
	return n
}

// rank 按规格排好能用的提供方；全都在退避时 forced 非空（只有一个，最早解除的）。调用时持锁
func (e *Engine) rank(cat, region string, has func(string) bool, s Settings, now time.Time) (list []candidate, forced *candidate) {
	var all, waiting []candidate
	for _, p := range e.cat.Providers {
		if p.Category != cat || !p.Serves(region) || !has(p.ID) {
			continue
		}
		route := routeOf(p, s)
		st, _ := e.stateOf(p, route, now)
		if st == StateOff || st == StateAttention {
			continue
		}
		left, okQ := e.quotaLeft(p, s, now)
		if !okQ {
			continue
		}
		c := candidate{p: p, route: route, tier: p.TierFor(region), pinned: e.st.Manual[p.ID].Pin}
		var samples []sample
		if ps := e.st.Providers[stKey(p.ID, route)]; ps != nil {
			samples = ps.Samples
		}
		rate, n := successRate(samples, now) // 没样本时正好是先验 0.8，不凭空编分数
		c.n = n
		c.score = wSuccess*rate + wRegion*p.RegionMatch(region) + wQuota*left
		if st == StateBackoff || st == StatePaused {
			waiting = append(waiting, c)
			continue
		}
		all = append(all, c)
	}
	e.addLatency(all, now)
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.pinned != b.pinned {
			return a.pinned
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		return a.score > b.score
	})
	// 同档里分数贴近最高分的随机一个排前面
	if len(all) > 1 && !all[0].pinned {
		top := 1
		for top < len(all) && all[top].tier == all[0].tier && all[top].score >= all[0].score*(1-tieBuffer) {
			top++
		}
		if top > 1 {
			k := e.o.Rand(top)
			all[0], all[k] = all[k], all[0]
		}
	}
	if len(all) == 0 && len(waiting) > 0 {
		best := waiting[0]
		for _, c := range waiting[1:] {
			if e.untilOf(c).Before(e.untilOf(best)) {
				best = c
			}
		}
		return nil, &best
	}
	return all, nil
}

func (e *Engine) untilOf(c candidate) time.Time {
	ps := e.st.Providers[stKey(c.p.ID, c.route)]
	if ps == nil {
		return time.Time{}
	}
	u := ps.OpenUntil
	if ps.PausedUntil > u {
		u = ps.PausedUntil
	}
	return time.UnixMilli(u)
}

// addLatency 加延迟分：中位延迟和本类别最快者的比，没数据按 1（不凭空编分数）
func (e *Engine) addLatency(list []candidate, now time.Time) {
	med := make([]int, len(list))
	best := 0
	for i, c := range list {
		if ps := e.st.Providers[stKey(c.p.ID, c.route)]; ps != nil {
			med[i] = medianMS(ps.Samples, now)
		}
		if med[i] > 0 && (best == 0 || med[i] < best) {
			best = med[i]
		}
	}
	for i := range list {
		r := 1.0
		if med[i] > 0 && best > 0 {
			r = float64(best) / float64(med[i])
		}
		list[i].score += wLatency * r
	}
}

// strongTryAllowed：全都在退避时的“强试”，每个类别每 30 秒最多放一次。调用时持锁
func (e *Engine) strongTryAllowed(cat string, now time.Time) bool {
	if last, ok := e.force[cat]; ok && now.Sub(last) < 30*time.Second {
		return false
	}
	e.force[cat] = now
	return true
}
