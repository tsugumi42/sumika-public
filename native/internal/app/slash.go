package app

// 本地私聊的斜杠命令入口（规格-目标和循环 第一节、第四节第 1 步）。
//
// 只有一个入口：主人在本地私聊输入框发来的整条消息，在 DesktopReader.SendLocal、SubmitLocal 里先过命令表。
// QQ、群聊、她自己说的话、转发和链接正文都不经过这两个方法，所以不会过命令表；
// "是不是本地私聊的主人"的判断统一写在 slashEligible 和 ownerLocalPrivate 里，/goal 续轮、schedule_next 也用同一个判断。
//
// 命令的回复是一条系统行（kind=notice、actor_kind=system），存进会话记录、不发给模型；
// 不认识的命令照原文发给她，并在主人这条消息后面补一行"没有这个命令"。

import (
	"context"
	"errors"
	"strings"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/slash"
	"sumika-native/internal/store"
)

// slashEligible 说这个会话键是不是本地私聊（主窗口）。斜杠命令只在这里认。
func slashEligible(k api.ConversationKey) bool {
	return k.Platform == "local" && k.Channel == "console" && k.Scene == "private" && localPrivateConv(k.Conversation)
}

// ownerLocalPrivate 说这个回合身份是不是主人在本地私聊：/goal 续轮、schedule_next 只对这样的回合生效。
func ownerLocalPrivate(s kernel.Scope) bool {
	return s.Platform == "local" && s.Channel == "console" && s.Scene == "private" && s.ActorKind == "owner" && s.ActorID == "owner" && localPrivateConv(s.Conversation)
}

// selftestSegs 给自检话题里存的行加 selftest 标记（和 turn 里存主人事件、她的回复同一个写法）：
// 记忆整理、链接插件、记忆插件据此跳过它们。不是自检话题原样返回 segs。
func selftestSegs(conv string, segs map[string]any) any {
	if !audience.IsSelftest(conv) {
		if segs == nil {
			return nil // 不能把空 map 装进 any 交出去：会被存成 JSON null，不是 NULL
		}
		return segs
	}
	m := map[string]any{}
	for k, v := range segs {
		m[k] = v
	}
	m["tags"] = []string{audience.SelftestTag}
	return m
}

func localPrivateConv(conv string) bool {
	return strings.HasPrefix(conv, "console:") && !strings.Contains(conv, ":group:")
}

// characterOfConv 从本地私聊会话号取出角色（console:<角色> 或 console:<角色>:topic:<话题>）。
func characterOfConv(conv string) string {
	parts := strings.Split(conv, ":")
	if len(parts) < 2 || parts[0] != "console" {
		return ""
	}
	return parts[1]
}

// slashCall 是一次命令的上下文。
type slashCall struct {
	Conv, Character string
	Text            string // 整条原文
	Inv             slash.Invocation
}

// slashResult：Reply 是给主人看的系统行（空就不记，Source 是它的来源名，空按 slash）；
// Send 非空时，把它当作这条消息发给她（/goal 设目标）。
type slashResult struct{ Reply, Send, Source string }

func (r slashResult) source() string {
	if r.Source == "" {
		return noticeSlash
	}
	return r.Source
}

// slashHandler 执行一条命令。返回的 error 文字会原样显示成系统行（比如 /loop 间隔写错了）。
type slashHandler func(ctx context.Context, c slashCall) (slashResult, error)

type slashTable struct {
	reg      *slash.Registry
	handlers map[string]slashHandler
}

func newSlashTable() *slashTable {
	return &slashTable{reg: slash.NewRegistry(), handlers: map[string]slashHandler{}}
}

// add 登记命令和它的处理函数；加命令只登记这一处。
func (t *slashTable) add(c slash.Command, h slashHandler) {
	t.reg.Register(c)
	t.handlers[strings.ToLower(c.Name)] = h
}

// 一条系统行的来源名，也是它的 actor_id。
const (
	noticeSlash = "slash"
	noticeGoal  = "goal"
	noticeLoop  = "loop"
)

