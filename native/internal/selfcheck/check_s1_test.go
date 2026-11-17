package selfcheck_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

type s1env struct {
	t    *testing.T
	c    selfcheck.Check
	live *selfcheck.Counters
	now  time.Time
}

func newS1(t *testing.T) *s1env {
	return &s1env{t: t, c: findCheck(t, "intake.unaccounted", selfcheck.DefaultThresholds()), live: selfcheck.NewCounters(), now: t0}
}

// 评估一次：每次往后走 5 分钟，上一帧都在静默点之外
func (s *s1env) eval() selfcheck.Result {
	s.t.Helper()
	s.now = s.now.Add(5 * time.Minute)
	return s.c.Eval(context.Background(), envAt(s.now, s.live))
}

// 一条消息走完完整路径：帧收到、交给入库、给一个结局
func (s *s1env) deliver(outcome selfcheck.Counter) {
	s.live.Frame()
	s.live.Touch(s.now)
	s.live.Add(selfcheck.FrameToIntake)
	s.live.Add(selfcheck.IntakeLive)
	s.live.Add(selfcheck.IntakeSubmitted)
	s.live.Add(outcome)
}

// B3：每个出口都平衡：入库、重复、准入丢弃、关了丢弃、补录被取消、panic、没收下、入库失败，再加解码失败、不是消息
func TestS1EveryExitIsAccounted(t *testing.T) {
	s := newS1(t)
	for _, k := range []selfcheck.Counter{selfcheck.IntakeStored, selfcheck.IntakeDuplicate, selfcheck.IntakeAdmitDropped, selfcheck.IntakeClosed,
		selfcheck.IntakeCancelled, selfcheck.IntakePanicked, selfcheck.IntakeRejected, selfcheck.IntakeStoreFailed} {
		s.deliver(k)
	}
	s.live.Frame()
	s.live.Add(selfcheck.FrameDecodeFailed)
	s.live.Frame()
	s.live.Add(selfcheck.FrameNonMessage)
	// 补录不经过收帧点：交来的多于收到的帧，也是平衡的
	s.live.Add(selfcheck.IntakeSubmitted)
	s.live.Add(selfcheck.IntakeStored)
	for i := 0; i < 3; i++ {
		if r := s.eval(); r.State != selfcheck.Ok || r.Value != 0 {
			t.Fatalf("第 %d 次评估：%+v", i, r)
		}
	}
}

// B3：造一条消息交给入库一站、却没有任何结局 → 差额 1；第一次不报，连续两次才 bad
func TestS1LostMessageIsReportedOnSecondQuietEvaluation(t *testing.T) {
	s := newS1(t)
	s.eval() // 先建基线
	s.live.Frame()
	s.live.Touch(s.now)
	s.live.Add(selfcheck.FrameToIntake)
	s.live.Add(selfcheck.IntakeLive)
	s.live.Add(selfcheck.IntakeSubmitted) // 没有结局
	if r := s.eval(); r.State != selfcheck.Skip {
		t.Fatalf("第一次不为零先不报：%+v", r)
	}
	r := s.eval()
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["intake"] != "1" {
		t.Fatalf("第二次还不为零应 bad：%+v", r)
	}
	// 适配器这一侧丢的：收到的帧没有去向
	s2 := newS1(t)
	s2.eval()
	s2.live.Frame() // 收到的帧没有任何去向
	s2.eval()
	if r := s2.eval(); r.State != selfcheck.Bad || r.Example["frames"] != "1" {
		t.Fatalf("帧收到了但没有结局也该报：%+v", r)
	}
}

