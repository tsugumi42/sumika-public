package main

import (
	"context"
	"errors"
	"strconv"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/turn"
)

// 回复进行时的插话和排队、重启后没处理的话（held）、结局原因的显示。
//
// 插话（Enter）：内核把这句话交给正在跑的那一轮，她在下一个安全点（一步做完、工具返回之后）读到；
// 排队（Alt+Enter）：这一轮说完才开始处理，输入框上方列出来，轮到之前可以取消。
// 停止（Esc、停止键）一并取消排队的话（后台按序号一起停），不破坏分三段的停止。

// queuedInput 是已经受理、还没轮到的一句话（界面自己记；内核的持久队列才是真相）。
type queuedInput struct {
	InputID, RunID, Text string
	Seq                  uint64
	withdrawing          bool
}

func (st *localRun) fail(f runFail) { st.err, st.errDetail, st.errOpen = f.Line, f.Detail, false }

func (a *app) held(conv string) []api.HeldInput { return a.heldBy[conv] }

// refreshHeld 重新取一遍重启后没处理的话（所有会话）；连上后台和换会话时调。
func (a *app) refreshHeld() {
	inputs, ok := a.reader.(api.LocalHeld)
	if !ok || a.demoMode || a.reader == nil {
		return
	}
	ctx, gen := a.viewCtx, a.srcGen
	if ctx == nil {
		return
	}
	go func() {
		list, err := inputs.HeldLocal(ctx)
		if err != nil {
			return // 后台还没起来，或这个数据源不记这些：没有就不显示，下次再取
		}
		by := map[string][]api.HeldInput{}
		for _, h := range list {
			by[h.Key.Conversation] = append(by[h.Key.Conversation], h)
		}
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil {
				a.heldBy = by
			}
		})
	}()
}

func (a *app) removeHeld(conv, id string) {
	list := a.heldBy[conv]
	for i, h := range list {
		if h.InputID == id {
			a.heldBy[conv] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// releaseHeld 是“现在发”：后台按排队处理这句话；她正忙就接在已排的后面，闲着就直接接上这一轮。
func (a *app) releaseHeld(k api.ConversationKey, h api.HeldInput) {
	inputs, ok := a.reader.(api.LocalHeld)
	li, ok2 := a.reader.(api.LocalInputs)
	if !ok || !ok2 {
		return
	}
	st := a.localRun(k.Conversation)
	if st.heldBusy[h.InputID] {
		return
	}
	if st.heldBusy == nil {
		st.heldBusy = map[string]bool{}
	}
	st.heldBusy[h.InputID] = true
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		rec, err := inputs.ReleaseHeldLocal(ctx, k, h.InputID)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			delete(st.heldBusy, h.InputID)
			if err != nil {
				st.fail(userFail("没能放行这句话", err))
				return
			}
			a.removeHeld(k.Conversation, h.InputID)
			st.err, st.errDetail = "", ""
			q := queuedInput{InputID: rec.InputID, RunID: rec.RunID, Seq: rec.Sequence, Text: clipRunes(h.Text, 60)}
			st.through = max(st.through, rec.Sequence)
			if st.busy {
				st.queued = append(st.queued, q)
				return
			}
			st.queued = append(st.queued, q)
			a.startNextQueued(ctx, li, k, gen, st)
		})
	}()
}

// discardHeld 是“算了”：后台只把它标成取消，聊天里的原文和库里的记录都留着。
func (a *app) discardHeld(k api.ConversationKey, h api.HeldInput) {
	inputs, ok := a.reader.(api.LocalHeld)
	if !ok {
		return
	}
	st := a.localRun(k.Conversation)
	if st.heldBusy[h.InputID] {
		return
	}
	if st.heldBusy == nil {
		st.heldBusy = map[string]bool{}
	}
	st.heldBusy[h.InputID] = true
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		err := inputs.DiscardHeldLocal(ctx, k, h.InputID)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			delete(st.heldBusy, h.InputID)
			if err != nil {
				st.fail(userFail("没能放弃这句话", err))
				return
			}
			a.removeHeld(k.Conversation, h.InputID)
		})
	}()
}

// withdrawQueued 是“取消排队”：后台把这句话那一轮取消；原文仍留在聊天记录里。
// 已经开始处理的取消不了，界面照实说，并继续跟着它。
func (a *app) withdrawQueued(k api.ConversationKey, st *localRun, id string) {
	inputs, ok := a.reader.(api.LocalHeld)
	if !ok {
		return
	}
	idx := -1
	for i := range st.queued {
		if st.queued[i].InputID == id && !st.queued[i].withdrawing {
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	st.queued[idx].withdrawing = true
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		err := inputs.WithdrawQueuedLocal(ctx, k, id)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			for i := range st.queued {
				if st.queued[i].InputID != id {
					continue
				}
				if err == nil {
					st.queued = append(st.queued[:i:i], st.queued[i+1:]...)
				} else {
					st.queued[i].withdrawing = false
				}
				break
			}
			if err != nil {
				if errors.Is(err, turn.ErrInputNotPending) || err.Error() == turn.ErrInputNotPending.Error() {
					st.fail(runFail{Line: "这句话已经开始处理了，取消不了"})
				} else {
					st.fail(userFail("没能取消排队", err))
				}
			}
		})
	}()
}

