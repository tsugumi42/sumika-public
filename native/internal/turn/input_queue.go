package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"sumika-native/internal/kernel"
	"sumika-native/internal/runstate"
	"sumika-native/internal/store"
)

func (h *Hub) inputClientKey(in Input) string {
	if len(in.Seqs) == 0 {
		return store.NewID(h.Cfg.Now())
	}
	raw, _ := json.Marshal(in.Seqs)
	return string(raw)
}

// enqueueAcceptedLocked 只认已经持久受理的记录；所有输入共用同一个准入与执行器。
func (h *Hub) enqueueAcceptedLocked(in Input, row store.KernelInput) error {
	lc := h.lifecycle
	in.Identity = row.Identity
	if w := reviewFor(in); w != nil {
		w.request.Invocation = row.Identity
	}
	parent := in.requestCtx
	if parent == nil {
		parent = h.base
	}
	// 新工作同步接收父取消，已经派发的工具由同一控制器按宽限升级。
	runCtx, staged := newStagedStop(parent, h.Cfg.Lifecycle.defaults().StopGrace, h.plugins.logf, func() { lc.manager.CancelRun(in.Identity.RunID) })
	cancel := staged.stop
	stop := func() bool { return false }
	if in.requestCtx != nil {
		stop = context.AfterFunc(h.base, cancel)
	}
	_, activity, err := lc.manager.Begin(runCtx, in.Identity)
	if err != nil {
		stop()
		staged.finish()
		return err
	}
	r := &inputRun{input: in, record: row, ctx: runCtx, activity: activity, cancel: cancel, stop: staged, stopCaller: stop, outcome: make(chan Outcome, 1), wakes: map[*RunSubscription]struct{}{}}
	r.snapshot = RunSnapshot{Identity: in.Identity, State: kernel.RunQueued, Revision: 1}
	draft := in.onDraft
	r.input.onDraft = func(v LocalDraft) {
		h.updateDraft(r, v)
		if draft != nil {
			draft(v)
		}
	}
	steps := in.onSteps
	r.input.onProcess = func(v ProcessStep) {
		log := h.updateProcess(r, v)
		if steps != nil && log != nil {
			steps(log.Steps)
		}
	}
	lc.runs[in.Identity.RunID] = r
	lc.pending[row.Domain] = append(lc.pending[row.Domain], r)
	h.markQueued(in)
	h.startInputLocked(row.Domain)
	return nil
}

// claimQueuedInput 在渠道完成上一轮裁决/Commit 后认领晚到插话；不再次受理，也不绕过载荷和原域核对。
func (h *Hub) claimQueuedInput(ctx context.Context, in Input, key string) (<-chan Outcome, bool, error) {
	if h.trace() == nil {
		return nil, true, errors.New("持久输入没有 trace 库")
	}
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.sealed.Load() {
		return nil, true, runstate.ErrSealed
	}
	// 重复提交一句已受理还没排上的话，也是受理：出事的会话在恢复之前不排
	if fe := lc.faultFor(convKey(in.CharacterID, in.Conversation)); fe != nil {
		return nil, true, fe
	}
	row, err := h.trace().KernelInput(ctx, convKey(in.CharacterID, in.Conversation), key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if row.State != kernel.InputAccepted || row.Mode != kernel.InputQueue {
		return nil, false, nil
	}
	if lc.runs[row.Identity.RunID] != nil {
		return nil, false, nil
	}
	var stored Input
	if err = json.Unmarshal([]byte(row.Payload), &stored); err != nil {
		return nil, true, err
	}
	if stored.Identity.Scope != in.Identity.Scope || stored.Text != in.Text || !slices.Equal(stored.Seqs, in.Seqs) {
		return nil, true, store.ErrInputConflict
	}
	stored.requestCtx, stored.onDraft, stored.onSteps = in.requestCtx, in.onDraft, in.onSteps
	if err = h.enqueueAcceptedLocked(stored, row); err != nil {
		return nil, true, err
	}
	return lc.runs[row.Identity.RunID].outcome, true, nil
}
