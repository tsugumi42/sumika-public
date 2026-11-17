// Package contract 是契约测试的框架（规格-渠道中间件 第十五节第 0 步、第十六节 B）：一套对着 types.Adapter 的规矩写的检查，
// 任何适配器实现都拿同一套去过——第 0 步的假适配器（internal/channel/fake）、第 6 步的 OneBot 适配器。
// 规矩的出处是规格，不是哪个实现的习惯（第十六节 B"独立于 Bun"）。
//
// 写法：每条规矩是一个 Case，Check 对一个全新的适配器跑一遍，返回 nil 表示守规矩，返回 error 说明违反了什么。
// 测试里 contract.Adapter(t, 造适配器的函数) 把每个 Case 跑成子测试；检查本身也要在故意写坏的实现上红过一次（第十六节 C），
// 所以 Check 是导出的，contract_test.go 里拿坏实现直接调。
//
// 要求适配器让人知道"Run 起来了"：实现 Started() <-chan struct{}（接好 sink、开始干活以后关闭），或者往 sink 报出第一个 Status
// （连上、重连中都算，真适配器本来就要报）。契约测试等到这个信号才去读 Caps、调 Send、Poke——不等的话，Run 的 goroutine 还没跑到，
// 查到的多半是"还没运行"那条路，运行中才有的写坏查不出来（第 0 步复核时量过：旧写法下 100 次里只有个位数到十几次真在运行中调到）。
// 超时没有信号、或者 ctx 还没取消 Run 就返回了，都算违规。
//
// 这里只收"哪个适配器都该守"的规矩。需要往适配器里推具体事件才能查的（翻译对不对、坏帧不关连接、心跳不进 events……）
// 要有协议端那一头配合，第 6 步 onebot 包带着它的假服务端再加；中间件层的契约（去重、事务、回执状态机……）是第 1 步起中间件自己的测试。
package contract

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 时限。测试里想让"违反规矩"的检查快点红，可以调小；真适配器按默认值
var (
	// StartTimeout：Run 开始之后最多多久要给出"起来了"的信号
	StartTimeout = 2 * time.Second
	// Settle：起来以后观察多久，这段时间里 Run 不能自己返回（只在 run-returns-on-cancel 里等）
	Settle = 20 * time.Millisecond
	// StopTimeout：ctx 取消以后 Run 最多多久必须返回
	StopTimeout = 2 * time.Second
	// CallTimeout：一次 Send、Poke 最多多久必须返回
	CallTimeout = 5 * time.Second
)

// Factory 造一个全新的、还没 Run 的适配器；每条规矩各用各的，互不影响
type Factory func() types.Adapter

// Case 是一条规矩
type Case struct {
	Name  string // 子测试的名字
	Rule  string // 这条规矩的出处（规格的哪一节）
	Check func(f Factory) error
}

// ErrNotApplicable：这个适配器的 Caps 让这条规矩无从查起（比如什么都支持，就没有"不支持"可查）。Check 用 %w 包着它返回，
// Adapter 把这样的子测试标成跳过
var ErrNotApplicable = errors.New("不适用")

// Cases 是全部规矩，按这个顺序跑
func Cases() []Case {
	return []Case{
		{"caps-fixed", "第五节：Name 不空，Caps 启动时读一次、之后不变（运行中也读一次）", capsFixed},
		{"run-returns-on-cancel", "第五节、第十八节④：Run 起来要有信号，ctx 取消前不返回，取消后要返回且返回 nil", runReturnsOnCancel},
		{"receipt-valid", "第五节、第十六节 B 回执缺失：还没 Run 和运行中，Send、Poke 一律给合法的回执，error 只给 ErrUnsupported", receiptValid},
		{"unsupported-is-failure", "第五节：Caps 里没有的动作返回 ErrUnsupported，不能当成功", unsupportedIsFailure},
		{"stopped-never-confirms", "第十八节④、第十六节 B 回执缺失：Run 返回以后，Send、Poke 照样给合法的回执，不能再报确认", stoppedNeverConfirms},
	}
}

