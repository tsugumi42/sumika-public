package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
	"sumika-native/internal/store"
)

const linkOrigin = "automation:link-reader"

func (a *App) outletMode(character, origin string, conv types.Conversation) string {
	if origin == remoteApprovalOrigin {
		return a.remoteMode(character, conv)
	}
	if origin != linkOrigin {
		return a.modeOf(conv)
	}
	// 只开本地时 a.character 是空串；链接插件给的 character 也可能是空串。空对空不能当成同一个角色
	if a.character == "" || character != a.character || !a.currentLinks().Values.Expand.Allows(conv) {
		return channel.ModeRecord
	}
	if conv.Scene == types.ScenePrivate && conv.Self != a.account {
		return channel.ModeRecord
	}
	return a.hardQQMode(conv)
}
func (a *App) checkLinkParts(character string, conv types.Conversation, parts []types.Part) error {
	if a.outletMode(character, linkOrigin, conv) != channel.ModeSpeak {
		return &linkplugin.Blocked{Why: "发送条件不齐（这个会话现在只记录不发，比如 qq.goSend 没开）", Err: errors.New("展开没有可用发送许可")}
	}
	st, e := a.set.For("qq")
	if e != nil {
		return e
	}
	checked := map[int64]bool{}
	for _, p := range parts {
		if p.SourceSeq <= 0 {
			return errors.New("展开缺少已入库的源消息")
		}
		if checked[p.SourceSeq] {
			continue
		}
		checked[p.SourceSeq] = true
		source, ok, e := st.LinkSource(context.Background(), conv.String(), p.SourceSeq)
		if e != nil {
			return e
		}
		if !ok || !a.roster.CanTrigger("qq", source.ActorID) {
			return &linkplugin.Blocked{Why: "源消息不再允许展开（撤回了或发的人不能触发）", Err: errors.New("源消息不再允许展开")}
		}
		// 群里的发送者名单（linkReader.expand.senders）：按库里源消息的身份再查一次，插件那边漏了这里也拦得住
		if !linkplugin.SenderAllowed(a.currentLinks().Values.Expand, conv, source.ActorKind, source.ActorID) {
			return &linkplugin.Blocked{Why: "发消息的人不在这个群的展开名单里（或在排除名单里）", Err: errors.New("发送者不在展开名单里")}
		}
	}
	return nil
}
func (a *App) admitLink(s channel.Stored) bool {
	// 没有 QQ 单元时 a.account、a.character 都是空串：空对空不成立，不然本地存的消息可能被当成 QQ 的来展开
	if a.account == "" || a.character == "" {
		return false
	}
	if s.Seq <= 0 || s.Inbound.Kind != types.InboundMessage || s.Inbound.Conv.Channel != "qq" || s.Inbound.Account != a.account || s.Character != a.character || !a.roster.CanTrigger("qq", s.Actor.ID) {
		return false
	}
	if s.Actor.Kind != channel.ActorOwner && s.Actor.Kind != channel.ActorHuman {
		return false
	}
	if s.Origin != channel.OriginOwner && s.Origin != channel.OriginPeer {
		return false
	}
	n, e := strconv.ParseInt(s.Inbound.PlatformID, 10, 64)
	return e == nil && n != 0 && strconv.FormatInt(n, 10) == s.Inbound.PlatformID
}
func (a *App) permitLink(s channel.Stored, parts []types.Part) error {
	if !a.admitLink(s) {
		return &linkplugin.Blocked{Why: "不能触发展开（发消息的人或号不对）", Err: errors.New("展开源消息不属于可触发的当前接收消息")}
	}
	if len(parts) == 0 {
		parts = []types.Part{{SourceSeq: s.Seq}}
	}
	return a.checkQQSend(s.Character, linkOrigin, s.Inbound.Conv, parts)
}
func (a *App) prepareLink(ctx context.Context, s channel.Stored, snapshot linkplugin.Snapshot, r *linkreader.Result, draft *linkexpand.Draft) (linkexpand.Prepared, error) {
	frozen := snapshot.Settings.Expand
	// 媒体能力先取适配器声明的（ADR-310 默认能发），再拿掉这个场合里记下明确失败的组合
	direct, forward := a.mediaBlocks.narrow(s.Inbound.Conv.Scene, a.adapter.Caps().Media(s.Inbound.Conv.Scene), a.now())
	p := linkexpand.Preparer{Media: a.linkMedia, Degrade: a.degrade, Cards: a.linkCards, Direct: direct, Forward: forward, ImageMaxBytes: int64(frozen.ImageMaxMB) * 1_000_000, Limits: linkexpand.VideoLimits{Enabled: frozen.Video.Enabled, Timeout: time.Duration(frozen.Video.TimeoutSeconds) * time.Second, Bytes: int64(frozen.Video.MaxMB) * 1_000_000},
		Check: func(ctx context.Context) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := a.permitLink(s, nil); e != nil {
				return e
			}
			return nil
		},
		// 链接展开发的是网页原文、不是她的话，不过记忆守卫（ADR-312）；出口那一道也按来源放过（channel.go 的 UnguardedOrigins）
		Guard: func(context.Context, string) error { return nil },
		// 拆图拼回的判断结果（通过或不拼的原因和各道缝的数），只写数字，不写链接
		Notice: func(line string) { a.logf("%s", line) },
	}
	result, e := p.Prepare(ctx, r, draft)
	if e != nil {
		return result, e
	}
	return result, nil
}

