package config

import (
	"sort"
	"strings"

	"sumika-native/internal/applog"
)

// LogSettings 是日志配置（规格-日志分级第六节）：全局最低级别和按模块覆盖。调试级别默认不写。
// 模块名按 applog 的封闭名单校验（applog 只依赖 filelog 和 paths，不会成环）。
type LogSettings struct {
	Level   string            `yaml:"level"`
	Modules map[string]string `yaml:"modules"`
}

// DefaultLogSettings 是默认：INFO，不按模块覆盖
func DefaultLogSettings() LogSettings {
	return LogSettings{Level: "info", Modules: map[string]string{}}
}

// check 认四个级别词，模块名必须在 applog 的名单里（写错的名字会让覆盖悄悄不生效，所以报出来）
func (l LogSettings) check() []string {
	switch strings.ToLower(l.Level) {
	case "debug", "info", "warn", "error":
	default:
		return []string{"logs.level 要是 debug、info、warn、error 之一（现在是 " + l.Level + "）"}
	}
	var bad []string
	names := make([]string, 0, len(l.Modules))
	for name := range l.Modules {
		names = append(names, name)
	}
	sort.Strings(names) // 报错顺序固定
	for _, name := range names {
		lvl := l.Modules[name]
		if _, ok := applog.ByName(name); !ok {
			bad = append(bad, "logs.modules."+name+" 不是日志模块名（可用："+strings.Join(applog.Names(), "、")+"）")
		}
		switch strings.ToLower(lvl) {
		case "debug", "info", "warn", "error":
		default:
			bad = append(bad, "logs.modules."+name+" 要是 debug、info、warn、error 之一（现在是 "+lvl+"）")
		}
	}
	return bad
}
