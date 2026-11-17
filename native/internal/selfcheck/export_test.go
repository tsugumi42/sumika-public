package selfcheck

// DayFromLive 给外部测试：把一组计数器当前的值当作"当天的数"，造 Env.Day
func DayFromLive(c *Counters) map[string]int64 {
	out := map[string]int64{}
	s := c.snapshot()
	for i := Counter(0); i < numCounters; i++ {
		out[i.Name()] = s[i]
	}
	return out
}
