// Package bench 是表二（规格-Go后台 8.2，真实模型）的统一执行器和三个壳共用的约定：执行器给每个壳一份题目（Task，JSON 文件），
// 壳跑完写一份结果（Result，JSON 文件）。Go 壳是 cmd/bench-shell，Bun 壳是 sumika-bun 仓库的 scripts/bench-t2/shell-bun.ts（照这里的字段写），
// omp 由执行器用它的 rpc 模式直接驱动。字段名两边都用这里的 JSON 名
package bench

import (
	"slices"
	"strings"
)

// Task 是一个壳要跑的一道题
type Task struct {
	ID        string `json:"id"`        // 这一次的编号（题号-壳-第几次），执行器的记账代理按它给请求打标签
	Workdir   string `json:"workdir"`   // 工作目录：题目的文件在这里，她只在这里写
	DataDir   string `json:"dataDir"`   // 壳自己的数据目录（库、角色卡），和工作目录互不包含
	Gateway   string `json:"gateway"`   // 模型网关的地址（…/v1），执行器的记账代理
	Model     string `json:"model"`     // 模型名（网关上的）
	Thinking  string `json:"thinking"`  // 思考档位
	Character string `json:"character"` // 角色 id（卡是编的，三个壳一样）
	Seed      []Line `json:"seed,omitempty"`
	Steps     []Step `json:"steps"`
	// KeepTokens 大于 0 时，压缩保留的最近原文改成这么多 token（B4 要让压缩切得出来）
	KeepTokens int `json:"keepTokens,omitempty"`
	// RecentMessages 大于 0 时，建实例读最近这么多条聊天（默认 30）。B4 的历史有三四十条、答案在最前面几条，
	// 照默认值不压缩的对照组也看不到它，压缩的材料（实例里那份）同样缺这几条；B4 设得比历史长，两组都从全部原文出发
	RecentMessages int `json:"recentMessages,omitempty"`
	// Conversation 为空是 console:<角色>（本地私聊）；给群聊会话时按群听众跑。只给 Go 壳的对照测试用，
	// 钉住群聊仍走 recentMessages 的锚定窗口（本地私聊已改成从上下文起点读，不看 recentMessages）
	Conversation string `json:"conversation,omitempty"`
	// ApproveInWorkdir 为真时，要问的审批里只在工作目录里的批准一次（ApproveInWorkdir 函数判），其余说明规则拒绝、让模型换法继续。
	// B2、B3 第一轮 yolo 下要问的一律取消，她停下来问"要继续吗"，量到的是审批不是长任务（主人 2026-10-28："可以，加上工作目录里一律批准"）
	ApproveInWorkdir bool `json:"approveInWorkdir,omitempty"`
	// TurnSeconds 是一句话最多等多久（到了按停止，记成超时）；0 是 600 秒
	TurnSeconds int `json:"turnSeconds,omitempty"`
}

// Line 是先存进库的一句历史（B4 的长历史）
type Line struct {
	Who  string `json:"who"` // owner / character
	Text string `json:"text"`
	At   int64  `json:"at"` // 毫秒时间戳；0 按先后编
}

