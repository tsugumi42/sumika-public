package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// checkStructKeys 按目标类型核对映射键：要解码成结构体的映射里，键必须是文字。
//
// 为什么要自己查：goccy/go-yaml v1.19.2 解码结构体时遇到非文字的键（`0:`、`true:`、`null:`），
// decode.go 的 keyToNodeMap 返回 nil, nil，整个映射一个字段都不设，Unmarshal 还报成功——误写一行，
// 整份配置静默回到默认值（2026-11-18 性质测试撞出，种子 testdata/fuzz/FuzzConfigParsePatch/37f48b4cac424d4f）。
// 要解码成 map 的地方照收：`linkReader.expand.senders.groups` 允许不加引号的数字群号。
// 库修好以后这道检查可以删（goccy 上游 v1.19.2 是最新版，2026-11-18 查过）。
func checkStructKeys(b []byte, t reflect.Type) error {
	tree, err := parser.ParseBytes(b, 0)
	if err != nil {
		return nil // 语法错误交给 Unmarshal 报，它的错误文字带位置
	}
	for _, doc := range tree.Docs {
		if doc == nil || doc.Body == nil {
			continue
		}
		if err := walkStructKeys(doc.Body, t, "", 0); err != nil {
			return err
		}
	}
	return nil
}

// 嵌套上限：配置不会这么深，防止病态输入把栈打爆
const maxStructKeyDepth = 64

func walkStructKeys(n ast.Node, t reflect.Type, path string, depth int) error {
	if depth > maxStructKeyDepth {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	n = unwrapNode(n)
	if n == nil {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		fields := yamlFields(t)
		for _, kv := range mappingEntries(n) {
			if kv.Key.IsMergeKey() {
				if err := walkStructKeys(kv.Value, t, path, depth+1); err != nil {
					return err
				}
				continue
			}
			key, ok := unwrapNode(kv.Key).(*ast.StringNode)
			if !ok {
				where := path
				if where == "" {
					where = "最外层"
				}
				line := 0
				if tok := kv.Key.GetToken(); tok != nil && tok.Position != nil {
					line = tok.Position.Line
				}
				return fmt.Errorf("第 %d 行：%s 下面的键 %q 不是文字（像数字、true、null 这样的键会让这一整段被忽略）；键名要写成配置项的名字，确实要用这个写法就加上引号", line, where, strings.TrimSpace(kv.Key.String()))
			}
			ft, ok := fields[key.Value]
			if !ok {
				continue // 未知键照旧不管
			}
			next := key.Value
			if path != "" {
				next = path + "." + key.Value
			}
			if err := walkStructKeys(kv.Value, ft, next, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, kv := range mappingEntries(n) {
			next := path + "." + strings.TrimSpace(kv.Key.String())
			if err := walkStructKeys(kv.Value, t.Elem(), next, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if seq, ok := n.(*ast.SequenceNode); ok {
			for i, v := range seq.Values {
				if err := walkStructKeys(v, t.Elem(), fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// unwrapNode 去掉标签和锚点；别名不展开（它指向的那段已经在原处查过，展开还可能绕圈）
func unwrapNode(n ast.Node) ast.Node {
	for {
		switch v := n.(type) {
		case *ast.TagNode:
			n = v.Value
		case *ast.AnchorNode:
			n = v.Value
		case *ast.AliasNode:
			return nil
		default:
			return n
		}
	}
}

func mappingEntries(n ast.Node) []*ast.MappingValueNode {
	switch v := n.(type) {
	case *ast.MappingNode:
		return v.Values
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{v}
	}
	return nil
}

// yamlFields 是结构体在 YAML 里的键名 → 字段类型；`,inline` 的嵌入结构体字段并进来。
// 没写标签的字段按 goccy 的规则用小写字段名，原名也收下（只用来往下走，不会因此报错）
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(","+opts+",", ",inline,") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range yamlFields(ft) {
					out[k] = v
				}
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			out[strings.ToLower(f.Name)] = f.Type
			out[f.Name] = f.Type
			continue
		}
		out[name] = f.Type
	}
	return out
}
