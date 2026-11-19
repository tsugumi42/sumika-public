package config

// 模型引用的对象写法（规格-模型配置 3.2、3.3）：各用途键除了字符串 `服务商/模型`，也认
//
//	model: { provider: deepseek, model: some-model }
//
// 读取时在内存里换成字符串形式再解析，文件不改写；设置页保存时一律写字符串（那是后面的步骤）。
// 键和值的类型都是现有的 string，下游（校验、模型页、turn）一个都不用认对象。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// normalizeModelRefObjects 把配置里写成对象的模型引用换成 `服务商/模型` 字符串，返回换过的 YAML 字节。
// 一个对象写法都没有时原样返回 b（不重新序列化）。对象缺 provider 或 model、带别的键、值不是字符串，
// 报错并写明是哪个键，不猜
func normalizeModelRefObjects(b []byte) ([]byte, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil || raw == nil {
		return b, nil // 不是合法 YAML 的由后面的解析报它自己的错
	}
	changed := false
	var bad []string
	// fix 换一个值：是对象就换成字符串，否则原样
	fix := func(key string, v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		changed = true
		provider, _ := m["provider"].(string)
		model, _ := m["model"].(string)
		for k := range m {
			if k != "provider" && k != "model" {
				bad = append(bad, fmt.Sprintf("%s：对象写法只认 provider 和 model，多了 %q", key, k))
			}
		}
		if provider == "" || model == "" || strings.ContainsAny(provider, "/ ") {
			bad = append(bad, fmt.Sprintf("%s：对象写法要同时写 provider 和 model（服务商 ID 不含 /），或者直接写 服务商/模型 字符串", key))
			return v
		}
		return provider + "/" + model
	}
	fixList := func(key string, v any) any {
		list, ok := v.([]any)
		if !ok {
			return v
		}
		for i := range list {
			list[i] = fix(fmt.Sprintf("%s[%d]", key, i), list[i])
		}
		return list
	}
	sub := func(parent map[string]any, name string) map[string]any {
		m, _ := parent[name].(map[string]any)
		return m
	}
	if chars := sub(raw, "characters"); chars != nil {
		ids := make([]string, 0, len(chars))
		for id := range chars {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ch, _ := chars[id].(map[string]any)
			if ch == nil {
				continue
			}
			for _, k := range []string{"model", "background", "topic"} {
				if v, ok := ch[k]; ok {
					ch[k] = fix("characters."+id+"."+k, v)
				}
			}
			if v, ok := ch["fallbackModels"]; ok {
				ch["fallbackModels"] = fixList("characters."+id+".fallbackModels", v)
			}
		}
	}
	for _, p := range []struct {
		section, key string
		list         bool
	}{{"defaults", "backgroundModel", false}, {"defaults", "fallbackModels", true}, {"subagents", "model", false}, {"vision", "model", false}} {
		if s := sub(raw, p.section); s != nil {
			if v, ok := s[p.key]; ok {
				if p.list {
					s[p.key] = fixList(p.section+"."+p.key, v)
				} else {
					s[p.key] = fix(p.section+"."+p.key, v)
				}
			}
		}
	}
	if agent := sub(raw, "agent"); agent != nil {
		for _, name := range []string{"advisor", "verify"} {
			if s := sub(agent, name); s != nil {
				if v, ok := s["model"]; ok {
					s["model"] = fix("agent."+name+".model", v)
				}
			}
		}
	}
	if len(bad) > 0 {
		return b, fmt.Errorf("%s", strings.Join(bad, "；"))
	}
	if !changed {
		return b, nil
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return b, fmt.Errorf("模型引用的对象写法换成字符串时出错：%w", err)
	}
	return out, nil
}
