package turn

import (
	"context"

	"sumika-native/internal/llm"
	"sumika-native/internal/persona"
)

// maxOverflowTries：本地私聊一轮可以恢复几次；其他能恢复的场合（本地群）照旧一次
func maxOverflowTries(cfg Config, in Input) int {
	if workHistoryEnabled(cfg, in) {
		return maxOverflowRecovery
	}
	return 1
}

// recoverOverflow 恢复一次上下文溢出，只重发失败的这一步（T22，不走 fitHard 的裁剪退路）。第一次压本轮之前的历史，压成才换上快照；
// 本轮已经执行的工具、参考信息、插话原样接回，不重跑。本地私聊还会折叠本轮更早的工具步骤：压不了、没压成，或者压完估算还超硬门槛时
// （第二次起前面的轮次不会再长，只折叠）。什么都没省下返回 false，保留原实例，交回服务商的错误。
// tail 里的贡献仍在，所以保留插件账本；代价是摘要掉的更早参考信息在本实例内不再重给（沿用原行为）。
// ContextID 仍换新，让按代次管理的资料在新上下文重新给当前版本。返回恢复之后本轮从哪里开始
func (h *Hub) recoverOverflow(ctx context.Context, l *Live, c persona.Character, in Input, historyEnd, try int) (bool, int, error) {
	l.mu.Lock()
	l.compacting = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.compacting = false
		l.mu.Unlock()
	}()
	compacted := false
	if try == 1 {
		previous := &Live{cfg: l.cfg, b: l.b, in: l.in, msgs: l.msgs[:historyEnd], lastTotalInput: l.lastTotalInput, workState: l.workState, workRecord: l.workRecord, noWorkFold: true}
		r := h.compactOrWait(ctx, previous, "overflow", 0)
		if ctx.Err() != nil {
			h.plugins.logf("会话 %s 上下文超了，压缩的时候被停下了：%s", in.Conversation, r.Err)
			return false, historyEnd, nil
		}
		if r.OK {
			compacted = true
			var err error
			if historyEnd, err = h.swapHistory(l, c, in, historyEnd); err != nil {
				return false, historyEnd, err
			}
		} else {
			h.plugins.logf("会话 %s 上下文超了，压缩没成：%s", in.Conversation, r.Err)
		}
	}
	folded := 0
	if workHistoryEnabled(l.cfg, in) && (!compacted || float64(l.b.Fixed+conversationTokens(l.msgs)) > l.b.Hard) {
		if folded = l.foldTail(historyEnd, try); folded > 0 {
			h.plugins.logf("会话 %s 上下文超了，折叠了这一轮更早的工具步骤，省下约 %d token（第 %d 次）", in.Conversation, folded, try)
		}
	}
	if !compacted && folded == 0 {
		return false, historyEnd, nil
	}
	// 尾部回复带的是压之前的用量：留着的话 requestFor 会以为上下文还是满的，把输出上限压成 1
	for i := historyEnd; i < len(l.msgs); i++ {
		l.msgs[i].Usage = nil
	}
	return true, historyEnd, nil
}

// swapHistory 换上新快照：前面的轮次从库里重建（含旧 root 的工具历史），本轮的尾部原样接回。返回本轮在新实例里从哪里开始
func (h *Hub) swapHistory(l *Live, c persona.Character, in Input, historyEnd int) (int, error) {
	tail, seqs, contexts := l.msgs[historyEnd:], l.turnSeqs, l.turnCtx
	from := l.turnFrom
	relation, clock, todo, state := l.relation, l.lastClockAt, l.todoChecked, l.state
	// 保留原批次的边界：buildWith 会滤掉本轮后续的事件和排队的下一轮；本轮工具、插话只从 tail 接回。
	l.midRun = true
	err := h.rebuildIn(l, c, l.b.Model, in, false, RebuildProviderExceed)
	l.midRun = false
	if err != nil {
		return historyEnd, err
	}
	end := len(l.msgs)
	delta := end - historyEnd
	l.turnFrom, l.turnSeqs, l.turnCtx = from+delta, seqs, contexts
	for i := range l.turnCtx {
		l.turnCtx[i].at += delta
	}
	l.relation, l.lastClockAt, l.todoChecked, l.state = relation, clock, todo, state
	for _, seq := range seqs {
		l.seen[seq] = true
	}
	l.msgs = append(l.msgs, tail...)
	return end, nil
}

// compactingNotice 是通知，不让界面回调的 panic 打断压缩或这一轮；结束通知也照样发。
func (h *Hub) compactingNotice(cfg Config, b *Built, starting bool) {
	if cfg.OnCompacting == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			h.plugins.logf("会话 %s 压缩回调出错：%v", b.Conversation, p)
		}
	}()
	cfg.OnCompacting(b.CharacterID, b.Conversation, starting)
}

// pi-ai dist/utils/overflow.js isContextOverflow 的 Case 2（z.ai）、Case 3（MiMo）；
// Bun agent.ts:881 也调用它认 length 零输出。窗口用当前实例的封顶值，缓存写不计入这两条上游规则。
func resultOverflow(res *llm.Result, window int) bool {
	if window <= 0 {
		return false
	}
	input := res.Usage.Input + res.Usage.CacheRead
	return res.Stop == "stop" && input > int64(window) ||
		res.Stop == "length" && res.Usage.Output == 0 && float64(input) >= float64(window)*0.99
}
