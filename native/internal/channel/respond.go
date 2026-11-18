package channel

// 回合：闸门放行的一批怎么交给内核，回合进行中来了新的怎么办（规格-渠道中间件 第六节第 4 步、第九节、第十节 10.1 的屏障）。
// 回合定了以后的裁决、改写、写库在 settle.go。
//
// 一个（角色，会话）同一时间最多一个回合在中间件手里，它有三种样子：
//   - 跑着：Respond 发出去了，"回合定了"还没到；这时来的新批次按下面的路由走；
//   - 处理着：内核说回合定了，裁决、写库、Hub.Commit 还没做完——屏障：这时来的新批次挂起，不 Steer 也不 Respond，
//     等处理完再发（新回合的历史才一定含上一回合已经发出去的句子）；
//   - 没有：新批次直接 Respond。
//
// 回合跑着时来的新批次（闸门放行了的），先看这一批的听众和档：
//   - 批里有被叫到的（Addressed），或者 OwnerAsked 为真，或者档和跑着的回合不一样：StopIn 把正在跑的停下，这一批挂起，等被停下的那个回合
//     处理完（写一条"已打断"的计划）再 Respond。插话只收文字，不带听众和档，插话进上下文用的是跑着那一轮的听众，主人在群里没叫她的话
//     会被当群聊听众处理，她想不起主人的私事——所以这几种不能走插话（规格 第六节第 4 步，2026-10-26 复核改的路由）；
//   - 其余（没被叫到、OwnerAsked 为假、档相同）：SteerIn 插话，这一批已经由入库一站存进库，seq 交给内核。SteerIn 返回 false（这一轮其实不在跑）
//     就改走 Respond：挂起等屏障。插话没送到就回合结束了（内核退回来），或者被后来的 StopIn 丢掉，都要再发：这些话内核没让她看到，
//     OnSteer 收到"丢掉"就把那批放回挂起的里面，下一次 Respond 一起带上。
//
// 不同的（角色，会话）互不等待；"回合定了"的回调只把回合放进自己按（角色，会话）排的队列就返回（OnSettled 的回调有 30 秒时限，超时同一个订阅者
// 接着送下一条、前一条的回调还在跑，顺序就乱了）。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// SeenNotifier 接收渠道事件写库后的通知。
type SeenNotifier interface{ NotifySeen(turn.Seen) }

// Kernel 是中间件要用的内核入口，*turn.Hub 就是它。分出来是为了用假内核测（规格 第二十一节：第 3 步在内核线给出前用假内核）
type Kernel interface {
	Respond(in turn.Input) <-chan turn.Outcome
	SteerIn(characterID, conversation, text string, seqs []int64) (string, bool)
	StopIn(characterID, conversation string) bool
	RestartIn(characterID, conversation string)
	Commit(characterID, conversation, turnID string, lines []turn.Committed) error
}

var _ Kernel = (*turn.Hub)(nil)

// Writer 是回合定了以后写库和发送的地方，*Outlet 就是它
type Writer interface {
	SendHeld(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part) (Held, error)
	Record(ctx context.Context, characterID, origin string, conv types.Conversation, kind, reason string) (string, error)
}

var _ Writer = (*Outlet)(nil)

// Shaper 是一个渠道怎么改写、怎么拆条：Rewrite 是 10.1 第 2 步（改写出错用原文），Split 是第 6 步（改写以后的字拆成几段）。
// 没配的渠道：不改写，只按空行拆（不掷骰子拆长句——那是 QQ 的规则）
type Shaper struct {
	Rewrite func(text string) string
	Split   func(rewritten string) []string
}

// QQShaper 是 QQ 的：去句尾句号；拆条按 ADR-163 的规则（asSentQQ）
var QQShaper = Shaper{
	Rewrite: dropTrailingPeriods,
	Split:   func(rewritten string) []string { return splitMessage(asSentQQ(rewritten)) },
}

