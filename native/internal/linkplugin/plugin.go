// Package linkplugin 是链接解析接进渠道和内核的适配器（规格-链接解析 第八节"给她看"，第十三节第 4 步）：
// 唯一同时认识 linkreader、channel、turn 的地方（linkreader 不引渠道和内核）。
//
//   - 观察者（channel.Observer / 本地入库回调）：入库以后从文字段和卡片里提链接，每条最多 MaxLinks 个（ADR-217），排进自己的队列；
//     队列里的活取数（共享缓存、在途合并）再和那条消息关联存库，失败也记一次。队列满了丢、记日志（不保证每条都送到）。
//   - 拼参考信息（turn OnContext）：回合开头和插话时，把这个会话里她看到过、还没给过的链接资料追加在末尾（ADR-022：
//     晚到的不改已提交的行，在下一次输入的末尾补）。这一批里还有在取的，最多等 Wait。
//   - 详情工具 read_link_detail：只能查这个会话里她看到过的消息（store.LinkNotes 按会话、角色查），不接受任意网址。
//
// 自动展开共享同一取数任务，通过注册的 automation 出口发送，权限与角色上下文分别判断。
package linkplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktrunc"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/turn"
)

const (
	// ID 是插件在内核里的名字，也是观察者的名字
	ID = "link-reader"

	stateKey = "link-reader"

	// briefMax 是一条消息的链接摘要总长（rune，省略号算进去；ADR-192）
	briefMax = 40
	// pageMax、pageBlocks 是详情工具一页的正文上限和块数（规格 8.1）
	pageMax    = 1500
	pageBlocks = 20
	// recentNotes 是拼参考信息时看最近多少条带链接的消息；noteAge 以前的不再补
	recentNotes = 20
	noteAge     = 24 * time.Hour
)

// Options 是装配处给的东西；零值用括号里的默认
type Options struct {
	Service        *linkreader.Service
	Stores         store.Stores
	Logf           func(format string, args ...any) // 只写计数和原因，不写原文、链接
	Warnf          func(format string, args ...any) // WARN（取数失败、入队失败等，会自己接上或降级）；nil 时用 Logf
	Errorf         func(format string, args ...any) // ERROR（结果、说明没能入队，用户收不到）；nil 时用 Logf
	Debugf         func(format string, args ...any) // DEBUG（设置里关了这一类，正常不展开）；nil 时用 Logf
	Degrade        *taskrun.Degrader                // 队列满等"没取"的情况记进 task_runs；nil 就不记
	Now            func() time.Time
	MaxLinks       int           // 每条消息最多处理几个链接（3，ADR-217）
	Workers        int           // 同时取几条消息（2）
	Queue          int           // 队列长度（256）
	JobTimeout     time.Duration // 一条消息的所有链接最多取多久（45 秒）
	Wait           time.Duration // 拼参考信息时最多等在取的多久（2 秒；内核给这个钩子 5 秒）
	Settings       func() Snapshot
	ContextAllowed func(channel.Stored) bool
	Expand         *ExpandOptions
}

type job struct {
	conv     string
	seq      int64
	links    []linkreader.Link
	context  bool
	limit    int
	snapshot Snapshot
	run      *expansionRun
}

// Plugin 同时是渠道的观察者和内核的插件。生命周期：New 起队列，Close 取消在取的、等它们退出；Close 要在 Service.Close、关库之前
type Plugin struct {
	opt    Options
	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan job
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	pending map[string]*pend // 会话 → 还在取的消息
	active  map[string]*expansionRun
	// verdicts 记图被协议端拒收的结论（按场合加图片内容哈希），见 clone.go
	verdicts verdictBook
	plans    planBook       // 最近发出去的计划是哪个链接的（迟到回声的日志用），见 echo.go
	quirks   quirkBook      // 会话的合并转发里放不了视频 / 引用（6 小时），见 echo.go
	guards   guardBook      // 大图等不到回声、按没送到换形式时开的晚到回声看守，见 late.go
	lateWG   sync.WaitGroup // 晚到回声触发的撤回协程，Close 等它们退出
	// tracer 是宿主给的插件日志（Setup 时换上，停掉时清掉）；nil 不记。取数和发送都在队列的协程里，所以要原子地换
	tracer atomic.Pointer[func(plugintrace.Event)]
	// legacyOverOnce 只提示一次“站点还写着 forwardOver（x、telegram 已改按字节）”
	legacyOverOnce sync.Once
}