// Adapter 把每条规矩跑成 t 的子测试
func Adapter(t *testing.T, f Factory) {
	t.Helper()
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			err := c.Check(f)
			if errors.Is(err, ErrNotApplicable) {
				t.Skip(err.Error())
			}
			if err != nil {
				t.Errorf("%v\n  规矩：%s", err, c.Rule)
			}
		})
	}
}

// startSink 是契约测试交给 Run 的 sink：事件记在 Recorder 里，第一个 Status 到达就通知 start（"起来了"的信号之一）
type startSink struct {
	fake.Recorder
	first chan struct{}
	once  sync.Once
}

var _ types.Sink = (*startSink)(nil)

func (s *startSink) Status(ctx context.Context, st types.Status) error {
	err := s.Recorder.Status(ctx, st)
	s.once.Do(func() { close(s.first) })
	return err
}

// session 是把一个适配器跑在后台：stop 取消它的 ctx，等 Run 返回
type session struct {
	cancel context.CancelFunc
	done   chan error
}

func earlyReturn(err error) error {
	return fmt.Errorf("ctx 还没取消，Run 就返回了（返回值 %v）：Run 要一直阻塞到 ctx 取消", err)
}

// start 把适配器跑起来，等到它给出"起来了"的信号（Started() 关闭，或者第一个 Status）才返回，之后的检查才是在运行中查的。
// Run 先返回了、或者 StartTimeout 内没有信号，都是违规，返回 error（这时 Run 已被取消）
func start(a types.Adapter) (*session, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{cancel: cancel, done: make(chan error, 1)}
	sink := &startSink{first: make(chan struct{})}
	go func() { s.done <- a.Run(ctx, sink) }()

	var started <-chan struct{} // 适配器没有 Started() 就一直是 nil，那一路永远不会触发，只靠第一个 Status
	if st, ok := a.(interface{ Started() <-chan struct{} }); ok {
		started = st.Started()
	}
	timer := time.NewTimer(StartTimeout)
	defer timer.Stop()
	select {
	case <-started:
	case <-sink.first:
	case err := <-s.done:
		cancel()
		return nil, earlyReturn(err)
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("Run 开始 %s 内没有给出起来的信号：既没有关闭 Started()，也没有往 sink 报 Status，契约测试不知道它什么时候算在运行", StartTimeout)
	}
	return s, nil
}

// stop 取消并等 Run 返回；取消之前它就已经返回了、超时、或者返回了 nil 以外的错（context.Canceled 也不要）都算违规。
// 超时以后那个 Run 的 goroutine 就漏在那儿了，适配器已经违规，漏就漏吧
func (s *session) stop() error {
	select {
	case err := <-s.done:
		return earlyReturn(err)
	default:
	}
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			return fmt.Errorf("ctx 取消后 Run 返回了 %v，要 nil（取消是正常的收尾，不是错误）", err)
		}
		return nil
	case <-time.After(StopTimeout):
		return fmt.Errorf("ctx 取消 %s 后 Run 还没返回", StopTimeout)
	}
}

// outcome 是一次 Send 或 Poke 的结果；timedOut 为真是适配器过了 CallTimeout 还不返回（没理 ctx）
type outcome struct {
	r        types.Receipt
	err      error
	timedOut bool
}

// call 在 CallTimeout 内跑一次 Send 或 Poke：ctx 到期以后再多等 StopTimeout，还不返回就是不理 ctx
func call(do func(ctx context.Context) (types.Receipt, error)) outcome {
	ctx, cancel := context.WithTimeout(context.Background(), CallTimeout)
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		r, err := do(ctx)
		done <- outcome{r: r, err: err}
	}()
	select {
	case x := <-done:
		return x
	case <-time.After(CallTimeout + StopTimeout):
		return outcome{timedOut: true}
	}
}

