package watchdog

import (
	"context"
	"fmt"
	"os"
	"time"

	"sumika-native/internal/config"
)

// ExitRestart 是桌面程序主动要求"马上重启我"的退出码（沿用 ADR-070 的约定）。现在 Go 版没有谁用它，约定先留着
const ExitRestart = 75

// minUptime：拉起后不到这么久就退出的，不管退出码是 0 还是 75 都要小心——0 多半是"已经有一个在运行"（单实例），
// 75 反复出现就是启动循环
const minUptime = 10 * time.Second

// Child 是守护进程拉起的桌面程序。测试里用假的
type Child interface {
	Pid() int
	// Done 在进程退出后关闭；ExitCode 在 Done 之后才有意义
	Done() <-chan struct{}
	ExitCode() int
	Kill() error
}

// Notifier 弹桌面通知。测试里用假的，断言调用
type Notifier interface {
	Notify(title, body string) error
}

// Params 是守护进程的各项时限，来自配置 watchdog 一节
type Params struct {
	Stuck, StartGrace, CrashWindow, RestartDelay time.Duration
	CrashLimit                                   int
	Notify                                       bool
	// CheckEvery 是检查周期；ProbeTimeout 是探测控制接口的时限；ProbeStrikes 是连续几次探测不通才杀。
	// 控制接口的 status 在界面线程忙时最多要 5 秒才回，所以探测时限取 10 秒，并且要连着两次不通
	CheckEvery, ProbeTimeout time.Duration
	ProbeStrikes             int
}

// ParamsFrom 把配置换成时限
func ParamsFrom(w config.Watchdog) Params {
	return Params{
		Stuck:        time.Duration(w.StuckMinutes) * time.Minute,
		StartGrace:   time.Duration(w.StartGraceMinutes) * time.Minute,
		CrashWindow:  time.Duration(w.CrashWindowMinutes) * time.Minute,
		RestartDelay: time.Duration(w.RestartDelaySeconds) * time.Second,
		CrashLimit:   w.CrashLimit,
		Notify:       w.Notify,
		CheckEvery:   30 * time.Second,
		ProbeTimeout: 10 * time.Second,
		ProbeStrikes: 2,
	}
}

// Result 是守护进程结束的原因
type Result int

const (
	ResultNormal    Result = iota // 桌面程序正常退出（退出码 0）：守护进程跟着结束
	ResultQuit                    // 用户要求退出，退出过程中出了非零退出码（多半是退出超时）：不拉起
	ResultShutdown                // 系统正在关机或注销：不拉起
	ResultGaveUp                  // 连续异常退出太多次：不再拉起
	ResultCancelled               // 守护进程自己被要求停（ctx 取消）：桌面程序不动
)

func (r Result) String() string {
	return [...]string{"正常退出", "用户要求退出", "系统关机", "连续崩溃停止拉起", "守护进程被停"}[r]
}

// Supervisor 拉起桌面程序、盯着它。所有和外界的接触（拉起进程、时钟、通知、探测）都由字段注入，测试里换成假的
type Supervisor struct {
	DataDir  string
	Spawn    func(ctx context.Context) (Child, error)
	Params   func() Params
	Notifier Notifier
	// Probe 探测桌面程序的控制接口：pid 是我们拉起的那个进程；返回 nil 表示它还在回话
	Probe        func(ctx context.Context, pid int) error
	ShuttingDown func() bool
	Now          func() time.Time
	After        func(d time.Duration) <-chan time.Time
	Logf         func(format string, args ...any)
	Warnf        func(format string, args ...any) // WARN（结束进程失败等，会再试或放弃之前）；nil 时用 Logf
	Errorf       func(format string, args ...any) // ERROR（异常退出、结束不掉要放弃）；nil 时用 Logf
	Pid          int                              // 守护进程自己的进程号，写进状态文件
	// Alive 查进程还在不在，SessionStart 是这次登录的开始时间（取不到是零值），都是启动对账用的（reconcile.go）
	Alive        func(pid int) bool
	SessionStart func() time.Time
}

func (s *Supervisor) fill() {
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.After == nil {
		s.After = time.After
	}
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}
	if s.Warnf == nil {
		s.Warnf = s.Logf
	}
	if s.Errorf == nil {
		s.Errorf = s.Logf
	}
	if s.ShuttingDown == nil {
		s.ShuttingDown = func() bool { return false }
	}
	if s.Probe == nil {
		s.Probe = func(context.Context, int) error { return fmt.Errorf("没有探测函数") }
	}
	if s.Params == nil {
		s.Params = func() Params { return ParamsFrom(config.DefaultWatchdog()) }
	}
	if s.Pid == 0 {
		s.Pid = os.Getpid()
	}
	if s.Alive == nil {
		s.Alive = ProcessAlive
	}
	if s.SessionStart == nil {
		s.SessionStart = SessionStart
	}
}

