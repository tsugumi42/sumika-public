package linkplugin

// 链接插件自己的日志（规格-插件两层 第二点五节第 4 条，格式见规格-Bun后台第5批 9.7）：
//   - 取数：每个链接一条，site 平台、route 来源、url 清洗后的链接、ms；失败的 kind、error 带 HTTP 状态和底层原因。命中缓存记 ok 加 note cache；
//   - 展开发送：每个链接一条，route 是 send；没发的 skip、note 写原因；发了失败的 fail 带发送错误；结果不明 fail 加 kind unknown。
// 记录交给宿主（Registry.Trace），宿主管格式和文件；插件没启用（还没 Setup）时什么也不记。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/plugintrace"
)

// RouteSend 是展开发送那一段的 route
const RouteSend = "send"

// 没发的原因（note）。写成固定的几种，汇总时按 note 看得出是哪一类
const (
	noteBackfill   = "补录"
	noteDuplicate  = "去重（同一条重复送到）"
	noteSameLink   = "去重（同一条消息里重复的链接）"
	noteExpandOff  = "开关关（自动展开没开）"
	noteNotListed  = "白名单外"
	noteBlacklist  = "黑名单内"
	noteNotAdmit   = "不能触发展开（发消息的人或号不对）"
	noteSender     = "发消息的人不在这个群的展开名单里（或在排除名单里）"
	noteKindOff    = "开关关（网站或页面类型）"
	noteFetchFail  = "取数失败"
	noteNoContent  = "没取到内容"
	noteEmpty      = "结果为空"
	noteTimeout    = "超时"
	noteCanceled   = "取消了（源消息撤回、展开关了或在关闭）"
	notePermitGone = "开关关或白名单外（展开许可已撤销）"
	noteOverLimit  = "超出一条消息的展开上限"
	noteQueueFull  = "队列满"
	noteStoreAbort = "同一条消息里前面的链接记关联失败，不再往下发"
	// noteRetryTimeout 是取数每条线路都超时、隔 5 秒整条重试过一次（规格-链接发送改版 C3）
	noteRetryTimeout = "超时后重试了一次"
)

// Blocked 是“这次不许发”的原因（宿主的发送许可返回它）：Why 写进插件日志的 note，比如“闸门：QQ 发送已暂停”“群档位不是说话”。
// 返回 Blocked 的发送在日志里记 skip（没发），不当发送失败
type Blocked struct {
	Why string
	Err error
}

func (b *Blocked) Error() string {
	if b.Err != nil {
		return b.Err.Error()
	}
	return b.Why
}
func (b *Blocked) Unwrap() error { return b.Err }

// skipNote 说一个发送许可、准备、提交的错误是不是“没发”（不许发、取消、超时），是的话给出 note
func skipNote(err error) (string, bool) {
	var b *Blocked
	switch {
	case errors.As(err, &b):
		return b.Why, true
	case errors.Is(err, context.DeadlineExceeded):
		return noteTimeout, true
	case errors.Is(err, context.Canceled):
		return noteCanceled, true
	}
	return "", false
}

// setTrace 换上（或清掉）宿主给的记录函数
func (p *Plugin) setTrace(f func(plugintrace.Event)) {
	if f == nil {
		p.tracer.Store(nil)
		return
	}
	p.tracer.Store(&f)
}

func (p *Plugin) trace(e plugintrace.Event) {
	if f := p.tracer.Load(); f != nil {
		(*f)(e)
	}
}

// traceFetch 记一个链接的取数（9.7“链接读取”）
func (p *Plugin) traceFetch(l linkreader.Link, began time.Time, r linkreader.Result, err error) {
	e := plugintrace.Event{Site: l.Platform, URL: l.Clean, Took: p.opt.Now().Sub(began), Timed: true}
	if e.Took < 0 {
		e.Timed = false
	}
	var fe *linkreader.FetchError
	switch {
	case err == nil:
		e.Result, e.Route = plugintrace.OK, r.Source
		if r.Platform != "" {
			e.Site = r.Platform
		}
		if r.CleanURL != "" {
			e.URL = r.CleanURL
		}
		if !r.FetchedAt.IsZero() && r.FetchedAt.UnixMilli() < began.UnixMilli() {
			e.Note = "cache" // 结果的取数时间早于这次开始：命中缓存
		}
	case errors.As(err, &fe):
		e.Result, e.Route, e.Kind, e.Error = plugintrace.Fail, fe.Source, fe.Kind, reason(err)
	case errors.Is(err, context.DeadlineExceeded):
		e.Result, e.Kind, e.Error = plugintrace.Fail, "timeout", reason(err)
	case errors.Is(err, context.Canceled):
		e.Result, e.Kind = plugintrace.Skip, "canceled"
	default:
		e.Result, e.Kind, e.Error = plugintrace.Fail, "error", reason(err)
	}
	// 全部超时后整条重试过：成功、失败都写进 note；命中缓存的行照旧写 cache
	if e.Note != "cache" && (r.RetriedAfterTimeout || (fe != nil && fe.RetriedAfterTimeout)) {
		e.Note = noteRetryTimeout
	}
	p.trace(e)
}

// sendRow 是展开发送那一行；site、url 优先用取到的结果（短链解析后才知道平台）
func sendRow(l linkreader.Link, r *linkreader.Result) plugintrace.Event {
	e := plugintrace.Event{Site: l.Platform, Route: RouteSend, URL: l.Clean}
	if r != nil && r.Platform != "" {
		e.Site = r.Platform
	}
	if r != nil && r.CleanURL != "" {
		e.URL = r.CleanURL
	}
	return e
}

// traceSkip 记一个没发的链接
func (p *Plugin) traceSkip(l linkreader.Link, r *linkreader.Result, note string) {
	e := sendRow(l, r)
	e.Result, e.Note = plugintrace.Skip, note
	p.trace(e)
}

// traceSendErr 记一个发送许可、准备、提交出错的链接：不许发、取消、超时记 skip，别的记 fail（kind 是哪一步）
func (p *Plugin) traceSendErr(l linkreader.Link, r *linkreader.Result, began time.Time, step string, err error) {
	if note, skip := skipNote(err); skip {
		p.traceSkip(l, r, note)
		return
	}
	e := sendRow(l, r)
	e.Took, e.Timed = p.opt.Now().Sub(began), true
	e.Result, e.Kind, e.Error = plugintrace.Fail, step, reason(err)
	p.trace(e)
}

// traceDelivered 记一条发出去的展开：全部确认 ok（退了文字版的 note 写明）；有段结果不明 fail 加 kind unknown；没送到 fail 带原因
func (p *Plugin) traceDelivered(l linkreader.Link, r *linkreader.Result, began time.Time, rep sendReport) {
	e := sendRow(l, r)
	e.Took, e.Timed = p.opt.Now().Sub(began), true
	switch {
	case rep.delivered && rep.unsure:
		e.Result, e.Kind, e.Note = plugintrace.Fail, "unknown", "结果不明，可能已经发出去了（不补发）"
	case rep.delivered && rep.fallback:
		e.Result, e.Note = plugintrace.OK, "图发不出去，发了文字版"
	case rep.delivered:
		e.Result = plugintrace.OK
	case rep.broken:
		e.Result, e.Kind, e.Error = plugintrace.Fail, "aborted", rep.why
	default:
		e.Result, e.Kind, e.Error = plugintrace.Fail, "rejected", rep.why
	}
	p.trace(e)
}
