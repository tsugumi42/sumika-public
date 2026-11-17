package linkplugin

// 一条消息里的多个链接：取数并发、发送按原顺序（用户 2026-11-13：不要等全部取完才发第一个，也不要第 2 个排队时第 1 个干等）。
//   - 取数：同时最多 j.limit 个在途。遇到规范链接重复的（取到才知道）才去取下一个候选，不为“可能有重复”提前多取。
//     同平台的限速、在途合并都在取数层（linkreader），这里不另造；
//   - 发送：消费者按链接在消息里的顺序等每个取数结束，去重、记关联、准备、发送都在这一个协程里做，所以前面的发完才轮到后面的，
//     后面的早已取好就直接接着发，没取好就等它；
//   - 取消：源消息撤回、展开关了或在关闭时，所有在途取数一起取消并等它们退出（f.wg），再各记一行取消。

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

// fetched 是一条链接取到（或没取成）的结果，已经过去重和记关联，等着发
type fetched struct {
	link     linkreader.Link
	result   linkreader.Result
	previous int
	err      error
	trans    *translating // 正在翻译正文、被引推文、长文；nil 表示都不翻
}

// fetchTask 是一条链接的取数；done 关闭以后才能读下面几项
type fetchTask struct {
	link        linkreader.Link
	began       time.Time
	done        chan struct{}
	result      linkreader.Result
	err         error
	association *linkreader.Result // 记进库的那份（给她看的任务有自己的一份取数）
	trans       *translating
}

// linkFlow 是一个任务（一条消息）的取数和发送；只有 run 的协程碰这里的状态，取数协程只写自己的 fetchTask
type linkFlow struct {
	p     *Plugin
	j     job
	links []linkreader.Link // 设置里关了的网站和类型已经去掉

	// delivered、failed 给收尾用：至少一个链接的内容发出去了、有链接没成
	delivered, failed bool
	unknown           bool // 有链接结果不明、没有一段确认送到（不贴完成表情；别的链接有成功的仍贴完成）
	skippedOff, noted bool
	staged            bool

	meta      context.Context // 所有取数共用的父上下文：关闭、提前结束时一起取消
	stopFetch context.CancelFunc
	wg        sync.WaitGroup
	tasks     []*fetchTask // tasks[k] 是 links[k]
	next      int          // 下一个要启动的链接
	dups      int          // 已发现重复的条数：它们不占展开上限
	halted    bool         // 不再启动新的取数（许可没了）
	haltErr   error
	haltAt    time.Time
	canonical map[string]bool
}

// fill 在名额内启动取数：已启动、不是重复的条数（next-dups）不超过上限。每个取数启动前先过许可，不许发了就不再启动
func (f *linkFlow) fill() {
	p, j := f.p, f.j
	for !f.halted && f.next < len(f.links) && f.next-f.dups < j.limit {
		l := f.links[f.next]
		began := p.opt.Now()
		var permitErr error
		if j.run != nil {
			permitErr = p.permit(j.run, nil)
		}
		viaRun := j.run != nil && permitErr == nil
		if !viaRun && !j.context {
			// 只为展开取数、又不许发了（群档位、闸门、许可撤了）：剩下的链接不取，run 末尾各记一行没发的原因
			f.halted, f.haltErr, f.haltAt = true, permitErr, began
			return
		}
		f.tasks = append(f.tasks, f.startFetch(l, began, viaRun))
		f.next++
	}
}

func (f *linkFlow) startFetch(l linkreader.Link, began time.Time, viaRun bool) *fetchTask {
	t := &fetchTask{link: l, began: began, done: make(chan struct{})}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer close(t.done)
		f.fetch(t, viaRun)
	}()
	return t
}

// fetch 取一条链接。viaRun 为真时随展开的上下文取消；给她看的任务（j.context）展开关闭不取消它，只随插件关闭和自己的时限
func (f *linkFlow) fetch(t *fetchTask, viaRun bool) {
	p, j := f.p, f.j
	ctx, cancel := context.WithTimeout(f.meta, p.opt.JobTimeout)
	defer cancel()
	type ctxGet struct {
		r   linkreader.Result
		err error
	}
	var contextResult chan ctxGet
	if j.context && j.run != nil {
		ch := make(chan ctxGet, 1)
		contextResult = ch
		go func() { r, e := p.opt.Service.Get(ctx, t.link.Raw); ch <- ctxGet{r, e} }()
	}
	var r linkreader.Result
	var err error
	if viaRun {
		fetchCtx, stop := context.WithCancel(ctx)
		stopExpansion := context.AfterFunc(j.run.ctx, stop)
		r, err = p.opt.Service.Get(fetchCtx, t.link.Raw)
		stopExpansion()
		stop()
	} else {
		r, err = p.opt.Service.Get(ctx, t.link.Raw)
	}
	p.traceFetch(t.link, t.began, r, err)
	t.result, t.err = r, err
	if err == nil {
		t.association = &t.result
	}
	// 上下文消费者有自己的合法等待；展开关闭不会取消它。
	if contextResult != nil {
		c := <-contextResult
		if c.err == nil {
			cr := c.r
			t.association = &cr
		} else if err != nil {
			t.association = nil
		}
	}
	// 取到就开始翻译，和别的链接的取数、前面链接的发送重叠
	if j.run != nil {
		t.trans = p.startTranslate(j, &t.result, t.err)
	}
}

