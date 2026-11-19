package tools

import (
	"sort"
	"strings"

	"sumika-native/internal/jsjson"
)

// CallTarget 是一次工具调用的对象：界面上写“读了 a.md”“搜了 关键词”用。只取一行、不超过 200 字。
func CallTarget(name string, args jsjson.Value) string {
	if args.Kind != jsjson.Object {
		return ""
	}
	key := "path"
	switch name {
	case "grep", "find":
		key = "pattern"
	case "memory_search", "recall_raw":
		key = "query"
	case "powershell":
		key = "command"
	}
	text := args.Obj[key].Str
	if text == "" {
		text = firstStringArg(args)
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if rs := []rune(text); len(rs) > 200 {
		text = string(rs[:200])
	}
	return text
}

// firstStringArg 给没有约定主参数的工具（MCP、插件）找一个能说明在干什么的参数：常见的名字优先，否则按键名排序取第一个字符串。
func firstStringArg(args jsjson.Value) string {
	for _, key := range []string{"query", "url", "name", "title", "id", "text", "content"} {
		if v := args.Obj[key].Str; v != "" {
			return v
		}
	}
	keys := make([]string, 0, len(args.Obj))
	for k := range args.Obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := args.Obj[k].Str; v != "" {
			return v
		}
	}
	return ""
}
