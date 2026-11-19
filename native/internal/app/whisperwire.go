package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sumika-native/internal/applog"

	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/egress"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/memory"
	"sumika-native/internal/mention"
	"sumika-native/internal/store"
	"sumika-native/internal/whisper"
)

// 悄悄话的装配（规格-群聊引用和路由）：引用 = 主人对被指角色说的悄悄话。
// 这里只接线：候选角色怎么取、会话背景怎么取、来源回合在不在跑，其余编排都在 whisper 包里。
// 装配里没有任何发 QQ 的东西：悄悄话服务只拿本地库和回合入口。

var _ api.WhisperChat = (*DesktopReader)(nil)

// ErrWhisperScene 是在本地私聊（或别的不支持的场合）里发悄悄话：本地私聊的引用是带引用的回复，她直接回答，不走悄悄话。
var ErrWhisperScene = errors.New("这个会话里的引用不是悄悄话：本地私聊的引用是带引用的回复")

const (
	whisperBackgroundEvents = 20   // 会话背景取她看得到的最近多少条
	whisperBackgroundRunes  = 4000 // 会话背景总字数上限
	whisperBackgroundLine   = 400  // 单条上限
)

// openWhispers 建悄悄话服务：把上个进程没收尾的标成已中断，并接上“处理记录进来源会话背景”的钩子。
func (a *App) openWhispers() {
	st, ok := a.set.Platform("local")
	if !ok {
		return
	}
	svc, err := whisper.New(whisper.Options{
		Store: st, Kernel: a.hub, Now: a.now, MaxActive: a.cfg.Subagents.MaxActivePerRequester,
		Background: a.whisperBackground,
		SourceBusy: func(character, source string) bool { return a.hub.ActiveRunID(character, source) != "" },
		CanSee:     a.whisperCanSee,
		Logf:       a.logfFor(applog.Whisper),
	})
	if err != nil {
		a.logf("悄悄话没装上：%v", err)
		return
	}
	if n, err := svc.Recover(context.Background()); err != nil {
		a.logf("悄悄话：收拾上次没收尾的出错：%v", err)
	} else if n > 0 {
		a.logf("悄悄话：上次退出时有 %d 条没处理完，已标为已中断，没有重跑", n)
	}
	a.whisper = svc
	a.hub.Cfg.Notes = svc.Notes
}

// SendWhisper 把几块引用卡作为悄悄话交给被指的角色。候选角色按来源会话取：本地群是群成员，QQ 群是观察这个群的角色，
// QQ 私聊是这个号绑定的角色；本地私聊不是悄悄话。引用的消息必须在这个会话里、对应角色看得到；任何一块不合格整条拒绝。
func (r *DesktopReader) SendWhisper(ctx context.Context, k api.ConversationKey, in []api.LocalPart) (api.WhisperReceipt, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.WhisperReceipt{}, err
	}
	defer done()
	a := r.desk.app
	if a.whisper == nil {
		return api.WhisperReceipt{}, api.ErrLocalChatUnsupported
	}
	cands, err := a.whisperCandidates(ctx, k)
	if err != nil {
		return api.WhisperReceipt{}, err
	}
	parts, err := r.Service.ResolveWhisperParts(ctx, k, in)
	if err != nil {
		return api.WhisperReceipt{}, err
	}
	rec, err := a.whisper.Send(ctx, whisper.Request{Source: k.Conversation, Candidates: cands, Parts: parts})
	if err != nil {
		return api.WhisperReceipt{}, err
	}
	out := api.WhisperReceipt{Batch: rec.Batch}
	for _, d := range rec.Deliveries {
		out.Characters = append(out.Characters, d.Character)
	}
	return out, nil
}