// 规矩用的编的会话、账号：号不是真实的
const (
	testAccount = "acct-a"
	testGroup   = "group-1"
	testPeer    = "user-1"
)

func testConv(a types.Adapter) types.Conversation {
	return types.Conversation{Channel: a.Name(), Scene: types.SceneGroup, ID: testGroup}
}

func textPart(a types.Adapter) types.Part {
	return types.Part{ID: "plan-1/0", Conv: testConv(a), Kind: types.PartMessage, Body: types.TextBody("你好")}
}

func imagePart(a types.Adapter) types.Part {
	p := textPart(a)
	p.ID, p.Body = "plan-1/1", []types.BodySegment{{Kind: types.BodyImage, Media: types.MemoryMedia("fixture", "image/png", 1, 1, []byte{1})}}
	return p
}

// actionParts 覆盖群和私聊；私聊回应始终拒绝。
func actionParts(a types.Adapter) []types.Part {
	var parts []types.Part
	for _, scene := range []string{types.SceneGroup, types.ScenePrivate} {
		conv := testConv(a)
		conv.Scene = scene
		if scene == types.ScenePrivate {
			conv.ID, conv.Self = testPeer, testAccount
		}
		parts = append(parts,
			types.Part{ID: "plan-action/0", Conv: conv, Kind: types.PartMessage, Body: types.TextBody("引用正文"), Quote: "-90004"},
			types.Part{ID: "plan-action/1", Conv: conv, Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "外部内容·纸鸢", Body: types.TextBody("编的文章")}}},
			types.Part{ID: "plan-action/2", Conv: conv, Kind: types.PartReact, Ref: "-90004", EmojiID: "66"})
	}
	return parts
}

// 适配器所处的阶段，错误信息里用
const (
	phaseBefore  = "还没 Run"
	phaseRunning = "运行中"
	phaseStopped = "Run 返回以后"
)

// checkOutcome 按"结果一律写在回执里"查一次调用：不理 ctx、返回 error（文字段、Caps 里有的 Poke 都不该出 ErrUnsupported，
// 否则中间件会当成"不支持、别重试"，永远发不出去）、回执不合法，都违规；notConfirm 为真时回执还不能是确认
func checkOutcome(what, phase string, x outcome, notConfirm bool) error {
	switch {
	case x.timedOut:
		return fmt.Errorf("%s：%s 在 ctx 到期后还没有返回，没理 ctx", phase, what)
	case x.err != nil:
		return fmt.Errorf("%s：%s 返回了 error：%v（结果写在回执里，error 只给 Caps 里没有的动作的 ErrUnsupported）", phase, what, x.err)
	}
	if err := x.r.Validate(); err != nil {
		return fmt.Errorf("%s：%s 的回执不合法：%v", phase, what, err)
	}
	if notConfirm && x.r.Status == types.ReceiptConfirmed {
		return fmt.Errorf("%s：%s 还报了确认：关掉的渠道不能再说发出去了", phase, what)
	}
	return nil
}

// checkCalls 在适配器的某个阶段调一次 Send 文字段，Caps.Poke 为真时再调一次 Poke，各自过 checkOutcome；
// Send 的回执还要照抄 Part.ID
func checkCalls(a types.Adapter, phase string, notConfirm bool) error {
	p := textPart(a)
	x := call(func(ctx context.Context) (types.Receipt, error) { return a.Send(ctx, testAccount, p) })
	if err := checkOutcome("Send 文字段", phase, x, notConfirm); err != nil {
		return err
	}
	if x.r.PartID != p.ID {
		return fmt.Errorf("%s：Send 的回执 PartID 是 %q，要照抄 Part.ID %q", phase, x.r.PartID, p.ID)
	}
	for _, p := range actionParts(a) {
		if !a.Caps().Supports(p) {
			continue
		}
		x := call(func(ctx context.Context) (types.Receipt, error) { return a.Send(ctx, testAccount, p) })
		if err := checkOutcome("Send "+p.Kind+" "+p.Conv.Scene, phase, x, notConfirm); err != nil {
			return err
		}
		if x.r.PartID != p.ID || (p.Kind == types.PartReact && x.r.PlatformID != "") {
			return fmt.Errorf("%s：%s 回执的段号或平台消息号不合规：%+v", phase, p.Kind, x.r)
		}
	}
	if !a.Caps().Poke {
		return nil
	}
	conv := testConv(a)
	x = call(func(ctx context.Context) (types.Receipt, error) { return a.Poke(ctx, testAccount, conv, testPeer) })
	return checkOutcome("Poke", phase, x, notConfirm)
}

