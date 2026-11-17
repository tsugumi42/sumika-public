package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/config"
)

// Row 是日志（proactive_decisions）的一行
type Row struct {
	ID        int64
	At        time.Time
	Character string
	Kind      Kind
	Reason    string
	Inputs    json.RawMessage // Inputs 的 JSON，可为空
	NextAt    time.Time
	Thread    string
	ModelNote string // 模型自己写的一句"为什么不开口"，限 80 字，只本机显示；导演自己的错误也写在这里
	Count     int    // 这一行代表几次（同样的错误合并计数），至少 1
}

// Store 是导演要存取的东西。DBStore 是 trace.db 的实现，MemStore 给测试
type Store interface {
	LoadState(character string) (State, bool, error)
	SaveState(State) error
	AddRow(Row) (int64, error)
	// AddCount 给已有的一行加 n 次（合并同样的错误）
	AddCount(id int64, n int) error
	// Annotate 把一次醒的结局补到放行时写下的那一行（空串不改）
	Annotate(id int64, reason, thread, note string) error
	// HasRowSince 这个角色有没有 at >= since 的日志行
	HasRowSince(character string, since time.Time) (bool, error)
	// Prune 删掉 before 以前的日志行，返回删了几行
	Prune(before time.Time) (int, error)
}

// WakeRequest 是放行以后交给"叫模型"那一步的东西（第 2 步实现）
type WakeRequest struct {
	Character string
	Kind      Kind // wake 或 followup
	Reason    string
	At        time.Time
	Inputs    Inputs
	RowID     int64 // 这次放行已经写下的日志行，模型的结果回来后宿主往同一行补
}

// WakeResult 是一次醒的结局。Reason 空按 due 记；NextAt 零值按系数算的间隔排
type WakeResult struct {
	Reason    string
	Thread    string
	ModelNote string
	NextAt    time.Time
}

// Waker 把一次放行变成线里的一轮模型调用（第 2 步）。第 1 步不接：Options.Waker 为空，到点只写日志"本该醒"。
// 可以耗时：调度循环在它返回前不会再检查别的到点，所以第 2 步的实现应当只起线、立刻返回，结局靠 RowID 回头补。
type Waker interface {
	Wake(ctx context.Context, req WakeRequest) (WakeResult, error)
}

// Options 是宿主接线时注入的东西。Store、Config、Signals 必填
type Options struct {
	Store Store
	// Config 每次轮询取一遍（配置热更新）。Enabled 为假或没列角色就什么都不做、不写日志
	Config func() config.Proactive
	// Signals 取一个角色这一刻的信号。失败记一行 error，顺延 deferMinutes，同样的错误只记两次
	Signals func(ctx context.Context, character string, now time.Time) (Snapshot, error)
	Waker   Waker
	// Housekeeping 每次轮询（Tick）开头调一遍，不受各角色 nextWake 限制：线的超时收尾、认领宽限到点自动交回之类
	// 靠"现在几点"推出的事放这里（第 2 步）。可以为空
	Housekeeping func(ctx context.Context, now time.Time)
	Now          func() time.Time
	Rand         Rand
	// Poll 是检查到点的间隔，默认 30 秒
	Poll time.Duration
	Logf func(format string, args ...any)
	// Warnf：读状态失败、模型出错、清旧日志失败（这一轮跳过，下轮再来）；Errorf：存状态、写日志失败。nil 时用 Logf
	Warnf, Errorf func(format string, args ...any)
}

// Director 管各角色的下次醒来时间并写日志
type Director struct {
	opt Options

	mu     sync.Mutex
	errs   map[string]*errRun // 角色 → 最近连着的同一个错误
	pruned time.Time
	// stMu 管着"读状态 → 改 → 存状态"：调度壳的 tickOne、线关闭时的系数更新（ThreadClosed）、主人说别烦（SetQuiet）、
	// 重排下次（SetNextWake）都在它里面做整行读改写，不会互相冲掉。Waker.Wake 是在 tickOne 持有它时被调的，
	// 所以 Wake 里不能再调这几个方法
	stMu sync.Mutex
}

