// Package rpcconv 把 api 的展示类型和协议的线上类型（rpcwire）互相转换（规格-Bun后台第1批 4.2、第五节）。
// 只有这里同时知道两边：api 改了字段，在这里补上，往返测试（conv_test.go）会提醒漏掉的。
//
// 约定：切片空就不发（线上缺省 = 空），收回来是 nil；Result 的错误只上线 {code,message}，收回来是一个普通 error。
package rpcconv

import (
	"errors"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcwire"
)

// ───── 小件 ─────

func KeyOut(k api.ConversationKey) rpcwire.ConversationKey {
	return rpcwire.ConversationKey{Platform: k.Platform, Channel: k.Channel, Scene: k.Scene, Conversation: k.Conversation}
}

func KeyIn(k rpcwire.ConversationKey) api.ConversationKey {
	return api.ConversationKey{Platform: k.Platform, Channel: k.Channel, Scene: k.Scene, Conversation: k.Conversation}
}

func RefOut(r api.EventRef) rpcwire.EventRef {
	return rpcwire.EventRef{Conversation: KeyOut(r.Conversation), Event: rpcwire.EventKey{Platform: r.Event.Platform, EventID: r.Event.EventID}}
}

func RefIn(r rpcwire.EventRef) api.EventRef {
	return api.EventRef{Conversation: KeyIn(r.Conversation), Event: api.EventKey{Platform: r.Event.Platform, EventID: r.Event.EventID}}
}

func refPtrOut(r *api.EventRef) *rpcwire.EventRef {
	if r == nil {
		return nil
	}
	v := RefOut(*r)
	return &v
}

func refPtrIn(r *rpcwire.EventRef) *api.EventRef {
	if r == nil {
		return nil
	}
	v := RefIn(*r)
	return &v
}

