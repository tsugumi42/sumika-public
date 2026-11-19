package app

// 她向主人提问（规格-向主人提问）的装配：谁能问、什么时候算没人看着、问答怎么进聊天记录、桌面怎么看和答。
// 回答不是批准：这里不碰 approvals、Grants，答案只变成文字。

import (
	"context"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/askowner"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func (a *App) openAskOwner() error {
	return a.hub.Use(askowner.New(askowner.Options{
		Center:       a.asks,
		Allowed:      askAllowed,
		Unattended:   a.askUnattended,
		SteerPending: func(c turn.ToolCall) bool { return a.hub.SteerPending(c.TurnID) },
		WaitFor:      func() time.Duration { return time.Duration(a.approvalGate.ApprovalMinutes * float64(time.Minute)) },
		Said: func(c turn.ToolCall, text string) error {
			return a.askEvent(c, "character", c.CharacterID, text)
		},
		Answered: func(c turn.ToolCall, text string) error {
			return a.askEvent(c, "owner", "owner", text)
		},
		Notify: func(c turn.ToolCall, text string) {
			a.attend(AttentionQuestion, c.Conversation, "她有件事想问你", text)
		},
		Now:  a.now,
		Logf: a.logfFor(applog.Askowner),
	}))
}

// askAllowed 执行时再核一遍身份（工具清单已经按 owner-console 挡过）：主人在本地私聊的前台回合，会话、角色对得上。
// 身份只看宿主绑定的 Identity 和听众，不看参数和名字
func askAllowed(c turn.ToolCall) bool {
	s := c.Identity.Scope
	return c.Identity.Kind == kernel.KindForeground && ownerLocalPrivate(s) && s.Conversation == c.Conversation && s.CharacterID == c.CharacterID &&
		askowner.Visible(c.Audience)
}

// askUnattended 是这个会话现在没人等着回答：目标进行中（第一轮也算）、自动轮、自检话题。只看宿主自己的状态
func (a *App) askUnattended(conv string) bool {
	return audience.IsSelftest(conv) || a.auto.has(conv) || a.goalActive(conv)
}

// askEvent 把问答存成一条普通消息（来源和她、主人平时说的一样，界面按来源分左右）：问题一问出口就在记录里，
// 重启不用恢复任何东西，主人照常回一句就开新一轮。这一轮的实例不会再追一遍：不在 done 里，重建历史时才读到
func (a *App) askEvent(c turn.ToolCall, actorKind, actorID, text string) error {
	st, err := a.set.ForConversation(c.Conversation)
	if err != nil {
		return err
	}
	_, err = st.AppendEvent(store.NewEvent{Channel: "console", Conversation: c.Conversation, ActorKind: actorKind, ActorID: actorID, Origin: actorKind, Kind: "message",
		Text: text, Segments: selftestSegs(c.Conversation, nil), CreatedAt: a.now().UnixMilli()}, []string{c.CharacterID})
	return err
}

func (d *Desktop) localQuestions() []api.LocalQuestion {
	var out []api.LocalQuestion
	for _, q := range d.app.asks.Snapshot() {
		v := api.LocalQuestion{ID: q.ID, CharacterID: q.CharacterID, Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: q.Conversation}, Text: q.Text, ExpiresAt: q.ExpiresAt}
		for _, o := range q.Options {
			v.Options = append(v.Options, api.LocalQuestionOption{Label: o.Label, Detail: o.Detail})
		}
		out = append(out, v)
	}
	return out
}

// AnswerLocal 核对会话和角色后回答；问题已结束、不属于这个会话、下标越界都是 ErrQuestionGone
func (r *DesktopReader) AnswerLocal(ctx context.Context, k api.ConversationKey, id string, option int) error {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	character, err := r.localCharacter(ctx, k)
	if err != nil {
		return err
	}
	for _, q := range r.desk.app.asks.Snapshot() {
		if q.ID == id && q.CharacterID == character && q.Conversation == k.Conversation {
			if r.desk.app.asks.Answer(id, option) {
				return nil
			}
			break
		}
	}
	return api.ErrQuestionGone
}

type localQuestionSub struct {
	out    chan api.Snapshot[[]api.LocalQuestion]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *localQuestionSub) Updates() <-chan api.Snapshot[[]api.LocalQuestion] { return s.out }
func (s *localQuestionSub) Close()                                            { s.cancel(); <-s.done }

// WatchLocalQuestions 和审批卡共用一组唤醒：哪边变了都重读，自己的快照没变也只是多发一次
func (r *DesktopReader) WatchLocalQuestions(ctx context.Context) (api.Subscription[[]api.LocalQuestion], error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	s := &localQuestionSub{out: make(chan api.Snapshot[[]api.LocalQuestion], 1), cancel: cancel, done: make(chan struct{})}
	d := r.desk
	wake := make(chan struct{}, 1)
	d.mu.Lock()
	d.approvalWakes[wake] = struct{}{}
	d.mu.Unlock()
	go func() {
		defer done()
		defer close(s.done)
		defer close(s.out)
		defer stop()
		defer cancel()
		defer func() { d.mu.Lock(); delete(d.approvalWakes, wake); d.mu.Unlock() }()
		for version := uint64(1); ; version++ {
			value := d.localQuestions()
			select {
			case s.out <- api.Snapshot[[]api.LocalQuestion]{Version: version, Result: api.Result[[]api.LocalQuestion]{State: api.Ready, Value: value}}:
			case <-ctx.Done():
				return
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}

var _ api.LocalQuestions = (*DesktopReader)(nil)
