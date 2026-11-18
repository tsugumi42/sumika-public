// Package channel 是渠道中间件（规格-渠道中间件，ADR-177 的第二层）：收、存、认人、决定她接不接、发出去的整套规矩。
// 它不认识任何一个平台的字段名，只看 types 里的统一事件（会话、人、段、通知、回执）；平台适配器（internal/onebot）把协议翻译成
// 这些事件交给它，平台插件（internal/channel/qq）和复读通过它给的接口接进来。
//
// 这个文件是进来的路径的第一站（第十五节第 1 步）：入库。一条收到的消息怎么存：
//   - 去重：键是（渠道、会话、平台消息号），同一条第二次到达只补"看到了"，不再存事件（两个号看同一个群是正常情况，ADR-007）；
//     号为空或全是 0 的不去重；
//   - 双号观察：事件存一份，收到它的每个号绑的角色各记一条"看到了"（observations）；
//   - 一个事务：事件、平台消息号映射、"看到了"要么都在要么都不在（store.AppendInbound）；
//   - 按会话排队：同一个（渠道、会话）的消息一个接一个处理，实时的和补录的进同一条队列，水位才能按顺序推进；
//     不同会话互不等待，一个会话慢了不堵别的（lanes）；
//   - 回声认领：她的号发出的消息在存之前先问出口（Config.Echo），是我们刚发的某一段的回声就认领，不当成"她的号上别人发的"存一条新的（第 2 步）。
//
// 后面的步骤（拦截链、闸门）挂在 Config.OnStored 上：它在同一条队列里、入库提交以后调用。
// 通知（戳一戳、撤回……）走同一条会话队列，怎么存、怎么挂在 intake_notice.go。
package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

var (
	// ErrClosed：中间件已经关闭，这条没有收下（清单④：关掉以后还在回来的东西不写库）
	ErrClosed = errors.New("渠道已关闭")
	// ErrUnknownAccount：这条消息是哪个号收到的，名册里没有。不在配置里的号不接入，不悄悄接新号（规格 第六节第 2 步）
	ErrUnknownAccount = errors.New("名册里没有这个号")
)

// 带给订阅者的标签（沿用 ADR-141 的 message/stored 的口径）。过滤在消费者那一边：中间件不因为标签丢事件
// （ADR-092 的记忆不吃补录，ADR-101 的学说法吃补录，两边口径不同，规格 第六节第 8 步）
const (
	TagBackfill = "backfill" // 补录来的，不是实时收到的
	TagFromSelf = "fromSelf" // 从她自己的号发出的
)

// Admission 是平台插件对一条收到的消息的准入决定（Bun 的群白名单、陌生人私聊、收发自检，规格 第十四节"陌生人私聊"一行）：
// 名单在平台的配置里，中间件只执行决定，不认识名单
type Admission struct {
	Drop      bool     // 完全不收（白名单外的群）：不存、不记看到了
	NoObserve bool     // 存下来，但不记"看到了"（陌生人私聊、自检期间的）：不进记忆，以后也不会被补着总结
	Tags      []string // 加给这条的标签（stranger、selftest），同时存进事件、带给订阅者
}

// EchoClaimer 认领"我们自己发出去的消息被协议端推回来"的那一份（出站的 Outlet 实现它，规格 10.4"回声认领"）。
// 认领成功是把回声的平台消息号记成我们那一段的号（channel_msgs 指向这次回复的事件），入库一站接着往下存时按键去重，
// 发现是已有的事件，只补"看到了"；不认领就当一条新消息存
type EchoClaimer interface {
	ClaimEcho(in types.Inbound) (claimed bool, err error)
}

