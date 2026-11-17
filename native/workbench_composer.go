package main

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 新界面的输入区（chatui.Composer）接后台：本地私聊走 api.LocalInputs（提交有回音，被拒时保留草稿），
// 审批走 api.LocalChat；本地群聊仍是只发文字，走旧的 SendLocalGroup。

func (a *app) composerOf(conversation string) *chatui.Composer {
	if a.composers == nil {
		a.composers = map[string]*chatui.Composer{}
	}
	p := a.composers[conversation]
	if p == nil {
		p = chatui.NewComposer()
		a.composers[conversation] = p
	}
	return p
}

// chatTokens 是新界面的令牌：外壳那一份（原型色板）；外壳还没建起来的测试里退回默认设置。
func (a *app) chatTokens() chatui.Tokens {
	if k := a.workbench.shell.kit; k != nil {
		return k.T
	}
	set := chatui.DefaultSettings()
	set.Dark = a.look.dark // 外壳还没建起来（测试里）：深浅取窗口当前的
	return chatui.Derive(set)
}

func composerColors(th *ui.Theme) chatui.ComposerColors {
	return chatui.ComposerColors{Fill: th.Surface, Card: th.Background, Line: th.Border, Text: th.Text, Muted: th.TextMuted,
		Accent: th.Accent, OnAccent: th.AccentText, Danger: th.Danger}
}

func (a *app) workbenchNote(c *ui.Context, text string) {
	if kit := a.workbench.shell.kit; kit != nil {
		kit.NoteBar(c, "composer-note", text)
		return
	}
	th := c.Theme()
	ui.Text(c.Key("composer-note"), text).Font(th.Font).TextColor(th.TextMuted).Wrap().FillWidth().Padding(8, 0)
}

