package ctlproto

import (
	"errors"
	"strings"
)

// CmdStats 是运行页的统计命令（规格-运行页 第三节）：只读，界面和命令行共用一份结构体。
// 子命令：usage、storage、quality、spans、resources；不带子命令的汇总还没做，回 invalid，不给假数据。
// 请求里的参数放在 Stats 一个字段里，别的命令带 Stats 服务端回 invalid。
const CmdStats = "stats"

// 统计命令的限制
const (
	StatsDefaultDays = 7    // 不写范围时看最近 7 天（含今天）
	StatsMaxDays     = 3660 // --days 的上限，约十年
	StatsMinYear     = 2000
	StatsMaxYear     = 2200
)

// stats 的子命令
const (
	StatsUsage = "usage"
)

// StatsBy 是 stats usage 的分组方式（--by）
const (
	ByTask  = "task"  // 按任务类别（默认）
	ByModel = "model" // 按模型
	ByDay   = "day"   // 按本地时区的天
	ByWeek  = "week"  // 按本地时区的周，周一起算
)

// ValidBy 是不是认识的分组方式
func ValidBy(s string) bool {
	switch s {
	case ByTask, ByModel, ByDay, ByWeek:
		return true
	}
	return false
}

// StatsArgs 是 stats 的可选参数。Days、Year、All 互斥（照 opencode 的 stats），一个都不写就是最近 StatsDefaultDays 天。
// 校验只在服务端做（"唯一的关口"），客户端原样放进请求
type StatsArgs struct {
	Days int    `json:"days,omitempty"`
	Year int    `json:"year,omitempty"`
	All  bool   `json:"all,omitempty"`
	By   string `json:"by,omitempty"`
	Full bool   `json:"full,omitempty"` // 加了才列到模型
	// Task、Failures 只给 stats quality：只看某个任务类别或模块、"最近的失败"列几条（0 用默认）
	Task     string `json:"task,omitempty"`
	Failures int    `json:"failures,omitempty"`
	// Turn 只给 stats spans：看哪一轮的时间线，last 是最近一轮
	Turn string `json:"turn,omitempty"`
}

// Validate 检查参数合不合：Days、Year、All 最多写一个；Days 在 1 到 StatsMaxDays，Year 在合理年份；By 要认识。
// 服务端在碰后端前调，统计服务自己也调一遍（界面直接用服务时不经过服务端）
func (a StatsArgs) Validate() error {
	n := 0
	if a.Days != 0 {
		n++
	}
	if a.Year != 0 {
		n++
	}
	if a.All {
		n++
	}
	if n > 1 {
		return errors.New("--days、--year、--all 只能写一个")
	}
	if a.Days != 0 && (a.Days < 1 || a.Days > StatsMaxDays) {
		return errors.New("--days 要在 1 到 3660 之间")
	}
	if a.Year != 0 && (a.Year < StatsMinYear || a.Year > StatsMaxYear) {
		return errors.New("--year 要写四位年份，比如 2026")
	}
	if a.By != "" && !ValidBy(a.By) {
		return errors.New("--by 只能是 task、model、day、week")
	}
	return nil
}

// 任务类别的稳定键。JSON 里用这些 ASCII 键，中文名只是显示用的 Label，不拿中文当键
const (
	TaskForeground    = "foreground"     // 前台回合
	TaskMemory        = "memory"         // 记忆整理
	TaskGuard         = "guard"          // 守卫
	TaskVision        = "vision"         // 识图
	TaskCompact       = "compact"        // 上下文压缩
	TaskChild         = "child"          // 子代理
	TaskAdvisor       = "advisor"        // 顾问、完成核验
	TaskGoal          = "goal"           // /goal 的评判
	TaskLoop          = "loop"           // /loop
	TaskLinkTranslate = "link_translate" // 链接展开的翻译
	TaskUnknown       = "unknown"        // 认不出的值：单列并带原文，不落进别的类
)