// systemNotice 把一行系统行存进会话记录：不是主人和她说的话，不进她的历史和记忆，界面显示成灰色的一行。
func (a *App) systemNotice(conv, source, text string) error {
	st, err := a.set.ForConversation(conv)
	if err != nil {
		return err
	}
	_, err = st.AppendEvent(store.NewEvent{
		Channel: "console", Conversation: conv, ActorKind: "system", ActorID: source, Origin: "system:" + source, Kind: "notice",
		Text: text, Segments: selftestSegs(conv, map[string]any{"notice": "system", "source": source}), CreatedAt: a.now().UnixMilli(),
	}, []string{characterOfConv(conv)})
	if err == nil {
		a.statusChanged(conv)
	}
	return err
}

// 要等主人那句话存进库以后再补的系统行（让系统行排在主人消息后面）。
type pendingNotice struct{ conv, text, notice, source string }

type pendingNotices struct {
	mu   sync.Mutex
	list []*pendingNotice
}

// defer 登记一条：conv 里存下文字恰好是 text 的主人消息时补上 notice。返回的函数撤销还没补的那条。
func (p *pendingNotices) add(conv, text, source, notice string) (cancel func()) {
	n := &pendingNotice{conv: conv, text: text, notice: notice, source: source}
	p.mu.Lock()
	p.list = append(p.list, n)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for i, x := range p.list {
			if x == n {
				p.list = append(p.list[:i], p.list[i+1:]...)
				return
			}
		}
	}
}

func (p *pendingNotices) take(conv, text string) []*pendingNotice {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*pendingNotice
	kept := p.list[:0]
	for _, n := range p.list {
		if n.conv == conv && n.text == text {
			out = append(out, n)
		} else {
			kept = append(kept, n)
		}
	}
	p.list = kept
	return out
}

// onLocalStored 是内核"主人消息入库"的回调：先照旧交给链接插件，再补排队的系统行。只写本地库，不等网络。
func (a *App) onLocalStored(conv string, seq int64, text string) {
	a.observeLocalLinks(conv, seq, text)
	for _, n := range a.pendingNotes.take(conv, text) {
		if err := a.systemNotice(n.conv, n.source, n.notice); err != nil {
			a.logf("系统行没写进去：%v", err)
		}
	}
}

// callSlash 调命令的处理函数并通知界面刷新状态：输入框里的斜杠命令和 sumika-ctl 的 goal、loop 共用，不各写一套。
func (a *App) callSlash(ctx context.Context, conv, character, text string, inv slash.Invocation) (slashResult, error) {
	h := a.slashes.handlers[strings.ToLower(inv.Command.Name)]
	if h == nil {
		return slashResult{}, errors.New("命令没有处理函数：/" + inv.Command.Name)
	}
	res, err := h(ctx, slashCall{Conv: conv, Character: character, Text: text, Inv: inv})
	a.statusChanged(conv) // 命令可能改了目标或定期任务
	return res, err
}

// localSlash 处理主人在本地私聊发来的整条消息。
//   - handled 为真：这是一条命令，已经执行完，不要再发给她（回复已存成系统行；err 只是写系统行失败）；
//   - handled 为假：照常发送，send 是要发的文字（/goal 设目标时是目标原文，其余是原文）；
//     done 在发送返回后调，撤销没补成的系统行。
func (a *App) localSlash(ctx context.Context, k api.ConversationKey, character, text string) (send string, handled bool, done func(), err error) {
	noop := func() {}
	if a.slashes == nil || !slashEligible(k) {
		return text, false, noop, nil
	}
	inv, perr := a.slashes.reg.Parse(text)
	var unknown *slash.UnknownError
	switch {
	case errors.Is(perr, slash.ErrNotCommand):
		return text, false, noop, nil
	case errors.As(perr, &unknown):
		cancel := a.pendingNotes.add(k.Conversation, text, noticeSlash, "没有这个命令：/"+unknown.Name+"，已经当普通消息发给她")
		return text, false, cancel, nil
	case perr != nil:
		return text, false, noop, perr
	}
	res, herr := a.callSlash(ctx, k.Conversation, character, text, inv)
	if herr != nil {
		// 命令自己报的错（写法不对、没有这个目标……）显示成系统行，这条消息不再发给她
		return text, true, noop, a.systemNotice(k.Conversation, noticeSlash, herr.Error())
	}
	if res.Send != "" {
		cancel := func() {}
		if res.Reply != "" {
			cancel = a.pendingNotes.add(k.Conversation, res.Send, res.source(), res.Reply)
		}
		return res.Send, false, cancel, nil
	}
	if res.Reply != "" {
		return text, true, noop, a.systemNotice(k.Conversation, res.source(), res.Reply)
	}
	return text, true, noop, nil
}
