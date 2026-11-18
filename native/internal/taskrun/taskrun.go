// Package taskrun 记录模型调用以外的任务（发送、链接取数等）的耗时和结局，并提供统一的 Degrade：
// 退回、不支持、失败时写一行"模块 · 类别"的日志，按需提醒，同时记一行 task_runs（规格-运行页 第七节、盘点-静默失败"统一的做法"）。
//
// 类别用各模块自己的字符串常量（本文件只放起步词表，已有的哨兵错误优先，不新造）；页面不造全局错误树，原样显示"模块 · 类别"。
// 本包只依赖 internal/alert，不 import store、各插件：真正写库的是装配处传进来的 Sink，插件只拿到 *Degrader，
// 它是 nil 也能调（什么都不做），所以没接线的地方、测试里不用特意造。
package taskrun

import (
	"time"

	"sumika-native/internal/alert"
)

// 结局
const (
	OutcomeOK        = "ok"
	OutcomeFailed    = "failed"
	OutcomeTimeout   = "timeout"
	OutcomeCancelled = "cancelled"
	OutcomeDegraded  = "degraded" // 做成了，但变样了（图退成文字、只存不回等）
	OutcomeSkipped   = "skipped"  // 没做：不支持、被挡
)

// 模块。显示给人看的名字在界面一侧，这里是稳定的 ASCII 键
const (
	ModuleSend  = "send"  // 发送（QQ）
	ModuleLink  = "link"  // 链接取数与展开
	ModuleMedia = "media" // 媒体下载与转码（链接展开里的图片、动图、视频）
	ModuleHooks = "hooks" // 用户钩子（规格-用户钩子 第六节）
)

// 用户钩子的原因类别
const (
	ClassCheckFailed   = "check_failed"   // 钩子说检查没过（退出码 2、decision:block）
	ClassExitNonzero   = "exit_nonzero"   // 退出码不是 0 也不是 2
	ClassSpawnFailed   = "spawn_failed"   // 命令没跑起来
	ClassInvalidOutput = "invalid_output" // 钩子回的 JSON 用不了
	ClassLoopLimit     = "loop_limit"     // 结束前钩子到了续跑上限还没过
	ClassConfigInvalid = "config_invalid" // 钩子配置有问题
	ClassUntrusted     = "untrusted"      // 项目级钩子所在目录不受信任，没跑
)

// 发送的原因类别
const (
	ClassUnsupported    = "unsupported"
	ClassRetryExhausted = "retry_exhausted"
	ClassRejected       = "rejected" // 协议端明确拒绝这份内容：不重试，由上层决定换形式
	ClassReceiptUnknown = "receipt_unknown"
	ClassSendCheck      = "send_check"
	ClassIntentMissing  = "intent_missing"
	ClassClamped        = "clamped" // 被钳成只存不回（意图、守卫、登录状态等），原因写在详情里
)

// 链接的原因类别
const (
	ClassNotFound         = "not_found"
	ClassLoginRequired    = "login_required"
	ClassRateLimited      = "rate_limited"
	ClassNetwork          = "network"
	ClassTimeout          = "timeout"
	ClassToolsMissing     = "tools_missing"
	ClassCapability       = "capability"
	ClassContainerInvalid = "container_invalid"
	ClassBlocked          = "blocked" // 被开关关掉、超过大小上限等：不是出错，是规则不让
	ClassQueueFull        = "queue_full"
	ClassRotateRetry      = "rotate_retry" // 图被协议端拒收后转 180° 重发一次（ADR-323）；结局 ok 是重发成功，failed 是没重发成或仍被拒
	ClassTranslate        = "translate"    // 链接展开的翻译没出来（超时、模型报错、没配模型），只发了原文；结局 timeout 或 failed
	ClassFormDegrade      = "form_degrade" // 图被协议端拒收后换形式：直接发 → 合并转发 → 转发里转 180° → 文字；每一步一行，结局 ok、failed（被拒、没准备成）、degraded（结果不明，不换形式）
	ClassUnknown          = "unknown"
)

// Row 是要记的一行，和 task_runs 一一对应
type Row struct {
	At       time.Time
	Module   string
	Outcome  string
	Class    string
	Ms       int64 // 小于 0 表示没有耗时
	Attempts int
	RootID   string
	Detail   string // 已脱敏、最多 500 字节
}

