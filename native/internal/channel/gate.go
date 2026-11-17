package channel

// 群聊闸门（规格-渠道中间件 第八节，规格-群聊闸门 第三～五节；第十五节第 5 步）：每条群消息过了拦截链以后，判断这一批值不值得让她"想一想"。
// 程序算、不调模型、很快；每次判断存一行（trace.db 的 gate_decisions），界面上悬停能看为什么没接。放行的批次交给 GateConfig.Release（Responder.Release）。
// 一个角色在一个群里一份窗口状态，存在 gate_state，重启后接着算（规格 3.3）。算式在 gate_calc.go（和 Bun 对拍过），这里管状态、库和先后。
//
// 一条群消息的路（顺序是固定的，每一步的判定都带一句理由记进 gate_decisions）：
//
//	只记录档            她不进来看，闸门不算、不调模型（主人 @ 她也不回）：off
//	拦截链接了          只记这一轮的可能性（"这小时热闹不热闹"），不判断、不放行：taken
//	这个渠道暂停了      通道门：这个渠道连着发不出去，闸门直接不放行：paused
//	频率是 0            只有主人叫到她才回：muted
//	叫到她（@、引用她的话、别名） 1 分钟内的一串合成一轮，等这一分钟过完再一起看：merged；
//	                    到点（或第一个）：保险丝（被叫到的到上限 2 倍才不接）→ 放行：forced
//	没叫到              记这一轮 → 空闲退避（连着看了没说话就先等）→ 动态门槛（可能性 ≥ 门槛）→ 保险丝（到上限先不主动接）→ 放行：enter / skip / backoff / fuse
//
// 出错（读库、存库）不放行，记一行日志（规格 第八节："出错 = 不放行"，和拦截插件出错放行相反）。
//
// 档位、频率、保险丝、通道门都是闸门自己的事：档位读 GateConfig.Mode（和出口用同一个钩子，GateModes 是照 gate_modes 的实现），
// 频率读 gate_rules 三层，保险丝读状态里的"这小时说了几次"（TurnDone 告诉它），通道门由出口告诉它每一段发出去没有（PartEnded）和连接状态（Resume）。
//
// 这一步没做的：多个角色同群时"只让一个进"（规格 第八节最后一段，等有第二个号再做，群聊闸门第六节）；私聊不过闸门（Handle 只管群，
// 私聊的放行由装配处直接调 Responder.Release）；本地群聊（渠道 console）和 Bun 一样不过闸门。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 闸门每次判断的结论（gate_decisions.verdict）。前七个和 Bun 一样；taken、paused、fuse 是渠道中间件新加的
const (
	VerdictOff     = "off"     // 这个群是只记录档
	VerdictTaken   = "taken"   // 拦截链接了：只计数
	VerdictPaused  = "paused"  // 通道门：这个渠道暂停了
	VerdictMuted   = "muted"   // 频率是 0，叫她的又不是主人
	VerdictMerged  = "merged"  // 1 分钟内又被叫，合进上一串
	VerdictForced  = "forced"  // 叫到她，放行
	VerdictEnter   = "enter"   // 过了门槛，放行
	VerdictSkip    = "skip"    // 没过门槛
	VerdictBackoff = "backoff" // 她刚连着几次看了没说话，先等
	VerdictFuse    = "fuse"    // 保险丝：这小时说多了
)

const defaultPauseAfter = 3 // 连着几段明确失败就暂停（初值，规格 第十九节 B12）

