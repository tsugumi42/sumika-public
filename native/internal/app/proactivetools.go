package app

// 话题线里的工具（规格-主动联系 第八、九、十四节）。声明在 proactive.Decls，这里管谁能看见、怎么执行：
//   - owner_presence、thread_send、stay_quiet、end_thread、hand_back 只在话题线里可见（audience.IsThread 且 PrivateOK 为假）；
//   - proactive_snooze 另外在主人的本地私聊里可见；
//   - 执行时再核一遍：会话串、预先绑定的 system 身份、线没关。线的工具清单里没有执行工具，改坏检查靠 PrivateOK。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/proactive"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

type proactiveTools struct{ h *proactiveHost }

func (proactiveTools) ID() string { return "proactive-thread-tools" }

func (p proactiveTools) Setup(r *turn.Registry) (func(), error) {
	var undos []func()
	stop := func() {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
	}
	for _, name := range proactive.ToolNames {
		t := turn.Tool{Decl: proactive.Decls[name], For: p.visible(name), Exec: p.exec(name)}
		undo, err := r.OnTool(t)
		if err != nil {
			stop()
			return nil, err
		}
		undos = append(undos, undo)
	}
	return stop, nil
}

// inThread 是线的听众：本地、线的会话串、PrivateOK 和 OwnerAsked 都为假。提醒专用会话形状是线，但不是主动联系的线：线的工具、提醒工具对它都不可见
func inThread(a audience.Audience) bool {
	return a.Channel == "console" && audience.IsThread(a.Conversation) && !audience.IsReminderRun(a.Conversation) && !a.PrivateOK && !a.OwnerAsked
}

// ownerLocalPrivateAudience 是主人在本地私聊主会话里的听众（不是线、不是自检话题）
func ownerLocalPrivateAudience(a audience.Audience) bool {
	return a.Channel == "console" && a.PrivateOK && a.OwnerAsked && a.Scene != "group" && localPrivateConv(a.Conversation) &&
		!audience.IsThread(a.Conversation) && !audience.IsSelftest(a.Conversation)
}

func (p proactiveTools) visible(name string) func(audience.Audience) bool {
	return func(a audience.Audience) bool {
		if inThread(a) {
			return p.h.enabledFor(characterOfConv(a.Conversation))
		}
		if name == proactive.ToolProactiveSnooze {
			return ownerLocalPrivateAudience(a) && p.h.enabledFor(characterOfConv(a.Conversation))
		}
		return false
	}
}

func (p proactiveTools) exec(name string) func(context.Context, turn.ToolCall) (tools.Result, error) {
	return func(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
		fail := func(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }
		s := c.Identity.Scope
		if c.Conversation != s.Conversation || s.CharacterID != c.CharacterID {
			return fail("当前身份不能用这个工具")
		}
		if name == proactive.ToolProactiveSnooze && ownerLocalPrivate(s) && ownerLocalPrivateAudience(c.Audience) && p.h.enabledFor(c.CharacterID) {
			return p.h.toolSnooze(c)
		}
		// 其余只给线：宿主预先绑定的 system 身份、线的会话串、线的听众，三样都对上
		if !inThread(c.Audience) || !audience.IsThread(c.Conversation) || s.ActorKind != "system" || s.ActorID != audience.ThreadActor || !p.h.enabledFor(c.CharacterID) {
			return fail("当前身份不能用这个工具")
		}
		th, err := p.h.mgr.Get(audience.ThreadID(c.Conversation))
		if err != nil || th.Conversation != c.Conversation || th.Character != c.CharacterID {
			return fail("找不到这条线")
		}
		if !th.Open() {
			return fail("thread_closed：这条线已经关了，不能再用它。")
		}
		switch name {
		case proactive.ToolOwnerPresence:
			return p.h.toolPresence(th)
		case proactive.ToolThreadSend:
			return p.h.toolSend(th, c.Args)
		case proactive.ToolStayQuiet:
			return p.h.toolQuiet(th, c.Args)
		case proactive.ToolEndThread:
			return p.h.toolEnd(th, c.Args)
		case proactive.ToolHandBack:
			return p.h.toolHandBack(th)
		case proactive.ToolProactiveSnooze:
			return p.h.toolSnooze(c)
		}
		return fail("不认识这个工具")
	}
}

func strArg(args jsjson.Value, key string) string {
	if args.Kind != jsjson.Object {
		return ""
	}
	if v, ok := args.Obj[key]; ok && v.Kind == jsjson.String {
		return v.Str
	}
	return ""
}

func numArg(args jsjson.Value, key string) (float64, bool) {
	if args.Kind != jsjson.Object {
		return 0, false
	}
	if v, ok := args.Obj[key]; ok && v.Kind == jsjson.Number {
		return v.Num, true
	}
	return 0, false
}