// Step 是题目里的一步：说一句话，或者先手动压缩一次
type Step struct {
	Say string `json:"say,omitempty"`
	// StopAfterTool 大于 0：这一句里第 n 个工具结束以后按停止（B3 在两次工具调用之间停）。
	// 三个壳数法一样：只数真执行了的内置工具（被审批拦下、取消的不算，记忆插件的工具不算；omp 不审批，照数）
	StopAfterTool int `json:"stopAfterTool,omitempty"`
	// StopDuringTool 大于 0：这一句里第 n 个工具开始执行以后 StopDelayMs 毫秒按停止（B3 在工具正在跑时停），数法同上
	StopDuringTool int `json:"stopDuringTool,omitempty"`
	StopDelayMs    int `json:"stopDelayMs,omitempty"` // 0 是 1000
	// StopMatch 不空时，上面两种停法只数"工具名 + 空格 + 参数 JSON"里带其中任一段字的调用。B3 要停在发布脚本正在跑时：
	// 别的工具都不到一秒就跑完，按序号数停不到"正在跑"；匹配串写 "publish.ps1 -Name"，读脚本本身的那次不算
	// （原来的 "publish.ps1 " 排得掉 read 工具（参数里脚本名后面是引号），排不掉 `Get-Content tools/publish.ps1 -TotalCount 40`
	// 这类命令，2026-10-27 b3-07 Go 停在了它上面）。
	// 只比子串、不用正则，三个壳行为一样
	StopMatch []string `json:"stopMatch,omitempty"`
	Compact   bool     `json:"compact,omitempty"`
	// Expect 是这一句的期望答案（B4 问答；壳不看它，执行器判分用）
	Expect *Expect `json:"expect,omitempty"`
}

// Expect 是一问的期望：Answer 里任一种说法出现在回复里算答对（归一化后包含匹配）；Abstain 为真是历史里没有答案、该说不知道。
// NotAnswer 是"后来改了主意"的题里被改掉的旧值：回复里说到它就判"要人看"（她可能两个都说了、选的是旧的）
type Expect struct {
	Answer    []string `json:"answer,omitempty"`
	Abstain   bool     `json:"abstain,omitempty"`
	NotAnswer []string `json:"notAnswer,omitempty"`
}

// StopDelay 是工具开始以后等多久按停止
func (s Step) StopDelay() int {
	if s.StopDelayMs > 0 {
		return s.StopDelayMs
	}
	return 1000
}

// Counts 是这一次工具调用算不算进停止的计数（StopMatch 为空都算）
func (s Step) Counts(name, args string) bool {
	if len(s.StopMatch) == 0 {
		return true
	}
	text := name + " " + args
	return slices.ContainsFunc(s.StopMatch, func(m string) bool { return strings.Contains(text, m) })
}

// Result 是一道题跑完的结果
type Result struct {
	Shell string       `json:"shell"` // go / bun / omp
	Steps []StepResult `json:"steps"`
	Error string       `json:"error,omitempty"` // 壳本身没跑成（不是她做错）
}

// StepResult 是一步的结果
type StepResult struct {
	Reply    string `json:"reply"`
	Stopped  bool   `json:"stopped,omitempty"`
	TimedOut bool   `json:"timedOut,omitempty"`
	// StopTriggered 只记题设工具停止点确实触发；超时或别的取消不算。指针让旧结果的“没记录”区别于 false。
	StopTriggered *bool  `json:"stopTriggered,omitempty"`
	Error         string `json:"error,omitempty"`
	// Outcome 是这一句的回合结局（turns.outcome：done、stopped、error、budget_exhausted）；壳没记就是空
	Outcome string        `json:"outcome,omitempty"`
	Ms      int64         `json:"ms"`
	Tools   []ToolUse     `json:"tools"`
	Calls   []Call        `json:"calls"`
	Compact *CompactState `json:"compact,omitempty"`
}

// ToolUse 是一次工具调用
type ToolUse struct {
	Name    string `json:"name"`
	Args    string `json:"args"`
	Status  string `json:"status"` // done / error / denied / cancelled / …（照 steps 表）
	IsError bool   `json:"isError"`
	Preview string `json:"preview"`
}

// Call 是一次模型请求（壳自己记的；记账代理和 Magpie 另记一份）
type Call struct {
	TTFTMs     int64  `json:"ttftMs"`
	Ms         int64  `json:"ms"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cacheRead"`
	CacheWrite int64  `json:"cacheWrite"`
	Reasoning  int64  `json:"reasoning"`
	Error      string `json:"error,omitempty"`
}

// CompactState 是手动压缩的结果
type CompactState struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Summary 是这个会话最新一份成功的摘要原文（B4 直接看要紧的事实还在不在摘要里）
	Summary string `json:"summary,omitempty"`
}
