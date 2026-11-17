package desktopread

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/history"
)

// ErrQuoteTarget 是引用的那条消息找不到：不存在、在别的会话、她看不到、或不是聊天消息。几种情形不区分，别人探不出别处有没有这个 id。
var ErrQuoteTarget = api.ErrQuoteTarget

// ResolveQuote 核对主人框选的引用并定下要引用的片段（规格-引用第三节）。
// 目标必须是 key 这个会话里她看得到的聊天消息，查法和读投影、媒体凭据同一套（store.ReadDisplayEvent）；
// 片段按 history.Select 定：选中的文字不在原文里时退回原文开头，Fallback 为 true，由调用方记日志。
func (s *Service) ResolveQuote(ctx context.Context, key api.ConversationKey, q api.LocalQuote) (history.Quote, error) {
	out, _, err := s.resolveQuote(ctx, key, q)
	return out, err
}

// ResolveWhisperParts 核对悄悄话的各块：每块都要引用一条消息，目标必须是 key 这个会话里她看得到的聊天消息（本地群聊、QQ 私聊、QQ 群都行，
// 查法同 ResolveQuote）。和本地私聊分块不同的是：Quote.Owner 按被引事件的 ActorKind=owner 标上（给模型“主人自己说过”用，不按名字认），
// Mentions、All 原样带上，附件不收（悄悄话不带附件）。任何一块不合格整条拒绝，错误写明第几块。
func (s *Service) ResolveWhisperParts(ctx context.Context, key api.ConversationKey, in []api.LocalPart) ([]history.Part, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w：悄悄话至少要有一块", ErrPart)
	}
	if len(in) > MaxParts {
		return nil, fmt.Errorf("%w：最多 %d 块", ErrPart, MaxParts)
	}
	out := make([]history.Part, 0, len(in))
	for i, p := range in {
		if p.Quote == nil {
			return nil, fmt.Errorf("第 %d 块：%w：悄悄话的每一块都要引用一句话", i+1, ErrPart)
		}
		if len(p.Attachments) > 0 {
			return nil, fmt.Errorf("第 %d 块：%w：悄悄话不带附件", i+1, ErrPart)
		}
		q, owner, err := s.resolveQuote(ctx, key, *p.Quote)
		if err != nil {
			return nil, fmt.Errorf("第 %d 块：%w", i+1, err)
		}
		q.Owner = owner
		out = append(out, history.Part{Quote: &q, Text: p.Text, Mentions: append([]string(nil), p.Mentions...), All: p.All})
	}
	return out, nil
}

// resolveQuote 同 ResolveQuote，另外交出被引的是不是主人自己说的（事件的 ActorKind=owner）。
func (s *Service) resolveQuote(ctx context.Context, key api.ConversationKey, q api.LocalQuote) (history.Quote, bool, error) {
	id := q.Ref.Event.EventID
	if id == "" || q.Ref.Conversation != key || (q.Ref.Event.Platform != "" && q.Ref.Event.Platform != key.Platform) {
		return history.Quote{}, false, ErrQuoteTarget
	}
	st, scope, err := s.scope(api.ConversationRequest{Key: key, Limit: 1})
	if err != nil {
		return history.Quote{}, false, err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return history.Quote{}, false, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return history.Quote{}, false, ctx.Err()
	}
	row, ok, err := st.ReadDisplayEvent(ctx, scope, id)
	<-s.queryGate
	if err != nil {
		return history.Quote{}, false, err
	}
	// 没说出口的（试运行、看完不说）她看不到，和撤回了的一样不能引用。
	if !ok || row.Kind != "message" || row.Recalled || history.NotSaid(history.Event{Origin: row.Origin}) || row.Text == "" {
		return history.Quote{}, false, ErrQuoteTarget
	}
	sel := history.Select(row.Text, q.Text, q.Start, q.End)
	sel.ID, sel.At, sel.Name = row.ID, row.CreatedAt, s.displayName(row)
	sel.Clock = time.UnixMilli(row.CreatedAt).Format("15:04") // 本机时区，引用当时定下，之后不随时区变
	return sel, row.ActorKind == "owner", nil
}

// ErrPart 是某一块本身不合格：空块、附件缺来源或种类不对、块太多。错误里写明是第几块。
var ErrPart = api.ErrPart

// ResolveParts 核对主人分块输入的每一块（规格-引用第三节）：引用块走 ResolveQuote，附件核对来源。
// 任何一块不合格整条都不发，错误写明第几块（只拒那一块再发其余的会让主人以为整条都发出去了）。
// 返回的 text 是各块文字用换行连起来的样子（events.text 存它）；只有一个没有引用、没有附件的普通块时 parts 返回 nil，
// 走和旧的纯文字输入完全一样的路径。
func (s *Service) ResolveParts(ctx context.Context, key api.ConversationKey, in []api.LocalPart) (parts []history.Part, text string, err error) {
	if len(in) == 0 {
		return nil, "", nil
	}
	if len(in) > MaxParts {
		return nil, "", fmt.Errorf("%w：最多 %d 块", ErrPart, MaxParts)
	}
	out := make([]history.Part, 0, len(in))
	for i, p := range in {
		if p.Text == "" && len(p.Attachments) == 0 {
			return nil, "", fmt.Errorf("第 %d 块：%w：没有文字也没有附件", i+1, ErrPart)
		}
		part := history.Part{Text: p.Text}
		if p.Quote != nil {
			q, err := s.ResolveQuote(ctx, key, *p.Quote)
			if err != nil {
				return nil, "", fmt.Errorf("第 %d 块：%w", i+1, err)
			}
			part.Quote = &q
		}
		for _, a := range p.Attachments {
			att, err := s.resolveAttachment(ctx, key, a)
			if err != nil {
				return nil, "", fmt.Errorf("第 %d 块：%w", i+1, err)
			}
			part.Attachments = append(part.Attachments, att)
		}
		out = append(out, part)
	}
	text = history.JoinText(out)
	if len(out) == 1 && out[0].Quote == nil && len(out[0].Attachments) == 0 {
		return nil, text, nil
	}
	return out, text, nil
}

// MaxParts 是一条消息最多几块，和 turn.MaxInputParts 一致。
const MaxParts = 20

func (s *Service) resolveAttachment(ctx context.Context, key api.ConversationKey, a api.LocalAttachment) (history.Attachment, error) {
	bad := func(why string) (history.Attachment, error) {
		return history.Attachment{}, fmt.Errorf("%w：附件%s", ErrPart, why)
	}
	if a.Kind != "image" && a.Kind != "file" {
		return bad("种类要是 image 或 file")
	}
	out := history.Attachment{Kind: a.Kind, Name: a.Name}
	switch {
	case a.Media != nil && a.Path != "":
		return bad("不能同时给媒体凭据和路径")
	case a.Media != nil:
		m := *a.Media
		if a.Kind != "image" || len(m.Forward) > 0 || m.Source.Conversation != key {
			return bad("媒体凭据只收本会话里的图片")
		}
		if _, err := s.mediaRow(ctx, m, false); err != nil {
			return history.Attachment{}, err
		}
		out.Hash, out.Conversation, out.Event = m.Hash, key.Conversation, m.Source.Event.EventID
	case a.Path != "":
		if strings.ContainsRune(a.Path, 0) {
			return bad("路径不合法")
		}
		out.Path = a.Path
		if a.Kind == "image" {
			out.Width, out.Height = imageDims(a.Path) // 发送时量好存下：之后文件被挪走，气泡仍按原比例占位
		}
	default:
		return bad("要给媒体凭据或路径")
	}
	return out, nil
}