// UsageTokens 是一组 token 数。口径（规格-运行页 第二节第 3 条）：
//   - Input 是总输入 = 没命中缓存的输入 + CacheRead + CacheWrite，不是模型接口回的"未缓存输入"；
//   - Uncached、CacheRead、CacheWrite 是它的三个组成；
//   - Output 已经包含思考的 token，Reasoning 只是其中的思考部分（拿来看，不要再加到 Output 上）；
//   - 出了错的请求如果拿到了用量也算进来（那些 token 是真花了的），只有耗时不算失败的；
//   - 没拿到用量的请求（NoUsage）token 记 0 是"不知道"，不是真的 0，单独计数。
type UsageTokens struct {
	Input      int64 `json:"input"`
	Uncached   int64 `json:"uncached"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
}

// UsageStats 是一个分组（一个类别、一个模型、一天）的用量
type UsageStats struct {
	Runs    int64 `json:"runs"`
	Failed  int64 `json:"failed"`
	NoUsage int64 `json:"noUsage"` // 供应商没回用量（usage_seen 为假或 incomplete 为真）的请求数
	UsageTokens
	// 耗时只统计成功的请求：OKRuns 是有耗时的成功请求数，OKMs 是它们的总耗时，AvgMs = OKMs / OKRuns（OKRuns 为 0 时是 0）
	OKRuns int64   `json:"okRuns"`
	OKMs   int64   `json:"okMs"`
	AvgMs  int64   `json:"avgMs"`
	Hit    float64 `json:"cacheHit"` // 缓存命中率 = CacheRead / Input；Input 为 0 时是 0（先看 Input 再用）
}

// UsageModel 是类别下按模型的一行（--full 才有）
type UsageModel struct {
	Model string `json:"model"`
	UsageStats
}

// UsageTask 是一个任务类别的用量。没有记录的预留类别也在表里：Wired 为假、Runs 为 0 时写"未接入"，不写 0 次；
// Wired 为假而 Runs 大于 0 是历史数据（识图：Go 侧没有调用方，只有 Bun 时代的记录）
type UsageTask struct {
	Key    string       `json:"key"`
	Label  string       `json:"label"`
	Raw    string       `json:"raw,omitempty"` // Key 是 unknown 时：认不出的 purpose/stage 原文（截短、清过）
	Wired  bool         `json:"wired"`         // Go 侧有没有调用方在写这一类
	Models []UsageModel `json:"models,omitempty"`
	UsageStats
}

// UsageBucket 是一天或一周的合计。Date 是本地时区的日期 2006-01-02；周是周一的日期
type UsageBucket struct {
	Date string `json:"date"`
	UsageStats
}

// UsageRange 是查哪段时间：FromMS 含、ToMS 不含，毫秒时间戳；Kind、Label、Zone 只是说明。
// 界面可以自己填 FromMS、ToMS（今天、本月、自定义）；命令行的 --days、--year、--all 由 runstats.Service.RangeOf 算出来
type UsageRange struct {
	Kind   string `json:"kind"` // days / year / all / custom
	Label  string `json:"label"`
	FromMS int64  `json:"fromMs"`
	ToMS   int64  `json:"toMs"`
	Zone   string `json:"zone"`
}

// UsageReport 是 stats usage 的 data
type UsageReport struct {
	Range  UsageRange    `json:"range"`
	By     string        `json:"by"`
	Source string        `json:"source,omitempty"` // rollup（汇总表）或 raw（直接查原始行），结果一样，只是快慢
	Totals UsageStats    `json:"totals"`
	Tasks  []UsageTask   `json:"tasks,omitempty"`
	Models []UsageModel  `json:"models,omitempty"`
	Days   []UsageBucket `json:"days,omitempty"`  // By 是 day 时：按日期升序，没有记录的日子不列
	Weeks  []UsageBucket `json:"weeks,omitempty"` // By 是 week 时：按周一的日期升序
}

// Shape 按 --by 和 --full 裁出要给的部分。服务端查全量（分组行数很少），出去前裁，回复里不带用不着的行：
//   - task（默认）：Tasks，--full 才在每个类别下列模型；
//   - model：Models（全部类别合并）；
//   - day：Days；week：Weeks。
//
// Totals 总是在。by 为空按 task；不认识的 by 原样当 task 处理前应已被 ValidBy 拦下
func (r UsageReport) Shape(by string, full bool) UsageReport {
	if by == "" {
		by = ByTask
	}
	r.By = by
	switch by {
	case ByModel:
		r.Tasks, r.Days, r.Weeks = nil, nil, nil
	case ByDay:
		r.Tasks, r.Models, r.Weeks = nil, nil, nil
	case ByWeek:
		r.Tasks, r.Models, r.Days = nil, nil, nil
	default:
		r.Models, r.Days, r.Weeks = nil, nil, nil
		if !full {
			tasks := make([]UsageTask, len(r.Tasks))
			copy(tasks, r.Tasks)
			for i := range tasks {
				tasks[i].Models = nil
			}
			r.Tasks = tasks
		}
	}
	return r
}

// CleanRaw 是未知类别原文的显示写法：去掉控制字符、截短
func CleanRaw(s string) string { return Truncate(Clean(strings.TrimSpace(s)), 80) }
