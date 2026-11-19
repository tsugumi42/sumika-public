package config

import (
	"strings"
	"testing"
)

const refYAML = `
providers:
  deepseek:
    baseUrl: https://api.example.invalid/v1
  off:
    baseUrl: http://127.0.0.1:2/v1
    enabled: false
`

// 引用解析器：已声明的服务商（含停用的）才认前缀，裸引用归 defaultProvider；改 defaultProvider 裸引用的归属跟着变
func TestModelRefsFollowProvidersAndDefault(t *testing.T) {
	c, err := Parse([]byte(refYAML), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	refs := c.ModelRefs()
	for in, want := range map[string]string{
		"deepseek/chat":       "deepseek/chat",
		"magpie/group/auto-x": "magpie/group/auto-x", // 合成的 magpie 也是已声明的
		"group/auto-x":        "magpie/group/auto-x", // 首段 group 没声明：整串是模型 ID，归默认
		"off/m":               "off/m",               // 停用的照样认前缀
		"chat":                "magpie/chat",
	} {
		if got := refs.Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q，想要 %q", in, got, want)
		}
	}
	if got := c.DisabledProviders(); len(got) != 1 || got[0] != "off" {
		t.Errorf("DisabledProviders = %v", got)
	}
	// 改了它行为会变：defaultProvider 换成 deepseek，裸引用跟着归 deepseek
	c2, err := Parse([]byte(refYAML+"defaultProvider: deepseek\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.ModelRefs().Canonical("chat"); got != "deepseek/chat" {
		t.Errorf("defaultProvider: deepseek 后裸引用 chat = %q，想要 deepseek/chat", got)
	}
	// 零值配置（没读过文件）：谁也不认，引用原样
	if got := (Config{}).ModelRefs().Canonical("deepseek/chat"); got != "deepseek/chat" {
		t.Errorf("零值配置的解析器该原样放行：%q", got)
	}
}

// 各用途键接受引用：裸字符串原样保留（不自动改写），带前缀的也读得进来；服务商后面没写模型的拒绝并指出键
func TestModelRefKeysAcceptRefs(t *testing.T) {
	c, err := Parse([]byte(refYAML+`
characters:
  suzu:
    model: deepseek/chat
    background: group/auto-x
    fallbackModels: [magpie/a, b]
defaults:
  backgroundModel: deepseek/chat
  fallbackModels: [deepseek/chat]
`), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Characters["suzu"]
	if ch.Model != "deepseek/chat" || ch.Background != "group/auto-x" || len(ch.FallbackModels) != 2 || ch.FallbackModels[1] != "b" || c.Defaults.BackgroundModel != "deepseek/chat" {
		t.Fatalf("引用该原样保留：%+v %+v", ch, c.Defaults)
	}
	for key, doc := range map[string]string{
		"characters.suzu.model":             "characters:\n  suzu:\n    model: deepseek/\n",
		"characters.suzu.background":        "characters:\n  suzu:\n    background: deepseek/\n",
		"characters.suzu.fallbackModels[1]": "characters:\n  suzu:\n    fallbackModels: [x, deepseek/]\n",
		"defaults.backgroundModel":          "defaults:\n  backgroundModel: deepseek/\n",
		"defaults.fallbackModels[0]":        "defaults:\n  fallbackModels: [deepseek/]\n",
		"agent.advisor.model":               "agent:\n  advisor:\n    model: deepseek/\n",
		"subagents.model":                   "subagents:\n  model: deepseek/\n",
		"vision.model":                      "vision:\n  model: deepseek/\n",
	} {
		_, err := Parse([]byte(refYAML+doc), "sumika.yml")
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "后面没写模型") {
			t.Errorf("%s 写 deepseek/ 该被拒并指出键：%v", key, err)
		}
	}
	// 指向没声明的服务商：保留值，不在读配置时拒（请求时才明确报错，规格 9.8）
	if _, err := Parse([]byte("characters:\n  suzu:\n    model: nowhere/m\n"), "sumika.yml"); err != nil {
		t.Errorf("没声明的前缀是裸模型 ID，不该拒：%v", err)
	}
}

// 对象写法 {provider, model} 和字符串写法等价；文件版本认磁盘原文；写错的对象指出键
func TestModelRefObjectForm(t *testing.T) {
	doc := refYAML + `
characters:
  suzu:
    model: { provider: deepseek, model: chat }
    background:
      provider: magpie
      model: group/auto-x
    fallbackModels:
      - { provider: deepseek, model: backup }
      - plain
defaults:
  backgroundModel: { provider: deepseek, model: bg }
subagents:
  model: { provider: deepseek, model: sub }
`
	c, err := Parse([]byte(doc), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Characters["suzu"]
	if ch.Model != "deepseek/chat" || ch.Background != "magpie/group/auto-x" || len(ch.FallbackModels) != 2 || ch.FallbackModels[0] != "deepseek/backup" || ch.FallbackModels[1] != "plain" {
		t.Fatalf("对象写法该换成字符串：%+v", ch)
	}
	if c.Defaults.BackgroundModel != "deepseek/bg" || c.Subagents.Model != "deepseek/sub" {
		t.Fatalf("defaults / subagents：%q %q", c.Defaults.BackgroundModel, c.Subagents.Model)
	}
	if c.Version != ContentVersion([]byte(doc)) {
		t.Errorf("版本该是磁盘原文的，不是换过写法的")
	}
	for name, bad := range map[string]string{
		"characters.suzu.model：对象写法要同时写":     "characters:\n  suzu:\n    model: { provider: deepseek }\n",
		"defaults.backgroundModel：对象写法只认":    "defaults:\n  backgroundModel: { provider: deepseek, model: x, extra: 1 }\n",
		"characters.suzu.fallbackModels[1]：": "characters:\n  suzu:\n    fallbackModels: [a, { model: x }]\n",
	} {
		_, err := Parse([]byte(refYAML+bad), "sumika.yml")
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("该报 %q：%v", name, err)
		}
	}
}
