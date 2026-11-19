package app

import (
	"context"
	"errors"
	"sort"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func (r *DesktopReader) localInputScope(ctx context.Context, k api.ConversationKey) (kernel.Scope, error) {
	character, err := r.localCharacter(ctx, k)
	if err != nil {
		return kernel.Scope{}, err
	}
	inv, err := r.desk.app.bindKernelInput(ctx, kernel.Invocation{Scope: kernel.Scope{CharacterID: character, Conversation: k.Conversation, ScopeID: turn.ConsoleScope, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}, Kind: kernel.KindForeground})
	return inv.Scope, err
}
func (r *DesktopReader) SubmitLocal(ctx context.Context, k api.ConversationKey, s api.LocalInputSubmission) (api.InputReceipt, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.InputReceipt{}, err
	}
	defer done()
	if s.Text != "" && len(s.Parts) > 0 {
		return api.InputReceipt{}, errors.New("Text 和 Parts 只能给一个")
	}
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		return api.InputReceipt{}, err
	}
	// 命令不论 start、steer、queue 都不发给她；只有整条消息是一个普通文字块（没有引用、没有附件）才当命令
	if cmd, plain := plainText(s.Text, s.Parts); plain {
		send, handled, noticeDone, err := r.desk.app.localSlash(ctx, k, scope.CharacterID, cmd)
		if handled || err != nil {
			return api.InputReceipt{State: "command"}, err
		}
		defer noticeDone()
		if len(s.Parts) > 0 {
			s.Parts = []api.LocalPart{{Text: send}}
		} else {
			s.Text = send
		}
	}
	// 目录用不了就在落库前原样退回：草稿不丢，这句话也不进库
	if err := r.desk.app.precheckWorkspace(ctx, k.Conversation); err != nil {
		return api.InputReceipt{}, err
	}
	in0 := s.Parts
	if len(in0) == 0 {
		in0 = []api.LocalPart{{Text: s.Text}}
	}
	parts, text, err := r.resolveLocalParts(ctx, k, in0)
	if err != nil {
		return api.InputReceipt{}, err
	}
	in := turn.Input{Identity: kernel.Invocation{Scope: scope, Kind: kernel.KindForeground}, CharacterID: scope.CharacterID, Conversation: k.Conversation, Scope: scope.ScopeID, Text: text, Audience: audience.Console(k.Conversation), Addressed: true, Parts: parts}
	mode, expected := kernel.InputMode(s.Mode), s.ExpectedRunID
	auto := mode == kernel.InputStart && r.desk.app.auto.has(k.Conversation)
	// 有问题在等回答：这句话就是回答，插进这一轮（规格-向主人提问 第四节）
	asking := mode == kernel.InputStart && !auto && r.desk.app.asks.Waiting(k.Conversation)
	if auto {
		// 宿主自动跑的轮（目标续轮、定期任务）在进行：主人的话不挡回去。
		// 那一轮还在做事就当插话塞进去；正在两轮之间（裁判在判断）或已经在收尾，就排队，下一轮最先处理。
		if run := r.desk.app.hub.ActiveRunID(scope.CharacterID, k.Conversation); run != "" {
			mode, expected = kernel.InputSteer, run
		} else {
			mode = kernel.InputQueue
		}
	} else if asking {
		if run := r.desk.app.hub.ActiveRunID(scope.CharacterID, k.Conversation); run != "" {
			mode, expected = kernel.InputSteer, run
		}
	}
	rec, err := r.desk.app.hub.Submit(ctx, turn.Submission{Input: in, Mode: mode, ClientKey: s.ClientKey, ExpectedRunID: expected})
	if mode == kernel.InputSteer && errors.Is(err, store.ErrInputConflict) {
		switch {
		case auto: // 认到的那一轮刚好结束了：改成排队
			rec, err = r.desk.app.hub.Submit(ctx, turn.Submission{Input: in, Mode: kernel.InputQueue, ClientKey: s.ClientKey})
		case asking: // 同样刚好结束了：就是普通的一句
			mode = kernel.InputStart
			rec, err = r.desk.app.hub.Submit(ctx, turn.Submission{Input: in, Mode: mode, ClientKey: s.ClientKey})
		}
	}
	if err == nil && mode == kernel.InputSteer {
		// 插话要等这批工具做完才送到，问题还等着就互相等到超时：不管这句从哪来，都结束等待
		r.desk.app.asks.Interrupt(k.Conversation)
	}
	if errors.Is(err, turn.ErrInputBusy) {
		err = api.ErrLocalChatBusy
	}
	return api.InputReceipt{InputID: rec.InputID, RunID: rec.RunID, State: string(rec.State), Sequence: rec.Sequence, Revision: rec.Revision}, err
}
func (r *DesktopReader) StopLocalRun(ctx context.Context, k api.ConversationKey, run string, through uint64) (api.StopReceipt, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.StopReceipt{}, err
	}
	defer done()
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		return api.StopReceipt{}, err
	}
	rec, err := r.desk.app.hub.StopRun(ctx, kernel.StopRequest{Scope: scope, RunID: run, ThroughSequence: through})
	return api.StopReceipt{RunID: rec.RunID, Revision: rec.Revision, Drained: rec.Drained}, err
}