// workbenchComposer 画聊天列最下面的输入区：流式草稿、审批卡、输入框。
func (a *app) workbenchComposer(c *ui.Context, column float32, dropped []string, droppedText string, dropHot bool) {
	// 照 Grok bot 的基线（设计规格-照Grokbot）：输入框居中、限宽，和消息列同宽
	ui.Column(c.Key("workbench-composer")).FillWidth().Shrink(0).AlignItems(ui.Center).Children(func() {
		ui.Column(c.Key("composer-column")).FillWidth().MaxWidth(column).Gap(a.chatTokens().S2).Children(func() {
			if a.demoMode {
				p := a.composerOf(a.sel)
				h := a.composerHost(c, p, nil, func() {
					a.input, p.Main.Text, p.Main.Atts = p.Main.SendText(), "", nil
					a.send()
				}, nil)
				h.Draw(c, p)
				return
			}
			k := a.keyOf(a.sel)
			switch {
			case k.Platform != "local" || k.Channel != "console" || (k.Scene != "private" && k.Scene != "group"):
				a.workbenchWhisperApprovals(c, k) // 悄悄话处理那一轮的审批卡（批的是本地那一轮，不是 QQ）
				if a.workbenchHandoff(c, k) {     // QQ 场合：有引用卡时出现“交给她”（悄悄话，不发到 QQ）
					return
				}
				a.workbenchNote(c, "这里只能看。要说话就到 QQ 里发，她们会在那边回")
				return
			case a.host.localSendOff() != "":
				a.workbenchNote(c, a.host.localSendOff())
				return
			}
			if a.picking() { // 多选时输入区换成操作条（规格 3.3）
				a.workbenchPickBar(c)
				return
			}
			writer := a.reader
			private := k.Scene == "private"
			if private {
				if _, ok := writer.(api.LocalInputs); !ok {
					a.workbenchNote(c, api.ErrLocalChatUnsupported.Error())
					return
				}
			} else if _, ok := writer.(api.LocalGroupWriter); !ok {
				a.workbenchNote(c, api.ErrLocalGroupUnsupported.Error())
				return
			}
			st := a.localRun(a.sel)
			p := a.composerOf(a.sel)
			status := a.localStatus[k.Conversation]
			if private {
				a.workbenchApprovals(c, k)
				a.workbenchQuestions(c, k)
				a.workbenchStatus(c, k, status)
				a.workbenchHeld(c, k)
			}
			if !private {
				a.workbenchWhisperApprovals(c, k) // 本地群聊：悄悄话处理那一轮的审批卡
			}
			if st.stopped {
				a.workbenchNote(c, "已停止")
			} else if a.host.CatalogError != "" {
				a.workbenchCatalogNote(c) // 能发消息、来源却连不上：只显示这一行，不带错误原文（规格-模型配置 6.1）
			}
			a.workbenchError(c, st)
			if private {
				a.workbenchQueued(c, k, st, st.busy && !st.stopped && !st.stopping)
			}
			h := a.composerHost(c, p, st, func() { a.submitComposer(writer, k, p) }, func() { a.stopComposer(writer, k) })
			if private { // 回复进行时：回车插话、Alt+回车排队；本地群聊仍然只能停
				h.OnSteer = func() { a.submitLive(writer, k, p, "steer") }
				h.OnQueue = func() { a.submitLive(writer, k, p, "queue") }
			}
			if !private && a.whisperKey(k) {
				// 本地群聊：引用卡是悄悄话。群里有人在回复时，有卡就能把卡交出去（群消息照旧等这一轮完）
				p.Whisper = true
				h.Targets = a.whisperTargets("")
				if len(p.Cards) > 0 {
					h.OnSteer = func() { a.submitWhisper(writer, k, p, false) }
				}
			}
			if private && a.hasQuestion(k) && p.Sendable() {
				h.Busy = false  // 她在等回答：输入框有字时发送键回来，这句话就是回答（规格-向主人提问 第四节）
				h.OnQueue = nil // 排队不算回答（同节第 7 条）：这时 Alt+回车什么都不做，不会被当成普通发送
			}
			// 模型、工作区、审批方式在右栏“上下文”栏顶部（ADR-351），输入框里只有字和发送键
			if auto := private && status.AutoRunning && !st.busy; auto {
				// 宿主自动跑的轮（目标续轮、定期任务）也显示停止；点停止同时暂停目标
				h.Busy = true
				h.OnStop = func() { a.stopAutoLocal(writer, k) }
				// 有字时键会变成“发送”：这里没有本界面自己的一轮（st.runID 为空），
				// 按 start 发出去，后台自己转成插话或排队（app/local_inputs.go），不能静默吞掉。
				h.OnSteer = func() { a.submitComposer(writer, k, p) }
				h.OnQueue = h.OnSteer
			}
			h.OnOpenText = a.openPasteView // 查看框是全局的，群聊折叠的药丸也要能点开
			if private {
				if lister, ok := writer.(api.SlashLister); ok { // 命令名单取自后台的登记表；群聊不过命令表，不补全
					h.Commands = func() []chatui.SlashItem { return slashItems(lister.SlashCommands()) }
				}
				h.DropHot = dropHot
				h.OnPasteImage = func(b *chatui.Block, png []byte) { a.pasteImage(k, p, b, png) }
				h.OnFiles = func(b *chatui.Block, paths []string) { a.attachPaths(p, b, paths) }
				if len(dropped) > 0 {
					a.attachPaths(p, nil, dropped)
				}
				if droppedText != "" {
					p.AppendText(nil, droppedText)
				}
			}
			a.composerWindowKeys(c, h, p, k)
			h.Draw(c, p)
		})
	})
}

func (a *app) composerHost(c *ui.Context, p *chatui.Composer, st *localRun, send, stop func()) chatui.ComposerHost {
	h := chatui.ComposerHost{T: a.chatTokens(), C: composerColors(c.Theme()), Font: c.Theme().Font, OnSend: send, OnStop: stop, QuoteThumb: a.messageThumb, Past: a.pastTexts}
	if k := a.workbench.shell.kit; k != nil { // 输入框外框跟面板材质（液态玻璃、磨砂、实色）走
		fill := h.C.Fill
		h.Surface = func(e ui.Element) { k.Glass(e, winshell.LevelInput, fill) }
	}
	h.OnOpenImage = func(id uint64) { a.openComposerImage(p, id) }
	if st != nil {
		h.Busy, h.Stop, h.Locked = st.busy && !st.stopped, st.stopping, st.submitting || (st.busy && st.stopped)
	}
	return h
}

// draftHost 是流式草稿用的宿主。
func (a *app) draftHost(c *ui.Context, column float32) chatui.Host {
	return chatui.Host{T: a.chatTokens(), Font: c.Theme().Font, SelfChar: chatui.DefaultChar, Group: a.group, Column: column, NoEnter: true}
}