// B9：在途的消息（不在静默点）不判；在途的处理完了，差额回零，不报
func TestS1InFlightIsNotJudged(t *testing.T) {
	s := newS1(t)
	s.eval()
	s.live.Add(selfcheck.IntakeSubmitted)                // 在途
	s.live.Touch(s.now.Add(5*time.Minute - time.Second)) // 评估前 1 秒刚交来一条消息：不在静默点
	s.live.Frame()
	s.live.Add(selfcheck.FrameToIntake)
	s.live.Add(selfcheck.IntakeLive)
	if r := s.eval(); r.State != selfcheck.Skip {
		t.Fatalf("不在静默点不判：%+v", r)
	}
	s.live.Add(selfcheck.IntakeStored) // 在途的处理完了
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("在途的处理完了，差额回零：%+v", r)
	}
	// 一次不为零（在途）、下一次就平了：不报
	s.live.Add(selfcheck.IntakeSubmitted)
	if r := s.eval(); r.State != selfcheck.Skip {
		t.Fatalf("%+v", r)
	}
	s.live.Add(selfcheck.IntakeStored)
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("%+v", r)
	}
}

// 过了零点：以那一刻的差额当基线，只报当天新出现的；昨天丢的不带到今天
func TestS1ResetsBaselineAtMidnight(t *testing.T) {
	s := newS1(t)
	s.eval()
	s.live.Add(selfcheck.IntakeSubmitted)
	s.eval()
	if r := s.eval(); r.State != selfcheck.Bad {
		t.Fatalf("今天丢了一条：%+v", r)
	}
	s.now = time.Date(2026, 11, 14, 0, 30, 0, 0, time.Local)
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("过零点以后昨天的差额当基线，不再报：%+v", r)
	}
	s.live.Add(selfcheck.IntakeSubmitted)
	s.eval()
	if r := s.eval(); r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("今天新丢的照报：%+v", r)
	}
}

// 没有计数器：unknown
func TestS1UnknownWithoutCounters(t *testing.T) {
	c := findCheck(t, "intake.unaccounted", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Live = nil
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Unknown {
		t.Fatalf("%+v", r)
	}
}

// 心跳每几秒来一帧也不能让 S1 永远判不出来：非消息帧不刷新静默点，只有交来的消息刷新
func TestS1HeartbeatFramesDoNotStarveTheQuietPoint(t *testing.T) {
	s := newS1(t)
	beat := func() { // 评估前 2 秒刚来过一帧心跳（走 dispatch：收到一帧、不是消息）
		s.live.Frame()
		s.live.Add(selfcheck.FrameNonMessage)
	}
	beat()
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("心跳不影响静默点，应判得出 ok：%+v", r)
	}
	// 丢了一条消息，心跳照常来：仍然判得出 bad
	s.live.Add(selfcheck.IntakeSubmitted)
	beat()
	if r := s.eval(); r.State != selfcheck.Skip {
		t.Fatalf("第一次不为零先不报：%+v", r)
	}
	beat()
	if r := s.eval(); r.State != selfcheck.Bad {
		t.Fatalf("心跳不断的时候，丢了的消息也要报出来：%+v", r)
	}
}

// 报过的 bad 重启后不丢：换 exe 以后计数器归零，差额也归零，但当天丢的那条没回来；状态记在 KV 里，新实例读到仍是 bad
func TestS1BadSurvivesRestartViaKV(t *testing.T) {
	kv := &selfcheck.KV{}
	eval := func(c selfcheck.Check, live *selfcheck.Counters, now time.Time) selfcheck.Result {
		e := envAt(now, live)
		e.KV = kv
		return c.Eval(context.Background(), e)
	}
	th := selfcheck.DefaultThresholds()
	c1, live1 := findCheck(t, "intake.unaccounted", th), selfcheck.NewCounters()
	now := t0
	eval(c1, live1, now)                    // 基线
	live1.Add(selfcheck.IntakeSubmitted)    // 丢一条
	eval(c1, live1, now.Add(5*time.Minute)) // 第一次不为零
	if r := eval(c1, live1, now.Add(10*time.Minute)); r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("丢了一条应 bad：%+v", r)
	}
	// 换 exe：新的检查实例、全新的计数器（全是 0），同一份状态文件
	c2, live2 := findCheck(t, "intake.unaccounted", th), selfcheck.NewCounters()
	r := eval(c2, live2, now.Add(15*time.Minute))
	if r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("重启后当天仍应是 bad（差额记在状态文件里）：%+v", r)
	}
	// 过了零点：新的一天从 ok 开始
	if r := eval(c2, live2, time.Date(2026, 11, 14, 0, 30, 0, 0, time.Local)); r.State != selfcheck.Ok {
		t.Fatalf("零点以后应恢复：%+v", r)
	}
}

