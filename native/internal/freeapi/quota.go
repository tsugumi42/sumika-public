package freeapi

import "time"

// 次数（规格-免费接口 第六节）。两类计数：
//   - 提供方：每天、每分钟，只在真的发出请求时扣（含探测和回退里的每次尝试）；
//   - 人和场合：每人每天、每群每小时，按逻辑调用扣（缓存命中也扣），在联网之前扣一次，回退不再扣。
// 状态放 state.json，用户号和会话号只存哈希。

// Who 是一次工具调用的身份。Owner 由宿主核对过，不在这里判
type Who struct {
	Owner              bool
	Group              bool // 群场合：每群每小时才适用
	ActorKind, ActorID string
	Conversation       string
}

// Denial 是被拒的说明
type Denial struct {
	Denied bool
	Reason string // "actor"：这个人今天用够了；"group"：这个群这个小时用够了
	Limit  int
}

// counters 里的键
func provDayKey(id string) string   { return "p:" + id }
func toolUseKey(tool string) string { return "u:" + tool }
func actorKey(tool string, w Who) string {
	// 身份缺失归到同一个“未知”桶按普通人限，不是不限
	kind, id := w.ActorKind, w.ActorID
	if kind == "" || id == "" {
		kind, id = "unknown", "unknown"
	}
	return "a:" + tool + ":" + hashID(kind, id)
}
func groupKey(tool string, w Who) string { return "g:" + tool + ":" + hashID(w.Conversation) }

// used 读一个计数在当前窗口的值（过了窗口就是 0）。调用时持锁
func (e *Engine) used(key, window string) int {
	if c, ok := e.st.Counters[key]; ok && c.W == window {
		return c.N
	}
	return 0
}

func (e *Engine) add(key, window string, n int) {
	c := e.st.Counters[key]
	if c.W != window {
		c = counter{W: window}
	}
	c.N += n
	e.st.Counters[key] = c
}

// Allow 占一次“人”的次数。返回的 Denial.Denied 为真就是被拒，什么都没扣。
// 主人不计、不拒（总用量照记，给页面看）；每人和每群两个维度先都检查、再一起扣
func (e *Engine) Allow(tool string, w Who) Denial {
	s := e.o.Settings()
	lim := s.Limits[tool]
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	day, hour := e.dayKey(now), e.hourKey(now)
	if w.Owner {
		e.add(toolUseKey(tool), day, 1)
		e.save()
		return Denial{}
	}
	ak, gk := actorKey(tool, w), groupKey(tool, w)
	if lim.PerActorDaily > 0 && e.used(ak, day) >= lim.PerActorDaily {
		return Denial{Denied: true, Reason: "actor", Limit: lim.PerActorDaily}
	}
	if w.Group && lim.PerConversationHourly > 0 && e.used(gk, hour) >= lim.PerConversationHourly {
		return Denial{Denied: true, Reason: "group", Limit: lim.PerConversationHourly}
	}
	e.add(ak, day, 1)
	if w.Group {
		e.add(gk, hour, 1)
	}
	e.add(toolUseKey(tool), day, 1)
	e.save()
	return Denial{}
}

// ToolUsedToday 是这个工具今天一共被调了几次（页面显示）
func (e *Engine) ToolUsedToday(tool string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.used(toolUseKey(tool), e.dayKey(e.now()))
}

// caps 是这个提供方现在的每天、每分钟上限（配置覆盖优先）
func (e *Engine) caps(p Provider, s Settings) (day, minute int) {
	day, minute = p.DefaultCaps()
	if c, ok := s.Providers[p.ID]; ok {
		if c.DailyCap > 0 {
			day = c.DailyCap
		}
		if c.PerMinute > 0 {
			minute = c.PerMinute
		}
	}
	return day, minute
}

// quotaLeft 是这个提供方今天还剩多少比例（0～1）和能不能再请求。调用时持锁
func (e *Engine) quotaLeft(p Provider, s Settings, now time.Time) (float64, bool) {
	day, minute := e.caps(p, s)
	used := e.used(provDayKey(p.ID), e.dayKey(now))
	if used >= day {
		return 0, false
	}
	n := 0
	for _, t := range e.minutes[p.ID] {
		if now.Sub(t) < time.Minute {
			n++
		}
	}
	if n >= minute {
		return float64(day-used) / float64(day), false
	}
	return float64(day-used) / float64(day), true
}

// takeProvider 真要发请求前占提供方的次数；上限到了返回 false（不占）
func (e *Engine) takeProvider(p Provider, s Settings) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if _, ok := e.quotaLeft(p, s, now); !ok {
		return false
	}
	kept := e.minutes[p.ID][:0]
	for _, t := range e.minutes[p.ID] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	e.minutes[p.ID] = append(kept, now)
	e.add(provDayKey(p.ID), e.dayKey(now), 1)
	e.save()
	return true
}

// ProviderUsedToday 是这个提供方今天已经发了几次请求
func (e *Engine) ProviderUsedToday(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.used(provDayKey(id), e.dayKey(e.now()))
}
