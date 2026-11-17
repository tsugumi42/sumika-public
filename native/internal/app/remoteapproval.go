package app

// 主人 QQ 私聊审批的装配（规格-主人QQ私聊审批）：默认关，打开后本地发起的审批在主人离开时发到主人 QQ 私聊，回复同意或拒绝就结算。
// 判断都在 approvalrelay 里；这里只接线，并负责“现读配置的绑定”：关着、读不到、主人号对不上、主人号等于某个账号，一律按关处理。
// 群里一律不发、不批：绑定只给主人私聊这一个会话。

import (
	"context"
	"errors"
	"slices"
	"sumika-native/internal/applog"
	"sync/atomic"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/approvalrelay"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
)

// remoteApprovalOrigin 是卡片和结局的来源（出口的自动化来源，模型历史看不到，见 history.OriginRemoteApproval）
const remoteApprovalOrigin = history.OriginRemoteApproval

// remoteSendLimit 是一次发卡等计划终态的上限；超了取消，按没送达办（本机的卡照常等）
const remoteSendLimit = 90 * time.Second

type remoteState struct {
	relay atomic.Pointer[approvalrelay.Relay]
}

// remoteBinding 现读配置，算出此刻有效的绑定。每一个入口（发卡、认回复、结算、迟到回复）都调它，不缓存。
func (a *App) remoteBinding(character string) (approvalrelay.Binding, bool) {
	saved, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil || !saved.QQ.RemoteApproval.Enabled {
		return approvalrelay.Binding{}, false
	}
	owner := string(saved.QQ.Owner)
	// 运行中的名册是启动时那份：配置里的主人改了、重启接收前，旧主人和新主人都不能用
	if owner == "" || !allDigits(owner) || owner != string(a.cfg.QQ.Owner) {
		return approvalrelay.Binding{}, false
	}
	if character == "" || character != a.character || a.account == "" {
		return approvalrelay.Binding{}, false
	}
	bound := false
	for _, acc := range saved.QQ.Accounts {
		if string(acc.ID) == owner {
			return approvalrelay.Binding{}, false // 主人号等于某个账号：自己的号发的话和主人的话分不开
		}
		if string(acc.ID) == a.account && acc.Character == character {
			bound = true
		}
	}
	if !bound || a.account == owner {
		return approvalrelay.Binding{}, false
	}
	return approvalrelay.Binding{
		Away:      time.Duration(saved.QQ.RemoteApproval.AwayAfterSeconds) * time.Second,
		Conv:      types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: owner, Self: a.account},
		Character: character, Owner: owner, Account: a.account,
	}, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// remoteEligible：只有主窗口里显示、本机能点的卡（本地私聊和话题，不含自检）才发 QQ。别的来源的卡不在这里批。
func (a *App) remoteEligible(r approval.Request) bool {
	return localPrivateConv(r.Conversation) && !audience.IsSelftest(r.Conversation) && !audience.IsWhisper(r.Conversation) && r.CharacterID == a.character && characterOfConv(r.Conversation) == r.CharacterID
}

// openRemoteApproval 建投影。在出口、审批都建好之后调；关着的时候它什么也不做（每个入口都现读配置）。
func (a *App) openRemoteApproval() error {
	if a.outlet == nil || a.approvals == nil {
		return nil
	}
	out, err := a.outlet.BindAutomation("remote-approval")
	if err != nil {
		return err
	}
	a.remoteOut = out
	r, err := approvalrelay.New(approvalrelay.Deps{
		Approver: a.approvals,
		Binding:  a.remoteBinding,
		Eligible: a.remoteEligible,
		Away:     func() bool { return a.opt.WindowForeground == nil || !a.opt.WindowForeground() },
		Send:     a.remoteSend,
		Now:      a.now,
		Logf:     a.logfFor(applog.Approval),
		Tick:     a.opt.RemoteApprovalTick,
	})
	if err != nil {
		return err
	}
	a.remote.relay.Store(r)
	r.Start()
	return nil
}

func (a *App) closeRemoteApproval() {
	if r := a.remote.relay.Load(); r != nil {
		r.Close()
	}
}