// approvalHost 是审批卡用的宿主。
func (a *app) approvalHost(c *ui.Context) chatui.Host {
	h := chatui.Host{T: a.chatTokens(), Font: c.Theme().Font, SelfChar: chatui.DefaultChar}
	if k := a.settingsKit(); k != nil { // “更多选择”菜单画在窗口里，高亮和别的菜单一样是中性浅底
		h.MenuTheme = k.MenuTheme()
	}
	return h
}

func (a *app) workbenchApprovals(c *ui.Context, k api.ConversationKey) {
	a.drawApprovals(c, func(v api.LocalApproval) bool { return v.Key == k && v.Whisper == nil })
}

// workbenchWhisperApprovals 是来源会话（本地群聊、QQ 私聊、QQ 群）窗口里关于悄悄话审批的一行提示：悄悄话处理那一轮的审批卡
// 现在放在右栏的“对话日志”里批（规格-群聊引用和路由 8.3、第十五节），这里只在有卡等着时指过去。
// 键直接写成群会话的卡仍然不画（群里不能有可操作的普通审批卡）。
func (a *app) workbenchWhisperApprovals(c *ui.Context, k api.ConversationKey) {
	if !a.whisperKey(k) {
		return
	}
	a.sideLogPendingHint(c, k)
}

func (a *app) drawApprovals(c *ui.Context, match func(api.LocalApproval) bool) {
	th := c.Theme()
	if a.approvalErr != "" {
		ui.Text(c.Key("approval-error"), a.approvalErr).Font(th.Font).TextColor(th.Danger).Wrap().FillWidth()
	}
	var mine []api.LocalApproval
	byID := map[string]api.LocalApproval{}
	for _, v := range a.approvals {
		if match(v) {
			mine = append(mine, v)
			byID[v.ID] = v
		}
	}
	if len(mine) == 0 {
		return
	}
	h := a.approvalHost(c)
	decide := func(id string, choice api.ApprovalChoice) {
		if v, ok := byID[id]; ok {
			a.decideLocal(v, a.approvalStateOf(id), choice)
		}
	}
	h.OnApprove = func(id string, scope chatui.ApprovalScope) {
		switch scope {
		case chatui.AllowConversation:
			decide(id, api.ApproveConversation)
		case chatui.AllowAlways:
			decide(id, api.ApproveAlways)
		default:
			decide(id, api.ApproveOnce)
		}
	}
	h.OnDeny = func(id, _ string) { decide(id, api.ApproveDeny) }
	ui.Scroll(c.Key("local-approvals")).MaxHeight(280).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(h.T.S2).Children(func() {
			for _, v := range mine {
				state := a.approvalStateOf(v.ID)
				if state.shown.IsZero() {
					state.shown = c.Now()
				}
				lead, notes := "", v.Notes
				if v.Whisper != nil && len(notes) > 0 { // 悄悄话卡：来历那一行放在摘要最前面，不折起来
					lead, notes = notes[0], notes[1:]
				}
				info := chatui.ApprovalInfo{ID: v.ID, Title: v.Title, Summary: strings.Join(nonEmpty(lead, v.Tool, v.Subject, v.Why, v.Purpose, workdirLine(v.Workdir)), "\n"),
					Shown: state.shown, Error: state.err, Detail: strings.Join(nonEmpty(append([]string{v.Preview}, notes...)...), "\n"), KindLabel: v.KindLabel}
				if e := egressInfo(v.Egress); e != nil {
					info.Egress = e
					info.Detail = strings.Join(nonEmpty(info.Detail, egressDetail(v.Egress)), "\n")
				}
				for _, o := range v.Options {
					switch o {
					case api.ApproveConversation:
						info.Scopes = append(info.Scopes, chatui.AllowConversation)
					case api.ApproveAlways:
						info.Scopes = append(info.Scopes, chatui.AllowAlways)
					}
				}
				if state.busy {
					info.State = chatui.ApprovalSubmitting
				}
				h.ApprovalCard(c, info, &state.detail, th.Border, th.Surface)
			}
		})
	})
}

// egressInfo 把审批卡的外发地址换成卡上要画的块：危险的在前（后台已排好），危险的行后面跟原因。
func egressInfo(e *api.ApprovalEgress) *chatui.ApprovalEgress {
	if e == nil || (len(e.Targets) == 0 && !e.Unchecked) {
		return nil
	}
	out := &chatui.ApprovalEgress{ID: e.ID, Total: len(e.Targets), Danger: e.Danger, Unchecked: e.Unchecked}
	for _, t := range e.Targets {
		out.Lines = append(out.Lines, chatui.EgressLine{Text: egressText(t), Danger: t.Danger})
	}
	return out
}