// startNextQueued：这一轮收尾（或放行了一句）以后，跟上排在最前面的那一句的那一轮。调用方在界面线程里。
func (a *app) startNextQueued(ctx context.Context, inputs api.LocalInputs, k api.ConversationKey, gen uint64, st *localRun) {
	if len(st.queued) == 0 || st.busy {
		return
	}
	next := st.queued[0]
	st.queued = st.queued[1:]
	st.busy, st.stopping, st.stopped, st.draft = true, false, false, api.LocalDraft{}
	st.runID = next.RunID
	st.startRun()
	a.reconcileLive()
	go a.watchComposerRun(ctx, inputs, k, gen, st, next.RunID)
}

// workbenchError 画输入区上方的失败：常驻一行原因和做法，有细节的带“详情”折叠。
func (a *app) workbenchError(c *ui.Context, st *localRun) {
	if st.err == "" {
		return
	}
	th := c.Theme()
	ui.Row(c.Key("composer-error-row")).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c.Key("composer-error"), st.err).Font(th.Font).TextColor(th.Danger).Wrap().Grow(1).MinWidth(0)
		if st.errDetail != "" {
			label := "详情"
			if st.errOpen {
				label = "收起"
			}
			a.wbButton(c, "composer-error-toggle", label, false, func() { st.errOpen = !st.errOpen })
		}
	})
	if st.errOpen && st.errDetail != "" {
		ui.Text(c.Key("composer-error-detail"), st.errDetail).Font(th.Font).TextColor(th.TextMuted).Wrap().Selectable().FillWidth()
	}
}

// wbButton 是输入区附近的小按钮：外壳建好了用外壳的，测试里退回基础按钮。
func (a *app) wbButton(c *ui.Context, key, label string, primary bool, fn func()) {
	if kit := a.workbench.shell.kit; kit != nil {
		kit.Button(c, key, label, primary, false, fn)
		return
	}
	th := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(32).Padding(10, 0).Radius(8).OnClick(fn)
	bg, ink := th.Surface, th.Text
	if primary {
		bg, ink = th.Accent, th.AccentText
	}
	b.Background(bg).TextColor(ink).Center().Children(func() {
		ui.Text(c, label).Font(th.Font).SingleLine()
	})
}

// workbenchHeld 画这个会话里重启后没处理的话：每句一行，写明“还没处理”，给“现在发”和“算了”。
func (a *app) workbenchHeld(c *ui.Context, k api.ConversationKey) {
	list := a.held(k.Conversation)
	if len(list) == 0 {
		return
	}
	th := c.Theme()
	ui.Scroll(c.Key("held-list")).MaxHeight(200).FillWidth().Shrink(0).Label("没处理的话").Children(func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			for _, h := range list {
				h := h
				key := "held-" + h.InputID
				ui.Row(c.Key(key)).FillWidth().Gap(8).Padding(8, 12).Radius(10).Border(1, th.Border).Background(th.Surface).AlignItems(ui.Center).Children(func() {
					ui.Column(c.Key(key + "-text")).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c.Key(key+"-title"), "这句话还没处理（重启时没来得及回）").Font(th.Font).TextColor(th.TextMuted).SingleLine()
						ui.Text(c.Key(key+"-body"), clipRunes(h.Text, 80)).Font(th.Font).TextColor(th.Text).MaxLines(2).Wrap()
					})
					a.wbButton(c, key+"-send", "现在发", true, func() { a.releaseHeld(k, h) })
					a.wbButton(c, key+"-drop", "算了", false, func() { a.discardHeld(k, h) })
				})
			}
		})
	})
}

// workbenchQueued 画排队中的话：每句一行，轮到之前可以取消；回复进行时再加一行按键提示。
func (a *app) workbenchQueued(c *ui.Context, k api.ConversationKey, st *localRun, showHint bool) {
	th := c.Theme()
	if len(st.queued) > 0 {
		ui.Scroll(c.Key("queued-list")).MaxHeight(140).FillWidth().Shrink(0).Label("排队中的话").Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				for i, q := range st.queued {
					q := q
					key := "queued-" + strconv.Itoa(i) + "-" + q.InputID
					ui.Row(c.Key(key)).FillWidth().Gap(8).Padding(8, 12).Radius(10).Border(1, th.Border).Background(th.Surface).AlignItems(ui.Center).Children(func() {
						ui.Text(c.Key(key+"-text"), "排队中，说完这一轮再处理：「"+clipRunes(q.Text, 40)+"」").Font(th.Font).TextColor(th.TextMuted).SingleLine().Grow(1).MinWidth(0)
						label := "取消排队"
						if q.withdrawing {
							label = "取消中…"
						}
						a.wbButton(c, key+"-cancel", label, false, func() { a.withdrawQueued(k, st, q.InputID) })
					})
				}
			})
		})
	}
	if showHint {
		ui.Text(c.Key("composer-steer-hint"), "回车插话（她做完这一步就看到）· Alt+回车排队（说完这一轮再处理）· Esc 停止").Font(th.Font).FontSize(th.FontSize - 2).TextColor(th.TextMuted).SingleLine().FillWidth()
	}
}
