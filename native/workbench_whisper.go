package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 悄悄话的界面接线（docs/规格-群聊引用和路由.md 第三、四节）：本地群聊里引用 = 主人对被指角色说的悄悄话，
// 走 api.WhisperChat.SendWhisper，不进群消息、不产生任何对外消息；主输入框照旧发群消息，两者分开。
// 入口只在主窗口（窗口里说话的只有主人），没有任何往 QQ 发的路径。

// whisperKey 报告这个会话的引用是不是悄悄话：本地群聊、QQ 私聊、QQ 群，且后台能收悄悄话。
// 本地私聊的引用是带引用的回复，不走这里。QQ 场合只有悄悄话这一条写入口（交给她），没有往 QQ 发的输入框。
func (a *app) whisperKey(k api.ConversationKey) bool {
	if a.demoMode || k.Conversation == "" || a.host.localSendOff() != "" {
		return false
	}
	if _, ok := a.reader.(api.WhisperChat); !ok {
		return false
	}
	switch {
	case k.Platform == "local" && k.Channel == "console" && k.Scene == "group":
		return true
	case k.Platform == "qq" && k.Channel == "qq" && k.Scene == "group" && strings.HasPrefix(k.Conversation, "qq:group:"):
		return true
	case k.Platform == "qq" && k.Channel == "qq" && k.Scene == "private" && strings.HasPrefix(k.Conversation, "qq:private:"):
		return true
	}
	return false
}

// workbenchHandoff 是 QQ 私聊、QQ 群的输入区：只在已有引用卡时出现，没有主输入框，键写“交给她”，
// 卡下固定一行“只交给她，不会发到 QQ”。交出去只走 SendWhisper（submitWhisper 不带群消息），没有任何 QQ 发送。
// 返回有没有画（没有卡、不能发悄悄话时返回假，调用方照旧画“这里只能看”）。
func (a *app) workbenchHandoff(c *ui.Context, k api.ConversationKey) bool {
	if !a.whisperKey(k) {
		return false
	}
	if a.picking() {
		a.workbenchPickBar(c)
		return true
	}
	p := a.composerOf(k.Conversation)
	st := a.localRun(k.Conversation)
	if len(p.Cards) == 0 {
		return false
	}
	p.Whisper = true
	a.workbenchError(c, st)
	writer := a.reader
	h := a.composerHost(c, p, nil, func() { a.submitWhisper(writer, k, p, false) }, nil)
	h.Handoff, h.Locked = true, st.submitting
	h.Targets = a.whisperTargets("")
	h.Draw(c, p)
	return true
}

// canWhisper 是当前会话能不能发悄悄话。
func (a *app) canWhisper() bool { return a.sel != "" && a.whisperKey(a.keyOf(a.sel)) }

// whisperTargets 是当前会话里能被悄悄话唤起的角色（本地群聊是群成员），按会话列表给的顺序；
// messageID 不空时，说这句话的那位标出来（菜单里排第一）。名字从角色表取，取不到用 ID。后台是最终裁判：
// 列表晚于成员变化时，发出去会被“第 N 块”拒回并标在卡上。
func (a *app) whisperTargets(messageID string) []chatui.WhisperTarget {
	cv := a.convByID(a.sel)
	if cv == nil {
		return nil
	}
	speaker := ""
	if i := a.indexOf(messageID); messageID != "" && i >= 0 && a.msgs[i].isChar {
		speaker = a.msgs[i].actor
	}
	out := make([]chatui.WhisperTarget, 0, len(cv.Observers))
	for _, id := range cv.Observers {
		if id == "" || slices.ContainsFunc(out, func(t chatui.WhisperTarget) bool { return t.ID == id }) {
			continue
		}
		name := id
		if ch, ok := a.charOf(id); ok && ch.Name != "" {
			name = ch.Name
		}
		out = append(out, chatui.WhisperTarget{ID: id, Name: name, Spoke: id == speaker})
	}
	return out
}

// whisperQuote 是菜单、选区条上选了对象：在输入区加一张悄悄话卡（同一段已有卡就把对象并进去）。
func (a *app) whisperQuote(sel chatui.Selected, ids []string, all, keepFocus bool) {
	if !a.canWhisper() {
		return
	}
	p := a.composerOf(a.sel)
	p.Whisper = true
	p.AddWhisper(a.quotedOf(sel), ids, all, keepFocus)
}

