package linkplugin

// 大图没回声以后按没送到换形式，原来那份的回声却晚到了：撤回后补发的那份，群里只剩一份
// （规格-链接发送改版 B15，用户 2026-11-14）。
//
// 背景：2026-11-13 到 14 日 4 次“大图没回声”之后日志里一条晚到的回声都没有，是 QQ 把大图吞了；B12 第 4 条因此当成
// “结果不明、不补发”，用户在群里就什么也没看到。现在大图（≥ types.BigMediaBytes）等回声等满（types.EchoAllowance，30 秒上下）
// 还没有，就和小图一样当没送到、走换形式的阶梯。但“吞了”只是多数，慢上传的回声仍可能晚到，那时群里有两份：
// 等不到回声的时候开一个看守（lateGuard），记着原段和它换形式后发出去、已确认送到的每一份；原段的回声认领到了（回调或自己查），
// 就撤回补发的那些份。撤回补发的、留原来的：原来那份是原样原图，补发的是转过 180° 的图或文字版。
//
// 三个时间窗都汇到 lateGuard.arrive 这一个结算点（一把锁，到达只算一次，每一份只撤一次）：
//   - 换形式发出去之前到：每一级、文字版发之前都看一眼（originalArrived），到了就停，不再补发；
//   - 补发在途或刚确认时到：补发确认时（noteReplacement）看到已经到了，就当场撤这一份；
//   - 展开结束以后才到：靠出口的认领回调（onClaim → lateEcho），在插件自己的协程里撤，用插件的 ctx（展开的 ctx 此时已取消）。
//
// 只有强匹配的认领（EchoNote.Strong：消息段、图的 md5 都对上）才撤：转发段不比内容、读不出图标识的只比形状，认错了会把群里的内容撤光，
// 这样的晚到回声只记一行日志，不撤也不翻“被拒”的结论（复核问题 1）。
// 只开给大图：小图的行为不变（规格 A 节“回声迟到不撤回已补发的”，用户接受的取舍）。
// 撤回只撤“已经拿到平台消息号”的补发份：补发份结果不明、没有号的，没法撤，只记日志；
// 补发份是几个原段合并的文字版时，要它装着的每个原段的回声都到了才撤，免得撤掉还有用的内容。
// 迟到回声只带文字（图被吞，MediaMissing）时原段不算到了：补发的那份才有图，不动它（和旧行为一致，群里会有两份文字，已知的坑）。

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/taskrun"
)

// PartRef 是计划里的一段，撤回按它向宿主核对（宿主按库里的计划独立再判一次，不信插件的话）
type PartRef struct {
	PlanID     string
	Idx        int
	PlatformID string // 这一段的平台消息号（原段是它晚到的回声的号，补发份是回包或回声认下的号）
}

const (
	noteRecallDup  = "撤回补发的那份（原来的回声晚到了）"
	noteLateOrigin = "大图原来那份晚到，不再补发"
	// 晚到的回声只凭形状认领：不撤补发的那份，只记这一行（量有多少，再定要不要放宽）
	noteRecallWeak = "回声只凭形状认领，不撤补发的那份"
	kindRecallWeak = "weak_match"
	// lateRecallTimeout 是一次撤回调用的时限（用插件的 ctx 派生，展开的 ctx 此时多半已取消）
	lateRecallTimeout = 15 * time.Second
	guardBookMax      = 128
)

// guardHint 是阶梯里发某一级之前留的话：这一级若没回声要开看守，按图的结论的键和形式用这里的（转过的图自己算不出阶梯用的键）
type guardHint struct {
	key  string
	form form
}

// replacement 是换形式后发出去、已确认送到的一份
type replacement struct {
	ref      PartRef
	sentAtMS int64
	needs    []*lateGuard // 这一份里装着哪些原段（各自的看守）；有 nil 说明里面还有别的内容，不能撤
	taken    atomic.Bool  // 已经有人接手去撤了（只撤一次）
}

