package linkplugin

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"sumika-native/internal/artists"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
	"sumika-native/internal/taskrun"
)

type Snapshot struct {
	Settings config.LinkReaderConfig
	Version  string
}

// Outbound 是展开发送用的出口（宿主给 *channel.AutomationOutlet；测试换成假的，才测得到回执读不到这类情形）
type Outbound interface {
	Submit(ctx context.Context, character string, conv types.Conversation, parts []types.Part, resources []*types.MediaResource) (channel.Held, error)
	CancelPlan(id string)
}

type ExpandOptions struct {
	Outlet  Outbound
	Admit   func(channel.Stored) bool
	Permit  func(channel.Stored, []types.Part) error
	Prepare func(context.Context, channel.Stored, Snapshot, *linkreader.Result, *linkexpand.Draft) (linkexpand.Prepared, error)
	// Outcomes 读一个计划里每段的终态（宿主用出口的 Plan 读）；Block 记下这一段的媒体组合在这个场合明确失败过（ADR-310）
	Outcomes func(types.Conversation, string) ([]PartOutcome, error)
	Block    func(types.Conversation, types.Part, string)
	// After 取这个会话里 anchor（源消息的平台消息号）之后的消息，核实“没拿到回执”的那一条有没有进群；nil 或 Account 为空就不核实（ADR-333）
	After         func(ctx context.Context, conv types.Conversation, anchor string) (types.AfterPage, error)
	Account       string        // 发送用的号：核实时认“自己发的”
	VerifyTimeout time.Duration // 开了 historyCheck 时翻历史的时限；0 用默认 10 秒
	// EchoWait 非 0 时代替配置里的 echoWaitMS（只给测试用：配置下限是 500 毫秒）
	EchoWait time.Duration
	// Recall 撤回这次展开刚发的、图被吞了的那一条（规格-链接发送改版 A4）：planID、idx 是那一段，platformID 是认领到的回声的号。
	// 宿主在装配处再判一次门槛（自己号、本次刚认领、2 分钟内），不成立返回 ErrRecallRefused、不调适配器；nil 是这个渠道不能撤回
	Recall func(ctx context.Context, conv types.Conversation, planID string, idx int, platformID string) error
	// RecallDup 撤回换形式后补发的那一份（规格-链接发送改版 B15）：orig 是原来那段（回声晚到了，PlatformID 是回声的号），
	// dup 是补发的、已确认送到的那一份。宿主按库里的计划独立再判：两段都是链接展开用发送用的号在这个会话里发的、
	// 原段是被回声认领成确认的、补发份已确认且号对得上又不是原段的号、从补发份第一次调 Send 起不到 2 分钟、这个会话现在仍有发送许可；
	// 不成立返回 ErrRecallRefused、不调适配器；nil 是这个渠道不能撤回
	RecallDup func(ctx context.Context, conv types.Conversation, orig, dup PartRef) error
	// Translate 翻译帖子正文（规格-链接翻译）：cleanURL 做缓存键，ex 是这个任务冻结的翻译设置；nil 就不翻。
	// 失败、超时一律回没有译文的结果，插件只发原文
	Translate func(ctx context.Context, cleanURL, text string, ex config.LinkTranslate) linktranslate.Outcome
	// Artists 是画师表（规格-画师原图）：命中确认过的画师时换发法，展开时按需判断不在表里的作者；nil 就是没有这个功能
	Artists *artists.Book
}
type ActiveJob struct{ JobID, Conversation, Version, Stage string }
type expansionRun struct {
	source  channel.Stored
	state   ActiveJob
	ctx     context.Context
	cancel  context.CancelFunc
	plans   []string
	mu      sync.Mutex
	reacted string // 已经确认贴上的开始表情；完成时要先撤掉它
}

func (p *Plugin) snapshot() Snapshot { return p.opt.Settings() }
func (p *Plugin) Active() []ActiveJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ActiveJob, 0, len(p.active))
	for _, j := range p.active {
		out = append(out, j.state)
	}
	slices.SortFunc(out, func(a, b ActiveJob) int {
		if a.JobID < b.JobID {
			return -1
		}
		if a.JobID > b.JobID {
			return 1
		}
		return 0
	})
	return out
}
func (p *Plugin) stage(j *expansionRun, stage string) {
	p.mu.Lock()
	j.state.Stage = stage
	p.mu.Unlock()
}
func (p *Plugin) stopExpansion(j *expansionRun) {
	j.cancel()
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.plans {
		p.opt.Expand.Outlet.CancelPlan(id)
	}
}

