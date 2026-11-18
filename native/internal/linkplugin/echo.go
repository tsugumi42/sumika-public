package linkplugin

// 等回声、迟到的回声、图被吞后的撤回、两个实验的退回记录（规格-链接发送改版 A1、A4、A6、A7）。
//
// 回包分三种：明确失败当场换形式；成功带消息号当场算到了；没确认送达（协议端没确认、没回包、超时、写出后断线）才等回声。
// 等回声不轮询、不睡：出口认领回声时叫醒等待方（channel.Outlet.AwaitPart，按出口的时钟计时），默认等 2 秒
// （linkReader.expand.send.echoWaitMS）；到点没回声就当这一条没送到，和“这一张被拒”一样换形式。
// 迟到的回声（等过、放弃了才到）仍由出口认领，出口通过按来源登记的回调告诉插件，插件记一行日志；
// 小图不撤回已补发的（用户接受这个取舍），大图（≥ 2 MiB）开了看守，撤回后补发的那份，群里只剩一份（B15，late.go）。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
)

// echoOutbound 是能等回声的出口（宿主的 *channel.AutomationOutlet 实现它；没实现的出口当作永远等不到回声）
type echoOutbound interface {
	AwaitPart(ctx context.Context, planID string, idx int, wait time.Duration) (channel.EchoNote, bool)
	OnClaim(fn func(channel.EchoClaimed))
}

// RouteEcho、RouteRecall 是插件日志里等回声、撤回那几行的 route
const (
	RouteEcho   = "echo"
	RouteRecall = "recall"
)

// RecallWithin 是撤回的时限：从这一段第一次调 Send 起 2 分钟（QQ 普通成员撤回自己消息的时限）；不做成配置，放宽没有意义。
// 插件和宿主的撤回兜底都按它判
const RecallWithin = 2 * time.Minute

// quirkTTL 是“这个会话的合并转发里放不了视频 / 引用”记多久
const quirkTTL = 6 * time.Hour

// 插件日志的 note 和 kind
const (
	noteEchoArrived  = "回声到达"
	noteEchoLate     = "回声迟到"
	noteRecall       = "撤回只剩文字的一条"
	noteVideoBack    = "视频退回单独一条"
	noteReplyBack    = "引用退回旧排法"
	kindEchoTimeout  = "echo_timeout"
	kindFwdVideo     = "forward_video_unsupported"
	kindFwdReply     = "forward_reply_unsupported"
	kindRecallRefuse = "refused"
)

// ---- 计划号 → 哪次展开（迟到的回声查它） ----

type planEntry struct {
	site, url string
	until     time.Time
}

// planBook 记最近 5 分钟里展开发出去的计划是哪个链接的（迟到回声的日志要写 site、url）；不为每段起 goroutine 等
type planBook struct {
	mu sync.Mutex
	m  map[string]planEntry
}

const planBookMax = 512

func (b *planBook) put(id string, e planEntry, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]planEntry{}
	}
	for k, v := range b.m {
		if !now.Before(v.until) {
			delete(b.m, k)
		}
	}
	if len(b.m) >= planBookMax {
		var oldest string
		for k, v := range b.m {
			if oldest == "" || v.until.Before(b.m[oldest].until) {
				oldest = k
			}
		}
		delete(b.m, oldest)
	}
	b.m[id] = e
}

func (b *planBook) get(id string, now time.Time) (planEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[id]
	if !ok || !now.Before(e.until) {
		return planEntry{}, false
	}
	return e, true
}