// ready 说这一份该撤了：它装着的每个原段的回声都到了，而且认领是强匹配（图 md5 对上，EchoNote.Strong）。
// 弱认领（转发段、只凭形状的）到了也只算原段没丢，不撤：认错了会把群里的内容撤光（复核问题 1）
func (r *replacement) ready() bool {
	for _, g := range r.needs {
		if g == nil || !g.arrived.Load() || !g.strong.Load() {
			return false
		}
	}
	return len(r.needs) > 0
}

// lateGuard 看守一个等不到回声、已按没送到处理的大图原段
type lateGuard struct {
	conv        types.Conversation
	site, url   string
	orig        PartRef // 原段：PlanID、Idx 是它自己的，PlatformID 在回声晚到以后才有
	verdictKey  string  // 按图的结论的键；原段晚到说明它没被拒，要把“被拒”翻回去
	verdictForm form
	until       time.Time

	arrived atomic.Bool // 原段的回声到了（图是全的）
	strong  atomic.Bool // 到的回声是强匹配（先于 arrived 写）；弱的不翻结论、不撤补发份
	mu      sync.Mutex
	echoID  string
	repls   []*replacement
	unsure  bool // 有补发份结果不明（没有号，撤不了）
}

// guardBook 记最近 5 分钟里开的看守，键是原段（计划号、段序）
type guardBook struct {
	mu sync.Mutex
	m  map[string]*lateGuard
}

func guardKey(planID string, idx int) string { return fmt.Sprintf("%s#%d", planID, idx) }

func (b *guardBook) put(g *lateGuard, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*lateGuard{}
	}
	for k, v := range b.m {
		if !now.Before(v.until) {
			delete(b.m, k)
		}
	}
	if len(b.m) >= guardBookMax {
		var oldest string
		for k, v := range b.m {
			if oldest == "" || v.until.Before(b.m[oldest].until) {
				oldest = k
			}
		}
		delete(b.m, oldest)
	}
	b.m[guardKey(g.orig.PlanID, g.orig.Idx)] = g
}

func (b *guardBook) drop(planID string, idx int) {
	b.mu.Lock()
	delete(b.m, guardKey(planID, idx))
	b.mu.Unlock()
}

func (b *guardBook) get(planID string, idx int, now time.Time) *lateGuard {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.m[guardKey(planID, idx)]
	if g == nil || !now.Before(g.until) {
		return nil
	}
	return g
}

// openGuard 给一个等不到回声、按没送到换形式的大图原段开看守
func (p *Plugin) openGuard(d *delivery, planID string, idx int, part types.Part) *lateGuard {
	g := &lateGuard{conv: d.conv, site: d.site, url: d.url, orig: PartRef{PlanID: planID, Idx: idx}, verdictForm: formOf(part), until: p.opt.Now().Add(planBookTTL)}
	if k := partKey(part); k != "" {
		g.verdictKey = d.conv.String() + "|" + k
	}
	if d.hint != nil {
		g.verdictKey, g.verdictForm = d.hint.key, d.hint.form
	}
	p.guards.put(g, p.opt.Now())
	return g
}

// addRepl 把一份补发的记到看守名下
func (g *lateGuard) addRepl(r *replacement) {
	g.mu.Lock()
	g.repls = append(g.repls, r)
	g.mu.Unlock()
}

func (g *lateGuard) noteUnsure() {
	g.mu.Lock()
	g.unsure = true
	g.mu.Unlock()
}

