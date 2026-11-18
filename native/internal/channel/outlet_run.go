package channel

// 一条计划怎么发出去：在会话队列的 goroutine 里一段一段做（规格-渠道中间件 10.2、10.4、10.5）。
//
// 一段的一生：待发 → 发送中 → 确认 / 明确失败 / 结果未知。每一步转移都是库里的条件更新（store.EndPart）：
// 这个 goroutine、回声认领（另一个 goroutine）、关闭几方同时碰同一段，先到的算数，后到的转不动，
// 转不动就去看现在是什么状态——被回声认领成确认了，就当确认接着往下发；别的（被清理了、封库了）就不再往下。

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
)

// flight 是一段正在发的东西：回声认领成功时往 wake 里放一个信号，叫醒还在等回包或等重试的 goroutine
type flight struct {
	wake     chan struct{}
	notified bool // resultMu 保护，回声与回包共用
}

func (o *Outlet) enter(id string) *flight {
	f := &flight{wake: make(chan struct{}, 1)}
	o.mu.Lock()
	o.flights[id] = f
	o.mu.Unlock()
	return f
}

func (o *Outlet) leave(id string) {
	o.mu.Lock()
	delete(o.flights, id)
	o.mu.Unlock()
}

// wakeFlight 叫醒正在发这一段的 goroutine（没有在发的就什么也不做）。信号是缓冲 1 的，叫两次不会堵
func (o *Outlet) wakeFlight(id string) {
	o.mu.Lock()
	f := o.flights[id]
	o.mu.Unlock()
	if f == nil {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// outcome 是一段发完的结果，决定计划往不往下走
type outcome int

const (
	outConfirmed outcome = iota // 确认（回包确认，或被回声认领）：接着发下一段
	outFailed                   // 明确失败且用完重试，或适配器不支持：后面的段不发了
	outUnknown                  // 结果未知：不重试，后面的段不发了
	outStopped                  // 被中止、关闭、封库：不再往下发，剩下的由中止的那一方（或关闭时最后的清理）转成已中止
)

// runPlan 在会话队列里发一条计划。出意外（适配器 panic 之类）也不能让队列停，也不能把段留在"发送中"不管
func (o *Outlet) runPlan(pr *planRun) {
	defer func() {
		if r := recover(); r != nil {
			o.logf("会话 %s 的计划 %s 发送时出了意外：%v，在途的段按结果未知办，没发的中止", pr.conv.String(), pr.id, r)
			_ = o.db(func() error {
				_, e := pr.db.SweepPlan(pr.id, "发送时出了意外，不知道发出去没有", "发送时出了意外，没发", o.nowMS())
				return e
			})
		}
	}()
	for i := range pr.parts {
		if o.isClosing() {
			return // 关闭：剩下的段由 Close 最后的清理转成已中止
		}
		if reason := o.notSpeaking(pr); reason != "" {
			o.abortRest(pr, reason)
			return
		}
		// 停顿从上一段的终态算起（上一段终态一出来就走到这里）；等的时候关闭了就不发了。
		// 停顿之后再问一次档位：停顿期间切档，后面的段不调 Send（10.6）
		if d := pr.pauses[i]; d > 0 {
			if !o.sleep(d) {
				return
			}
			if reason := o.notSpeaking(pr); reason != "" {
				o.abortRest(pr, reason)
				return
			}
		}
		switch o.sendPart(pr, i) {
		case outConfirmed:
		case outFailed, outUnknown:
			o.abortRest(pr, "前面的段没发出去，后面的不发")
			return
		default:
			return
		}
	}
}

// notSpeaking：这个会话现在不是说话档就返回中止的原因（切档），是就返回空
func (o *Outlet) notSpeaking(pr *planRun) string {
	if pr.ctx.Err() != nil {
		return "计划已取消，没发的段中止"
	}
	if m := o.modeOf(pr.character, pr.origin, pr.conv); m != ModeSpeak {
		return fmt.Sprintf("切档：会话现在是 %s 档，没发的段不发了", m)
	}
	return ""
}

// abortRest 把这条计划里还没发的段转成已中止
func (o *Outlet) abortRest(pr *planRun, reason string) {
	var n int
	err := o.db(func() (e error) { n, e = pr.db.AbortPlanParts(pr.id, reason, o.nowMS()); return e })
	switch {
	case errors.Is(err, errSealed):
	case err != nil:
		o.logf("会话 %s 的计划 %s 中止剩下的段失败：%v", pr.conv.String(), pr.id, err)
	case n > 0:
		o.logf("会话 %s 的计划 %s：%d 段没发，已中止（%s）", pr.conv.String(), pr.id, n, reason)
	}
}

// sleep 等 d；关闭了就不等了，返回 false
func (o *Outlet) sleep(d time.Duration) bool {
	t := o.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-o.closing:
		return false
	}
}

// partState 读一段现在的状态；读不到（封库了）就是空
func (o *Outlet) partState(pr *planRun, i int) string {
	var st string
	err := o.db(func() (e error) { st, e = pr.db.PartState(pr.id, i); return })
	if err != nil {
		return ""
	}
	return st
}

// lost：这一段转不动了（别人先改了它）。被回声认领成确认了，就当确认接着发；别的都停
func (o *Outlet) lost(pr *planRun, i int) outcome {
	if o.partState(pr, i) == store.PartConfirmed {
		o.partResult(pr, i, true, "")
		return outConfirmed
	}
	return outStopped
}

// sendPart 发一段，含重试。返回这一段的结果
func (o *Outlet) sendPart(pr *planRun, i int) outcome {
	p := pr.parts[i]
	fl := o.enter(p.ID)
	defer o.leave(p.ID)
	began := o.clock.Now()
	for {
		// 排队、停顿和重试期间通道门可能暂停；每次实际调用前再检查。
		if reason := o.notSpeaking(pr); reason != "" {
			o.abortRest(pr, reason)
			return outStopped
		}
		if err := o.check(pr.character, pr.origin, pr.conv, pr.parts); err != nil {
			o.abortRest(pr, "发送检查没过")
			return outStopped
		}
		var attempt int
		var ok bool
		err := o.db(func() (e error) { attempt, ok, e = pr.db.BeginSend(pr.id, i, o.nowMS()); return })
		if err != nil {
			if !errors.Is(err, errSealed) {
				o.errorf("会话 %s 的计划 %s 第 %d 段开始发送前写库失败：%v，这一段不发", pr.conv.String(), pr.id, i, err)
			}
			return outStopped
		}
		if !ok {
			return o.lost(pr, i)
		}

		res, why := o.call(pr, i, fl)
		switch why {
		case callGaveUp:
			return outStopped // 关闭时等不到回执：这一段由封库时的清理转成结果未知，回执以后来也不写
		case callWoken:
			return o.lost(pr, i) // 回声先到，已经认领成确认
		case callTimeout:
			res = verdict{kind: types.ReceiptUnknown, errText: types.ReceiptTimeoutErr}
		}

		switch res.kind {
		case types.ReceiptConfirmed:
			if o.end(pr, i, store.PartConfirmed, store.PartEnd{PlatformID: res.platformID, Via: types.ViaReply}) {
				o.cfg.Degrade.Finish(taskrun.ModuleSend, began, attempt, taskrun.OutcomeOK, "", nil)
				o.partResult(pr, i, true, "")
				return outConfirmed
			}
			return o.lost(pr, i)
		case types.ReceiptUnknown:
			// 不重试：协议端可能已经发了，重试就是发两遍。5 分钟内她号上对得上的回声会把它认领成确认
			if o.end(pr, i, store.PartUnknown, store.PartEnd{Err: res.errText}) {
				o.logf("会话 %s 的计划 %s 第 %d 段结果未知（%s），不重发", pr.conv.String(), pr.id, i, res.errText)
				o.cfg.Selfcheck.Add(selfcheck.OutletReceiptUnknown)
				o.degrade(began, attempt, taskrun.OutcomeFailed, taskrun.ClassReceiptUnknown, res.errText)
				return outUnknown
			}
			return o.lost(pr, i)
		}

		// 明确失败。适配器说不支持、协议端明确拒绝的重试没用（原样再发还是被拒，QQ 那边只会多记一次），直接段失败；
		// 只有本地原因（没连上、没写出去）才最多发 MaxAttempts 次。结果不明在上面已经走了，不会到这里
		if res.unsupported || res.rejected || attempt >= MaxAttempts {
			failClass := taskrun.ClassRetryExhausted
			outcome := taskrun.OutcomeFailed
			switch {
			case res.unsupported:
				failClass, outcome = taskrun.ClassUnsupported, taskrun.OutcomeSkipped
			case res.rejected:
				failClass = taskrun.ClassRejected
			}
			if o.end(pr, i, store.PartFailed, store.PartEnd{Err: res.errText}) {
				o.degrade(began, attempt, outcome, failClass, res.errText)
				o.logf("会话 %s 的计划 %s 第 %d 段没发出去（试了 %d 次）：%s", pr.conv.String(), pr.id, i, attempt, res.errText)
				o.partResult(pr, i, false, failClass)
				return outFailed
			}
			return o.lost(pr, i)
		}
		if !o.end(pr, i, store.PartRetrying, store.PartEnd{Err: res.errText}) {
			return o.lost(pr, i)
		}
		if !o.waitRetry(pr, i, fl) {
			return o.lost(pr, i) // 回声在重试等待期里到了（重试作废），或者关闭了
		}
	}
}

// partResult 告诉 OnPart 一段有了明确的结果
func (o *Outlet) partResult(pr *planRun, i int, confirmed bool, class string) {
	o.resultMu.Lock()
	defer o.resultMu.Unlock()
	o.partResultLocked(pr, i, confirmed, class)
}

func (o *Outlet) partResultLocked(pr *planRun, i int, confirmed bool, class string) {
	o.mu.Lock()
	if f := o.flights[partID(pr.id, i)]; f != nil {
		if f.notified {
			o.mu.Unlock()
			return
		}
		f.notified = true
	}
	o.mu.Unlock()
	if o.cfg.OnPart == nil {
		return
	}
	if _, err := safe(func() struct{} {
		o.cfg.OnPart(PartResult{Conv: pr.conv, Character: pr.character, Origin: pr.origin, PlanID: pr.id, Index: i, Confirmed: confirmed, Class: class})
		return struct{}{}
	}); err != nil {
		o.logf("会话 %s 的计划 %s 第 %d 段的 OnPart%v", pr.conv.String(), pr.id, i, err)
	}
}

// end 把这一段从发送中转成 to；转成了返回 true。封库以后什么也不写（返回 false）
func (o *Outlet) end(pr *planRun, i int, to string, e store.PartEnd) bool {
	var ok bool
	err := o.db(func() (er error) {
		ok, er = pr.db.EndPart(pr.id, i, []string{store.PartSending}, to, e, o.nowMS())
		if er == nil && ok && e.PlatformID != "" {
			er = o.linkReceipt(context.Background(), pr.id, partID(pr.id, i), e.PlatformID)
		}
		return
	})
	if err != nil && !errors.Is(err, errSealed) {
		o.logf("会话 %s 的计划 %s 第 %d 段转 %s 写库失败：%v", pr.conv.String(), pr.id, i, to, err)
	}
	return ok
}

// waitRetry 等重试间隔。到点了返回 true（接着重试）；回声认领了这一段、或者关闭了返回 false
func (o *Outlet) waitRetry(pr *planRun, i int, fl *flight) bool {
	t := o.clock.NewTimer(o.cfg.Pace.Retry)
	defer t.Stop()
	for {
		select {
		case <-t.C():
			return true
		case <-o.closing:
			return false
		case <-fl.wake:
			// 叫醒我的是回声认领；认领成了，这一段已经是确认，重试作废。信号是别的原因留下的（段还在等重试）就接着等
			if o.partState(pr, i) != store.PartRetrying {
				return false
			}
		}
	}
}

// callEnd 是一次调用怎么结束的
type callEnd int

const (
	callDone    callEnd = iota // 适配器返回了（回执在 verdict 里）
	callTimeout                // 等了 SendTimeout 还没回
	callWoken                  // 回声认领把这一段认成确认了，不用再等回包
	callGaveUp                 // 关闭时等回执的时间到了
)

// verdict 是对一次调用的回执的判断
type verdict struct {
	kind        string // types.ReceiptConfirmed / Failed / Unknown
	platformID  string
	errText     string
	unsupported bool // 适配器说不支持：failed 里不重试的那种
	rejected    bool // 协议端明确拒绝这份内容（types.IsRejection）：原样重发只会再被拒，不重试
}

type callResult struct {
	r   types.Receipt
	err error
}

// call 调适配器发这一段，等结果。适配器在另一个 goroutine 里调：它不返回（没守"ctx 取消就返回"）也不会把这里卡死，
// 超时、回声先到、关闭放弃都能让这里走下去。那个 goroutine 之后返回什么都没人读（通道缓冲 1，不会堵）
func (o *Outlet) call(pr *planRun, i int, fl *flight) (verdict, callEnd) {
	p := pr.parts[i]
	ctx, cancel := context.WithCancel(pr.ctx)
	defer cancel()
	ch := make(chan callResult, 1)
	pr.calls.Add(1)
	go func() {
		defer pr.calls.Done()
		defer func() {
			if r := recover(); r != nil {
				ch <- callResult{err: fmt.Errorf("适配器 panic：%v", r)}
			}
		}()
		var res callResult
		if p.Kind == types.PartPoke {
			res.r, res.err = pr.rt.ad.Poke(ctx, pr.rt.account, pr.conv, p.Ref)
		} else {
			res.r, res.err = pr.rt.ad.Send(ctx, pr.rt.account, p)
		}
		ch <- res
	}()
	// 大图上传慢：兜底时限按段里的媒体字节数放宽（乘 3/2 是 base64 和 JSON 的膨胀，和适配器按帧长算的量对得上），小图不受影响
	t := o.clock.NewTimer(o.cfg.SendTimeout + types.UploadAllowance(types.PartMediaBytes(p)*3/2))
	defer t.Stop()
	for {
		select {
		case res := <-ch:
			return classify(p, res.r, res.err), callDone
		case <-t.C():
			return verdict{}, callTimeout
		case <-o.giveUp:
			return verdict{}, callGaveUp
		case <-fl.wake:
			if o.partState(pr, i) != store.PartSending {
				return verdict{}, callWoken
			}
		}
	}
}

// classify 把适配器给的回执判成三种之一（types.Adapter 的规矩）。适配器不守规矩——零值回执、状态不认识、说的不是这一段、
// 给了 ErrUnsupported 以外的 error——一律按结果未知办：不知道发出去没有，不能当成功（盘点 4.4 第 1 条），
// 也不能当明确失败去重发（可能发两遍）
func classify(p types.Part, r types.Receipt, err error) verdict {
	switch {
	case errors.Is(err, types.ErrUnsupported):
		return verdict{kind: types.ReceiptFailed, unsupported: true, errText: clipRunes("适配器不支持这个动作", 200)}
	case err != nil:
		return verdict{kind: types.ReceiptUnknown, errText: clipRunes("适配器没守规矩，返回了 ErrUnsupported 以外的错误："+err.Error(), 200)}
	}
	if verr := r.Validate(); verr != nil {
		return verdict{kind: types.ReceiptUnknown, errText: clipRunes("适配器给了不合规的回执："+verr.Error(), 200)}
	}
	// 戳一戳没有段，回执的 PartID 空着；别的要等于这一段的段号（契约测试查过，这里是最后一道）
	if p.Kind != types.PartPoke && r.PartID != p.ID {
		return verdict{kind: types.ReceiptUnknown, errText: clipRunes(fmt.Sprintf("回执说的不是这一段（%q）", r.PartID), 200)}
	}
	switch r.Status {
	case types.ReceiptConfirmed:
		if p.Kind == types.PartReact {
			return verdict{kind: types.ReceiptConfirmed} // 回应不产生新消息，绝不写 channel_msgs
		}
		return verdict{kind: types.ReceiptConfirmed, platformID: r.PlatformID}
	case types.ReceiptFailed:
		return verdict{kind: types.ReceiptFailed, errText: clipRunes(r.Err, 200), rejected: types.IsRejection(r.Err)}
	default:
		return verdict{kind: types.ReceiptUnknown, errText: clipRunes(r.Err, 200)}
	}
}

// clipRunes 截到最多 n 个字（rune），不切开一个字
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// degrade 把一段没发成的结局记进 task_runs（同时写一行"模块 · 类别"日志）。errText 是适配器给的原因，
// 进库前由 taskrun.Detail 脱敏；没接 Degrade 的出口（测试、没装配）什么都不做
func (o *Outlet) degrade(began time.Time, attempts int, outcome, class, errText string) {
	var err error
	if errText != "" {
		err = errors.New(errText)
	}
	o.cfg.Degrade.Degrade(taskrun.ModuleSend, class, err, taskrun.Opts{Outcome: outcome, Ms: max(1, o.clock.Now().Sub(began).Milliseconds()), Attempts: attempts})
}