// errRun 记一个角色最近连续的同一个错误：前两次各写一行，之后合并到第二行的计数上
type errRun struct {
	key   string
	n     int
	rowID int64
}

// New 建导演。不读库，也不排时间：第一次轮询才排
func New(opt Options) (*Director, error) {
	if opt.Store == nil || opt.Config == nil || opt.Signals == nil {
		return nil, errors.New("proactive：Store、Config、Signals 必须提供")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Poll <= 0 {
		opt.Poll = 30 * time.Second
	}
	return &Director{opt: opt, errs: map[string]*errRun{}}, nil
}

func (d *Director) logf(f string, a ...any) {
	if d.opt.Logf != nil {
		d.opt.Logf(f, a...)
	}
}

func (d *Director) warnf(f string, a ...any) {
	if d.opt.Warnf != nil {
		d.opt.Warnf(f, a...)
		return
	}
	d.logf(f, a...)
}

func (d *Director) errorf(f string, a ...any) {
	if d.opt.Errorf != nil {
		d.opt.Errorf(f, a...)
		return
	}
	d.logf(f, a...)
}

// Run 先补记没醒到的，再每个 Poll 检查一次到点，直到 ctx 取消
func (d *Director) Run(ctx context.Context) {
	d.Catchup(ctx, d.opt.Now())
	t := time.NewTicker(d.opt.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.Tick(ctx, d.opt.Now())
		}
	}
}

func (d *Director) active() (config.Proactive, bool) {
	p := d.opt.Config()
	return p, p.Enabled && len(p.Characters) > 0
}

// Catchup 启动补记（规格第七节"日志"）：上一个 next_wake_at 已经过了、却没有那之后的日志行，说明到点时进程没开，
// 补一行 skip/missed-offline，不当场补跑，5 到 30 分钟后醒。对"夜间任务悄悄不跑"的回应。
func (d *Director) Catchup(ctx context.Context, now time.Time) {
	p, ok := d.active()
	if !ok {
		return
	}
	for _, c := range p.Characters {
		if ctx.Err() != nil {
			return
		}
		st, found, err := d.opt.Store.LoadState(c)
		if err != nil {
			d.warnf("主动联系：读 %s 的状态失败：%v", c, err)
			continue
		}
		if !found || st.NextWake.IsZero() || st.NextWake.After(now) {
			continue
		}
		seen, err := d.opt.Store.HasRowSince(c, st.NextWake)
		if err != nil {
			d.warnf("主动联系：查 %s 的日志失败：%v", c, err)
			continue
		}
		if seen {
			// 日志里有这次之后的行：到点时进程是开着的（或已经补过），状态只是没来得及往后排，不记"没醒到"
			continue
		}
		in := Inputs{SinceContactMin: -1, Factor: st.Factor, ColdStreak: st.ColdStreak, MissedMin: agoMin(st.NextWake, now),
			OwnerLastActiveAgoMin: -1, LocalLastInputAgoMin: -1, InSleep: InSleep(p, now)}
		// 不从头再排一整段：程序常要重启（换 exe），每次都重排就几乎醒不了。过一小会儿就醒，到点照常过睡觉和正在聊的判断
		next := now.Add(catchupDelay(d.opt.Rand))
		d.addRow(c, Row{At: now, Kind: KindSkip, Reason: ReasonMissedOffline, Inputs: marshal(in), NextAt: next})
		st.NextWake = next
		d.save(st)
	}
	d.prune(now)
}

// Tick 检查一遍：每个到点的角色走 Decide，写日志、重排下次。没开或没列角色什么都不做。
// 能被测试直接调；Run 只是定时调它
func (d *Director) Tick(ctx context.Context, now time.Time) {
	p, ok := d.active()
	if !ok {
		return
	}
	if d.opt.Housekeeping != nil {
		d.opt.Housekeeping(ctx, now)
	}
	for _, c := range p.Characters {
		if ctx.Err() != nil {
			return
		}
		d.tickOne(ctx, p, c, now)
	}
	d.prune(now)
}