// onClaim 是出口的认领回调：只处理迟到的回声，记一行日志（出口在放开认领的锁以后调）
func (p *Plugin) onClaim(c channel.EchoClaimed) {
	if !c.Late {
		return
	}
	e, ok := p.plans.get(c.PlanID, p.opt.Now())
	if !ok {
		return
	}
	p.trace(plugintrace.Event{Site: e.site, Route: RouteEcho, URL: e.url, Took: time.Duration(c.Note.LatencyMS) * time.Millisecond, Timed: true, Result: plugintrace.OK, Note: noteEchoLate})
	p.opt.Logf("计划 %s 第 %d 段的回声迟到了 %d 毫秒（已经按没送到补发过）", c.PlanID, c.Idx+1, c.Note.LatencyMS)
	p.lateEcho(c) // 大图：撤回后补发的那份，保证群里只剩一份（late.go）；小图不开看守，什么都不做
}

// ---- 会话的实验退回 ----

type quirkBook struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (b *quirkBook) mark(key string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]time.Time{}
	}
	for k, t := range b.m {
		if !now.Before(t) {
			delete(b.m, k)
		}
	}
	b.m[key] = now.Add(quirkTTL)
}

func (b *quirkBook) on(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.m[key]
	return ok && now.Before(t)
}

func quirkKey(conv types.Conversation, what string) string { return conv.String() + "|" + what }

// forwardVideoOn 说这个会话这次展开要不要把视频放进合并转发：开关开着，且这个会话 6 小时内没退回过
func (p *Plugin) forwardVideoOn(on bool, conv types.Conversation) bool {
	return on && !p.quirks.on(quirkKey(conv, "video"), p.opt.Now())
}

// ---- 一次展开里的等回声 ----

// await 等计划里第 idx 段的回声，wait 为 0 只看已经认领的
func (d *delivery) await(planID string, idx int, wait time.Duration) (channel.EchoNote, bool) {
	eo, ok := d.p.opt.Expand.Outlet.(echoOutbound)
	if !ok || planID == "" {
		return channel.EchoNote{}, false
	}
	return eo.AwaitPart(d.run.ctx, planID, idx, wait)
}

// echoWait 是等这一段回声的时长：配置的基础值（默认 2 秒）加按这一段媒体字节数算的放宽量（types.EchoAllowance）。
// 大图从协议端传到 QQ 要十几秒，回声比 2 秒晚得多，不放宽就把“还在上传”当成没送到（用户 2026-11-13，规格-链接发送改版 B12 第 4 条）；
// 小图几百 KB 加不到一秒，大图（≥ 2 MiB）放宽到 30 秒上下封顶（2026-11-14 B15）。EchoWait 是测试用的固定值，同样加放宽量。
func (d *delivery) echoWait(part types.Part) time.Duration {
	ms := d.send.EchoWaitMS
	if ms <= 0 {
		ms = 2000
	}
	base := time.Duration(ms) * time.Millisecond
	if w := d.p.opt.Expand.EchoWait; w > 0 {
		base = w
	}
	return base + types.EchoAllowance(types.PartMediaBytes(part))
}

// traceRow 记一行这个链接的插件日志
func (d *delivery) traceRow(e plugintrace.Event) {
	e.Site, e.URL = d.site, d.url
	d.p.trace(e)
}

