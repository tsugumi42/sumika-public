package main

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/store"
)

// op 仅保护取得当前接口及接受任务，不等历史网络请求或媒体下载。
func (b *ctlBackend) withBackfill(fn func(api.GapReader, api.BackfillActions, string) error) error {
	h, err := b.host()
	if err != nil {
		return err
	}
	h.op.Lock()
	defer h.op.Unlock()
	if h.desk == nil {
		return ctlproto.Errorf(ctlproto.CodeFailed, "后台未就绪")
	}
	return fn(&h.desk.Reader, &h.desk.Reader, h.account)
}
func backfillError(err error) error {
	if err == nil {
		return nil
	}
	code, reason := ctlproto.CodeFailed, "io"
	var be *api.BackfillError
	if errors.As(err, &be) {
		reason = be.Code
		switch reason {
		case "readonly":
			code = ctlproto.CodeReadOnly
		case "invalid":
			code = ctlproto.CodeInvalid
		}
	}
	if errors.Is(err, store.ErrDisplayParameter) || errors.Is(err, store.ErrDisplayScope) {
		code, reason = ctlproto.CodeInvalid, "invalid"
	}
	return &ctlproto.Error{Code: code, Msg: "补录请求未完成（" + reason + "）", Data: ctlproto.BackfillFailure{ReasonCode: reason}}
}
func ctlMedia(v api.AlignmentMedia) ctlproto.BackfillMedia {
	return ctlproto.BackfillMedia{Scope: v.Scope, AsOfMS: v.AsOfMS, State: v.State, Queued: v.Queued, Running: v.Running, Downloaded: v.Downloaded, Expired: v.Expired, Failed: v.Failed, AwaitingManual: v.AwaitingManual, Skipped: v.Skipped, Unchecked: v.Unchecked}
}
func ctlJob(j *api.BackfillJob) *ctlproto.BackfillJob {
	if j == nil {
		return nil
	}
	masked := maskDigits(j.CurrentConversation.Conversation)
	return &ctlproto.BackfillJob{ID: j.ID, Scope: j.Scope, Trigger: j.Trigger, State: j.State, StartedMS: j.StartedMS, EndedMS: j.EndedMS, MaskedConversation: masked, Pages: j.Pages, Inserted: j.Inserted, Duplicates: j.Duplicates, Checked: j.Checked, Pending: j.Pending, Messages: j.Messages, CompleteCoverage: j.CompleteCoverage, Media: ctlMedia(j.Media), ReasonCode: j.ReasonCode}
}
func (b *ctlBackend) BackfillStatus(ctx context.Context) (out ctlproto.BackfillStatus, err error) {
	err = b.withBackfill(func(r api.GapReader, _ api.BackfillActions, _ string) error {
		sub, e := r.WatchAlignment(ctx)
		if e != nil {
			return e
		}
		defer sub.Close()
		select {
		case s, ok := <-sub.Updates():
			if !ok {
				return api.BackfillFailure("unavailable")
			}
			if s.Err != nil {
				return s.Err
			}
			v := s.Value
			out = ctlproto.BackfillStatus{Scope: v.Scope, AsOfMS: v.AsOfMS, LastAttempt: ctlJob(v.LastAttempt), CurrentJob: ctlJob(v.CurrentJob), RemainingOpen: v.RemainingOpen, RemainingAbandoned: v.RemainingAbandoned, Excluded: v.Excluded, Media: ctlMedia(v.Media), CanRun: v.CanRun, ReasonCode: v.ReasonCode, Discovery: v.Discovery}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return out, backfillError(err)
}
func (b *ctlBackend) BackfillGaps(ctx context.Context, cursor string, n int) (out ctlserver.BackfillPage, err error) {
	err = b.withBackfill(func(r api.GapReader, _ api.BackfillActions, account string) error {
		v, e := r.ReadAlignmentGaps(ctx, api.Cursor(cursor), n)
		if e != nil {
			return e
		}
		out = ctlserver.BackfillPage{Items: []ctlserver.BackfillRow{}, NextCursor: string(v.Value.NextCursor), HasMore: v.Value.HasMore, AsOfMS: time.Now().UnixMilli(), Scope: "qq:account:" + config.MaskID(account) + ":readable"}
		for _, g := range v.Value.Items {
			out.Items = append(out.Items, ctlserver.BackfillRow{Ref: ctlserver.BackfillRef{Platform: g.ConversationKey.Platform, Account: account, Conversation: g.ConversationKey.Conversation, ID: g.ID, FromMS: g.FromMS, ToMS: g.ToMS}, View: ctlproto.BackfillGap{State: g.State, Reason: g.Reason, CanRetry: g.CanRetry, RetryAfterMS: g.RetryAfterMS, JobID: g.JobID}})
		}
		return nil
	})
	return out, backfillError(err)
}
func (b *ctlBackend) BackfillRetry(ctx context.Context, ref ctlserver.BackfillRef) (out ctlproto.BackfillAccepted, err error) {
	err = b.withBackfill(func(_ api.GapReader, a api.BackfillActions, account string) error {
		if ref.Account != account || ref.Platform != "qq" {
			return api.BackfillFailure("invalid")
		}
		scene := "group"
		if len(ref.Conversation) > 11 && ref.Conversation[:11] == "qq:private:" {
			scene = "private"
		}
		j, e := a.RequestGapBackfill(ctx, api.GapRef{ConversationKey: api.ConversationKey{Platform: "qq", Channel: "qq", Scene: scene, Conversation: ref.Conversation}, ID: ref.ID, FromMS: ref.FromMS, ToMS: ref.ToMS})
		if e != nil {
			return e
		}
		out = ctlAccepted(j)
		return nil
	})
	return out, backfillError(err)
}
func (b *ctlBackend) BackfillRun(ctx context.Context) (out ctlproto.BackfillAccepted, err error) {
	err = b.withBackfill(func(_ api.GapReader, a api.BackfillActions, _ string) error {
		j, e := a.RequestAlignment(ctx)
		if e != nil {
			return e
		}
		out = ctlAccepted(j)
		return nil
	})
	return out, backfillError(err)
}
func ctlAccepted(j api.BackfillJob) ctlproto.BackfillAccepted {
	return ctlproto.BackfillAccepted{Accepted: true, JobID: j.ID, Scope: j.Scope, State: j.State}
}

var _ ctlserver.BackfillBackend = (*ctlBackend)(nil)
