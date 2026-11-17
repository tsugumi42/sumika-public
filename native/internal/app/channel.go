package app

// 渠道中间件的装配（规格第四节第 6、7 步，第五节）：一套库、一份名册、一个档位函数，链、闸门、出口、回合、流水线、入库一站
// 和适配器的接收端都接在同一个 Hub 上。QQ 只有发送上线七项条件齐了才摘钳制；私聊只回主人，只连 -account 一个号。

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/qq"
	"sumika-native/internal/channel/repeat"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/snowluma"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// openLinks 保持一份取数和队列；上下文与自动展开使用独立许可。
func (a *App) openLinks() error {
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Proxy: a.cfg.LinkReader.Proxy, FXTimeout: time.Duration(a.cfg.LinkReader.X.FXTimeoutSeconds) * time.Second, NoSyndication: !a.cfg.LinkReader.X.Syndication, FakeIPRanges: a.cfg.LinkReader.FakeIPRanges, Rate: linkRateOptions(a.cfg.LinkReader.Rate)})
	if err != nil {
		return fmt.Errorf("链接解析的传输：%w", err)
	}
	var fetcher linkreader.ContentFetcher = a.opt.LinkFetcher
	if fetcher == nil {
		fetcher = f
	} else if supplied, ok := fetcher.(*linkreader.Fetcher); ok {
		f = supplied
	}
	svc, err := linkreader.NewService(a.set, fetcher, linkreader.CacheOptions{Now: a.now})
	if err != nil {
		return err
	}
	a.linkSvc = svc
	a.linkMedia = linkexpand.NewMedia(f, a.paths.Root)
	if err = a.linkMedia.Sweep(a.now()); err != nil {
		return fmt.Errorf("清理展开临时资源：%w", err)
	}
	// 推特卡片的配色每次画时读当前设置（热改立即生效）；字体从 exe 旁的 fonts/ 和系统字体目录找，加载问题、缺字写日志
	a.linkCards = linkcard.NewWith(linkcard.Options{Now: a.now, Logf: a.logfFor(applog.Linkplugin), Theme: func() string {
		if v := a.currentLinks(); v != nil {
			return v.Values.Expand.TweetCard.Theme
		}
		return linkcard.ThemeAuto
	}, MaxHeight: func() int {
		if v := a.currentLinks(); v != nil {
			return v.Values.Expand.TweetCard.MaxHeight
		}
		return linkcard.DefaultTweetMaxHeight
	}})
	bound, err := a.outlet.BindAutomation(linkplugin.ID)
	if err != nil {
		return err
	}
	lp, err := linkplugin.New(linkplugin.Options{Service: svc, Stores: a.set, Logf: a.logfFor(applog.Linkplugin), Warnf: a.warnFor(applog.Linkplugin), Errorf: a.errorFor(applog.Linkplugin), Debugf: a.debugFor(applog.Linkplugin), Degrade: a.degrade, Now: a.now,
		Settings: func() linkplugin.Snapshot {
			v := a.currentLinks()
			return linkplugin.Snapshot{Settings: v.Values, Version: v.Version}
		},
		ContextAllowed: func(s channel.Stored) bool {
			mode := a.modeOf(s.Inbound.Conv)
			return s.Observed && a.roster.CanTrigger(s.Inbound.Conv.Channel, s.Actor.ID) && (mode == channel.ModeSpeak || mode == channel.ModeDryRun)
		},
		Expand: &linkplugin.ExpandOptions{Outlet: bound, Admit: a.admitLink, Permit: a.permitLink, Prepare: a.prepareLink, Outcomes: a.linkOutcomes, Block: a.recordLinkMediaFailure,
			After: a.linkAfter, Translate: a.translateLink, Artists: a.artistBook(), Account: a.account, EchoWait: a.opt.LinkEchoWait, VerifyTimeout: a.opt.LinkVerifyTimeout, Recall: a.recallLink, RecallDup: a.recallLinkDup},
	})
	if err != nil {
		return err
	}
	a.links = lp
	return a.mountPlugin(lp, true)
}

// observeLocalLinks 只接可信本地入库通知；关着时不排队，沿用插件的生命周期和网络预算。
func (a *App) observeLocalLinks(conversation string, seq int64, text string) {
	if a.links != nil && a.pluginActive(linkplugin.ID) && !audience.IsSelftest(conversation) { // 自检会话里的链接不取；插件关着就不取
		a.links.ObserveLocal(conversation, seq, text)
	}
}

// onLinkStored 入库的消息交给链接插件；插件关着就不交：不排队、不取、不展开（渠道入口和本地路径一样看开关）。
// 放回旧代码（直接挂 a.links.OnStored）就红：TestLinkIngestSkipsWhenPluginOff。
func (a *App) onLinkStored(s channel.Stored) {
	if a.pluginActive(linkplugin.ID) {
		a.links.OnStored(s)
	}
}