func capsFixed(f Factory) error {
	a := f()
	name, before := a.Name(), a.Caps()
	if name == "" {
		return errors.New("Name() 是空的：它是会话号的第一段，不能空")
	}
	s, err := start(a)
	if err != nil {
		return err
	}
	during := a.Caps() // start 等到了起来的信号，这一次是运行中读的
	if err := s.stop(); err != nil {
		return err
	}
	after := a.Caps()
	if during != before || after != before {
		return fmt.Errorf("Caps 变了：运行前 %+v，运行中 %+v，停止后 %+v", before, during, after)
	}
	if a.Name() != name {
		return fmt.Errorf("Name() 变了：%q → %q", name, a.Name())
	}
	return nil
}

func runReturnsOnCancel(f Factory) error {
	s, err := start(f())
	if err != nil {
		return err
	}
	time.Sleep(Settle) // 起来以后观察一小会儿：Run 要一直阻塞，不能起来就自己返回（stop 里会查）
	return s.stop()
}

func receiptValid(f Factory) error {
	a := f()
	if err := checkCalls(a, phaseBefore, false); err != nil {
		return err
	}
	s, err := start(a)
	if err != nil {
		return err
	}
	defer s.stop()
	return checkCalls(a, phaseRunning, false)
}

func unsupportedIsFailure(f Factory) error {
	a := f()
	caps := a.Caps()
	s, err := start(a)
	if err != nil {
		return err
	}
	defer s.stop()

	if !caps.MediaGroup.Image {
		p := imagePart(a)
		x := call(func(ctx context.Context) (types.Receipt, error) { return a.Send(ctx, testAccount, p) })
		switch {
		case x.timedOut:
			return fmt.Errorf("Send 在 ctx 到期后还没有返回，没理 ctx")
		case !errors.Is(x.err, types.ErrUnsupported):
			return fmt.Errorf("MediaGroup.Image 为假，Send 图片却返回了 error=%v、回执=%+v，要 ErrUnsupported", x.err, x.r)
		}
	}
	if !caps.Poke {
		conv := testConv(a)
		x := call(func(ctx context.Context) (types.Receipt, error) { return a.Poke(ctx, testAccount, conv, testPeer) })
		switch {
		case x.timedOut:
			return fmt.Errorf("Poke 在 ctx 到期后还没有返回，没理 ctx")
		case !errors.Is(x.err, types.ErrUnsupported):
			return fmt.Errorf("Caps.Poke 为假，Poke 却返回了 error=%v、回执=%+v，要 ErrUnsupported", x.err, x.r)
		}
	}
	for _, p := range actionParts(a) {
		if caps.Supports(p) {
			continue
		}
		x := call(func(ctx context.Context) (types.Receipt, error) { return a.Send(ctx, testAccount, p) })
		if x.timedOut || !errors.Is(x.err, types.ErrUnsupported) {
			return fmt.Errorf("Caps 不支持 %s %s，却返回 error=%v、回执=%+v、超时=%v", p.Conv.Scene, p.Kind, x.err, x.r, x.timedOut)
		}
	}
	return nil
}

func stoppedNeverConfirms(f Factory) error {
	a := f()
	s, err := start(a)
	if err != nil {
		return err
	}
	if err := s.stop(); err != nil {
		return err
	}
	return checkCalls(a, phaseStopped, true)
}