// GateConfig 是闸门的配置
type GateConfig struct {
	// Stores 是按渠道取库的那一组（ADR-199）：聊天库的三样（攒着的消息、最近窗口、按 seq 取一条）按会话的渠道取；
	// 窗口状态、判断记录、频率、档位在共用的 trace 里，用 TraceStore()
	Stores store.Stores
	Roster Roster // 要和入库一站、流水线用的是同一份（"叫到她"认她的每一个号靠它）
	// Release 是放行了一批：交给内核回（Responder.Release）。马上返回，不在闸门的锁里调
	Release func(types.Batch)
	// Mode 说一个会话现在是什么档（和出口、拦截链用同一个钩子）；nil 就是都说话。record 和不认识的值都是只记录
	Mode func(types.Conversation) string
	// Frequency 是配置里的默认频率（规格-群聊闸门 第四节，ADR-061 定 0.3）；0 用 DefaultFrequency。要默认就是 0 的话，往 gate_rules 里写一条 "*" 的 0
	Frequency float64
	// Jitter 是门槛抖动的幅度（ADR-077，±0.03）；0 用 GateJitter，负数是关掉
	Jitter float64
	// Rand 给抖动用的 [0,1) 随机数；nil 用 math/rand/v2
	Rand func() float64
	// Aliases 是主人填写的角色别名，空表只认 @ 和引用（ADR-187）。至少两字，短英文数字用词边界；和流水线用同一匹配函数。
	Aliases func(character string) []string
	// NoFuse 关掉保险丝（回放验收用：Bun 的回放没有保险丝，要拿同样的口径比）
	NoFuse bool
	// PauseAfter 是连着几段明确失败就暂停这个渠道（初值 3，规格 第八节"通道门"，第十九节 B12）；0 用 3
	PauseAfter int
	// OnPause 在一个渠道暂停、恢复时调一次（paused 为真是暂停），reason 不含聊天原文。守卫切到退路、QQ 掉线的提醒也挂这类钩子，等 native/internal/alert
	OnPause func(channel string, paused bool, reason string)
	// OnResume 每次 Resume 都调，包括尚未暂停时：第一段失败的提醒也要清。
	OnResume func(channel string)
	Clock    types.Clock    // nil 用系统的钟；合并一串 @ 的计时、"现在"都从它取
	Loc      *time.Location // 频率的时段（"22:00-07:00"）按哪个时区；nil 用本机的
	Logf     func(format string, args ...any)
	Warnf    func(format string, args ...any) // WARN：出错或降级，系统会自己接上；nil 时用 Logf
}

// Gate 是闸门
type Gate struct {
	cfg   GateConfig
	clock types.Clock
	ctx   context.Context
	stop  context.CancelFunc

	mu       sync.Mutex
	bursts   map[string]*gateBurst
	chans    map[string]*gateChan
	handoffs sync.WaitGroup

	afterDecide func() // 只给测试用：判断写完、交接检查关闭之前调

	mu2  sync.Mutex // 只护 addr
	addr map[addrKey]bool
}

type gateBurst struct {
	timer types.Timer
	d     Dispatch
}

// gateChan 是一个渠道的通道门状态
type gateChan struct {
	fails  int
	paused bool
	why    string
}

