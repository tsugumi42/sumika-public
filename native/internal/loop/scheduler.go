package loop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Request 是交给宿主的"在这个会话里发起一轮"
type Request struct {
	Task  Task   // 这一轮开始前的任务快照
	Input string // 要当作这一轮输入发给她的文字：任务原文加一行"（定期任务第 N 次……）"
}

// Report 是一轮结束、分类之后交给宿主的结果。
// Kind 为 ResultQuiet 时宿主不要显示、不要通知（Text 已清空）；ResultError 时看 Err；ResultContent 时照常显示。
type Report struct {
	Task   Task // 更新之后的任务
	Kind   ResultKind
	Text   string
	Err    error
	Missed int // 这一轮之前程序没开着错过的次数
}

// Options 是宿主接线时注入的东西。Store、Run 必填，其余有默认
type Options struct {
	Store Store
	// Run 在任务所属会话里跑一轮，返回她这一轮的最终回答。
	// 返回时这一轮的回答还没有给主人看：看不看由 Deliver 收到的 Report.Kind 决定
	Run func(ctx context.Context, req Request) (reply string, err error)
	// SessionBusy 报告这个会话是否有别的轮在进行（可为空）；本包自己的在途轮已经另外算
	SessionBusy func(session string) bool
	// GoalActive 报告这个会话是否有进行中的 /goal（可为空）。有就顺延：目标优先
	GoalActive func(session string) bool
	// Deliver 收一轮的结果（可为空）
	Deliver func(Report)
	Now     func() time.Time
	// Poll 是 Run 循环检查到点的间隔，默认 10 秒
	Poll time.Duration
	// Postpone 是遇忙顺延的时长，默认 1 分钟
	Postpone time.Duration
	Logf     func(format string, args ...any)
}

// Scheduler 管任务表并到点发起一轮
type Scheduler struct {
	opt      Options
	mu       sync.Mutex
	st       State
	inflight map[string]*flight // 会话 → 正在跑的一轮
	wg       sync.WaitGroup
}

type flight struct {
	taskID int
	conv   string // 每次新聊天的任务这一轮实际在哪个会话里跑（宿主 Attach 进来），空表示就在任务的会话

	nextSecs int
	reason   string
}