type pend struct {
	n    int
	done chan struct{}
}

// New 造插件并起队列
func New(opt Options) (*Plugin, error) {
	if opt.Service == nil || opt.Stores == nil {
		return nil, errors.New("链接插件要 Service 和 Stores")
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.MaxLinks <= 0 {
		opt.MaxLinks = 3
	}
	if opt.Workers <= 0 {
		opt.Workers = 2
	}
	if opt.Queue <= 0 {
		opt.Queue = 256
	}
	if opt.JobTimeout <= 0 {
		opt.JobTimeout = 45 * time.Second
	}
	if opt.Wait <= 0 {
		opt.Wait = 2 * time.Second
	}
	if opt.Expand != nil && (opt.Expand.Outlet == nil || opt.Expand.Admit == nil || opt.Expand.Permit == nil || opt.Expand.Prepare == nil || opt.Expand.Outcomes == nil || opt.Expand.Block == nil) {
		return nil, errors.New("链接展开缺少注册出口或许可、准备接口")
	}
	if opt.Settings == nil {
		opt.Settings = func() Snapshot { return Snapshot{Settings: config.DefaultLinks()} }
	}
	if opt.ContextAllowed == nil {
		opt.ContextAllowed = func(s channel.Stored) bool { return s.Observed }
	}
	p := &Plugin{opt: opt, jobs: make(chan job, opt.Queue), pending: map[string]*pend{}, active: map[string]*expansionRun{}}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if opt.Expand != nil {
		if eo, ok := opt.Expand.Outlet.(echoOutbound); ok {
			eo.OnClaim(p.onClaim) // 迟到的回声由出口认领后通知这里，记一行日志
		}
	}
	for range opt.Workers {
		p.wg.Add(1)
		go p.work()
	}
	return p, nil
}

// Close 不再收新的，取消在取的，等队列的协程退出。排着没取的丢掉（下次有人再发同一个链接照样取）
// warnf、debugf 按级别写；没注入时退回 Logf（级别在 applog 里就是 INFO）
func (p *Plugin) warnf(format string, args ...any) {
	if p.opt.Warnf != nil {
		p.opt.Warnf(format, args...)
		return
	}
	p.opt.Logf(format, args...)
}

func (p *Plugin) errorf(format string, args ...any) {
	if p.opt.Errorf != nil {
		p.opt.Errorf(format, args...)
		return
	}
	p.opt.Logf(format, args...)
}

func (p *Plugin) debugf(format string, args ...any) {
	if p.opt.Debugf != nil {
		p.opt.Debugf(format, args...)
		return
	}
	p.opt.Logf(format, args...)
}

func (p *Plugin) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	p.cancel()
	p.wg.Wait()
	p.lateWG.Wait()
	p.mu.Lock()
	for k, pd := range p.pending {
		close(pd.done)
		delete(p.pending, k)
	}
	p.mu.Unlock()
}