// Refresh 只撤销当前不再允许的出口；共享 Service 的上下文等待者独立存活。
func (p *Plugin) Refresh() {
	if p.opt.Expand == nil {
		return
	}
	current := p.snapshot()
	p.mu.Lock()
	var stopped []*expansionRun
	for _, j := range p.active {
		if !current.Settings.Expand.Allows(j.source.Inbound.Conv) || !senderOK(current.Settings.Expand, j.source) {
			stopped = append(stopped, j)
		}
	}
	p.mu.Unlock()
	for _, j := range stopped {
		p.stopExpansion(j)
	}
}
func (p *Plugin) Recall(conv types.Conversation, seq int64) {
	p.mu.Lock()
	var found []*expansionRun
	for _, j := range p.active {
		if j.source.Seq == seq && j.source.Inbound.Conv == conv {
			found = append(found, j)
		}
	}
	p.mu.Unlock()
	for _, j := range found {
		p.stopExpansion(j)
	}
}
func (p *Plugin) permit(j *expansionRun, parts []types.Part) error {
	if e := j.ctx.Err(); e != nil {
		return e
	}
	if e := p.snapshot().Settings.Expand; !e.Allows(j.source.Inbound.Conv) || !senderOK(e, j.source) {
		return &Blocked{Why: notePermitGone, Err: errors.New("展开许可已撤销")}
	}
	return p.opt.Expand.Permit(j.source, parts)
}

// send 提交一个计划并等它终态，返回计划号（回执要按计划号读）
func (p *Plugin) send(j *expansionRun, parts []types.Part, resources []*types.MediaResource) (string, error) {
	for i := range parts {
		parts[i].SourceSeq = j.source.Seq
	}
	if e := p.permit(j, parts); e != nil {
		for _, r := range resources {
			if e := r.Release(); e != nil {
				p.warnf("资源释放失败")
			}
		}
		return "", e
	}
	held, e := p.opt.Expand.Outlet.Submit(j.ctx, j.source.Character, j.source.Inbound.Conv, parts, resources)
	if e != nil {
		return "", e
	}
	j.mu.Lock()
	j.plans = append(j.plans, held.PlanID)
	cancelled := j.ctx.Err() != nil
	j.mu.Unlock()
	if cancelled {
		p.opt.Expand.Outlet.CancelPlan(held.PlanID)
	}
	// 媒体仍在协议端读取时保留 worker，避免下一批准备结果无限堆入出口。
	select {
	case <-held.Done:
		return held.PlanID, nil
	case <-j.ctx.Done():
		p.opt.Expand.Outlet.CancelPlan(held.PlanID)
		<-held.Done
		return "", j.ctx.Err()
	}
}
func (p *Plugin) start(j job) {
	run := j.run
	if run == nil || p.permit(run, nil) != nil {
		return
	}
	s, e := run.source, j.snapshot.Settings.Expand
	var part types.Part
	if s.Inbound.Conv.Scene == types.ScenePrivate {
		part = types.Part{Kind: types.PartMessage, Quote: s.Inbound.PlatformID, Body: types.TextBody(e.PrivateStatus)}
	} else {
		if e.StartEmoji == "" {
			return
		}
		part = types.Part{Kind: types.PartReact, Ref: s.Inbound.PlatformID, EmojiID: e.StartEmoji}
	}
	planID, err := p.send(run, []types.Part{part}, nil)
	if err != nil {
		p.warnf("开始状态未入队（%s）", reason(err))
		return
	}
	if part.Kind == types.PartReact {
		if outs, e := p.opt.Expand.Outcomes(s.Inbound.Conv, planID); e == nil && allConfirmed(outs) {
			run.reacted = part.EmojiID
		} else {
			p.opt.Logf("开始表情没有确认贴上，之后不撤它")
		}
	}
}