// closeLinks 先停队列（取消在取的、迟到的不写库），再关共享的取数；没装的跳过
func (a *App) closeLinks() {
	if a.links != nil {
		a.links.Close()
	}
	if a.linkSvc != nil {
		a.linkSvc.Close()
	}
	if a.linkCards != nil {
		a.linkCards.Close()
	}
}

// openQQUnit 是装配处的 QQ 单元：取发送权、连协议端的适配器、往名册里加 QQ 账号。
// 角色卡已在 Open 的模型那一段（记忆、嵌入、内核之前）由 checkModels 查过。
// 只开本地（LocalOnly）时整个不调；账号非空时失败的先后和改动前一样（发送权在适配器之前）。
// 临时：P2-1 过渡，P5-7 换成平台监督器。
func (a *App) openQQUnit(roster *channel.Roster) error {
	// 默认仍钳制。出口和复读配置用的仍是同一份检查，撤掉检查或听众以后不能摘钳制。
	a.takeSendLock()
	// 适配器每条连接先核验登录号和收发健康；查询不受发送钳制影响。
	adapter, err := snowluma.NewWS(snowluma.Config{URL: strings.TrimSpace(a.accountURL()), Token: a.tokens[0], Account: a.account, Logf: a.logfFor(applog.Snowluma), Warnf: a.warnFor(applog.Snowluma), Errorf: a.errorFor(applog.Snowluma), CallTimeout: a.opt.QQCallTimeout, QueryGap: a.opt.QQQueryGap, Selfcheck: a.selfcheck.counters}, a.clock(), a.alerts)
	if err != nil {
		return err
	}
	a.adapter = adapter
	// 只放 -account 这一个号（ADR-197）：主人的大号只进 Owners 认身份，不连
	roster.Accounts = append(roster.Accounts, channel.Account{Channel: "qq", ID: a.account, Character: a.character})
	return nil
}

