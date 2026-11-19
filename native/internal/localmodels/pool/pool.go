package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/localmodels/engine"
)

// 默认值（规格 GM4"具体参数"）。Idle 是例外：0 就是"不自动卸"（生产传 --idle-unload 0），所以它的 600 秒默认只在命令行层。
const (
	DefaultMaxLoaded    = 2
	DefaultIdle         = 600 * time.Second
	DefaultMaxBytes     = uint64(4) << 30
	DefaultWait         = 30 * time.Second
	DefaultReapInterval = 15 * time.Second
	DefaultCloseWait    = 10 * time.Second
)

// 实测的内存超过估计的 3/2 倍就写一行告警（规格 2.5 池一行）。
const (
	warnNum = 3
	warnDen = 2
)

// Loaded 是加载函数交回的东西：嵌入模型给 Embedder，重排模型给 Reranker，二选一，要和登记表的 kind 对得上。
// Close 释放它占的会话（卸载时池调用，且一定是没有租约的时候）；nil 当作没有东西可释放。
type Loaded struct {
	Embedder *engine.Embedder
	Reranker *engine.Reranker
	HashMs   int64 // 加载里花在核对文件哈希上的毫秒数，只用于日志（缓存命中就是 0）
	Close    func() error
}

// LoadFunc 读文件、核哈希、建分词器和会话。它在池自己的 goroutine 里跑，不持任何池锁；ctx 在池关闭时取消，
// 不随请求取消。失败时它要自己把已经建出来的东西关掉，不留半成品。
type LoadFunc func(ctx context.Context, spec engine.Spec) (Loaded, error)

// EstimateFunc 在加载之前估计这个模型要占多少字节（要读文件大小；文件不在就返回错误，说清缺的是哪个）。
// 在池锁外调用，每次冷加载一次。
type EstimateFunc func(spec engine.Spec) (uint64, error)

// Entry 是池里登记的一个模型。
type Entry struct {
	Spec engine.Spec
	// Problem 是启动时就知道的问题（比如 --lazy-models 里的模型文件缺了）：非空时这个模型 ready=false，
	// 别的模型不受影响；下一次加载成功就清掉，所以文件补上以后不用重启。
	Problem string
}

// Config 是池的配置。零值都有默认（Idle 除外，见上）。
type Config struct {
	Models   []Entry
	Load     LoadFunc
	Estimate EstimateFunc

	MaxLoaded    int           // 同时装几个：0 = 缺省 2，负数按 1
	Idle         time.Duration // 空闲多久卸载；0 = 不自动卸
	MaxBytes     uint64        // 内存估计的软上限，缺省 4 GiB
	Wait         time.Duration // Acquire 最多等多久（槽位满、正在加载），缺省 30 秒
	ReapInterval time.Duration // 空闲检查间隔，缺省 15 秒
	CloseWait    time.Duration // Close 最多等仍被租用的模型多久，缺省 10 秒；到点强制关

	Clock  Clock                  // 缺省真时钟
	RSS    func() (uint64, error) // 读进程常驻内存（字节），缺省 ProcessRSS；读不到就不对账
	LogOut io.Writer              // 日志写哪里，缺省 os.Stderr
}

// Pool 是模型池。可以多个 goroutine 同时用。
type Pool struct {
	load     LoadFunc
	estimate EstimateFunc
	rss      func() (uint64, error)
	clock    Clock
	logger   *log.Logger

	maxLoaded    int
	idle         time.Duration
	maxBytes     uint64
	wait         time.Duration
	reapInterval time.Duration
	closeWait    time.Duration

	slots map[string]*slot // New 以后不再增删，读它不用锁；slot 里的字段要持 mu
	order []string         // 按 id 排好，遍历和挑卸载对象时顺序稳定

	mu      sync.Mutex
	changed chan struct{} // 记账有变动时关掉重建：放租约、卸载完、加载完。等槽位的人等它，不轮询
	closed  bool

	ctx      context.Context // 加载函数用的 ctx，Close 时取消
	cancel   context.CancelFunc
	loadTurn chan struct{} // 容量 1：同一时刻只加载一个模型，加载前后的 RSS 差才只含这一次
	loads    sync.WaitGroup

	stop       chan struct{}
	reaperDone chan struct{} // 没有启动 reaper（Idle=0）时为 nil
	closeOnce  sync.Once

	waiting atomic.Int32 // 正在等槽位 / 加载的 Acquire 数，测试用来知道"已经在等了"
}