func (d *Director) tickOne(ctx context.Context, p config.Proactive, c string, now time.Time) {
	d.stMu.Lock()
	defer d.stMu.Unlock()
	st, found, err := d.opt.Store.LoadState(c)
	if err != nil {
		d.warnf("主动联系：读 %s 的状态失败：%v", c, err)
		return
	}
	if !found || st.Factor <= 0 {
		// 第一次见到这个角色：从现在起排第一次，不立刻醒
		st.Character, st.Factor = c, 1
	}
	if !found || st.NextWake.IsZero() {
		st.NextWake = now.Add(Interval(p, st.Factor, d.opt.Rand))
		d.save(st)
		return
	}
	if st.NextWake.After(now) {
		return
	}

	sg, err := d.opt.Signals(ctx, c, now)
	if err != nil {
		next := now.Add(minutes(p.DeferMinutes))
		d.addError(c, Row{At: now, Kind: KindSkip, Reason: ReasonError, Inputs: marshal(SnapshotInputs(p, st, Snapshot{}, now)), NextAt: next}, "取信号失败："+err.Error())
		st.NextWake = next
		d.save(st)
		return
	}
	dec := Decide(p, st, sg, now, d.opt.Rand)
	in := SnapshotInputs(p, st, sg, now)
	row := Row{At: now, Kind: dec.Kind, Reason: dec.Reason, Inputs: marshal(in), NextAt: dec.NextAt}
	if !dec.Passes() {
		d.addRow(c, row)
		st.NextWake = dec.NextAt
		d.save(st)
		return
	}

	// 放行。第 1 步没有 Waker：只写"本该醒"，不叫模型、不建线
	st.LastWake = now
	if d.opt.Waker == nil {
		d.addRow(c, row)
		st.NextWake = dec.NextAt
		d.save(st)
		return
	}
	id := d.addRow(c, row)
	res, err := d.opt.Waker.Wake(ctx, WakeRequest{Character: c, Kind: dec.Kind, Reason: dec.Reason, At: now, Inputs: in, RowID: id})
	next := dec.NextAt
	if err == nil && !res.NextAt.IsZero() {
		next = res.NextAt
	}
	if err != nil {
		d.addError(c, Row{At: now, Kind: dec.Kind, Reason: ReasonError, Inputs: row.Inputs, NextAt: next}, "叫醒失败："+err.Error())
	} else if id != 0 && (res.Reason != "" || res.Thread != "" || res.ModelNote != "") {
		if err := d.opt.Store.Annotate(id, res.Reason, res.Thread, clip(res.ModelNote, 80)); err != nil {
			d.errorf("主动联系：补日志失败：%v", err)
		}
	}
	st.NextWake = next
	d.save(st)
}

// modify 在 stMu 里对一个角色的状态做整行读改写；从没见过这个角色的从系数 1 起
func (d *Director) modify(c string, fn func(State) State) error {
	d.stMu.Lock()
	defer d.stMu.Unlock()
	st, _, err := d.opt.Store.LoadState(c)
	if err != nil {
		return err
	}
	if st.Character == "" {
		st.Character = c
	}
	if st.Factor <= 0 {
		st.Factor = 1
	}
	return d.opt.Store.SaveState(fn(st))
}

// ThreadClosed 一条线关闭了：按热度更新系数和连冷次数（OnClose），下次从现在起按新系数重排（线开着时下次醒是续醒的检查时间，
// 线一关就该回到正常的间隔）。别烦不走这里，走 SetQuiet
func (d *Director) ThreadClosed(c string, k CloseKind, now time.Time) error {
	p := d.opt.Config()
	return d.modify(c, func(st State) State {
		st = OnClose(p, st, k)
		st.NextWake = now.Add(Interval(p, st.Factor, d.opt.Rand))
		return st
	})
}

// SetQuiet 主人说别烦：到 until 前不找，系数不动；下次醒排到 until 之后（Decide 到点时再看 quiet_until）
func (d *Director) SetQuiet(c string, until, now time.Time) error {
	return d.modify(c, func(st State) State {
		st = Snooze(st, until)
		if st.NextWake.IsZero() || st.NextWake.Before(until) {
			st.NextWake = until
		}
		return st
	})
}

