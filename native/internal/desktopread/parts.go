package desktopread

import (
	"sumika-native/internal/api"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
)

func fillSnippet(v *api.QuoteView, q *history.Quote) {
	v.Snippet, v.SnippetStart, v.SnippetEnd = q.Text, q.Start, q.End
	v.SnippetWhole, v.SnippetFallback, v.SnippetTruncated = q.Whole, q.Fallback, q.Truncated
}

// projectParts 把主人分块消息的存档还原成界面用的块，顺序同发送。引用块的出处走 DisplayRow.QuoteTargets：
// 范围和可见性与 Reply 同一套，被引用那条后来看不到时 Quote.State 为 Unsupported，不交片段以外的任何原文。
func (s *Service) projectParts(k api.ConversationKey, r store.DisplayRow, parts []history.Part) []api.MessagePart {
	out := make([]api.MessagePart, 0, len(parts))
	for _, p := range parts {
		mp := api.MessagePart{Text: p.Text}
		if q := p.Quote; q != nil {
			target := api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: q.ID}}
			mp.HasQuote, mp.Ref = true, &target
			mp.Quote.State = api.Unsupported
			if row, ok := r.QuoteTargets[q.ID]; ok {
				v := api.QuoteView{Ref: target, ActorKind: row.ActorKind, DisplayName: s.displayName(row), Text: row.Text, Recalled: row.Recalled}
				if env := parseEnvelope(row.Segments); env.ok {
					v.Segments = displaySegments(target, env, row.AtNames, nil)
				}
				fillSnippet(&v, q)
				mp.Quote = api.Result[api.QuoteView]{State: api.Ready, Value: v}
			}
		}
		for _, a := range p.Attachments {
			la := api.LocalAttachment{Kind: a.Kind, Path: a.Path, Name: a.Name, Width: a.Width, Height: a.Height}
			if a.Kind == "image" && la.Width <= 0 && la.Path != "" {
				la.Width, la.Height = imageDims(la.Path) // 老记录没存宽高：读回时补量一次
			}
			if a.Hash != "" {
				src := keyOf(a.Conversation)
				la.Media = &api.MediaRef{Source: api.EventRef{Conversation: src, Event: api.EventKey{Platform: src.Platform, EventID: a.Event}}, Hash: a.Hash}
			}
			mp.Attachments = append(mp.Attachments, la)
		}
		out = append(out, mp)
	}
	return out
}
