package config

// 模型服务商（规格-模型配置 3.1，ADR-390）。M1 只读形状、校验，再在内存里补上 magpie；
// 目录发现、用途分配、兼容开关和预设怎么用是后面的步骤，这里不接行为。

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Provider 是一个模型服务商的连接声明。服务商 ID 是它在 providers 里的键：
// 小写字母、数字、- 和 _，不含 / # :（引用按第一个斜杠切，见规格 3.2）。
type Provider struct {
	// Preset 是预设名（magpie 等），省了就是通用服务商；M1 只记下来
	Preset string `yaml:"preset"`
	// Name 是显示名；空 = 用预设默认，没有预设时用 ID
	Name string `yaml:"name"`
	// Kind 是线路，第一版只认 openai-compatible；空也当它
	Kind string `yaml:"kind"`
	// BaseURL 是 OpenAI 兼容接口的根地址（不带末尾 /）
	BaseURL string `yaml:"baseUrl"`
	// Auth 是认证方式：none | bearer | header
	Auth string `yaml:"auth"`
	// KeyRef 只放密钥引用：${secret:名字} 或 ${env:名字}，和 web.keyRef 同格式；yml 里不放明文
	KeyRef string `yaml:"keyRef"`
	// Enabled 不写就是启用。停用的服务商保留引用，请求时报"服务商已停用"，不静默换别家
	Enabled *bool `yaml:"enabled"`
	// Compat 是兼容开关，M1 只解析、还没接到请求上（默认值按预设，见规格 3.1）
	Compat ProviderCompat `yaml:"compat"`
}

// On 是服务商现在是否启用：不写 enabled 就是启用。
func (p Provider) On() bool { return p.Enabled == nil || *p.Enabled }

// ProviderCompat 是逐项的兼容开关（规格 3.1）。指针区分"没写"和"写了 false"，
// 字符串为空表示"用预设默认"。
type ProviderCompat struct {
	StreamUsage     *bool          `yaml:"streamUsage"`
	CacheKey        *bool          `yaml:"cacheKey"`
	LongCache       *bool          `yaml:"longCache"`
	Store           *bool          `yaml:"store"`
	MaxTokensField  string         `yaml:"maxTokensField"`
	SystemRole      string         `yaml:"systemRole"`
	ReasoningEffort string         `yaml:"reasoningEffort"`
	ExtraBody       map[string]any `yaml:"extraBody"`
}

// providerIDPattern：小写字母、数字、- 和 _，最长 64 个字符（不含 / # :）
var providerIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// fineProviderAuth：auth 的取值（空 = 还没选，按预设）
var fineProviderAuth = map[string]bool{"": true, "none": true, "bearer": true, "header": true}

// prepareProviders 在内存里补上 magpie（规格-模型配置 第四节第 1 条，不写回文件），
// 规范一下每项，并在两个来源同时存在时留一条给用户看的提示。
func (c *Config) prepareProviders(raw map[string]any) {
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	if _, ok := c.Providers["magpie"]; !ok {
		c.Providers["magpie"] = Provider{
			Preset:  "magpie",
			Name:    "Magpie",
			Kind:    "openai-compatible",
			BaseURL: c.Magpie.BaseURL,
			Auth:    "bearer",
		}
	}
	for id, p := range c.Providers {
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		if p.Kind == "" {
			p.Kind = "openai-compatible"
		}
		c.Providers[id] = p
	}
	if c.DefaultProvider == "" {
		c.DefaultProvider = "magpie"
	}
	if _, declared := providerEntry(raw, "magpie"); declared && c.Present["magpie.baseUrl"] {
		c.Notices = append(c.Notices, "providers.magpie 已经在用，magpie.baseUrl 被忽略；改地址请改 providers.magpie.baseUrl")
	}
}

// MagpieBaseURL 是现在实际连的 magpie 地址：providers.magpie 存在就以它为准，
// 否则是 magpie.baseUrl（没有 providers 时合成的那条和它一样）。
func (c Config) MagpieBaseURL() string {
	if p, ok := c.Providers["magpie"]; ok && p.BaseURL != "" {
		return p.BaseURL
	}
	return c.Magpie.BaseURL
}