// QuietUntil 是主人说别烦到什么时候；零值是没说过。提醒的调度器问它（线里建的提醒到点要遵守）
func (d *Director) QuietUntil(c string) time.Time {
	d.stMu.Lock()
	defer d.stMu.Unlock()
	st, _, err := d.opt.Store.LoadState(c)
	if err != nil {
		d.warnf("主动联系：读别烦状态失败：%v", err)
		return time.Time{}
	}
	return st.QuietUntil
}

// SetNextWake 线的状态变了，把下次醒（续醒或截止的检查）排到 at。只在这个角色已经排过期时才改
func (d *Director) SetNextWake(c string, at time.Time) error {
	return d.modify(c, func(st State) State {
		st.NextWake = at
		return st
	})
}

// Log 写一行日志（收线、被拦等），同时清掉这个角色连续错误的记录；返回行号，写失败返回 0
func (d *Director) Log(c string, r Row) int64 { return d.addRow(c, r) }

func (d *Director) save(st State) {
	if err := d.opt.Store.SaveState(st); err != nil {
		d.errorf("主动联系：存 %s 的状态失败：%v", st.Character, err)
	}
}

// addRow 写一行日志并清掉这个角色连续错误的记录（来了别的结果，错误就不算"连着"了）
func (d *Director) addRow(c string, r Row) int64 {
	r.Character = c
	if r.Count < 1 {
		r.Count = 1
	}
	id, err := d.opt.Store.AddRow(r)
	if err != nil {
		d.errorf("主动联系：写日志失败：%v", err)
		return 0
	}
	d.mu.Lock()
	delete(d.errs, c)
	d.mu.Unlock()
	return id
}

// addError 写一行错误日志；同一个角色连着发生同样的错误，前两次各一行，之后只给第二行的计数加一
func (d *Director) addError(c string, r Row, msg string) {
	r.Character = c
	r.ModelNote = clip(msg, 80)
	key := r.Kind.string() + "|" + r.Reason + "|" + r.ModelNote
	d.mu.Lock()
	run := d.errs[c]
	if run != nil && run.key == key && run.n >= 2 && run.rowID != 0 {
		run.n++
		id := run.rowID
		d.mu.Unlock()
		if err := d.opt.Store.AddCount(id, 1); err != nil {
			d.errorf("主动联系：合并重复错误失败：%v", err)
		}
		return
	}
	d.mu.Unlock()
	if r.Count < 1 {
		r.Count = 1
	}
	id, err := d.opt.Store.AddRow(r)
	if err != nil {
		d.errorf("主动联系：写日志失败：%v", err)
		return
	}
	d.mu.Lock()
	if run != nil && run.key == key {
		run.n++
		run.rowID = id
	} else {
		d.errs[c] = &errRun{key: key, n: 1, rowID: id}
	}
	d.mu.Unlock()
	d.warnf("主动联系：%s", fmt.Sprintf("%s 出错：%s", c, r.ModelNote))
}

func (k Kind) string() string { return string(k) }

// prune 每天最多清一次超过 decisionLogDays 的日志
func (d *Director) prune(now time.Time) {
	p := d.opt.Config()
	d.mu.Lock()
	if !d.pruned.IsZero() && now.Sub(d.pruned) < 24*time.Hour {
		d.mu.Unlock()
		return
	}
	d.pruned = now
	d.mu.Unlock()
	if p.DecisionLogDays <= 0 {
		return
	}
	if _, err := d.opt.Store.Prune(now.Add(-time.Duration(p.DecisionLogDays) * 24 * time.Hour)); err != nil {
		d.warnf("主动联系：清旧日志失败：%v", err)
	}
}

func marshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// clip 按字数截断（不拆汉字），并去掉换行
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// catchupDelay 是错过一次之后再过多久醒：5 到 30 分钟。
func catchupDelay(r Rand) time.Duration {
	x := 0.5
	if r != nil {
		x = r()
	}
	return 5*time.Minute + time.Duration(x*float64(25*time.Minute))
}