// NewGate 造闸门
func NewGate(cfg GateConfig) (*Gate, error) {
	if cfg.Stores == nil {
		return nil, fmt.Errorf("闸门需要库")
	}
	if cfg.Release == nil {
		return nil, fmt.Errorf("闸门需要知道放行的批次交给谁")
	}
	if err := cfg.Roster.validate(); err != nil {
		return nil, fmt.Errorf("名册不合法：%w", err)
	}
	if cfg.Frequency == 0 {
		cfg.Frequency = DefaultFrequency
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = GateJitter
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.PauseAfter == 0 {
		cfg.PauseAfter = defaultPauseAfter
	}
	if cfg.Loc == nil {
		cfg.Loc = time.Local
	}
	g := &Gate{cfg: cfg, clock: cfg.Clock, bursts: map[string]*gateBurst{}, chans: map[string]*gateChan{}, addr: map[addrKey]bool{}}
	if g.clock == nil {
		g.clock = SystemClock{}
	}
	g.ctx, g.stop = context.WithCancel(context.Background())
	return g, nil
}

// Close 关闭：不再判断，停掉等着合并的一串 @，等在途的交接做完；库要最后关。
func (g *Gate) Close() {
	g.stop()
	g.mu.Lock()
	for k, b := range g.bursts {
		b.timer.Stop()
		delete(g.bursts, k)
	}
	g.mu.Unlock()
	g.handoffs.Wait()
}

func (g *Gate) logf(format string, args ...any) {
	if g.cfg.Logf != nil {
		g.cfg.Logf(format, args...)
	}
}

func (g *Gate) warnf(format string, args ...any) {
	if g.cfg.Warnf != nil {
		g.cfg.Warnf(format, args...)
		return
	}
	g.logf(format, args...)
}

func gateKey(character string, conv types.Conversation) string {
	return character + "|" + conv.String()
}

// ───────────── 档位（规格-群聊闸门 10.1）和频率（第四节） ─────────────

// 档位在 gate_modes 里存的值（和 Bun 一样），对应出口认的三个值
const (
	storedOff   = "off"
	storedDry   = "dry"
	storedSpeak = "speak"
)

// GateModes 造"一个会话现在是什么档"的钩子，读 gate_modes（这个群自己的 > 所有群的 > def）。出口、拦截链、闸门共用它。
// 档位在共用的 trace 里，用 TraceStore()（不分平台）。只有平台的群有档位；私聊、本地会话都是说话（档位是群的概念，规格 10.1）。
// 读库出错按只记录办（宁可不发），记一行日志。def 是没设过时的默认档（新进的群也是）：规格 10.1 定的是试运行
func GateModes(stores store.Stores, def string, logf func(string, ...any)) func(types.Conversation) string {
	return func(c types.Conversation) string {
		if c.Scene != types.SceneGroup || c.Channel == "console" {
			return ModeSpeak
		}
		m, ok, err := stores.TraceStore().GateModeOf(c.String())
		if err != nil {
			if logf != nil {
				logf("读会话 %s 的档位出错：%v，按只记录办", c.String(), err)
			}
			return ModeRecord
		}
		if !ok {
			return def
		}
		switch m {
		case storedSpeak:
			return ModeSpeak
		case storedDry:
			return ModeDryRun
		case storedOff:
			return ModeRecord
		}
		return ModeRecord // 不认识的值
	}
}

func (g *Gate) modeOf(c types.Conversation) string {
	if g.cfg.Mode == nil {
		return ModeSpeak
	}
	m, err := safe(func() string { return g.cfg.Mode(c) })
	if err != nil {
		g.warnf("会话 %s 取档位%v，按只记录办", c.String(), err)
		return ModeRecord
	}
	return m
}

// FrequencyOf 说一个群现在生效的频率，和是哪一条规则定的（规格-群聊闸门 第四节）
func (g *Gate) FrequencyOf(conv types.Conversation, now time.Time) (FreqPick, error) {
	rows, err := g.cfg.Stores.TraceStore().GateRules(conv.String())
	if err != nil {
		return FreqPick{}, err
	}
	rules := make([]FreqRule, len(rows))
	for i, r := range rows {
		rules[i] = FreqRule{Conversation: r.Conversation, Slot: r.Slot, Frequency: r.Frequency, Until: r.Until}
	}
	return FrequencyFrom(rules, conv.String(), now.In(g.cfg.Loc), g.cfg.Frequency, false), nil
}

// ───────────── 状态和判断记录 ─────────────

func (g *Gate) load(key string) (GateState, int64, error) {
	raw, since, ok, err := g.cfg.Stores.TraceStore().LoadGateState(key)
	if err != nil {
		return GateState{}, 0, err
	}
	s := NewGateState()
	if ok {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return GateState{}, 0, fmt.Errorf("窗口状态解不开：%w", err)
		}
		if s.Rounds == nil {
			s.Rounds = [][2]float64{}
		}
		if s.Forced == nil {
			s.Forced = []int64{}
		}
		if s.Replies == nil {
			s.Replies = []int64{}
		}
	}
	return s, since, nil
}

func (g *Gate) save(key string, s GateState, since int64) {
	raw, err := json.Marshal(s)
	if err == nil {
		err = g.cfg.Stores.TraceStore().SaveGateState(key, string(raw), since, g.clock.Now().UnixMilli())
	}
	if err != nil {
		g.warnf("存窗口状态 %s 出错：%v", key, err)
	}
}

// decisionFields 是一次判断里可能有的数
type decisionFields struct {
	features *GateFeatures
	d        *GateDecision
	batch    int
}

func (g *Gate) log(b types.Batch, seq int64, at time.Time, freq float64, verdict, reason string, f decisionFields) int64 {
	row := store.GateDecisionRow{At: at.UnixMilli(), CharacterID: b.Character, Conversation: b.Conv.String(), Seq: seq, Batch: max(f.batch, 1), Frequency: freq, Verdict: verdict, Reason: reason}
	if f.features != nil {
		rec := *f.features
		rec.SelfRatio = math.Round(rec.SelfRatio*1000) / 1000
		rec.SinceSelfSec = math.Round(rec.SinceSelfSec*1000) / 1000
		if raw, err := json.Marshal(rec); err == nil {
			row.Features = string(raw)
		}
	}
	if d := f.d; d != nil {
		row.Probability, row.Threshold, row.Expected, row.Target, row.Actual = &d.Probability, &d.Threshold, &d.Expected, &d.Target, &d.Actual
	}
	id, err := g.cfg.Stores.TraceStore().AddGateDecision(row)
	if err != nil {
		g.warnf("会话 %s 记判断出错：%v", b.Conv.String(), err)
	}
	return id
}

// Decisions 读一个会话最近的判断，新的在前（界面悬停、回放验收用）
func (g *Gate) Decisions(conv types.Conversation, limit int) ([]store.GateDecisionRow, error) {
	return g.cfg.Stores.TraceStore().GateDecisions(conv.String(), limit)
}