// 评估循环层：重启后同一个 bad 不写假的“自检恢复”，当天文件也不翻回 ok
func TestS1RunnerKeepsBadAcrossRestartWithoutFakeRecovery(t *testing.T) {
	dir := t.TempDir()
	clk := t0
	var warn, info []string
	mk := func(live *selfcheck.Counters) *selfcheck.Runner {
		return selfcheck.New(selfcheck.Config{Dir: dir, Counters: live, Checks: selfcheck.Checks(selfcheck.DefaultThresholds()),
			Now: func() time.Time { return clk }, StartDelay: -1,
			Warnf: func(f string, a ...any) { warn = append(warn, fmt.Sprintf(f, a...)) },
			Infof: func(f string, a ...any) { info = append(info, fmt.Sprintf(f, a...)) }})
	}
	row := func() selfcheck.CheckRow {
		b, err := os.ReadFile(filepath.Join(dir, "2026-11-13.json"))
		if err != nil {
			t.Fatal(err)
		}
		var f selfcheck.DayFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Checks {
			if r.ID == "intake.unaccounted" {
				return r
			}
		}
		t.Fatal("没有 intake.unaccounted")
		return selfcheck.CheckRow{}
	}
	live1 := selfcheck.NewCounters()
	r1 := mk(live1)
	r1.Evaluate(context.Background())
	live1.Add(selfcheck.IntakeSubmitted)
	for i := 0; i < 2; i++ {
		clk = clk.Add(5 * time.Minute)
		r1.Evaluate(context.Background())
	}
	if row().State != selfcheck.Bad {
		t.Fatalf("重启前应 bad：%+v", row())
	}
	r1.Close()
	// 换 exe
	clk = clk.Add(time.Minute)
	r2 := mk(selfcheck.NewCounters())
	r2.Evaluate(context.Background())
	if r := row(); r.State != selfcheck.Bad {
		t.Fatalf("重启后当天文件不该翻回 ok：%+v", r)
	}
	for _, l := range info {
		if strings.Contains(l, "intake.unaccounted") {
			t.Fatalf("不该写假的恢复：%v", info)
		}
	}
	n := 0
	for _, l := range warn {
		if strings.Contains(l, "intake.unaccounted") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一个 bad 只在第一次写一条 WARN：%v", warn)
	}
}

// 评估层面看得到的在途：入库队列里还有排着、没做完的活（拦截链、闸门卡住队列），即使最后一条交来已经超过 5 秒也不是静默点。
// 排着的消息已经算“交来”、还没有结局，差额非零，不能连续两次评估就报成丢了；队列空了、结局补上，差额回零，不报。
func TestS1PendingQueueIsNotQuiet(t *testing.T) {
	s := newS1(t)
	pending := 3
	newEnv := func() *selfcheck.Env {
		e := envAt(s.now, s.live)
		e.Pending = func() int { return pending }
		return e
	}
	s.eval() // 先走一次评估，让时间往前
	// 三条消息交来、都排在被卡住的队列里：交来 +3，结局 0；最后一条交来已经是 10 分钟前
	for i := 0; i < 3; i++ {
		s.live.Frame()
		s.live.Add(selfcheck.FrameToIntake)
		s.live.Add(selfcheck.IntakeLive)
		s.live.Add(selfcheck.IntakeSubmitted)
	}
	s.live.Touch(s.now.Add(-10 * time.Minute))
	for i := 0; i < 4; i++ { // 评估多少次都不报：队列还没空
		s.now = s.now.Add(5 * time.Minute)
		if r := s.c.Eval(context.Background(), newEnv()); r.State != selfcheck.Skip {
			t.Fatalf("第 %d 次：队列里还有 3 条在排，不是静默点：%+v", i, r)
		}
	}
	// 队列做完了：结局补上，差额回零
	pending = 0
	for i := 0; i < 3; i++ {
		s.live.Add(selfcheck.IntakeStored)
	}
	s.now = s.now.Add(5 * time.Minute)
	if r := s.c.Eval(context.Background(), newEnv()); r.State != selfcheck.Ok {
		t.Fatalf("队列空了、结局都有了：%+v", r)
	}
	// 对照：队列空了而结局真的没有，照旧连续两次报出来（Pending 不会把真丢的藏起来）
	s.live.Add(selfcheck.IntakeSubmitted)
	s.live.Add(selfcheck.IntakeLive)
	s.live.Add(selfcheck.FrameToIntake)
	s.live.Frame()
	s.now = s.now.Add(5 * time.Minute)
	if r := s.c.Eval(context.Background(), newEnv()); r.State != selfcheck.Skip {
		t.Fatalf("第一次不为零先不报：%+v", r)
	}
	s.now = s.now.Add(5 * time.Minute)
	if r := s.c.Eval(context.Background(), newEnv()); r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("队列空了还是没有结局，要报：%+v", r)
	}
}