// translateLink 翻译一条帖子正文（规格-链接翻译）。用后台模型、无工具、不带任何会话和人设；
// 失败和超时都只回没有译文的结果，由链接插件发原文。
func (a *App) translateLink(ctx context.Context, cleanURL, text string, ex config.LinkTranslate) linktranslate.Outcome {
	a.linkTransOnce.Do(func() { a.linkTransCache = linktranslate.NewCache(linktranslate.DefaultCacheSize) })
	tr := linktranslate.Translator{
		Call: func(ctx context.Context, system, user string) (string, error) {
			return a.translateCall(ctx, ex.Model, system, user)
		},
		Cache:   a.linkTransCache,
		Model:   a.translateModel(ex.Model),
		Timeout: time.Duration(ex.TimeoutSeconds) * time.Second,
		Retries: ex.Retries,
		Now:     a.now,
	}
	return tr.Translate(ctx, cleanURL, text, ex.Target)
}

// translateModel 是这次翻译用哪个模型：linkReader.expand.translate.model 填了就用它，
// 空串跟后台模型（defaults.backgroundModel，按角色覆盖的照旧）。填的 ID 不在列表里时不另外处理，
// 和后台模型配错时一样走 memoryGenerator 的错误路径（翻译失败，只发原文）。
func (a *App) translateModel(configured string) string {
	if m := strings.TrimSpace(configured); m != "" {
		return m
	}
	return a.backgroundModel(a.character)
}

