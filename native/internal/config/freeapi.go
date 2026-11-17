package config

import (
	"fmt"
	"regexp"
)

// 免费接口的设置（规格-免费接口 第七节，ADR-082 第 3、4 条）：探测、每个工具每人每群的次数、提供方上限。
// 次数 0 = 不限。键 freeApi 取代了设置工具线原先按场合分池的“每天次数”（freeApi.weather.ownerDaily/othersDaily 和 freequota 包，已删）。

// FreeAPILimit 是一个工具每个人、每个群的次数
type FreeAPILimit struct {
	PerActorDaily         int `yaml:"perActorDaily"`
	PerConversationHourly int `yaml:"perConversationHourly"`
}

// FreeAPIProviderCap 是手写覆盖的某个提供方上限；0 = 用注册表里的
type FreeAPIProviderCap struct {
	DailyCap  int `yaml:"dailyCap"`
	PerMinute int `yaml:"perMinute"`
}

// FreeAPI 是 freeApi 一节
type FreeAPI struct {
	// NetworkRegion 是这台机器所在的网络区域，节假日、汇率这类看网络线路的类别用：cn 或 global
	NetworkRegion string `yaml:"networkRegion"`
	Probe         struct {
		Enabled       bool `yaml:"enabled"`
		IntervalHours int  `yaml:"intervalHours"`
	} `yaml:"probe"`
	Limits struct {
		Weather      FreeAPILimit `yaml:"weather"`
		ExchangeRate FreeAPILimit `yaml:"exchangeRate"`
		Calendar     FreeAPILimit `yaml:"calendar"`
	} `yaml:"limits"`
	Providers map[string]FreeAPIProviderCap `yaml:"providers"`
}

// 探测间隔的范围（小时）：用户说“半天到一天”，放宽到 1 到 48
const (
	FreeAPIMinProbeHours = 1
	FreeAPIMaxProbeHours = 48
	freeAPIMaxCount      = 100000
)

// DefaultFreeAPI 是起步值（用户 2026-11-09 定的）：每人每天 20、每群每小时 15，主人不限；探测默认开、12 小时一次。
// 天气和汇率限，日历默认不限（本地算加缓存，联网由提供方额度兜住）
func DefaultFreeAPI() FreeAPI {
	var f FreeAPI
	f.NetworkRegion = "cn"
	f.Probe.Enabled, f.Probe.IntervalHours = true, 12
	f.Limits.Weather = FreeAPILimit{PerActorDaily: 20, PerConversationHourly: 15}
	f.Limits.ExchangeRate = FreeAPILimit{PerActorDaily: 20, PerConversationHourly: 15}
	return f
}

var freeAPIID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (f FreeAPI) check() []string {
	var bad []string
	if f.NetworkRegion != "" && f.NetworkRegion != "cn" && f.NetworkRegion != "global" {
		bad = append(bad, "freeApi.networkRegion：要写 cn 或 global")
	}
	if h := f.Probe.IntervalHours; h < FreeAPIMinProbeHours || h > FreeAPIMaxProbeHours {
		bad = append(bad, fmt.Sprintf("freeApi.probe.intervalHours：%d 不在 %d～%d", h, FreeAPIMinProbeHours, FreeAPIMaxProbeHours))
	}
	for name, l := range map[string]FreeAPILimit{"weather": f.Limits.Weather, "exchangeRate": f.Limits.ExchangeRate, "calendar": f.Limits.Calendar} {
		if l.PerActorDaily < 0 || l.PerActorDaily > freeAPIMaxCount {
			bad = append(bad, fmt.Sprintf("freeApi.limits.%s.perActorDaily：%d 不在 0～%d", name, l.PerActorDaily, freeAPIMaxCount))
		}
		if l.PerConversationHourly < 0 || l.PerConversationHourly > freeAPIMaxCount {
			bad = append(bad, fmt.Sprintf("freeApi.limits.%s.perConversationHourly：%d 不在 0～%d", name, l.PerConversationHourly, freeAPIMaxCount))
		}
	}
	for id, c := range f.Providers {
		if !freeAPIID.MatchString(id) {
			bad = append(bad, "freeApi.providers：接口 id 写法不对")
		}
		if c.DailyCap < 0 || c.PerMinute < 0 || c.DailyCap > 100_000_000 || c.PerMinute > 100_000_000 {
			bad = append(bad, "freeApi.providers."+id+"：上限要写 0 或正整数")
		}
	}
	return bad
}