// New 读回任务表并算出错过的次数：程序没开着时到点的任务不补跑，启动后只跑一次，错过的次数写进那一轮
func New(opt Options) (*Scheduler, error) {
	if opt.Store == nil || opt.Run == nil {
		return nil, errors.New("loop：Store 和 Run 必须提供")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Poll <= 0 {
		opt.Poll = 10 * time.Second
	}
	if opt.Postpone <= 0 {
		opt.Postpone = time.Minute
	}
	st, err := opt.Store.Load()
	if err != nil {
		return nil, err
	}
	if st.NextID < 1 {
		st.NextID = 1
	}
	s := &Scheduler{opt: opt, st: st, inflight: map[string]*flight{}}
	now := opt.Now()
	kept := make([]Task, 0, len(st.Tasks))
	for _, t := range st.Tasks {
		if t.Status == StatusStopped && !t.StoppedAt.IsZero() && now.Sub(t.StoppedAt) > stoppedKeep {
			continue // 停了很久的旧任务不再留着
		}
		if t.Status == StatusRunning && !t.NextRun.After(now) {
			t.PendingMissed = 0
			if t.Interval > 0 {
				t.PendingMissed = int(now.Sub(t.NextRun) / t.Interval)
			} else if sc := t.Schedule(); sc.Clock() {
				t.PendingMissed = sc.countMissed(t.NextRun, now)
			}
		}
		kept = append(kept, t)
	}
	s.st.Tasks = kept
	return s, s.save()
}

func (s *Scheduler) logf(f string, a ...any) {
	if s.opt.Logf != nil {
		s.opt.Logf(f, a...)
	}
}

// save 要在持锁时调用
func (s *Scheduler) save() error {
	err := s.opt.Store.Save(s.st)
	if err != nil {
		s.logf("定期任务表保存失败：%v", err)
	}
	return err
}

// AddOptions 是新建任务的选项
type AddOptions struct {
	// Interval 固定间隔，不小于 MinInterval；At、Days 是每天或每周几某时（见 Schedule）；三样都空表示由她自己定节奏
	Interval time.Duration
	At       string
	Days     []int
	// Forever 为真则不到期；否则 DefaultTTL 后自动停
	Forever bool
	// Notify 通知档（空是默认）；NewChat 每一轮在新开的话题里跑
	Notify  string
	NewChat bool
}

// ValidNotify 检查通知档（空算默认）
func ValidNotify(n string) error {
	switch n {
	case "", NotifyContent, NotifyErrors, NotifyNone:
		return nil
	}
	return fmt.Errorf("通知档要是 %s、%s 或 %s", NotifyContent, NotifyErrors, NotifyNone)
}

// Add 新建任务。固定间隔和自定节奏的第一轮在下一次检查时就跑（不等一个间隔）；每天、每周几的第一轮是下一个钟点
func (s *Scheduler) Add(session, prompt string, o AddOptions) (Task, error) {
	prompt = strings.TrimSpace(prompt)
	if session == "" {
		return Task{}, errors.New("缺少会话")
	}
	if prompt == "" {
		return Task{}, errors.New("任务内容是空的")
	}
	sc := Schedule{Interval: o.Interval, At: o.At, Days: o.Days}
	if err := sc.Validate(); err != nil {
		return Task{}, err
	}
	if err := ValidNotify(o.Notify); err != nil {
		return Task{}, err
	}
	sc = sc.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	t := Task{ID: s.st.NextID, Session: session, Prompt: prompt, Interval: sc.Interval, At: sc.At, Days: sc.Days,
		Notify: o.Notify, NewChat: o.NewChat, NextRun: now, Status: StatusRunning, Created: now}
	if sc.Clock() {
		t.NextRun = sc.Next(now)
	}
	if !o.Forever {
		t.Expires = now.Add(DefaultTTL)
	}
	s.st.NextID++
	s.st.Tasks = append(s.st.Tasks, t)
	return t, s.save()
}

// List 列出一个会话的任务（按编号）；session 为空列全部
func (s *Scheduler) List(session string) []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Task
	for _, t := range s.st.Tasks {
		if session == "" || t.Session == session {
			t.History = append([]Entry(nil), t.History...)
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get 按编号取一个任务
func (s *Scheduler) Get(id int) (Task, bool) {
	for _, t := range s.List("") {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// Stop 停掉任务；id 为 0 表示这个会话里所有没停的。返回动到的个数
func (s *Scheduler) Stop(session string, id int) (int, error) {
	return s.change(session, id, func(t *Task, now time.Time) bool {
		if t.Status == StatusStopped {
			return false
		}
		stopTask(t, "手动停止", now)
		return true
	})
}

// Pause 暂停（id 为 0 表示会话里所有在跑的）
func (s *Scheduler) Pause(session string, id int) (int, error) {
	return s.change(session, id, func(t *Task, _ time.Time) bool {
		if t.Status != StatusRunning {
			return false
		}
		t.Status = StatusPaused
		return true
	})
}

// Resume 恢复暂停或已停的任务（id 为 0 表示会话里所有暂停的）；马上排下一轮。
// 已停的任务只在指定编号时恢复：清掉出错计数，过了期的重新给一个默认存活期
func (s *Scheduler) Resume(session string, id int) (int, error) {
	return s.change(session, id, func(t *Task, now time.Time) bool {
		switch {
		case t.Status == StatusPaused:
		case t.Status == StatusStopped && id != 0:
			t.ConsecErrors, t.StopReason, t.StoppedAt = 0, "", time.Time{}
			if !t.Expires.IsZero() && !now.Before(t.Expires) {
				t.Expires = now.Add(DefaultTTL)
			}
		default:
			return false
		}
		t.Status = StatusRunning
		t.NextRun = firstRun(t.Schedule(), now)
		return true
	})
}

func (s *Scheduler) change(session string, id int, fn func(*Task, time.Time) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	n, found := 0, false
	for i := range s.st.Tasks {
		t := &s.st.Tasks[i]
		if t.Session != session || (id != 0 && t.ID != id) {
			continue
		}
		found = true
		if fn(t, now) {
			n++
		}
	}
	if id != 0 && !found {
		return 0, fmt.Errorf("这个会话里没有定期任务 #%d", id)
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.save()
}

// EditOptions 是修改任务的选项：零值（空、nil）的项不改
type EditOptions struct {
	Interval time.Duration // 新的固定间隔（/loop edit 用），不小于 MinInterval；0 不改
	Prompt   string        // 新的任务文字；空不改
	Schedule *Schedule     // 新的日程，含改成每天、每周几、让她自己定（Schedule{}）；和 Interval 不要同时给
	Notify   *string       // 新的通知档
	NewChat  *bool         // 改成每次新聊天或同一个聊天
}

// firstRun 是一个日程"从现在起"的第一次：钟点日程等下一个钟点，其余马上
func firstRun(sc Schedule, now time.Time) time.Time {
	if sc.Clock() {
		return sc.Next(now)
	}
	return now
}

// Edit 修改任务（停了的任务也能改，不会因此恢复；暂停的改完仍是暂停）。改了日程就重算下次时间：
// 固定间隔从上次开始跑的时间起算，已经过点的马上跑；每天、每周几等下一个钟点；改成让她自己定则从上次起过一小时。
// 只改任务文字、通知档、聊天方式时下次时间不动。
func (s *Scheduler) Edit(session string, id int, o EditOptions) (Task, error) {
	o.Prompt = strings.TrimSpace(o.Prompt)
	if o.Schedule == nil && o.Interval != 0 {
		o.Schedule = &Schedule{Interval: o.Interval}
	}
	if o.Schedule == nil && o.Prompt == "" && o.Notify == nil && o.NewChat == nil {
		return Task{}, errors.New("没有要改的内容：间隔和任务文字至少改一样")
	}
	var sc Schedule
	if o.Schedule != nil {
		sc = *o.Schedule
		if err := sc.Validate(); err != nil {
			return Task{}, err
		}
		sc = sc.Normalized()
	}
	if o.Notify != nil {
		if err := ValidNotify(*o.Notify); err != nil {
			return Task{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.find(id)
	if t == nil || t.Session != session {
		return Task{}, fmt.Errorf("这个会话里没有定期任务 #%d", id)
	}
	if o.Prompt != "" {
		t.Prompt = o.Prompt
	}
	if o.Notify != nil {
		t.Notify = *o.Notify
	}
	if o.NewChat != nil {
		t.NewChat = *o.NewChat
	}
	if o.Schedule != nil {
		now := s.opt.Now()
		t.Interval, t.At, t.Days, t.NextReason = sc.Interval, sc.At, sc.Days, ""
		switch {
		case sc.Clock():
			t.NextRun = sc.Next(now)
		default:
			base := t.LastRun
			if base.IsZero() {
				base = now
			}
			step := sc.Interval
			if step == 0 {
				step = DefaultNext
			}
			t.NextRun = base.Add(step)
			if t.NextRun.Before(now) {
				t.NextRun = now
			}
		}
		t.PendingMissed = 0
	}
	err := s.save()
	out := *t
	out.History = append([]Entry(nil), t.History...)
	return out, err
}

// InFlight 说这个会话现在是不是正在跑一轮定期任务（schedule_next 只在这时对她可见）
func (s *Scheduler) InFlight(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, fl := s.flightFor(session)
	return fl != nil
}

// flightFor 找 conv 对应的在途轮：键是任务的会话，每次新聊天的任务这一轮跑在 Attach 进来的新会话里，也算
func (s *Scheduler) flightFor(conv string) (string, *flight) {
	if fl := s.inflight[conv]; fl != nil {
		return conv, fl
	}
	for k, fl := range s.inflight {
		if fl.conv == conv {
			return k, fl
		}
	}
	return "", nil
}

// Attach 由宿主在"每次新聊天"的任务开跑时调用：告诉调度器这一轮实际在 conv 里跑，
// 这样 schedule_next 在新会话里也认得出是定期任务的一轮
func (s *Scheduler) Attach(session, conv string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fl := s.inflight[session]; fl != nil {
		fl.conv = conv
	}
}

func stopTask(t *Task, reason string, now time.Time) {
	t.Status, t.StopReason, t.StoppedAt = StatusStopped, reason, now
}

// ScheduleNext 是 schedule_next 工具的后台逻辑：在这个会话正在跑的一轮里，让她自己定下次多久后醒。
// 只对不带间隔的任务有效；这一轮结束时生效，没调用就按 DefaultNext 再醒。
func (s *Scheduler) ScheduleNext(session string, seconds int, reason string) error {
	if seconds < MinNextSeconds || seconds > MaxNextSeconds {
		return fmt.Errorf("seconds 要在 %d 到 %d 之间，收到 %d", MinNextSeconds, MaxNextSeconds, seconds)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, fl := s.flightFor(session)
	if fl == nil {
		return errors.New("这个会话现在不在定期任务的一轮里，不需要定下次时间")
	}
	if t := s.find(fl.taskID); t != nil && !t.SelfPaced() {
		return fmt.Errorf("这个定期任务的时间是固定的（%s），不用你定", t.Schedule().Text())
	}
	fl.nextSecs, fl.reason = seconds, strings.TrimSpace(reason)
	return nil
}

// Run 按 Poll 的间隔检查到点的任务，直到 ctx 结束；返回前等在途的轮结束
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

// Wait 等所有在途的轮结束（测试用）
func (s *Scheduler) Wait() { s.wg.Wait() }

// Tick 检查一次：到期的停掉，到点的发起一轮（每轮在自己的 goroutine 里）。
// 会话里有在途的轮、宿主说会话忙、会话有进行中的目标，都顺延 Postpone，不算一次运行。
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.opt.Now()
	s.mu.Lock()
	changed := false
	var due []Task
	for i := range s.st.Tasks {
		t := &s.st.Tasks[i]
		if t.Status != StatusRunning {
			continue
		}
		if expired(t, now) {
			stopTask(t, "到期", now)
			changed = true
			continue
		}
		if !t.NextRun.After(now) {
			due = append(due, *t)
		}
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })

	// 宿主的判断函数在锁外问，免得它们回头调本包时死锁
	hostBusy := map[string]bool{}
	for _, t := range due {
		if _, ok := hostBusy[t.Session]; ok {
			continue
		}
		hostBusy[t.Session] = (s.opt.SessionBusy != nil && s.opt.SessionBusy(t.Session)) ||
			(s.opt.GoalActive != nil && s.opt.GoalActive(t.Session))
	}

	s.mu.Lock()
	var launch []Task
	changed = false
	for _, d := range due {
		t := s.find(d.ID)
		if t == nil || t.Status != StatusRunning || t.NextRun.After(now) {
			continue // 排队期间被改过
		}
		if s.inflight[t.Session] != nil || hostBusy[t.Session] {
			t.NextRun = now.Add(s.opt.Postpone)
			changed = true
			continue
		}
		s.inflight[t.Session] = &flight{taskID: t.ID}
		launch = append(launch, *t)
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	for _, t := range launch {
		s.wg.Add(1)
		go s.runOne(ctx, t, now)
	}
}

func expired(t *Task, now time.Time) bool {
	return !t.Expires.IsZero() && !now.Before(t.Expires)
}

// find 要在持锁时调用
func (s *Scheduler) find(id int) *Task {
	for i := range s.st.Tasks {
		if s.st.Tasks[i].ID == id {
			return &s.st.Tasks[i]
		}
	}
	return nil
}

func buildInput(t Task) string {
	note := fmt.Sprintf("定期任务第 %d 次", t.Runs+1)
	if !t.LastRun.IsZero() {
		note += "，上次在 " + t.LastRun.Format("2006-01-02 15:04")
	}
	if t.PendingMissed > 0 {
		note += fmt.Sprintf("，程序没开着，错过了 %d 次", t.PendingMissed)
	}
	return t.Prompt + "\n\n（" + note + "）"
}

func (s *Scheduler) runOne(ctx context.Context, t Task, started time.Time) {
	defer s.wg.Done()
	reply, err := func() (reply string, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("这一轮崩溃了：%v", r)
			}
		}()
		return s.opt.Run(ctx, Request{Task: t, Input: buildInput(t)})
	}()
	s.finish(ctx, t.ID, t.Session, started, reply, err)
}

func (s *Scheduler) finish(ctx context.Context, id int, session string, started time.Time, reply string, err error) {
	s.mu.Lock()
	fl := s.inflight[session]
	delete(s.inflight, session)
	t := s.find(id)
	if t == nil || (err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		// 任务没了，或程序正在退出：这一轮不算，下次启动时按"错过"处理
		s.mu.Unlock()
		return
	}
	now := s.opt.Now()
	kind, note := Classify(reply)
	if err != nil {
		kind, note = ResultError, err.Error()
	}
	missed := t.PendingMissed
	t.PendingMissed = 0
	if missed > 0 {
		m := fmt.Sprintf("错过了 %d 次", missed)
		if note == "" {
			note = m
		} else {
			note = m + "；" + note
		}
	}
	t.Runs++
	t.LastRun, t.LastResult, t.LastNote = started, kind, note
	t.History = append(t.History, Entry{At: started, Result: kind, Note: note, Missed: missed})
	if len(t.History) > historyKeep {
		t.History = append([]Entry(nil), t.History[len(t.History)-historyKeep:]...)
	}
	if kind == ResultError {
		t.ConsecErrors++
	} else {
		t.ConsecErrors = 0
	}
	if t.Status == StatusRunning {
		switch {
		case t.ConsecErrors >= MaxConsecutiveErrors:
			stopTask(t, fmt.Sprintf("连续 %d 次出错，最后一次：%s", t.ConsecErrors, note), now)
		case expired(t, now):
			stopTask(t, "到期", now)
		case t.SelfPaced():
			d := DefaultNext
			t.NextReason = ""
			if fl != nil && fl.nextSecs > 0 {
				d, t.NextReason = time.Duration(fl.nextSecs)*time.Second, fl.reason
			}
			t.NextRun = now.Add(d)
		case t.Schedule().Clock():
			t.NextRun = t.Schedule().Next(now)
		default:
			next := t.NextRun
			if !next.After(now) {
				next = next.Add((now.Sub(next)/t.Interval + 1) * t.Interval)
			}
			t.NextRun = next
		}
	}
	s.save()
	rep := Report{Task: *t, Kind: kind, Text: reply, Err: err, Missed: missed}
	rep.Task.History = append([]Entry(nil), t.History...)
	s.mu.Unlock()

	if kind == ResultQuiet {
		rep.Text = ""
	}
	if s.opt.Deliver != nil {
		s.opt.Deliver(rep)
	}
}