type slot struct {
	spec    engine.Spec
	problem string  // 最近一次已知的问题，空 = 没问题
	est     uint64  // 最近一次估计，Snapshot 用
	model   *model  // 已加载
	flight  *flight // 正在加载
	closing *model  // 正在卸载：Session.Close 还没返回，它占的名额和字节照记
}

type model struct {
	loaded   Loaded
	refs     int
	lastUsed time.Time
	loadMs   int64
	hashMs   int64
	estimate uint64 // 加载前的估计
	measured uint64 // 加载前后进程常驻内存的差，没量到是 0
	charge   uint64 // 记账用的：max(估计, 实测)
}

// flight 是一次进行中的加载。第一个请求者（leader）等它，后来的（follower）等同一个 done。
type flight struct {
	done       chan struct{}
	reserve    uint64 // 加载期间记账用的估计
	leaderGone bool   // leader 已经不等了（ctx 取消或超时）：装好以后不替它占引用
	handed     bool   // 装好时替 leader 占了一个引用，等它认领
	finished   bool
	model      *model
	err        *Error
}

// New 建池并启动空闲卸载（Idle > 0 才有）。池里每个模型的登记项都要通过 Spec.Validate。
func New(cfg Config) (*Pool, error) {
	if cfg.Load == nil || cfg.Estimate == nil {
		return nil, errors.New("pool: 必须给 Load 和 Estimate")
	}
	if len(cfg.Models) == 0 {
		return nil, errors.New("pool: 没有登记任何模型")
	}
	p := &Pool{
		load:         cfg.Load,
		estimate:     cfg.Estimate,
		rss:          cfg.RSS,
		clock:        cfg.Clock,
		maxLoaded:    cfg.MaxLoaded,
		idle:         cfg.Idle,
		maxBytes:     cfg.MaxBytes,
		wait:         cfg.Wait,
		reapInterval: cfg.ReapInterval,
		closeWait:    cfg.CloseWait,
		slots:        make(map[string]*slot, len(cfg.Models)),
		changed:      make(chan struct{}),
		loadTurn:     make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
	if p.rss == nil {
		p.rss = ProcessRSS
	}
	if p.clock == nil {
		p.clock = realClock{}
	}
	switch { // 没配（0）用默认；配成负数夹成 1，和 server.py 的 max(1, n) 一样（命令行传 0 要由调用方先夹成 1）
	case p.maxLoaded == 0:
		p.maxLoaded = DefaultMaxLoaded
	case p.maxLoaded < 0:
		p.maxLoaded = 1
	}
	if p.idle < 0 {
		p.idle = 0
	}
	if p.maxBytes == 0 {
		p.maxBytes = DefaultMaxBytes
	}
	if p.wait <= 0 {
		p.wait = DefaultWait
	}
	if p.reapInterval <= 0 {
		p.reapInterval = DefaultReapInterval
	}
	if p.closeWait <= 0 {
		p.closeWait = DefaultCloseWait
	}
	out := cfg.LogOut
	if out == nil {
		out = os.Stderr
	}
	p.logger = log.New(out, "", log.LstdFlags)
	for _, e := range cfg.Models {
		if err := e.Spec.Validate(); err != nil {
			return nil, err
		}
		if _, dup := p.slots[e.Spec.ID]; dup {
			return nil, fmt.Errorf("pool: %s 登记了两次", e.Spec.ID)
		}
		spec := e.Spec
		spec.AllowedDimensions = append([]int(nil), spec.AllowedDimensions...)
		p.slots[spec.ID] = &slot{spec: spec, problem: e.Problem}
		p.order = append(p.order, spec.ID)
	}
	sort.Strings(p.order)
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if p.idle > 0 {
		p.reaperDone = make(chan struct{})
		go p.reap()
	}
	return p, nil
}

func (p *Pool) logf(format string, a ...any) { p.logger.Printf(format, a...) }

// notifyLocked 叫醒所有在等"有变动"的人。必须持 mu。
func (p *Pool) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// ---- 查 ----

// Require 按 id 和用途取登记项：不认识、种类不对都是 404 的 *Error（和 server.py 的 require 一样）。
func (p *Pool) Require(id string, kind engine.Kind) (engine.Spec, error) {
	sl, ok := p.slots[id]
	if !ok {
		return engine.Spec{}, p.unknownModel(id)
	}
	if sl.spec.Kind != kind {
		return engine.Spec{}, newError(404, "Model '%s' is a %s model, not %s", id, sl.spec.Kind, kind)
	}
	return copySpec(sl.spec), nil
}

func (p *Pool) unknownModel(id string) *Error {
	return newError(404, "Unknown model '%s'; available: %s", id, strings.Join(p.order, ", "))
}

func copySpec(s engine.Spec) engine.Spec {
	s.AllowedDimensions = append([]int(nil), s.AllowedDimensions...)
	return s
}

// ---- 租约 ----

// Lease 是一次对已加载模型的占用：持有期间池不会卸载它。用完调 Release（可以重复调，只算一次）。
type Lease struct {
	p    *Pool
	sl   *slot
	m    *model
	once sync.Once
}

// Embedder 是嵌入模型的计算入口（重排模型是 nil）。
func (l *Lease) Embedder() *engine.Embedder { return l.m.loaded.Embedder }

// Reranker 是重排模型的计算入口（嵌入模型是 nil）。
func (l *Lease) Reranker() *engine.Reranker { return l.m.loaded.Reranker }

// Spec 是这个模型的登记项。
func (l *Lease) Spec() engine.Spec { return copySpec(l.sl.spec) }

// LoadMs 是这一份加载花了多少毫秒（含核哈希）。
func (l *Lease) LoadMs() int64 { return l.m.loadMs }

// Release 放开租约。放开以后它才可能被卸载。
func (l *Lease) Release() {
	l.once.Do(func() {
		p := l.p
		p.mu.Lock()
		l.m.refs--
		l.m.lastUsed = p.clock.Now()
		p.notifyLocked()
		p.mu.Unlock()
	})
}

// ---- 加载 ----

// Acquire 取一个模型的租约，没加载就加载。槽位满（都在用）或还在加载时放开锁等到 Wait 截止才回 503，不是当场回；
// 一个模型自己就超过内存上限的才立刻回。同一个 id 同时只有一个在加载，别的请求等同一次结果（失败也共用）。
// 调用方的 ctx 取消了返回 ctx 的错误（已经开始的加载不受影响，会装好留在池里）。
func (p *Pool) Acquire(ctx context.Context, id string) (*Lease, error) {
	sl, ok := p.slots[id]
	if !ok {
		return nil, p.unknownModel(id)
	}
	start := p.clock.Now()
	var timer Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	deadline := func() <-chan time.Time { // 第一次要等的时候才建；截止时间从 Acquire 开始算
		if timer == nil {
			left := p.wait - p.clock.Now().Sub(start)
			if left < 0 {
				left = 0
			}
			timer = p.clock.NewTimer(left)
		}
		return timer.C()
	}

	var est uint64
	haveEst := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, newError(503, "The model pool is shutting down")
		}
		if m := sl.model; m != nil {
			m.refs++
			m.lastUsed = p.clock.Now()
			p.mu.Unlock()
			return &Lease{p: p, sl: sl, m: m}, nil
		}

		var (
			waitCh <-chan struct{}
			reason string
			fl     *flight
		)
		switch {
		case sl.flight != nil: // 别人正在加载同一个：等同一个结果
			fl = sl.flight
			waitCh, reason = fl.done, fmt.Sprintf("Model '%s' is still loading", id)
		case sl.closing != nil: // 同一个模型正在卸载：等它关完再加载，不同时装两份
			waitCh, reason = p.changed, fmt.Sprintf("Model '%s' is still being unloaded", id)
		case !haveEst:
			p.mu.Unlock()
			var err error
			if est, err = p.estimate(sl.spec); err != nil {
				p.setProblem(sl, err.Error())
				return nil, &Error{Status: 500, Detail: fmt.Sprintf("Model '%s' failed to load: %s", id, err), Err: err}
			}
			haveEst = true
			continue
		default:
			sl.est = est
			act, victim, why := p.roomLocked(sl, est)
			switch act {
			case roomFail:
				p.mu.Unlock()
				return nil, newError(503, "%s", why)
			case roomEvict:
				vm := victim.model
				victim.model, victim.closing = nil, vm
				p.mu.Unlock()
				p.closeModel(victim, vm, "UNLOADED")
				continue
			case roomWait:
				waitCh, reason = p.changed, why
			case roomOK:
				fl = &flight{done: make(chan struct{}), reserve: est}
				sl.flight = fl
				p.loads.Add(1)
				p.mu.Unlock()
				go p.runLoad(sl, fl)
				return p.awaitLeader(ctx, sl, fl, deadline())
			}
		}
		p.mu.Unlock()

		dl := deadline() // 先建定时器再记"在等"：测试看到"在等"就可以放心拨时钟
		p.waiting.Add(1)
		select {
		case <-waitCh:
		case <-dl:
			p.waiting.Add(-1)
			return nil, newError(503, "%s", reason)
		case <-ctx.Done():
			p.waiting.Add(-1)
			return nil, ctx.Err()
		}
		p.waiting.Add(-1)
		if fl != nil && fl.err != nil { // 同一次加载失败了：大家拿到同一个错误，不各自再试一遍
			return nil, fl.err
		}
	}
}