type localRunSub struct {
	out    chan api.Snapshot[api.LocalRunSnapshot]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *localRunSub) Updates() <-chan api.Snapshot[api.LocalRunSnapshot] { return s.out }
func (s *localRunSub) Close()                                             { s.cancel(); <-s.done }
func (r *DesktopReader) WatchLocalRun(ctx context.Context, k api.ConversationKey, run string) (api.Subscription[api.LocalRunSnapshot], error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		done()
		return nil, err
	}
	source, err := r.desk.app.hub.WatchRun(scope, run)
	if err != nil {
		done()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	s := &localRunSub{out: make(chan api.Snapshot[api.LocalRunSnapshot], 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer done()
		defer close(s.done)
		defer close(s.out)
		defer source.Close()
		defer stop()
		defer cancel()
		publish := func() {
			v := source.Snapshot()
			next := api.Snapshot[api.LocalRunSnapshot]{Version: v.Revision, Result: api.Result[api.LocalRunSnapshot]{State: api.Ready, Value: api.LocalRunSnapshot{InputID: v.Identity.InputID, RootID: v.Identity.RootID, RunID: v.Identity.RunID, State: string(v.State), Outcome: string(v.Outcome), Text: v.Text, Revision: v.Revision, Step: v.Step, SealedStep: v.SealedStep, Drained: v.Drained, ReasonClass: v.Reason.Class, ReasonDetail: v.Reason.Detail, ReasonModel: v.Reason.Model, Requeued: localRequeued(v.Requeued)}}}
			next.Value.Process = localProcess(v.Process)
			// 丢掉未读旧快照，最新累计态始终可取，终态不会堵在旧草稿后面。
			select {
			case <-s.out:
			default:
			}
			s.out <- next
		}
		publish()
		for {
			select {
			case <-ctx.Done():
				return
			case <-source.Wake():
				publish()
			}
		}
	}()
	return s, nil
}

func localProcess(log *turn.ProcessLog) []api.LocalProcessStep {
	if log == nil {
		return nil
	}
	out := make([]api.LocalProcessStep, len(log.Steps))
	for i, s := range log.Steps {
		out[i] = api.LocalProcessStep{ID: s.ID, Kind: s.Kind, Tool: s.Tool, Target: s.Target, Thought: s.Thought, Output: s.Output, State: s.State, Rule: s.Rule, Step: s.Step, Seconds: s.Seconds, Started: s.Started, Recall: apiRecall(s.Recall)}
	}
	return out
}

// apiRecall 把这一轮自动召回的明细换成界面的类型（字段一一对应，不另算）。
func apiRecall(items []turn.RecallItem) []api.RecallItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.RecallItem, len(items))
	for i, v := range items {
		out[i] = api.RecallItem{Text: v.Text, Kind: v.Kind, Source: v.Source, CreatedAt: v.CreatedAt, Pinned: v.Pinned, Hearsay: v.Hearsay,
			Score: v.Score, Rerank: v.Rerank, Sim: v.Sim, Lexical: v.Lexical, Vector: v.Vector}
		for _, p := range v.Previous {
			out[i].Previous = append(out[i].Previous, api.RecallPrior{Text: p.Text, CreatedAt: p.CreatedAt})
		}
	}
	return out
}