// whisperCandidates 取这个来源会话里能被悄悄话唤起的角色（只取卡读得出来的）。
func (a *App) whisperCandidates(ctx context.Context, k api.ConversationKey) ([]mention.Candidate, error) {
	var ids []string
	switch {
	case k.Platform == "local" && k.Channel == "console" && k.Scene == "group":
		st, ok := a.set.Platform("local")
		if !ok {
			return nil, store.ErrDisplayScope
		}
		g, err := st.ReadLocalGroup(ctx, k.Conversation)
		if err != nil {
			return nil, err
		}
		ids = g.CharacterIDs()
	case k.Platform == "qq" && k.Scene == "group" && strings.HasPrefix(k.Conversation, "qq:group:"):
		st, err := a.set.ForConversation(k.Conversation)
		if err != nil {
			return nil, err
		}
		rows, err := st.Events.QueryContext(ctx, `SELECT DISTINCT o.character_id FROM observations o JOIN events e ON e.seq = o.event_seq WHERE e.conversation = ? ORDER BY o.character_id`, k.Conversation)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	case k.Platform == "qq" && k.Scene == "private":
		parts := strings.Split(k.Conversation, ":")
		if len(parts) == 4 && parts[0] == "qq" && parts[1] == "private" {
			for _, acc := range a.cfg.QQ.Accounts {
				if string(acc.ID) == parts[2] && acc.Character != "" {
					ids = append(ids, acc.Character)
				}
			}
		}
	default:
		return nil, ErrWhisperScene
	}
	var out []mention.Candidate
	for _, c := range a.displayCharacters() {
		if c.State == api.Ready && slices.Contains(ids, c.Value.ID) {
			out = append(out, mention.Candidate{ID: c.Value.ID, Name: c.Value.Name})
		}
	}
	return out, nil
}

