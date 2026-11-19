package remind

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits 是防跑飞的上限（规格第七节）。她建、改时才看；主人在界面上不受这些管
type Limits struct {
	MaxActive       int           // 每个角色同时存在的条数
	MinInterval     time.Duration // 周期下限；线里建的一次性至少这么久以后才响
	ThreadMaxActive int           // 线里建的、没结束的最多几条
	AllowQQ         bool          // 能不能发到主人 QQ 私聊
}

// Actor 是谁在动提醒
type Actor struct {
	Source    Source
	Character string // 她的话是她自己；主人在界面上由界面给
}

// RingRequest 是交给宿主的“这条提醒到点了”
type RingRequest struct {
	Reminder  Reminder // 响之前的快照
	Scheduled time.Time
	Missed    int                      // 除了这一次，之前还错过几次
	Resumed   bool                     // 这一次上回没收尾（程序退出或投递失败）：先看已经送到了哪
	Prior     map[string]DeliveryState // 上回记下的各目标结果
	Attempt   int                      // 这个进程里第几次重试，0 是第一次
}

// RingResult 是宿主响完的结果
type RingResult struct {
	// Aborted：程序在退出，这一次不算，下次启动接着响
	Aborted bool
	// Dropped：还没发出去就被取消、删除或暂停了
	Dropped bool
	// Retry：一个目标都没送到（比如写库出错）：过一会儿再试，不丢
	Retry bool
	// Note 写进记录：谁的话、晚了多久、各目标的结果
	Note string
}

// Options 是宿主接线时注入的东西。Store、Ring 必填
type Options struct {
	Store Store
	Ring  func(ctx context.Context, req RingRequest) RingResult
	// Limits 取当前的上限；nil 用默认值
	Limits func() Limits
	// QuietUntil 是这个角色主人说了“别烦”到什么时候（线里建的提醒到点要遵守）；nil 当没有
	QuietUntil func(character string) time.Time
	// Changed 在提醒变了以后调（锁外）：刷新界面
	Changed func()
	Now     func() time.Time
	// Poll 是 Run 循环检查到点的间隔，默认 10 秒；RetryAfter 是投递失败后多久再试，默认 1 分钟
	Poll, RetryAfter time.Duration
	Logf             func(format string, args ...any)
	// Warnf：这一次没送到、过会儿再试；Errorf：提醒表存不上、响的时候崩溃。nil 时用 Logf
	Warnf, Errorf func(format string, args ...any)
}

// Scheduler 管提醒表并到点发起响铃
type Scheduler struct {
	opt      Options
	mu       sync.Mutex
	st       State
	inflight map[string]int // 角色 → 正在响的编号（同一个角色同一时间只响一条）
	attempts map[int]int    // 编号 → 这个进程里失败了几次
	retryAt  map[int]time.Time
	lastSeen time.Time // 上次清旧数据的时间
	wg       sync.WaitGroup
}