// openChannel 建渠道中间件和适配器。responder 先声明、后赋值：闸门的 Release、出口的 OnPart、内核的 OnSteer 互相要对方，
// 用闭包解开先后（任务第 5a 条），不改组件的接口
func (a *App) openChannel(responder **channel.Responder) error {
	cfg := a.cfg
	roster := channel.Roster{
		Owners:  map[string]string{"qq": string(cfg.QQ.Owner)},
		NoReply: cfg.Reply.Deny,
	}
	// 临时：P2-1 过渡，P5-7 换成平台监督器。
	// 只开本地时名册里一个 QQ 账号都没有：闸门、出口、入库一站、回合都照常建，只是 QQ 那一条不装。
	if !a.opt.LocalOnly {
		if err := a.openQQUnit(&roster); err != nil {
			return err
		}
	}
	for _, c := range a.opt.ExtraChannels {
		if c.Adapter == nil || c.Admit == nil || c.AdmitNotice == nil || c.Dispatch == nil {
			return fmt.Errorf("多装的渠道 %q 缺适配器或准入、分流", c.Channel)
		}
		// 出口按适配器自己报的名字找它，入库按渠道名走：两边对不上，发送要到运行时才找不到适配器
		if c.Adapter.Name() != c.Channel {
			return fmt.Errorf("多装的渠道 %q 的适配器自称 %q，对不上", c.Channel, c.Adapter.Name())
		}
		roster.Accounts = append(roster.Accounts, channel.Account{Channel: c.Channel, ID: c.Account, Character: c.Character})
	}
	// 名册的合法性由闸门、出口、入库一站的构造各查一次（Roster 的 validate 在 channel 包里不对外）
	a.roster = roster

	outCfg := channel.OutletConfig{Check: a.checkQQSend, GuardFor: a.guardFor, UnguardedOrigins: map[string]bool{linkOrigin: true, remoteApprovalOrigin: true}, Pace: a.opt.OutletPace, Degrade: a.degrade, Selfcheck: a.selfcheck.counters}
	repCfg := repeat.Config{GuardFor: func(id string) repeat.Guard { return a.guardFor(id) }, Audience: repeatAudience, CanTrigger: roster.CanTrigger}
	raw := channel.GateModes(a.set, storedMode(cfg.Group.Mode), a.logfFor(applog.Channel))
	a.hardQQMode = a.qqSendMode(func(types.Conversation) string { return channel.ModeSpeak }, &outCfg)
	qqMode := func(c types.Conversation) string {
		m := raw(c)
		if m != channel.ModeSpeak {
			return m
		}
		return a.hardQQMode(c)
	}
	// QQ 以外的平台渠道还没有自己的发送规矩（规格-多平台装配第 3 步复核）：一律只记录、不发。兜底只能收紧，
	// 不然出口里有了它的适配器，私聊默认档位又是说话，就绕过 QQ 那套七个条件直接发出去了
	a.modeOf = func(c types.Conversation) string {
		if c.Channel == "qq" || c.Channel == "console" {
			return qqMode(c)
		}
		return channel.ModeRecord
	}

	// 频率 0 通过全局规则表达；只清理由装配写入、仍未被主人修改的规则。
	freq, muted := cfg.Group.GateFrequency()
	if err := a.syncZeroFrequency(muted); err != nil {
		return err
	}

	// 渠道清单（规格-多平台装配第 3 步）：QQ 是第一项；名册、出口、闸门仍是全局一份。
	// 库打不开的平台整个不装：不进 a.channels（Run 不会起它、不连协议端），也不进出口的适配器清单（发不出去）。
	a.channels = nil
	if !a.opt.LocalOnly {
		if _, off := a.set.Unavailable()["qq"]; off {
			a.logf("QQ 的库没打开，QQ 通道不启动（不收、不发、不补录、不连协议端）")
		} else {
			a.channels = append(a.channels, &channelUnit{channel: "qq", account: a.account, adapter: a.adapter, policy: a.qqPolicy()})
		}
	}
	for _, c := range a.opt.ExtraChannels {
		if _, off := a.set.Unavailable()[c.Platform]; off {
			a.logf("渠道 %s（平台 %s）的库没打开，这个渠道不启动", c.Channel, c.Platform)
			continue
		}
		a.channels = append(a.channels, &channelUnit{channel: c.Channel, account: c.Account, adapter: c.Adapter,
			policy: channelPolicy{admit: c.Admit, admitNotice: c.AdmitNotice, dispatch: c.Dispatch}})
	}
	adapters := make([]types.Adapter, 0, len(a.channels))
	for _, u := range a.channels {
		u.sink = &sink{a: a, channel: u.channel, account: u.account}
		adapters = append(adapters, u.adapter)
	}

	aliases := func(id string) []string { return cfg.Characters[id].Aliases }
	// 闸门：放行的批次交给回合（turn 之后才建，闭包接到那上面）
	gate, err := channel.NewGate(channel.GateConfig{
		Stores: a.set, Roster: roster, Mode: a.modeOf,
		Release: func(b types.Batch) {
			if *responder == nil {
				a.logf("闸门放行了一批，但回合还没建好：这一批没有交出去")
				return
			}
			(*responder).Release(b)
		},
		Frequency: freq, Jitter: cfg.Group.GateJitter(),
		Aliases: aliases,
		Clock:   a.clock(), Logf: a.logfFor(applog.Channel), Warnf: a.warnFor(applog.Channel),
		OnPause: a.onQQPause, OnResume: a.onQQResume,
	})
	if err != nil {
		return err
	}
	a.gate = gate

	// 明确结果统一交给通道门和发送提醒；回声也走这一个回调。
	outCfg.Stores, outCfg.Roster, outCfg.Seen = a.set, roster, a.hub
	outCfg.Adapters, outCfg.Mode = adapters, a.outletMode
	outCfg.OnPart, outCfg.Clock, outCfg.Logf = a.onQQPart, a.clock(), a.logfFor(applog.Channel)
	outCfg.Errorf = a.errorFor(applog.Channel)
	outlet, err := channel.NewOutlet(outCfg)
	if err != nil {
		return err
	}
	a.outlet = outlet

	line := a.lineEnv()
	for _, id := range a.memoryIDs {
		level := a.memories[id].GuardLevel()
		a.logf("角色 %s 发送前守卫能力：%s", id, level)
		if level == "literal-pending" && (a.embedding == nil || a.embedding.Model() == "") {
			a.alerts.Raise(alert.Alert{Key: "guard:literal-pending", Class: "guard", Title: "发送前检查只比对原文", Body: "发送前检查这一步只比对原文，相似度还没做，不是嵌入服务坏了"})
		}
	}
	if a.character != "" && a.guardFor(a.character) == nil {
		a.logf("角色 %s 没接守卫，发送钳制保持", a.character)
	}
	// 回合：内核是 Hub，写库的是出口；一个回合定了就告诉闸门（一步一句，试运行也算开口）
	r, err := channel.NewResponder(channel.ResponderConfig{
		Kernel: a.kernel, Writer: outlet, Stores: a.set, Roster: roster,
		GuardFor:   a.guardFor,
		OnTurnDone: gate.TurnDone,
		Line:       line,
		OwnerCall:  a.ownerCall,
		Logf:       a.logfFor(applog.Channel),
		Warnf:      a.warnFor(applog.Channel),
	})
	if err != nil {
		return err
	}
	if err := a.hub.Use(r.Plugin()); err != nil {
		return fmt.Errorf("挂回合插件：%w", err)
	}
	*responder = r
	a.responder = r

	// 拦截链：复读（包一层补调闸门的 TurnDone）和戳回去，档位用同一个函数。
	// 按完整会话读配置覆盖；未设沿用群默认关、可接私聊戳回去默认开（ADR-189）。
	repCfg.Out = outlet
	// 复读的图片和表情包照发（主人 2026-11-08 答"复读要不要复读图片和表情包"："可以"）；只在开了复读的会话里
	repCfg.Media = func(ctx context.Context, conv types.Conversation, hash string) (*types.MediaResource, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := mediastore.OpenMedia(a.paths.Root, conv.Channel, hash)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 20_000_001))
		if err != nil {
			return nil, err
		}
		if len(b) > 20_000_000 {
			return nil, fmt.Errorf("复读图片超过20 MB")
		}
		return types.MemoryMedia(hash, http.DetectContentType(b), 0, 0, b), nil
	}
	rep, err := repeat.New(repCfg)
	if err != nil {
		return err
	}
	poke, err := qq.NewPoke(qq.PokeConfig{Out: outlet, IsBot: func(ch, id string) bool {
		reason := cfg.Reply.Deny[ch+":"+id]
		return reason == "ai" || reason == "bot"
	}})
	if err != nil {
		return err
	}
	chain, err := channel.NewChain(channel.ChainConfig{
		Plugins: []types.Interceptor{countRepeatTurnDone(rep.Interceptor(), gate.TurnDone), a.limitQQPoke(poke.Interceptor())},
		Switch: func(plugin, _ string, conv types.Conversation) (bool, bool) {
			return cfg.PluginSwitch(plugin, conv.String())
		},
		Mode: a.modeOf, Clock: a.clock(), Logf: a.logfFor(applog.Channel),
	})
	if err != nil {
		return err
	}
	a.chain = chain

	// 流水线：过了链的交给闸门。分流是装配处的事（闸门的 Handle 只管平台的群）
	if err := a.openLinks(); err != nil {
		return err
	}
	pipeline, err := channel.NewPipeline(channel.PipelineConfig{
		Stores: a.set, Roster: roster, Seen: a.hub, Chain: chain, Gate: a.dispatch, OnMessage: a.onLinkStored, Logf: a.logfFor(applog.Channel),
		Aliases: aliases, Control: a.remoteControl,
	})
	if err != nil {
		return err
	}
	a.pipeline = pipeline

	// 收图下载：入库一站的回调先把图片段交给它排队，再交流水线
	if !a.opt.LocalOnly {
		// 临时：P2-1 过渡，P5-7 换成平台监督器。
		// 只开本地时这些 QQ 单元一律不建：桌面补录页拿到 nil 回“这次没开 QQ”，发送权、收图、转发也不碰 QQ 库。
		if err := a.openMedia(); err != nil {
			return err
		}
		if err := a.openForwards(); err != nil {
			return err
		}
	}
	// 入库一站：准入按渠道查（QQ 照 qq.groups / qq.privateAllow，查不到的渠道丢），回声认领是出口
	intake, err := channel.NewIntake(channel.Config{
		Stores: a.set, Roster: roster, Admit: a.admit, AdmitNotice: a.admitNotice, Echo: outlet,
		OnStored: a.withForwards(a.withMedia(pipeline.OnStored)), OnNotice: func(n channel.Noticed) {
			if n.Notice.Kind == types.NoticeRecall {
				a.links.Recall(n.Notice.Conv, n.Target.Seq)
			}
			pipeline.OnNotice(n)
		},
		Line:      line,
		OwnerCall: a.ownerCall,
		Now:       a.now, Logf: a.logfFor(applog.Channel), Errorf: a.errorFor(applog.Channel), Selfcheck: a.selfcheck.counters,
	})
	if err != nil {
		return err
	}
	a.intake = intake
	if !a.opt.LocalOnly {
		// 临时：P2-1 过渡，P5-7 换成平台监督器。只开本地不建补录，a.backfill 保持 nil。
		if err := a.openBackfill(); err != nil {
			return err
		}
	}

	// 提醒：掉线、恢复写进 QQ 库的 qq_alerts（试运行统计要排除掉线时段），别的键只记日志
	sub, open := a.alerts.SubscribeWithOpen()
	a.alertSub, a.alertDone = sub, make(chan struct{})
	go a.watchAlerts(open)
	return nil
}