// providerEntry 是 raw 里 providers.<id> 那一项；键在就算声明过，值是 null 也算。
func providerEntry(raw map[string]any, id string) (map[string]any, bool) {
	providers, ok := raw["providers"].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := providers[id]
	if !ok {
		return nil, false
	}
	entry, _ := v.(map[string]any)
	return entry, true
}

// providerIssues 查服务商这一节。magpieDeclared 说 providers.magpie 是不是配置里写的：
// 不是的话那是内存里合成的，地址由 magpie.baseUrl 那条检查负责，不重复报。
func (c Config) providerIssues(magpieDeclared bool) []string {
	var bad []string
	ids := make([]string, 0, len(c.Providers))
	for id := range c.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id == "magpie" && !magpieDeclared {
			continue
		}
		if id == "" {
			bad = append(bad, "providers：服务商 ID 不能为空")
			continue
		}
		prefix := "providers." + id
		if !providerIDPattern.MatchString(id) {
			bad = append(bad, fmt.Sprintf("%s：服务商 ID 只能用小写字母、数字、- 和 _（不能含 / # :），最长 64 个字符", prefix))
		}
		p := c.Providers[id]
		if p.BaseURL == "" {
			bad = append(bad, prefix+".baseUrl：没写地址")
		} else if !validProviderURL(p.BaseURL) {
			bad = append(bad, prefix+".baseUrl：不是 http/https 地址（要有主机名，不带用户名密码）")
		}
		if p.Kind != "openai-compatible" {
			bad = append(bad, fmt.Sprintf("%s.kind：%q 不认识，这个版本只支持 openai-compatible", prefix, p.Kind))
		}
		if !fineProviderAuth[p.Auth] {
			bad = append(bad, fmt.Sprintf("%s.auth：%q 不认识，只有 none / bearer / header", prefix, p.Auth))
		}
		if p.KeyRef != "" {
			if _, ok := keyRefName(p.KeyRef); !ok {
				bad = append(bad, prefix+".keyRef：只放密钥引用（${secret:名字} 或 ${env:名字}），明文 key 不收")
			}
		}
	}
	if c.DefaultProvider != "" {
		if _, ok := c.Providers[c.DefaultProvider]; !ok {
			bad = append(bad, fmt.Sprintf("defaultProvider：引用了不存在的服务商 %q", c.DefaultProvider))
		}
	}
	return bad
}

// providerRawIssues 看 raw 里有没有把明文 key 写进服务商项（apiKey、key、token）：
// 这些字段不认识，普通解析会静默忽略，所以要在原始 YAML 上看一眼再拒收。
func providerRawIssues(raw map[string]any) []string {
	providers, ok := raw["providers"].(map[string]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var bad []string
	for _, id := range ids {
		entry, ok := providers[id].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"apiKey", "key", "token"} {
			if _, exists := entry[field]; exists {
				bad = append(bad, fmt.Sprintf("providers.%s.%s：明文 key 不收；写 keyRef: ${secret:名字} 或 ${env:名字}", id, field))
			}
		}
	}
	return bad
}

// validProviderURL：只认 http/https、有主机名、不带用户名密码、没有控制字符
func validProviderURL(s string) bool {
	if strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

// keyRefName 是密钥引用的名字：${secret:名字} 或 ${env:名字}，也认不带 ${} 的写法（和 web.keyRef 一样）。
// 明文、名字为空、别的来源都返回 false。
func keyRefName(ref string) (string, bool) {
	s := ref
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		s = s[2 : len(s)-1]
	}
	if s == "" || s != strings.TrimSpace(s) {
		return "", false
	}
	kind, name, ok := strings.Cut(s, ":")
	if !ok || name == "" || name != strings.TrimSpace(name) {
		return "", false
	}
	if kind != "secret" && kind != "env" {
		return "", false
	}
	return name, true
}