func (p *Pool) setProblem(sl *slot, msg string) {
	p.mu.Lock()
	sl.problem = msg
	p.mu.Unlock()
}

type roomAction int

const (
	roomOK    roomAction = iota // 位置够，可以开始加载
	roomEvict                   // 先卸掉返回的那一个，再重新判断
	roomWait                    // 现在腾不出，等有变动再试
	roomFail                    // 永远装不下，立刻报错
)

// roomLocked 判断能不能给 sl 腾出位置（server.py 的 _make_room，改成一步一返回，卸载由调用方在锁外做）。必须持 mu。
func (p *Pool) roomLocked(sl *slot, est uint64) (roomAction, *slot, string) {
	if est > p.maxBytes { // 先判，免得白卸一堆别的
		return roomFail, nil, fmt.Sprintf("Model '%s' alone needs about %d MiB, over the %d MiB resident budget",
			sl.spec.ID, est/mib, p.maxBytes/mib)
	}
	count, bytes := p.usageLocked()
	overCount := count+1 > p.maxLoaded
	overBytes := bytes+est > p.maxBytes
	if !overCount && !overBytes {
		return roomOK, nil, ""
	}
	var victim *slot
	for _, id := range p.order {
		o := p.slots[id]
		if o.model == nil || o.model.refs > 0 {
			continue
		}
		if victim == nil || o.model.lastUsed.Before(victim.model.lastUsed) {
			victim = o
		}
	}
	if victim != nil {
		return roomEvict, victim, ""
	}
	if overCount {
		return roomWait, nil, "Every loaded model is in use; waiting for one to be released"
	}
	return roomWait, nil, fmt.Sprintf("Loading '%s' would exceed the %d MiB resident budget; waiting for a model to be unloaded",
		sl.spec.ID, p.maxBytes/mib)
}

