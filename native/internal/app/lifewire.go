package app

// 日历、天气两个工具的装配（规格-主动联系 第十九节，lifetools）。
// 重点是"不传地点时能拿到所在地的哪一级"（ADR-330 第 6 条）：主人本人的场合看完整的；主人开放的群（本地群聊、QQ 群）只知道城市；
// 别人的 QQ 私聊当不知道。线（主动联系）算主人本人，接 QQ 群时要重审（规格第十九节）。
// 身份按 ActorKind、ActorID 认，不按名字、不按 PrivateOK（本地群聊、复读、子代理的 PrivateOK 也为真，不能当主人在场）。

import (
	"context"
	"slices"
	"strings"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
	"sumika-native/internal/kernel"
	"sumika-native/internal/lifetools"
)

func (a *App) openLifeTools() error {
	p := lifetools.New(lifetools.Options{
		CacheDir:         a.paths.HolidayCacheDir(),
		FreeAPIStateFile: a.paths.FreeAPIStateFile(),
		Settings:         a.lifeSettings,
		DefaultPlace:     a.lifeDefaultPlace,
		QuotaOwner:       a.lifeQuotaOwner,
		Now:              a.now,
		Logf:             a.logfFor(applog.Lifetools),
		Client:           a.opt.LifeClient,
		GeocodeURL:       a.opt.LifeGeocodeURL,
		ForecastURL:      a.opt.LifeForecastURL,
	})
	a.life = p
	return a.mountPlugin(p, true)
}

// FreeAPI 是免费接口引擎（设置页和 sumika-ctl 用）；工具没装配时是 nil
func (a *App) FreeAPI() *freeapi.Engine {
	if a.life == nil {
		return nil
	}
	return a.life.Engine()
}

// lifeSettings 每次调用时重读配置文件，所在地、免费接口的设置改了马上生效；读不出来就用启动时的那份，并标 Stale（群里当不知道）
func (a *App) lifeSettings() lifetools.Settings {
	c := a.cfg
	stale := false
	if fresh, err := config.LoadIn(a.root, a.opt.DataDir); err == nil {
		c = fresh
	} else {
		stale = true
	}
	return lifetools.Settings{Location: c.Location, HolidayURL: c.Calendar.HolidayURL, Proxy: c.LinkReader.Proxy, FreeAPI: lifeFreeAPISettings(c.FreeAPI), Stale: stale}
}

// lifeFreeAPISettings 把配置里的 freeApi 一节换成引擎要的设置
func lifeFreeAPISettings(f config.FreeAPI) freeapi.Settings {
	lim := func(l config.FreeAPILimit) freeapi.Limit {
		return freeapi.Limit{PerActorDaily: l.PerActorDaily, PerConversationHourly: l.PerConversationHourly}
	}
	s := freeapi.Settings{
		NetworkRegion: f.NetworkRegion,
		ProbeEnabled:  f.Probe.Enabled,
		ProbeInterval: time.Duration(f.Probe.IntervalHours) * time.Hour,
		Limits: map[string]freeapi.Limit{
			lifetools.ToolWeather: lim(f.Limits.Weather), lifetools.ToolExchangeRate: lim(f.Limits.ExchangeRate), lifetools.ToolCalendar: lim(f.Limits.Calendar),
		},
		Providers: map[string]freeapi.ProviderCap{},
	}
	for id, c := range f.Providers {
		s.Providers[id] = freeapi.ProviderCap{DailyCap: c.DailyCap, PerMinute: c.PerMinute}
	}
	return s
}

// lifeQuotaOwner 判这一次调用是不是主人本人（每人每天、每群每小时的次数不计）：先确认调用确实是宿主正在跑的那一次，
// 前台、没有父运行（子代理不算），身份是 owner/owner（本地和 QQ 都是，群里主人说话也是）；QQ 还要看现在配置里的主人号和账号绑定；
// 主动联系的线和提醒到点那一轮（只发给主人）算主人在场。身份缺失、种类不对都不是主人
func (a *App) lifeQuotaOwner(ctx context.Context, in kernel.Invocation, aud audience.Audience) bool {
	if a.hub == nil || a.hub.CheckInvocation(ctx, in) != nil {
		return false
	}
	if lifeThreadShape(in, aud) || reminderRunShape(in, aud) {
		return true
	}
	s := in.Scope
	if in.Kind != kernel.KindForeground || in.ParentRunID != "" || s.ActorKind != "owner" || s.ActorID != "owner" || s.CharacterID == "" || s.Conversation != aud.Conversation {
		return false
	}
	if s.Channel != "qq" {
		return true
	}
	if !a.lifeQQBindingCurrent(in) {
		return false
	}
	// 别人的 QQ 私聊里就算身份写着 owner 也不认：私聊对面必须就是配置里现在的主人号
	conv, err := types.ParseConversation(s.Conversation)
	return err == nil && (conv.Scene != types.ScenePrivate || conv.ID == string(a.cfg.QQ.Owner))
}

