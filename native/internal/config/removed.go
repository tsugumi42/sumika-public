package config

import "strings"

// 已经删掉的配置键：读配置不失败、值不起作用；启动时记日志，后台状态页“配置里的旧项”也列出来，
// 别让主人以为还在用。每个键一条说明，说明里写明现在怎么处理、怎么解除（删掉那个键）。
//
// 加新条目：配置结构里删掉这个键（让读它的代码跟着消失），在这里登记，示例配置里不再写它。
// 未登记的未知键照旧静默忽略；这里只为“以前认、现在不认”的键提个醒。
var removedKeys = []struct {
	key  string
	note func(key, value string) string // value 是这个键写的值（只有字符串值才有，别的是空串）
}{
	{"embed.python", embedGone},
	{"embed.serverDir", embedGone},
	{"backend.local", func(key, value string) string {
		if strings.EqualFold(strings.TrimSpace(value), "bun") {
			return key + " 写着 bun：本地私聊的 Bun 后台已移除，现在由 Go 处理；可以把 " + key + " 从 sumika.yml 里删掉"
		}
		return key + " 已不再使用：本地私聊一直由 Go 处理（Bun 后台已移除）；可以把它从 sumika.yml 里删掉"
	}},
	{"backend.bun", bunGone},
	{"backend.server", bunGone},
}

func embedGone(key, _ string) string {
	return key + " 已删除：旧版借 MaiBot 嵌入的退路整体移除了，嵌入现在只走自带的 sumika-models，模型目录是 embed.modelsRoot（空 = <数据目录>/models）；可以把它从 sumika.yml 里删掉"
}

func bunGone(key, _ string) string {
	return key + " 已不再使用：Bun 后台已移除；可以把它从 sumika.yml 里删掉"
}

// removedKeysIn 在读到的配置里找已删键，回键和说明（按登记顺序）。present 是 Config.Present，raw 是解出来的原始 YAML。
func removedKeysIn(raw map[string]any, present map[string]bool) (keys, notes []string) {
	for _, r := range removedKeys {
		if !present[r.key] {
			continue
		}
		keys = append(keys, r.key)
		notes = append(notes, r.note(r.key, rawString(raw, strings.Split(r.key, ".")...)))
	}
	return keys, notes
}

// rawString 取原始 YAML 里这条点分路径上的字符串值；路径不在或不是字符串回空串
func rawString(raw map[string]any, path ...string) string {
	var cur any = raw
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		if cur, ok = m[key]; !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}
