package main

// sumika-ctl 在 bun 模式下的自检对话、目标和定期任务、悄悄话（规格-本地私聊切到Bun 第十一节）。
// backend.local 是 bun 时本地私聊由 Bun 写，Go 的 DesktopReader.SelftestChat、SlashCommand、SelftestWhisper 等一律回
// ErrLocalOwnedByBun；这里把同样的命令转给 Bun 的 selftest.* 方法，执行体在 Bun（server/src/local/selftest.ts），Go 只做翻译：
//   - 走宿主的 bunLink（和界面桥同一条连接、同一个“Bun 不在”判断）；Bun 不在回“本地后台没连上”，绝不退回 Go 去写本地；
//   - 闸门和 go 模式一致：演示模式、没有可写后台由调用方先挡；只看历史阶段回和 go 模式同一句 readonly；
//   - 空的角色由 Go 换成具体的角色 id（Bun 不知道主窗口选了谁）；角色不存在回 invalid“没有这个角色”；
//   - 结果翻成后台的 SelftestResult、SlashCommandResult、WhisperOutcome，再走 go 模式同一份 chatReply、slashReply、whisperReply，
//     所以 ctl 看到的字段、状态和错误码两种模式一样；
//   - go 模式（默认）不经过这个文件的任何转发：bunMode 回 false，上面三个函数照旧直调 DesktopReader。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/whisper"
)

// bunMode 说本地私聊是不是交给了 Bun，是的话交出到 Bun 的连接和宿主此刻是不是只看历史阶段。
// 调用方已经过了 checkpointReader（演示 → demo，没有可写后台 → readonly）。
func (b *ctlBackend) bunMode() (link *bunLink, archive bool, on bool) {
	h, err := b.host()
	if err != nil {
		return nil, false, false
	}
	h.op.Lock()
	defer h.op.Unlock()
	if !h.localByBun {
		return nil, false, false
	}
	return h.bunLink, h.mode == backend.Archive, true
}

// bunClient 取到 Bun 的客户端并核对它有这个方法。Bun 不在、连接断了、Bun 是只读（方法没登记）都回明确的 failed
func bunClient(link *bunLink, method string) (*clientCall, error) {
	if link == nil {
		return nil, cerr(ctlproto.CodeFailed, "本地后台没连上：宿主没有 Bun 的连接")
	}
	f, err := link.get()
	if err != nil {
		reason := strings.TrimPrefix(err.Error(), api.ErrLocalBackendDown.Error())
		reason = strings.TrimPrefix(reason, "：")
		if reason == "" {
			return nil, cerr(ctlproto.CodeFailed, "本地后台没连上")
		}
		return nil, cerr(ctlproto.CodeFailed, "本地后台没连上："+reason)
	}
	if !f.C.Has(method) {
		return nil, cerr(ctlproto.CodeFailed, "Bun 后台现在是只读，不能自检（原因见 sumika-ctl status）")
	}
	return &clientCall{call: func(ctx context.Context, params, out any) error { return f.C.Call(ctx, method, params, out) }}, nil
}

type clientCall struct {
	call func(ctx context.Context, params, out any) error
}

// cerr 是不带格式的 ctlproto.Errorf（文字里可能有 %，不能当格式串）
func cerr(code, msg string) error { return ctlproto.Errorf(code, msg) }

// newChatWire 把 *bool 换成协议里的三态字符串：空 = 没写（add 用默认、edit 不改），new = 每次新聊天，same = 同一个聊天
func newChatWire(v *bool) string {
	switch {
	case v == nil:
		return ""
	case *v:
		return "new"
	}
	return "same"
}

// bunErr 把 Bun 回的错翻成控制接口的错：自检的两个码按各自的意思，其余带原文；连接断了说连接断了。
// 错误文字过 b.redact（和 go 模式里 RedactLog 过的错一样）：Bun 的模型报错可能带密钥或号码片段
func (b *ctlBackend) bunErr(err error) error {
	if err == nil {
		return nil
	}
	var we *rpcwire.Error
	if errors.As(err, &we) {
		switch we.Code {
		case rpcwire.CodeSelftestCharacter:
			return backend.ErrSelftestCharacter
		case rpcwire.CodeSelftestInvalid:
			return cerr(ctlproto.CodeInvalid, b.redact(we.Message))
		case rpcwire.CodeLocalChatBusy:
			return cerr(ctlproto.CodeFailed, b.redact(we.Message))
		}
		return cerr(ctlproto.CodeFailed, b.redact(we.Message))
	}
	return cerr(ctlproto.CodeFailed, b.redact(fmt.Sprintf("和 Bun 后台的连接出了问题：%v", err)))
}