// arrive 是原段的回声到了的结算点：只算一次；翻回按图的结论；返回这一刻该撤的补发份（已标记，调用方去撤）。
// 回声只剩文字（吞图）不算原段到了：补发的那份才有图
func (p *Plugin) arrive(g *lateGuard, n channel.EchoNote) []*replacement {
	if n.MediaMissing {
		p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，但图被吞了，补发的那份留着", g.orig.PlanID, g.orig.Idx+1)
		return nil
	}
	g.mu.Lock()
	if g.arrived.Load() {
		g.mu.Unlock()
		return nil
	}
	g.echoID = n.PlatformID
	g.orig.PlatformID = n.PlatformID
	g.strong.Store(n.Strong)
	g.arrived.Store(true)
	repls := slices.Clone(g.repls)
	unsure := g.unsure
	g.mu.Unlock()
	p.markGood(g)
	if !n.Strong {
		// 只凭形状认领的（转发段不比内容，或回声里的图读不出标识）：当原段没丢，不再补发，但不撤已经补发的、也不翻“被拒”的结论
		p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，但认领只凭形状（不是图的 md5 对上的），补发的 %d 份不撤", g.orig.PlanID, g.orig.Idx+1, len(repls))
		p.trace(plugintrace.Event{Site: g.site, URL: g.url, Route: RouteRecall, Result: plugintrace.Fail, Kind: kindRecallWeak, Note: noteRecallWeak})
		return nil
	}
	var out []*replacement
	for _, r := range repls {
		if r.ready() && r.taken.CompareAndSwap(false, true) {
			out = append(out, r)
		}
	}
	switch {
	case len(out) > 0:
	case unsure:
		p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，补发的那份结果不明、没有消息号，撤不了", g.orig.PlanID, g.orig.Idx+1)
	case len(repls) == 0:
		p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，还没补发，不再补发", g.orig.PlanID, g.orig.Idx+1)
	}
	return out
}

// markGood 把按图的结论翻回去：原段到了，不是被拒，别让 6 小时内同一张图都不再原样发。
// 阶梯在原段没回声时先记了“被拒”，所以每次看到“到了”而停下时都要再记一次（顺序上它在“被拒”之后）
func (p *Plugin) markGood(g *lateGuard) {
	if !g.strong.Load() {
		return // 弱认领不是“到了”的证据，不翻结论
	}
	p.verdicts.mark(g.verdictKey, g.verdictForm, true, p.opt.Now())
}

// lateEcho 是出口的认领回调（迟到的回声）进来的一支：开过看守的大图原段，回声到了就在插件自己的协程里撤补发的那些份
func (p *Plugin) lateEcho(c channel.EchoClaimed) {
	g := p.guards.get(c.PlanID, c.Idx, p.opt.Now())
	if g == nil {
		return
	}
	todo := p.arrive(g, c.Note)
	if len(todo) == 0 {
		return
	}
	p.goLate(func() {
		for _, r := range todo {
			p.dropDup(g, r)
		}
	})
}

// goLate 起一个撤回协程：算进 lateWG，Close 等它们退出；已经关闭就不起
func (p *Plugin) goLate(fn func()) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		// 不静默：原来那份的回声晚到了，但插件已经关闭，补发的那份没人去撤（复核问题 5）
		p.opt.Logf("原来那份的回声晚到了，但插件在关闭，补发的那份没撤：群里可能有两份")
		return
	}
	p.lateWG.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.lateWG.Done()
		fn()
	}()
}

