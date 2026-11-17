// Package fake 是"假协议端"（规格-渠道中间件 第十五节第 0 步）：一个实现了 types.Adapter 的、由测试操纵的适配器。
// 第 0 到 4 步全靠它测，不碰 SnowLuma。它站在协议端的位置：测试从这一头往里推消息、通知、状态（PushInbound 等），
// 并规定下一次 Send、Poke 怎么回（OnSend、OnPoke）；Sent、Pokes 记下中间件都发了什么。
//
// 这里的"协议端"指适配器这一层的替身，不是 OneBot 线上的格式：假的 OneBot WebSocket 服务端（按 OneBot v11 回包）是第 6 步
// onebot 包自己测试用的，到时候它也过 contract 里同一套契约。
//
// 它自己守着适配器的规矩（types.Adapter 的注释），所以默认的行为是合规的；要测"适配器不守规矩"（零值回执、迟到的回执）时，
// 用 OnSend 故意返回那样的回执——假适配器不替中间件检查回执。
package fake

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
)

var (
	// ErrNotRunning：适配器没在运行（还没 Run，或者已经停了），事件推不进去。推不进去的事件不会交给 sink
	ErrNotRunning = errors.New("假适配器没在运行")
	// ErrAlreadyRunning：同一个适配器同时只跑一个 Run
	ErrAlreadyRunning = errors.New("假适配器已经在运行")
)

// SendCall 是中间件调了一次 Send（不管是不是支持、有没有在运行，都记）
type SendCall struct {
	N       int // 第几次调用，从 1 数（Send 和 Poke 各数各的）
	Account string
	Part    types.Part
	At      time.Time
}

// PokeCall 是中间件调了一次 Poke
type PokeCall struct {
	N       int
	Account string
	Conv    types.Conversation
	Target  string
	At      time.Time
}

// SendFunc、PokeFunc 规定一次调用怎么回：可以阻塞（用 ctx 取消它），可以返回不合规的回执。
// 在运行、而且支持这个动作时才会被调用
type (
	SendFunc func(ctx context.Context, c SendCall) (types.Receipt, error)
	PokeFunc func(ctx context.Context, c PokeCall) (types.Receipt, error)
)

// Confirmed、Failed、Unknown 造一份合规的回执（Via 是回包）
func Confirmed(partID, platformID string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptConfirmed, PlatformID: platformID, Via: types.ViaReply, At: at}
}
func Failed(partID, reason string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptFailed, Via: types.ViaReply, Err: reason, At: at}
}
func Unknown(partID, reason string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptUnknown, Via: types.ViaReply, Err: reason, At: at}
}

// Adapter 是假适配器
type Adapter struct {
	// Now 给回执和调用记录打时间；测试里可以换成固定的钟，nil 用 time.Now
	Now func() time.Time

	name string
	caps types.Caps

	mu       sync.Mutex
	running  bool
	sink     types.Sink
	inflight sync.WaitGroup // 正在交给 sink 的事件：Run 要等它们交完才返回，返回以后不再碰 sink
	started  chan struct{}
	once     sync.Once
	sends    []SendCall
	pokes    []PokeCall
	onSend   SendFunc
	onPoke   PokeFunc
}

var _ types.Adapter = (*Adapter)(nil)

// New 造一个假适配器：name 是渠道名，caps 是它声明的能力。默认每次 Send、Poke 都确认（平台消息号是递增的编号）
func New(name string, caps types.Caps) *Adapter {
	return &Adapter{name: name, caps: caps, started: make(chan struct{})}
}

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Adapter) Name() string     { return a.name }
func (a *Adapter) Caps() types.Caps { return a.caps }

// Run 记下 sink，一直阻塞到 ctx 取消；返回前先等在途的事件交完，所以返回以后 sink 不会再被调用
func (a *Adapter) Run(ctx context.Context, sink types.Sink) error {
	if sink == nil {
		return errors.New("sink 是空的")
	}
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return ErrAlreadyRunning
	}
	a.running, a.sink = true, sink
	a.mu.Unlock()
	a.once.Do(func() { close(a.started) })

	<-ctx.Done()

	a.mu.Lock()
	a.running, a.sink = false, nil
	a.mu.Unlock()
	a.inflight.Wait()
	return nil
}

// Started 在第一次 Run 把 sink 接好以后关闭，测试用它等适配器起来，不靠 sleep；契约测试（contract）也认它当"起来了"的信号
func (a *Adapter) Started() <-chan struct{} { return a.started }