func egressText(t api.EgressTarget) string {
	if t.Danger && t.Reason != "" {
		return t.Value + "（" + t.Reason + "）"
	}
	return t.Value
}

// egressDetail 是详情里的完整地址清单（卡上只露出前几行）
func egressDetail(e *api.ApprovalEgress) string {
	if e == nil || len(e.Targets) == 0 {
		return ""
	}
	lines := []string{"外发地址 " + e.ID + "："}
	for _, t := range e.Targets {
		mark := "  "
		if t.Danger {
			mark = "！"
		}
		lines = append(lines, mark+egressText(t))
	}
	return strings.Join(lines, "\n")
}

// workdirLine 是审批卡上“在哪个项目里”的一行；没有目录（旧数据）不写。
func workdirLine(dir string) string {
	if dir == "" {
		return ""
	}
	return "目录：" + dir
}

func nonEmpty(items ...string) []string {
	var out []string
	for _, s := range items {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (a *app) approvalStateOf(id string) *approvalUI {
	if a.approvalState == nil {
		a.approvalState = map[string]*approvalUI{}
	}
	state := a.approvalState[id]
	if state == nil {
		state = &approvalUI{}
		a.approvalState[id] = state
	}
	return state
}

var clientKeySeq uint64

func newClientKey() string {
	clientKeySeq++
	return "wb-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(clientKeySeq, 10)
}

// submitComposer 把输入区发出去。私聊先交给后台核对（有回音）：收下了才清草稿，被拒了原样保留；
// 群聊只发文字，沿用旧的整轮发送。
func (a *app) submitComposer(writer api.Reader, k api.ConversationKey, p *chatui.Composer) {
	a.submitMode(writer, k, p, "start")
}

// submitLive 是她在回复时发话：steer 插话（她做完这一步就看到）、queue 排队（这一轮说完再处理）。
// 只在这一轮正常进行时有效：已经在停、已经停了、还没拿到这一轮的编号，都不发，草稿留着。
func (a *app) submitLive(writer api.Reader, k api.ConversationKey, p *chatui.Composer, mode string) {
	st := a.localRun(k.Conversation)
	if k.Scene != "private" || !st.busy || st.stopping || st.stopped || st.runID == "" {
		return
	}
	a.submitMode(writer, k, p, mode)
}

func (a *app) submitMode(writer api.Reader, k api.ConversationKey, p *chatui.Composer, mode string) {
	st := a.localRun(k.Conversation)
	live := mode != "start"
	answering := !live && st.busy && k.Scene == "private" && a.hasQuestion(k) && !st.stopping && !st.stopped // 回答她的提问：插进正在等的这一轮
	if (st.busy && !live && !answering) || st.submitting || !p.Sendable() {
		return
	}
	if k.Scene == "group" {
		if len(p.Cards) > 0 { // 引用卡是悄悄话：先交给被指的角色，收下后再发群消息（workbench_whisper.go）
			a.submitWhisper(writer, k, p, true)
			return
		}
		a.sendGroupMain(writer, k, p)
		return
	}
	inputs := writer.(api.LocalInputs)
	p.Notice = ""
	if !p.Check() {
		return
	}
	blocks := p.Blocks()
	sub := api.LocalInputSubmission{Mode: mode, ClientKey: newClientKey()}
	if mode == "steer" {
		sub.ExpectedRunID = st.runID
	}
	if len(blocks) == 1 && blocks[0] == p.Main && len(p.Main.Atts) == 0 {
		sub.Text = p.Main.SendText()
	} else {
		sub.Parts = composerParts(k, blocks)
	}
	st.submitting = true
	st.fail(runFail{})
	p.Begin(blocks)
	sent := a.addPending(k.Conversation, composerPreview(blocks))
	sent.liveLoose = sub.Text == ""
	if sub.Text == "" { // 分块发的话，待发气泡就按块画：引用、附件一开始就在，存好接替时不跳
		sent.chatParts = pendingParts(blocks)
		sent.bindPartImages(sent.chatParts, pendingPartImages(blocks))
	}
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		rec, err := inputs.SubmitLocal(ctx, k, sub)
		if err == nil && (rec.State == "rejected" || rec.State == "cancelled") {
			err = errors.New("这条消息没有被收下")
		}
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			st.submitting = false
			if err != nil {
				a.dropPending(k.Conversation, sent)
				p.Reject(blocks)
				a.rejectComposer(st, p, blocks, err)
				return
			}
			p.Remember(composerPreview(blocks))
			p.Accept(blocks)
			if rec.State == "command" {
				a.dropPending(k.Conversation, sent) // 斜杠命令不是一句话，回复是会话里的系统行
				return                              // 后台已执行，没有跑起一轮
			}
			if answering && st.busy && rec.RunID == st.runID {
				// 插进了正在跑的这一轮：已经在监听，不重开
				st.through = max(st.through, rec.Sequence)
				a.reconcileLive()
				return
			}
			if live {
				a.afterLiveSubmit(ctx, inputs, k, gen, st, rec, sent.text, mode) // 此时输入框已清空，文字取待发气泡上的预览
				return
			}
			st.busy, st.stopping, st.stopped, st.draft = true, false, false, api.LocalDraft{}
			st.runID, st.through = rec.RunID, rec.Sequence
			st.startRun()
			a.reconcileLive()
			go a.watchComposerRun(ctx, inputs, k, gen, st, rec.RunID)
		})
	}()
}

