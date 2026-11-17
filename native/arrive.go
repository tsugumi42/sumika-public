package main

// 实时到的新消息按事件水位线进入；RevealState 跟消息身份存活，虚拟行重挂不重播。

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"time"
)

const (
	// maxFlying 在途表的上限。同时新到的行数有限（一次轮询最多 20 条）；真超了，最早开始的那条不再继续播，直接停在终点
	maxFlying = 64
)

type arrivals struct {
	seen   int64                // 水位线
	flying map[string]time.Time // 在途：消息 id → 开始时刻
	states map[string]*arrival
	span   time.Duration
}

type arrival struct {
	state chat.RevealState
	row   ui.Handle
}

func (r *arrivals) reveal(id string) *arrival {
	if r.states == nil {
		r.states = map[string]*arrival{}
	}
	if r.states[id] == nil {
		s := &arrival{}
		s.state.Start(r.flying[id])
		r.states[id] = s
	}
	return r.states[id]
}

// enter 推进事件水位线；同一条在途消息重建时沿用起点。
func (r *arrivals) enter(m *msg, now time.Time, reduceMotion bool) bool {
	fresh := (m.live && m.seq > r.seen && !m.noEnter) || m.enterNow
	r.seen = max(r.seen, m.seq)
	if t0, ok := r.flying[m.id]; ok {
		if now.Sub(t0) < r.span {
			return !reduceMotion
		}
		delete(r.flying, m.id)
		delete(r.states, m.id)
		return false
	}
	if !fresh || reduceMotion || r.span == 0 {
		return false
	}
	if r.flying == nil {
		r.flying = map[string]time.Time{}
	}
	if len(r.flying) >= maxFlying {
		var oldest string
		for id, t0 := range r.flying {
			if oldest == "" || t0.Before(r.flying[oldest]) {
				oldest = id
			}
		}
		delete(r.flying, oldest)
		delete(r.states, oldest)
	}
	r.flying[m.id] = now
	return true
}

// left 这一行的在途还剩多久；不在途返回 0。画的一方拿它去 c.After，到点重建一次，在途表才清得掉，
// 不能每帧续整个时长，否则清场期限会不断后移。
func (r *arrivals) left(id string, now time.Time) time.Duration {
	t0, ok := r.flying[id]
	if !ok {
		return 0
	}
	return max(r.span-now.Sub(t0), time.Millisecond)
}

// sweep 清掉过期的在途项。构建时清、切走会话时清（reset）都不够：滚出屏幕的行不再构建，它的项只有这里清得到
func (r *arrivals) sweep(now time.Time) {
	for id, t0 := range r.flying {
		if now.Sub(t0) >= r.span {
			delete(r.flying, id)
			delete(r.states, id)
		}
	}
}

// reset 换会话：水位线从头算，在途表清空
func (r *arrivals) reset() {
	r.seen = 0
	clear(r.flying)
	clear(r.states)
}