// lifeDefaultPlace 判这一次调用能拿到所在地的哪一级：先确认调用确实是宿主正在跑的那一次，再看身份形状。
// 主人本人，或提醒到点那一轮（那句话只发给主人，“早上告诉我天气”要用）拿完整的；主人开放的群只拿城市
func (a *App) lifeDefaultPlace(ctx context.Context, in kernel.Invocation, aud audience.Audience) lifetools.PlaceGrant {
	if a.ownerListener(ctx, in, aud) {
		return lifetools.PlaceFull
	}
	if a.hub == nil || a.hub.CheckInvocation(ctx, in) != nil {
		return lifetools.PlaceNone
	}
	if reminderRunShape(in, aud) {
		return lifetools.PlaceFull
	}
	if a.lifeOpenGroup(in, aud) {
		return lifetools.PlaceCity
	}
	return lifetools.PlaceNone
}

// ownerListener 判这一次调用的听众是不是主人：先确认调用确实是宿主正在跑的那一次，再看身份形状。
// 所在地（lifetools）和提醒（remindwire.go）共用这一处定义
func (a *App) ownerListener(ctx context.Context, in kernel.Invocation, aud audience.Audience) bool {
	if a.hub == nil || a.hub.CheckInvocation(ctx, in) != nil {
		return false
	}
	if !a.lifeOwnerShape(in, aud) {
		return false
	}
	return in.Scope.Channel != "qq" || a.lifeQQBindingCurrent(in)
}

// lifeQQBindingCurrent：QQ 私聊的"主人"以配置现在写的为准。qq.owner 在设置里可改、重启接收才生效，
// 启动时那份快照里的旧主人不能再拿到所在地；读配置失败、账号到角色的绑定没了都按不给处理（写法同 inspectClient）
func (a *App) lifeQQBindingCurrent(in kernel.Invocation) bool {
	saved, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil || saved.QQ.Owner != a.cfg.QQ.Owner {
		return false
	}
	return slices.ContainsFunc(saved.QQ.Accounts, func(x config.QQAccount) bool {
		return string(x.ID) == a.account && x.Character == in.Scope.CharacterID
	})
}

// lifeOwnerShape 是"听众是主人"的身份形状：本地私聊的主人、QQ 私聊的主人（沿用客户端自知的判法，
// 核账号、核对面就是配置里的主人号），或主动联系的线（线只能经 thread_send 对主人的本地私聊说话）
func (a *App) lifeOwnerShape(in kernel.Invocation, aud audience.Audience) bool {
	return a.clientInfoOwner(in, aud) || lifeThreadShape(in, aud)
}

// lifeOpenGroup 是"主人开放的群"的形状：前台回合、没有父运行、身份和听众的会话场合对得上、有说话的人（缺身份、0、线的 system 都不认）。
// 本地群聊看会话串；QQ 群还要角色对，而且这个群现在还在配置的 qq.groups 里（读不出配置按不给）。
// QQ 群回合的 AccountID 是 "channel"（群的号由出口定，见 turn 的 fillIdentity），写了号的也得是当前接收账号。
// 这里故意不看 ActorKind 是不是主人：群里谁问都一样，只到城市；几个人合成一批的 mixed 也一样。
func (a *App) lifeOpenGroup(in kernel.Invocation, aud audience.Audience) bool {
	s := in.Scope
	if in.Kind != kernel.KindForeground || in.ParentRunID != "" || s.CharacterID == "" || s.Scene != "group" || aud.Scene != "group" ||
		s.Conversation != aud.Conversation || s.Channel != aud.Channel {
		return false
	}
	if s.ActorKind == "" || s.ActorKind == "system" || s.ActorID == "" || s.ActorID == "0" {
		return false
	}
	if s.Channel == "console" {
		return s.Platform == "local" && strings.HasPrefix(s.Conversation, "console:group:")
	}
	conv, err := types.ParseConversation(s.Conversation)
	// 保险：前后已由 CharacterID 判断挡住，测试打不到（只开本地时 a.account、a.character 都是空串）
	if err != nil || a.account == "" || a.character == "" || s.Channel != "qq" || s.Platform != "qq" || conv.Channel != "qq" || conv.Scene != types.SceneGroup ||
		(s.AccountID != "channel" && s.AccountID != a.account) || s.CharacterID != a.character || !validClientQQID(conv.ID) {
		return false
	}
	saved, err := config.LoadIn(a.root, a.opt.DataDir)
	return err == nil && a.inIDs(saved.QQ.Groups, conv.ID)
}

// lifeThreadShape：线的听众、会话串、预先绑定的 system 身份和线的权限档都对得上，且角色就是会话串里的那个
func lifeThreadShape(in kernel.Invocation, aud audience.Audience) bool {
	return inThread(aud) && boundThreadShape(in, aud)
}

// reminderRunShape 是提醒到点那一轮：形状和线一样，会话是这个角色的提醒专用会话
func reminderRunShape(in kernel.Invocation, aud audience.Audience) bool {
	return audience.IsReminderRun(aud.Conversation) && aud.Channel == "console" && !aud.PrivateOK && !aud.OwnerAsked && boundThreadShape(in, aud)
}

// boundThreadShape 是宿主预先绑定的 system 身份那一半（线和提醒到点那一轮共用）
func boundThreadShape(in kernel.Invocation, aud audience.Audience) bool {
	s := in.Scope
	return in.Kind == kernel.KindForeground && in.ParentRunID == "" &&
		audience.IsThread(aud.Conversation) &&
		s.Conversation == aud.Conversation && s.Channel == "console" && s.Platform == "local" && s.Scene == "private" &&
		s.ActorKind == "system" && s.ActorID == audience.ThreadActor && s.ScopeID == audience.ThreadScope &&
		s.CharacterID != "" && s.CharacterID == characterOfConv(aud.Conversation)
}
