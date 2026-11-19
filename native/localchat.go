package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/api"
)

type localRun struct {
	busy      bool
	draft     api.LocalDraft
	err       string
	errDetail string // 失败的技术细节（供应商原文），折叠起来才看
	errOpen   bool
	input     string
	cancel    context.CancelFunc
	stopping  bool
	stopped   bool
	ended     bool
	// 新界面输入区（workbench_composer.go）：提交有没有回音、这一轮的编号和停止用的序号
	submitting bool
	runID      string
	through    uint64
	// 插话和排队（workbench_held.go）：已受理、还没轮到的话；放行 / 放弃 held 的话时防连点
	queued   []queuedInput
	heldBusy map[string]bool
	// 消息列里的临时行（live_rows.go）
	pending []*msg
	replies []*msg
	procs   []*msg // 思考和工具（过程气泡），真回复到了就挂到它前面
	handed  int    // 这一轮已经被真消息接替的步数
	replied []int  // 这一轮说过话的步
	typing  *msg
	sentAt  int64
	// 本地群聊：一轮里好几位成员轮流说，actor 是现在说话的这位，turn 分清换了人（live_group.go）
	actor  string
	turn   string
	sealed int
}

type approvalUI struct {
	busy   bool
	detail bool
	err    string
	shown  time.Time // 新界面审批卡第一次出现的时刻
}

func (a *app) localRun(key string) *localRun {
	if a.localRuns == nil {
		a.localRuns = map[string]*localRun{}
	}
	if a.localRuns[key] == nil {
		a.localRuns[key] = &localRun{}
	}
	return a.localRuns[key]
}

// localInput 沿用库 Editor 的布局；尚无宿主的附件和表情动作明确禁用。
func localInput(c *ui.Context, input *string, busy, stopping, stopped bool, submit func(string), stop func()) {
	t := theme.T(c)
	m := t.Metrics().Compose()
	ui.Row(c).FillWidth().Shrink(0).Gap(0).AlignItems(ui.End).
		Padding(0, m.SendRight()).Background(t.Materials().Compose().Background()).Children(func() {
		compose.ActionButton(c.Key("attach"), compose.Attach, compose.ActionOptions{Label: "附件输入尚未接入", Disabled: true})
		ui.Column(c.Key("field-slot")).Grow(1).MinWidth(0).Padding(m.SendPadding(), 0).Children(func() {
			options := compose.FieldOptions{Label: "发一条本地消息", Placeholder: "发一条本地消息", ReadOnly: busy}
			if !busy {
				options.OnSubmit = submit
			}
			compose.Field(c.Key("field"), input, options)
		})
		compose.ActionButton(c.Key("emoji"), compose.Emoji, compose.ActionOptions{Label: "表情选择尚未接入", Disabled: true})
		c.Key("submit")
		if busy && !stopped {
			phase := compose.Generating
			if stopping {
				phase = compose.Stopping
			}
			compose.StopGeneration(c, compose.Generation{Phase: phase}, compose.StopOptions{Label: "停止生成", OnStop: func(string) { stop() }})
		} else {
			compose.ActionButton(c, compose.Send, compose.ActionOptions{Label: "发送", Disabled: busy || strings.TrimSpace(*input) == "", OnClick: func() { submit(*input) }})
		}
	})
}

func localCaption(c *ui.Context, value string) ui.Element {
	t := theme.T(c)
	style := t.Typography().Body()
	return ui.Text(c, value).Font(t.Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).
		FontWeight(style.Weight()).TextColor(t.Colors().Window().MutedText()).Wrap()
}