// New 读回提醒表。上回没收尾的留在 Pending，下次 Tick 接着响；正在发 QQ 的记成不明，不重发
func New(opt Options) (*Scheduler, error) {
	if opt.Store == nil || opt.Ring == nil {
		return nil, errors.New("remind：Store 和 Ring 必须提供")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Poll <= 0 {
		opt.Poll = 10 * time.Second
	}
	if opt.RetryAfter <= 0 {
		opt.RetryAfter = time.Minute
	}
	if opt.Limits == nil {
		opt.Limits = func() Limits {
			return Limits{MaxActive: DefaultMaxActive, MinInterval: DefaultMinIntervalMinutes * time.Minute, ThreadMaxActive: DefaultThreadMaxActive}
		}
	}
	st, err := opt.Store.Load()
	if err != nil {
		return nil, err
	}
	if st.NextID < 1 {
		st.NextID = 1
	}
	s := &Scheduler{opt: opt, st: st, inflight: map[string]int{}, attempts: map[int]int{}, retryAt: map[int]time.Time{}}
	changed := s.prune(opt.Now())
	for i := range s.st.Reminders {
		r := &s.st.Reminders[i]
		if r.Pending == nil {
			continue
		}
		if r.Status != StatusActive {
			r.Pending, changed = nil, true
			continue
		}
		for t, d := range r.Pending.Deliveries {
			if d == DeliverySending {
				r.Pending.Deliveries[t] = DeliveryUnknown
				changed = true
			}
		}
	}
	if changed {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Scheduler) logf(f string, a ...any) {
	if s.opt.Logf != nil {
		s.opt.Logf(f, a...)
	}
}

func (s *Scheduler) warnf(f string, a ...any) {
	if s.opt.Warnf != nil {
		s.opt.Warnf(f, a...)
		return
	}
	s.logf(f, a...)
}

func (s *Scheduler) errorf(f string, a ...any) {
	if s.opt.Errorf != nil {
		s.opt.Errorf(f, a...)
		return
	}
	s.logf(f, a...)
}

// save 要在持锁时调用
func (s *Scheduler) save() error {
	err := s.opt.Store.Save(s.st)
	if err != nil {
		s.errorf("提醒表保存失败：%v", err)
	}
	return err
}

func (s *Scheduler) changed() {
	if s.opt.Changed != nil {
		s.opt.Changed()
	}
}

// prune 清掉结束很久的；要在持锁时（或还没开始用时）调用
func (s *Scheduler) prune(now time.Time) bool {
	s.lastSeen = now
	n := len(s.st.Reminders)
	s.st.Reminders = slices.DeleteFunc(s.st.Reminders, func(r Reminder) bool {
		return r.Status.Ended() && !r.EndedAt.IsZero() && now.Sub(r.EndedAt) > endedKeep
	})
	return len(s.st.Reminders) != n
}

// ───────────── 建、看、改、结束、删 ─────────────

// Draft 是新建的内容
type Draft struct {
	Text    string
	At      time.Time
	Repeat  Repeat
	Targets []string
	Conv    string // 本地那一路写到哪；空是 console:<角色>
}

func checkText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("提醒内容是空的：text 写要提醒什么。")
	}
	if n := utf8.RuneCountInString(text); n > TextMax {
		return "", fmt.Errorf("提醒内容太长了（%d 个字），最多 %d 个字，写要点就行。", n, TextMax)
	}
	if strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return "", errors.New("提醒内容里有不能用的控制字符。")
	}
	return text, nil
}