func strs(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func mapSlice[A, B any](in []A, f func(A) B) []B {
	if len(in) == 0 {
		return nil
	}
	out := make([]B, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// ───── Result、List ─────

// ResultOut 转一个 Result；Err 只留 {state 当 code, 文字}
func ResultOut[A, B any](r api.Result[A], f func(A) B) rpcwire.Result[B] {
	out := rpcwire.Result[B]{State: string(r.State), Stale: r.Stale}
	if out.State == "" {
		out.State = rpcwire.StateLoading
	}
	if r.State == api.Ready || r.Stale {
		v := f(r.Value)
		out.Value = &v
	}
	if r.Err != nil {
		out.Error = &rpcwire.ResultError{Code: string(r.State), Message: r.Err.Error()}
	}
	return out
}

func ResultIn[A, B any](r rpcwire.Result[A], f func(A) B) api.Result[B] {
	out := api.Result[B]{State: api.State(r.State), Stale: r.Stale}
	if r.Value != nil {
		out.Value = f(*r.Value)
	}
	if r.Error != nil {
		out.Err = errors.New(r.Error.Message)
	}
	return out
}

func listOut[A, B any](l api.List[A], f func(A) B) rpcwire.List[B] {
	return rpcwire.List[B]{Items: mapSlice(l.Items, f), NextCursor: string(l.NextCursor), HasMore: l.HasMore}
}

func listIn[A, B any](l rpcwire.List[A], f func(A) B) api.List[B] {
	return api.List[B]{Items: mapSlice(l.Items, f), NextCursor: api.Cursor(l.NextCursor), HasMore: l.HasMore}
}

// ───── 运行状态、角色、概览 ─────

func RuntimeOut(v api.RuntimeView) rpcwire.RuntimeView {
	return rpcwire.RuntimeView{Mode: v.Mode, Phase: v.Phase, Account: v.Account, Connection: v.Connection, ReasonCode: v.ReasonCode, Reason: v.Reason,
		Alerts: listOut(v.Alerts, func(a api.RuntimeAlert) rpcwire.RuntimeAlert {
			return rpcwire.RuntimeAlert{Key: a.Key, Title: a.Title, Body: a.Body}
		})}
}

func RuntimeIn(v rpcwire.RuntimeView) api.RuntimeView {
	return api.RuntimeView{Mode: v.Mode, Phase: v.Phase, Account: v.Account, Connection: v.Connection, ReasonCode: v.ReasonCode, Reason: v.Reason,
		Alerts: listIn(v.Alerts, func(a rpcwire.RuntimeAlert) api.RuntimeAlert {
			return api.RuntimeAlert{Key: a.Key, Title: a.Title, Body: a.Body}
		})}
}

func characterOut(c api.CharacterView) rpcwire.CharacterView {
	return rpcwire.CharacterView{ID: c.ID, Name: c.Name, Color: c.Color, Model: c.Model, Status: c.Status}
}

func characterIn(c rpcwire.CharacterView) api.CharacterView {
	return api.CharacterView{ID: c.ID, Name: c.Name, Color: c.Color, Model: c.Model, Status: c.Status}
}

func ConversationOut(c api.ConversationView) rpcwire.ConversationView {
	out := rpcwire.ConversationView{Key: KeyOut(c.Key), Title: c.Title, Observers: strs(c.Observers), CreatedAt: c.CreatedAt,
		Unread: c.Unread, HasUnread: c.HasUnread, WorkspaceCWD: c.WorkspaceCWD, WorkspaceRoot: c.WorkspaceRoot}
	if c.Last != nil {
		m := MessageOut(*c.Last)
		out.Last = &m
	}
	return out
}

func ConversationIn(c rpcwire.ConversationView) api.ConversationView {
	out := api.ConversationView{Key: KeyIn(c.Key), Title: c.Title, Observers: strs(c.Observers), CreatedAt: c.CreatedAt,
		Unread: c.Unread, HasUnread: c.HasUnread, WorkspaceCWD: c.WorkspaceCWD, WorkspaceRoot: c.WorkspaceRoot}
	if c.Last != nil {
		m := MessageIn(*c.Last)
		out.Last = &m
	}
	return out
}

func OverviewRequestOut(r api.OverviewRequest) rpcwire.OverviewRequest {
	return rpcwire.OverviewRequest{Cursor: string(r.Cursor), Limit: r.Limit, Character: r.Character}
}

func OverviewRequestIn(r rpcwire.OverviewRequest) api.OverviewRequest {
	return api.OverviewRequest{Cursor: api.Cursor(r.Cursor), Limit: r.Limit, Character: r.Character}
}

func OverviewOut(o api.Overview) rpcwire.Overview {
	return rpcwire.Overview{
		Characters: listOut(o.Characters, func(r api.Result[api.CharacterView]) rpcwire.Result[rpcwire.CharacterView] {
			return ResultOut(r, characterOut)
		}),
		Conversations: listOut(o.Conversations, ConversationOut),
		Capabilities: mapSlice(o.Capabilities, func(c api.Capability) rpcwire.Capability {
			return rpcwire.Capability{Name: c.Name, State: string(c.State), Reason: c.Reason}
		}),
		Runtime: RuntimeOut(o.Runtime),
	}
}

func OverviewIn(o rpcwire.Overview) api.Overview {
	return api.Overview{
		Characters: listIn(o.Characters, func(r rpcwire.Result[rpcwire.CharacterView]) api.Result[api.CharacterView] {
			return ResultIn(r, characterIn)
		}),
		Conversations: listIn(o.Conversations, ConversationIn),
		Capabilities: mapSlice(o.Capabilities, func(c rpcwire.Capability) api.Capability {
			return api.Capability{Name: c.Name, State: api.State(c.State), Reason: c.Reason}
		}),
		Runtime: RuntimeIn(o.Runtime),
	}
}

// ───── 消息 ─────

func MediaRefOut(m api.MediaRef) rpcwire.MediaRef {
	return rpcwire.MediaRef{Source: RefOut(m.Source), Hash: m.Hash, Forward: strs(m.Forward)}
}

func MediaRefIn(m rpcwire.MediaRef) api.MediaRef {
	return api.MediaRef{Source: RefIn(m.Source), Hash: m.Hash, Forward: strs(m.Forward)}
}

func mediaPtrOut(m *api.MediaRef) *rpcwire.MediaRef {
	if m == nil {
		return nil
	}
	v := MediaRefOut(*m)
	return &v
}

func mediaPtrIn(m *rpcwire.MediaRef) *api.MediaRef {
	if m == nil {
		return nil
	}
	v := MediaRefIn(*m)
	return &v
}

func segmentOut(s api.DisplaySegment) rpcwire.DisplaySegment {
	out := rpcwire.DisplaySegment{Kind: s.Kind, Text: s.Text, Target: s.Target, Ref: s.Ref, Media: mediaPtrOut(s.Media), Cover: mediaPtrOut(s.Cover),
		DurationMS: s.DurationMS, FetchState: s.FetchState, FileKind: s.FileKind, Width: s.Width, Height: s.Height, Animated: s.Animated,
		Sticker: s.Sticker, Summary: s.Summary, Name: s.Name, Size: s.Size, Links: strs(s.Links), MediaState: s.MediaState, Issue: s.Issue}
	if s.Download != nil {
		out.Download = &rpcwire.VideoDownloadRef{Source: RefOut(s.Download.Source), Index: s.Download.Index, Forward: strs(s.Download.Forward), Node: s.Download.Node}
	}
	if s.Card != nil {
		out.Card = &rpcwire.CardView{Title: s.Card.Title, Description: s.Card.Description, Source: s.Card.Source, URL: s.Card.URL}
	}
	if s.Forward != nil {
		f := s.Forward
		lines := f.Lines[:]
		for len(lines) > 0 && lines[len(lines)-1] == "" { // 三行定长数组：尾部空行不上线
			lines = lines[:len(lines)-1]
		}
		out.Forward = &rpcwire.ForwardBrief{Title: f.Title, Lines: strs(lines), Count: f.Count, Missing: f.Missing, State: f.State}
	}
	return out
}

func segmentIn(s rpcwire.DisplaySegment) api.DisplaySegment {
	out := api.DisplaySegment{Kind: s.Kind, Text: s.Text, Target: s.Target, Ref: s.Ref, Media: mediaPtrIn(s.Media), Cover: mediaPtrIn(s.Cover),
		DurationMS: s.DurationMS, FetchState: s.FetchState, FileKind: s.FileKind, Width: s.Width, Height: s.Height, Animated: s.Animated,
		Sticker: s.Sticker, Summary: s.Summary, Name: s.Name, Size: s.Size, Links: strs(s.Links), MediaState: s.MediaState, Issue: s.Issue}
	if s.Download != nil {
		out.Download = &api.VideoDownloadRef{Source: RefIn(s.Download.Source), Index: s.Download.Index, Forward: strs(s.Download.Forward), Node: s.Download.Node}
	}
	if s.Card != nil {
		out.Card = &api.CardView{Title: s.Card.Title, Description: s.Card.Description, Source: s.Card.Source, URL: s.Card.URL}
	}
	if s.Forward != nil {
		f := s.Forward
		b := &api.ForwardBrief{Title: f.Title, Count: f.Count, Missing: f.Missing, State: f.State}
		copy(b.Lines[:], f.Lines)
		out.Forward = b
	}
	return out
}

func quoteOut(q api.QuoteView) rpcwire.QuoteView {
	return rpcwire.QuoteView{Ref: RefOut(q.Ref), ActorKind: q.ActorKind, DisplayName: q.DisplayName, Text: q.Text, Recalled: q.Recalled,
		Segments: mapSlice(q.Segments, segmentOut), Snippet: q.Snippet, SnippetStart: q.SnippetStart, SnippetEnd: q.SnippetEnd,
		SnippetWhole: q.SnippetWhole, SnippetFallback: q.SnippetFallback, SnippetTruncated: q.SnippetTruncated}
}

func quoteIn(q rpcwire.QuoteView) api.QuoteView {
	return api.QuoteView{Ref: RefIn(q.Ref), ActorKind: q.ActorKind, DisplayName: q.DisplayName, Text: q.Text, Recalled: q.Recalled,
		Segments: mapSlice(q.Segments, segmentIn), Snippet: q.Snippet, SnippetStart: q.SnippetStart, SnippetEnd: q.SnippetEnd,
		SnippetWhole: q.SnippetWhole, SnippetFallback: q.SnippetFallback, SnippetTruncated: q.SnippetTruncated}
}

// quoteResultOut：State 为空表示没有引用，不上线
func quoteResultOut(r api.Result[api.QuoteView]) *rpcwire.Result[rpcwire.QuoteView] {
	if r.State == "" {
		return nil
	}
	v := ResultOut(r, quoteOut)
	return &v
}

func quoteResultIn(r *rpcwire.Result[rpcwire.QuoteView]) api.Result[api.QuoteView] {
	if r == nil {
		return api.Result[api.QuoteView]{}
	}
	return ResultIn(*r, quoteIn)
}

func recallOut(r api.RecallItem) rpcwire.RecallItem {
	return rpcwire.RecallItem{Text: r.Text, Kind: r.Kind, Source: r.Source, CreatedAt: r.CreatedAt, Pinned: r.Pinned, Hearsay: r.Hearsay,
		Score: r.Score, Rerank: r.Rerank, Sim: r.Sim, Lexical: r.Lexical, Vector: r.Vector,
		Previous: mapSlice(r.Previous, func(p api.RecallPrior) rpcwire.RecallPrior {
			return rpcwire.RecallPrior{Text: p.Text, CreatedAt: p.CreatedAt}
		})}
}

func recallIn(r rpcwire.RecallItem) api.RecallItem {
	return api.RecallItem{Text: r.Text, Kind: r.Kind, Source: r.Source, CreatedAt: r.CreatedAt, Pinned: r.Pinned, Hearsay: r.Hearsay,
		Score: r.Score, Rerank: r.Rerank, Sim: r.Sim, Lexical: r.Lexical, Vector: r.Vector,
		Previous: mapSlice(r.Previous, func(p rpcwire.RecallPrior) api.RecallPrior {
			return api.RecallPrior{Text: p.Text, CreatedAt: p.CreatedAt}
		})}
}

func stepOut(s api.LocalProcessStep) rpcwire.LocalProcessStep {
	return rpcwire.LocalProcessStep{ID: s.ID, Kind: s.Kind, Tool: s.Tool, Target: s.Target, Thought: s.Thought, Output: s.Output, State: s.State,
		Rule: s.Rule, Step: s.Step, Seconds: s.Seconds, Started: s.Started, Recall: mapSlice(s.Recall, recallOut)}
}

func stepIn(s rpcwire.LocalProcessStep) api.LocalProcessStep {
	return api.LocalProcessStep{ID: s.ID, Kind: s.Kind, Tool: s.Tool, Target: s.Target, Thought: s.Thought, Output: s.Output, State: s.State,
		Rule: s.Rule, Step: s.Step, Seconds: s.Seconds, Started: s.Started, Recall: mapSlice(s.Recall, recallIn)}
}

func attachmentOut(a api.LocalAttachment) rpcwire.LocalAttachment {
	return rpcwire.LocalAttachment{Kind: a.Kind, Media: mediaPtrOut(a.Media), Path: a.Path, Name: a.Name, Width: a.Width, Height: a.Height}
}

func attachmentIn(a rpcwire.LocalAttachment) api.LocalAttachment {
	return api.LocalAttachment{Kind: a.Kind, Media: mediaPtrIn(a.Media), Path: a.Path, Name: a.Name, Width: a.Width, Height: a.Height}
}

func partOut(p api.MessagePart) rpcwire.MessagePart {
	return rpcwire.MessagePart{HasQuote: p.HasQuote, Ref: refPtrOut(p.Ref), Quote: quoteResultOut(p.Quote), Text: p.Text, Attachments: mapSlice(p.Attachments, attachmentOut)}
}

func partIn(p rpcwire.MessagePart) api.MessagePart {
	return api.MessagePart{HasQuote: p.HasQuote, Ref: refPtrIn(p.Ref), Quote: quoteResultIn(p.Quote), Text: p.Text, Attachments: mapSlice(p.Attachments, attachmentIn)}
}

func reactionOut(r api.ReactionView) rpcwire.ReactionView {
	return rpcwire.ReactionView{ID: r.ID, Ref: r.Ref, ActorKind: r.ActorKind, ActorID: r.ActorID, DisplayName: r.DisplayName, At: r.At}
}

func reactionIn(r rpcwire.ReactionView) api.ReactionView {
	return api.ReactionView{ID: r.ID, Ref: r.Ref, ActorKind: r.ActorKind, ActorID: r.ActorID, DisplayName: r.DisplayName, At: r.At}
}

func MessageOut(m api.MessageView) rpcwire.MessageView {
	out := rpcwire.MessageView{Ref: RefOut(m.Ref), Version: int64(m.Version), Seq: m.Seq, ActorKind: m.ActorKind, ActorID: m.ActorID, ActorUID: m.ActorUID,
		DisplayName: m.DisplayName, Kind: m.Kind, Process: mapSlice(m.Process, stepOut), Service: m.Service, Origin: m.Origin, CreatedAt: m.CreatedAt,
		Text: m.Text, Segments: mapSlice(m.Segments, segmentOut), Unsent: m.Unsent, Partial: m.Partial, Aborted: m.Aborted, Recalled: m.Recalled,
		ReplyTo: refPtrOut(m.ReplyTo), Reply: quoteResultOut(m.Reply), Reactions: mapSlice(m.Reactions, reactionOut), Parts: mapSlice(m.Parts, partOut)}
	if m.Proactive != nil {
		out.Proactive = &rpcwire.ProactiveMark{Thread: m.Proactive.Thread, Reason: m.Proactive.Reason}
	}
	return out
}

func MessageIn(m rpcwire.MessageView) api.MessageView {
	out := api.MessageView{Ref: RefIn(m.Ref), Version: uint64(m.Version), Seq: m.Seq, ActorKind: m.ActorKind, ActorID: m.ActorID, ActorUID: m.ActorUID,
		DisplayName: m.DisplayName, Kind: m.Kind, Process: mapSlice(m.Process, stepIn), Service: m.Service, Origin: m.Origin, CreatedAt: m.CreatedAt,
		Text: m.Text, Segments: mapSlice(m.Segments, segmentIn), Unsent: m.Unsent, Partial: m.Partial, Aborted: m.Aborted, Recalled: m.Recalled,
		ReplyTo: refPtrIn(m.ReplyTo), Reply: quoteResultIn(m.Reply), Reactions: mapSlice(m.Reactions, reactionIn), Parts: mapSlice(m.Parts, partIn)}
	if m.Proactive != nil {
		out.Proactive = &api.ProactiveMark{Thread: m.Proactive.Thread, Reason: m.Proactive.Reason}
	}
	return out
}

func PageOut(p api.MessagePage) rpcwire.MessagePage {
	out := rpcwire.MessagePage{Items: mapSlice(p.Items, MessageOut), NextCursor: string(p.NextCursor), HasMore: p.HasMore, HighWater: p.HighWater}
	if p.Before != nil {
		b := MessageOut(*p.Before)
		out.Before = &b
	}
	return out
}

func PageIn(p rpcwire.MessagePage) api.MessagePage {
	out := api.MessagePage{List: api.List[api.MessageView]{Items: mapSlice(p.Items, MessageIn), NextCursor: api.Cursor(p.NextCursor), HasMore: p.HasMore}, HighWater: p.HighWater}
	if p.Before != nil {
		b := MessageIn(*p.Before)
		out.Before = &b
	}
	return out
}

func ConversationRequestOut(r api.ConversationRequest) rpcwire.ConversationRequest {
	return rpcwire.ConversationRequest{Key: KeyOut(r.Key), Character: r.Character, Limit: r.Limit}
}

func ConversationRequestIn(r rpcwire.ConversationRequest) api.ConversationRequest {
	return api.ConversationRequest{Key: KeyIn(r.Key), Character: r.Character, Limit: r.Limit}
}

func SnapshotOut(s api.ConversationSnapshot) rpcwire.ConversationSnapshot {
	return rpcwire.ConversationSnapshot{Conversation: ConversationOut(s.Conversation), Page: PageOut(s.Page), Changed: mapSlice(s.Changed, MessageOut)}
}

func SnapshotIn(s rpcwire.ConversationSnapshot) api.ConversationSnapshot {
	return api.ConversationSnapshot{Conversation: ConversationIn(s.Conversation), Page: PageIn(s.Page), Changed: mapSlice(s.Changed, MessageIn)}
}

// ───── 媒体、小黄脸、设置 ─────

func MediaOut(m api.MediaView) rpcwire.MediaView {
	return rpcwire.MediaView{Bytes: m.Bytes, MIME: m.MIME, Size: m.Size}
}
func MediaIn(m rpcwire.MediaView) api.MediaView {
	return api.MediaView{Bytes: m.Bytes, MIME: m.MIME, Size: m.Size}
}

func FacesOut(l api.List[api.FaceView]) rpcwire.List[rpcwire.FaceView] {
	return listOut(l, func(f api.FaceView) rpcwire.FaceView {
		return rpcwire.FaceView{ID: f.ID, Name: f.Name, Kind: f.Kind, Char: f.Char}
	})
}

func FacesIn(l rpcwire.List[rpcwire.FaceView]) api.List[api.FaceView] {
	return listIn(l, func(f rpcwire.FaceView) api.FaceView {
		return api.FaceView{ID: f.ID, Name: f.Name, Kind: f.Kind, Char: f.Char}
	})
}

func settingOut(s api.Setting) rpcwire.Setting {
	return rpcwire.Setting{Key: s.Key, Value: s.Value, Source: s.Source, Configured: s.Configured, Effective: s.Effective, EffectiveValue: s.EffectiveValue,
		Supported: s.Supported, Name: s.Name, Help: s.Help, Unit: s.Unit, Choices: s.Choices}
}

func settingIn(s rpcwire.Setting) api.Setting {
	return api.Setting{Key: s.Key, Value: s.Value, Source: s.Source, Configured: s.Configured, Effective: s.Effective, EffectiveValue: s.EffectiveValue,
		Supported: s.Supported, Name: s.Name, Help: s.Help, Unit: s.Unit, Choices: s.Choices}
}

func SettingsOut(s api.Settings) rpcwire.Settings {
	return rpcwire.Settings{Items: listOut(s.Items, func(r api.Result[api.Setting]) rpcwire.Result[rpcwire.Setting] { return ResultOut(r, settingOut) })}
}

func SettingsIn(s rpcwire.Settings) api.Settings {
	return api.Settings{Items: listIn(s.Items, func(r rpcwire.Result[rpcwire.Setting]) api.Result[api.Setting] { return ResultIn(r, settingIn) })}
}

// Same 是给 ResultOut/ResultIn 用的恒等函数
func Same[T any](v T) T { return v }