// conclude 在这条消息的链接都处理完后收尾（只在群里）：撤掉开始表情，换上完成或失败表情。
// delivered 为真表示至少一个链接的内容发出去了；failed 为真表示有链接没成。表情失败只记日志，不影响内容。
func (p *Plugin) conclude(run *expansionRun, e config.LinkExpand, delivered, failed bool) {
	s := run.source
	if s.Inbound.Conv.Scene != types.SceneGroup {
		return
	}
	if err := run.ctx.Err(); err != nil {
		// 源消息撤回、展开关了、在关闭：发送都过不了许可，不再试
		p.opt.Logf("展开已取消（%s），不撤开始表情", reason(err))
		return
	}
	if run.reacted != "" {
		unset := types.Part{Kind: types.PartReact, Ref: s.Inbound.PlatformID, EmojiID: run.reacted, Unset: true}
		if _, err := p.send(run, []types.Part{unset}, nil); err != nil {
			p.opt.Logf("撤开始表情未入队（%s），照样换上结果表情", reason(err))
		}
	}
	final := ""
	switch {
	case delivered:
		final = e.DoneEmoji
	case failed:
		final = e.FailEmoji
	}
	if final == "" {
		return
	}
	if _, err := p.send(run, []types.Part{{Kind: types.PartReact, Ref: s.Inbound.PlatformID, EmojiID: final}}, nil); err != nil {
		p.warnf("结果表情未入队（%s）", reason(err))
	}
}
func (p *Plugin) run(j job) {
	f := &linkFlow{p: p, j: j}
	if j.run != nil {
		defer func() { p.mu.Lock(); delete(p.active, j.run.state.JobID); p.mu.Unlock(); j.run.cancel() }()
		p.start(j)
		// 收尾统一在这里（后注册先跑，在撤任务之前）：中途从哪里 return（关闭、许可撤了、记关联失败）都撤开始表情、换结果表情。
		// 以前几处提前 return 跳过了收尾，开始表情一直挂着
		defer func() {
			if f.unknown && !f.delivered && !f.failed {
				p.opt.Logf("结果不明、没有一段确认送到：不贴完成也不贴失败表情，只撤开始表情")
			}
			p.conclude(j.run, j.snapshot.Settings.Expand, f.delivered, f.failed)
		}()
		p.stage(j.run, "metadata")
	}
	f.links = j.links
	// 只为展开取数的任务（没有“给她看”）先丢掉设置里关了的网站和页面类型：不取数，也不回失败文字
	if j.run != nil && !j.context {
		off := j.snapshot.Settings.Expand
		if kept := slices.DeleteFunc(slices.Clone(f.links), func(l linkreader.Link) bool { return kindOff(off, l) }); len(kept) < len(f.links) {
			p.debugf("%d 条链接的网站或页面类型在设置里关了，不取数", len(f.links)-len(kept))
			for _, l := range f.links {
				if kindOff(off, l) {
					p.traceSkip(l, nil, noteKindOff)
				}
			}
			f.links = kept
		}
	}
	f.run()
}

// maxEmbedTranslations 是长文里嵌入推文同时送翻的上限（ADR-391）：再多就排队，和分段翻译共用任务时限。
const maxEmbedTranslations = 4

// translating 是一条链接正在进行的几次翻译：帖子正文（含投票选项）、被引推文、X 长文各一次独立的调用，同时发出，
// 各自有自己的时限、缓存和失败退回：一段翻不出来只有那一段发原文（2026-11-14）。某一段不翻就是 nil。
type translating struct {
	main, quote, article, quoteArticle <-chan linktranslate.Outcome
	embeds                             []<-chan linktranslate.Outcome
	embedKeys                          []string
}