// abandon 给这条消息里不再往下发的链接各记一行没发的原因：当前这个（cur 为真时，带上它取到的结果）和后面所有的（已启动的、没启动的）。
// 前面的链接已经发过、各有自己的行（只为展开的任务才记）
func (f *linkFlow) abandon(i int, cur bool, r *linkreader.Result, note string) {
	if f.j.run == nil {
		return
	}
	if cur {
		f.p.traceSkip(f.links[i], r, note)
	}
	for _, rest := range f.links[i+1:] {
		f.p.traceSkip(rest, nil, note)
	}
}

// traceTail 给没启动的链接各记一行：许可没了（perr 不空）时上限内记许可的原因，超出上限的记超限；perr 为空全是超限
func (f *linkFlow) traceTail(perr error, began time.Time) {
	if f.j.run == nil {
		return
	}
	room := 0
	if perr != nil {
		room = f.j.limit - (f.next - f.dups)
	}
	for k, rest := range f.links[f.next:] {
		if k < room {
			f.p.traceSendErr(rest, nil, began, "permit", perr)
		} else {
			f.p.traceSkip(rest, nil, noteOverLimit)
		}
	}
}

func (f *linkFlow) run() {
	p, j := f.p, f.j
	f.meta, f.stopFetch = context.WithCancel(p.ctx)
	// 不管从哪里返回：取消所有在途取数并等它们退出，不留协程
	defer func() { f.stopFetch(); f.wg.Wait() }()
	f.canonical = map[string]bool{}
	f.fill()
	for i := 0; i < len(f.tasks); i++ {
		t := f.tasks[i]
		<-t.done
		l, r, err := t.link, &t.result, t.err
		if p.ctx.Err() != nil || (!j.context && j.run != nil && j.run.ctx.Err() != nil) {
			// 在关闭，或源消息撤回、展开关了：不再往下发，当前的、后面的各记一行“取消了”
			var cur *linkreader.Result
			if err == nil {
				cur = r
			}
			f.abandon(i, true, cur, noteCanceled)
			return
		}
		key := l.Clean // 取不到时的关联键也用清过的链接，不把带追踪参数的原文记进库
		if err == nil {
			key = r.CleanURL
		}
		association := t.association
		if association != nil {
			key = association.CleanURL
		}
		if f.canonical[key] {
			if j.run != nil {
				p.traceSkip(l, r, noteSameLink)
			}
			f.dups++
			f.fill() // 重复的不占上限：空出名额，再取下一个候选
			continue
		}
		f.canonical[key] = true
		if association != nil && association.Stage == "metadata" {
			association = nil
		}
		previous, e := p.opt.Service.Associate(p.ctx, j.conv, j.seq, key, association)
		if e != nil {
			p.warnf("记关联失败（%s）", reason(e))
			if j.run != nil {
				f.failed = true
				p.traceSendErr(l, r, t.began, "store", e)
			}
			f.abandon(i, false, nil, noteStoreAbort)
			return
		}
		if j.run == nil {
			continue
		}
		if !f.deliverOne(fetched{link: l, result: *r, previous: previous, err: err, trans: t.trans}, i) {
			return
		}
	}
	if j.run == nil {
		return
	}
	f.traceTail(f.haltErr, f.haltAt)
	if f.skippedOff && !f.delivered && !f.failed {
		p.explainOff(j.run)
	}
}