// ───────────── 判断 ─────────────

var atTextRe = regexp.MustCompile(`@\S`)

// msgOf 把库里的一行排成交给拦截链、内核的 Msg。addressed 为真才算 Addressed（@ 她的号、引用她说过的话、别名）：
// 攒着的那几条要算，最近 5 分钟的窗口只用来数占比，不用算（引用要查库）
func (g *Gate) msgOf(b types.Batch, r store.PendingRow, addressed bool) types.Msg {
	m := types.Msg{Seq: r.Seq, Time: time.UnixMilli(r.CreatedAt), Who: r.ActorID, Origin: r.Origin, Self: gateSelf(r, b.Character)}
	if r.Segments.Valid {
		m.Segs, m.Name = segsAndName(r.Segments.String)
	}
	if addressed && !m.Self {
		m.Addressed = g.addressedRow(b, r, m.Segs)
	}
	return m
}

// addressedRow：一行是不是叫到了这个角色。攒着没放行的那几条，每来一条新消息都要再算一遍，叫到了的记下来
func (g *Gate) addressedRow(b types.Batch, r store.PendingRow, segs []types.Seg) bool {
	if !g.cfg.Roster.CanTrigger(b.Conv.Channel, r.ActorID) {
		return false
	}
	k := addrKey{b.Character, b.Conv.String(), r.Seq}
	g.mu2.Lock()
	v, ok := g.addr[k]
	g.mu2.Unlock()
	if ok {
		return v
	}
	db, err := g.cfg.Stores.For(b.Conv.Channel)
	if err != nil {
		g.warnf("会话 %s 取库出错：%v，这一条当没叫到", b.Conv.String(), err)
		return false
	}
	v = addressedBy(db, g.cfg.Roster, b.Conv, b.Character, segs, g.warnf) || aliasInSegs(g.cfg.Aliases, b.Character, segs)
	if v { // 只记"叫到了"：这是不会变的事实；"没叫到"可能是引用的那条还没存进来、或者查库出错，下次再算
		g.mu2.Lock()
		if len(g.addr) >= 8192 {
			clear(g.addr)
		}
		g.addr[k] = true
		g.mu2.Unlock()
	}
	return v
}

type addrKey struct {
	character, conv string
	seq             int64
}

// gateSelf：是她自己说的。她的号发出去、但不是她说的（真人登录她的号手动发的，或以前别的程序用这个号说的）不算
func gateSelf(r store.PendingRow, character string) bool {
	return r.ActorKind == ActorCharacter && r.ActorID == character && r.Origin != OriginHuman && r.Origin != OriginExternal
}

// gateMsgOf 排成闸门算特征用的
func (g *Gate) gateMsgOf(b types.Batch, r store.PendingRow, m types.Msg) GateMsg {
	atOther := false
	if len(m.Segs) > 0 {
		mine := g.cfg.Roster.accountsOf(b.Conv.Channel, b.Character)
		for _, s := range m.Segs {
			if s.Kind == types.SegAt && s.Target != types.AtAll && !slices.Contains(mine, s.Target) {
				atOther = true
			}
		}
		// 段里没有 @、但文字里有 "@某人"（以前存的、没解成段的）也算 @ 了别人
		if !atOther && !slices.ContainsFunc(m.Segs, func(s types.Seg) bool { return s.Kind == types.SegAt }) {
			atOther = atTextRe.MatchString(r.Text)
		}
	} else {
		atOther = atTextRe.MatchString(r.Text)
	}
	return GateMsg{At: m.Time, Text: r.Text, Self: m.Self, AtOther: atOther, Mentions: m.Addressed}
}

// pending 是她上次看完（since）以后别人说的话：最多最近 MaxPending 条，撤回的、她自己的、插件自动发的、补录的不算
func (g *Gate) pending(b types.Batch, since int64) (rows []store.PendingRow, msgs []types.Msg, gm []GateMsg, earlier int, err error) {
	db, err := g.cfg.Stores.For(b.Conv.Channel)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	rows, earlier, err = db.PendingMessages(b.Conv.String(), b.Character, since, MaxPending, EarlierCap)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	for _, r := range rows {
		m := g.msgOf(b, r, true)
		msgs = append(msgs, m)
		if g.cfg.Roster.CanTrigger(b.Conv.Channel, r.ActorID) {
			gm = append(gm, g.gateMsgOf(b, r, m))
		}
	}
	return rows, msgs, gm, earlier, nil
}