// Config 是中间件的配置
type Config struct {
	// Stores 是按渠道取库的那一组（ADR-199）：每个平台一个聊天库，运行记录 trace 共用一份。
	// 一条消息进哪个库看它的 Conv.Channel；没登记的渠道报错，不落到别的库
	Stores store.Stores
	Roster Roster
	// Admit 是准入检查；nil 就是全收。在这条消息的队列里调用，不要阻塞
	Admit func(types.Inbound) Admission
	// Echo 是回声认领；nil 就是不认领。发言人是某个角色的号的消息，在准入检查之后、入库之前问它——
	// 对任何账号交来的角色号的消息都问，不只问收到它的那个号（B 号的回复被 A 号先看到，A 号交来的也得认领）。
	// 在这条消息的会话队列里调用，不要阻塞；出错只记日志，这条按普通消息存
	Echo EchoClaimer
	// OnStored 在一条消息入库（提交）以后、在它所在的那条会话队列里调用：后面的步骤（拦截链、闸门）挂在这里，同一个会话的下一条要等它返回。
	// 只在"这个角色这一次看到了新东西"时调：新存的事件调；重复到达的，只有 Character 是第一次看到这条（另一个号先收到了，Duplicate 为真）才调，
	// 各个角色的闸门各算各的。同一个号把同一条再交一遍（重连后协议端重推、补录和实时重叠）不调：这个角色早就处理过它，
	// 不然补录先存、同号又实时到的那一份没有 backfill 标签，会被当成一条新消息而对旧消息开口。陌生人、自检这类不记"看到了"的，
	// 只有新存的那一份调（重复的什么也没新增）。
	// 不要在里面等同一个会话的 Ticket（会等自己）；出错、panic 只记日志，不影响后面的
	OnStored func(Stored)
	// AdmitNotice 是通知的准入检查，规矩同 Admit（白名单外的群的通知也不存、不处理）；nil 就是全收
	AdmitNotice func(types.Notice) Admission
	// OnNotice 在一条通知处理完（该存的存进了库）以后、在它所在的会话队列里调用，和消息走同一条队列：同一个会话里的通知和消息一个接一个，
	// 戳回去、复读的状态才不会被两条路同时改。拦截链、撤回处理挂在这里。
	// 同一个角色第二次看到同一条通知（同号重推）不调；另一个号绑的角色第一次看到才调。出错、panic 只记日志，不影响后面的
	OnNotice func(Noticed)
	// Line 是排"给模型看的一行"用的名字查询、引用查询、时区（line.go 的 LineEnv）；Mine 由入库按收到它的角色的号填。
	// 入库时把这一行存进事件（segments.line），实例重建后群友的话才有名字和身份。查询钩子在会话队列里调用，不要阻塞；
	// 钩子出错、panic 只记日志，这条照存（没有这一行，历史里退回转义过的原文）
	Line LineEnv
	// OwnerCall 是这个角色怎么称呼所有者（排所有者那一行的括号用）；nil 或空 = "用户"
	OwnerCall func(character string) string
	// Logf 记日志；nil 用 log.Printf。日志里只放会话、号、结果，不放消息原文
	Logf func(format string, args ...any)
	// Errorf 记 ERROR（有数据会丢、要人处理）；nil 时用 Logf
	Errorf func(format string, args ...any)
	// Now 给没有平台时间的消息打时间；nil 用 time.Now
	Now func() time.Time
	// Selfcheck 是运行自检的计数器（规格-运行时自检）：入库失败等结局往里数；nil 就不数。只数，不改任何一条路径的行为和返回值
	Selfcheck *selfcheck.Counters
}

// Options 是交一条消息时的附加说明
type Options struct {
	// Backfill：这是补录来的，不是实时收到的。补录的只入库、只观察：不进拦截链、不触发回复、不进闸门（规格 第六节"补录和实时的交错"）；
	// 适配器不知道自己交的是不是补录，这个标记由补录的一方（第 8 步）打
	Backfill bool
}