// watchComposerRun 跟着这一轮，直到后台实际排空才恢复输入。
func (a *app) watchComposerRun(ctx context.Context, inputs api.LocalInputs, k api.ConversationKey, gen uint64, st *localRun, run string) {
	end := func(f runFail, stopped bool, requeued []api.LocalRequeued) {
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil || st.runID != run {
				return
			}
			stopped = stopped || st.stopped
			st.busy, st.stopping, st.draft, st.runID = false, false, api.LocalDraft{}, ""
			if f.Line != "" {
				st.fail(f)
			}
			a.clearLive(k.Conversation, st)
			if stopped {
				st.queued = nil // 停下后排队的先不发：后台已经按序号一起取消了
				return
			}
			// 收尾时才到的插话被后台改排成了新的几轮：放进排队，界面跟着跑，不能显示空闲却停不了
			for _, q := range requeued {
				if !slices.ContainsFunc(st.queued, func(x queuedInput) bool { return x.InputID == q.InputID }) {
					st.queued = append(st.queued, queuedInput{InputID: q.InputID, RunID: q.RunID, Seq: q.Seq, Text: clipRunes(q.Text, 60)})
				}
			}
			a.startNextQueued(ctx, inputs, k, gen, st)
		})
	}
	sub, err := inputs.WatchLocalRun(ctx, k, run)
	if err != nil {
		end(userFail("读取回复进度失败", err), false, nil)
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		v := snap.Value
		stopped := v.Outcome == "stopped"
		text, step, sealed, process := v.Text, v.Step, v.SealedStep, v.Process
		requeued := v.Requeued
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil && st.runID == run {
				if stopped && !st.stopped {
					st.stopped = true
					st.draft = api.LocalDraft{}
					a.clearLive(k.Conversation, st)
				}
				a.liveProcessUpdate(k.Conversation, st, process)
				if !st.stopping && !st.stopped {
					st.draft = api.LocalDraft{Text: text, Step: step}
					a.liveReplyUpdate(k.Conversation, st, step, text, sealed)
				}
			}
		})
		if v.Drained {
			end(runEndFail(v), stopped, requeued)
			return
		}
	}
	if ctx.Err() == nil {
		end(runFail{Line: "和后台的连接断了，读不到这一轮的进度。可以再发一次", Detail: "回复进度订阅中断"}, false, nil) // 不留在忙碌状态，否则发不了也停不掉
	}
}

func (a *app) stopComposer(writer api.Reader, k api.ConversationKey) {
	st := a.localRun(k.Conversation)
	if k.Scene == "group" {
		a.stopLocal(writer, k)
		return
	}
	if !st.busy || st.stopping || st.runID == "" {
		return
	}
	st.stopping = true
	inputs := writer.(api.LocalInputs)
	run, through := st.runID, st.through
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		_, err := inputs.StopLocalRun(ctx, k, run, through)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil || st.runID != run {
				return
			}
			if err != nil {
				st.stopping = false
				st.fail(userFail("没能停下", err))
			} else if !st.stopped {
				st.stopped = true
				st.draft = api.LocalDraft{}
				st.queued = nil
				a.clearLive(k.Conversation, st)
			}
		})
	}()
}

