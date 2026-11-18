package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// 服务商这一节的形状（规格-模型配置 3.1，ADR-390 M1）：读得进来、规范得对，行为还没接。
func TestProviderConfigShape(t *testing.T) {
	c, err := Parse([]byte(`
providers:
  deepseek:
    name: DeepSeek
    baseUrl: https://api.example.invalid/v1/
    auth: bearer
    keyRef: ${env:DEEPSEEK_API_KEY}
    enabled: false
    compat:
      cacheKey: false
      maxTokensField: max_tokens
  ollama:
    baseUrl: http://127.0.0.1:11434/v1
    keyRef: env:OLLAMA_API_KEY
defaultProvider: deepseek
`), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultProvider != "deepseek" {
		t.Fatalf("defaultProvider = %q", c.DefaultProvider)
	}
	p, ok := c.Providers["deepseek"]
	if !ok || p.Name != "DeepSeek" || p.Kind != "openai-compatible" || p.BaseURL != "https://api.example.invalid/v1" ||
		p.Auth != "bearer" || p.KeyRef != "${env:DEEPSEEK_API_KEY}" || p.On() {
		t.Fatalf("deepseek 读错了：%+v", p)
	}
	if p.Compat.CacheKey == nil || *p.Compat.CacheKey || p.Compat.MaxTokensField != "max_tokens" {
		t.Fatalf("compat 读错了：%+v", p.Compat)
	}
	o := c.Providers["ollama"]
	if o.Kind != "openai-compatible" || o.BaseURL != "http://127.0.0.1:11434/v1" || !o.On() || o.KeyRef != "env:OLLAMA_API_KEY" {
		t.Fatalf("ollama 读错了：%+v", o)
	}
	// 没有 providers.magpie 时内存里补一条，地址取 magpie.baseUrl 的默认值；不写回文件
	if m := c.Providers["magpie"]; m.Preset != "magpie" || m.BaseURL != "http://127.0.0.1:3425/v1" || !m.On() {
		t.Fatalf("合成的 magpie 不对：%+v", m)
	}
	if len(c.Notices) != 0 {
		t.Fatalf("没写 magpie.baseUrl 不该有提示：%v", c.Notices)
	}
}

// 迁移第 1 条：没有 providers: 或没有 providers.magpie，都在内存里合成，不写回文件。
func TestProvidersSynthesizeMagpie(t *testing.T) {
	c, err := Parse([]byte("magpie:\n  baseUrl: http://127.0.0.1:9999/v1\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Providers["magpie"]; m.BaseURL != "http://127.0.0.1:9999/v1" || c.DefaultProvider != "magpie" || c.MagpieBaseURL() != "http://127.0.0.1:9999/v1" {
		t.Fatalf("没按 magpie.baseUrl 合成：%+v default=%q 实际=%q", m, c.DefaultProvider, c.MagpieBaseURL())
	}
	c, err = Parse([]byte("providers: {}\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Providers["magpie"]; m.BaseURL != "http://127.0.0.1:3425/v1" || c.DefaultProvider != "magpie" {
		t.Fatalf("空 providers 该补默认 magpie：%+v default=%q", m, c.DefaultProvider)
	}

	// 没有配置文件也合成（LoadIn 的缺文件路径）
	t.Setenv("SUMIKA_CONFIG", filepath.Join(t.TempDir(), "没有这个文件.yml"))
	c, err = Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Providers["magpie"]; m.BaseURL != "http://127.0.0.1:3425/v1" || c.DefaultProvider != "magpie" {
		t.Fatalf("缺配置文件也该合成 magpie：%+v default=%q", m, c.DefaultProvider)
	}
}

// providers.magpie 存在时 magpie.baseUrl 被忽略，并存时给一条提示；被忽略的旧值不挡加载。
func TestProvidersMagpieReplacesBaseURL(t *testing.T) {
	c, err := Parse([]byte(`
magpie:
  baseUrl: 不是网址
providers:
  magpie:
    baseUrl: http://127.0.0.1:7777/v1
`), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Providers["magpie"]; m.BaseURL != "http://127.0.0.1:7777/v1" || c.MagpieBaseURL() != "http://127.0.0.1:7777/v1" {
		t.Fatalf("该用 providers.magpie 的地址：%+v 实际=%q", m, c.MagpieBaseURL())
	}
	if len(c.Notices) != 1 || !strings.Contains(c.Notices[0], "magpie.baseUrl") {
		t.Fatalf("两处并存该给一条提示：%v", c.Notices)
	}

	// 只写 providers.magpie、没写 magpie.baseUrl：没有"被取代"这回事，不提示
	c, err = Parse([]byte("providers:\n  magpie:\n    baseUrl: http://127.0.0.1:7777/v1\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Notices) != 0 {
		t.Fatalf("没有并存的 magpie.baseUrl 不该提示：%v", c.Notices)
	}
}

// 校验：缺、空、非法 ID、未知 kind、明文 key，每种都要报出是哪一项。
func TestProviderValidation(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want string
	}{
		{"没写地址", "providers:\n  x: {}\n", "providers.x.baseUrl"},
		{"空地址", "providers:\n  x:\n    baseUrl: \"\"\n", "providers.x.baseUrl"},
		{"地址不是 http", "providers:\n  x:\n    baseUrl: ftp://127.0.0.1/x\n", "providers.x.baseUrl"},
		{"地址带用户名密码", "providers:\n  x:\n    baseUrl: http://u:p@127.0.0.1:1/v1\n", "providers.x.baseUrl"},
		{"ID 带斜杠", "providers:\n  \"a/b\":\n    baseUrl: http://127.0.0.1:1/v1\n", "服务商 ID"},
		{"ID 带冒号", "providers:\n  \"a:b\":\n    baseUrl: http://127.0.0.1:1/v1\n", "服务商 ID"},
		{"ID 带井号", "providers:\n  \"a#b\":\n    baseUrl: http://127.0.0.1:1/v1\n", "服务商 ID"},
		{"ID 大写", "providers:\n  Magpie:\n    baseUrl: http://127.0.0.1:1/v1\n", "服务商 ID"},
		{"ID 是空串", "providers:\n  \"\":\n    baseUrl: http://127.0.0.1:1/v1\n", "服务商 ID"},
		{"未知 kind", "providers:\n  x:\n    kind: anthropic\n    baseUrl: http://127.0.0.1:1/v1\n", "providers.x.kind"},
		{"明文 keyRef", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    keyRef: sk-test\n", "明文 key"},
		{"裸密钥库名", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    keyRef: provider.x.apiKey\n", "providers.x.keyRef"},
		{"名字为空", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    keyRef: \"${env:}\"\n", "providers.x.keyRef"},
		{"明文 apiKey 字段", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    apiKey: sk-test\n", "providers.x.apiKey"},
		{"明文 key 字段", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    key: sk-test\n", "providers.x.key"},
		{"auth 不认识", "providers:\n  x:\n    baseUrl: http://127.0.0.1:1/v1\n    auth: basic\n", "providers.x.auth"},
		{"defaultProvider 不存在", "defaultProvider: nope\n", "defaultProvider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yml), "sumika.yml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("该报 %q，得到 %v", tc.want, err)
			}
		})
	}
}

// 回退用：带 providers: 的配置交给不认识它的旧解析函数（普通 yaml.Unmarshal 到旧结构）不报错，
// 旧字段照常读出来；规格第零节问题 6、第十节验收 12。
func TestOldParserIgnoresProviders(t *testing.T) {
	var old struct {
		Magpie struct {
			BaseURL string `yaml:"baseUrl"`
		} `yaml:"magpie"`
		Defaults struct {
			BackgroundModel string `yaml:"backgroundModel"`
		} `yaml:"defaults"`
	}
	b := []byte(`
magpie:
  baseUrl: http://127.0.0.1:3425/v1
providers:
  deepseek:
    kind: openai-compatible
    baseUrl: https://api.example.invalid/v1
    keyRef: ${env:DEEPSEEK_API_KEY}
    compat: { cacheKey: false }
defaultProvider: deepseek
defaults:
  backgroundModel: 背景模型
`)
	if err := yaml.Unmarshal(b, &old); err != nil {
		t.Fatalf("旧解析函数不该因为不认识 providers 报错：%v", err)
	}
	if old.Magpie.BaseURL != "http://127.0.0.1:3425/v1" || old.Defaults.BackgroundModel != "背景模型" {
		t.Fatalf("旧字段读错了：%+v", old)
	}
}