// remoteSend 把几段文字走唯一出口发到主人私聊，等计划终态。只有全部段都确认才算送达：
// 试运行、部分发送、结果未知、失败、超时都不算。
func (a *App) remoteSend(ctx context.Context, b approvalrelay.Binding, texts []string) (approvalrelay.Delivery, error) {
	if a.remoteOut == nil {
		return approvalrelay.Delivery{}, errors.New("没有自动化出口")
	}
	ctx, cancel := context.WithTimeout(ctx, remoteSendLimit)
	defer cancel()
	parts := make([]types.Part, len(texts))
	for i, t := range texts {
		parts[i] = types.Message(t)
	}
	held, err := a.remoteOut.Submit(ctx, b.Character, b.Conv, parts, nil)
	if err != nil {
		return approvalrelay.Delivery{}, err
	}
	if held.DryRun {
		return approvalrelay.Delivery{}, nil
	}
	select {
	case <-held.Done:
	case <-ctx.Done():
		a.remoteOut.CancelPlan(held.PlanID)
		<-held.Done
		// 取消前可能已有几段发出去了：按计划里的段状态算，不当成“什么都没发”
		return approvalrelay.Delivery{Attempted: a.planAttempted(held.PlanID, b.Conv)}, ctx.Err()
	}
	plan, err := a.outlet.Plan(held.PlanID, b.Conv)
	if err != nil {
		return approvalrelay.Delivery{Attempted: true}, err // 读不到计划：不知道发出去没有
	}
	if plan.State != store.PlanDone || len(plan.Parts) != len(texts) {
		return approvalrelay.Delivery{Attempted: partsAttempted(plan)}, nil
	}
	ids := make([]string, 0, len(plan.Parts))
	for _, p := range plan.Parts {
		if p.State != store.PartConfirmed || !store.HasPlatformID(p.PlatformID) {
			return approvalrelay.Delivery{Attempted: true}, nil
		}
		ids = append(ids, p.PlatformID)
	}
	return approvalrelay.Delivery{Confirmed: true, Attempted: true, MessageIDs: ids}, nil
}

// partsAttempted：计划里是否有段已确认、结果未知或还在发。这样的卡主人手机上可能已经有了，
// 没发出去的明确失败、没发就关掉的才算“什么都没发”。
func partsAttempted(p store.Plan) bool {
	for _, pt := range p.Parts {
		switch pt.State {
		case store.PartConfirmed, store.PartUnknown, store.PartSending:
			return true
		}
	}
	return false
}

func (a *App) planAttempted(planID string, conv types.Conversation) bool {
	p, err := a.outlet.Plan(planID, conv)
	if err != nil {
		return true
	}
	return partsAttempted(p)
}

// remoteMode 是这个来源的出口档位：绑定有效且发到主人私聊才走发送钳制（qqSendMode 的七项），其余只记录。
func (a *App) remoteMode(character string, conv types.Conversation) string {
	b, ok := a.remoteBinding(character)
	if !ok || conv != b.Conv || conv.Channel != "qq" {
		return channel.ModeRecord
	}
	return a.hardQQMode(conv)
}

// remoteControl 接流水线最前面的一道：身份来自入库后的名册判断（按号，不按昵称），不是消息里的任何字。
func (a *App) remoteControl(s channel.Stored) bool {
	r := a.remote.relay.Load()
	if r == nil {
		return false
	}
	in := approvalrelay.Inbound{
		Character:  s.Character,
		Conv:       s.Inbound.Conv,
		Account:    s.Inbound.Account,
		FromID:     s.Inbound.From.ID,
		IsOwner:    s.Actor.Kind == channel.ActorOwner && s.Origin == channel.OriginOwner && s.Observed,
		Message:    s.Inbound.Kind == types.InboundMessage,
		PlatformID: s.Inbound.PlatformID,
		Segs:       s.Inbound.Segs,
		Time:       s.Inbound.Time,
		Seq:        s.Seq,
		Backfill:   slices.Contains(s.Tags, channel.TagBackfill),
		FromSelf:   slices.Contains(s.Tags, channel.TagFromSelf),
		Stranger:   slices.Contains(s.Tags, "stranger"),
		Duplicate:  s.Duplicate,
	}
	if !r.Control(in) {
		return false
	}
	// 被截走的回复行改标来源：界面照常显示，模型历史、记忆都不把它当主人说过的话
	if st, err := a.set.For("qq"); err == nil && s.Seq > 0 {
		if _, err := st.RetagOrigin(s.Inbound.Conv.String(), s.Seq-1, s.Seq, channel.OriginOwner, remoteApprovalOrigin, channel.ActorOwner); err != nil {
			a.logf("审批回复行改标来源失败：%v", err)
		}
	}
	return true
}