// 交接：适配器交出去的帧，到入库一站实时入口之间丢了（接收端以外的路径），A、B 两条都看不见，C 要报出来；
// 接收端因号对不上有意丢的，记成去向，不报。
func TestS1HandoffLossIsReportedAndSinkDropIsNot(t *testing.T) {
	// 有意丢的：号对不上，记 FrameSinkDropped
	s := newS1(t)
	s.eval()
	s.live.Frame()
	s.live.Add(selfcheck.FrameToIntake)
	s.live.Add(selfcheck.FrameSinkDropped)
	for i := 0; i < 3; i++ {
		if r := s.eval(); r.State != selfcheck.Ok {
			t.Fatalf("第 %d 次：接收端有意丢的是去向，不报：%+v", i, r)
		}
	}
	// 没记去向就没了：交出去的帧在入库一站没有实时交来的记录
	s2 := newS1(t)
	s2.eval()
	s2.live.Frame()
	s2.live.Add(selfcheck.FrameToIntake)
	if r := s2.eval(); r.State != selfcheck.Skip {
		t.Fatalf("第一次不为零先不报：%+v", r)
	}
	r := s2.eval()
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["handoff"] != "1" || r.Example["frames"] != "0" || r.Example["intake"] != "0" {
		t.Fatalf("交接丢了应 bad 且例子指向 handoff：%+v", r)
	}
	// 不数帧的适配器（没有收到过帧）不判 C：补录、测试用的假适配器走实时入口，不会有对应的交出帧
	s3 := newS1(t)
	s3.eval()
	s3.live.Add(selfcheck.IntakeLive)
	s3.live.Add(selfcheck.IntakeSubmitted)
	s3.live.Add(selfcheck.IntakeStored)
	for i := 0; i < 3; i++ {
		if r := s3.eval(); r.State != selfcheck.Ok {
			t.Fatalf("第 %d 次：没数帧的适配器不判 C：%+v", i, r)
		}
	}
}

// 第一次评估的基线是 0：计数器随进程从 0 开始，启动后到第一次评估之间真丢的消息要报，不能被当成基线吸收
func TestS1FirstEvaluationDoesNotAbsorbStartupLoss(t *testing.T) {
	s := newS1(t)
	s.live.Add(selfcheck.IntakeSubmitted) // 启动后 2 分钟里丢了一条，还没评估过
	if r := s.eval(); r.State != selfcheck.Skip {
		t.Fatalf("第一次评估：差额 1，先不报（等下一次静默点确认）：%+v", r)
	}
	if r := s.eval(); r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("启动期丢的那条要报：%+v", r)
	}
	// 过零点才重建基线：零点那次评估把已有的差额当基线（已知盲区，注释里写明）
	s.now = time.Date(2026, 11, 14, 0, 30, 0, 0, time.Local)
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("过了零点恢复：%+v", r)
	}
}