// confirm 把一段的回执改写成到了 / 没到（只看带媒体的段）：
//   - 回包已确认：看出口有没有认领过它的回声（回声先于回包到时），带上回声的样子（可能是吞图）；
//   - 没确认送达：等回声，等到了算到了；到点没等到，开着 historyCheck 就翻一次历史，否则按“这一张被拒”（noEcho）；
//     大图（≥ types.BigMediaBytes）也一样：等得更久（EchoAllowance），等满了照样按没送到换形式，不再停在结果不明
//     （2026-11-14 B15：4 次大图没回声之后一次晚到的都没有，是被 QQ 吞了），但先开一个晚到回声的看守（late.go），
//     换形式发出去以后原来那份的回声才到，就撤回后发的那份；
//   - 别的结果不明（没配号、取消了等待……）原样返回。
func (d *delivery) confirm(planID string, o PartOutcome, part types.Part, label string) PartOutcome {
	if !hasMedia(part) {
		return o
	}
	switch {
	case o.State == OutcomeConfirmed:
		if n, ok := d.await(planID, o.Index, 0); ok {
			o.echo = &n
			d.traceRow(plugintrace.Event{Route: RouteEcho, Took: time.Duration(n.LatencyMS) * time.Millisecond, Timed: true, Result: plugintrace.OK, Note: noteEchoArrived})
		}
		return o
	case o.State == OutcomeUnknown && o.NoReceipt:
		wait := d.echoWait(part)
		// 大图先开看守再等：等的时候、放弃以后到换形式发出去之前，回声随时可能晚到，看守得已经在（换形式的重发段 d.guard 非空，
		// 说明正在替别的原段重发，自己没回声时不再另开：撤回只认最先那份原段）
		big := types.PartMediaBytes(part) >= types.BigMediaBytes
		var g *lateGuard
		if big && d.guard == nil {
			g = d.p.openGuard(d, planID, o.Index, part)
		}
		if n, ok := d.await(planID, o.Index, wait); ok {
			if g != nil {
				d.p.guards.drop(planID, o.Index) // 等到了，不用看守
			}
			d.logf("%s：没拿到确认，回声 %d 毫秒后到了，按成功算", label, n.LatencyMS)
			d.traceRow(plugintrace.Event{Route: RouteEcho, Took: time.Duration(n.LatencyMS) * time.Millisecond, Timed: true, Result: plugintrace.OK, Note: noteEchoArrived})
			o.State, o.PlatformID, o.echo = OutcomeConfirmed, n.PlatformID, &n
			return o
		}
		if d.run.ctx.Err() != nil || d.p.ctx.Err() != nil {
			// 等回声的时候展开被取消了（源消息撤回、展开关了、插件在关闭）：AwaitPart 是被打断提前返回的，不是等满了没回声。
			// 不记“这张图被拒”（会让 6 小时内同一张图都不再原样发）、不换形式（也发不出去）；回执保持“结果不明”（复核问题 6）
			d.logf("%s：等回声时展开取消了，不当没送到，不换形式、不记结论", label)
			if g != nil {
				d.p.guards.drop(planID, o.Index)
			}
			return o
		}
		d.traceRow(plugintrace.Event{Route: RouteEcho, Took: wait, Timed: true, Result: plugintrace.Fail, Kind: kindEchoTimeout})
		if d.send.HistoryCheck {
			pr, ok := probeOf(part)
			o = d.historyCheck(o, pr, ok, label)
		} else {
			d.logf("%s：没拿到确认，%d 毫秒内也没有回声，按没送到处理，换形式", label, wait.Milliseconds())
			d.verifyStep("没回声")
			o.State, o.Category, o.noEcho = OutcomeFailed, CategoryRejected, true
		}
		switch {
		case o.noEcho:
			o.guard = g
		case g != nil:
			d.p.guards.drop(planID, o.Index) // 核实到了或查不了：不换形式，不用看守
		}
		return o
	}
	return o
}

// ---- 图被吞：撤回只剩文字的那条 ----

// recallGate 查撤回的门槛（插件这一道；宿主装配处再查一道）。返回不撤的原因，空串是可以撤
func (d *delivery) recallGate(o PartOutcome) string {
	ex := d.p.opt.Expand
	switch {
	case o.echo == nil:
		return "不是这一次展开刚认领的回声"
	case ex.Recall == nil:
		return "这个渠道不能撤回"
	case ex.Account == "" || o.echo.Account != ex.Account:
		return "回声不是发送用的号发的"
	case !ValidPlatformID(o.echo.PlatformID) || o.echo.PlatformID != o.PlatformID:
		return "平台消息号不明"
	case o.SentAtMS <= 0 || d.p.opt.Now().Sub(time.UnixMilli(o.SentAtMS)) > RecallWithin:
		return "超过 2 分钟"
	case d.run.ctx.Err() != nil:
		return "展开已取消"
	}
	return ""
}