// ResponderConfig 是回合的配置
type ResponderConfig struct {
	Kernel Kernel
	Writer Writer
	// Stores 读插件自己发的话（复读等，origin 是 automation:*）补进下一批：按会话的渠道取库；nil 就不补
	Stores store.Stores
	Roster Roster
	// Guard 发送前守卫；nil 是不查。GuardFor 按角色取守卫，装配多角色时优先用它。
	Guard    Guard
	GuardFor func(character string) Guard
	// OnGuardRoute 守卫走的路变了（正常 → 嵌入挂了……）调一次，参数是变之前和变之后的；提醒主人挂在这里
	OnGuardRoute func(prev, cur string)
	// OnTurnDone 在一个回合定了、裁决和写库做完以后调（结局是 done 的）：spoke 是她说了话（模型回了字，哪怕被守卫拦下），
	// count 是通过裁决并写进计划的句数（一步一句，试运行也算，拦下的不算），at 是此刻。闸门靠它记“这小时说了几次”和空闲退避（Gate.TurnDone）。
	// 被打断、出错的回合不调：那不是"看了没说话"。在裁决的队列里调，不要阻塞；panic 当没调。nil 就是不通知
	OnTurnDone func(character string, conv types.Conversation, spoke bool, count int, at time.Time)
	// Audit 出站记数的分类器；nil 是不记。命中的每一笔交给 OnAudit
	Audit   func(text string) []AuditHit
	OnAudit func(AuditRecord)
	// Shapers 各渠道的改写和拆条；渠道名没在里面的不改写、只按空行拆。默认 qq 用 QQShaper
	Shapers map[string]Shaper
	// Scenes 选场合提示词段（ADR-175）；nil 用 DefaultScenes
	Scenes func(conv types.Conversation) []string
	// Relation 写"对面是谁"那一句；nil 用 DefaultRelation
	Relation func(b types.Batch, a audience.Audience) string
	// Line 排给模型看的那几行用的（名字查询、引用查询、时区）；Mine 由中间件按名册填
	Line LineEnv
	// OwnerCall 是这个角色怎么称呼所有者（排所有者那一行的括号用）；nil 或空 = "用户"
	OwnerCall func(character string) string

	MaxChars     int           // 一句最多多少字，超了拦下（保险丝，200，规格-群聊闸门 10.1 第 172 行）
	GuardTimeout time.Duration // 守卫整体时限（10 秒，规格-记忆-v2 13.2.8）
	StuckAfter   time.Duration // 一个回合过了这么久还没处理完，记一行日志（5 分钟）
	Logf         func(format string, args ...any)
	Warnf        func(format string, args ...any) // WARN：出错或降级，系统会自己接上；nil 时用 Logf
}

// DefaultScenes 是场合提示词段的默认值（第 5 步前内容定；段名照现有的出厂提示词文件）：
// 平台的私聊 scene-private、群 scene-group，再接平台那一段（QQ 是 platform-qq）；本地群聊 scene-home-group。
// 风格预设（Input.Style）内核还没有，不在这里
func DefaultScenes(conv types.Conversation) []string {
	var out []string
	switch {
	case conv.Channel == "console" && conv.Scene == types.SceneGroup:
		out = []string{"scene-home-group"}
	case conv.Channel == "console":
		return nil // 本地私聊是主窗口的，内核自己拼
	case conv.Scene == types.SceneGroup:
		out = []string{"scene-group"}
	default:
		out = []string{"scene-private"}
	}
	if conv.Channel == "qq" {
		out = append(out, "platform-qq")
	}
	return out
}