// guardFor 不把空的记忆指针装进接口，避免没挂记忆的角色被当成有守卫。
// 回合、复读、引用 / 转发动作三处检查都从这里取守卫，包一层以后守卫走了退路（嵌入挂了、确认模型挂了）就提醒用户（guardroute.go）。
func (a *App) guardFor(id string) channel.Guard {
	if m := a.memories[id]; m != nil {
		return &routedGuard{id: id, core: m, routes: &a.guardRoutes, hub: a.alerts}
	}
	return nil
}

// guardFull 供发送启用条件读取；能力随嵌入状态变化，不能缓存启动时的结果。
func (a *App) guardFull(id string) bool {
	m := a.memories[id]
	return m != nil && m.GuardLevel() == "full"
}

// repeatAudience 不把复读参与者当成主人在问，也不带群友的私聊范围。
func repeatAudience(conv types.Conversation) (audience.Audience, error) {
	if conv.Scene != types.SceneGroup {
		return audience.Audience{}, fmt.Errorf("复读没有群听众")
	}
	return audience.Audience{Channel: conv.Channel, Conversation: conv.String(), Scene: conv.Scene, PrivateOK: conv.Channel == "console"}, nil
}

// takeSendLock 核对 protocol 1 的交接并持锁；摘钳制还要经过 qqSendMode 的七项检查。
// 没抢到时只提醒，不退出收包。
func (a *App) takeSendLock() {
	if !a.cfg.QQ.GoSend {
		return
	}
	h, err := sendlock.Take(a.paths.Root, a.account)
	if err != nil {
		a.logf("QQ 发送权没有交过来（%v，号 %s）：发送次数保持 0，照常收消息", err, config.MaskID(a.account))
		a.alerts.Raise(alert.Alert{
			Key:   "qq:" + a.account + ":lock",
			Title: "QQ 发送权没有交过来",
			Body:  "qq.goSend 开着，但" + err.Error() + "；发送次数保持 0，照常收消息",
		})
		return
	}
	a.sendLock = h
	if h.IntentRecovered {
		body := "发送权的意图文件不见了；交接记录显示之前已由 Go 接管，按 Go 持有继续发，并已补回文件"
		if h.IntentWriteErr != nil {
			body = "发送权的意图文件不见了；交接记录显示之前已由 Go 接管，这次按 Go 持有继续发，但补回文件失败（" + h.IntentWriteErr.Error() + "），重启后可能又不发"
		}
		a.logf("QQ 发送权：%s（号 %s）", body, config.MaskID(a.account))
		a.alerts.Raise(alert.Alert{Key: "qq:" + a.account + ":intent", Title: "发送权意图文件不见了", Body: body})
	}
	if h.Resumed {
		a.logf("QQ 发送锁拿到了（号 %s）：上次没正常交出，接着发", config.MaskID(a.account))
	} else {
		a.logf("QQ 发送锁拿到了（号 %s）：等发送启用条件齐全", config.MaskID(a.account))
	}
}