// OnStored 只提链入队；角色观察和展开消费在同一消息任务中协调。
// 补录来的、重复送到的不取不展开（规格-QQ收消息与补录 I-27：补录不触发回复；规格-链接自动展开：补录零触发），
// 但这个会话本来会展开时在插件日志里记一行 skip，查得到为什么没展开
func (p *Plugin) OnStored(s channel.Stored) {
	// 自己号发的、陌生人、自检、角色的消息永远不展开，先过滤（补录、重复送到的也一样，不写“补录”“去重”那行）
	if slices.Contains(s.Tags, channel.TagFromSelf) || slices.Contains(s.Tags, "stranger") || slices.Contains(s.Tags, "selftest") || s.Actor.Kind == channel.ActorCharacter {
		return
	}
	backfill := slices.Contains(s.Tags, channel.TagBackfill)
	if s.Duplicate || backfill {
		note := noteDuplicate
		if backfill {
			note = noteBackfill
		}
		p.traceIgnored(s, note)
		return
	}
	snapshot := p.snapshot()
	contextAllowed := snapshot.Settings.Context.Enabled && p.opt.ContextAllowed(s)
	expandAllowed := p.opt.Expand != nil && snapshot.Settings.Expand.Allows(s.Inbound.Conv) && senderOK(snapshot.Settings.Expand, s) && p.opt.Expand.Admit(s)
	if !expandAllowed {
		p.traceNotExpanded(s, snapshot)
	}
	if !contextAllowed && !expandAllowed {
		return
	}
	links, badCards := linksWithBare(s.Inbound.Segs, expandAllowed && snapshot.Settings.Expand.BareBiliID)
	for _, err := range badCards {
		// 卡片载荷解析不了：以前直接吞掉，查不到卡片里的链接为什么没处理。日志只写原因，不写载荷
		p.warnf("卡片解析不了（%v），这张卡片里的链接不处理", err)
		p.trace(plugintrace.Event{Site: "card", Route: "card", Result: plugintrace.Fail, Kind: "card", Error: err.Error()})
	}
	if len(links) == 0 {
		return
	}
	if expandAllowed {
		expandAllowed = slices.ContainsFunc(links, func(l linkreader.Link) bool {
			return expandable(snapshot.Settings.Expand, l)
		})
		if !expandAllowed && slices.ContainsFunc(links, func(l linkreader.Link) bool { return linkexpand.Supports(l.Platform, l.Kind) }) {
			// 有能展开的链接，但网站或页面类型在设置里关了：记一行，查得到为什么没展开
			p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassBlocked, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: "网站或页面类型已关"})
			for _, l := range links {
				if linkexpand.Supports(l.Platform, l.Kind) {
					p.traceSkip(l, nil, noteKindOff)
				}
			}
		}
		if !contextAllowed && !expandAllowed {
			return
		}
	}
	limit := p.opt.MaxLinks
	if expandAllowed {
		limit = snapshot.Settings.Expand.MaxLinks
	}
	j := job{conv: s.Inbound.Conv.String(), seq: s.Seq, links: links, limit: limit, context: contextAllowed, snapshot: snapshot}
	p.enqueue(j, s, expandAllowed)
}

// traceIgnored 记补录、重复送到的消息里没展开的链接：只在这个会话本来会展开（在名单里、发消息的人能触发）、链接也能展开时记，
// 别的消息不写，免得每条都一行
func (p *Plugin) traceIgnored(s channel.Stored, note string) {
	if p.opt.Expand == nil || p.tracer.Load() == nil {
		return
	}
	e := p.snapshot().Settings.Expand
	if !e.Allows(s.Inbound.Conv) || !senderOK(e, s) || !p.opt.Expand.Admit(s) {
		return
	}
	links, _ := linksWithBare(s.Inbound.Segs, e.BareBiliID)
	for _, l := range links {
		if expandable(e, l) {
			p.traceSkip(l, nil, note)
		}
	}
}

// traceNotExpanded 记接了展开（QQ）却没展开的链接为什么没展开：开关没开、白名单外、黑名单内、发消息的人或号不能触发。
// 只记能展开的链接（网站和页面类型开着），别的消息不写
func (p *Plugin) traceNotExpanded(s channel.Stored, snapshot Snapshot) {
	if p.opt.Expand == nil || p.tracer.Load() == nil || s.Inbound.Conv.Channel != "qq" {
		return
	}
	e := snapshot.Settings.Expand
	note := noteNotAdmit
	switch {
	case !e.Enabled:
		note = noteExpandOff
	case !e.Allows(s.Inbound.Conv) && e.Mode == "blacklist":
		note = noteBlacklist
	case !e.Allows(s.Inbound.Conv):
		note = noteNotListed
	case !senderOK(e, s):
		note = noteSender
	}
	links, _ := linksWithBare(s.Inbound.Segs, e.BareBiliID)
	for _, l := range links {
		if expandable(e, l) {
			p.traceSkip(l, nil, note)
		}
	}
}