// composerPreview 是发出的话在消息列里先显示的文字：几块的正文连起来；
// 折叠的粘贴只留占位，不带全文（待发气泡和 ↑ 回忆历史都读它）。
func composerPreview(blocks []*chatui.Block) string {
	var parts []string
	for _, b := range blocks {
		if text := b.PreviewText(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// pendingParts 是待发气泡的分块，和后台存好后 chatParts 算出来的同一个样子（主人 2026-11-09：
// 引用回复发送的一瞬间先变成普通消息再跳成有引用）。折叠的粘贴只写占位，和预览一样。
func pendingParts(blocks []*chatui.Block) []chatui.Part {
	out := make([]chatui.Part, 0, len(blocks))
	for _, b := range blocks {
		part := chatui.Part{Text: b.PreviewText()}
		for _, x := range localAttachments(b.Atts) {
			name := x.Name
			if name == "" {
				name = filepath.Base(x.Path)
			}
			part.Atts = append(part.Atts, chatui.PartAtt{Name: name, Image: x.Kind == "image"})
		}
		if q := b.Quote; q != nil {
			part.Quote = &chatui.PartQuote{Target: q.MessageID, Name: strings.TrimSpace(q.Name), Snippet: q.Text, Start: q.Start, End: q.End}
		}
		out = append(out, part)
	}
	return out
}

// composerParts 把输入区的几块转成后台的分块：顺序不变，引用块带被引消息和选中的片段。
func composerParts(k api.ConversationKey, blocks []*chatui.Block) []api.LocalPart {
	parts := make([]api.LocalPart, 0, len(blocks))
	for _, b := range blocks {
		part := api.LocalPart{Text: b.SendText(), Attachments: localAttachments(b.Atts)}
		if q := b.Quote; q != nil {
			part.Quote = &api.LocalQuote{
				Ref:  api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: q.MessageID}},
				Text: q.Text, Start: q.Start, End: q.End,
			}
		}
		parts = append(parts, part)
	}
	return parts
}

var rejectedBlock = regexp.MustCompile(`^第 (\d+) 块：`)

// rejectComposer 把后台的拒收原因标在出错的那一块上；认不出是哪一块的写在输入区上方。
// 后台整条都没发、没写库，所以所有块都还在，用户改完这一块直接再发。
func (a *app) rejectComposer(st *localRun, p *chatui.Composer, blocks []*chatui.Block, err error) {
	msg := err.Error()
	if m := rejectedBlock.FindStringSubmatch(msg); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 1 && n <= len(blocks) {
			p.SetError(blocks[n-1], rejectionText(err, strings.TrimPrefix(msg, m[0])))
			return
		}
	}
	if errors.Is(err, api.ErrQuoteTarget) || errors.Is(err, api.ErrPart) {
		st.fail(runFail{Line: "没有发出去：" + rejectionText(err, msg)})
		return
	}
	st.fail(userFail("没有发出去", err))
}

func rejectionText(err error, detail string) string {
	switch {
	case errors.Is(err, api.ErrQuoteTarget):
		return "引用的消息已经看不到了"
	case errors.Is(err, api.ErrPart):
		detail = strings.TrimPrefix(detail, api.ErrPart.Error()+"：")
		if strings.Contains(detail, "没有文字也没有附件") {
			return "这一块是空的"
		}
	}
	return detail
}

// canQuote：只有本地私聊的真实后台能发引用，其余会话的选区条和右键菜单只放"复制"。
// 群聊（本地群聊、QQ 群）的引用改为"引用 ▸ 指定角色 / 所有人"，由另一条线实现；QQ 私聊要等"补充信息只交给角色、不发进 QQ"的后台路径做好。
func (a *app) canQuote() bool {
	if a.demoMode || a.sel == "" {
		return false
	}
	k := a.keyOf(a.sel)
	if k.Platform != "local" || k.Channel != "console" || k.Scene != "private" || a.host.localSendOff() != "" {
		return false
	}
	_, ok := a.reader.(api.LocalInputs)
	return ok
}