// releaseSendLock 交出发送权：记录写成 released、再放锁（规格 5.2 的顺序，不许先放锁）。
// 调用处要先确保已经停发（关机第 5 步关完出口以后）
func (a *App) releaseSendLock() {
	if a.sendLock == nil {
		return
	}
	if err := a.sendLock.Release(); err != nil {
		a.logf("交出 QQ 发送锁出错：%v", err)
	} else {
		a.logf("QQ 发送锁交出去了（号 %s）", config.MaskID(a.account))
	}
	// 保留已释放的 Holder；CanSend 读到关闭句柄立即收紧，避免和档位读取争写指针。
}

const autoStarZero = "app:auto-star-zero"

func (a *App) syncZeroFrequency(muted bool) error {
	st := a.set.TraceStore()
	_, _, owned, err := st.LoadGateState(autoStarZero)
	if err != nil {
		return fmt.Errorf("读自动零频率标记：%w", err)
	}
	rules, err := st.GateRules("*")
	if err != nil {
		return fmt.Errorf("读全局频率规则：%w", err)
	}
	var star *store.GateRule
	for i := range rules {
		if rules[i].Conversation == "*" && rules[i].Slot == "" {
			star = &rules[i]
		}
	}
	if muted {
		// 库里的全局规则优先；只在原本没有任何 * 规则时写装配自己的永久零。
		if len(rules) != 0 {
			if !owned {
				a.logf("库里已有 * 规则，配置的 0 不覆盖")
			}
			return nil
		}
		return st.SetGateRuleWithState(&store.GateRule{Conversation: "*", Frequency: 0}, autoStarZero, false, a.now().UnixMilli())
	}
	if owned {
		if star != nil && (star.Frequency != 0 || star.Until != 0) {
			star = nil // 主人改过规则，只清标记。
		}
		return st.SetGateRuleWithState(star, autoStarZero, true, a.now().UnixMilli())
	}
	return nil
}

// channelPolicy 是一个渠道的准入和分流（规格-多平台装配第 1 步）。中间件的 Admit、AdmitNotice 和流水线的 Gate 是全局一份，
// 按批次的渠道名在这里查；查不到的渠道一律丢、不往下走，不落回 QQ 的规矩（别的平台的号可能和 QQ 主人的号撞上）
type channelPolicy struct {
	admit       func(types.Inbound) channel.Admission
	admitNotice func(types.Notice) channel.Admission
	dispatch    func(channel.Dispatch)
}