// DefaultRelation 是"对面是谁"（规格 第九节第 7 条，照 Bun daemon/plugins/router.ts）：所有者的私聊、朋友的私聊各一句，群里不给（空）。
// ownerCall 是角色怎么称呼所有者；nil 或空写"所有者"。朋友的名字是平台的缓存，过一遍 escapeName，伪造不出另一句话。
func DefaultRelation(b types.Batch, a audience.Audience, ownerCall func(character string) string) string {
	if b.Conv.Scene != types.ScenePrivate || b.Conv.Channel == "console" {
		return ""
	}
	owner := "所有者"
	if ownerCall != nil {
		if v := ownerCall(b.Character); v != "" {
			owner = v
		}
	}
	if a.PrivateOK {
		return "对面是" + owner + "。这里只有你们两个。"
	}
	name := ""
	for i := len(b.Msgs) - 1; i >= 0; i-- {
		if !b.Msgs[i].Self {
			name = b.Msgs[i].Name
			if name == "" {
				name = b.Msgs[i].Who
			}
			break
		}
	}
	return fmt.Sprintf("对面是%s：%s允许你私聊的朋友，不是%s。只说可以让别人知道的事，%s的私事不要说。", escapeName(name), owner, owner, owner)
}

// Responder 是回合的中间件一侧
type Responder struct {
	cfg    ResponderConfig
	ctx    context.Context
	cancel context.CancelFunc
	arrive *lanes // 放行的批次进来的路由，按（角色，会话）排：StopIn 会等回合停下，不能占着入库一站的队列
	settle *lanes // 回合定了以后的裁决，按（角色，会话）排

	mu     sync.Mutex
	keys   map[string]*keyState
	steers map[string]steerRef // 插话 id → 它带的那一批：没送到的要放回挂起的里面

	routeMu   sync.Mutex
	lastRoute string
}

type keyState struct {
	rounds   []*round // 排头的是还没处理完的那个回合；最多一个
	held     []types.Batch
	requeued []types.Batch // 已有持久 InputID 的下一工作，不能和新批合并改载荷
	lastSeq  int64         // 交给内核的最后一条事件（Respond 的批、插话）；插件自己发的话从这条之后找
}

// round 是交给内核的一个回合
type round struct {
	conv            types.Conversation
	character       string
	scope           string
	settled         bool // 内核说回合定了：处理中了
	finished        bool
	turnID          string
	gateDecisionIDs []int64 // 这一回合接下的全部放行判断，含已送达的插话
	outcomeClaimed  bool    // 结局已有人接手回填，在 r.mu 下读写
	timer           *time.Timer
}

type steerRef struct {
	key   string
	round *round
	batch types.Batch
}

// NewResponder 造回合
func NewResponder(cfg ResponderConfig) (*Responder, error) {
	if cfg.Kernel == nil || cfg.Writer == nil {
		return nil, errors.New("回合需要内核和写库的出口")
	}
	if err := cfg.Roster.validate(); err != nil {
		return nil, fmt.Errorf("名册不合法：%w", err)
	}
	if cfg.MaxChars == 0 {
		cfg.MaxChars = 200
	}
	if cfg.GuardTimeout == 0 {
		cfg.GuardTimeout = 10 * time.Second
	}
	if cfg.StuckAfter == 0 {
		cfg.StuckAfter = 5 * time.Minute
	}
	if cfg.Scenes == nil {
		cfg.Scenes = DefaultScenes
	}
	if cfg.Relation == nil {
		cfg.Relation = func(b types.Batch, a audience.Audience) string {
			return DefaultRelation(b, a, cfg.OwnerCall)
		}
	}
	if cfg.Shapers == nil {
		cfg.Shapers = map[string]Shaper{"qq": QQShaper}
	}
	r := &Responder{cfg: cfg, arrive: newLanes(), settle: newLanes(), keys: map[string]*keyState{}, steers: map[string]steerRef{}, lastRoute: GuardRouteNormal}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r, nil
}

func (r *Responder) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(format, args...)
	}
}

func (r *Responder) warnf(format string, args ...any) {
	if r.cfg.Warnf != nil {
		r.cfg.Warnf(format, args...)
		return
	}
	r.logf(format, args...)
}

