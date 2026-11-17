package app

import (
	"context"
	"encoding/json"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/store"
)

func backfillKey(c types.Conversation) api.ConversationKey {
	return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: c.Scene, Conversation: c.String()}
}
func (a *App) backfillAccount() string {
	if a.opt.LocalOnly {
		return "" // 只开本地：补录页拿空的账号，回“这次没开 QQ”
	}
	if a.account != "" {
		return a.account
	}
	if a.opt.Account != "" {
		return a.opt.Account
	}
	if len(a.cfg.QQ.Accounts) == 1 {
		return string(a.cfg.QQ.Accounts[0].ID)
	}
	return ""
}
func (a *App) backfillAllowed(conv string) bool {
	c, err := types.ParseConversation(conv)
	if err != nil || c.Channel != "qq" {
		return false
	}
	if c.Scene == types.ScenePrivate {
		return c.Self == a.backfillAccount()
	}
	if !a.cfg.QQ.Backfill.Groups {
		return false
	}
	for _, g := range a.cfg.QQ.Groups {
		if string(g) == c.ID {
			return true
		}
	}
	return false
}
func alignmentJob(r *store.AlignmentRecord) (*api.BackfillJob, error) {
	if r == nil {
		return nil, nil
	}
	j := &api.BackfillJob{ID: r.JobID, Scope: r.Scope, Trigger: r.Trigger, State: r.State, StartedMS: r.StartedMS, EndedMS: r.EndedMS, Inserted: r.Inserted, Duplicates: r.Duplicates, Messages: r.Messages, ReasonCode: r.Reason, CompleteCoverage: r.Covered}
	if err := json.Unmarshal([]byte(r.MediaJSON), &j.Media); err != nil {
		return nil, err
	}
	return j, nil
}
func (a *App) alignment(ctx context.Context) (api.AlignmentView, error) {
	account := a.backfillAccount()
	v := api.AlignmentView{Scope: "qq:account:" + config.MaskID(account) + ":eligible", AsOfMS: a.now().UnixMilli(), Discovery: "unsupported", ReasonCode: "unavailable", Media: api.AlignmentMedia{Scope: "connection-7d+round-new", State: "unchecked"}}
	if a.opt.Mode == Archive {
		v.ReasonCode = "readonly"
	}
	if account == "" {
		return v, api.BackfillFailure("unavailable")
	}
	st, err := a.set.For("qq")
	if err != nil {
		return v, err
	}
	active, last, err := st.ReadAlignment(ctx, account)
	if err != nil {
		return v, err
	}
	v.CurrentJob, err = alignmentJob(active)
	if err != nil {
		return v, err
	}
	v.LastAttempt, err = alignmentJob(last)
	if err != nil {
		return v, err
	}
	// 持久记录不是活工人；当前会话的内存任务会在下面覆盖这份历史投影。
	if v.CurrentJob != nil {
		cancelAlignmentMedia(&v.CurrentJob.Media)
	}
	if v.LastAttempt != nil {
		cancelAlignmentMedia(&v.LastAttempt.Media)
	}
	if v.CurrentJob != nil && a.opt.Mode == Archive {
		v.CurrentJob.State = "canceled"
		v.CurrentJob.ReasonCode = "interrupted"
		v.LastAttempt = v.CurrentJob
		v.CurrentJob = nil
	}
	if a.backfill != nil {
		b := a.backfill
		b.tasks.Lock()
		id := b.tasks.current
		if id == "" {
			id = b.tasks.accountJob
		}
		if id == "" && len(b.tasks.queue) > 0 {
			id = b.tasks.queue[0]
		}
		if j, ok := b.tasks.jobs[id]; ok {
			j = cloneJob(j)
			v.CurrentJob = &j
		}
		if j, ok := b.tasks.jobs[b.tasks.last]; ok && v.LastAttempt != nil && j.ID == v.LastAttempt.ID {
			j = cloneJob(j)
			v.LastAttempt = &j
		}
		online := b.tasks.online
		b.tasks.Unlock()
		v.ReasonCode = b.capability()
		if !online {
			v.ReasonCode = "unavailable"
		}
		v.CanRun = v.ReasonCode == ""
	}
	v.RemainingOpen, v.RemainingAbandoned, v.Excluded, err = st.AlignmentGapCounts(ctx, account, a.backfillAllowed)
	if err != nil {
		return v, err
	}
	if v.CurrentJob != nil {
		v.Media = v.CurrentJob.Media
	} else if v.LastAttempt != nil {
		v.Media = v.LastAttempt.Media
	}
	return v, ctx.Err()
}
func (a *App) backfillProvider() desktopread.BackfillProvider {
	p := desktopread.BackfillProvider{Account: a.backfillAccount(), Alignment: a.alignment, Project: func(g store.ChannelGap) api.GapView {
		v := desktopread.GapProjection(g)
		if a.opt.Mode != Archive {
			v.ReasonCode = "unavailable"
		}
		if !a.backfillAllowed(g.Conversation) {
			v.ReasonCode = "excluded"
		}
		return v
	}}
	if a.opt.Mode != Archive {
		p.SetAcknowledged = a.setGapAcknowledged
	}
	if a.opt.Mode != Archive && a.backfill == nil {
		p.RequestGap = func(context.Context, api.GapRef) (api.BackfillJob, error) {
			return api.BackfillJob{}, api.BackfillFailure("unavailable")
		}
		p.RequestAccount = func(context.Context) (api.BackfillJob, error) {
			return api.BackfillJob{}, api.BackfillFailure("unavailable")
		}
	}
	if b := a.backfill; b != nil {
		p.Project = b.projectGap
		p.RequestGap = b.requestGap
		p.RequestAccount = b.requestAccount
		p.Job = b.job
	}
	return p
}

func (a *App) interruptAlignment(ctx context.Context, st *store.Store) error {
	active, _, err := st.ReadAlignment(ctx, a.account)
	if err != nil || active == nil {
		return err
	}
	j, err := alignmentJob(active)
	if err != nil {
		return err
	}
	media := j.Media
	cancelAlignmentMedia(&media)
	now := a.now().UnixMilli()
	media.AsOfMS = now
	raw, err := json.Marshal(media)
	if err != nil {
		return err
	}
	active.MediaJSON = string(raw)
	active.State, active.EndedMS, active.Reason = "canceled", now, "interrupted"
	if active.Messages == "queued" || active.Messages == "running" {
		active.Messages = "canceled"
	}
	return st.SaveAlignment(ctx, a.account, *active)
}
