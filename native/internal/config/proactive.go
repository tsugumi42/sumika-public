package config

import (
	"fmt"
	"strings"
	"time"
)

// Proactive 是主动联系的配置（规格-主动联系 第七节的配置键表；数字全是暂定值，看几天日志再调）。
// 默认关：新用户不会被打扰，真实配置里由主人打开。
type Proactive struct {
	Enabled    bool     `yaml:"enabled"`
	Characters []string `yaml:"characters"` // 对哪些角色开
	// Send：live 真发；think 只想不说（走完整流程、日志写"本想对谁说什么"，不发）
	Send string `yaml:"send"`

	BaseMinMinutes int `yaml:"baseMinMinutes"` // 基准间隔，在这两个数之间均匀随机
	BaseMaxMinutes int `yaml:"baseMaxMinutes"`
	MinGapMinutes  int `yaml:"minGapMinutes"` // 乘过系数以后的间隔下限
	MaxGapHours    int `yaml:"maxGapHours"`   // 上限

	HeatFactor float64 `yaml:"heatFactor"` // 主人回得热：系数乘它
	ColdFactor float64 `yaml:"coldFactor"` // 主人没理：系数乘它
	FactorMin  float64 `yaml:"factorMin"`
	FactorMax  float64 `yaml:"factorMax"`

	FollowUpMinutes int `yaml:"followUpMinutes"`
	MaxFollowUps    int `yaml:"maxFollowUps"`

	// Sleep 是默认睡觉区间（本机时区，HH:MM，可以跨午夜）；只是软信号，主人实际在活跃就以活跃为准
	Sleep struct {
		Start string `yaml:"start"`
		End   string `yaml:"end"`
	} `yaml:"sleep"`

	ActiveWithinMinutes   int `yaml:"activeWithinMinutes"`   // 这么久内有活跃迹象，算"在活跃"
	ChattingWithinMinutes int `yaml:"chattingWithinMinutes"` // 这么久内发过言，算"正在聊"，先不打扰
	DeferMinutes          int `yaml:"deferMinutes"`          // 被拦下以后顺延多久再看

	MaxThreadsPerDay  int `yaml:"maxThreadsPerDay"`
	MaxSendsPerThread int `yaml:"maxSendsPerThread"`
	MaxOpenThreads    int `yaml:"maxOpenThreads"`

	ClaimWindowMinutes int `yaml:"claimWindowMinutes"`
	ClaimGraceMinutes  int `yaml:"claimGraceMinutes"`
	ChatIdleMinutes    int `yaml:"chatIdleMinutes"`

	CloseAfterHours     int `yaml:"closeAfterHours"`
	PresenceWindowHours int `yaml:"presenceWindowHours"`

	SnoozeDefaultHours int `yaml:"snoozeDefaultHours"`
	SnoozeMaxDays      int `yaml:"snoozeMaxDays"`

	DecisionLogDays int  `yaml:"decisionLogDays"` // 日志保留天数，和运行页日志期限一致
	Notify          bool `yaml:"notify"`          // 本地消息窗口不在前台时弹系统通知
}

// DefaultProactive 是规格第七节配置表里的默认值
func DefaultProactive() Proactive {
	p := Proactive{
		Send:           "live",
		BaseMinMinutes: 120, BaseMaxMinutes: 180, MinGapMinutes: 45, MaxGapHours: 12,
		HeatFactor: 0.7, ColdFactor: 1.5, FactorMin: 0.5, FactorMax: 3,
		FollowUpMinutes: 20, MaxFollowUps: 2,
		ActiveWithinMinutes: 30, ChattingWithinMinutes: 10, DeferMinutes: 15,
		MaxThreadsPerDay: 4, MaxSendsPerThread: 3, MaxOpenThreads: 1,
		ClaimWindowMinutes: 10, ClaimGraceMinutes: 2, ChatIdleMinutes: 10,
		CloseAfterHours: 6, PresenceWindowHours: 24,
		SnoozeDefaultHours: 24, SnoozeMaxDays: 7,
		DecisionLogDays: 90, Notify: true,
	}
	p.Sleep.Start, p.Sleep.End = "01:00", "09:00"
	return p
}