func (a *app) localComposer(c *ui.Context) {
	if a.demoMode {
		ui.Column(c.Key("demo-compose")).FillWidth().Shrink(0).Children(func() {
			localInput(c, &a.input, false, false, false, func(text string) { a.input = text; a.send() }, nil)
		})
		return
	}
	k := a.keyOf(a.sel)
	if k.Platform != "local" || k.Channel != "console" || (k.Scene != "private" && k.Scene != "group") {
		localCaption(c, "这里仅查看记录：QQ 发送需走平台发送规则")
		return
	}
	if off := a.host.localSendOff(); off != "" { // 只看历史阶段不能发（hostState.localSendOff）
		localCaption(c, off)
		return
	}
	writer := a.reader
	if k.Scene == "group" {
		if _, ok := writer.(api.LocalGroupWriter); !ok {
			localCaption(c, api.ErrLocalGroupUnsupported.Error())
			return
		}
	} else if _, ok := writer.(api.LocalChat); !ok {
		localCaption(c, api.ErrLocalChatUnsupported.Error())
		return
	}
	state := a.localRun(a.sel)
	status := a.localStatus[k.Conversation]
	ui.Column(c.Key(k)).FillWidth().Shrink(0).Children(func() {
		a.localStatusLines(c, k, status)
		if state.err != "" {
			localCaption(c, state.err).TextColor(theme.T(c).Colors().Semantic().Danger())
		}
		if state.stopped {
			localCaption(c, "已停止")
		}
		if state.draft.Text != "" {
			ui.Scroll(c.Key("local-stream")).MaxHeight(200).FillWidth().Label("正在生成的回复").Children(func() {
				ui.Text(c, state.draft.Text).Wrap().Selectable()
			})
		}
		if k.Scene == "private" {
			a.localApprovalCards(c, k)
			a.workbenchQuestions(c, k) // 问题卡新旧界面共用（旧输入框等待时只读，打字回答只在新界面）
		} else if state.busy && !state.stopped {
			localCaption(c, "正在回复")
		}
		// 宿主自动跑的轮（目标续轮、定期任务）也显示停止；点停止同时暂停目标
		auto := status.AutoRunning && !state.busy
		localInput(c, &state.input, state.busy || auto, state.stopping, state.stopped && state.busy, func(text string) { a.sendLocal(writer, k, text) }, func() {
			if auto {
				a.stopAutoLocal(writer, k)
				return
			}
			a.stopLocal(writer, k)
		})
	})
}

func (a *app) sendLocal(writer api.Reader, k api.ConversationKey, text string) {
	a.sendLocalShown(writer, k, text, text)
}

// sendLocalShown 发出 text，消息列里先显示 shown：折叠粘贴的全文只发给后台，不铺进待发气泡。
func (a *app) sendLocalShown(writer api.Reader, k api.ConversationKey, text, shown string) {
	state := a.localRun(k.Conversation)
	if state.busy || strings.TrimSpace(text) == "" {
		return
	}
	state.busy, state.err, state.draft, state.stopped = true, "", api.LocalDraft{}, false
	state.turn, state.actor, state.sealed = "", "", 0
	state.input = ""
	if a.workbenchEnabled() {
		a.addPending(k.Conversation, shown)
	}
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	// UI 当场持有这次请求的取消权：worker 尚未登记时按停止也能取消。
	runCtx, cancel := context.WithCancel(ctx)
	state.cancel = cancel
	state.ended = false
	go func() {
		defer cancel()
		var err error
		if k.Scene == "group" {
			req := api.LocalGroupSend{Key: k, Text: text}
			if a.workbenchEnabled() {
				alive := func() bool { return a.srcGen == gen && ctx.Err() == nil && !state.stopped }
				req.Draft = func(v api.LocalDraft) {
					a.post(func() {
						if alive() {
							a.liveGroupDraft(k.Conversation, state, v)
						}
					})
				}
				req.Process = func(turn string, steps []api.LocalProcessStep) {
					a.post(func() {
						if alive() && turn == state.turn {
							a.liveProcessUpdate(k.Conversation, state, steps)
						}
					})
				}
			}
			err = writer.(api.LocalGroupWriter).SendLocalGroup(runCtx, req)
		} else {
			err = writer.(api.LocalChat).SendLocal(runCtx, k, text, func(v api.LocalDraft) {
				a.post(func() {
					if a.srcGen == gen && ctx.Err() == nil && !state.stopping && !state.stopped {
						state.draft = v
					}
				})
			})
		}
		// 内核在请求前或审批等待中被停下时也会返回普通 aborted 错误，按这次请求的取消事实认。
		stopped := runCtx.Err() != nil
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			state.ended, state.busy, state.draft = true, state.stopping, api.LocalDraft{}
			state.cancel = nil
			if a.workbenchEnabled() {
				a.clearLive(k.Conversation, state)
			}
			if err != nil && !stopped && !errors.Is(err, context.Canceled) {
				state.err = "发送失败：" + err.Error()
			}
		})
	}()
}