// expandable 说一条链接值不值得为展开排队：短链要等解析后才知道页面类型，先只看网站开关；
// 其余要有发送格式，且网站和页面类型的开关都开着。
func expandable(e config.LinkExpand, l linkreader.Link) bool {
	if l.Kind == "short" {
		return e.PlatformEnabled(l.Platform)
	}
	return linkexpand.Supports(l.Platform, l.Kind) && e.KindEnabled(l.Platform, l.Kind)
}

// ObserveLocal 只接已提交的本地主人消息；本地不创建展开任务。
func (p *Plugin) ObserveLocal(conversation string, seq int64, text string) {
	snapshot := p.snapshot()
	if !snapshot.Settings.Context.Enabled {
		return
	}
	links := Links([]types.Seg{{Kind: types.SegText, Text: text}})
	if len(links) == 0 {
		return
	}
	p.enqueue(job{conv: conversation, seq: seq, links: links, limit: p.opt.MaxLinks, context: true, snapshot: snapshot}, channel.Stored{}, false)
}

func (p *Plugin) enqueue(j job, s channel.Stored, expandAllowed bool) {
	// URL 子串不能把整条长消息留在后台队列里。
	for i := range j.links {
		j.links[i].Raw = strings.Clone(j.links[i].Raw)
		j.links[i].ID = strings.Clone(j.links[i].ID)
	}
	if !p.push(j, s, expandAllowed) && expandAllowed {
		// 队列满了丢掉的：能展开的链接各记一行没发（插件日志是宿主写文件，不在锁里记）
		for _, l := range j.links {
			if expandable(j.snapshot.Settings.Expand, l) {
				p.traceSkip(l, nil, noteQueueFull)
			}
		}
	}
}

// push 把任务放进队列；队列满了丢掉，返回 false（已关闭也返回 true：不算丢）
func (p *Plugin) push(j job, s channel.Stored, expandAllowed bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return true
	}
	if len(p.jobs) == cap(p.jobs) {
		p.warnf("队列满了（%d），这条消息的 %d 个链接不取", cap(p.jobs), len(j.links))
		// 记一行 task_runs（类别 queue_full），页面上能看到"链接没取"有多少次；不开提醒（没有"恢复"的时刻，开了就清不掉）
		p.opt.Degrade.Degrade(taskrun.ModuleLink, taskrun.ClassQueueFull, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: "队列满"})
		return false
	}
	if expandAllowed {
		// 后台只需源消息身份；正文和媒体由已提取的链接及 Result 提供。
		s.Inbound = types.Inbound{Kind: s.Inbound.Kind, Account: s.Inbound.Account, Conv: s.Inbound.Conv, PlatformID: s.Inbound.PlatformID}
		ctx, cancel := context.WithCancel(p.ctx)
		j.run = &expansionRun{source: s, ctx: ctx, cancel: cancel, state: ActiveJob{JobID: store.NewID(p.opt.Now()), Conversation: j.conv, Version: j.snapshot.Version, Stage: "queued"}}
		p.active[j.run.state.JobID] = j.run
	}
	p.jobs <- j
	pd := p.pending[j.conv]
	if pd == nil {
		pd = &pend{done: make(chan struct{})}
		p.pending[j.conv] = pd
	}
	pd.n++
	return true
}

// Links 是一条消息里的链接：文字段和卡片（完整载荷）里的，按出现的先后、按干净链接去重
func Links(segs []types.Seg) []linkreader.Link { ls, _ := linksWithBare(segs, false); return ls }