func agoZh(t, now time.Time) string {
	if t.IsZero() {
		return "很久没有（或这一天里没有）"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d/time.Minute))
	}
	return fmt.Sprintf("%d 小时 %d 分钟前", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// toolPresence 返回可去的地方。第 2 步只有主人的本地私聊一项；句柄 t1 只在本线内有效。
// 只写数字和叫法，不返回消息内容和别人是谁
func (h *proactiveHost) toolPresence(th proactive.Thread) (tools.Result, error) {
	now := h.a.now()
	conv := localConvOf(th.Character)
	st, err := h.a.set.ForConversation(conv)
	if err != nil {
		return tools.Result{Text: "读不到本地私聊的情况：" + err.Error(), IsError: true}, nil
	}
	since := now.Add(-time.Duration(h.cfg().PresenceWindowHours) * time.Hour).UnixMilli()
	var ownerAt, anyAt time.Time
	if at, ok, err := st.OwnerLastSpokeIn(conv, since); err == nil && ok {
		ownerAt = time.UnixMilli(at)
	}
	if at, ok, err := st.LastMessageAt(conv); err == nil && ok {
		anyAt = time.UnixMilli(at)
	}
	fg := false
	if f := h.a.opt.WindowForeground; f != nil {
		fg = f()
	}
	h.mu.Lock()
	h.handles[th.ID] = map[string]string{"t1": conv}
	h.mu.Unlock()
	var b strings.Builder
	b.WriteString("可以去的地方（句柄只在这条线里有效）：\n")
	fmt.Fprintf(&b, "t1 本地私聊（所有者的主窗口）：他最后一次在这里说话是 %s；这里最近一条消息是 %s；发得出去（直接出现在窗口里）。\n", agoZh(ownerAt, now), agoZh(anyAt, now))
	if fg {
		b.WriteString("主窗口现在在前台，他很可能正在看。")
	} else {
		b.WriteString("主窗口现在不在前台，发出去会弹一个系统通知。")
	}
	return tools.Result{Text: b.String()}, nil
}

// toolSend 是 thread_send（第十四节形状 × 入口表）：句柄、文字、线状态、发送条数、send 档位都在这里核；
// 先写发送记录（pending），再写库，再按结果改记录；本地目标写库成功就是终态。
// 返回的第一个词是五类结果之一：sent、written_not_sent（第 2 步没有）、thought_only、refused、failed，另有 invalid_target、thread_closed
func (h *proactiveHost) toolSend(th proactive.Thread, args jsjson.Value) (tools.Result, error) {
	refuse := func(code, why string) (tools.Result, error) {
		return tools.Result{Text: code + "：" + why, IsError: true}, nil
	}
	target := strings.TrimSpace(strArg(args, "target"))
	h.mu.Lock()
	conv, ok := h.handles[th.ID][target]
	h.mu.Unlock()
	if target == "" || !ok {
		return refuse("invalid_target", "这个句柄不是这次 owner_presence 给的（或已经过期）。先调 owner_presence 看可以去哪里。")
	}
	// 句柄背后的会话再核一遍：只能是这个角色自己的本地私聊主会话，不是线、不是话题
	if conv != localConvOf(th.Character) {
		return refuse("invalid_target", "这个句柄指向的地方不能发。")
	}
	text := strings.TrimSpace(strArg(args, "text"))
	if text == "" {
		return refuse("refused", "text 是空的，没有可发的话。")
	}
	if utf8.RuneCountInString(text) > threadTextMax {
		h.noteRefusal(th.ID, reasonSendBlockedGate)
		return refuse("refused", fmt.Sprintf("这句话太长了（超过 %d 个字），像平时发消息那样短一点再发。", threadTextMax))
	}
	reason := strArg(args, "reason")
	label := "本地私聊"
	if h.cfg().Send == "think" {
		// think 档：不写目标会话、不 MarkStale、不发，只记下"本想对谁说什么"，线按没开口处理
		s, err := h.mgr.BeginSend(th.ID, conv, label, "本地私聊", text)
		if err == nil {
			_, err = h.mgr.FinishSend(s.ID, proactive.SendThought, 0, "")
		}
		if err != nil {
			return h.sendError(th, err)
		}
		h.mu.Lock()
		h.thought[th.ID] = true
		h.quiet[th.ID] = "本想对" + label + "说：" + proactive.ClipNote(text, 40)
		h.mu.Unlock()
		return tools.Result{Text: "thought_only：现在是只想不说的观察档，这句没有发出去，也不会有人回复。这次就当没开口。"}, nil
	}
	first := th.Sends == 0
	s, err := h.mgr.BeginSend(th.ID, conv, label, "本地私聊", text)
	if err != nil {
		return h.sendError(th, err)
	}
	seq, eventID, err := h.deliverLocal(th, conv, text, reason)
	if err != nil {
		_, _ = h.mgr.FinishSend(s.ID, proactive.SendFailed, 0, "")
		h.noteRefusal(th.ID, reasonSendFailed)
		h.a.logf("主动联系：线 %s 写进本地私聊失败：%v", th.ID, err)
		return tools.Result{Text: "failed：这句没有发出去（写进主窗口时出错）。不算开口，可以稍后再试。", IsError: true}, nil
	}
	if _, err := h.mgr.FinishSend(s.ID, proactive.SendSent, seq, eventID); err != nil {
		h.a.logf("主动联系：线 %s 的发送记录没改成已发：%v", th.ID, err)
	}
	if first {
		_ = h.mgr.SetOpenedReason(th.ID, reason)
	}
	// 线发到被认领的那个会话，就算回了那条认领；只算这一轮起跑以前认领的：
	// 这一轮跑着时主人又来的话，转述排在后面那一轮里，这一轮没看到，不能替它记成已回
	cutoff := h.a.now()
	if started, ok := h.currentRunStart(th.ID); ok {
		cutoff = started
	}
	if claims, err := h.mgr.ClaimsOf(th.ID, proactive.ClaimPending); err == nil {
		for _, c := range claims {
			if c.Conversation == conv && !c.At.After(cutoff) {
				_, _ = h.mgr.SetClaim(c.Conversation, c.Seq, proactive.ClaimReplied)
			}
		}
	}
	h.mu.Lock()
	h.spoke[th.ID]++
	h.mu.Unlock()
	h.reschedule(th.ID)
	return tools.Result{Text: "sent：已经发到他的本地私聊了。他回复的话我会转述进这条线。"}, nil
}

func (h *proactiveHost) noteRefusal(id, code string) {
	h.mu.Lock()
	h.refused[id] = code
	h.mu.Unlock()
}

// sendError 把 BeginSend、FinishSend 的错误翻成给模型的话：发送条数用完（并收线）、线已经关了、别的错
func (h *proactiveHost) sendError(th proactive.Thread, err error) (tools.Result, error) {
	switch {
	case errors.Is(err, proactive.ErrCap):
		h.noteRefusal(th.ID, reasonSendBlockedCap)
		h.closeVia(th.ID, proactive.Event{Kind: proactive.EvCap, Conclusion: "每条线最多发 " + fmt.Sprint(h.cfg().MaxSendsPerThread) + " 条，用完了"})
		return tools.Result{Text: fmt.Sprintf("refused：这条线最多发 %d 条，已经用完了（cap）。这条线现在关掉。", h.cfg().MaxSendsPerThread), IsError: true}, nil
	case errors.Is(err, proactive.ErrClosed):
		return tools.Result{Text: "thread_closed：这条线已经关了，不能再发。", IsError: true}, nil
	}
	h.a.logf("主动联系：线 %s 发送出错：%v", th.ID, err)
	return tools.Result{Text: "failed：这句没有发出去（内部出错）。不算开口。", IsError: true}, nil
}

func (h *proactiveHost) toolQuiet(th proactive.Thread, args jsjson.Value) (tools.Result, error) {
	note := strings.TrimSpace(strArg(args, "reason"))
	if note == "" {
		note = "没写理由"
	}
	h.mu.Lock()
	h.quiet[th.ID] = note
	h.mu.Unlock()
	return tools.Result{Text: "好，这次不找他。"}, nil
}

func (h *proactiveHost) toolEnd(th proactive.Thread, args jsjson.Value) (tools.Result, error) {
	conclusion := strings.TrimSpace(strArg(args, "conclusion"))
	if _, closed := h.closeVia(th.ID, proactive.Event{Kind: proactive.EvEnd, Conclusion: conclusion}); !closed {
		return tools.Result{Text: "thread_closed：这条线已经关了。", IsError: true}, nil
	}
	return tools.Result{Text: "这条线已经收了。"}, nil
}

func (h *proactiveHost) toolHandBack(th proactive.Thread) (tools.Result, error) {
	// 模型主动交回：这条线所有还在等的认领都交，并且本线内不再认领那个会话
	if n := h.handBackPending(th, true, time.Time{}); n == 0 {
		return tools.Result{Text: "现在没有被这条线认领、还在等你的所有者消息，不用交回。", IsError: true}, nil
	}
	return tools.Result{Text: "已经交回给平时的对话去回，这条线里不会再接那个地方的话。"}, nil
}

func (h *proactiveHost) toolSnooze(c turn.ToolCall) (tools.Result, error) {
	hours, _ := numArg(c.Args, "hours")
	until, err := h.snooze(c.CharacterID, hours, strArg(c.Args, "reason"))
	if err != nil {
		return tools.Result{Text: "没有设上：" + err.Error(), IsError: true}, nil
	}
	return tools.Result{Text: "好，到 " + until.Format("01-02 15:04") + " 之前不会再主动去找他，开着的线也收了。"}, nil
}
