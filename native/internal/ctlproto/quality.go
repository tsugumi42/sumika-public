package ctlproto

import (
	"errors"
	"fmt"
)

// 运行页质量标签的数据（规格-运行页 第七节）。界面和 sumika-ctl stats quality 共用这份结构，不各写一份。
// 报告里不放会话名、号码、URL、令牌、模型回复原文：场合只写"本地私聊 / 本地群聊 / QQ 私聊 / QQ 群"，
// 失败摘要是写入或读出时脱敏、最多 500 字节的短句。

// StatsQuality 是 stats 的质量子命令
const StatsQuality = "quality"

// 质量标签的限制
const (
	StatsDefaultFailures = 50  // "最近的失败"默认列几条
	StatsMaxFailures     = 500 // --failures 的上限
	QualityUnknownAlarm  = 0.1 // unknown 类别占失败的比例超过它就标红（规格第七节第 2 条）
	QualityThinSamples   = 20  // 样本少于它的"最慢的环节"灰显，别被个别请求误导
	QualitySlowRows      = 10  // "最慢的环节"列几行
)

// 模型调用以外的模块键（task_runs.module）。和 internal/taskrun 的常量一一对应，runstats 的测试对一遍；
// 这里不 import taskrun，协议包保持精简
const (
	ModuleSend         = "send"          // 发送（QQ）
	ModuleLink         = "link"          // 链接取数与展开
	ModuleMedia        = "media"         // 媒体下载与转码
	ModuleBackfill     = "backfill"      // 补录（还没接入）
	ModuleMemoryPass   = "memory_pass"   // 记忆整理一轮（还没接入）
	ModuleStorageClean = "storage_clean" // 存储清理（还没接入）
	ModuleHooks        = "hooks"         // 用户钩子
)

// QualityModules 是模块表，顺序就是显示顺序。Wired 说的是"Go 侧有没有调用方在写 task_runs"：
// 为假的模块在没有记录时显示"未接入"，不显示 0 次失败
var QualityModules = []QualityModule{
	{ModuleSend, "发送（QQ）", true},
	{ModuleLink, "链接取数", true},
	{ModuleMedia, "媒体下载与转码", true},
	{ModuleHooks, "用户钩子", true},
	{ModuleBackfill, "补录", false},
	{ModuleMemoryPass, "记忆整理一轮", false},
	{ModuleStorageClean, "存储清理", false},
}

// QualityModule 是一个模块的固定信息
type QualityModule struct {
	Key   string
	Label string
	Wired bool
}

// ValidQualityTask 是不是 --task 认识的键：任务类别（TaskForeground 等）、模块键，或 TaskUnknown
func ValidQualityTask(s string) bool {
	switch s {
	case TaskForeground, TaskMemory, TaskGuard, TaskVision, TaskCompact, TaskChild, TaskAdvisor, TaskGoal, TaskLoop, TaskLinkTranslate, TaskUnknown:
		return true
	}
	for _, m := range QualityModules {
		if m.Key == s {
			return true
		}
	}
	return false
}

// ValidateQuality 检查 stats quality 的参数：只认 --days / --all（不认 --year、--by、--full，质量没有这些维度），
// --task 要是认识的键，--failures 在 0 到 StatsMaxFailures（0 表示默认）。服务端在碰后端前调
func (a StatsArgs) ValidateQuality() error {
	if a.Year != 0 {
		return errors.New("stats quality 不接受 --year（用 --days 或 --all）")
	}
	if a.By != "" || a.Full || a.Turn != "" {
		return errors.New("stats quality 不接受 --by、--full、--turn")
	}
	if a.Task != "" && !ValidQualityTask(a.Task) {
		return fmt.Errorf("--task 不认识 %q：要写任务类别（foreground、memory、guard、vision、compact、advisor、goal、unknown 等）或模块（send、link、media 等）", CleanRaw(a.Task))
	}
	if a.Failures < 0 || a.Failures > StatsMaxFailures {
		return fmt.Errorf("--failures 要在 0 到 %d 之间", StatsMaxFailures)
	}
	return a.Validate()
}

// QualityStats 是一组任务的成败和耗时。口径：
//   - Runs = OK + Failed + Timeout + Cancelled + Degraded + Skipped；
//   - FailRate = (Failed + Timeout) / (OK + Failed + Timeout + Degraded)：被停下（Cancelled）和没做（Skipped）不是失败也不是成功，
//     不进分母；分母为 0 时是 0（先看 Runs 再用）；
//   - 耗时只统计成功的请求（失败、超时单独看次数，不混进分位），分位用 nearest-rank，Samples 是参与分位的个数；
//   - Retries：模型请求是"出错后又发了一次"的次数（stage 为 reply-retry 的行），模块是 attempts-1 的合计。
type QualityStats struct {
	Runs      int64   `json:"runs"`
	OK        int64   `json:"ok"`
	Failed    int64   `json:"failed"`
	Timeout   int64   `json:"timeout"`
	Cancelled int64   `json:"cancelled"`
	Degraded  int64   `json:"degraded"` // 做成了但变样了（图退成文字、只存不回等）
	Skipped   int64   `json:"skipped"`  // 没做：不支持、被挡
	Retries   int64   `json:"retries"`
	FailRate  float64 `json:"failRate"`
	P50Ms     int64   `json:"p50Ms"`
	P95Ms     int64   `json:"p95Ms"`
	Samples   int64   `json:"samples"`
	Sampled   bool    `json:"sampled,omitempty"` // 样本超过上限，分位是抽样算的
}