// timeoutMS 把时限换成毫秒，向上取整：不足 1 毫秒的正值也是 1（ctlserver 放行大于 0 的时限，换成 0 毫秒 Bun 会回 invalid，go 模式却接受）
func timeoutMS(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

func (b *ctlBackend) redact(s string) string {
	h, err := b.host()
	if err != nil {
		return s
	}
	return h.redactor.Redact(s)
}

// ───── chat ─────

func (b *ctlBackend) chatViaBun(ctx context.Context, r *backend.DesktopReader, link *bunLink, archive bool, req ctlserver.ChatRequest) (ctlproto.ChatResult, error) {
	if archive {
		return chatReply(backend.SelftestResult{}, api.ErrLocalChatUnsupported)
	}
	char, err := r.SelftestCharacter(req.Character)
	if err != nil {
		return chatReply(backend.SelftestResult{}, err)
	}
	cl, err := bunClient(link, rpcwire.MethodSelftestChat)
	if err != nil {
		return ctlproto.ChatResult{}, err
	}
	var w rpcwire.SelftestChatResult
	if err := cl.call(ctx, rpcwire.SelftestChatParams{Character: char, New: req.New, Text: req.Text, TimeoutMS: timeoutMS(req.Timeout)}, &w); err != nil {
		return chatReply(backend.SelftestResult{}, b.bunErr(err))
	}
	res := backend.SelftestResult{Status: w.Status, Character: w.Character, Topic: w.Topic, Conversation: w.Conversation, TurnID: w.TurnID,
		Reply: w.Reply, Draft: w.Draft, Model: w.Model, Route: w.Route, ElapsedMS: w.ElapsedMS, Error: b.redact(w.Error)}
	for _, t := range w.Tools {
		res.Tools = append(res.Tools, backend.SelftestTool{Name: t.Name, Args: b.redact(t.Args), Result: b.redact(t.Result), Status: t.Status, Error: t.Error})
	}
	if a := w.Approval; a != nil {
		res.Approval = &backend.SelftestApproval{ID: a.ID, Title: a.Title, Tool: a.Tool, Subject: a.Subject, Why: a.Why, Purpose: a.Purpose, Preview: b.redact(a.Preview), Notes: a.Notes}
	}
	return chatReply(res, nil)
}

// ───── goal、loop、trust ─────

func (b *ctlBackend) slashViaBun(ctx context.Context, r *backend.DesktopReader, link *bunLink, archive bool, req ctlserver.SlashRequest) (ctlproto.SlashResult, error) {
	if archive {
		return slashReply(backend.SlashCommandResult{}, api.ErrLocalChatUnsupported)
	}
	char, err := r.SelftestCharacter(req.Character)
	if err != nil {
		return slashReply(backend.SlashCommandResult{}, err)
	}
	cl, err := bunClient(link, rpcwire.MethodSelftestSlash)
	if err != nil {
		return ctlproto.SlashResult{}, err
	}
	p := rpcwire.SelftestSlashParams{Command: req.Command, Sub: req.Sub, Character: char, Text: req.Text, ID: req.ID}
	if l := req.Loop; l != nil {
		p.Loop = &rpcwire.SelftestLoopPatch{Kind: l.Kind, IntervalSec: l.IntervalSec, At: l.At, Days: l.Days, NewChat: newChatWire(l.NewChat), Notify: l.Notify}
	}
	var w rpcwire.SelftestSlashResult
	if err := cl.call(ctx, p, &w); err != nil {
		berr := b.bunErr(err)
		var we *rpcwire.Error
		if errors.As(err, &we) && we.Code == rpcwire.CodeSelftestInvalid {
			berr = fmt.Errorf("%w：%s", backend.ErrSlashInvalid, b.redact(we.Message)) // 和 go 模式一样回 invalid
		}
		return slashReply(backend.SlashCommandResult{}, berr)
	}
	res := backend.SlashCommandResult{Character: w.Character, Topic: w.Topic, Conversation: w.Conversation, Reply: w.Reply, Started: w.Started}
	if g := w.Goal; g != nil {
		res.Goal = &backend.GoalView{Text: g.Text, Status: g.Status, PauseReason: g.PauseReason, LastJudge: g.LastJudge, Line: g.Line, Rounds: g.Rounds, MaxRounds: g.MaxRounds, TokensUsed: g.TokensUsed}
	}
	for _, t := range w.Loops {
		v := backend.LoopView{ID: t.ID, Status: t.Status, Prompt: t.Prompt, IntervalSec: t.IntervalSec, Runs: t.Runs, Line: t.Line, Kind: t.Kind, At: t.At, Days: t.Days, Notify: t.Notify, NewChat: t.NewChat}
		if t.NextRun != "" {
			if at, err := time.Parse(time.RFC3339, t.NextRun); err == nil {
				v.NextRun = at
			}
		}
		res.Loops = append(res.Loops, v)
	}
	return slashReply(res, nil)
}

// ───── whisper ─────

func (b *ctlBackend) whisperCreateViaBun(ctx context.Context, link *bunLink, archive bool, req ctlserver.WhisperCreateRequest) (ctlproto.WhisperResult, error) {
	if archive {
		return whisperReply(backend.WhisperOutcome{}, api.ErrLocalChatUnsupported, true)
	}
	cl, err := bunClient(link, rpcwire.MethodSelftestWhisper)
	if err != nil {
		return ctlproto.WhisperResult{}, err
	}
	var w rpcwire.SelftestWhisperResult
	p := rpcwire.SelftestWhisperParams{Characters: req.Characters, SourceText: req.SourceText, QuoteName: req.QuoteName, Say: req.Say, TimeoutMS: timeoutMS(req.Timeout)}
	if err := cl.call(ctx, p, &w); err != nil {
		return whisperReply(backend.WhisperOutcome{}, b.whisperBunErr(err), true)
	}
	return whisperReply(whisperOutcomeOf(w), nil, true)
}

func (b *ctlBackend) whisperBatchViaBun(ctx context.Context, link *bunLink, archive bool, method, batch string) (ctlproto.WhisperResult, error) {
	if archive {
		return whisperReply(backend.WhisperOutcome{}, api.ErrLocalChatUnsupported, false)
	}
	cl, err := bunClient(link, method)
	if err != nil {
		return ctlproto.WhisperResult{}, err
	}
	var w rpcwire.SelftestWhisperResult
	if err := cl.call(ctx, rpcwire.SelftestWhisperBatchParams{Batch: batch}, &w); err != nil {
		return whisperReply(backend.WhisperOutcome{}, b.whisperBunErr(err), false)
	}
	return whisperReply(whisperOutcomeOf(w), nil, false)
}

// whisperBunErr：批号没见过按 go 模式的 ErrWhisperBatch（invalid）回，别的照 bunErr
func (b *ctlBackend) whisperBunErr(err error) error {
	var we *rpcwire.Error
	if errors.As(err, &we) && we.Code == rpcwire.CodeSelftestInvalid {
		return fmt.Errorf("%w：%s", backend.ErrWhisperBatch, b.redact(we.Message))
	}
	return b.bunErr(err)
}

func whisperOutcomeOf(w rpcwire.SelftestWhisperResult) backend.WhisperOutcome {
	o := backend.WhisperOutcome{Batch: w.Batch, Notes: map[string]string{}, Pending: w.Pending, Elapsed: time.Duration(w.ElapsedMS) * time.Millisecond}
	for _, e := range w.Entries {
		o.Entries = append(o.Entries, whisper.Entry{Batch: w.Batch, Character: e.Character, State: e.State, Position: e.Position, SourceTurnRunning: e.SourceTurnRunning, Detail: e.Detail})
		if e.Note != "" {
			o.Notes[e.Character] = e.Note
		}
	}
	return o
}

// ───── bun 模式下还不能用的几条：回明确的话，不碰 Go 的库、不退回 Go 去写 ─────

// bunNotYet 把 Go 后台在 bun 模式下回的“本地私聊现在由 Bun 管”换成一句说清楚的话（whisper log、whisper list、checkpoint）
func bunNotYet(err error, what string) error {
	if errors.Is(err, api.ErrLocalOwnedByBun) {
		return cerr(ctlproto.CodeFailed, "Bun 模式下 sumika-ctl 还不能"+what+"（本地私聊现在由 Bun 管，这部分 Bun 还没做）")
	}
	return err
}