// recent 是这个群最近（5 分钟多一点）的消息，含她自己说的：算她说话的占比、离她上次说话多久
func (g *Gate) recent(b types.Batch, now time.Time) ([]GateMsg, error) {
	db, err := g.cfg.Stores.For(b.Conv.Channel)
	if err != nil {
		return nil, err
	}
	rows, last, err := db.RecentWindow(b.Conv.String(), b.Character, now.Add(-GateRecent).UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	if last != nil && !slices.ContainsFunc(rows, func(r store.PendingRow) bool { return r.Seq == last.Seq }) {
		rows = append([]store.PendingRow{*last}, rows...)
	}
	out := make([]GateMsg, len(rows))
	for i, r := range rows {
		out[i] = g.gateMsgOf(b, r, g.msgOf(b, r, false))
	}
	return out, nil
}

// repliesLastHour 是这个（角色，会话）这小时她说了几次（保险丝用）
func repliesLastHour(s *GateState, now time.Time) int {
	s.Prune(now)
	return len(s.Replies)
}

func earlierNote(n int) string {
	if n == 0 {
		return ""
	}
	if n > EarlierCap {
		return fmt.Sprintf("；更早的一万条以上没算、没逐条给她看（一次最多看 %d 条）", MaxPending)
	}
	return fmt.Sprintf("；更早的 %d 条没算、没逐条给她看（一次最多看 %d 条）", n, MaxPending)
}

// release 造放行的那一批
func (g *Gate) release(d Dispatch, msgs []types.Msg, now time.Time) types.Batch {
	b := d.Batch
	out := types.Batch{
		Conv: b.Conv, Character: b.Character, Account: b.Account, Now: now, Trigger: types.TriggerMessage, Msgs: msgs,
		Recent: recentFuncFor(g.cfg.Stores, b.Conv, firstSeq(msgs), b.Character),
	}
	for _, m := range msgs {
		if m.Addressed {
			out.Addressed = true
		}
	}
	return out
}

func firstSeq(msgs []types.Msg) int64 {
	if len(msgs) == 0 {
		return 0
	}
	return msgs[0].Seq
}

// Handle 接流水线交来的一批（Pipeline 的 Gate 钩子就是它）
func (g *Gate) Handle(d Dispatch) {
	b := d.Batch
	if g.ctx.Err() != nil || b.Conv.Scene != types.SceneGroup || b.Conv.Channel == "console" || len(b.Msgs) == 0 || !g.cfg.Roster.CanTrigger(b.Conv.Channel, b.Msgs[0].Who) {
		return
	}
	var out *types.Batch
	func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.ctx.Err() != nil {
			return
		}
		defer func() {
			if p := recover(); p != nil {
				g.warnf("会话 %s 闸门判断时出了意外：%v，这一批不放行", b.Conv.String(), p)
			}
		}()
		out = g.handleLocked(d)
		if out != nil {
			g.handoffs.Add(1)
		}
	}()
	g.handoff(out)
}

// handoff 把放行判断交给回合；已经关闭就由闸门补写结局，不能留下没结局的判断。
func (g *Gate) handoff(out *types.Batch) {
	if out == nil {
		return
	}
	defer g.handoffs.Done()
	if g.afterDecide != nil {
		g.afterDecide()
	}
	if g.ctx.Err() == nil {
		// 检查后才关闭也能交出去：装配先关闸门、后关 Responder，由它接住并补写 interrupted。
		g.cfg.Release(*out)
		return
	}
	for _, id := range out.GateDecisionIDs {
		if err := g.cfg.Stores.TraceStore().SetGateOutcome(id, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完"); err != nil {
			g.warnf("闸门判断 %d 记回合结局失败：%v", id, err)
		}
	}
}