// Stored 是"一条消息入库了"的通知（OnStored 的参数）。
// Inbound、Character、Tags 说的是交来的这一份（哪个号、是不是补录来的）；Actor、Origin、Seq、EventID 说的是库里那条事件——
// 重复到达时是已有的那条，不按这一份重新算（同一条事件在库里和通知里的来源只能有一个口径，清单⑪）
type Stored struct {
	Inbound   types.Inbound
	Character string // 这一份属于哪个角色（收到它的号绑的）
	Actor     Actor
	Origin    string
	Seq       int64
	EventID   string
	Duplicate bool // 键已经在库里：这一份没有新增事件，只给 Character 补了"看到了"（Character 早就看到过的不会通知，见 Config.OnStored）
	Observed  bool // Character 看到了这条没有（陌生人、自检的没有）
	Tags      []string
}

// Result 是一条消息入库的结果
type Result struct {
	Seq       int64
	EventID   string
	Duplicate bool // 同 Stored.Duplicate
	Dropped   bool // 准入检查说不收：没有存，Seq、EventID 是空的
}

// Ticket 是交一条消息以后拿到的凭据：等它能知道这条入库没有（补录要据此推水位）
type Ticket struct {
	done chan struct{}
	res  Result
	err  error
}

func newTicket() *Ticket { return &Ticket{done: make(chan struct{})} }

func (t *Ticket) finish(r Result, err error) {
	t.res, t.err = r, err
	close(t.done)
}