// usageLocked 是占着名额和字节的模型数与字节数：已加载的、加载中的（按估计预留）、卸载中的（Close 还没返回）。必须持 mu。
func (p *Pool) usageLocked() (count int, bytes uint64) {
	for _, sl := range p.slots {
		if sl.model != nil {
			count++
			bytes += sl.model.charge
		}
		if sl.flight != nil {
			count++
			bytes += sl.flight.reserve
		}
		if sl.closing != nil {
			count++
			bytes += sl.closing.charge
		}
	}
	return count, bytes
}

// awaitLeader 等自己发起的那次加载。装好时池已经替它占了一个引用（flight.handed），认领就是它的租约；
// 它不等了（ctx 取消、到截止时间）要把这个引用还回去，免得没人用的模型一直钉在池里。
func (p *Pool) awaitLeader(ctx context.Context, sl *slot, fl *flight, deadline <-chan time.Time) (*Lease, error) {
	p.waiting.Add(1)
	select {
	case <-fl.done:
		p.waiting.Add(-1)
	case <-deadline:
		p.waiting.Add(-1)
		p.abandon(fl)
		return nil, newError(503, "Model '%s' is still loading", sl.spec.ID)
	case <-ctx.Done():
		p.waiting.Add(-1)
		p.abandon(fl)
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if fl.err != nil {
		return nil, fl.err
	}
	if !fl.handed { // 不会发生：leader 没走就一定替它占了引用
		return nil, newError(500, "Model '%s' loaded but could not be handed over", sl.spec.ID)
	}
	fl.handed = false
	return &Lease{p: p, sl: sl, m: fl.model}, nil
}

func (p *Pool) abandon(fl *flight) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !fl.finished {
		fl.leaderGone = true
		return
	}
	if fl.handed {
		fl.handed = false
		fl.model.refs--
		fl.model.lastUsed = p.clock.Now()
		p.notifyLocked()
	}
}