func (g *Gate) handleLocked(d Dispatch) *types.Batch {
	b := d.Batch
	trig := b.Msgs[0]
	now := trig.Time
	conv := b.Conv
	key := gateKey(b.Character, conv)
	logAt := func(verdict, reason string, freq float64, f decisionFields) {
		g.log(b, trig.Seq, now, freq, verdict, reason, f)
	}

	// 只记录档：她不进来看（不算、不进回合，主人 @ 也不回）；消息照存
	if m := g.modeOf(conv); m == ModeRecord || (m != ModeSpeak && m != ModeDryRun) {
		logAt(VerdictOff, "这个群设成了只记录，她不进来看", 0, decisionFields{})
		return nil
	}

	st, since, err := g.load(key)
	if err != nil {
		g.warnf("会话 %s 读窗口状态出错：%v，这一批不放行", conv.String(), err)
		return nil
	}
	fp, err := g.FrequencyOf(conv, now)
	if err != nil {
		g.warnf("会话 %s 读频率出错：%v，这一批不放行", conv.String(), err)
		return nil
	}
	freq := fp.Value

	// 拦截链接了：只计数（这小时热闹不热闹），不判断、不放行（规格 第七节、第八节）
	if d.Result.Taken {
		_, _, gm, _, err := g.pending(b, since)
		if err != nil {
			g.warnf("会话 %s 读攒着的消息出错：%v", conv.String(), err)
			return nil
		}
		if len(gm) > 0 {
			recent, err := g.recent(b, now)
			if err != nil {
				g.warnf("会话 %s 读最近的消息出错：%v", conv.String(), err)
				return nil
			}
			f := GateFeaturesOf(gm, recent, now)
			st.RecordRound(BorrowedWeights.Probability(f), now)
			g.save(key, st, since)
		}
		logAt(VerdictTaken, fmt.Sprintf("拦截插件 %s 接了（%s），只计数，不放行", d.Result.By, d.Result.Reason), freq, decisionFields{})
		return nil
	}

	// 通道门：这个渠道连着发不出去，直接不放行
	if why, paused := g.channelPaused(conv.Channel); paused {
		logAt(VerdictPaused, "这个渠道暂停了（"+why+"），闸门不放行，恢复连接或在界面上点恢复", freq, decisionFields{})
		return nil
	}

	called := trig.Addressed
	if !called && g.cfg.Aliases != nil {
		if db, e := g.cfg.Stores.For(conv.Channel); e != nil {
			g.warnf("会话 %s 取库出错：%v，别名没查，这一条当没叫到", conv.String(), e)
		} else if r, ok, err := db.EventRow(conv.String(), trig.Seq); err == nil && ok {
			segs, _ := segsAndName(r.Segments.String)
			called = aliasInSegs(g.cfg.Aliases, b.Character, segs)
		}
	}
	// 频率 0：只有主人叫到她才回（ADR-061 第 3 条）
	if freq <= 0 && !(called && trig.Origin == OriginOwner) {
		logAt(VerdictMuted, fmt.Sprintf("这个群现在不说话（%s），只有主人叫到她才回", fp.Label), freq, decisionFields{})
		return nil
	}

	if called {
		if wait := st.BurstWait(now); wait > 0 {
			// 1 分钟内的一串叫到她合成一轮：等这一分钟过完，再一起看一次（规格 3.3）
			logAt(VerdictMerged, fmt.Sprintf("1 分钟内又被叫，合进上一串，%d 秒后一起看", int(math.Ceil(wait.Seconds()))), freq, decisionFields{})
			g.scheduleBurst(key, d, wait)
			g.save(key, st, since)
			return nil
		}
		rows, msgs, _, earlier, err := g.pending(b, since)
		if err != nil {
			g.warnf("会话 %s 读攒着的消息出错：%v，这一批不放行", conv.String(), err)
			return nil
		}
		if len(rows) == 0 { // 叫到她的这一条已经被撤回了（规格-群聊闸门 2.2：撤回的不进回合），或早就看过了：没什么可看的，也不算被叫到一次
			return nil
		}
		st.RecordForced(now)
		last := rows[len(rows)-1].Seq
		return g.finishEnter(d, key, &st, trig.Seq, last, msgs, now, freq, VerdictForced, "叫到她（@、引用她的话或别名）"+earlierNote(earlier), true, decisionFields{batch: len(msgs)})
	}

	// 没叫到她：这批（她上次看完以后别人说的）值不值得看
	rows, msgs, gm, earlier, err := g.pending(b, since)
	if err != nil {
		g.warnf("会话 %s 读攒着的消息出错：%v，这一批不放行", conv.String(), err)
		return nil
	}
	if len(gm) == 0 {
		return nil
	}
	recent, err := g.recent(b, now)
	if err != nil {
		g.warnf("会话 %s 读最近的消息出错：%v，这一批不放行", conv.String(), err)
		return nil
	}
	roundBatch := gm[len(gm)-1:]
	if st.RoundAt != nil && now.UnixMilli()-*st.RoundAt < GateRound.Milliseconds() {
		roundBatch = nil
		for _, m := range gm {
			if m.At.UnixMilli() >= *st.RoundAt {
				roundBatch = append(roundBatch, m)
			}
		}
	}
	st.RecordRound(BorrowedWeights.Probability(GateFeaturesOf(roundBatch, recent, now)), now)
	if left := st.BackoffLeft(now, len(rows), false); left > 0 {
		logAt(VerdictBackoff, fmt.Sprintf("她刚连着 %d 次看了没说话，先等 %d 秒（攒够 %d 条或者有人叫她就不等）", st.Idle.Count, int(math.Ceil(left.Seconds())), GateBackoffFree), freq, decisionFields{batch: len(rows)})
		g.save(key, st, since)
		return nil
	}
	f := GateFeaturesOf(gm, recent, now)
	jitter := 0.0
	if g.cfg.Jitter > 0 {
		jitter = (g.cfg.Rand()*2 - 1) * g.cfg.Jitter
	}
	dec := st.Evaluate(BorrowedWeights.Probability(f), freq, now, jitter)
	why := fmt.Sprintf("可能性 %.2f，门槛 %.2f（这小时预计 %.1f 次、目标 %.1f 次、已经说了 %d 次；%s）%s", dec.Probability, dec.Threshold, dec.Expected, dec.Target, dec.Actual, fp.Label, earlierNote(earlier))
	fields := decisionFields{features: &f, d: &dec, batch: len(rows)}
	if !dec.Enter {
		logAt(VerdictSkip, why, freq, fields)
		g.save(key, st, since)
		return nil
	}
	st.CloseRound()
	return g.finishEnter(d, key, &st, trig.Seq, rows[len(rows)-1].Seq, msgs, now, freq, VerdictEnter, why, false, fields)
}

