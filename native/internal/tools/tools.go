// Package tools 是她能用的工具（第 2 步，规格-Go后台 四点七）。现在只有声明（declarations_gen.go；原从 Bun 的 Tools 插件导出，
// 导出脚本已退役，现直接维护），参数的 JSON Schema 原样保存（属性先后、TypeBox 的 anyOf / const 都不动）；执行在 2c。
// 同一份声明有三种写法：发进请求的（pi convertTools）、Sumika 估上下文用的（agent.ts fixedTokens、context.ts contextBreakdown）、
// pi 估输出上限用的（transcript.js toToolDeclaration + estimate.js estimateToolsTokens），三种都要和 Bun 一字不差
package tools

import (
	"strings"

	"sumika-native/internal/store"
)

// Declaration 是一个工具给模型看的样子
type Declaration struct {
	Name, Description string
	// Parameters 是参数的 JSON Schema，JSON.stringify 的原样
	Parameters string
	// ConstrainedSampling 是 pi 工具上的 constrainedSampling（JSON.stringify 原样；没有是空）：不进请求（这个地址不支持 strict），
	// 但 pi 估输出上限时算进去
	ConstrainedSampling string
}

// 导出的 11 个里，记忆的三个执行在记忆插件里，由它注册（规格-Go后台 8.1"插件贡献工具"）
var memoryTools = map[string]bool{"memory_search": true, "recall_raw": true, "remember": true}

// OwnerConsole 是主人 × 本地主窗口能用的内置工具（tools.ts forAudience 去掉记忆的三个），顺序就是发进请求的顺序
func OwnerConsole() []Declaration {
	return append(pick(func(d Declaration) bool { return !memoryTools[d.Name] }), desktopDecls...)
}

// MemoryDecls 是记忆插件的正式声明出口。记忆行为由 Go 规格决定，不能由退役的 Bun 生成器覆盖。
func MemoryDecls() []Declaration {
	out := pick(func(d Declaration) bool { return memoryTools[d.Name] })
	for i := range out {
		if out[i].Name == "remember" {
			out[i].Description = rememberDescription + " 所有者自身事实和偏好可供所有角色想起；你和所有者的关系、称呼、约定、边界、共同经历、只告诉你的事，only_her 写 true；只有所有者自身事实写 false，拿不准写 true。"
			out[i].Parameters = rememberParameters
		}
	}
	return append(out, forgetDecl)
}

// forgetDecl 是 memory_forget 的声明：Go 自己加的第四个记忆工具，不在 Bun 导出的声明里（所以不进 declarations_gen.go）
var forgetDecl = Declaration{Name: "memory_forget", Description: forgetDescription, Parameters: forgetParameters}

const forgetDescription = "忘掉一条记忆：以后不再想起它，也不会被后台整理或 remember 复活；库里没有真删，留着记录。只在所有者明确说这条不对、过时了或者不要再记的时候用：先用 memory_search 查到那条的编号（# 开头的 6 位），id 填它，reason 一句话写为什么，source_quote 引用所有者要你忘掉的那句原话。只能忘掉关于所有者或你自己的记忆，受所有者确认或历史导入保护的、片段、已经忘掉的、编号不存在的都会报错。只是改错用 remember 的 replaces，不用忘掉。工具成功才说忘掉了。"

const forgetParameters = `{"type":"object","required":["id","reason","source_quote"],"properties":{"id":{"type":"string","description":"memory_search 里那条的六位编号，# 可有可无"},"reason":{"type":"string","description":"为什么要忘掉，一句话"},"source_quote":{"type":"string","description":"本轮已经看到的所有者输入中要求忘掉它的完整连续原文；不能引用尚未看到的新消息"}}}`

const rememberDescription = "保存以后确实用得上的稳定偏好、明确边界或近期约定。先判断价值，再核来源：普通闲聊、重复晚安或表白、缺对象的评论不用逐条记；后台会自动整理，但也允许不保存。先搜索已有记忆，重复不新增。source_quote 引用所有者原话；你自己的现实经历不能凭空存成事实。工具写入是 AI 提炼，不等于所有者确认，不能自行覆盖受保护或撤销的记忆。临时安排填写期限，到期不代表已完成。工具成功才说记住了。"

const rememberParameters = `{"type":"object","required":["kind","text","temporal","source_quote"],"properties":{"kind":{"type":"string","enum":["fact","promise","preference","boundary"]},"text":{"type":"string","description":"第三人称一句话，带时间"},"subject":{"type":"string","description":"所有者写 owner，你自己写 self"},"only_her":{"type":"boolean","description":"关系、约定、共同经历或只告诉你的事写 true；只有所有者自身事实或偏好写 false；拿不准写 true"},"replaces":{"type":"string","description":"memory_search 的六位编号；不是改错就省略"},"temporal":{"type":"string","enum":["stable","temporary"],"description":"稳定偏好或事实用 stable；近期状态、一次安排用 temporary"},"valid_until":{"type":"string","description":"仅 temporary：明确的未来截止时刻，RFC3339 含时区；未知则省略，七天后停止现状召回，不表示完成"},"source_quote":{"type":"string","description":"本轮已经看到的所有者输入中支持本条的完整连续原文；歧义时引用更完整，不能引用尚未看到的新消息"}}}`

// Builtin 说 name 是不是内置的工具（OwnerConsole 里的）
func Builtin(name string) bool { return builtin[name] }

var builtin = func() map[string]bool {
	m := map[string]bool{}
	for _, d := range OwnerConsole() {
		m[d.Name] = true
	}
	return m
}()

func pick(keep func(Declaration) bool) []Declaration {
	var out []Declaration
	for _, d := range ownerConsole {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out
}

func lit(s string) string {
	j, _ := store.JSString(s)
	return j
}

func join(ds []Declaration, one func(Declaration) string) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = one(d)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// Item 是一个工具给 Sumika 估算用的那份：{"name","description","parameters"}（context.ts contextBreakdown 每项单算）
func Item(d Declaration) string {
	return `{"name":` + lit(d.Name) + `,"description":` + lit(d.Description) + `,"parameters":` + d.Parameters + `}`
}

// FixedJSON 是 agent.ts 算固定前缀时的那份：JSON.stringify(tools.map(t => ({ name, description, parameters })))
func FixedJSON(ds []Declaration) string { return join(ds, Item) }

// PiJSON 是 pi 估输出上限时的那份：系统消息上 toolsAdded 的 JSON.stringify（toToolDeclaration：有 constrainedSampling 才带）
func PiJSON(ds []Declaration) string {
	return join(ds, func(d Declaration) string {
		s := `{"name":` + lit(d.Name) + `,"description":` + lit(d.Description) + `,"parameters":` + d.Parameters
		if d.ConstrainedSampling != "" {
			s += `,"constrainedSampling":` + d.ConstrainedSampling
		}
		return s + "}"
	})
}