// translateCall 是翻译用的模型调用：system 放规则，user 放被 <post> 包住的正文；思考档关到最低。
func (a *App) translateCall(ctx context.Context, configured, system, user string) (string, error) {
	if a.opt.LinkTranslateCall != nil {
		return a.opt.LinkTranslateCall(ctx, system, user)
	}
	model := a.translateModel(configured)
	if model == "" {
		return "", linktranslate.ErrNoModel
	}
	if a.opt.LinkTranslateGenerate != nil {
		return a.opt.LinkTranslateGenerate(ctx, model, system, user)
	}
	r, err := memoryGenerator{app: a, character: a.character, kind: kernel.KindLinkTranslate, system: system, thinking: "off"}.Call(ctx, model, user, "link-translate")
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// recallLink 是链接插件的 Recall（规格-链接发送改版 A4）：图被吞时撤回只剩文字的那一条。
// 门槛在插件里判过一次，这里按库里的计划独立再判一次（兜底，防止插件以后被别的路径误用）：
// 这个会话现在仍有展开的发送许可，且这一段是链接展开用发送用的号在这个会话里发的、被回声认领成确认、认领的号就是要撤的号、从第一次调 Send 起不到 2 分钟。
// 任何一条不成立都不调适配器。只有链接插件拿得到它，模型、工具、别的插件都拿不到。
func (a *App) recallLink(ctx context.Context, conv types.Conversation, planID string, idx int, platformID string) error {
	if why := a.recallRefusal(conv, planID, idx, platformID); why != "" {
		a.logf("链接展开：撤回被拒（%s）", why)
		return linkplugin.ErrRecallRefused(why)
	}
	return a.adapter.Recall(ctx, a.account, conv, platformID)
}

// recallRefusal 是撤回门槛不成立的原因；空串是都成立
func (a *App) recallRefusal(conv types.Conversation, planID string, idx int, platformID string) string {
	if a.outlet == nil || a.adapter == nil {
		return "没有能撤回的渠道"
	}
	if conv.Channel != "qq" || conv.Valid() != nil {
		return "不是 QQ 会话"
	}
	if !linkplugin.ValidPlatformID(platformID) {
		return "平台消息号不明"
	}
	if a.outletMode(a.character, linkOrigin, conv) != channel.ModeSpeak {
		// 撤回直接调适配器、不过出口：展开发出后关了 qq.goSend、关了这个会话的展开或 QQ 通道被暂停，这里也得停
		return "这个会话现在没有发送许可"
	}
	pt, why := a.linkPart(conv, planID, idx)
	if why != "" {
		return why
	}
	switch {
	case pt.State != store.PartConfirmed || pt.Via != types.ViaEcho || pt.PlatformID != platformID:
		return "不是这一段刚认领的回声"
	case pt.SentAt <= 0 || a.now().Sub(time.UnixMilli(pt.SentAt)) > linkplugin.RecallWithin:
		return "超过 2 分钟"
	}
	return ""
}

// linkPart 按库里的计划取链接展开发的一段：计划是链接展开用发送用的号、在这个会话里发的，段在计划里。取不到返回原因
func (a *App) linkPart(conv types.Conversation, planID string, idx int) (store.Part, string) {
	plan, err := a.outlet.Plan(planID, conv)
	switch {
	case err != nil:
		return store.Part{}, "读不到这一段的计划"
	case plan.Origin != linkOrigin:
		return store.Part{}, "不是链接展开发的"
	case a.account == "" || plan.Account != a.account:
		return store.Part{}, "不是发送用的号发的"
	case plan.Conversation != conv.String():
		return store.Part{}, "不是这个会话里的"
	case idx < 0 || idx >= len(plan.Parts):
		return store.Part{}, "计划里没有这一段"
	}
	return plan.Parts[idx], ""
}

// recallLinkDup 是链接插件的 RecallDup（规格-链接发送改版 B15）：大图原来那份的回声晚到了，撤回换形式后补发的那一份。
// 和 recallLink 一样不信插件的话，按库里的计划独立再判：原段是链接展开发的、被回声认领成确认、认领的号就是 orig.PlatformID；
// 补发份也是链接展开发的、已确认（回包或回声认下的号都行，补发份多半是回包确认的）、号对得上又不是原段的号、
// 从补发份第一次调 Send 起不到 2 分钟；这个会话现在仍有发送许可。复核后补的两段关系：原段是消息段、出口记的认领是图 md5 强匹配
// （outlet.ClaimedStrong），两段是为同一条源消息发的（outlet.PlanSource，内存表，重启后查不到就拒绝）、补发份的第一次发送晚于原段。
// 任何一条不成立都不调适配器。
// 只有链接插件拿得到它，模型、工具、别的插件都拿不到。
func (a *App) recallLinkDup(ctx context.Context, conv types.Conversation, orig, dup linkplugin.PartRef) error {
	if why := a.recallDupRefusal(conv, orig, dup); why != "" {
		a.logf("链接展开：撤回补发的那份被拒（%s）", why)
		return linkplugin.ErrRecallRefused(why)
	}
	return a.adapter.Recall(ctx, a.account, conv, dup.PlatformID)
}

// recallDupRefusal 是撤回补发份的门槛不成立的原因；空串是都成立
func (a *App) recallDupRefusal(conv types.Conversation, orig, dup linkplugin.PartRef) string {
	if a.outlet == nil || a.adapter == nil {
		return "没有能撤回的渠道"
	}
	if conv.Channel != "qq" || conv.Valid() != nil {
		return "不是 QQ 会话"
	}
	if !linkplugin.ValidPlatformID(dup.PlatformID) || !linkplugin.ValidPlatformID(orig.PlatformID) {
		return "平台消息号不明"
	}
	if dup.PlatformID == orig.PlatformID {
		return "补发的号和原段的回声是同一个"
	}
	if a.outletMode(a.character, linkOrigin, conv) != channel.ModeSpeak {
		return "这个会话现在没有发送许可"
	}
	op, why := a.linkPart(conv, orig.PlanID, orig.Idx)
	if why != "" {
		return "原段：" + why
	}
	if op.State != store.PartConfirmed || op.Via != types.ViaEcho || op.PlatformID != orig.PlatformID {
		return "原段不是被回声认领的"
	}
	dp, why := a.linkPart(conv, dup.PlanID, dup.Idx)
	if why != "" {
		return "补发份：" + why
	}
	switch {
	case dp.State != store.PartConfirmed || dp.PlatformID != dup.PlatformID || (dp.Via != types.ViaEcho && dp.Via != types.ViaReply):
		return "补发份不是确认送到的那一段"
	case dp.SentAt <= 0 || a.now().Sub(time.UnixMilli(dp.SentAt)) > linkplugin.RecallWithin:
		return "超过 2 分钟"
	}
	// 下面是两段的关系（复核问题 1、3）：前面只验了两段各自合规，凑不出“补发份是为这个原段补发的”。
	// 原段的认领必须是强匹配（出口自己记的，不信插件说的）：转发段、只凭形状的认领可能认错，据此撤会把群里的内容撤光
	if op.Kind != types.PartMessage {
		return "原段是转发段，回声认领不比内容，不撤"
	}
	if !a.outlet.ClaimedStrong(orig.PlanID, orig.Idx, orig.PlatformID) {
		return "原段的回声认领不是图的 md5 强匹配（或记录已过期）"
	}
	// 同一条源消息、补发份晚于原段：库里没有源消息，用出口入队时记在内存里的；查不到（重启过、过期）就拒绝
	os, ok1 := a.outlet.PlanSource(orig.PlanID)
	ds, ok2 := a.outlet.PlanSource(dup.PlanID)
	switch {
	case !ok1 || !ok2:
		return "查不到这两段是为哪条源消息发的"
	case os != ds:
		return "两段不是同一条源消息的展开"
	case op.SentAt <= 0 || dp.SentAt <= op.SentAt:
		return "补发份不晚于原段"
	}
	return ""
}