// deliverOne 准备并发送一个链接的结果（轮到它才发）；返回 false 是许可没了，后面的都不再发
func (f *linkFlow) deliverOne(result fetched, i int) bool {
	p, j := f.p, f.j
	if !f.staged {
		f.staged = true
		p.stage(j.run, "media")
	}
	if e := p.permit(j.run, nil); e != nil {
		now := p.opt.Now()
		p.traceSendErr(result.link, &result.result, now, "permit", e)
		for _, rest := range f.links[i+1 : f.next] {
			p.traceSendErr(rest, nil, now, "permit", e)
		}
		f.traceTail(e, now)
		return false
	}
	began := p.opt.Now()
	o := j.snapshot.Settings.Expand
	p.noteLegacyForwardOver(o)
	var draft linkexpand.Draft
	if kindOff(o, result.link) {
		// 带“给她看”的任务里关掉的链接也取了数（给她看不受展开开关管）；展开这边按链接本身的网站和类型跳过，失败也不回失败文字
		p.debugf("%s/%s 在设置里关了，不展开", result.link.Platform, result.link.Kind)
		p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassBlocked, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: "网站或页面类型已关"})
		p.traceSkip(result.link, nil, noteKindOff)
		return true
	}
	if result.err != nil {
		// 取数失败：群里不发文字，靠 conclude 把开始表情换成失败表情；私聊贴不了表情，回一句 parseFailedText。原因只记日志
		p.warnf("取数失败（%s/%s %s：%s）", result.link.Platform, result.link.Kind, result.link.Clean, reason(result.err))
		f.failed = true
		p.traceSkip(result.link, nil, noteFetchFail)
		p.noteFailure(j.run)
		return true
	} else if !o.KindEnabled(result.result.Platform, result.result.Kind) {
		// 短链解析后才知道页面类型；这一类在设置里关了就不展开，不当失败，记一行
		p.debugf("%s/%s 在设置里关了，不展开", result.result.Platform, result.result.Kind)
		p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassBlocked, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: "网站或页面类型已关"})
		p.traceSkip(result.link, &result.result, noteKindOff)
		f.skippedOff = true
		return true
	} else {
		bo := BuildOptions(o)
		bo.ForwardVideo = p.forwardVideoOn(o.Send.ForwardVideo, j.run.source.Inbound.Conv) // 这个会话退回过就按旧排法（规格-链接发送改版 A6）
		bo.Translations = p.takeTranslations(j.run, result.trans)
		for _, why := range linkexpand.TranslationProblems(&result.result, bo) {
			// 译文有一段用不上（投票选项、长文或被引长文的段数对不上）：记一行带原因标签，那一段发原文
			p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassTranslate, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: why})
		}
		// 先按表里现有的行定这一次的发法，再按需判断：新记的 pending 不影响这一次
		if p.opt.Expand.Artists != nil {
			bo.Artist = ArtistMode(p.opt.Expand.Artists, &result.result, o.Artist)
			p.observeArtist(&result.result, o.Artist)
		}
		draft = linkexpand.Build(&result.result, j.run.source.Inbound.PlatformID, bo, result.previous)
	}
	if draft.NoContent {
		// 取到了但标题和正文都没有：和取数失败一样处理
		p.warnf("%s/%s 没取到内容", result.result.Platform, result.result.Kind)
		f.failed = true
		p.traceSkip(result.link, &result.result, noteNoContent)
		p.noteFailure(j.run)
		return true
	}
	if len(draft.Parts) == 0 {
		// 模板拼出来什么都没有：不发，记一行（以前静默跳过，查不到为什么没展开）；收尾照常
		p.opt.Logf("%s/%s 展开结果为空，不发", result.result.Platform, result.result.Kind)
		p.traceSkip(result.link, &result.result, noteEmpty)
		return true
	}
	// “还有 N 条没处理”挂在第一条真正发出去的结果上；前面的链接失败了也不丢。
	// 发第一条时后面的取数还没结束，所以按当时还没启动的条数算；之后发现有重复空出名额，就可能比实际多说一两条（只在超过上限时才有）
	if excess := len(f.links) - f.next; !f.noted && excess > 0 {
		f.noted = true
		draft.AppendNote(fmt.Sprintf("还有 %d 条没处理", excess)) // 第一段是合并转发时接在第一个节点里
	}
	prepared, err := p.opt.Expand.Prepare(j.run.ctx, j.run.source, j.snapshot, &result.result, &draft)
	if err != nil {
		p.errorf("准备结果失败（%s）", reason(err))
		p.traceSendErr(result.link, &result.result, began, "prepare", err)
		f.failed = true
		return true
	}
	rep, err := p.deliver(j.run, retryInput{snapshot: j.snapshot, result: &result.result}, prepared.Parts, prepared.Resources)
	if err != nil {
		p.errorf("结果未入队（%s）", reason(err))
		p.traceSendErr(result.link, &result.result, began, "submit", err)
		f.failed = true
		return true
	}
	p.traceDelivered(result.link, &result.result, began, rep)
	switch {
	case rep.delivered && rep.unsure && !rep.landed:
		// 结果不明、没有一段确认送到：不知道群里有没有东西，不贴完成也不贴失败（规格-链接解析模板第六节“宁可少贴，不贴错”，B15）；
		// 只撤掉开始表情。以前这里算 delivered，群里什么也没出现却贴了“完成”
		f.unknown = true
	case rep.delivered:
		f.delivered = true
	default:
		f.failed = true
	}
	return true
}