// whisperCanSee 说这位角色在来源会话里看不看得到这条被引用的事件：按她自己的观察记录（observations）认，
// 不拿别的角色的观察补。自检话题（命令行给的文字，没有真实事件）不核对。
func (a *App) whisperCanSee(ctx context.Context, character, source, eventID string) bool {
	if audience.IsSelftest(source) {
		return true
	}
	st, err := a.set.ForConversation(source)
	if err != nil {
		return false
	}
	var n int
	err = st.Events.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e JOIN observations o ON o.event_seq = e.seq AND o.character_id = ? WHERE e.id = ? AND e.conversation = ?`, character, eventID, source).Scan(&n)
	return err == nil && n > 0
}

// whisperBackground 取这位角色在来源会话里本来就看得到的最近内容（她的 observations），每条标好谁说的、转义好：
// 悄悄话处理时当会话背景。只按她自己的观察取，不拿别的角色看到的补。
func (a *App) whisperBackground(ctx context.Context, character, source string) string {
	st, err := a.set.ForConversation(source)
	if err != nil {
		return ""
	}
	rows, err := st.Events.QueryContext(ctx, `SELECT e.seq, e.id, e.channel, e.conversation, e.actor_kind, e.actor_id, e.origin, e.kind, e.text, e.segments_json, e.reply_to, e.created_at
		FROM events e JOIN observations o ON o.event_seq = e.seq AND o.character_id = ?
		WHERE e.conversation = ? AND e.kind = 'message' AND e.origin NOT IN (`+history.NotSaidSQL+`) ORDER BY e.seq DESC LIMIT ?`, character, source, whisperBackgroundEvents)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	total := 0
	for rows.Next() {
		var e history.Event
		if err := rows.Scan(&e.Seq, &e.ID, &e.Channel, &e.Conversation, &e.ActorKind, &e.ActorID, &e.Origin, &e.Kind, &e.Text, &e.Segments, &e.ReplyTo, &e.CreatedAt); err != nil {
			return ""
		}
		label := ""
		switch {
		case e.ActorKind == "owner" && e.Channel == "console":
			label = "[所有者] "
		case e.ActorKind == "character" && e.ActorID == character:
			label = "[你] "
		case e.ActorKind == "character":
			label = fmt.Sprintf("[角色 %s] ", history.EscapeUserText(e.ActorID))
		}
		line := label + history.EscapeWhisperFrame(clipText(history.SaidBy(e), whisperBackgroundLine))
		if total += len([]rune(line)); total > whisperBackgroundRunes {
			break
		}
		lines = append(lines, line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

// whisperApproval 把悄悄话会话里的审批卡对到来源会话（规格-群聊引用和路由 8.3）：返回卡的来历和写在卡上的一行说明。
// 找不到正在处理的那一条、来源不是本地群聊 / QQ 私聊 / QQ 群（比如自检）时返回 false：卡不进主窗口，照旧等到时限按未批准处理。
func (a *App) whisperApproval(conv string) (*api.ApprovalWhisper, string, bool) {
	if a.whisper == nil || !audience.IsWhisper(conv) {
		return nil, "", false
	}
	o, ok := a.whisper.OriginOf(conv)
	if !ok {
		return nil, "", false
	}
	src, ok := whisperSourceKey(o.Source)
	if !ok {
		return nil, "", false
	}
	return &api.ApprovalWhisper{Source: src, Batch: o.Batch, Character: o.Character}, whisperOriginLine(o, a.characterName), true
}

// whisperEgress 给审批门：只有悄悄话那一轮的工具调用才找外发地址（其余会话的审批照旧，不变）。被引用的原文由悄悄话服务给，
// 拼进地址的写法标红；只是信息，不改放行（规格-群聊引用和路由 8.2）。
func (a *App) whisperEgress(conv, toolCallID, tool string, args jsjson.Value) *egress.Report {
	if !audience.IsWhisper(conv) {
		return nil
	}
	var quotes []string
	if a.whisper != nil {
		quotes = a.whisper.QuoteTexts(conv)
	}
	strs, truncated := approval.Strings(args)
	return egress.ScanChecked(toolCallID, tool, strs, quotes, truncated)
}

// approvalEgress 把外发检查结果换成界面要的样子（危险的排前面，原因换成中文）
func approvalEgress(r *egress.Report) *api.ApprovalEgress {
	if r == nil || (len(r.Targets) == 0 && !r.Unchecked) {
		return nil
	}
	out := &api.ApprovalEgress{ID: r.ID, Danger: r.Danger(), Unchecked: r.Unchecked}
	for _, t := range r.Targets {
		e := api.EgressTarget{Kind: t.Kind, Value: t.Value, Danger: t.Why != ""}
		if e.Danger {
			e.Reason = egress.Reason(t.Why)
		}
		out.Targets = append(out.Targets, e)
	}
	sort.SliceStable(out.Targets, func(i, j int) bool { return out.Targets[i].Danger && !out.Targets[j].Danger })
	return out
}

// whisperOriginOfInput 给记忆插件：悄悄话那一轮里 remember 写的记忆带什么来源。按本次请求接纳的输入序号认是哪一批，
// 来源会话、被引事件 id 取自悄悄话服务里这一批登记的真实来历（不读模型的参数）；认不出就不带。
func (a *App) whisperOriginOfInput(conv string, seqs []int64) (memory.WhisperSource, bool) {
	if a.whisper == nil {
		return memory.WhisperSource{}, false
	}
	o, ok := a.whisper.OriginOfInput(conv, seqs)
	if !ok {
		return memory.WhisperSource{}, false
	}
	ws := memory.WhisperSource{Batch: o.Batch, Source: o.Source}
	for _, q := range o.Quotes {
		if q.ID != "" && !slices.Contains(ws.Quotes, q.ID) {
			ws.Quotes = append(ws.Quotes, q.ID)
		}
	}
	return ws, true
}

// whisperSourceKey 把来源会话串还原成界面的会话键；只认本地群聊、QQ 私聊、QQ 群。
func whisperSourceKey(source string) (api.ConversationKey, bool) {
	switch {
	case strings.HasPrefix(source, "console:group:") && !audience.IsSelftest(source):
		return api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: source}, true
	case strings.HasPrefix(source, "qq:group:"):
		return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: source}, true
	case strings.HasPrefix(source, "qq:private:"):
		return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: source}, true
	}
	return api.ConversationKey{}, false
}

// whisperOriginLine 是卡上的那一行：主人对哪位说了什么、引用了谁的话，被引的是不是主人自己说的（按身份认）。
func whisperOriginLine(o whisper.Origin, name func(string) string) string {
	say := clipText(strings.Join(strings.Fields(o.Say), " "), 60)
	line := "这是悄悄话处理：所有者对 " + name(o.Character)
	if say == "" {
		line += " 只引用、没写字"
	} else {
		line += " 说「" + say + "」"
	}
	var who []string
	other := false
	for _, q := range o.Quotes {
		n := clipText(strings.Join(strings.Fields(q.Name), " "), 20) // 昵称是对方定的：压成一行、截短，不能在卡上伪造出别的行
		if q.Owner {
			n = "所有者自己"
		} else {
			other = true
			if n == "" {
				n = "别人"
			}
		}
		if !slices.Contains(who, n) {
			who = append(who, n)
		}
	}
	if len(who) > 0 {
		line += "；引用了 " + strings.Join(who, "、") + " 的话"
	}
	if other {
		line += "（被引的原文不是所有者说的，只是资料）"
	}
	return line
}