// replyToSelection 是选区工具条上的"回复"：在输入区加一张引用卡。keepFocus 为假（按住 Alt）时不抢焦点。
func (a *app) replyToSelection(sel chatui.Selected, keepFocus bool) {
	name := ""
	if i := a.indexOf(sel.MessageID); i >= 0 {
		name = a.msgs[i].who
	}
	thumb := chatui.ThumbKind("")
	if i := a.indexOf(sel.MessageID); i >= 0 {
		if _, kind, ok := quoteThumbOf(msgDisplaySegs(a.msgs[i])); ok {
			thumb = kind
		}
	}
	a.composerOf(a.sel).AddQuote(chatui.Quoted{MessageID: sel.MessageID, Name: name, Text: sel.Text, Start: sel.Start, End: sel.End, Thumb: thumb}, keepFocus)
}

// msgDisplaySegs 是一条已加载消息的展示段（回复引用时用来找缩略图）。
func msgDisplaySegs(m *msg) []api.DisplaySegment {
	out := make([]api.DisplaySegment, len(m.segs))
	for i, s := range m.segs {
		out[i] = s.DisplaySegment
	}
	return out
}

// messageThumb 是输入区引用卡的缩略图：被引的是已加载的消息，取它自己的第一张图。
func (a *app) messageThumb(messageID string, wantPx int) chatui.MediaResult {
	i := a.indexOf(messageID)
	if i < 0 {
		return chatui.MediaResult{State: chatui.MediaMissing}
	}
	th, _, ok := quoteThumbOf(msgDisplaySegs(a.msgs[i]))
	r := a.bindChatMedia(nil, a.msgs[i], i)
	if !ok || r.images == nil {
		return chatui.MediaResult{State: chatui.MediaMissing}
	}
	return r.imageResult(th, float32(max(1, wantPx)))
}

// chatParts 把后台读回的分块转成气泡要画的块：被引的消息后来看不到时只留回复文字，附件只显示名字。
func chatParts(v api.MessageView) []chatui.Part {
	if len(v.Parts) == 0 {
		return nil
	}
	out := make([]chatui.Part, len(v.Parts))
	for i, p := range v.Parts {
		out[i] = chatui.Part{Text: p.Text}
		for _, a := range p.Attachments {
			name := a.Name
			if name == "" {
				name = filepath.Base(a.Path)
			}
			out[i].Atts = append(out[i].Atts, chatui.PartAtt{Name: name, Image: a.Kind == "image"})
		}
		if !p.HasQuote {
			continue
		}
		q := &chatui.PartQuote{Gone: true}
		if p.Ref != nil {
			q.Target = p.Ref.Event.EventID
		}
		if p.Quote.State == api.Ready {
			qv := p.Quote.Value
			q.Gone = false
			q.Name = strings.TrimSpace(qv.DisplayName)
			q.Snippet, q.Original = qv.Snippet, qv.Text
			if q.Snippet == "" {
				q.Snippet = qv.Text
			}
			q.Start, q.End, q.Whole, q.Fallback, q.Truncated = qv.SnippetStart, qv.SnippetEnd, qv.SnippetWhole, qv.SnippetFallback, qv.SnippetTruncated
		}
		out[i].Quote = q
	}
	return out
}

// jumpToQuote 是点引用块：滚回被引的那条，并高亮被引的那一段；选中的文字没对上原文（退回了开头）的不高亮。
func (a *app) jumpToQuote(_ string, q chatui.PartQuote) {
	a.quoteHL = chatui.Highlight{}
	if !q.Fallback && q.Snippet != "" {
		a.quoteHL = chatui.Highlight{MessageID: q.Target, Text: q.Snippet, Original: q.Original, Start: q.Start, End: q.End, Whole: q.Whole}
	}
	a.jumpTo(q.Target)
}

// afterLiveSubmit 处理插话、排队被后台收下以后：序号并进停止的范围（停止一并取消它们）；
// 排队的和“插话没赶上、被后台改成排队”的列进输入框上方；真被插进当前这一轮的就是普通消息，不另列。
// 发的时候这一轮刚好结束了的，直接接上它自己的那一轮。
func (a *app) afterLiveSubmit(ctx context.Context, inputs api.LocalInputs, k api.ConversationKey, gen uint64, st *localRun, rec api.InputReceipt, text, mode string) {
	st.through = max(st.through, rec.Sequence)
	if mode == "steer" && rec.RunID == st.runID && st.busy {
		return // 已经交给正在跑的这一轮
	}
	st.queued = append(st.queued, queuedInput{InputID: rec.InputID, RunID: rec.RunID, Seq: rec.Sequence, Text: clipRunes(text, 60)})
	a.startNextQueued(ctx, inputs, k, gen, st) // 这一轮已经收尾的话直接跟上；还在跑就什么都不做
}