// linksWithBare 同 Links，bare 为真时也认裸的 B 站号；badCards 是解析不了的卡片的原因（调用方记日志）
func linksWithBare(segs []types.Seg, bare bool) (out []linkreader.Link, badCards []error) {
	seen := map[string]bool{}
	add := func(ls []linkreader.Link) {
		for _, l := range ls {
			if !seen[l.Clean] {
				seen[l.Clean] = true
				out = append(out, l)
			}
		}
	}
	for _, s := range segs {
		switch s.Kind {
		case types.SegText:
			add(linkreader.ExtractWithBare(s.Text, bare))
		case types.SegCard:
			if s.CardJSON != "" {
				ls, _, err := linkreader.ParseCard(s.CardJSON)
				if err != nil {
					badCards = append(badCards, err)
				}
				add(ls)
			}
		}
	}
	return out, badCards
}

func (p *Plugin) work() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case j := <-p.jobs:
			p.run(j)
			p.finish(j.conv)
		}
	}
}

// reason 给日志用：写清是哪一类原因，不写网址、号码和外部原文（ADR-311 第 2 条）。
// 取数错误写类别、来源和状态码；超时、取消写类别；别的错误写自己的说明文字（含网址或长数字时只写错误类型）。
func reason(err error) string {
	var fe *linkreader.FetchError
	if errors.As(err, &fe) {
		s := fe.Kind
		if fe.Source != "" {
			s += "，来源 " + fe.Source
		}
		if fe.Status != 0 {
			s += fmt.Sprintf("，HTTP %d", fe.Status)
		}
		if fe.Trail != "" {
			s += "，线路 " + fe.Trail // 哪一路慢、各花多久（备用线路也超时时只看来源会误以为只有最后一路出事）
		}
		if fe.Detail != "" {
			s += "，底层 " + fe.Detail // EOF、connection reset、哪一步超时：只看 network 查不出是代理断了还是对方拒了
		}
		switch fe.Reason {
		case "channel_hidden":
			s += "，网页版不显示这个频道"
		case "private_channel":
			s += "，私有频道需要登录"
		}
		return s
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if err == nil {
		return "nil"
	}
	msg := err.Error()
	if strings.Contains(msg, "://") || longDigits.MatchString(msg) {
		return fmt.Sprintf("error %T", err)
	}
	if r := []rune(msg); len(r) > 80 {
		msg = string(r[:80]) + "…"
	}
	return "error：" + msg
}

var longDigits = regexp.MustCompile(`\d{6,}`)

func (p *Plugin) finish(conv string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pd := p.pending[conv]
	if pd == nil {
		return
	}
	if pd.n--; pd.n <= 0 {
		close(pd.done)
		delete(p.pending, conv)
	}
}

// waitPending 等这个会话里在取的都取完，最多 d；关机、ctx 到期就不等了
func (p *Plugin) waitPending(ctx context.Context, conv string, d time.Duration) {
	p.mu.Lock()
	pd := p.pending[conv]
	p.mu.Unlock()
	if pd == nil {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-pd.done:
	case <-t.C:
	case <-ctx.Done():
	case <-p.ctx.Done():
	}
}

// ---------- 内核 ----------

// ID 实现 turn.Plugin
func (p *Plugin) ID() string { return ID }

// Manifest 是清单（规格-插件两层 第二节第 1 条）
func (p *Plugin) Manifest() turn.Manifest {
	return turn.Manifest{Name: "链接解析", Sub: "读取聊天里的链接，把资料写进参考信息，她可以再查详情；打开自动展开后，展开的内容会发进所选会话"}
}

// Setup 实现 turn.Plugin：拼参考信息、详情工具，换上宿主给的插件日志（停掉时清掉）
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	r.OnContext(p.context)
	if _, err := r.OnTool(turn.Tool{Decl: Decl, Exec: p.detail, For: func(_ audience.Audience) bool { return p.snapshot().Settings.Context.Enabled }}); err != nil {
		return nil, err
	}
	p.setTrace(r.Trace)
	return func() { p.setTrace(nil) }, nil
}