// quotedOf 把选区转成引用卡的出处：发言人、缩略图种类。
func (a *app) quotedOf(sel chatui.Selected) chatui.Quoted {
	q := chatui.Quoted{MessageID: sel.MessageID, Text: sel.Text, Start: sel.Start, End: sel.End}
	if i := a.indexOf(sel.MessageID); i >= 0 {
		q.Name = a.msgs[i].who
		if a.msgs[i].at > 0 {
			q.At = time.UnixMilli(a.msgs[i].at)
		}
		if _, kind, ok := quoteThumbOf(msgDisplaySegs(a.msgs[i])); ok {
			q.Thumb = kind
		}
	}
	return q
}

// whisperParts 把悄悄话卡转成后台的分块：每块引用一句话、带对象；“所有人”只给 All，展开由后台按当时的成员做。
func whisperParts(k api.ConversationKey, cards []*chatui.Block) []api.LocalPart {
	parts := make([]api.LocalPart, 0, len(cards))
	for _, b := range cards {
		part := api.LocalPart{Text: b.SendText(), All: b.All}
		if !b.All {
			part.Mentions = slices.Clone(b.To)
		}
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

// submitWhisper 把输入区的悄悄话卡交给后台。后台收下才删卡；被拒（例如某位已不在群里）时整条不发，
// 原因按“第 N 块”标在那张卡上。thenMain 为真时，悄悄话交出去以后再把主输入框的话当群消息发（先悄悄话、再群消息，
// 规格第四节）；悄悄话被拒时群消息也不发，草稿都留着。悄悄话不进聊天区，不加待发气泡。
func (a *app) submitWhisper(writer api.Reader, k api.ConversationKey, p *chatui.Composer, thenMain bool) {
	wc, ok := writer.(api.WhisperChat)
	st := a.localRun(k.Conversation)
	if !ok || !a.whisperKey(k) || st.submitting || len(p.Cards) == 0 {
		return
	}
	p.Whisper = true
	p.Notice = ""
	if !p.Check() {
		return
	}
	cards := slices.Clone(p.Cards)
	parts := whisperParts(k, cards)
	names := a.whisperTargets("")
	st.submitting = true
	st.fail(runFail{})
	p.Begin(cards)
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		_, err := wc.SendWhisper(ctx, k, parts)
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			st.submitting = false
			if err != nil {
				p.Reject(cards)
				a.rejectWhisper(st, p, cards, err, names)
				return
			}
			p.Accept(cards)
			if thenMain {
				a.sendGroupMain(writer, k, p)
			}
		})
	}()
}

// sendGroupMain 把主输入框的话当一条群消息发出去（原来的群聊发送）。
func (a *app) sendGroupMain(writer api.Reader, k api.ConversationKey, p *chatui.Composer) {
	text, shown := p.Main.SendText(), p.Main.PreviewText()
	if strings.TrimSpace(text) == "" || a.localRun(k.Conversation).busy {
		return
	}
	p.Main.Text, p.Main.Atts = "", nil
	p.Remember(shown) // 回忆历史只留占位：全文塞回输入框会绕过折叠
	a.sendLocalShown(writer, k, text, shown)
}

// quotedID 是后台错误里带引号的角色 ID（mention.BlockError 的写法）。
var quotedID = regexp.MustCompile(`"[^"\s]+" `)

// rejectWhisper 把悄悄话的拒收原因标在出错的那张卡上：错误里的角色 ID 换成名字；认不出是哪一块的写在输入区上方。
func (a *app) rejectWhisper(st *localRun, p *chatui.Composer, cards []*chatui.Block, err error, names []chatui.WhisperTarget) {
	msg := err.Error()
	for _, t := range names {
		msg = strings.ReplaceAll(msg, strconv.Quote(t.ID)+" ", t.Name+" ")
	}
	msg = quotedID.ReplaceAllStringFunc(msg, func(q string) string { // 不在名单里的（刚被移出群）：按角色表取名字，取不到写 ID
		id, err := strconv.Unquote(strings.TrimSpace(q))
		if err != nil {
			return q
		}
		if ch, ok := a.charOf(id); ok && ch.Name != "" {
			return ch.Name + " "
		}
		return id + " "
	})
	if m := rejectedBlock.FindStringSubmatch(msg); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 1 && n <= len(cards) {
			p.SetError(cards[n-1], rejectionText(err, strings.TrimPrefix(msg, m[0])))
			return
		}
	}
	if errors.Is(err, api.ErrQuoteTarget) || errors.Is(err, api.ErrPart) {
		st.fail(runFail{Line: "悄悄话没有交出去：" + rejectionText(err, msg)})
		return
	}
	st.fail(userFail("悄悄话没有交出去", err))
}