// finishEnter 是放行前的最后一步：保险丝。被拦下的这一批也算看过了（和 Bun 一样：状态照放行存，只是不调模型）。
// forced 是被叫到的（保险丝放宽到上限的 2 倍）
func (g *Gate) finishEnter(d Dispatch, key string, st *GateState, trigSeq, last int64, msgs []types.Msg, now time.Time, freq float64, verdict, why string, forced bool, f decisionFields) *types.Batch {
	b := d.Batch
	g.save(key, *st, last)
	if !g.cfg.NoFuse {
		if reason := FuseBeforeTurn(repliesLastHour(st, now), freq, forced); reason != "" {
			g.log(b, trigSeq, now, freq, VerdictFuse, reason, f)
			return nil
		}
	}
	id := g.log(b, trigSeq, now, freq, verdict, why, f)
	rel := g.release(d, msgs, now)
	if id > 0 {
		rel.GateDecisionIDs = []int64{id}
	}
	return &rel
}

// ───────────── 一串叫到她的合成一轮 ─────────────

func (g *Gate) scheduleBurst(key string, d Dispatch, wait time.Duration) {
	if _, ok := g.bursts[key]; ok {
		return
	}
	t := g.clock.NewTimer(min(wait, GateBurst))
	bu := &gateBurst{timer: t, d: d}
	g.bursts[key] = bu
	go func() {
		select {
		case <-t.C():
			g.fire(key, bu)
		case <-g.ctx.Done():
		}
	}()
}

// FlushBursts 让攒着的一串叫到她的现在就一起看（回放验收、测试用；不用真等一分钟）
func (g *Gate) FlushBursts() {
	g.mu.Lock()
	var todo []struct {
		key string
		b   *gateBurst
	}
	for k, b := range g.bursts {
		b.timer.Stop()
		todo = append(todo, struct {
			key string
			b   *gateBurst
		}{k, b})
	}
	g.mu.Unlock()
	for _, x := range todo {
		g.fire(x.key, x.b)
	}
}

// fire 是这一分钟过完：把攒的一起看一次
func (g *Gate) fire(key string, bu *gateBurst) {
	var out *types.Batch
	func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.ctx.Err() != nil || g.bursts[key] != bu {
			return
		}
		delete(g.bursts, key)
		defer func() {
			if p := recover(); p != nil {
				g.warnf("会话 %s 合并的一串叫到她的，看的时候出了意外：%v", bu.d.Batch.Conv.String(), p)
			}
		}()
		out = g.fireLocked(key, bu.d)
		if out != nil {
			g.handoffs.Add(1)
		}
	}()
	g.handoff(out)
}

