package tools

import (
	"errors"
	"fmt"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/store"
)

// ───────────── todo_write（daemon/tools/todo-tool.ts） ─────────────

const todoMaxItems, todoMaxChars = 50, 200

// todo 检查清单、回一句"计划已更新"。清单不对就报错（pi 当成工具出错：steps 里 is_error = 1）。
// 参数结构（是列表、每项有 content 和三种 status 之一）Bun 版先由 TypeBox 按 schema 拦下，Go 还没有这一层（规格第七节 T8），
// 结构不对的这里当成内容空白、或不算进行中处理
func todo(args jsjson.Value) (string, error) {
	list := args.Obj["todos"]
	if args.Kind != jsjson.Object || list.Kind != jsjson.Array {
		return "", errors.New("清单要是一个列表")
	}
	if len(list.Arr) > todoMaxItems {
		return "", fmt.Errorf("清单最多 %d 项（现在 %d 项），合并一下再写", todoMaxItems, len(list.Arr))
	}
	seen := map[string]bool{}
	count := map[string]int{}
	for _, t := range list.Arr {
		c := ""
		if v := t.Obj["content"]; t.Kind == jsjson.Object && v.Kind == jsjson.String {
			c = jsstr.Trim(v.Str)
		}
		if c == "" {
			return "", errors.New("清单里有空白的一项")
		}
		if store.JSLen(c) > todoMaxChars {
			return "", fmt.Errorf("有一项太长了（超过 %d 字）：「%s…」", todoMaxChars, store.JSSlice(c, 20))
		}
		if seen[c] {
			return "", fmt.Errorf("清单里有两项一样的：「%s」", c)
		}
		seen[c] = true
		if s := t.Obj["status"]; s.Kind == jsjson.String {
			count[s.Str]++
		}
	}
	if count["in_progress"] > 1 {
		return "", fmt.Errorf("同一时间最多一项进行中（现在有 %d 项）", count["in_progress"])
	}
	return fmt.Sprintf("计划已更新：待做 %d，进行中 %d，已完成 %d。", count["pending"], count["in_progress"], count["completed"]), nil
}