// ParseClock 解析 "HH:MM"，返回从 0 点起的分钟数
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q 不是 HH:MM 写法", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (p Proactive) check() []string {
	var bad []string
	add := func(ok bool, f string, a ...any) {
		if !ok {
			bad = append(bad, "proactive."+fmt.Sprintf(f, a...))
		}
	}
	add(p.Send == "live" || p.Send == "think", "send：%q 不是 live / think", p.Send)
	seen := map[string]bool{}
	for i, id := range p.Characters {
		add(id != "" && id == strings.TrimSpace(id) && !seen[id], "characters[%d]：角色 ID 不能为空、含首尾空格或重复", i)
		seen[id] = true
	}
	for _, r := range []struct {
		name string
		n    int
		min  int
		max  int
	}{
		{"baseMinMinutes", p.BaseMinMinutes, 1, 10080}, {"baseMaxMinutes", p.BaseMaxMinutes, 1, 10080},
		{"minGapMinutes", p.MinGapMinutes, 1, 10080}, {"maxGapHours", p.MaxGapHours, 1, 720},
		{"followUpMinutes", p.FollowUpMinutes, 1, 1440}, {"maxFollowUps", p.MaxFollowUps, 0, 20},
		{"activeWithinMinutes", p.ActiveWithinMinutes, 1, 1440}, {"chattingWithinMinutes", p.ChattingWithinMinutes, 1, 1440},
		{"deferMinutes", p.DeferMinutes, 1, 1440},
		{"maxThreadsPerDay", p.MaxThreadsPerDay, 1, 100}, {"maxSendsPerThread", p.MaxSendsPerThread, 1, 100}, {"maxOpenThreads", p.MaxOpenThreads, 1, 10},
		{"claimWindowMinutes", p.ClaimWindowMinutes, 1, 1440}, {"claimGraceMinutes", p.ClaimGraceMinutes, 1, 1440}, {"chatIdleMinutes", p.ChatIdleMinutes, 1, 1440},
		{"closeAfterHours", p.CloseAfterHours, 1, 720}, {"presenceWindowHours", p.PresenceWindowHours, 1, 720},
		{"snoozeDefaultHours", p.SnoozeDefaultHours, 1, 24 * 30}, {"snoozeMaxDays", p.SnoozeMaxDays, 1, 365},
		{"decisionLogDays", p.DecisionLogDays, 1, 3650},
	} {
		add(r.n >= r.min && r.n <= r.max, "%s：%d 不在 %d～%d", r.name, r.n, r.min, r.max)
	}
	add(p.BaseMinMinutes <= p.BaseMaxMinutes, "baseMinMinutes：不能大于 baseMaxMinutes")
	add(p.MinGapMinutes <= p.MaxGapHours*60, "minGapMinutes：不能大于 maxGapHours")
	add(p.FactorMin > 0 && p.FactorMin <= 1, "factorMin：%v 不在 (0, 1]", p.FactorMin)
	add(p.FactorMax >= 1 && p.FactorMax <= 100, "factorMax：%v 不在 [1, 100]", p.FactorMax)
	add(p.HeatFactor > 0 && p.HeatFactor < 1, "heatFactor：%v 不在 (0, 1)", p.HeatFactor)
	add(p.ColdFactor > 1 && p.ColdFactor <= 100, "coldFactor：%v 不在 (1, 100]", p.ColdFactor)
	add(p.SnoozeDefaultHours <= p.SnoozeMaxDays*24, "snoozeDefaultHours：不能大于 snoozeMaxDays")
	s, err1 := ParseClock(p.Sleep.Start)
	e, err2 := ParseClock(p.Sleep.End)
	add(err1 == nil, "sleep.start：%v", err1)
	add(err2 == nil, "sleep.end：%v", err2)
	if err1 == nil && err2 == nil {
		add(s != e, "sleep：start 和 end 不能相同")
	}
	return bad
}

// validateProactiveRaw 拒绝整数键写成小数：YAML 解析会悄悄把 1.5 截成 1。只在配置输入边界查。
func validateProactiveRaw(raw map[string]any) error {
	m, _ := raw["proactive"].(map[string]any)
	for key, v := range m {
		switch key {
		case "enabled", "characters", "send", "sleep", "notify", "heatFactor", "coldFactor", "factorMin", "factorMax":
			continue
		}
		if f, ok := v.(float64); ok && f != float64(int64(f)) {
			return fmt.Errorf("proactive.%s：必须是整数", key)
		}
	}
	return nil
}