func checkTargets(ts []string, lim Limits) ([]string, error) {
	var out []string
	for _, t := range ts {
		switch t {
		case TargetLocal:
		case TargetQQ:
			if !lim.AllowQQ {
				return nil, errors.New("QQ 投递所有者还没开（reminders.qq 是关着的）。不写 targets 就发到本地窗口；要发 QQ 请他在配置里打开。")
			}
		default:
			return nil, fmt.Errorf("targets 只能写 local（本地窗口）、qq（QQ 私聊），收到 %q", clip(t, 20))
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// checkRepeat 校验周期并按来源查下限
func checkRepeat(rep Repeat, actor Actor, lim Limits) (Repeat, error) {
	rep, err := rep.normalize()
	if err != nil {
		return rep, err
	}
	if rep.None() {
		return rep, nil
	}
	if actor.Source == SourceThread {
		return rep, errors.New("线里只能建一次性的提醒。周期提醒要所有者开口。")
	}
	floor := lim.MinInterval
	if actor.Source == SourceOwner {
		floor = OwnerMinInterval
	}
	if rep.Gap() < floor {
		return rep, fmt.Errorf("周期提醒最短每 %s一次（reminders.minIntervalMinutes）。这不是用来隔一会儿找他说话的；想找他聊走主动联系。", DurationText(floor))
	}
	return rep, nil
}

// threadLead 查线里建的一次性提醒够不够晚：线要找他走 thread_send，不能拿提醒马上开口
func threadLead(at, now time.Time, lim Limits) error {
	if at.Before(now.Add(lim.MinInterval)) {
		return fmt.Errorf("线里建的提醒最早定到 %s 以后才响（%s 以后）。现在想找他用 thread_send。", DurationText(lim.MinInterval), TimeText(now.Add(lim.MinInterval), now))
	}
	return nil
}

func activeCount(rs []Reminder, character string, source Source) int {
	n := 0
	for _, r := range rs {
		if r.Character == character && !r.Status.Ended() && (source == "" || r.Source == source) {
			n++
		}
	}
	return n
}

// Create 新建一条。一样的内容和下次时间已有没结束的，直接返回那一条，existed 为真（模型重试不会多出一条）
func (s *Scheduler) Create(actor Actor, d Draft) (Reminder, bool, error) {
	if actor.Character == "" {
		return Reminder{}, false, errors.New("缺少角色")
	}
	text, err := checkText(d.Text)
	if err != nil {
		return Reminder{}, false, err
	}
	now := s.opt.Now()
	lim := s.opt.Limits()
	rep, err := checkRepeat(d.Repeat, actor, lim)
	if err != nil {
		return Reminder{}, false, err
	}
	targets, err := checkTargets(d.Targets, lim)
	if err != nil {
		return Reminder{}, false, err
	}
	if err := checkFuture(d.At, now); err != nil {
		return Reminder{}, false, err
	}
	if actor.Source == SourceThread {
		if err := threadLead(d.At, now, lim); err != nil {
			return Reminder{}, false, err
		}
	}
	conv := d.Conv
	if conv == "" {
		conv = "console:" + actor.Character
	}
	next := first(rep, d.At.In(now.Location()))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.st.Reminders {
		if r.Character == actor.Character && !r.Status.Ended() && r.Text == text && r.NextAt.Equal(next) {
			return r.clone(), true, nil
		}
	}
	if actor.Source != SourceOwner {
		if n := activeCount(s.st.Reminders, actor.Character, ""); n >= lim.MaxActive {
			return Reminder{}, false, fmt.Errorf("已经有 %d 条没结束的提醒了（上限 reminders.maxActive）。先用 reminder_list 看看，结束或删掉不用的再建。", n)
		}
	}
	if actor.Source == SourceThread {
		if n := activeCount(s.st.Reminders, actor.Character, SourceThread); n >= lim.ThreadMaxActive {
			return Reminder{}, false, fmt.Errorf("线里最多留 %d 条提醒（reminders.threadMaxActive），已经有 %d 条了。不用的先结束。", lim.ThreadMaxActive, n)
		}
	}
	r := Reminder{ID: s.st.NextID, Character: actor.Character, Conv: conv, Text: text, At: d.At.In(now.Location()), Repeat: rep, NextAt: next,
		Source: actor.Source, Targets: targets, Status: StatusActive, Created: now}
	r.note(Entry{At: now, Kind: "create", By: actor.Source})
	s.st.NextID++
	s.st.Reminders = append(s.st.Reminders, r)
	if err := s.save(); err != nil {
		s.st.NextID--
		s.st.Reminders = s.st.Reminders[:len(s.st.Reminders)-1]
		return Reminder{}, false, fmt.Errorf("提醒没存上：%w", err)
	}
	out := r.clone()
	go s.changed()
	return out, false, nil
}

// List 列提醒，按下次时间；character 空列全部。结束的排后面
func (s *Scheduler) List(character string) []Reminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Reminder
	for _, r := range s.st.Reminders {
		if character == "" || r.Character == character {
			out = append(out, r.clone())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Status.Ended() != b.Status.Ended() {
			return !a.Status.Ended()
		}
		if a.Status.Ended() {
			return a.EndedAt.After(b.EndedAt)
		}
		if !a.NextAt.Equal(b.NextAt) {
			return a.NextAt.Before(b.NextAt)
		}
		return a.ID < b.ID
	})
	return out
}

// Get 按编号取一条
func (s *Scheduler) Get(id int) (Reminder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.find(id); r != nil {
		return r.clone(), true
	}
	return Reminder{}, false
}

// find 要在持锁时调用
func (s *Scheduler) find(id int) *Reminder {
	for i := range s.st.Reminders {
		if s.st.Reminders[i].ID == id {
			return &s.st.Reminders[i]
		}
	}
	return nil
}

// ErrNotFound 是没有这条提醒（别的角色的也是这句，不透露有没有）
var ErrNotFound = errors.New("没有这条提醒。用 reminder_list 看现有的编号。")

// reach 找到 actor 能动的那条：别的角色的一律当没有；线只能动线里建的。要在持锁时调用
func (s *Scheduler) reach(actor Actor, id int) (*Reminder, error) {
	r := s.find(id)
	if r == nil || r.Character != actor.Character {
		return nil, ErrNotFound
	}
	if actor.Source == SourceThread && r.Source != SourceThread {
		return nil, errors.New("这条是所有者那边建的，线里不能动，等他开口再说。")
	}
	return r, nil
}

// Patch 是修改：nil 的项不改
type Patch struct {
	Text    *string
	At      *time.Time
	Repeat  *Repeat
	Targets *[]string
	Paused  *bool
}

func (p Patch) empty() bool {
	return p.Text == nil && p.At == nil && p.Repeat == nil && p.Targets == nil && p.Paused == nil
}

// Update 修改一条。结束的不能改（要的话新建）。改了时间或周期就重算下次
func (s *Scheduler) Update(actor Actor, id int, p Patch) (Reminder, error) {
	if p.empty() {
		return Reminder{}, errors.New("没有要改的内容：text、at、in_minutes、repeat、targets、paused 至少写一样。")
	}
	now := s.opt.Now()
	lim := s.opt.Limits()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.reach(actor, id)
	if err != nil {
		return Reminder{}, err
	}
	if r.Status.Ended() {
		return Reminder{}, fmt.Errorf("提醒 #%d 已经%s了，不能再改。要的话新建一条。", id, r.Status.Text()[len("已"):])
	}
	old := r.clone()
	next := *r
	var what []string
	if p.Text != nil {
		t, err := checkText(*p.Text)
		if err != nil {
			return Reminder{}, err
		}
		next.Text = t
		what = append(what, "内容")
	}
	if p.Targets != nil {
		ts, err := checkTargets(*p.Targets, lim)
		if err != nil {
			return Reminder{}, err
		}
		next.Targets = ts
		what = append(what, "发到哪")
	}
	timing := false
	if p.At != nil {
		if err := checkFuture(*p.At, now); err != nil {
			return Reminder{}, err
		}
		if actor.Source == SourceThread {
			if err := threadLead(*p.At, now, lim); err != nil {
				return Reminder{}, err
			}
		}
		next.At, timing = p.At.In(now.Location()), true
		what = append(what, "时间")
	}
	if p.Repeat != nil {
		rep, err := checkRepeat(*p.Repeat, actor, lim)
		if err != nil {
			return Reminder{}, err
		}
		next.Repeat, timing = rep, true
		what = append(what, "周期")
	}
	if timing {
		switch {
		case p.At != nil:
			next.NextAt = first(next.Repeat, next.At)
		case next.Repeat.None():
			// 变回一次性、没给新时间：下一次还是原来排好的那一次
		default:
			next.NextAt = Next(next.Repeat, next.At, now)
		}
	}
	entry := Entry{At: now, Kind: "edit", By: actor.Source, Note: strings.Join(what, "、")}
	if p.Paused != nil {
		switch {
		case *p.Paused && next.Status == StatusActive:
			next.Status = StatusPaused
			entry = Entry{At: now, Kind: "pause", By: actor.Source, Note: entry.Note}
		case !*p.Paused && next.Status == StatusPaused:
			next.Status = StatusActive
			entry = Entry{At: now, Kind: "resume", By: actor.Source, Note: entry.Note}
			// 暂停期间过了点的：周期提醒不补，从现在往后算；一次性的恢复后马上响
			if !timing && !next.NextAt.After(now) && !next.Repeat.None() {
				next.NextAt = Next(next.Repeat, next.At, now)
			}
		}
	}
	// 改了时间或周期、或暂停后恢复：没收尾的那一次作废，按新安排重新排，免得旧的新的各响一次
	if next.Pending != nil && (timing || (p.Paused != nil && !*p.Paused && r.Status == StatusPaused)) {
		next.Pending = nil
		delete(s.attempts, id)
		delete(s.retryAt, id)
	}
	next.note(entry)
	*r = next
	if err := s.save(); err != nil {
		*r = old
		return Reminder{}, fmt.Errorf("改动没存上：%w", err)
	}
	out := r.clone()
	go s.changed()
	return out, nil
}

// Finish 结束一条：不再响，记录留着。done 是完成，否则是取消。已经结束的不再动
func (s *Scheduler) Finish(actor Actor, id int, done bool) (Reminder, error) {
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.reach(actor, id)
	if err != nil {
		return Reminder{}, err
	}
	if r.Status.Ended() {
		return r.clone(), nil
	}
	old := r.clone()
	kind, st, note := "cancel", StatusCancelled, "取消了"
	if done {
		kind, st, note = "done", StatusDone, "标成完成"
	}
	r.Status, r.EndNote, r.EndedAt = st, actor.Source.Text()+note, now
	r.note(Entry{At: now, Kind: kind, By: actor.Source})
	if err := s.save(); err != nil {
		*r = old
		return Reminder{}, fmt.Errorf("改动没存上：%w", err)
	}
	out := r.clone()
	go s.changed()
	return out, nil
}

// Delete 从表里拿掉一条
func (s *Scheduler) Delete(actor Actor, id int) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.reach(actor, id)
	if err != nil {
		return Reminder{}, err
	}
	out := r.clone()
	old := append([]Reminder(nil), s.st.Reminders...)
	s.st.Reminders = slices.DeleteFunc(s.st.Reminders, func(x Reminder) bool { return x.ID == id })
	if err := s.save(); err != nil {
		s.st.Reminders = old
		return Reminder{}, fmt.Errorf("改动没存上：%w", err)
	}
	go s.changed()
	return out, nil
}

// ───────────── 到点 ─────────────

// Run 按 Poll 的间隔检查到点的提醒，直到 ctx 结束；返回前等在途的响铃结束
func (s *Scheduler) Run(ctx context.Context) {
	tk := time.NewTicker(s.opt.Poll)
	defer tk.Stop()
	defer s.wg.Wait()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

// Wait 等所有在途的响铃结束（测试用）
func (s *Scheduler) Wait() { s.wg.Wait() }

// Tick 检查一次：到点的认领（写盘）后发起响铃，每个角色同时一条，别的下一次检查再看。
// 线里建的提醒遇到主人说“别烦”就顺延到那之后。认领写不上盘就不响：宁可晚一点，也不能响了不记
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.opt.Now()
	s.mu.Lock()
	quiet := map[string]bool{}
	for i := range s.st.Reminders {
		r := &s.st.Reminders[i]
		if r.Status == StatusActive && r.Source == SourceThread && r.Pending == nil && !r.NextAt.After(now) {
			quiet[r.Character] = true
		}
	}
	s.mu.Unlock()
	until := map[string]time.Time{}
	if s.opt.QuietUntil != nil {
		for c := range quiet { // 宿主的判断函数在锁外问，免得它们回头调本包时死锁
			until[c] = s.opt.QuietUntil(c)
		}
	}

	s.mu.Lock()
	changed := false
	if now.Sub(s.lastSeen) > 24*time.Hour {
		changed = s.prune(now)
	}
	order := make([]int, 0, len(s.st.Reminders))
	for i := range s.st.Reminders {
		order = append(order, i)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return s.st.Reminders[order[i]].NextAt.Before(s.st.Reminders[order[j]].NextAt)
	})
	var launch []RingRequest
	for _, i := range order {
		r := &s.st.Reminders[i]
		if r.Status != StatusActive || r.NextAt.After(now) {
			continue
		}
		if _, busy := s.inflight[r.Character]; busy {
			continue
		}
		if at, ok := s.retryAt[r.ID]; ok && now.Before(at) {
			continue
		}
		req := RingRequest{Attempt: s.attempts[r.ID]}
		if r.Pending != nil {
			req.Resumed = true
		} else {
			if u := until[r.Character]; r.Source == SourceThread && u.After(now) {
				r.NextAt = u
				r.note(Entry{At: now, Kind: "defer", Note: "主人说了别烦，顺延到 " + ShortTime(u, now)})
				changed = true
				continue
			}
			missed := countSlots(r.Repeat, r.At, r.NextAt, now) - 1
			r.Pending = &Pending{At: r.NextAt, Missed: missed}
			if err := s.save(); err != nil {
				r.Pending = nil
				continue
			}
			changed = false
		}
		s.inflight[r.Character] = r.ID
		req.Reminder, req.Scheduled, req.Missed = r.clone(), r.Pending.At, r.Pending.Missed
		for t, d := range r.Pending.Deliveries {
			if req.Prior == nil {
				req.Prior = map[string]DeliveryState{}
			}
			req.Prior[t] = d
		}
		launch = append(launch, req)
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	for _, req := range launch {
		s.wg.Add(1)
		go s.runOne(ctx, req)
	}
}

func (s *Scheduler) runOne(ctx context.Context, req RingRequest) {
	defer s.wg.Done()
	res := func() (res RingResult) {
		defer func() {
			if p := recover(); p != nil {
				s.errorf("提醒 #%d 响的时候崩溃了：%v", req.Reminder.ID, p)
				res = RingResult{Retry: true, Note: "这一次崩溃了"}
			}
		}()
		return s.opt.Ring(ctx, req)
	}()
	s.finish(req, res)
}

// StillWanted 说这一次还要不要发：响的过程中被取消、删除、暂停的就不发了
func (s *Scheduler) StillWanted(id int, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.find(id)
	return r != nil && r.Status == StatusActive && r.Pending != nil && r.Pending.At.Equal(at)
}

// Mark 记下这一次在一个目标上的结果并落盘。发 QQ 之前记 sending，重启时才知道不能重发
func (s *Scheduler) Mark(id int, at time.Time, target string, st DeliveryState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.find(id)
	if r == nil || r.Pending == nil || !r.Pending.At.Equal(at) {
		return nil
	}
	if r.Pending.Deliveries == nil {
		r.Pending.Deliveries = map[string]DeliveryState{}
	}
	r.Pending.Deliveries[target] = st
	return s.save()
}

func (s *Scheduler) finish(req RingRequest, res RingResult) {
	id := req.Reminder.ID
	now := s.opt.Now()
	s.mu.Lock()
	if s.inflight[req.Reminder.Character] == id {
		delete(s.inflight, req.Reminder.Character)
	}
	r := s.find(id)
	if r == nil || r.Pending == nil || !r.Pending.At.Equal(req.Scheduled) {
		s.mu.Unlock()
		return
	}
	if res.Aborted {
		s.mu.Unlock() // 程序在退出：Pending 留在盘上，下次启动接着响
		return
	}
	if res.Retry {
		s.attempts[id]++
		s.retryAt[id] = now.Add(s.opt.RetryAfter)
		n := s.attempts[id]
		s.mu.Unlock()
		if n == 1 || n%60 == 0 {
			s.warnf("提醒 #%d 这一次没送到（第 %d 次），过一会儿再试：%s", id, n, res.Note)
		}
		return
	}
	delete(s.attempts, id)
	delete(s.retryAt, id)
	r.Pending = nil
	if res.Dropped {
		s.save()
		s.mu.Unlock()
		go s.changed()
		return
	}
	r.Rings++
	r.LastRing, r.LastNote = now, res.Note
	r.note(Entry{At: now, Kind: "ring", Note: res.Note})
	// 响的过程中时间被改过就不覆盖；一次性响完结束，周期从现在往后算（错过的不补跑）
	if r.Status == StatusActive && r.NextAt.Equal(req.Scheduled) {
		if r.Repeat.None() {
			r.Status, r.EndNote, r.EndedAt = StatusDone, "响过了", now
		} else {
			r.NextAt = Next(r.Repeat, r.At, now)
		}
	}
	s.save()
	s.mu.Unlock()
	s.changed()
}
