// Package modelref 是模型引用（规格-模型配置 3.2，ADR-390 第 1 条）：`服务商ID/模型ID`，按第一个斜杠切。
// 叶子包，谁都能 import：配置校验、llm 的路由、turn 的冷却和 trace 用同一套切法，不各写一份。
package modelref

import (
	"errors"
	"fmt"
	"strings"
)

// ErrEmpty：空引用就是"没选模型"，不是某个叫空串的模型
var ErrEmpty = errors.New("模型引用是空的（没选模型）")

// Ref 是解析好的引用：服务商 ID 和发给服务商的模型 ID（不带服务商前缀）。
// Provider 为空只出现在没有声明任何服务商的零值解析器（测试、单独的小工具），这时 Model 就是整串
type Ref struct {
	Provider string
	Model    string
}

// String 是规范形式 `服务商ID/模型ID`；没有服务商时就是模型 ID 本身
func (r Ref) String() string {
	if r.Provider == "" {
		return r.Model
	}
	return r.Provider + "/" + r.Model
}

// Resolver 按"已声明的服务商"和"裸引用归谁"解析引用。值类型、复制便宜；零值认为一个服务商都没声明，
// 所有字符串原样当模型 ID（和改动前一样）
type Resolver struct {
	declared map[string]bool
	def      string
}

// New 建解析器：ids 是已声明的服务商 ID（含停用的：停用的仍保留引用，请求时才报错），def 是裸引用归的服务商
func New(def string, ids ...string) Resolver {
	r := Resolver{declared: make(map[string]bool, len(ids)), def: def}
	for _, id := range ids {
		r.declared[id] = true
	}
	return r
}

// Default 是裸引用归的服务商
func (r Resolver) Default() string { return r.def }

// Declared 说这个 ID 是不是已声明的服务商
func (r Resolver) Declared(id string) bool { return r.declared[id] }

// Parse 解析一个引用：
//   - 第一个 `/` 之前是已声明的服务商 ID → 规范形式，`/` 之后整段（可含更多斜杠）是模型 ID；
//   - 否则整串是模型 ID，归 def（裸引用；Magpie 的模型 ID 自己带斜杠，形如 group/auto-xxx，走这条）；
//   - 空串 → ErrEmpty；服务商后面没写模型（`magpie/`）→ 报错，不拿别的顶。
//
// 不去空格、不改大小写：模型 ID 原样比对
func (r Resolver) Parse(s string) (Ref, error) {
	if s == "" {
		return Ref{}, ErrEmpty
	}
	if head, rest, ok := strings.Cut(s, "/"); ok && r.declared[head] {
		if rest == "" {
			return Ref{}, fmt.Errorf("模型引用 %q：服务商 %s 后面没写模型", s, head)
		}
		return Ref{Provider: head, Model: rest}, nil
	}
	return Ref{Provider: r.def, Model: s}, nil
}

// Canonical 是引用的规范形式；空串、读不懂的原样还回去（让后面的检查报它自己的错，这里不吞）
func (r Resolver) Canonical(s string) string {
	ref, err := r.Parse(s)
	if err != nil {
		return s
	}
	return ref.String()
}