// Run 拉起桌面程序并盯着它，直到出现不该再拉起的情形。ctx 取消时只让守护进程自己结束，不动桌面程序
func (s *Supervisor) Run(ctx context.Context) Result {
	s.fill()
	st := State{Pid: s.Pid, StartedAt: s.Now(), State: StateRunning}
	old, err := ReadState(s.DataDir)
	if err == nil {
		st.LastIncident = old.LastIncident // 上一轮守护进程留下的最近一次异常退出，带过来
	}
	if ref := s.reconcile(old, err == nil); ref != nil {
		st.LastIncident = ref // 上一个守护进程没留结束记录就没了：这次对账发现的，盖过更早的
	}
	finish := func(res Result, state string) Result {
		st.State, st.UpdatedAt, st.ChildPid = state, s.Now(), 0
		s.writeState(st)
		if res == ResultGaveUp { // 放弃拉起 = 要人处理，ERROR；其余结束（正常退出、用户退出、关机、取消）是 INFO
			s.Errorf("守护进程结束：%s", res)
		} else {
			s.Logf("守护进程结束：%s", res)
		}
		return res
	}
	var crashes []time.Time
	var pending *Incident // 刚发生的异常退出，等下一次拉起成功再通知"已经重启"
	first := true

	// crash 记一次异常退出：写事件文件、更新状态，超限就通知并停；否则等一会儿。返回 true 表示要结束 Run
	crash := func(p Params, in Incident, crashOffset int64) (Result, bool) {
		now := s.Now()
		crashes = append(crashes, now)
		kept := crashes[:0]
		for _, t := range crashes {
			if now.Sub(t) <= p.CrashWindow {
				kept = append(kept, t)
			}
		}
		crashes = kept
		in.Time, in.Attempt = now, len(crashes)
		in.GaveUp = len(crashes) >= p.CrashLimit
		in.CrashLog = crashLogSince(s.DataDir, crashOffset)
		in.DeskLog = deskTail(s.DataDir, s.Now)
		file, err := writeIncident(s.DataDir, in)
		if err != nil {
			s.Warnf("写事件记录失败：%v", err)
		}
		st.LastIncident = &IncidentRef{Time: now, Reason: in.Reason, ExitCode: in.ExitCode, File: file}
		s.Errorf("异常退出：%s（退出码 %d，运行了 %s，窗口内第 %d 次）事件记录 %s", in.Reason, in.ExitCode, in.Uptime.Round(time.Second), in.Attempt, file)
		if in.GaveUp {
			s.notify(p, "Sumika 反复异常退出，已停止自动重启",
				fmt.Sprintf("%d 分钟内连续异常退出 %d 次（最近一次：%s）。请看 logs\\incidents\\%s，修好后手动启动。", int(p.CrashWindow.Minutes()), in.Attempt, in.Reason, file))
			return finish(ResultGaveUp, StateGaveUp), true
		}
		in.Reason = fmt.Sprintf("%s；记录 %s", in.Reason, file)
		pending = &in
		select {
		case <-s.After(p.RestartDelay):
		case <-ctx.Done():
			return finish(ResultCancelled, StateStopped), true
		}
		if s.ShuttingDown() {
			return finish(ResultShutdown, StateStopped), true
		}
		return 0, false
	}

	for {
		p := s.Params()
		crashOffset := crashLogSize(s.DataDir)
		child, err := s.Spawn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return finish(ResultCancelled, StateStopped)
			}
			if res, stop := crash(p, Incident{Reason: "拉起失败：" + err.Error(), ExitCode: -1}, crashOffset); stop {
				return res
			}
			continue
		}
		started := s.Now()
		pid := child.Pid()
		if !first {
			st.Restarts++
		}
		first = false
		st.ChildPid, st.UpdatedAt = pid, started
		s.writeState(st)
		s.Logf("已拉起桌面程序，进程 %d", pid)
		if pending != nil {
			s.notify(p, "Sumika 刚才异常退出，已经自动重启", pending.Reason)
			pending = nil
		}

		var (
			beat      time.Time // 这个进程最近一次心跳的时间
			quitting  bool
			strikes   int
			resumeAt  time.Time
			lastTick  = started
			probeNote bool // 已经记过"心跳旧了但还在回话"
			stuckKill bool
		)
		for exited := false; !exited; {
			select {
			case <-child.Done():
				exited = true
			case <-ctx.Done():
				return finish(ResultCancelled, StateStopped)
			case <-s.After(p.CheckEvery):
				now := s.Now()
				gap := now.Sub(lastTick)
				lastTick = now
				st.UpdatedAt = now
				s.writeState(st)
				if hb, err := ReadHeartbeat(s.DataDir); err == nil && hb.Pid == pid {
					if hb.Time.After(beat) {
						beat = hb.Time
					}
					quitting = hb.Quitting
				}
				if gap > 3*p.CheckEvery { // 电脑睡过：醒来这一刻起重新算，不拿睡着的时间判卡死
					s.Logf("检查间隔 %s，像是睡眠或挂起后恢复，这一轮不判卡死", gap.Round(time.Second))
					resumeAt, strikes = now, 0
					continue
				}
				ref, limit := started, p.StartGrace
				if !beat.IsZero() {
					ref, limit = beat, p.Stuck
				}
				if resumeAt.After(ref) {
					ref = resumeAt
				}
				if now.Sub(ref) <= limit {
					strikes, probeNote = 0, false
					continue
				}
				pctx, cancel := context.WithTimeout(ctx, p.ProbeTimeout)
				perr := s.Probe(pctx, pid)
				cancel()
				if perr == nil {
					if !probeNote {
						s.Logf("心跳已经 %s 没更新，但控制接口还在回话，不当卡死", now.Sub(ref).Round(time.Second))
						probeNote = true
					}
					strikes = 0
					continue
				}
				strikes++
				s.Warnf("心跳已经 %s 没更新，控制接口也没回话（第 %d/%d 次）：%v", now.Sub(ref).Round(time.Second), strikes, p.ProbeStrikes, perr)
				if strikes < p.ProbeStrikes {
					continue
				}
				stuckKill = true
				s.Warnf("判定卡死，结束进程 %d", pid)
				if err := child.Kill(); err != nil {
					s.Warnf("结束进程失败：%v", err)
				}
				select {
				case <-child.Done():
				case <-s.After(15 * time.Second):
					s.Errorf("进程 %d 结束不掉，守护进程放弃", pid)
					return finish(ResultGaveUp, StateGaveUp)
				case <-ctx.Done():
					return finish(ResultCancelled, StateStopped)
				}
				exited = true
			}
		}

		code := child.ExitCode()
		uptime := s.Now().Sub(started)
		if hb, err := ReadHeartbeat(s.DataDir); err == nil && hb.Pid == pid && hb.Quitting {
			quitting = true
		}
		switch {
		case stuckKill:
			if res, stop := crash(p, Incident{Reason: "卡死：心跳和控制接口都没有回应，已结束进程", ExitCode: code, Pid: pid, Uptime: uptime}, crashOffset); stop {
				return res
			}
		case code == 0:
			if uptime < minUptime {
				s.Logf("桌面程序 %s 就正常退出了，多半是已经有一个 Sumika 在运行（单实例）；守护进程不再守着", uptime.Round(time.Millisecond))
			} else {
				s.Logf("桌面程序正常退出（运行了 %s）", uptime.Round(time.Second))
			}
			return finish(ResultNormal, StateStopped)
		case code == ExitRestart && uptime >= minUptime:
			s.Logf("桌面程序要求重启（退出码 %d），马上拉起", code)
			if ctx.Err() != nil {
				return finish(ResultCancelled, StateStopped)
			}
		case quitting:
			s.Warnf("用户要求退出，退出过程中退出码是 %d（多半是收尾超时），不拉起", code)
			return finish(ResultQuit, StateStopped)
		case s.ShuttingDown():
			s.Logf("系统正在关机或注销，退出码 %d，不拉起", code)
			return finish(ResultShutdown, StateStopped)
		default:
			reason := fmt.Sprintf("进程退出，退出码 %d", code)
			if code == ExitRestart {
				reason = "启动后不到 10 秒就要求重启，反复如此"
			}
			if res, stop := crash(p, Incident{Reason: reason, ExitCode: code, Pid: pid, Uptime: uptime}, crashOffset); stop {
				return res
			}
		}
	}
}

func (s *Supervisor) writeState(st State) {
	if err := WriteState(s.DataDir, st); err != nil {
		s.Warnf("写守护进程状态失败：%v", err)
	}
}

func (s *Supervisor) notify(p Params, title, body string) {
	if !p.Notify || s.Notifier == nil {
		return
	}
	if err := s.Notifier.Notify(title, body); err != nil {
		s.Warnf("弹桌面通知失败：%v", err)
	}
}