func (g *Gate) fireLocked(key string, d Dispatch) *types.Batch {
	b := d.Batch
	if m := g.modeOf(b.Conv); m != ModeSpeak && m != ModeDryRun {
		return nil
	}
	st, since, err := g.load(key)
	if err != nil {
		g.warnf("会话 %s 读窗口状态出错：%v，这一串不放行", b.Conv.String(), err)
		return nil
	}
	rows, msgs, _, earlier, err := g.pending(b, since)
	if err != nil {
		g.warnf("会话 %s 读攒着的消息出错：%v，这一串不放行", b.Conv.String(), err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	lastRow := rows[len(rows)-1]
	now := time.UnixMilli(max(g.clock.Now().UnixMilli(), lastRow.CreatedAt))
	fp, err := g.FrequencyOf(b.Conv, now)
	if err != nil {
		g.warnf("会话 %s 读频率出错：%v，这一串不放行", b.Conv.String(), err)
		return nil
	}
	if why, paused := g.channelPaused(b.Conv.Channel); paused {
		g.log(b, lastRow.Seq, now, fp.Value, VerdictPaused, "这个渠道暂停了（"+why+"），闸门不放行", decisionFields{batch: len(rows)})
		return nil
	}
	st.RecordForced(now)
	return g.finishEnter(d, key, &st, lastRow.Seq, lastRow.Seq, msgs, now, fp.Value, VerdictForced, fmt.Sprintf("1 分钟内被叫了好几次，攒的 %d 条一起看一次%s", len(rows), earlierNote(earlier)), true, decisionFields{batch: len(rows)})
}

// ───────────── 回合定了、发出去了：告诉闸门 ─────────────

// TurnDone 是一个回合定了：spoke 是她说了话（退避清零），没说话就是“看了没说话”，退避加一档。
// count 是通过裁决并写进计划的句数，一步算一句，试运行也算；拦下的、没写成的不算。
// 复读也走这里（spoke 为真、count 为 1，计退避，也计开口）；戳回去不走，它是通知。at 是回合定下的时刻。
func (g *Gate) TurnDone(character string, conv types.Conversation, spoke bool, count int, at time.Time) {
	if g.ctx.Err() != nil || conv.Scene != types.SceneGroup || conv.Channel == "console" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := gateKey(character, conv)
	st, since, err := g.load(key)
	if err != nil {
		g.warnf("会话 %s 读窗口状态出错：%v，这一轮没记", conv.String(), err)
		return
	}
	st.RecordTurn(spoke, at)
	for range count {
		st.RecordReply(at)
	}
	g.save(key, st, since)
}

// ───────────── 通道门（规格-渠道中间件 第八节） ─────────────

// PartEnded 是出口告诉闸门一段发完了：confirmed 为真是确认，failed 为真是明确失败（用完重试，或适配器说不支持）。
// 已中止、结果未知、守卫拦下都不要调（不算）。连着 PauseAfter 段明确失败，这个渠道就暂停，闸门不再放行；中间有一段确认就清零（"连续"）
func (g *Gate) PartEnded(channel string, confirmed, failed bool) {
	if g.ctx.Err() != nil || (confirmed == failed) {
		return
	}
	g.mu.Lock()
	c := g.chanLocked(channel)
	var notify func()
	if confirmed {
		c.fails = 0
	} else {
		c.fails++
		if c.fails >= g.cfg.PauseAfter && !c.paused {
			c.paused = true
			c.why = fmt.Sprintf("连着 %d 段明确发不出去", c.fails)
			why := c.why
			notify = func() { g.notifyPause(channel, true, why) }
		}
	}
	g.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// Resume 是适配器报这个渠道的连接恢复了，或者主人在界面上点了恢复：失败计数清零、允许放行
func (g *Gate) Resume(channel string) {
	g.mu.Lock()
	c := g.chanLocked(channel)
	was := c.paused
	*c = gateChan{}
	g.mu.Unlock()
	if was {
		g.notifyPause(channel, false, "连接恢复或主人点了恢复")
	}
	if g.cfg.OnResume != nil {
		_, _ = safe(func() struct{} { g.cfg.OnResume(channel); return struct{}{} })
	}
}

func (g *Gate) notifyPause(channel string, paused bool, why string) {
	g.logf("渠道 %s 的通道门：%s（%s）", channel, map[bool]string{true: "暂停", false: "恢复"}[paused], why)
	if g.cfg.OnPause != nil {
		_, _ = safe(func() struct{} { g.cfg.OnPause(channel, paused, why); return struct{}{} })
	}
}

func (g *Gate) chanLocked(channel string) *gateChan {
	c := g.chans[channel]
	if c == nil {
		c = &gateChan{}
		g.chans[channel] = c
	}
	return c
}

func (g *Gate) channelPaused(channel string) (why string, paused bool) {
	c := g.chans[channel]
	if c == nil || !c.paused {
		return "", false
	}
	return c.why, true
}

// Paused 说一个渠道现在是不是被通道门暂停了（右侧栏显示用）
func (g *Gate) Paused(channel string) (why string, paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.channelPaused(channel)
}