// runLoad 在自己的 goroutine 里做一次加载：不持池锁，不看任何请求的 ctx（只看池的）。
func (p *Pool) runLoad(sl *slot, fl *flight) {
	defer p.loads.Done()
	select { // 一次只加载一个模型
	case p.loadTurn <- struct{}{}:
	case <-p.ctx.Done():
		p.finishLoad(sl, fl, Loaded{}, newError(503, "The model pool is shutting down"), 0, 0)
		return
	}
	before, errB := p.rss()
	t0 := p.clock.Now()
	ld, err := p.callLoad(sl.spec)
	loadMs := p.clock.Now().Sub(t0).Milliseconds()
	// 加载过程（读文件、算哈希、建词表树）留下一堆垃圾，Go 的后台回收要等好一阵才还给系统：先还掉再量，
	// 量到的才是模型本身的常驻内存（真模型实测：gte 加载后进程常驻 479 MiB，这一步之后 401 MiB）。
	debug.FreeOSMemory()
	after, errA := p.rss()
	<-p.loadTurn

	var failure *Error
	if err != nil {
		failure = &Error{Status: 500, Detail: fmt.Sprintf("Model '%s' failed to load: %s", sl.spec.ID, err), Err: err}
	} else if (sl.spec.Kind == engine.KindEmbed && ld.Embedder == nil) || (sl.spec.Kind == engine.KindRerank && ld.Reranker == nil) {
		closeQuiet(ld.Close) // 种类对不上的半成品不留
		failure = newError(500, "Model '%s' failed to load: the loader returned no %s model", sl.spec.ID, sl.spec.Kind)
	}
	var measured uint64
	if errB == nil && errA == nil && after > before {
		measured = after - before
	}
	p.finishLoad(sl, fl, ld, failure, loadMs, measured)
}

// callLoad 调加载函数并接住它的 panic：不接的话等同一次加载的人会永远等下去。
func (p *Pool) callLoad(spec engine.Spec) (ld Loaded, err error) {
	defer func() {
		if r := recover(); r != nil {
			ld, err = Loaded{}, fmt.Errorf("加载时内部错误（%T）", r)
		}
	}()
	return p.load(p.ctx, spec)
}

// finishLoad 把一次加载的结果记进账：成功就装进 slot（替没走的 leader 占一个引用），失败就清掉预留、记下原因。
// 成功时 LOADED 那一行先写出去、再让模型对外可见：看到日志里有这一行的人，不会先看到别的请求已经在用它；
// 写日志的这一小段里，slot 仍是"加载中"，要用它的请求照旧等同一个 done，不会再去加载第二份。
func (p *Pool) finishLoad(sl *slot, fl *flight, ld Loaded, failure *Error, loadMs int64, measured uint64) {
	spec := sl.spec
	if failure == nil {
		dim := "-"
		if spec.Dimension > 0 {
			dim = fmt.Sprint(spec.Dimension)
		}
		p.logf("LOADED model=%s kind=%s dimension=%s load_ms=%d hash_ms=%d estimated_mb=%d rss_delta_mb=%d",
			spec.ID, spec.Kind, dim, loadMs, ld.HashMs, fl.reserve/mib, measured/mib)
		if measured > 0 && measured*warnDen > fl.reserve*warnNum {
			p.logf("WARN model=%s 加载后进程常驻内存涨了 %d MiB，超过估计 %d MiB 的 1.5 倍，估计偏低，记账改按实测算",
				spec.ID, measured/mib, fl.reserve/mib)
		}
	}

	var discard bool
	p.mu.Lock()
	sl.flight = nil
	switch {
	case failure != nil:
		if !p.closed { // 池关了以后的失败是关出来的，不算这个模型的问题
			sl.problem = failure.Detail
		}
		fl.err = failure
	case p.closed: // 加载期间池关了：装好的这份直接关掉，不登记，也不留半成品
		discard = true
		fl.err = newError(503, "The model pool is shutting down")
	default:
		m := &model{
			loaded:   ld,
			lastUsed: p.clock.Now(),
			loadMs:   loadMs,
			hashMs:   ld.HashMs,
			estimate: fl.reserve,
			measured: measured,
			charge:   max(fl.reserve, measured),
		}
		if !fl.leaderGone {
			m.refs = 1
			fl.handed = true
		}
		sl.model = m
		sl.problem = ""
		fl.model = m
	}
	fl.finished = true
	close(fl.done)
	p.notifyLocked()
	p.mu.Unlock()

	if discard {
		closeQuiet(ld.Close)
		p.logf("UNLOADED model=%s", spec.ID)
	}
}