var _ api.LocalInputs = (*DesktopReader)(nil)

// plainText 判断这条提交是不是一个只有文字的普通消息（Text，或只有一个没有引用和附件的文字块），是的话交出文字。
// 带引用块或附件的不当命令。
func plainText(text string, parts []api.LocalPart) (string, bool) {
	if len(parts) == 0 {
		return text, true
	}
	if len(parts) == 1 && parts[0].Quote == nil && len(parts[0].Attachments) == 0 {
		return parts[0].Text, true
	}
	return "", false
}

// SlashCommands 列出本地私聊认的斜杠命令（输入框补全用）：直接取命令登记表，加命令只登记那一处。
func (r *DesktopReader) SlashCommands() []api.SlashCommand {
	if r.desk == nil || r.desk.app == nil || r.desk.app.slashes == nil {
		return nil
	}
	cmds := r.desk.app.slashes.reg.Commands()
	out := make([]api.SlashCommand, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, api.SlashCommand{Name: c.Name, Summary: c.Summary})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *DesktopReader) HeldLocal(ctx context.Context) ([]api.HeldInput, error) {
	if _, done, err := r.desk.enterChat(); err != nil {
		return nil, err
	} else {
		defer done()
	}
	rows, err := r.desk.app.hub.HeldInputs(ctx)
	out := make([]api.HeldInput, 0, len(rows))
	for _, v := range rows {
		if v.Scope.Platform != "local" || v.Scope.Scene != "private" {
			continue
		}
		k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: v.Scope.Conversation}
		out = append(out, api.HeldInput{Key: k, InputID: v.InputID, Text: v.Text, AcceptedAt: v.AcceptedAt.UnixMilli()})
	}
	return out, err
}

func (r *DesktopReader) ReleaseHeldLocal(ctx context.Context, k api.ConversationKey, id string) (api.InputReceipt, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.InputReceipt{}, err
	}
	defer done()
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		return api.InputReceipt{}, err
	}
	rec, err := r.desk.app.hub.ReleaseHeld(ctx, scope, id)
	return api.InputReceipt{InputID: rec.InputID, RunID: rec.RunID, State: string(rec.State), Sequence: rec.Sequence, Revision: rec.Revision}, err
}

func (r *DesktopReader) DiscardHeldLocal(ctx context.Context, k api.ConversationKey, id string) error {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		return err
	}
	return r.desk.app.hub.DiscardHeld(ctx, scope, id)
}

func (r *DesktopReader) WithdrawQueuedLocal(ctx context.Context, k api.ConversationKey, id string) error {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	scope, err := r.localInputScope(ctx, k)
	if err != nil {
		return err
	}
	return r.desk.app.hub.WithdrawQueued(ctx, scope, id)
}

var _ api.LocalHeld = (*DesktopReader)(nil)

func localRequeued(in *turn.RequeuedInputs) []api.LocalRequeued {
	if in == nil {
		return nil
	}
	var out []api.LocalRequeued
	for _, r := range in.Inputs {
		out = append(out, api.LocalRequeued{InputID: r.InputID, RunID: r.RunID, Text: r.Text, Seq: r.Seq})
	}
	return out
}