// channelUnit 是渠道清单里的一项（规格-多平台装配第 3 步）：渠道名、连的号、适配器、它的接收端、准入和分流。
// QQ 专有的发送权、档位、戳回去的限制在 QQ 自己的函数里，不进这里
type channelUnit struct {
	channel, account string
	adapter          types.Adapter
	sink             *sink
	policy           channelPolicy
}

func (a *App) policy(ch string) (channelPolicy, bool) {
	for _, u := range a.channels {
		if u.channel == ch {
			return u.policy, true
		}
	}
	return channelPolicy{}, false
}

func (a *App) qqPolicy() channelPolicy {
	return channelPolicy{admit: a.qqAdmit, admitNotice: a.qqAdmitNotice, dispatch: a.qqDispatch}
}

func (a *App) admit(in types.Inbound) channel.Admission {
	p, ok := a.policy(in.Conv.Channel)
	if !ok {
		a.logf("渠道 %q 没有准入规矩，这条消息不收", in.Conv.Channel)
		return channel.Admission{Drop: true}
	}
	return p.admit(in)
}

func (a *App) admitNotice(n types.Notice) channel.Admission {
	// 被踢下线的通知没有会话（渠道名是空的），各渠道都照收：要靠它停发、提醒
	if n.Kind == types.NoticeKicked {
		return channel.Admission{}
	}
	p, ok := a.policy(n.Conv.Channel)
	if !ok {
		a.logf("渠道 %q 没有准入规矩，这条通知不收", n.Conv.Channel)
		return channel.Admission{Drop: true}
	}
	return p.admitNotice(n)
}

func (a *App) dispatch(d channel.Dispatch) {
	p, ok := a.policy(d.Batch.Conv.Channel)
	if !ok {
		a.logf("渠道 %s 这一版不处理，这一批没有往下走", d.Batch.Conv.Channel)
		return
	}
	p.dispatch(d)
}

// qqDispatch 在 QQ 群交闸门；QQ 私聊要三样同时成立才交出口回（qqPrivateReplyOK）：qq.reply 开着、对方是主人、
// 档位算出来是说话（也就是 qqSendMode 的七项发送启用条件都齐）。其余私聊（别人的、条件不齐的）只存不回
func (a *App) qqDispatch(d channel.Dispatch) {
	b := d.Batch
	if len(b.Msgs) == 0 || !a.roster.CanTrigger(b.Conv.Channel, b.Msgs[0].Who) {
		return
	}
	switch {
	case b.Conv.Channel == "qq" && b.Conv.Scene == types.SceneGroup:
		a.gate.Handle(d)
	case b.Conv.Channel == "qq" && b.Conv.Scene == types.ScenePrivate:
		if a.qqPrivateReplyOK(b.Conv) {
			a.responder.Release(b)
			return
		}
		a.logf("会话 %s 是私聊，qq.reply 没开、对方不是主人或发送启用条件不齐（条件不齐的原因见“QQ 发送钳制保持”那一行），只存不回", maskConv(b.Conv))
	default:
		a.logf("渠道 %s 这一版不处理，这一批没有往下走", b.Conv.Channel)
	}
}

// qqPrivateReplyOK 是 QQ 私聊能不能回：qq.reply 开着、对方就是主人、档位是说话（QQ 的说话要先过 qqSendMode 的七项，过不了算出来是试运行）
func (a *App) qqPrivateReplyOK(conv types.Conversation) bool {
	return a.cfg.QQ.Reply && conv.ID != "" && conv.ID == string(a.cfg.QQ.Owner) && a.modeOf(conv) == channel.ModeSpeak
}

// 戳回去同样先查不接话名单；私聊再沿用文字回复的主人、开关和发送钳制，群里仍由插件开关决定。
// 私聊里做事的人还必须就是这个私聊的对方：协议端的通知可以让会话落在主人的私聊、做事的人却是别的号（或空、0），
// 戳回去戳的是做事的人，不能借主人的私聊去戳别人。
func (a *App) limitQQPoke(inner types.Interceptor) types.Interceptor {
	call := inner.Intercept
	inner.Intercept = func(ctx context.Context, b types.Batch) types.Verdict {
		if b.Notice != nil && !a.roster.CanTrigger(b.Conv.Channel, a.roster.Resolve(b.Conv.Channel, b.Notice.Actor).ID) {
			return types.Passed("名单或发言人身份不允许触发，不戳回")
		}
		if b.Conv.Channel == "qq" && b.Conv.Scene == types.ScenePrivate {
			if !a.qqPrivateReplyOK(b.Conv) {
				return types.Passed("私聊 qq.reply 没开、发送启用条件不齐或对方不是主人，只存不回")
			}
			if b.Notice != nil && b.Notice.Actor != b.Conv.ID {
				return types.Passed("戳她的不是这个私聊的对方，不戳回")
			}
		}
		return call(ctx, b)
	}
	return inner
}

