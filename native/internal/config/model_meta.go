package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// ModelMetaConfig 是模型窗口和单次输出的来源配置（ADR-334）：
// 取值顺序是 用户覆盖（overrides）> 服务商或网关返回 > models.dev 本地缓存（只补空）> 保守默认并标"未知"。
type ModelMetaConfig struct {
	ModelsDev ModelsDevConfig `yaml:"modelsDev"`
	// Overrides 是用户对某个模型写的覆盖，键是模型 ID（和模型列表里一样，如 auto/deepseek-flash）；0 = 这一项不覆盖。
	// 将来模型配置规格（服务商、模型目录）落地后并入 providers.<id>.models.<模型>
	Overrides map[string]ModelOverride `yaml:"overrides"`
}

// ModelsDevConfig 管 models.dev 这一层：联网取全量目录，缓存到数据目录的 cache/models-dev.json，
// 离线用缓存，取不到不影响启动。请求只发 GET，不带任何用户数据和 key
type ModelsDevConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	// Proxy 空则沿用 linkReader.proxy，再没有按系统环境变量
	Proxy string `yaml:"proxy"`
	// RefreshHours 缓存过了这么久才联网刷新（带 ETag，没变化几乎不花流量）；手动刷新不受限
	RefreshHours float64 `yaml:"refreshHours"`
}

// ModelOverride 是对一个模型的覆盖，0 = 没覆盖
type ModelOverride struct {
	ContextWindow   int `yaml:"contextWindow"`
	MaxOutputTokens int `yaml:"maxOutputTokens"`
}

// DefaultModelMeta 的刷新间隔 4 小时取自 Hermes 的同类做法（带 ETag 的条件请求，便宜），没有 Sumika 自己的流量数据
func DefaultModelMeta() ModelMetaConfig {
	return ModelMetaConfig{ModelsDev: ModelsDevConfig{Enabled: true, URL: "https://models.dev/api.json", RefreshHours: 4}}
}

// ModelIDSafe 说一个模型 ID 能不能由设置页写进配置文件的键：只认字母数字（含中文）和 . _ / + -（网关的 id 形如 auto/deepseek-flash）。
// 含别的字符的模型请直接在配置文件里写覆盖
func ModelIDSafe(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '_', r == '/', r == '+', r == '-':
		default:
			return false
		}
	}
	return true
}

func (m ModelMetaConfig) check() []string {
	var bad []string
	if u := strings.TrimSpace(m.ModelsDev.URL); u != "" {
		if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			bad = append(bad, fmt.Sprintf("modelMeta.modelsDev.url：%q 不是 http(s) 地址", u))
		}
	}
	if m.ModelsDev.RefreshHours < 0 {
		bad = append(bad, fmt.Sprintf("modelMeta.modelsDev.refreshHours：%v 是负数（写 0 用默认 4 小时）", m.ModelsDev.RefreshHours))
	}
	ids := make([]string, 0, len(m.Overrides))
	for id := range m.Overrides {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := m.Overrides[id]
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			bad = append(bad, fmt.Sprintf("modelMeta.overrides：模型 ID %q 不能是空的或带首尾空白", id))
		}
		if o.ContextWindow < 0 || o.MaxOutputTokens < 0 {
			bad = append(bad, fmt.Sprintf("modelMeta.overrides.%s：窗口和单次输出不能是负数（0 = 不覆盖）", id))
		}
	}
	return bad
}

// ModelOverride 查一个模型的覆盖（没有就是零值）
func (m ModelMetaConfig) Override(id string) ModelOverride { return m.Overrides[id] }