// swallowed 处理回声只剩文字（图被吞）的一段：能撤回就撤回、把图转 180° 后图文一起重发；
// 撤不了（门槛不过、撤回失败）就只补发转好的图，不再发文字，免得群里出现两份文字。展开被取消了就什么都不做
func (d *delivery) swallowed(part types.Part, planID string, o PartOutcome, idx int) {
	label := fmt.Sprintf("第 %d 段", idx+1)
	if d.run.ctx.Err() != nil {
		d.logf("%s：图被吞，但展开已取消，不撤回、不补发", label)
		return
	}
	if !canTurn(part) {
		d.logf("%s：图被吞，里面不全是静态图（动图、视频不转），不撤回、不补发", label)
		d.failWhy("%s的图被吞了，没有补发", label)
		return
	}
	why := d.recallGate(o)
	if why == "" {
		err := d.p.opt.Expand.Recall(d.run.ctx, d.conv, planID, o.Index, o.PlatformID)
		if err == nil {
			d.logf("%s：图被吞，撤回了只剩文字的那条，转 180° 后图文重发", label)
			d.traceRow(plugintrace.Event{Route: RouteRecall, Result: plugintrace.OK, Note: noteRecall})
			d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeOK, Tag: "撤回：成功"})
			d.ladder(part, CategoryRejected, idx, 1, true)
			return
		}
		why = "撤回失败（" + reason(err) + "）"
		d.traceRow(plugintrace.Event{Route: RouteRecall, Result: plugintrace.Fail, Kind: recallKind(err), Error: reason(err), Note: noteRecall})
	} else {
		d.traceRow(plugintrace.Event{Route: RouteRecall, Result: plugintrace.Fail, Kind: kindRecallRefuse, Error: why, Note: noteRecall})
	}
	if d.run.ctx.Err() != nil {
		return
	}
	d.logf("%s：图被吞，%s，只补发转好的图（不带文字）", label, why)
	d.p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeFailed, Tag: "撤回：没撤"})
	only := imagesOnly(part)
	turned, err := d.build(only, formRotated)
	if err != nil {
		d.failWhy("%s的图被吞了，转图没准备成", label)
		return
	}
	o2, err := d.attempt(turned, label)
	if err != nil {
		return
	}
	if o2.State != OutcomeConfirmed && o2.State != OutcomeUnknown {
		d.failWhy("%s的图被吞了，补发的图也没发出去", label)
		d.hardFail = true
	}
}

func recallKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, types.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, errRecallRefused):
		return kindRecallRefuse
	}
	return "error"
}

// ValidPlatformID 说一个要撤回的平台消息号是不是规范的非 0 十进制整数（QQ 的消息号可以是负数）：
// 空、0、带空白或控制字符、前导 0 的都不算（插件和宿主的撤回门槛共用）
func ValidPlatformID(s string) bool {
	if !store.HasPlatformID(s) {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n != 0 && strconv.FormatInt(n, 10) == s
}

// errRecallRefused 是宿主兜底拒绝撤回时返回的错误
var errRecallRefused = errors.New("撤回门槛不成立")

// ErrRecallRefused 给宿主用：门槛不成立时包上它
func ErrRecallRefused(why string) error { return fmt.Errorf("%w：%s", errRecallRefused, why) }

// imagesOnly 是一段里只留图（去掉文字和引用）
func imagesOnly(p types.Part) types.Part {
	out := types.Part{Kind: types.PartMessage}
	for _, s := range p.Body {
		if s.Kind != types.BodyText {
			out.Body = append(out.Body, s)
		}
	}
	return out
}

// planBookTTL 是计划号表记多久：和出口认领回声的窗口一样 5 分钟
const planBookTTL = 5 * time.Minute

// traceEvent 是一行发送那段的插件日志（结果 fail，kind、note 由调用方给）
func traceEvent(route, kind, note string) plugintrace.Event {
	return plugintrace.Event{Route: route, Result: plugintrace.Fail, Kind: kind, Note: note}
}