// Wait 等这一条入库（或失败、被丢掉）。ctx 先到就返回 ctx 的错误，这一条照样会继续处理
func (t *Ticket) Wait(ctx context.Context) (Result, error) {
	select {
	case <-t.done:
		return t.res, t.err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Intake 是进来的路径的入库一站
type Intake struct {
	cfg     Config
	q       *lanes
	dropped atomic.Int64 // 关闭时排着没开始、被丢掉的条数

	mu       sync.Mutex
	takeover map[[2]string]*takeoverState // （渠道、角色）→ 实时接管的时刻
	// firstLive 默认是按渠道取库再 Store.FirstLiveAt；单独留一个字段是为了测试能数它被调了几次（缓存该挡住的查询没有漏出去）
	firstLive func(channel, character string) (at int64, has bool, err error)
}

// takeoverState 是 Store.FirstLiveAt 的缓存：查过一次以后，每次这个角色看到一条事件（新存的，或另一个号先存的、这个角色第一次看到的）
// 就按那条事件的时间和是不是补录精确更新，从不作废重查（补录一万条她自己的消息时，每条都扫一遍库是平方级的；
// 两个号同在一个群时，第二个号补录的每一条都是"第一次看到"，作废的话每条都要重扫）
type takeoverState struct {
	loaded bool
	has    bool
	at     int64
}

// NewIntake 造入库一站。名册不合法（号绑了两个角色……）启动即报错
func NewIntake(cfg Config) (*Intake, error) {
	if cfg.Stores == nil {
		return nil, errors.New("没有库")
	}
	if err := cfg.Roster.validate(); err != nil {
		return nil, fmt.Errorf("名册不合法：%w", err)
	}
	firstLive := func(channel, character string) (int64, bool, error) {
		st, err := cfg.Stores.For(channel)
		if err != nil {
			return 0, false, err
		}
		return st.FirstLiveAt(channel, character)
	}
	return &Intake{cfg: cfg, q: newLanes(), takeover: map[[2]string]*takeoverState{}, firstLive: firstLive}, nil
}

func (n *Intake) logf(format string, args ...any) {
	if n.cfg.Logf != nil {
		n.cfg.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (n *Intake) errorf(format string, args ...any) {
	if n.cfg.Errorf != nil {
		n.cfg.Errorf(format, args...)
		return
	}
	n.logf(format, args...)
}

func (n *Intake) now() time.Time {
	if n.cfg.Now != nil {
		return n.cfg.Now()
	}
	return time.Now()
}

// Inbound 实现 types.Sink 的 Inbound：把消息排进它的会话队列就返回，不等入库——适配器只有一个读循环，
// 一个会话的队列慢了不能把别的会话的消息也堵在读循环外面。返回的 error 只表示没收下（关了、号不认识、会话填错了）；
// 入库本身失败（库写不进）只记日志，那一条等补录再取
func (n *Intake) Inbound(ctx context.Context, in types.Inbound) error {
	n.cfg.Selfcheck.Add(selfcheck.IntakeLive) // 实时入口交来的：自检 S1 的交接守恒式（适配器交出 = 接收端丢掉 + 这里收到）
	_, err := n.Submit(ctx, in, Options{})
	return err
}

// Pending 是队列里排着、还没做完的活的个数（收下了、没有结局的）。自检 S1 用它判静默点：
// 拦截链、闸门在会话队列里同步跑，队列被它卡住时，后面排着的消息已经算交来、还没有结局，不能当成丢了。只读，不加队列的锁以外的东西
func (n *Intake) Pending() int {
	n.q.mu.Lock()
	defer n.q.mu.Unlock()
	return n.q.pending
}

// Submit 同 Inbound，但给一个 Ticket，补录等着知道入库的结果
func (n *Intake) Submit(ctx context.Context, in types.Inbound, opt Options) (*Ticket, error) {
	n.cfg.Selfcheck.Touch(time.Now())
	n.cfg.Selfcheck.Add(selfcheck.IntakeSubmitted) // 之后每条返回路径恰好记一个结局（规格-运行时自检 S1）
	if err := ctx.Err(); err != nil {
		n.cfg.Selfcheck.Add(selfcheck.IntakeRejected)
		return nil, err
	}
	acct, err := n.check(in)
	if err != nil {
		n.cfg.Selfcheck.Add(selfcheck.IntakeRejected)
		return nil, err
	}
	// 活是排队以后才做的，适配器可能已经在复用交来的那块内存：段先拷一份，Raw 过长的截短
	in.Segs = cleanSegs(in.Segs)
	tk := newTicket()
	ok := n.q.submit(queueKey(in.Conv), job{
		run: func() {
			// 补录绑定连接寿命：排队期间断线的旧消息不能在新连接里迟到写库。
			if opt.Backfill && ctx.Err() != nil {
				n.cfg.Selfcheck.Add(selfcheck.IntakeCancelled)
				tk.finish(Result{}, ctx.Err())
				return
			}
			n.process(in, opt, acct, tk)
		},
		drop: func() {
			n.dropped.Add(1)
			n.cfg.Selfcheck.Add(selfcheck.IntakeClosed)
			tk.finish(Result{}, ErrClosed)
		},
	})
	if !ok {
		n.cfg.Selfcheck.Add(selfcheck.IntakeClosed)
		return nil, ErrClosed
	}
	return tk, nil
}

// check 收下之前查一遍：号认识、会话填对了、会话和号对得上
func (n *Intake) check(in types.Inbound) (Account, error) {
	if in.Kind != types.InboundMessage && in.Kind != types.InboundSelf {
		return Account{}, fmt.Errorf("入站消息的 Kind %q 不是 message 或 self", in.Kind)
	}
	if in.From.ID == "" {
		return Account{}, errors.New("入站消息没有发言人的号")
	}
	acct, ok := n.cfg.Roster.account(in.Conv.Channel, in.Account)
	if !ok {
		return Account{}, fmt.Errorf("%w：%s:%s", ErrUnknownAccount, in.Conv.Channel, in.Account)
	}
	if err := in.Conv.Valid(); err != nil {
		return Account{}, fmt.Errorf("会话不合法：%w", err)
	}
	// 私聊会话串里带的是收信的号：对不上的话这条会存进另一个号的私聊里（清单⑤）
	if in.Conv.Scene == types.ScenePrivate && in.Conv.Self != in.Account {
		return Account{}, fmt.Errorf("私聊会话属于号 %s，却是号 %s 交来的", in.Conv.Self, in.Account)
	}
	return acct, nil
}

// cleanSegs 拷一份段，并把 Raw 预览截到 RawMax 个字（rune，不切开一个字）。
// 完整卡片的 CardJSON 是适配器限长后的不可变字符串，随段原样落库（规格-链接解析 3.1）。
func cleanSegs(segs []types.Seg) []types.Seg {
	out := make([]types.Seg, len(segs))
	copy(out, segs)
	for i := range out {
		if r := []rune(out[i].Raw); len(r) > types.RawMax {
			out[i].Raw = string(r[:types.RawMax])
		}
	}
	return out
}

// queueKey 是会话队列的键：用结构的各个字段拼，不拆会话字符串
func queueKey(c types.Conversation) string {
	return c.Channel + "\x00" + c.Scene + "\x00" + c.Self + "\x00" + c.ID
}

// process 在会话队列里做一条：准入、入库、通知。panic（准入检查或后面的步骤里）也接住，凭据一定要有结果，队列不能因此停
func (n *Intake) process(in types.Inbound, opt Options, acct Account, tk *Ticket) {
	settled := false
	defer func() {
		if r := recover(); r != nil {
			n.logf("渠道 %s 会话 %s 处理一条收到的消息时出了意外（准入、入库或入库后的步骤）：%v", in.Conv.Channel, in.Conv.String(), r)
			if !settled {
				n.cfg.Selfcheck.Add(selfcheck.IntakePanicked)
				tk.finish(Result{}, fmt.Errorf("处理时出了意外：%v", r))
			}
		}
	}()

	var adm Admission
	if n.cfg.Admit != nil {
		adm = n.cfg.Admit(in)
	}
	if adm.Drop {
		settled = true
		n.cfg.Selfcheck.Add(selfcheck.IntakeAdmitDropped)
		tk.finish(Result{Dropped: true}, nil)
		return
	}

	// 老历史不能拿同文字冒认当前发送的回声；实时仍按出口自己的回声窗口处理。
	if n.cfg.Echo != nil && n.cfg.Roster.Resolve(in.Conv.Channel, in.From.ID).Kind == ActorCharacter &&
		(!opt.Backfill || !in.Time.Before(n.now().Add(-5*time.Minute))) {
		if _, err := n.cfg.Echo.ClaimEcho(in); err != nil {
			n.logf("渠道 %s 会话 %s 认领回声时出错：%v，这条按普通消息存", in.Conv.Channel, in.Conv.String(), err)
		}
	}

	st, notify, err := n.store(in, opt, acct, adm)
	settled = true
	if err != nil {
		n.logf("渠道 %s 会话 %s 入库失败：%v", in.Conv.Channel, in.Conv.String(), err)
		n.cfg.Selfcheck.StoreFailed(selfcheck.StoreErrorClass(err))
		tk.finish(Result{}, err)
		return
	}
	if st.Duplicate {
		n.cfg.Selfcheck.Add(selfcheck.IntakeDuplicate)
	} else {
		n.cfg.Selfcheck.Add(selfcheck.IntakeStored)
	}
	// 先让等结果的人知道入库了，再跑后面的步骤：补录推水位不该等闸门
	tk.finish(Result{Seq: st.Seq, EventID: st.EventID, Duplicate: st.Duplicate}, nil)

	// 这个角色早就看到过的（同一个号重推、补录和实时重叠）入库一站什么也没新增，后面的步骤不再处理一遍
	if notify && n.cfg.OnStored != nil {
		n.cfg.OnStored(st) // panic 由上面的 recover 接住：入库已经成功，凭据已经有结果，队列接着往下走
	}
}

// store 把一条收到的消息写进库。notify 说要不要通知后面的步骤：新存的事件要；重复到达的，只有 Character 是第一次看到才要
// （陌生人、自检这类不记"看到了"的，重复的就没有新东西）
func (n *Intake) store(in types.Inbound, opt Options, acct Account, adm Admission) (st Stored, notify bool, err error) {
	segs := in.Segs // Submit 里已经拷过、不会是 nil
	at := n.now().UnixMilli()
	if !in.Time.IsZero() && in.Time.UnixMilli() > 0 {
		at = in.Time.UnixMilli()
	}

	actor := n.cfg.Roster.Resolve(in.Conv.Channel, in.From.ID)
	origin, err := n.originOf(in.Conv.Channel, actor, at, opt.Backfill)
	if err != nil {
		return Stored{}, false, err
	}

	var tags []string
	if opt.Backfill {
		tags = append(tags, TagBackfill)
	}
	if in.Kind == types.InboundSelf || in.From.ID == in.Account {
		tags = append(tags, TagFromSelf)
	}
	tags = append(tags, adm.Tags...)

	var replyTo string
	for _, s := range segs {
		if s.Kind == types.SegReply {
			replyTo = s.Target
			break
		}
	}
	// 存的是段（中立的种类名）、发言当时的名字、标签，和排好的一行（Bun 的 segments.line）：名片以后改了，重建出来的历史照旧，缓存不断
	body := struct {
		Segs []types.Seg `json:"segs"`
		From types.User  `json:"from"`
		Tags []string    `json:"tags,omitempty"`
		Line string      `json:"line,omitempty"`
	}{segs, in.From, tags, n.lineOf(in, segs, actor, origin, acct, at)}

	var observers []string
	if !adm.NoObserve {
		observers = []string{acct.Character}
	}
	db, err := n.cfg.Stores.For(in.Conv.Channel)
	if err != nil {
		return Stored{}, false, err
	}
	res, err := db.AppendInbound(store.NewInbound{
		Event: store.NewEvent{
			Channel: in.Conv.Channel, Conversation: in.Conv.String(),
			ActorKind: actor.Kind, ActorID: actor.ID, Origin: origin, Kind: "message",
			Text: PlainText(segs), Segments: body, CreatedAt: at,
		},
		PlatformID:      in.PlatformID,
		ReplyPlatformID: replyTo,
		Observers:       observers,
	})
	if err != nil {
		return Stored{}, false, err
	}
	if !store.HasPlatformID(in.PlatformID) {
		// 没有号就没法去重（规格 第六节第 3 步）：不同的消息不能因为"都没有号"被当成同一条，所以每条都存。
		// 协议端该给号而没给的话，重连后重推的消息会存成两条——留一行日志才看得出来。只写会话和号的原值，不写消息原文
		n.logf("渠道 %s 会话 %s 的一条消息没有平台消息号（原值 %q），不能去重，按单条入库", in.Conv.Channel, in.Conv.String(), in.PlatformID)
	}
	if !adm.NoObserve {
		n.noteObserved(in.Conv.Channel, acct.Character, at, res, opt.Backfill)
	}
	if res.Duplicate {
		// 重复到达：Actor、Origin 说的是库里已有的那条，不是这一份按这一次的情况算出来的
		actor, origin = Actor{Kind: res.ActorKind, ID: res.ActorID}, res.Origin
	}
	notify = !res.Duplicate || res.NewObservations > 0
	return Stored{
		Inbound: in, Character: acct.Character, Actor: actor, Origin: origin,
		Seq: res.Seq, EventID: res.EventID, Duplicate: res.Duplicate, Observed: !adm.NoObserve, Tags: tags,
	}, notify, nil
}

// originOf：这条是谁说的（daemon/api/event.ts Origin；Bun 的 originOf）。主人是 owner，别人是 peer；
// 她的号发出、又没被认成 Sumika 发的回声（回声认领是第 2 步，在入库之前）的，是真人手动发的（human）或以前别的程序说的（external）：
// 实时收到的都算 human；补录来的，发在 Sumika 实时接管这个号之前的是 external，之后的是 human。
// 接管时刻是启发式（"第一条实时事件的时间"），没有验证（规格 第十八节"takeoverAt 启发式"：照搬，不作为结论）
func (n *Intake) originOf(channel string, actor Actor, at int64, backfill bool) (string, error) {
	switch actor.Kind {
	case ActorOwner:
		return OriginOwner, nil
	case ActorHuman:
		return OriginPeer, nil
	}
	if !backfill {
		return OriginHuman, nil
	}
	t, ok, err := n.takeoverAt(channel, actor.ID)
	if err != nil {
		return "", err
	}
	if ok && at >= t {
		return OriginHuman, nil
	}
	return OriginExternal, nil
}

// lineOf 排这条消息给模型看的一行。字段的取法和 Pipeline.OnStored 交给链的那条 Msg 一样（Who 是 actor.ID、Name 是 nameOf、
// 她自己的号发的算 Self），所以实例里现排的和库里存的是同一行。出意外返回空串，由调用方照存事件
func (n *Intake) lineOf(in types.Inbound, segs []types.Seg, actor Actor, origin string, acct Account, at int64) (line string) {
	defer func() {
		if r := recover(); r != nil {
			n.logf("渠道 %s 会话 %s 排给模型看的一行时出了意外：%v，这条不存行头", in.Conv.Channel, in.Conv.String(), r)
			line = ""
		}
	}()
	env := n.cfg.Line
	env.Mine = n.cfg.Roster.accountsOf(in.Conv.Channel, acct.Character)
	if n.cfg.OwnerCall != nil {
		env.Owner = n.cfg.OwnerCall(acct.Character)
	}
	m := types.Msg{
		Time: time.UnixMilli(at), Who: actor.ID, Origin: origin, Segs: segs, Name: nameOf(in.From),
		Self: actor.Kind == ActorCharacter && actor.ID == acct.Character,
	}
	return FormatLine(m, in.Conv, env, time.UnixMilli(at))
}

func (n *Intake) takeoverAt(channel, character string) (int64, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k := [2]string{channel, character}
	st := n.takeover[k]
	if st == nil {
		st = &takeoverState{}
		n.takeover[k] = st
	}
	if !st.loaded {
		at, has, err := n.firstLive(channel, character)
		if err != nil {
			return 0, false, err
		}
		*st = takeoverState{loaded: true, has: has, at: at}
	}
	return st.at, st.has, nil
}

// noteObserved 在一条事件给某个角色记了"看到了"以后，更新这个角色接管时刻的缓存：它新看到的这条事件，
// 是不是补录来的、几点的，加进"第一条实时事件"的取小里。新存的事件是这一份的时间和标记；重复到达而这个角色是第一次看到的，
// 是库里已有那条的（另一个号先存的，它的标记不一定和这一份一样）。这个角色早就看到过的什么都没变，缓存照旧。
// 缓存还没读过的不用管：之后第一次读会从库里取，那时这条已经提交了
func (n *Intake) noteObserved(channel, character string, at int64, res store.InboundResult, backfill bool) {
	if res.Duplicate {
		if res.NewObservations == 0 {
			return
		}
		at, backfill = res.DupCreatedAt, res.DupBackfill
	}
	if backfill {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.takeover[[2]string{channel, character}]
	if st == nil || !st.loaded {
		return
	}
	if !st.has || at < st.at {
		st.has, st.at = true, at
	}
}

// Idle 等到现在收下的消息都处理完（入库了、通知也跑完了，或者被丢掉了）。测试收尾和平稳关闭用：
// 关库之前要先等，不然排着的活还在碰已经关掉的库（LESSONS"异步和收尾"）
func (n *Intake) Idle(ctx context.Context) error {
	select {
	case <-n.q.idleChan():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 关闭：不再收新消息（之后的 Inbound、Submit 返回 ErrClosed，什么也不写）；正在入库的那几条做完；
// 排着没开始的丢掉（等补录再取，它们的 Ticket 得到 ErrClosed）。返回时不会再有任何东西碰库，可以关库了。
// OnStored 里如果卡住，Close 就等它：它自己要有时限
func (n *Intake) Close() {
	n.q.close()
	if d := n.dropped.Swap(0); d > 0 {
		n.logf("渠道关闭时丢掉了 %d 条排着没开始的消息（重启以后靠补录取回）", d)
	}
}