// Sink 是写库的地方。写失败只记日志，不能反过来影响被记录的任务
type Sink interface {
	AddTaskRun(Row) error
}

// Degrader 把日志、提醒、task_runs 三件事合成一个调用。零个字段都可以不填：没有 Logf 就不写日志，没有 Alerts 就不提醒，
// 没有 Sink 就不记行。nil 的 *Degrader 调什么都是空操作
type Degrader struct {
	Logf   func(format string, args ...any)
	Alerts *alert.Hub
	Sink   Sink
	Redact func(string) string // 装配处传 app.RedactLog 那一路；Detail 在它之后还会再过一遍自己的脱敏
	Now    func() time.Time
}

func (d *Degrader) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Opts 是一次 Degrade 的可选项
type Opts struct {
	Outcome  string       // 空表示 degraded
	Alert    *alert.Alert // 非空就 Raise（同一个 Key 去重）；详情里不带原文
	Ms       int64        // 0 表示没有耗时
	Attempts int
	RootID   string
	Tag      string // 日志里附在类别后面的短标签（"图片"、"视频"这类固定词，不放原文）
}

// Degrade 记一次退回、不支持或失败：写一行"模块 · 类别"（不带原文），按需提醒，记一行 task_runs。
// err 只用来做详情（脱敏、截断后进 task_runs.detail，日志里不放）；可以是 nil。
func (d *Degrader) Degrade(module, class string, err error, o Opts) {
	if d == nil {
		return
	}
	outcome := o.Outcome
	if outcome == "" {
		outcome = OutcomeDegraded
	}
	if d.Logf != nil {
		if o.Tag != "" {
			d.Logf("%s · %s（%s，%s）", module, class, outcome, o.Tag)
		} else {
			d.Logf("%s · %s（%s）", module, class, outcome)
		}
	}
	if o.Alert != nil && d.Alerts != nil {
		d.Alerts.Raise(*o.Alert)
	}
	d.record(Row{At: d.now(), Module: module, Outcome: outcome, Class: class, Ms: msOf(o.Ms), Attempts: o.Attempts, RootID: o.RootID, Detail: d.detail(class, err)})
}

// msOf：0 表示没有耗时，记成 -1（库里是 NULL）
func msOf(ms int64) int64 {
	if ms == 0 {
		return -1
	}
	return ms
}

func (d *Degrader) detail(class string, err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if d.Redact != nil {
		s = d.Redact(s)
	}
	return Detail(class, s)
}

func (d *Degrader) record(r Row) {
	if d.Sink == nil {
		return
	}
	if err := d.Sink.AddTaskRun(r); err != nil && d.Logf != nil {
		d.Logf("%s · 记运行记录失败", r.Module)
	}
}

// Span 是一次有开始有结束的任务：Start 记开始时间，End 记结局和耗时
type Span struct {
	d        *Degrader
	module   string
	start    time.Time
	Attempts int
	RootID   string
}

// Start 开始计时。d 为 nil 时返回的 Span 仍可用，End 什么都不做
func (d *Degrader) Start(module string) *Span {
	s := &Span{d: d, module: module, Attempts: 1}
	if d != nil {
		s.start = d.now()
	}
	return s
}

// End 记结局。outcome 是 ok 时 class 必须空（传了也丢掉），不能把类别写进成功的行；err 只进脱敏后的详情
func (s *Span) End(outcome, class string, err error) {
	if s == nil || s.d == nil {
		return
	}
	s.d.finish(s.module, s.start, s.Attempts, s.RootID, outcome, class, err)
}

// Finish 记一个已经结束的任务：started 是开始时间，耗时算到现在。给开始时刻不在同一个函数里的调用方用（Span 也走它）。
// 和 Degrade 的区别：只记一行 task_runs，不写日志、不提醒；成功的任务用它。nil 的 *Degrader 什么都不做
func (d *Degrader) Finish(module string, started time.Time, attempts int, outcome, class string, err error) {
	d.finish(module, started, attempts, "", outcome, class, err)
}

func (d *Degrader) finish(module string, started time.Time, attempts int, root, outcome, class string, err error) {
	if d == nil {
		return
	}
	if outcome == OutcomeOK {
		class = ""
	}
	d.record(Row{At: started, Module: module, Outcome: outcome, Class: class, Ms: d.now().Sub(started).Milliseconds(), Attempts: attempts, RootID: root, Detail: d.detail(class, err)})
}
