package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
)

// LinkRate 是链接取数自己的节奏（linkreader.RateOptions）。主人 2026-11-13：这类额度不该在程序里卡死，
// 平台自己不拦就照发；额度满了只排队等空位，平台真回了限流（429、B 站风控）才冷却、才失败。
type LinkRate struct {
	// PerMinute 是每个平台每分钟最多发几次请求（默认 60）；用满了排队等，不算失败
	PerMinute int `yaml:"perMinute"`
	// Platforms 按平台覆盖 PerMinute，键是平台名（bilibili、pixiv、x……）。默认只给 B 站放慢到 10：
	// 它按 IP 风控，机器人连发会连累同一个家庭网络里主人自己刷 B 站
	Platforms map[string]int `yaml:"platforms"`
	// MaxWaitSeconds 是额度用满时最多等几秒（默认 45，0 = 不等、额度满就失败）；回合时限先到就不等
	MaxWaitSeconds int `yaml:"maxWaitSeconds"`
}

const (
	DefaultLinkRatePerMinute = 60
	DefaultLinkRateMaxWait   = 45
)

func defaultLinkRate() LinkRate {
	return LinkRate{PerMinute: DefaultLinkRatePerMinute, Platforms: map[string]int{"bilibili": 10}, MaxWaitSeconds: DefaultLinkRateMaxWait}
}

func (r LinkRate) clone() LinkRate {
	if r.Platforms != nil {
		r.Platforms = maps.Clone(r.Platforms)
	}
	return r
}

var ratePlatformName = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

func (r LinkRate) check() []string {
	var bad []string
	if r.PerMinute < 1 || r.PerMinute > 600 {
		bad = append(bad, "linkReader.rate.perMinute：必须在1～600次")
	}
	if r.MaxWaitSeconds < 0 || r.MaxWaitSeconds > 120 {
		bad = append(bad, "linkReader.rate.maxWaitSeconds：必须在0～120秒")
	}
	for _, k := range slices.Sorted(maps.Keys(r.Platforms)) {
		if !ratePlatformName.MatchString(k) {
			bad = append(bad, fmt.Sprintf("linkReader.rate.platforms：平台名 %q 只能是 1～32 个小写字母或数字", k))
			continue
		}
		if n := r.Platforms[k]; n < 1 || n > 600 {
			bad = append(bad, fmt.Sprintf("linkReader.rate.platforms.%s：必须在1～600次", k))
		}
	}
	return bad
}