func roundKey(character, conversation string) string { return character + "|" + conversation }

// Stop 只关入口并取消路由、裁决，不等正在做的停下；宿主先取消内核，再用同一截止时间等 Idle、Close。
// 返回时不会再发起 Respond；已经进入 StopIn 的路由仍可能在等内核收尾。
func (r *Responder) Stop() {
	r.cancel()
	r.arrive.stop()
	r.settle.stop()
	r.mu.Lock()
	r.mu.Unlock()
}

// Close 关闭：不再收新批次和“回合定了”，等在做的停下，没完成的判断统一记已打断；库要最后关。
func (r *Responder) Close() {
	r.Stop()
	r.arrive.close()
	r.settle.close()
	r.mu.Lock()
	var ids []int64
	for _, st := range r.keys {
		for _, rd := range st.rounds {
			ids = append(ids, r.claimOutcome(rd)...)
			if rd.timer != nil {
				rd.timer.Stop()
			}
		}
		for _, b := range st.held {
			ids = append(ids, b.GateDecisionIDs...)
		}
		for _, b := range st.requeued {
			ids = append(ids, b.GateDecisionIDs...)
		}
		st.requeued = nil
		st.held = nil
	}
	for _, ref := range r.steers {
		ids = append(ids, ref.batch.GateDecisionIDs...)
	}
	clear(r.steers)
	r.mu.Unlock()
	r.gateOutcome(ids, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
}

// Idle 在现在收下的路由、裁决都做完时返回（测试用）
func (r *Responder) Idle(ctx context.Context) error {
	for _, l := range []*lanes{r.arrive, r.settle} {
		select {
		case <-l.idleChan():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Holding 说这个（角色，会话）有没有批次挂在屏障后面（界面显示"等待上一句裁决"用）
func (r *Responder) Holding(character, conversation string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.keys[roundKey(character, conversation)]
	return st != nil && (len(st.held) > 0 || len(st.requeued) > 0)
}

// Plugin 把"回合定了"挂到内核上：hub.Use(responder.Plugin())
func (r *Responder) Plugin() turn.Plugin { return responderPlugin{r} }

type responderPlugin struct{ r *Responder }

func (responderPlugin) ID() string { return "channel-responder" }
func (p responderPlugin) Setup(reg *turn.Registry) (func(), error) {
	undo := reg.OnSettled(func(_ context.Context, s turn.Settled) { p.r.Settled(s) })
	return undo, nil
}

// Release 是闸门放行了一批：交给内核回。马上返回（路由在自己的队列里做，StopIn 会等回合停下，不能卡住入库一站）
func (r *Responder) Release(b types.Batch) {
	if r.ctx.Err() != nil {
		r.gateOutcome(b.GateDecisionIDs, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
		return
	}
	if len(b.Msgs) == 0 {
		return
	}
	key := roundKey(b.Character, b.Conv.String())
	j := job{run: func() { r.route(key, b) }, drop: func() {
		r.gateOutcome(b.GateDecisionIDs, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
	}}
	if !r.arrive.submit(key, j) {
		j.drop()
	}
}

func anyCalled(b types.Batch) bool {
	if b.Addressed {
		return true
	}
	for _, m := range b.Msgs {
		if m.Addressed {
			return true
		}
	}
	return false
}

func (r *Responder) lineEnv(b types.Batch) LineEnv {
	env := r.cfg.Line
	env.Mine = r.cfg.Roster.accountsOf(b.Conv.Channel, b.Character)
	if r.cfg.OwnerCall != nil {
		env.Owner = r.cfg.OwnerCall(b.Character)
	}
	return env
}

func (r *Responder) stateLocked(key string) *keyState {
	st := r.keys[key]
	if st == nil {
		st = &keyState{}
		r.keys[key] = st
	}
	return st
}

// route 是放行的一批怎么办（见文件开头）
func (r *Responder) route(key string, b types.Batch) {
	aud := AudienceOf(b, r.cfg.Roster)
	scope := ScopeOf(aud)
	conv := b.Conv.String()

	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		r.gateOutcome(b.GateDecisionIDs, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
		return
	}
	st := r.stateLocked(key)
	if len(st.rounds) == 0 {
		r.issueLocked(key, st, b)
		r.mu.Unlock()
		return
	}
	head := st.rounds[0]
	if head.settled {
		st.held = append(st.held, b)
		r.mu.Unlock()
		r.logf("会话 %s 上一回合还在处理（裁决、写库），这一批挂起，处理完再回", conv)
		return
	}
	if !anyCalled(b) && !aud.OwnerAsked && scope == head.scope {
		// 插话。SteerIn 在 r.mu 里调：收下的插话要先登记（steers），内核的"送到""丢掉"才找得到它；
		// 内核发"送到""丢掉"时不拿它自己的锁、"收下"那一声 OnSteer 不拿 r.mu，所以不会互相等
		text, seqs := FormatBatch(b, r.lineEnv(b))
		id, ok := r.cfg.Kernel.SteerIn(b.Character, conv, text, seqs)
		if ok {
			r.steers[id] = steerRef{key: key, round: head, batch: b}
			st.lastSeq = max(st.lastSeq, lastSeq(b))
			r.mu.Unlock()
			return
		}
		// 这一轮其实不在跑（刚结束，"回合定了"还在路上，或者还排着没开始）：等它处理完再回
		st.held = append(st.held, b)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	// 停下正在跑的（被叫到、主人在问、档不同）；停下以后"回合定了"才到，要等它处理完（屏障）再回
	r.cfg.Kernel.StopIn(b.Character, conv)
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		r.gateOutcome(b.GateDecisionIDs, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
		return
	}
	if len(st.rounds) == 0 {
		r.issueLocked(key, st, b)
		r.mu.Unlock()
		return
	}
	st.held = append(st.held, b)
	r.mu.Unlock()
}

func lastSeq(b types.Batch) int64 {
	var m int64
	for _, x := range b.Msgs {
		m = max(m, x.Seq)
	}
	return m
}

// mergeBatches 把挂起的几批并成一批：消息按 seq 排、去重，判断 ID 全保留，其余用最后一批的。
func mergeBatches(bs []types.Batch) types.Batch {
	out := bs[len(bs)-1]
	out.GateDecisionIDs = nil
	seen := map[int64]bool{}
	var msgs []types.Msg
	for _, b := range bs {
		out.GateDecisionIDs = append(out.GateDecisionIDs, b.GateDecisionIDs...)
		for _, m := range b.Msgs {
			if seen[m.Seq] {
				continue
			}
			seen[m.Seq] = true
			msgs = append(msgs, m)
		}
		if b.Addressed {
			out.Addressed = true
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	out.Msgs = msgs
	return out
}

// issueLocked 把一批交给内核回（调用时没有回合在手里）。r.mu 在手里；Respond 本身不阻塞
func (r *Responder) issueLocked(key string, st *keyState, b types.Batch) {
	aud := AudienceOf(b, r.cfg.Roster)
	lines := batchLines(b, r.lineEnv(b))
	if extra := r.automationLines(b, st.lastSeq); len(extra) > 0 {
		lines = append(lines, extra...)
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].seq < lines[j].seq })
	}
	var texts []string
	var seqs []int64
	for _, l := range lines {
		texts = append(texts, l.text)
		seqs = append(seqs, l.seq)
	}
	in := turn.Input{
		CharacterID: b.Character, Conversation: b.Conv.String(), Seqs: seqs, Text: strings.Join(texts, "\n"),
		Audience: aud, Addressed: anyCalled(b), Scope: ScopeOf(aud),
		Relation: r.cfg.Relation(b, aud), Scenes: r.cfg.Scenes(b.Conv),
	}
	platform, err := store.KernelPlatform(r.cfg.Stores, b.Conv.Channel)
	if err != nil {
		r.logf("输入平台无效：%v", err)
		return
	}
	scope := kernel.Scope{CharacterID: b.Character, Conversation: in.Conversation, ScopeID: in.Scope, Platform: platform, Channel: b.Conv.Channel, AccountID: b.Account, Scene: b.Conv.Scene}
	for _, m := range b.Msgs {
		if m.Self {
			continue
		}
		kind := ActorHuman
		if m.Who == "owner" {
			kind = ActorOwner
		}
		if scope.ActorID == "" {
			scope.ActorKind, scope.ActorID = kind, m.Who
		} else if scope.ActorID != m.Who {
			scope.ActorKind, scope.ActorID = "mixed", "mixed"
		}
	}
	in.Identity = kernel.Invocation{Scope: scope, Kind: kernel.KindForeground}
	out := r.cfg.Kernel.Respond(in)
	rd := &round{conv: b.Conv, character: b.Character, scope: in.Scope, gateDecisionIDs: b.GateDecisionIDs}
	st.rounds = append(st.rounds, rd)
	st.lastSeq = max(st.lastSeq, lastSeq(b))
	rd.timer = time.AfterFunc(r.cfg.StuckAfter, func() {
		r.mu.Lock()
		stuck, settled := !rd.finished, rd.settled
		r.mu.Unlock()
		if stuck {
			if settled {
				r.logf("会话 %s 的回合 %s 内核说定了已经 %v，裁决、写库还没做完，后面的批次一直在等", b.Conv.String(), rd.turnID, r.cfg.StuckAfter)
			} else {
				r.logf("会话 %s 的回合 %s 过了 %v 内核还没说定了，后面的批次一直在等", b.Conv.String(), rd.turnID, r.cfg.StuckAfter)
			}
		}
	})
	go func() {
		select {
		case o := <-out:
			r.mu.Lock()
			rd.turnID = o.TurnID
			r.mu.Unlock()
			if o.Err != nil && o.TurnID == "" {
				// 内核在受理这一步就拒了（会话故障、写库失败、身份不对……）：没有这一回合，也就不会有“回合定了”。
				// 不收尾的话这个会话的闸门永远挂着一个等不到的回合，后面的批次全进挂起、再也交不出去。
				r.rejected(key, rd, o.Err)
			}
		case <-r.ctx.Done():
		}
	}()
}

// OnSteer 接内核的插话进度（turn.Config.OnSteer 挂它）：插话"丢掉"了（被停下丢掉、回合收尾时没送到）就是她没看到这批话，
// 放回挂起的里面，下一次 Respond 一起带上；“送到”了就把判断 ID 合进原回合。别的会话（主窗口）的插话不认识，忽略。
func (r *Responder) OnSteer(e turn.SteerEvent) {
	if e.Phase != "delivered" && e.Phase != "dropped" {
		return // accepted 在内核的锁里发，这里不拿锁
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.steers[e.ID]
	if !ok {
		return
	}
	if r.ctx.Err() != nil {
		return
	}
	delete(r.steers, e.ID)
	if e.Phase == "delivered" {
		ref.round.gateDecisionIDs = append(ref.round.gateDecisionIDs, ref.batch.GateDecisionIDs...)
		return
	}
	if !e.Requeue {
		return
	}
	st := r.stateLocked(ref.key)
	st.requeued = append(st.requeued, ref.batch)
	if len(st.rounds) == 0 {
		r.issueLocked(ref.key, st, nextHeld(st))
	}
}

// mergeHeld 取走并合并挂起的
func mergeHeld(st *keyState) types.Batch {
	b := mergeBatches(st.held)
	st.held = nil
	return b
}

func nextHeld(st *keyState) types.Batch {
	if len(st.requeued) > 0 {
		b := st.requeued[0]
		st.requeued = st.requeued[1:]
		return b
	}
	return mergeHeld(st)
}

// Settled 接内核的"回合定了"（Plugin 挂的就是它）。只改个标记、把回合放进队列就返回
func (r *Responder) Settled(s turn.Settled) {
	if r.ctx.Err() != nil {
		return
	}
	key := roundKey(s.CharacterID, s.Conversation)
	r.mu.Lock()
	st := r.keys[key]
	if st == nil || len(st.rounds) == 0 {
		r.mu.Unlock()
		return // 不是我们交出去的回合（主窗口的）
	}
	rd := st.rounds[0]
	if rd.settled {
		r.mu.Unlock()
		r.logf("会话 %s 的回合 %s 收到了第二次「回合定了」，忽略", s.Conversation, s.TurnID)
		return
	}
	if rd.turnID != "" && s.TurnID != "" && rd.turnID != s.TurnID {
		r.logf("会话 %s 的回合定了说的是 %s，手里的是 %s：照手里的处理", s.Conversation, s.TurnID, rd.turnID)
	}
	rd.settled = true
	rd.turnID = s.TurnID
	r.mu.Unlock()
	r.settle.submit(key, job{run: func() { r.process(key, rd, s) }, drop: func() {}})
}

// rejected 是内核没有受理这一批（Respond 直接回了带错的结果、没有回合号）：这一批不回复、不重试，
// 判断回填成出错、留一条计划，闸门放开让后面的批次照常交给内核。被拒的原因（比如会话故障）解除以后，只处理解除之后新来的批次；
// 故障期间已经挂起的旧批次（前一回合还在跑时攒下的）照现有的挂起规则并成一批再交一次，仍被拒就同样收尾。
func (r *Responder) rejected(key string, rd *round, err error) {
	r.mu.Lock()
	// 先判断、再领结局：这一轮已经被标成定了（或已收尾）时什么都不碰，判断结局留给定了的那条路去写
	if rd.settled || rd.finished {
		r.mu.Unlock()
		return
	}
	ids := r.claimOutcome(rd)
	shutdown := r.ctx.Err() != nil
	r.mu.Unlock()
	if shutdown {
		// 关机窗口：和 Close、正常路径一样标成“被打断”，不记成出错，也不留计划
		r.gateOutcome(ids, store.GateOutcomeInterrupted, "后台关闭，这一轮没做完")
		r.finish(key, rd)
		return
	}
	reason := "内核没有受理这一批：" + truncRunes(err.Error(), 120)
	r.logf("会话 %s 的这一批被内核拒了，不回复：%v", rd.conv.String(), err)
	r.gateOutcome(ids, store.GateOutcomeError, reason)
	r.record(rd, store.PlanKindError, reason)
	r.finish(key, rd)
}

// finish 是一个回合处理完了：屏障放开，挂起的并成一批发出去
func (r *Responder) finish(key string, rd *round) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.keys[key]
	if st == nil || rd.finished {
		return
	}
	rd.finished = true
	if rd.timer != nil {
		rd.timer.Stop()
	}
	st.rounds = slices.DeleteFunc(st.rounds, func(x *round) bool { return x == rd })
	// 还留着的插话：内核该发"送到"或"丢掉"，到现在没有，当丢掉，别让这批话没人回
	for id, ref := range r.steers {
		if ref.key == key {
			r.logf("会话 %s 有一句插话（%s）回合处理完了还没有下文，当没送到，放回去再回", ref.batch.Conv.String(), id)
			st.requeued = append(st.requeued, ref.batch)
			delete(r.steers, id)
		}
	}
	if r.ctx.Err() != nil || (len(st.held) == 0 && len(st.requeued) == 0) || len(st.rounds) > 0 {
		return
	}
	r.issueLocked(key, st, nextHeld(st))
}