// qqAdmit 是 QQ 消息的准入（照 Bun 的 qq.ts）：白名单外的群完全不收；私聊里不是主人、不是角色、也不在私聊白名单的，
// 存原文、不记她看到了（陌生人）
func (a *App) qqAdmit(in types.Inbound) channel.Admission {
	if in.Conv.Scene == types.SceneGroup {
		if !a.groupAllowed(in.Conv.ID) {
			return channel.Admission{Drop: true}
		}
		return channel.Admission{}
	}
	if a.qqStranger(in.From.ID) {
		return channel.Admission{NoObserve: true, Tags: []string{"stranger"}}
	}
	return channel.Admission{}
}

// qqAdmitNotice 是 QQ 通知的准入：群的通知同白名单；私聊通知里陌生人做的照存不观察（和消息一样）
func (a *App) qqAdmitNotice(n types.Notice) channel.Admission {
	if n.Conv.Scene == types.SceneGroup {
		if !a.groupAllowed(n.Conv.ID) {
			return channel.Admission{Drop: true}
		}
		return channel.Admission{}
	}
	// 做事的人没说是谁（协议端没给，或者给的是 0，ADR-221）就按会话对方认：不然陌生人私聊里的这种通知会被记成看到了、进记忆
	who := n.Actor
	if who == "" {
		who = n.Conv.ID
	}
	if a.qqStranger(who) {
		return channel.Admission{NoObserve: true, Tags: []string{"stranger"}}
	}
	return channel.Admission{}
}

// qqStranger 说这个号在 QQ 私聊里是不是陌生人：别人（不是主人、不是角色），而且不在 privateAllow 里
func (a *App) qqStranger(id string) bool {
	if id == "" || a.roster.Resolve("qq", id).Kind != channel.ActorHuman {
		return false
	}
	return !a.inIDs(a.cfg.QQ.PrivateAllow, id)
}

func (a *App) groupAllowed(id string) bool { return a.inIDs(a.cfg.QQ.Groups, id) }

func (a *App) inIDs(list []config.QQID, id string) bool {
	for _, x := range list {
		if string(x) == id {
			return true
		}
	}
	return false
}

// accountURL 是适配器要的地址（配置里写了才连，这一版不自动找）
func (a *App) accountURL() string {
	for _, x := range a.cfg.QQ.Accounts {
		if string(x.ID) == a.account {
			return x.URL
		}
	}
	return ""
}

// storedMode 是 group.mode 给 GateModes 的默认档（没设过档位的群就看它）
func storedMode(mode string) string {
	switch mode {
	case "off":
		return channel.ModeRecord
	case "speak":
		return channel.ModeSpeak
	default:
		return channel.ModeDryRun
	}
}

// countRepeatTurnDone 把复读插件包一层（任务第 6a 条）：复读插件没有反馈钩子，它接下这一批（Took）时补调一次闸门的
// TurnDone（说了、一句）。戳一戳不包：戳回去不是说话，不计开口、不计退避（规格-群聊闸门 10.1）
func countRepeatTurnDone(inner types.Interceptor, turnDone func(character string, conv types.Conversation, spoke bool, count int, at time.Time)) types.Interceptor {
	out := inner
	call := inner.Intercept
	out.Intercept = func(ctx context.Context, b types.Batch) types.Verdict {
		v := call(ctx, b)
		if v.Taken && turnDone != nil {
			at := b.Now
			if at.IsZero() {
				at = time.Now()
			}
			turnDone(b.Character, b.Conv, true, 1, at)
		}
		return v
	}
	return out
}

// sink 是一个渠道的适配器的接收端（types.Sink）：消息和通知转给入库一站，状态里连上了就清这个渠道的通道门。
// 帧里的号和这一项要连的号对不上的（连错了端点）只记日志、不入库（规格第十节第 4 条）
type sink struct {
	a                *App
	channel, account string
}

// label 是日志里的渠道名：QQ 照旧写"QQ"
func (s *sink) label() string {
	if s.channel == "qq" {
		return "QQ"
	}
	return s.channel
}

func (s *sink) Inbound(ctx context.Context, in types.Inbound) error {
	if in.Account != s.account {
		s.a.logf("收到号 %s 的一条消息，和要连的号 %s 对不上，不入库（连错了端点？）", config.MaskID(in.Account), config.MaskID(s.account))
		s.a.selfcheck.counters.Add(selfcheck.FrameSinkDropped) // 有意丢的，记成去向：自检 S1 的交接守恒式不把它当丢失
		return nil
	}
	return s.a.intake.Inbound(ctx, in)
}

func (s *sink) Notice(ctx context.Context, n types.Notice) error {
	if n.Account != s.account {
		s.a.logf("收到号 %s 的一条通知（%s），和要连的号 %s 对不上，不处理", config.MaskID(n.Account), n.Kind, config.MaskID(s.account))
		return nil
	}
	return s.a.intake.Notice(ctx, n)
}

