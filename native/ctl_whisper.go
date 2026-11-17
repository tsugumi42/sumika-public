package main

// 悄悄话（sumika-ctl whisper，规格-群聊引用和路由 第十一节）接进桌面程序：走和界面同一个悄悄话服务（internal/whisper），
// 来源只有命令行给的文字、挂在自检话题上；控制接口收不到任何真实会话的 id。

import (
	"context"
	"errors"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/store"
	"sumika-native/internal/whisper"
	"sumika-native/internal/whisperlog"
)

var _ ctlserver.WhisperBackend = (*ctlBackend)(nil)
var _ ctlserver.WhisperLogBackend = (*ctlBackend)(nil)

func (b *ctlBackend) WhisperCreate(ctx context.Context, req ctlserver.WhisperCreateRequest) (ctlproto.WhisperResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.WhisperResult{}, err
	}
	if link, archive, on := b.bunMode(); on { // 本地私聊交给了 Bun：绝不调 Go 的 SelftestWhisper
		return b.whisperCreateViaBun(ctx, link, archive, req)
	}
	out, err := r.SelftestWhisper(ctx, backend.SelftestWhisperRequest{Characters: req.Characters, SourceText: req.SourceText, QuoteName: req.QuoteName, Say: req.Say, Timeout: req.Timeout})
	return whisperReply(out, err, true)
}

func (b *ctlBackend) WhisperStatus(ctx context.Context, batch string) (ctlproto.WhisperResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.WhisperResult{}, err
	}
	if link, archive, on := b.bunMode(); on { // 批在 Bun 里，Go 这边的 a.whisper 没装
		return b.whisperBatchViaBun(ctx, link, archive, rpcwire.MethodSelftestWhisperStatus, batch)
	}
	out, err := r.WhisperStatus(ctx, batch)
	return whisperReply(out, err, false)
}

func (b *ctlBackend) WhisperCancel(ctx context.Context, batch string) (ctlproto.WhisperResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.WhisperResult{}, err
	}
	if link, archive, on := b.bunMode(); on {
		return b.whisperBatchViaBun(ctx, link, archive, rpcwire.MethodSelftestWhisperCancel, batch)
	}
	out, err := r.WhisperCancel(ctx, batch)
	return whisperReply(out, err, false)
}

// whisperReply 把后台的结果翻成协议的结果；create 要等结局，到时限还没落定回 timeout，有失败的回 failed，data 都带着。
func whisperReply(o backend.WhisperOutcome, err error, create bool) (ctlproto.WhisperResult, error) {
	switch {
	case errors.Is(err, api.ErrLocalChatUnsupported):
		return ctlproto.WhisperResult{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式不能发悄悄话，请先开始接收")
	case errors.Is(err, backend.ErrSelftestCharacter):
		return ctlproto.WhisperResult{}, ctlproto.Errorf(ctlproto.CodeInvalid, "没有这个角色")
	case errors.Is(err, backend.ErrWhisperBatch):
		return ctlproto.WhisperResult{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	case err != nil:
		return ctlproto.WhisperResult{}, err
	}
	res := ctlproto.WhisperResult{Batch: o.Batch, ElapsedMS: o.Elapsed.Milliseconds(), Entries: []ctlproto.WhisperEntry{}}
	failed, pending := false, o.Pending
	for _, e := range o.Entries {
		res.Entries = append(res.Entries, ctlproto.WhisperEntry{Character: e.Character, State: e.State, Position: e.Position, SourceTurnRunning: e.SourceTurnRunning, Detail: e.Detail, Note: o.Notes[e.Character]})
		if e.State != whisper.StateDone {
			failed = failed || whisper.Terminal(e.State)
		}
	}
	switch {
	case pending:
		res.Status = ctlproto.WhisperPending
	case failed:
		res.Status = ctlproto.WhisperFailed
	default:
		res.Status = ctlproto.WhisperDone
	}
	if create && res.Status == ctlproto.WhisperPending {
		return res, &ctlproto.Error{Code: ctlproto.CodeTimeout, Msg: "到了时限，还有没处理完的（可以用 whisper status 再看，或 whisper cancel 取消）", Data: res}
	}
	if create && res.Status == ctlproto.WhisperFailed {
		return res, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: "有角色没处理成功，见各角色的状态", Data: res}
	}
	return res, nil
}

// WhisperLog 读对话日志的同一份投影（界面看到的那份）；后端只认自检来源。
func (b *ctlBackend) WhisperLog(ctx context.Context, args ctlproto.WhisperLogArgs) (ctlproto.WhisperLogResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	out, err := r.WhisperLog(ctx, args)
	return out, logErr(err)
}

// WhisperList 列悄悄话的批号和状态，不带正文。
func (b *ctlBackend) WhisperList(ctx context.Context, selftest bool) (ctlproto.WhisperLogResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.WhisperLogResult{}, err
	}
	out, err := r.WhisperList(ctx, selftest)
	return out, logErr(err)
}

func logErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, api.ErrLocalOwnedByBun):
		return bunNotYet(err, "看悄悄话的对话日志，请在界面里看")
	case errors.Is(err, api.ErrLocalChatUnsupported):
		return ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式没有对话日志，请先开始接收")
	case errors.Is(err, backend.ErrWhisperBatch):
		return ctlproto.Errorf(ctlproto.CodeInvalid, "没有这个自检会话或批号的日志（真实会话的日志读不到）")
	case errors.Is(err, whisperlog.ErrNoEntry):
		return ctlproto.Errorf(ctlproto.CodeInvalid, "没有这条日志")
	case errors.Is(err, whisperlog.ErrBadRequest), errors.Is(err, store.ErrLogCursor):
		return ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	return err
}