func (a *app) stopLocal(writer api.Reader, k api.ConversationKey) {
	state := a.localRun(k.Conversation)
	if !state.busy || state.stopping || state.stopped {
		return
	}
	// 确认停止后立刻显示已停止；busy 留到发送 worker 真实退出，宽限在后台走。
	state.stopping = true
	state.stopped = true
	state.draft = api.LocalDraft{}
	a.clearLive(k.Conversation, state)
	state.cancel()
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var err error
		if k.Scene == "group" {
			err = writer.(api.LocalGroupWriter).StopLocalGroup(ctx, k)
		} else {
			err = writer.(api.LocalChat).StopLocal(ctx, k)
		}
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			state.stopping = false
			if err != nil {
				state.stopped = false
				state.err = "停止失败：" + err.Error()
			}
			state.busy = !state.ended
		})
	}()
}

func (a *app) watchLocalApprovals(ctx context.Context, writer api.LocalChat, gen uint64) {
	sub, err := writer.WatchLocalApprovals(ctx)
	if errors.Is(err, api.ErrLocalChatUnsupported) {
		return
	}
	if err != nil {
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil {
				a.approvalErr = "读取审批失败：" + err.Error()
			}
		})
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		value := snap.Value
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			a.approvals = value
			active := map[string]bool{}
			for _, v := range value {
				active[v.ID] = true
			}
			for id := range a.approvalState {
				if !active[id] {
					delete(a.approvalState, id)
				}
			}
		})
	}
}

func (a *app) localApprovalCards(c *ui.Context, k api.ConversationKey) {
	if a.approvalErr != "" {
		localCaption(c, a.approvalErr).TextColor(theme.T(c).Colors().Semantic().Danger())
	}
	has := false
	for _, v := range a.approvals {
		if v.Key == k {
			has = true
			break
		}
	}
	if !has {
		return
	}
	c.Key("local-approvals")
	ui.Scroll(c).MaxHeight(280).FillWidth().Children(func() {
		ui.Column(c).Gap(c.Theme().Space(2)).Children(func() {
			for _, v := range a.approvals {
				if v.Key != k {
					continue
				}
				if a.approvalState == nil {
					a.approvalState = map[string]*approvalUI{}
				}
				state := a.approvalState[v.ID]
				if state == nil {
					state = &approvalUI{}
					a.approvalState[v.ID] = state
				}
				status, statusText := control.Awaiting, ""
				if state.busy {
					status, statusText = control.Submitting, "正在提交审批决定"
				} else if state.err != "" {
					status = control.ApprovalFailed
				}
				summary := strings.Join([]string{v.Tool, v.Subject, v.Why}, "\n")
				if v.Purpose != "" {
					summary += "\n" + v.Purpose
				}
				control.ApprovalCard(c, control.Approval{ID: v.ID, Title: v.Title, Summary: summary, Expanded: state.detail,
					Status: status, StatusText: statusText, Error: state.err}, control.ApprovalOptions{
					DetailsLabel: "审批详情", AllowLabel: "允许一次", RejectLabel: "拒绝", CanRetry: true,
					OnExpand: func(expanded bool) { state.detail = expanded },
					OnAction: func(_ string, action control.ApprovalAction) {
						choice := api.ApproveDeny
						if action == control.AllowOnce {
							choice = api.ApproveOnce
						}
						a.decideLocal(v, state, choice)
					},
				}, func() {
					ui.Text(c, strings.Join(append([]string{v.Preview}, v.Notes...), "\n")).Wrap().Selectable()
				})
			}
		})
	})
}

func (a *app) decideLocal(v api.LocalApproval, state *approvalUI, choice api.ApprovalChoice) {
	writer := a.reader.(api.LocalChat)
	state.busy = true
	state.err = ""
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		err := writer.DecideLocal(ctx, v.Key, v.ID, choice)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			state.busy = false
			if err != nil {
				state.err = err.Error()
			}
		})
	}()
}