func (s *sink) Status(ctx context.Context, st types.Status) error {
	if st.Account != s.account {
		s.a.logf("收到号 %s 的连接状态（%s），和要连的号 %s 对不上，不处理", config.MaskID(st.Account), st.Kind, config.MaskID(s.account))
		return nil
	}
	if st.Kind == types.StatusTyping {
		s.a.noteTyping(st.Conv.String())
		return nil
	}
	if s.channel == "qq" && s.a.backfill != nil {
		switch st.Kind {
		case types.StatusConnected:
			s.a.backfill.connected(ctx)
		case types.StatusDisconnected, types.StatusReconnecting, types.StatusKicked, types.StatusMismatch:
			s.a.backfill.stop()
		}
	}
	switch st.Kind {
	case types.StatusConnected:
		// 连上（重连成功也是）：通道门清零，闸门照常放行（规格-渠道中间件第八节）
		s.a.gate.Resume(s.channel)
		s.a.logf("%s 连上了", s.label())
		// 断线期间主人说的话可能在这之后才被推来：审批回复的歧义窗口重新计时（规格-主人QQ私聊审批第四节）
		if r := s.a.remote.relay.Load(); r != nil && s.channel == "qq" {
			r.NoteReconnect()
		}
		// 首次连接和重连成功后才补扫；工人启动时协议端尚未可用。
		if s.a.media != nil && s.channel == "qq" && s.a.backfill == nil {
			s.a.media.Resweep()
		}
		if s.a.forwards != nil && s.channel == "qq" && s.a.backfill == nil {
			s.a.forwards.Resweep()
		}
	case types.StatusHeartbeat:
		// 心跳整天周期性到达，不记
	case types.StatusDisconnected:
		s.a.logf("%s 断了，正在按退避重连（%s）", s.label(), st.Reason)
	case types.StatusReconnecting:
		s.a.logf("%s 还没连上，继续重连（%s）", s.label(), st.Reason)
	case types.StatusKicked:
		s.a.logf("%s 被踢下线了", s.label())
	case types.StatusMismatch:
		s.a.logf("%s 连上以后号对不上（%s）", s.label(), st.Reason)
	default:
		s.a.logf("%s 的连接状态：%s", s.label(), st.Kind)
	}
	return nil
}

// watchAlerts 把连接与发送提醒写进 QQ 库，别的只记日志。
func (a *App) watchAlerts(open []alert.Alert) {
	defer close(a.alertDone)
	for _, x := range open {
		a.logf("启动时已经开着的提醒：%s（%s）", x.Title, x.Body)
	}
	for c := range a.alertSub.C() {
		a.onAlert(c)
	}
}

func (a *App) onAlert(c alert.Change) {
	isSend := c.Alert.Key == "qq:"+a.account+":send"
	if c.Alert.Key != "qq:"+a.account+":conn" && !isSend {
		a.logf("提醒：%s（%s）", c.Alert.Title, c.Alert.Body)
		return
	}
	var kind, text string
	switch c.Kind {
	case alert.Raised:
		kind, text = "down", c.Alert.Title+"："+c.Alert.Body
	case alert.Cleared:
		// 恢复的那一行写恢复的标题和正文（Note），不是关掉之前那条的
		kind, text = "up", c.Note.Title+"："+c.Note.Body
		if strings.TrimSpace(c.Note.Title+c.Note.Body) == "" {
			text = c.Alert.Title + "：" + c.Alert.Body
		}
	default:
		a.logf("提醒更新：%s（%s）", c.Alert.Title, c.Alert.Body)
		return
	}
	if isSend {
		if c.Kind == alert.Cleared {
			a.logf("QQ 发送提醒清除了：%s", text)
			return
		}
		kind = "send-failed" // 复用现有提醒类型，不混进掉线时段统计。
	}
	if a.closing.Load() {
		return
	}
	db, err := a.set.For("qq")
	if err != nil {
		a.logf("记 QQ 掉线的时候取不到 QQ 的库：%v", err)
		return
	}
	if _, err := db.AddQQAlert(store.QQAlert{At: a.now().UnixMilli(), Kind: kind, Account: a.account, Text: text}); err != nil {
		a.logf("记 QQ 的一条提醒（%s）失败：%v", kind, err)
		return
	}
	a.logf("QQ 提醒记了一行（%s）：%s", kind, text)
}

// linkRateOptions 把配置里的取数节奏换成 linkreader 的选项：额度用满排队等（maxWaitSeconds 以内），平台真拦了才失败（主人 2026-11-13）
func linkRateOptions(r config.LinkRate) linkreader.RateOptions {
	return linkreader.RateOptions{RequestsPerMinute: r.PerMinute, PerPlatform: maps.Clone(r.Platforms), MaxBudgetWait: time.Duration(r.MaxWaitSeconds) * time.Second}
}
