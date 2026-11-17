// Package runstats 是运行页的统计服务（规格-运行页）：只读运行记录库，算出用量等统计，界面和 sumika-ctl 共用。
//
// 第 1 步只有用量（Usage）。统计查询一律走只读连接，只碰窄列，不 select model_runs 里
// sections_json、injected_json、context_json 那三列大文本（规格第九节）。
package runstats

import "sumika-native/internal/ctlproto"

// TaskMeta 是一个任务类别的固定信息。Wired 说的是"Go 侧有没有调用方在写这一类用量"：
// 为假的类别在没有记录时显示"未接入"，不显示 0（规格第九节"不许为了看起来有数给预留行填 0"）
type TaskMeta struct {
	Key   string
	Label string
	Wired bool
}

// Tasks 是用量页的类别表，顺序就是显示顺序。认不出的值不在这里，单列在最后（TaskUnknown）
var Tasks = []TaskMeta{
	{ctlproto.TaskForeground, "前台回合", true},
	{ctlproto.TaskMemory, "记忆整理", true},
	{ctlproto.TaskGuard, "守卫", true},
	{ctlproto.TaskVision, "识图", false}, // Go 侧没有识图调用，只有 Bun 时代的历史行；接入时必须走 modelcall
	{ctlproto.TaskCompact, "上下文压缩", true},
	{ctlproto.TaskChild, "子代理", false},
	{ctlproto.TaskAdvisor, "顾问、完成核验", true},
	{ctlproto.TaskGoal, "/goal 评判", false}, // goal_judge 已是合法 purpose，但 app 里还没有装配 goal
	{ctlproto.TaskLoop, "/loop", false},
	{ctlproto.TaskLinkTranslate, "链接翻译", true},
}

// purposeTask 是 Go 内核写的 purpose（kernel.Invocation.Kind，turnbudget.ValidInvocation 的合法值）对应的类别
var purposeTask = map[string]string{
	"foreground":     ctlproto.TaskForeground,
	"memory":         ctlproto.TaskMemory,
	"guard":          ctlproto.TaskGuard,
	"compact":        ctlproto.TaskCompact,
	"child":          ctlproto.TaskChild,
	"advisor":        ctlproto.TaskAdvisor,
	"verify":         ctlproto.TaskAdvisor,
	"goal_judge":     ctlproto.TaskGoal,
	"goal":           ctlproto.TaskGoal,
	"link_translate": ctlproto.TaskLinkTranslate,
}

// stageTask 是没有 purpose 的行（Bun 时代和导入的历史行）按 stage 归的类别。
// Go 自己写的行 stage 是 reply、reply-retry、compact，或和 purpose 相同的内核类型
var stageTask = map[string]string{
	"reply":             ctlproto.TaskForeground,
	"reply-retry":       ctlproto.TaskForeground,
	"summarize":         ctlproto.TaskMemory,
	"learn-expression":  ctlproto.TaskMemory,
	"review-expression": ctlproto.TaskMemory,
	"vision":            ctlproto.TaskVision,
	"guard":             ctlproto.TaskGuard,
	"compact":           ctlproto.TaskCompact,
	"foreground":        ctlproto.TaskForeground,
	"memory":            ctlproto.TaskMemory,
	"child":             ctlproto.TaskChild,
	"advisor":           ctlproto.TaskAdvisor,
	"verify":            ctlproto.TaskAdvisor,
	"goal_judge":        ctlproto.TaskGoal,
	"link_translate":    ctlproto.TaskLinkTranslate,
}

// TaskOf 算一行模型请求属于哪个任务类别，返回 ctlproto.Task* 之一。规则：
//   - purpose 不空就以它为准（Go 内核写的行，turn/loop.go 的 reply 行 purpose 是 foreground）；
//     purpose 不认识就是 unknown，不回头用 stage 猜；
//   - purpose 空（Bun 时代、导入的历史行）按 stage；
//   - 认不出的一律 unknown，由调用方带着原文单列，不许落进别的类。
//
// 页面、命令行和测试都只用这一个函数，SQL 只按 (purpose, stage, model, 天) 分组。
func TaskOf(purpose, stage string) string {
	if purpose != "" {
		if k, ok := purposeTask[purpose]; ok {
			return k
		}
		return ctlproto.TaskUnknown
	}
	if k, ok := stageTask[stage]; ok {
		return k
	}
	return ctlproto.TaskUnknown
}

// rawOf 是认不出的行在页面上显示的原文：purpose/stage
func rawOf(purpose, stage string) string {
	switch {
	case purpose == "":
		return ctlproto.CleanRaw(stage)
	case stage == "" || stage == purpose:
		return ctlproto.CleanRaw(purpose)
	}
	return ctlproto.CleanRaw(purpose + "/" + stage)
}