// ---- 卸载 ----

// closeModel 在池锁外关掉一个已经从 slot.model 摘到 slot.closing 的模型，关完才把名额还回去。
func (p *Pool) closeModel(sl *slot, m *model, tag string) {
	err := safeClose(m.loaded.Close)
	if err != nil {
		p.logf("WARN model=%s 释放会话时出错：%v", sl.spec.ID, err)
	}
	p.logf("%s model=%s", tag, sl.spec.ID) // 先写日志再还名额：同一个模型的下一次 LOADED 一定排在这一行后面
	p.mu.Lock()
	sl.closing = nil
	p.notifyLocked()
	p.mu.Unlock()
	// 卸载是少见的事：立刻做一次 GC，把空闲的堆还给系统。不做的话 Go 的后台回收要两分钟以后才还，进程常驻内存
	// 在这段时间里比卸载前高一大截（真模型实测：两个模型装着 1061 MiB，卸掉以后 162 MiB 停了两分钟才落到 105，
	// 做了这一步立刻是 43，刚就绪时是 32）。放在放开池锁、还完名额之后，不拖住等着的人。
	debug.FreeOSMemory()
}

func safeClose(fn func() error) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("释放时内部错误（%T）", r)
		}
	}()
	return fn()
}

func closeQuiet(fn func() error) { _ = safeClose(fn) }

// Unload 卸掉一个模型：有租约在用就不卸（"in_use"），没加载就是"not_loaded"，不认识的 id 是 "unknown"。
func (p *Pool) Unload(id string) (bool, string) {
	sl, ok := p.slots[id]
	if !ok {
		return false, "unknown"
	}
	p.mu.Lock()
	m := sl.model
	switch {
	case m == nil:
		p.mu.Unlock()
		return false, "not_loaded"
	case m.refs > 0:
		p.mu.Unlock()
		return false, "in_use"
	}
	sl.model, sl.closing = nil, m
	p.mu.Unlock()
	p.closeModel(sl, m, "UNLOADED")
	return true, "unloaded"
}

// reap 每 ReapInterval 查一次空闲的模型；Close 时退出。
func (p *Pool) reap() {
	defer close(p.reaperDone)
	for {
		t := p.clock.NewTimer(p.reapInterval)
		select {
		case <-t.C():
			p.reapOnce()
		case <-p.stop:
			t.Stop()
			return
		}
	}
}

// reapOnce 卸掉所有没有租约、空闲超过 Idle 的模型。判断和摘下是同一个临界区：和 Acquire 抢，要么 Acquire 先
// 占到引用（这里就看到 refs>0 不卸），要么这里先摘下（Acquire 看到没加载，等关完再重新加载）。
func (p *Pool) reapOnce() {
	if p.idle <= 0 {
		return
	}
	type victim struct {
		sl *slot
		m  *model
	}
	var vs []victim
	p.mu.Lock()
	now := p.clock.Now()
	for _, id := range p.order {
		sl := p.slots[id]
		if m := sl.model; m != nil && m.refs == 0 && now.Sub(m.lastUsed) > p.idle {
			sl.model, sl.closing = nil, m
			vs = append(vs, victim{sl, m})
		}
	}
	p.mu.Unlock()
	for _, v := range vs {
		p.closeModel(v.sl, v.m, "UNLOADED")
	}
}

// Close 先停空闲卸载，等进行中的加载做完（做完的丢掉不登记），再卸掉全部；仍被租用的最多等 CloseWait，到点强制关
// （进程要退出了；Session.Close 自己会等正在跑的那一行 Run 结束）。可以重复调用。
func (p *Pool) Close() { p.closeOnce.Do(p.doClose) }