// Running 现在是不是在运行
func (a *Adapter) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// enter 登记一个要交给 sink 的事件；done 在交完以后调。没在运行就不让交（返回 ErrNotRunning）
func (a *Adapter) enter() (sink types.Sink, done func(), err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return nil, nil, ErrNotRunning
	}
	a.inflight.Add(1)
	return a.sink, a.inflight.Done, nil
}

// PushInbound、PushNotice、PushStatus 是协议端往适配器推一个事件：原样交给 sink，返回 sink 的 error。
// 没在运行（还没起、或者已经停了）返回 ErrNotRunning，不交给 sink
func (a *Adapter) PushInbound(ctx context.Context, in types.Inbound) error {
	sink, done, err := a.enter()
	if err != nil {
		return err
	}
	defer done()
	return sink.Inbound(ctx, in)
}

func (a *Adapter) PushNotice(ctx context.Context, n types.Notice) error {
	sink, done, err := a.enter()
	if err != nil {
		return err
	}
	defer done()
	return sink.Notice(ctx, n)
}

func (a *Adapter) PushStatus(ctx context.Context, s types.Status) error {
	sink, done, err := a.enter()
	if err != nil {
		return err
	}
	defer done()
	return sink.Status(ctx, s)
}

// OnSend、OnPoke 规定之后的调用怎么回；不设就是默认的确认
func (a *Adapter) OnSend(f SendFunc) {
	a.mu.Lock()
	a.onSend = f
	a.mu.Unlock()
}

func (a *Adapter) OnPoke(f PokeFunc) {
	a.mu.Lock()
	a.onPoke = f
	a.mu.Unlock()
}

// Sent、Pokes 是到现在为止中间件的所有调用（拷贝）
func (a *Adapter) Sent() []SendCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]SendCall(nil), a.sends...)
}

func (a *Adapter) Pokes() []PokeCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]PokeCall(nil), a.pokes...)
}

// Send 规矩见 types.Adapter：不支持的返回 ErrUnsupported；没在运行是明确失败（没连上）；其余照 OnSend，不设就确认
func (a *Adapter) Send(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
	a.mu.Lock()
	c := SendCall{N: len(a.sends) + 1, Account: account, Part: p, At: a.now()}
	a.sends = append(a.sends, c)
	run, f := a.running, a.onSend
	a.mu.Unlock()

	switch {
	case !a.caps.Supports(p):
		return types.Receipt{}, types.ErrUnsupported
	case !run:
		return Failed(p.ID, "适配器没在运行", c.At), nil
	case f != nil:
		return f(ctx, c)
	case p.Kind == types.PartReact:
		return Confirmed(p.ID, "", c.At), nil
	}
	return Confirmed(p.ID, strconv.Itoa(1000+c.N), c.At), nil
}

// Poke 规矩同 Send
func (a *Adapter) Poke(ctx context.Context, account string, conv types.Conversation, target string) (types.Receipt, error) {
	a.mu.Lock()
	c := PokeCall{N: len(a.pokes) + 1, Account: account, Conv: conv, Target: target, At: a.now()}
	a.pokes = append(a.pokes, c)
	run, f := a.running, a.onPoke
	a.mu.Unlock()

	switch {
	case !a.caps.Poke:
		return types.Receipt{}, types.ErrUnsupported
	case !run:
		return Failed("", "适配器没在运行", c.At), nil
	case f != nil:
		return f(ctx, c)
	}
	return Confirmed("", strconv.Itoa(2000+c.N), c.At), nil
}

// Recorder 是记下所有交给它的事件的 Sink：测适配器时当 sink 用
type Recorder struct {
	mu       sync.Mutex
	inbounds []types.Inbound
	notices  []types.Notice
	statuses []types.Status
}

var _ types.Sink = (*Recorder)(nil)

func (r *Recorder) Inbound(_ context.Context, in types.Inbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inbounds = append(r.inbounds, in)
	return nil
}

func (r *Recorder) Notice(_ context.Context, n types.Notice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
	return nil
}

func (r *Recorder) Status(_ context.Context, s types.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, s)
	return nil
}

// Inbounds、Notices、Statuses 是收到的（拷贝），按到达的先后
func (r *Recorder) Inbounds() []types.Inbound {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.Inbound(nil), r.inbounds...)
}

func (r *Recorder) Notices() []types.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.Notice(nil), r.notices...)
}

func (r *Recorder) Statuses() []types.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.Status(nil), r.statuses...)
}