// dropDup 撤回补发的一份：插件这一道门（宿主按库里的计划再判一次，ExpandOptions.RecallDup）。失败只记日志
func (p *Plugin) dropDup(g *lateGuard, r *replacement) {
	ex := p.opt.Expand
	why := ""
	g.mu.Lock()
	echoID := g.echoID
	g.mu.Unlock()
	switch {
	case ex == nil || ex.RecallDup == nil:
		why = "这个渠道不能撤回"
	case ex.Account == "":
		why = "没配发送用的号"
	case !ValidPlatformID(r.ref.PlatformID):
		why = "平台消息号不明"
	case r.ref.PlatformID == echoID:
		why = "补发的号和原来的回声是同一个"
	case r.sentAtMS <= 0 || p.opt.Now().Sub(time.UnixMilli(r.sentAtMS)) > RecallWithin:
		why = "超过 2 分钟"
	case p.ctx.Err() != nil:
		why = "插件在关闭"
	}
	ev := plugintrace.Event{Site: g.site, URL: g.url, Route: RouteRecall, Note: noteRecallDup}
	if why == "" {
		ctx, cancel := context.WithTimeout(p.ctx, lateRecallTimeout)
		err := ex.RecallDup(ctx, g.conv, PartRef{PlanID: g.orig.PlanID, Idx: g.orig.Idx, PlatformID: echoID}, r.ref)
		cancel()
		if err == nil {
			p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，撤回了补发的那份", g.orig.PlanID, g.orig.Idx+1)
			ev.Result = plugintrace.OK
			p.trace(ev)
			p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeOK, Tag: "撤回补发：成功"})
			return
		}
		why = "撤回失败（" + reason(err) + "）"
		ev.Result, ev.Kind, ev.Error = plugintrace.Fail, recallKind(err), reason(err)
	} else {
		ev.Result, ev.Kind, ev.Error = plugintrace.Fail, kindRecallRefuse, why
	}
	p.trace(ev)
	p.opt.Logf("计划 %s 第 %d 段原来的回声晚到了，补发的那份没撤掉（%s）：群里可能有两份", g.orig.PlanID, g.orig.Idx+1, why)
	p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassFormDegrade, nil, taskrun.Opts{Outcome: taskrun.OutcomeFailed, Tag: "撤回补发：没撤"})
}

// ---- 展开自己的协程里用的几个入口 ----

// originalArrived 说这个看守的原段的回声到了没有：先看已经结算的，再向出口问一次（只看已经认领的，不等）
func (d *delivery) originalArrived(g *lateGuard) bool {
	if !g.arrived.Load() {
		if n, ok := d.await(g.orig.PlanID, g.orig.Idx, 0); ok {
			d.settleArrival(g, n)
		}
	}
	if g.arrived.Load() {
		d.p.markGood(g)
		return true
	}
	return false
}

// settleArrival 在展开自己的协程里结算原段到了：该撤的份当场撤
func (d *delivery) settleArrival(g *lateGuard, n channel.EchoNote) {
	for _, r := range d.p.arrive(g, n) {
		d.p.dropDup(g, r)
	}
}

// noteReplacement 把刚确认送到的补发份记到当前看守名下；原段的回声此时已经到了，就当场撤这一份
func (d *delivery) noteReplacement(planID string, o PartOutcome) {
	if g := d.guard; g != nil {
		d.attachReplacement(planID, o, []*lateGuard{g})
	}
}

// attachReplacement 记一份补发：needs 是它装着的各原段的看守（文字版可能合并了几个原段，有 nil 表示里面还有别的内容）。
// 分成登记和结算两步（测试按固定顺序走这两步和 arrive，逐一验各种交错下只撤一次）
func (d *delivery) attachReplacement(planID string, o PartOutcome, needs []*lateGuard) {
	d.settleReplacement(d.registerReplacement(planID, o, needs), needs)
}

// registerReplacement 把补发份记进各原段看守的名单（此后原段到达的那一边看得到它）
func (d *delivery) registerReplacement(planID string, o PartOutcome, needs []*lateGuard) *replacement {
	r := &replacement{ref: PartRef{PlanID: planID, Idx: o.Index, PlatformID: o.PlatformID}, sentAtMS: o.SentAtMS, needs: needs}
	for _, g := range needs {
		if g != nil {
			g.addRepl(r)
		}
	}
	return r
}

// settleReplacement 在登记以后看一眼：原段的回声若已经到了，当场撤这一份。和到达那一边抢同一份时靠 taken 只让一边撤
func (d *delivery) settleReplacement(r *replacement, needs []*lateGuard) {
	if r.ready() && r.taken.CompareAndSwap(false, true) {
		d.p.dropDup(needs[0], r)
	}
}