// startTranslate 在取到数据后立刻开始翻译帖子正文、被引推文、长文、被引长文和长文里嵌的推文，和别的链接取数、媒体下载重叠；
// 不该翻（没接翻译、开关关、模板没用译文、没有可翻的正文）返回 nil。
func (p *Plugin) startTranslate(j job, r *linkreader.Result, fetchErr error) *translating {
	if p.opt.Expand == nil || p.opt.Expand.Translate == nil || j.run == nil || fetchErr != nil {
		return nil
	}
	o := j.snapshot.Settings.Expand
	if !o.TranslateOn(r.Platform) || !o.KindEnabled(r.Platform, r.Kind) {
		return nil
	}
	src := linkexpand.TranslationSources(r, BuildOptions(o)) // 送去翻译的原文和发出去的正文用同一套排版设置（去不去 hashtag）
	url, ex, ctx := r.CleanURL, o.Translate, j.run.ctx
	start := func(text string) <-chan linktranslate.Outcome {
		if text == "" {
			return nil
		}
		ch := make(chan linktranslate.Outcome, 1)
		go func() { ch <- p.opt.Expand.Translate(ctx, url, text, ex) }()
		return ch
	}
	t := &translating{main: start(src.Main), quote: start(src.Quote), article: start(src.Article), quoteArticle: start(src.QuoteArticle)}
	// 长文里嵌的推文可能很多：最多 4 条同时翻，超过的排队；和分段翻译共用同一份任务时限（j.run.ctx）。
	sem := make(chan struct{}, maxEmbedTranslations)
	for _, e := range src.Embeds {
		text := e.Text
		ch := make(chan linktranslate.Outcome, 1)
		go func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				ch <- linktranslate.Outcome{Status: linktranslate.StatusFailed, Reason: "canceled"}
				return
			}
			defer func() { <-sem }()
			ch <- p.opt.Expand.Translate(ctx, url, text, ex)
		}()
		t.embeds = append(t.embeds, ch)
		t.embedKeys = append(t.embedKeys, e.Key)
	}
	if t.main == nil && t.quote == nil && t.article == nil && t.quoteArticle == nil && len(t.embeds) == 0 {
		return nil
	}
	return t
}

// takeTranslations 等各段翻译的结果（几次调用是并发的，各有时限）；每一段没出译文就只发那一段的原文。
func (p *Plugin) takeTranslations(run *expansionRun, t *translating) linkexpand.Translations {
	if t == nil {
		return linkexpand.Translations{}
	}
	tr := linkexpand.Translations{
		Main:         p.takeTranslation(run, t.main, ""),
		Quote:        p.takeTranslation(run, t.quote, "quote:"),
		Article:      p.takeTranslation(run, t.article, "article:"),
		QuoteArticle: p.takeTranslation(run, t.quoteArticle, "quote_article:"),
	}
	for i, ch := range t.embeds {
		if text := p.takeTranslation(run, ch, "embed:"); text != "" {
			if tr.Embeds == nil {
				tr.Embeds = map[string]linkexpand.Embedded{}
			}
			tr.Embeds[t.embedKeys[i]] = linkexpand.Embedded{Text: text}
		}
	}
	return tr
}

// takeTranslation 等一次翻译结果（翻译自己有时限，到点就回空结果）；没出译文只发原文，运行页留一行，不写错误文案。
// 每次没用上译文都记一行（ADR-391）：太短、已是目标语言、太长、模型说不用翻、命中“不用翻”缓存、失败、超时、分段失败
// 都带原因标签；没配模型不算（运行页不记）。tagPrefix 区分是哪一段（空是帖子正文）。
func (p *Plugin) takeTranslation(run *expansionRun, ch <-chan linktranslate.Outcome, tagPrefix string) string {
	if ch == nil {
		return ""
	}
	var out linktranslate.Outcome
	select {
	case out = <-ch:
	case <-run.ctx.Done():
		return ""
	}
	if out.Text == "" {
		if out.Reason != "no_model" {
			outcome := taskrun.OutcomeSkipped
			switch out.Status {
			case linktranslate.StatusFailed:
				outcome = taskrun.OutcomeFailed
			case linktranslate.StatusTimeout:
				outcome = taskrun.OutcomeTimeout
			}
			p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassTranslate, nil, taskrun.Opts{Outcome: outcome, Ms: out.Took.Milliseconds(), Tag: tagPrefix + out.Reason})
		}
		return ""
	}
	return out.Text
}

