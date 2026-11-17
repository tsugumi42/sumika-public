package config

import (
	"fmt"
	"slices"
)

// Reminders 是提醒的配置（规格-提醒 第十四节）。默认开：只有听众是主人的场合才看得到工具（ADR-330 第 1 条）；发 QQ 默认关。
type Reminders struct {
	Enabled bool `yaml:"enabled"`
	// MaxActive 是每个角色同时存在的条数，她建新的时数
	MaxActive int `yaml:"maxActive"`
	// MinIntervalMinutes 是她建或改周期提醒的最短间隔；线里建一次性的最早也是这么久以后。不让提醒绕过主动联系的节奏
	MinIntervalMinutes int `yaml:"minIntervalMinutes"`
	// ThreadMaxActive 是线里建的、没结束的最多几条
	ThreadMaxActive int `yaml:"threadMaxActive"`
	// QQ 为真才能发到主人 QQ 私聊
	QQ bool `yaml:"qq"`
	// DefaultTargets 是没写 targets 的提醒发到哪：local、qq
	DefaultTargets []string `yaml:"defaultTargets"`
}

// DefaultReminders 是规格第十四节的默认值
func DefaultReminders() Reminders {
	return Reminders{Enabled: true, MaxActive: 20, MinIntervalMinutes: 60, ThreadMaxActive: 3, DefaultTargets: []string{"local"}}
}

func (r Reminders) check() []string {
	var bad []string
	add := func(ok bool, f string, a ...any) {
		if !ok {
			bad = append(bad, "reminders."+fmt.Sprintf(f, a...))
		}
	}
	add(r.MaxActive >= 1 && r.MaxActive <= 1000, "maxActive：%d 不在 1～1000", r.MaxActive)
	add(r.MinIntervalMinutes >= 1 && r.MinIntervalMinutes <= 10080, "minIntervalMinutes：%d 不在 1～10080", r.MinIntervalMinutes)
	add(r.ThreadMaxActive >= 0 && r.ThreadMaxActive <= r.MaxActive, "threadMaxActive：%d 不在 0～maxActive", r.ThreadMaxActive)
	add(len(r.DefaultTargets) > 0, "defaultTargets：至少写一个（local、qq）")
	for i, t := range r.DefaultTargets {
		add(t == "local" || t == "qq", "defaultTargets[%d]：%q 不是 local / qq", i, t)
	}
	add(r.QQ || !slices.Contains(r.DefaultTargets, "qq"), "defaultTargets：含 qq 时要先把 reminders.qq 打开")
	return bad
}
