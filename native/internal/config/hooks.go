package config

import "fmt"

// Hooks 是用户钩子的参数（规格-用户钩子 第八节）。钩子本身写在 hooks.json 里，不在这里。
// 默认开着，但没写 hooks.json 时什么也不跑。
type Hooks struct {
	Enabled          bool `yaml:"enabled"`
	TimeoutSeconds   int  `yaml:"timeoutSeconds"`   // 单条命令默认时限，1～600
	MaxOutputBytes   int  `yaml:"maxOutputBytes"`   // 单条命令给模型的字节上限，256～65536
	MaxParallel      int  `yaml:"maxParallel"`      // 全局同时跑几条，1～16
	StopMaxContinues int  `yaml:"stopMaxContinues"` // 一轮最多因为 Stop 钩子续跑几次，0～20；0 只跑不续
}

// DefaultHooks 是钩子参数的默认值
func DefaultHooks() Hooks {
	return Hooks{Enabled: true, TimeoutSeconds: 60, MaxOutputBytes: 8192, MaxParallel: 4, StopMaxContinues: 3}
}

func (h Hooks) check() []string {
	var bad []string
	for _, r := range []struct {
		key       string
		v, lo, hi int
	}{
		{"hooks.timeoutSeconds", h.TimeoutSeconds, 1, 600},
		{"hooks.maxOutputBytes", h.MaxOutputBytes, 256, 65536},
		{"hooks.maxParallel", h.MaxParallel, 1, 16},
		{"hooks.stopMaxContinues", h.StopMaxContinues, 0, 20},
	} {
		if r.v < r.lo || r.v > r.hi {
			bad = append(bad, fmt.Sprintf("%s：%d 不在 %d～%d", r.key, r.v, r.lo, r.hi))
		}
	}
	return bad
}