// noteLegacyForwardOver 在 x、telegram 的旧键 forwardOver 还写着值时提示一次：这两个站点已按实际发出的字节数判断，
// 旧值不生效、也不自动迁移（ADR-391）；旧配置照样读得进来，不报配置错。
func (p *Plugin) noteLegacyForwardOver(o config.LinkExpand) {
	for _, key := range []string{"x", "telegram"} {
		if o.Sites[key].ForwardOver != nil {
			p.legacyOverOnce.Do(func() {
				p.warnf("linkReader.expand.sites.%s.forwardOver 已不生效：x、telegram 改按实际发出的字节数判断，请改用 forwardOverBytes（默认 16500）", key)
			})
			return
		}
	}
}

// kindOff 说一条链接的网站或页面类型在设置里关了。短链要解析后才知道页面类型，这里只看网站开关。
func kindOff(e config.LinkExpand, l linkreader.Link) bool {
	if l.Kind == "short" {
		return !e.PlatformEnabled(l.Platform)
	}
	return !e.KindEnabled(l.Platform, l.Kind)
}

// parseFailedText 是私聊里解析失败时回的那句话（用户 2026-11-10 定，写死不开配置键）。
const parseFailedText = "没解析出来"

// noteFailure 只在私聊回失败那句：私聊协议端贴不了表情，不回的话开始状态之后就没有下文。群里不发文字，由 conclude 换失败表情。
func (p *Plugin) noteFailure(run *expansionRun) {
	s := run.source
	if s.Inbound.Conv.Scene != types.ScenePrivate {
		return
	}
	part := types.Part{Kind: types.PartMessage, Quote: s.Inbound.PlatformID, Body: types.TextBody(parseFailedText)}
	if _, err := p.send(run, []types.Part{part}, nil); err != nil {
		p.errorf("失败说明未入队（%s）", reason(err))
	}
}

// explainOff 在私聊里补一句：开始状态已经发出去了，短链解析后才发现这类页面关了，不能让状态没有下文。群里靠撤掉开始表情收尾。
func (p *Plugin) explainOff(run *expansionRun) {
	s := run.source
	if s.Inbound.Conv.Scene != types.ScenePrivate {
		return
	}
	part := types.Part{Kind: types.PartMessage, Quote: s.Inbound.PlatformID, Body: types.TextBody("这类页面在设置里关了，没有展开")}
	if _, err := p.send(run, []types.Part{part}, nil); err != nil {
		p.errorf("已关页面的说明未入队（%s）", reason(err))
	}
}

// ForwardOptions 把配置里的合并转发外观交给 linkexpand。
func ForwardOptions(f config.LinkForward) linkexpand.ForwardOptions {
	return linkexpand.ForwardOptions{NodeName: f.NodeName, Source: f.Source, News: slices.Clone(f.News), Summary: f.Summary, Prompt: f.Prompt}
}

// BuildOptions 把展开设置换成 linkexpand 的排版选项；插件发送、送翻译的原文和预览都从这里取，免得新加的配置键只接上一处。
// 模板表是复制的，调用方（预览）可以改。Translation、ForwardMedia 每条链接不同，由调用方填。
func BuildOptions(o config.LinkExpand) linkexpand.Options {
	tpl := make(map[string]string, len(o.Templates))
	maps.Copy(tpl, o.Templates)
	return linkexpand.Options{Templates: tpl, Sites: siteSettings(o.Sites), VideoEnabled: o.Video.Enabled, Forward: ForwardOptions(o.Forward),
		PanoramaOriginals: o.PanoramaOriginals, KeepTrailingHashtags: !o.Text.StripTrailingHashtags, DirectImageMax: o.DirectImageMax,
		ForwardVideo: o.Send.ForwardVideo, TextCard: o.TweetCard.TextOnly}
}

// siteSettings 把配置里各站点的发送设置交给 linkexpand（两边的类型独立，免得发送端依赖配置包）。
func siteSettings(in map[string]config.LinkSite) map[string]linkexpand.SiteSetting {
	out := make(map[string]linkexpand.SiteSetting, len(in))
	for k, v := range in {
		out[k] = linkexpand.SiteSetting{Forward: v.Forward, ForwardOver: v.ForwardOver, ForwardOverBytes: v.ForwardOverBytes, DescLimit: v.DescLimit, ShowDesc: v.ShowDesc}
	}
	return out
}