func (p *Pool) doClose() {
	p.mu.Lock()
	p.closed = true
	p.notifyLocked()
	p.mu.Unlock()
	p.cancel()
	close(p.stop)
	if p.reaperDone != nil {
		<-p.reaperDone
	}
	p.loads.Wait()

	timer := p.clock.NewTimer(p.closeWait)
	defer timer.Stop()
	type victim struct {
		sl *slot
		m  *model
	}
	for {
		var (
			vs   []victim
			busy int
		)
		p.mu.Lock()
		for _, id := range p.order {
			sl := p.slots[id]
			mine := false
			if m := sl.model; m != nil {
				if m.refs == 0 {
					sl.model, sl.closing = nil, m
					vs = append(vs, victim{sl, m})
					mine = true
				} else {
					busy++
				}
			}
			if sl.closing != nil && !mine {
				busy++ // 别处正在关的（Unload 做到一半），等它关完
			}
		}
		changed := p.changed
		p.mu.Unlock()
		for _, v := range vs {
			p.closeModel(v.sl, v.m, "UNLOADED")
		}
		if len(vs) > 0 {
			continue
		}
		if busy == 0 {
			return
		}
		select {
		case <-changed:
		case <-timer.C():
			p.forceClose()
			return
		}
	}
}

// forceClose 关掉还被租用的模型（Close 等不下去了）。
func (p *Pool) forceClose() {
	type victim struct {
		sl *slot
		m  *model
	}
	var vs []victim
	p.mu.Lock()
	for _, id := range p.order {
		sl := p.slots[id]
		if m := sl.model; m != nil {
			sl.model, sl.closing = nil, m
			vs = append(vs, victim{sl, m})
		}
	}
	p.mu.Unlock()
	for _, v := range vs {
		p.logf("WARN model=%s 关闭时租约还没放开，强制关闭", v.sl.spec.ID)
		p.closeModel(v.sl, v.m, "UNLOADED")
	}
}

// ---- 快照 ----

// ModelState 是一个模型在 Snapshot 里的样子。
type ModelState struct {
	ID         string
	Kind       engine.Kind
	Dimension  int
	MaxTokens  int
	Matryoshka bool

	Ready   bool   // 没有已知的问题
	Problem string // Ready=false 时的原因，只有相对路径、不带绝对路径和输入文字

	Loaded        bool
	Loading       bool
	Unloading     bool
	Refs          int
	LoadMs        int64 // 已加载时才有
	HashMs        int64
	LastUsedMsAgo int64 // 已加载时才有

	EstimatedBytes uint64 // 最近一次加载前的估计，没估计过是 0
	MeasuredBytes  uint64 // 加载前后进程常驻内存的差，没量到是 0
	ChargedBytes   uint64 // 记账里它占的：已加载 = max(估计, 实测)，加载中 = 估计，卸载中 = 它加载时记的
}

// Snapshot 是池某一刻的拷贝，给 /health 和 /v1/models 用。
type Snapshot struct {
	Models        []ModelState // 按 id 排序
	LoadedCount   int
	MaxLoaded     int
	MaxBytes      uint64
	ResidentBytes uint64 // 记账里所有占着字节的（已加载 + 加载中 + 卸载中）之和，是估计，不是实测的进程内存
}

// Snapshot 只持锁拷一份记账，不等加载、不等 Run，/health 靠它在 3 秒的探测超时里一定答得出来。
func (p *Pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	s := Snapshot{MaxLoaded: p.maxLoaded, MaxBytes: p.maxBytes}
	_, s.ResidentBytes = p.usageLocked()
	for _, id := range p.order {
		sl := p.slots[id]
		st := ModelState{
			ID: id, Kind: sl.spec.Kind, Dimension: sl.spec.Dimension, MaxTokens: sl.spec.MaxTokens, Matryoshka: sl.spec.Matryoshka,
			Ready: sl.problem == "", Problem: sl.problem,
			Loading: sl.flight != nil, Unloading: sl.closing != nil, EstimatedBytes: sl.est,
		}
		switch {
		case sl.model != nil:
			m := sl.model
			s.LoadedCount++
			st.Loaded, st.Refs = true, m.refs
			st.LoadMs, st.HashMs = m.loadMs, m.hashMs
			st.LastUsedMsAgo = now.Sub(m.lastUsed).Milliseconds()
			st.EstimatedBytes, st.MeasuredBytes, st.ChargedBytes = m.estimate, m.measured, m.charge
		case sl.flight != nil:
			st.ChargedBytes = sl.flight.reserve
		case sl.closing != nil:
			st.ChargedBytes = sl.closing.charge
		}
		s.Models = append(s.Models, st)
	}
	return s
}