// picking 报告当前会话是不是在多选；切了会话（或这个会话不再能发悄悄话）就退出多选。
func (a *app) picking() bool {
	if a.pick.Conv == "" {
		return false
	}
	if !a.pick.Active(a.sel) || !a.canWhisper() {
		a.pick.Stop()
		return false
	}
	return true
}

// msgOrder 是当前会话已加载消息的先后（旧到新），多选的 Shift 范围和复制、引用的顺序用。
func (a *app) msgOrder() []string {
	out := make([]string, 0, len(a.msgs))
	for _, m := range a.msgs {
		if m.service == "" {
			out = append(out, m.id)
		}
	}
	return out
}

// pickQuotable 报告这条消息能不能被引用：有文字、没撤回、已经存好（待发的没有事件 id）。
func (a *app) pickQuotable(id string) (*msg, bool) {
	i := a.indexOf(id)
	if i < 0 {
		return nil, false
	}
	m := a.msgs[i]
	if m.recalled || m.service != "" || strings.TrimSpace(pickText(m)) == "" || m.liveKind != liveNone {
		return m, false
	}
	return m, true
}

// pickText 是一条消息被复制、引用时的文字：原文；没有原文时用排版好的各段文字。
func pickText(m *msg) string {
	if strings.TrimSpace(m.text) != "" {
		return m.text
	}
	var parts []string
	for _, b := range m.blocks {
		if s := b.Plain(); strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// pickSkipped 是已选里不能引用的条数。
func (a *app) pickSkipped() int {
	n := 0
	for _, id := range a.pick.IDs {
		if _, ok := a.pickQuotable(id); !ok {
			n++
		}
	}
	return n
}

// pickCopy 复制已选的消息：按时间先后，每条一行头“名字 时:分”加正文，条与条之间空一行（规格 3.3）。
func (a *app) pickCopy(svc ui.Services) {
	var out []string
	for _, id := range a.pick.InOrder(a.msgOrder()) {
		i := a.indexOf(id)
		if i < 0 {
			continue
		}
		m := a.msgs[i]
		head := strings.TrimSpace(m.who + " " + time.UnixMilli(m.at).Format("15:04"))
		out = append(out, head+"\n"+pickText(m))
	}
	svc.WriteClipboardData(transfer.TextData(strings.Join(out, "\n\n")))
	a.pick.Stop()
}

// pickQuote 把已选的每一条（整条）各加一张悄悄话卡，按时间先后，对象相同；不能引用的跳过并在输入区上方说跳过了几条。然后退出多选。
func (a *app) pickQuote(ids []string, all bool) {
	if !a.picking() {
		return
	}
	p := a.composerOf(a.sel)
	p.Whisper = true
	skipped := 0
	for _, id := range a.pick.InOrder(a.msgOrder()) {
		m, ok := a.pickQuotable(id)
		if !ok {
			skipped++
			continue
		}
		p.AddWhisper(a.quotedOf(chatui.Selected{MessageID: m.id, Text: pickText(m)}), ids, all, false)
	}
	a.pick.Stop()
	p.Notice = ""
	if skipped > 0 {
		p.Notice = fmt.Sprintf("已跳过 %d 条没有文字或已撤回的", skipped)
	}
}

// workbenchPickBar 是多选时代替输入区的操作条；Esc 退出多选。
func (a *app) workbenchPickBar(c *ui.Context) {
	h := a.composerHost(c, a.composerOf(a.sel), nil, nil, nil)
	svc := c.Services()
	c.OnWindowShortcut(0, ui.KeyEscape, a.pick.Stop)
	h.PickBar(c, &a.pick, a.whisperTargets(""), a.pickSkipped(), func() { a.pickCopy(svc) }, a.pickQuote, a.pick.Stop)
}