// QualityReason 是一个失败原因类别在一行里的次数。Class 是"类别"（llm.Class*、taskrun.Class*），原样显示
type QualityReason struct {
	Class  string  `json:"class"`
	Count  int64   `json:"count"`
	Share  float64 `json:"share"`  // 占这一行所有非成功记录的比例
	LastMS int64   `json:"lastMs"` // 最近一次的时间
}

// QualityRow 是总表的一行：一个任务类别（模型调用）或一个模块（task_runs）
type QualityRow struct {
	Kind    string          `json:"kind"` // model 或 module
	Key     string          `json:"key"`
	Label   string          `json:"label"`
	Raw     string          `json:"raw,omitempty"` // Key 是 unknown 时：认不出的 purpose/stage（截短、清过）
	Wired   bool            `json:"wired"`
	Reasons []QualityReason `json:"reasons,omitempty"` // 次数从多到少
	QualityStats
}

// QualitySlow 是"最慢的环节"的一行：任务类别 × 模型，或模块，或工具名（Kind 是 model、module、tool）
type QualitySlow struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Label   string `json:"label"`
	Model   string `json:"model,omitempty"`
	P50Ms   int64  `json:"p50Ms"`
	P95Ms   int64  `json:"p95Ms"`
	Samples int64  `json:"samples"`
	Thin    bool   `json:"thin,omitempty"` // 样本少于 QualityThinSamples：灰显
	Sampled bool   `json:"sampled,omitempty"`
}

// 场合的显示写法（词汇表：本地私聊、本地群聊、QQ 私聊、QQ 群）；认不出的写"未知场合"，不猜
const (
	ScenePlaceLocalPrivate = "本地私聊"
	ScenePlaceLocalGroup   = "本地群聊"
	ScenePlaceQQPrivate    = "QQ 私聊"
	ScenePlaceQQGroup      = "QQ 群"
	ScenePlaceWhisper      = "悄悄话" // 悄悄话处理那一轮（规格-群聊引用和路由）：会话是她的悄悄话会话，不是和主人的本地私聊，运行单列
	ScenePlaceUnknown      = "未知场合"
)

// QualityFailure 是"最近的失败"的一行。Summary 已脱敏、最多 500 字节；模型回复原文类的失败（输出不合规、打转）只留前缀
type QualityFailure struct {
	ID            string `json:"id,omitempty"` // 模型请求的 id；模块没有
	AtMS          int64  `json:"atMs"`
	Kind          string `json:"kind"` // model 或 module
	Key           string `json:"key"`
	Label         string `json:"label"`
	Model         string `json:"model,omitempty"`
	Outcome       string `json:"outcome"` // failed / timeout / degraded / skipped
	Class         string `json:"class"`
	Place         string `json:"place,omitempty"` // 场合；模块没有会话时为空
	Ms            int64  `json:"ms"`              // 耗时，没有是 -1
	Attempts      int    `json:"attempts,omitempty"`
	Summary       string `json:"summary,omitempty"`
	TurnID        string `json:"turnId,omitempty"` // 对应哪个回合
	RouteProvider string `json:"routeProvider,omitempty"`
	RouteModel    string `json:"routeModel,omitempty"`
}

// QualityOutcome 是回合结局分布里的一项（done、stopped、error、budget_exhausted 等）
type QualityOutcome struct {
	Outcome string `json:"outcome"`
	Count   int64  `json:"count"`
}

// QualityTurns 是回合层：结局分布和回合耗时（只算 done 的回合，结束时间减开始时间）
type QualityTurns struct {
	Total    int64            `json:"total"`
	Outcomes []QualityOutcome `json:"outcomes,omitempty"`
	P50Ms    int64            `json:"p50Ms"`
	P95Ms    int64            `json:"p95Ms"`
	Samples  int64            `json:"samples"`
}

// QualityTool 是一个工具的成败（照 opencode 的工具表）：Done 成功，Failed 出错，Incomplete 没有走到结局（过期、没结束）
type QualityTool struct {
	Name       string `json:"name"`
	Runs       int64  `json:"runs"`
	Done       int64  `json:"done"`
	Failed     int64  `json:"failed"`
	Incomplete int64  `json:"incomplete"`
	MedianMs   int64  `json:"medianMs"` // 成功的步骤的中位耗时；没有是 0
	Samples    int64  `json:"samples"`
}

// QualityUnknown 是 unknown 类别占比：这个比例本身是质量页的一个指标，超过 QualityUnknownAlarm 标红，提醒去补分类
type QualityUnknown struct {
	Count int64   `json:"count"`
	Total int64   `json:"total"` // 分母：所有失败、超时、退化的记录
	Share float64 `json:"share"`
	Alarm bool    `json:"alarm"`
}

// QualityReport 是 stats quality 的 data
type QualityReport struct {
	Range    UsageRange       `json:"range"`
	Task     string           `json:"task,omitempty"` // 只看这一类时的键
	Totals   QualityStats     `json:"totals"`
	Rows     []QualityRow     `json:"rows"`
	Slowest  []QualitySlow    `json:"slowest,omitempty"`
	Unknown  QualityUnknown   `json:"unknown"`
	Recent   []QualityFailure `json:"recent,omitempty"`
	RecentOf int64            `json:"recentOf"` // 范围内符合条件的失败一共多少条（Recent 只是最近的几条）
	Turns    QualityTurns     `json:"turns"`
	Tools    []QualityTool    `json:"tools,omitempty"`
}