// context 把这个会话里她看到过、还没给过的链接资料追加在末尾。给过的记在实例的 State 里，进了实例才记（OnAccepted）
func (p *Plugin) context(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
	if !p.snapshot().Settings.Context.Enabled {
		return nil, nil
	}
	p.waitPending(ctx, in.Conversation, p.opt.Wait)
	st, err := p.opt.Stores.ForConversation(in.Conversation)
	if err != nil {
		return nil, nil // 未登记平台的会话没有链接资料
	}
	notes, err := st.LinkNotes(ctx, in.Conversation, in.CharacterID, 0, recentNotes)
	if err != nil {
		return nil, err
	}
	given, _ := in.State[stateKey].(map[string]bool)
	since := p.opt.Now().Add(-noteAge).UnixMilli()
	var lines, keys []string
	for _, g := range group(notes) {
		if g[0].CreatedAt < since {
			continue
		}
		var fresh []store.LinkNote
		for _, n := range g {
			if k := noteKey(n); !given[k] {
				fresh = append(fresh, n)
				keys = append(keys, k)
			}
		}
		if len(fresh) > 0 {
			lines = append(lines, noteLine(fresh, len(Links(segsOf(g[0])))-len(g), p.opt.Now()))
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	lines = append([]string{"[链接资料] 聊天里链接的内容摘要（编号是消息编号，要看全文用 read_link_detail）："}, lines...)
	return &turn.Contribution{Lines: lines, OnAccepted: func() {
		m, _ := in.State[stateKey].(map[string]bool)
		if m == nil {
			m = map[string]bool{}
			in.State[stateKey] = m
		}
		for _, k := range keys {
			m[k] = true
		}
	}}, nil
}

func noteKey(n store.LinkNote) string { return fmt.Sprintf("%d|%s", n.Seq, n.CleanURL) }

// group 把同一条消息的几个链接放在一起（LinkNotes 已经按 seq 排好）
func group(notes []store.LinkNote) [][]store.LinkNote {
	var out [][]store.LinkNote
	for _, n := range notes {
		if len(out) > 0 && out[len(out)-1][0].Seq == n.Seq {
			out[len(out)-1] = append(out[len(out)-1], n)
			continue
		}
		out = append(out, []store.LinkNote{n})
	}
	return out
}

type stored struct {
	Segs []types.Seg `json:"segs"`
	From types.User  `json:"from"`
}

func parseStored(n store.LinkNote) stored {
	var s stored
	if n.Segments.Valid {
		_ = json.Unmarshal([]byte(n.Segments.String), &s)
	}
	// 本地主人消息只有原文和可信身份，不制造一份 QQ 信封写回事件。
	if len(s.Segs) == 0 && n.Text != "" {
		s.Segs = []types.Seg{{Kind: types.SegText, Text: n.Text}}
	}
	if n.ActorKind == "owner" && nameOf(s.From) == "" {
		s.From.Nick = "所有者"
	}
	return s
}

func segsOf(n store.LinkNote) []types.Seg { return parseStored(n).Segs }

func nameOf(u types.User) string {
	switch {
	case u.Card != "":
		return u.Card
	case u.Nick != "":
		return u.Nick
	}
	return u.ID
}

// noteLine 是一条消息的链接资料：「编号 12（名字 21:05 发的）：B站视频《……》｜UP；X：……（另有 1 条）」
func noteLine(ns []store.LinkNote, unprocessed int, now time.Time) string {
	s := parseStored(ns[0])
	at := time.UnixMilli(ns[0].CreatedAt).In(now.Location())
	var items []string
	for _, n := range ns {
		items = append(items, briefOf(n))
	}
	text := Brief(items, unprocessed)
	var again []string
	for _, n := range ns {
		if n.Previous > 0 {
			again = append(again, fmt.Sprintf("本会话之前发过 %d 次", n.Previous))
			break
		}
	}
	tail := ""
	if len(again) > 0 {
		tail = "（" + again[0] + "）"
	}
	return fmt.Sprintf("- 编号 %d（%s %02d:%02d 发的）：%s%s", ns[0].Seq, channel.EscapeName(nameOf(s.From)), at.Hour(), at.Minute(), text, tail)
}

// QuoteBrief 是被引用的那条消息的链接摘要，引用那一行用：「X 某某：正文开头（编号 12）」；没有关联是空串
func QuoteBrief(ns []store.LinkNote) string {
	if len(ns) == 0 {
		return ""
	}
	var items []string
	for _, n := range ns {
		items = append(items, briefOf(n))
	}
	return fmt.Sprintf("%s（编号 %d）", Brief(items, len(Links(segsOf(ns[0])))-len(ns)), ns[0].Seq)
}

func briefOf(n store.LinkNote) string {
	if len(n.Result) == 0 {
		return "链接没解析出来"
	}
	var r linkreader.Result
	if json.Unmarshal(n.Result, &r) != nil {
		return "链接没解析出来"
	}
	return ItemBrief(r)
}

// ItemBrief 是一个链接的短说法，确定性地拼（不调模型）：B站视频《标题》｜UP主、X 作者：正文开头、其余用标题或正文
func ItemBrief(r linkreader.Result) string {
	text := oneLine(r.Text.Value)
	title := oneLine(r.Title.Value)
	author := oneLine(r.Author.Name.Value)
	switch {
	case r.Platform == "bilibili" && r.Kind == "video":
		s := "B站视频《" + title + "》"
		if author != "" {
			s += "｜" + author
		}
		return s
	case r.Platform == "x":
		s := "X"
		if author != "" {
			s += " " + author
		}
		if text == "" {
			text = title
		}
		return s + "：" + text
	case title != "":
		return title
	case text != "":
		return text
	}
	return "链接没解析出来"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Brief 把几个链接的短说法拼成一条，总长 briefMax 个字（省略号算进去）：外部文字先转义再量；放不下的写"另有 N 条"，
// 没处理的（超过每条上限的）一起算进去
func Brief(items []string, unprocessed int) string {
	var b strings.Builder
	rest := 0
	for i, it := range items {
		it = channel.EscapeUserText(it)
		sep := ""
		if b.Len() > 0 {
			sep = "；"
		}
		left := len(items) - i - 1 + unprocessed
		tailLen := 0
		if left > 0 {
			tailLen = utf8.RuneCountInString(fmt.Sprintf("（另有 %d 条）", left))
		}
		room := briefMax - utf8.RuneCountInString(b.String()) - utf8.RuneCountInString(sep) - tailLen
		if room <= 1 {
			rest = len(items) - i + unprocessed
			break
		}
		if utf8.RuneCountInString(it) > room {
			it = linktrunc.Inline(it, room-utf8.RuneCountInString(linktrunc.Ellipsis)) // 截在最近的标点后补“……”，整条不超过 room
		}
		b.WriteString(sep + it)
		rest = left
	}
	if rest > 0 {
		b.WriteString(fmt.Sprintf("（另有 %d 条）", rest))
	}
	return b.String()
}

// SenderAllowed 说一条消息的发送者能不能触发展开（群里的发送者名单，config.LinkExpand.AllowsSender）。
// 插件收消息时和宿主发送前最后一道门（app.checkLinkParts，按库里的源消息）共用它，两处不会各算各的。
// 身份按 ActorKind、ActorID 认，不认名字：主人是 ActorOwner；别人是 ActorHuman，ID 是 "qq:<号>"，取不出号的当认不出（白名单不放、黑名单不拦）。
func SenderAllowed(e config.LinkExpand, conv types.Conversation, actorKind, actorID string) bool {
	qq := ""
	if actorKind == channel.ActorHuman {
		if n, ok := strings.CutPrefix(actorID, "qq:"); ok {
			qq = n
		}
	}
	return e.AllowsSender(conv, actorKind == channel.ActorOwner, qq)
}

func senderOK(e config.LinkExpand, s channel.Stored) bool {
	return SenderAllowed(e, s.Inbound.Conv, s.Actor.Kind, s.Actor.ID)
}
