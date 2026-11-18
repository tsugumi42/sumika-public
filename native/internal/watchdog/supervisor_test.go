package watchdog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// eventually 等条件成立。条件成立前每次让出 CPU 一小会儿，上限只是防卡死
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(guard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等 %s 等不到", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitIncidents 等到至少 n 份事件记录写完。writeIncident 先独占建文件、再写内容（同名要加后缀，靠 O_EXCL 抢名字），
// 只等“文件出现”会在两步之间读到空文件：负载下的合并队列里红过一次（2026-11-18，TestStuckChildIsKilledAfterTwoFailedProbes）。
// 内容是一次 WriteString 写进去的，读到非空就是写完了
func (h *harness) waitIncidents(n int) {
	h.t.Helper()
	eventually(h.t, "事件记录写完", func() bool {
		files := h.incidents()
		if len(files) < n {
			return false
		}
		for _, f := range files {
			if b, err := os.ReadFile(f); err != nil || len(b) == 0 {
				return false
			}
		}
		return true
	})
}

// 正常退出（退出码 0）：不拉起、不通知、不留事件记录，守护进程跟着结束
func TestNormalExitDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.clock.advance(time.Hour) // 跑了很久再正常退出
	c.exit(0)
	if r := h.wait(); r != ResultNormal {
		t.Fatalf("结果 %v", r)
	}
	if n := h.count.Load(); n != 1 {
		t.Fatalf("拉起了 %d 次，正常退出不该再拉起", n)
	}
	if calls := h.notifier.list(); len(calls) != 0 {
		t.Fatalf("正常退出不该通知：%v", calls)
	}
	if inc := h.incidents(); len(inc) != 0 {
		t.Fatalf("正常退出不该留事件记录：%v", inc)
	}
	st, err := ReadState(h.dir)
	if err != nil || st.State != StateStopped {
		t.Fatalf("状态 %+v %v", st, err)
	}
}

// 刚拉起就退出码 0：多半是单实例（已经有一个在运行）。同样是正常退出，守护进程直接结束
func TestInstantZeroExitIsNormal(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.child().exit(0)
	if r := h.wait(); r != ResultNormal || h.count.Load() != 1 || len(h.notifier.list()) != 0 {
		t.Fatalf("结果 %v，拉起 %d，通知 %v", r, h.count.Load(), h.notifier.list())
	}
}

// 异常退出：把 crash.log 新增的部分和 desk 日志最后几行抄进事件记录，拉起之后弹通知
func TestCrashWritesIncidentRestartsAndNotifies(t *testing.T) {
	h := newHarness(t)
	h.writeLog("crash.log", "旧的一次崩溃，不该出现在这次的记录里\n", false)
	h.writeLog("desk-2026-11-13.log", "10:00:00 桌面程序启动\n10:00:05 开始接收 QQ\n", false)
	h.start()
	c1 := h.child()
	h.writeLog("crash.log", "fatal error: all goroutines are asleep - deadlock!\n", true)
	h.clock.advance(2 * time.Minute)
	c1.exit(2)
	h.waitIncidents(1)

	// 通知要等到重新拉起以后才弹（"已经自动重启"）
	h.clock.stepUntil(t, 3*time.Second, "第二次拉起", func() bool { return h.count.Load() >= 2 })
	c2 := h.child()
	eventually(t, "通知", func() bool { return len(h.notifier.list()) == 1 })
	calls := h.notifier.list()
	if !strings.Contains(calls[0][0], "异常退出") || !strings.Contains(calls[0][0], "已经自动重启") {
		t.Fatalf("通知标题 %q", calls[0][0])
	}
	if !strings.Contains(calls[0][1], "退出码 2") {
		t.Fatalf("通知正文要说原因：%q", calls[0][1])
	}

	text := h.readIncident(h.incidents()[0])
	for _, want := range []string{"退出码：2", "all goroutines are asleep", "开始接收 QQ", "窗口内第 1 次", "会重新拉起", "运行了 2m0s"} {
		if !strings.Contains(text, want) {
			t.Errorf("事件记录里缺 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "旧的一次崩溃") {
		t.Errorf("拉起之前就在 crash.log 里的旧内容不该抄进来：\n%s", text)
	}

	st, err := ReadState(h.dir)
	if err != nil || st.State != StateRunning || st.Restarts != 1 || st.ChildPid != c2.pid || st.LastIncident == nil || st.LastIncident.ExitCode != 2 {
		t.Fatalf("状态 %+v %v", st, err)
	}
	c2.exit(0)
	if r := h.wait(); r != ResultNormal {
		t.Fatalf("结果 %v", r)
	}
}

// 通知开关关掉就不弹，其余照常
func TestNotifyOffStillRestarts(t *testing.T) {
	h := newHarness(t)
	h.params.Notify = false
	h.start()
	h.child().exit(1)
	h.clock.stepUntil(t, 3*time.Second, "第二次拉起", func() bool { return h.count.Load() >= 2 })
	h.child().exit(0)
	h.wait()
	if len(h.notifier.list()) != 0 || len(h.incidents()) != 1 {
		t.Fatalf("通知 %v，事件 %v", h.notifier.list(), h.incidents())
	}
}

// 连续崩溃：窗口里第三次就不再拉起，改弹"已停止"的通知
func TestCrashLoopStopsRestarting(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.child().exit(1)
	h.clock.stepUntil(t, 3*time.Second, "第二次拉起", func() bool { return h.count.Load() >= 2 })
	h.child().exit(1)
	h.clock.stepUntil(t, 3*time.Second, "第三次拉起", func() bool { return h.count.Load() >= 3 })
	h.child().exit(1)
	if r := h.wait(); r != ResultGaveUp {
		t.Fatalf("结果 %v", r)
	}
	if n := h.count.Load(); n != 3 {
		t.Fatalf("拉起了 %d 次，第三次崩溃以后不该再拉", n)
	}
	calls := h.notifier.list()
	if len(calls) != 3 || !strings.Contains(calls[0][0], "已经自动重启") || !strings.Contains(calls[1][0], "已经自动重启") || !strings.Contains(calls[2][0], "已停止自动重启") {
		t.Fatalf("通知 %v", calls)
	}
	if !strings.Contains(calls[2][1], "3 次") || len(h.incidents()) != 3 {
		t.Fatalf("停止拉起的通知要写次数 %q，事件记录 %v", calls[2][1], h.incidents())
	}
	if text := h.readIncident(h.incidents()[2]); !strings.Contains(text, "已停止自动拉起") {
		t.Fatalf("最后一份事件记录要写明停止拉起：\n%s", text)
	}
	if st, _ := ReadState(h.dir); st.State != StateGaveUp {
		t.Fatalf("状态 %+v", st)
	}
}

// 崩溃隔得够远（超出窗口）就不连着算：每次都照常拉起
func TestCrashesOutsideWindowDoNotAccumulate(t *testing.T) {
	h := newHarness(t)
	h.start()
	for i := 1; i <= 4; i++ {
		c := h.child()
		c.exit(1)
		h.waitIncidents(i)
		h.clock.advance(11 * time.Minute) // 超出 10 分钟的窗口；这一下也把"等几秒再拉起"放出去
		if i == 4 {
			break
		}
		h.clock.stepUntil(t, 3*time.Second, "重新拉起", func() bool { return h.count.Load() > int32(i) })
	}
	h.clock.stepUntil(t, 3*time.Second, "第四次崩溃后的重新拉起", func() bool { return h.count.Load() >= 5 })
	h.child().exit(0)
	if r := h.wait(); r != ResultNormal {
		t.Fatalf("结果 %v：隔得远的崩溃不该连着算", r)
	}
}

// 用户要求退出（心跳里 Quitting）：退出过程中出了非零码（比如收尾超时）也不拉起、不通知
func TestQuittingNonZeroExitIsNotCrash(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.beat(c.pid, true)
	c.exit(1)
	if r := h.wait(); r != ResultQuit {
		t.Fatalf("结果 %v", r)
	}
	if h.count.Load() != 1 || len(h.notifier.list()) != 0 || len(h.incidents()) != 0 {
		t.Fatalf("拉起 %d，通知 %v，事件 %v", h.count.Load(), h.notifier.list(), h.incidents())
	}
}

// 别的进程号写的 Quitting 不算：上一次运行留下的旧心跳不能让这一次的崩溃被当成正常退出
func TestQuittingFromOtherPidIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.beat(c.pid+500, true)
	c.exit(1)
	h.waitIncidents(1)
	h.clock.stepUntil(t, 3*time.Second, "重新拉起", func() bool { return h.count.Load() >= 2 })
}

// 系统正在关机：桌面程序是被系统结束的，不拉起、不通知
func TestSystemShutdownIsNotCrash(t *testing.T) {
	h := newHarness(t)
	h.shutting.Store(true)
	h.start()
	h.child().exit(1)
	if r := h.wait(); r != ResultShutdown || h.count.Load() != 1 || len(h.notifier.list()) != 0 {
		t.Fatalf("结果 %v，拉起 %d，通知 %v", r, h.count.Load(), h.notifier.list())
	}
}

// 等待重启的那几秒里碰上关机：不再拉起
func TestShutdownDuringRestartDelay(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.child().exit(1)
	h.waitIncidents(1)
	h.shutting.Store(true)
	h.clock.stepUntil(t, 3*time.Second, "守护进程结束", func() bool { return len(h.result) > 0 })
	if r := h.wait(); r != ResultShutdown || h.count.Load() != 1 {
		t.Fatalf("结果 %v，拉起 %d", r, h.count.Load())
	}
}

// 退出码 75 = 桌面程序要求重启：跑了一阵再要求的，马上重新拉起，不当崩溃（没有事件记录、没有通知）
func TestExit75RestartsQuietly(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.clock.advance(time.Minute)
	c.exit(ExitRestart)
	c2 := h.child()
	if len(h.incidents()) != 0 || len(h.notifier.list()) != 0 {
		t.Fatalf("要求重启不是崩溃：事件 %v 通知 %v", h.incidents(), h.notifier.list())
	}
	c2.exit(0)
	if r := h.wait(); r != ResultNormal {
		t.Fatalf("结果 %v", r)
	}
}

// 刚拉起几秒就要求重启、一直如此：当崩溃循环处理，最终停下
func TestExit75LoopIsCrashLoop(t *testing.T) {
	h := newHarness(t)
	h.start()
	for i := 1; i <= 3; i++ {
		h.child().exit(ExitRestart)
		if i < 3 {
			h.clock.stepUntil(t, 3*time.Second, "重新拉起", func() bool { return h.count.Load() > int32(i) })
		}
	}
	if r := h.wait(); r != ResultGaveUp {
		t.Fatalf("结果 %v", r)
	}
}

// 拉起本身失败（找不到 exe 之类）也算异常：留记录，次数够了就停
func TestSpawnFailureCountsAsCrash(t *testing.T) {
	h := newHarness(t)
	h.spawnErr.Store(errBox{err: errors.New("找不到 sumika.exe")})
	h.start()
	h.clock.stepUntil(t, 3*time.Second, "守护进程结束", func() bool { return len(h.result) > 0 })
	if r := h.wait(); r != ResultGaveUp {
		t.Fatalf("结果 %v", r)
	}
	if n := h.count.Load(); n != 3 {
		t.Fatalf("尝试了 %d 次", n)
	}
	calls := h.notifier.list()
	if len(calls) != 1 || !strings.Contains(calls[0][0], "已停止自动重启") || !strings.Contains(calls[0][1], "拉起失败") {
		t.Fatalf("通知 %v", calls)
	}
	if text := h.readIncident(h.incidents()[0]); !strings.Contains(text, "找不到 sumika.exe") {
		t.Fatalf("事件记录：\n%s", text)
	}
}

// 守护进程自己被停（ctx 取消）：桌面程序不动
func TestCancelLeavesChildAlone(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.cancel()
	if r := h.wait(); r != ResultCancelled {
		t.Fatalf("结果 %v", r)
	}
	if c.killed.Load() || h.count.Load() != 1 {
		t.Fatal("守护进程被停时不该动桌面程序")
	}
}

// ───────────── 卡死 ─────────────

// 心跳停了、控制接口也不回话：连着两次探测不通才杀，然后按异常退出处理（事件记录、通知、重新拉起）
func TestStuckChildIsKilledAfterTwoFailedProbes(t *testing.T) {
	h := newHarness(t)
	h.start()
	c1 := h.child()
	h.beat(c1.pid, false) // 最后一次心跳
	h.clock.stepUntil(t, 30*time.Second, "杀掉卡死的进程", func() bool { return c1.killed.Load() })
	if n := h.probes.Load(); n != 2 {
		t.Fatalf("探测了 %d 次，应当恰好连着两次不通才杀", n)
	}
	// 心跳停了 5 分钟才开始判，所以至少过了 5 分钟
	if el := h.clock.Now().Sub(time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)); el < 5*time.Minute {
		t.Fatalf("只过了 %v 就判了卡死", el)
	}
	h.waitIncidents(1)
	if text := h.readIncident(h.incidents()[0]); !strings.Contains(text, "卡死") {
		t.Fatalf("事件记录要写卡死：\n%s", text)
	}
	h.clock.stepUntil(t, 3*time.Second, "重新拉起", func() bool { return h.count.Load() >= 2 })
	h.child().exit(0)
	h.wait()
	calls := h.notifier.list()
	if len(calls) != 1 || !strings.Contains(calls[0][1], "卡死") {
		t.Fatalf("通知 %v", calls)
	}
}

// 心跳旧了但控制接口还在回话（比如一个很长的回合）：不是卡死，不杀
func TestStaleHeartbeatButProbeAnswersIsNotStuck(t *testing.T) {
	h := newHarness(t)
	h.probeAnswers(true)
	h.start()
	c := h.child()
	h.beat(c.pid, false)
	for i := 0; i < 40; i++ { // 20 分钟
		h.clock.step(t, 30*time.Second)
	}
	if c.killed.Load() {
		t.Fatal("控制接口在回话，不该杀")
	}
	if h.probes.Load() == 0 {
		t.Fatal("心跳早就旧了，应当探测过")
	}
	c.exit(0)
	h.wait()
}

// 心跳新鲜就根本不探测
func TestFreshHeartbeatNeverProbes(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	for i := 0; i < 40; i++ {
		h.beat(c.pid, false)
		h.clock.step(t, 30*time.Second)
	}
	if h.probes.Load() != 0 || c.killed.Load() {
		t.Fatalf("心跳一直在更新：探测 %d 次，killed=%v", h.probes.Load(), c.killed.Load())
	}
	c.exit(0)
	h.wait()
}

// 刚拉起还没写出第一次心跳：先给宽限（3 分钟），宽限之内不探测；过了宽限还没有才探测
func TestStartGraceBeforeFirstHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	for i := 0; i < 5; i++ { // 2.5 分钟，还在宽限内
		h.clock.step(t, 30*time.Second)
	}
	if h.probes.Load() != 0 {
		t.Fatalf("宽限内探测了 %d 次", h.probes.Load())
	}
	h.clock.stepUntil(t, 30*time.Second, "宽限过后的探测", func() bool { return h.probes.Load() > 0 })
	c.exit(0)
	h.wait()
}

// 别的进程号写的心跳不算：上一次运行留下的旧文件不能让新进程显得"心跳很旧"，也不能让它显得健康
func TestHeartbeatOfOtherPidIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.beat(c.pid+1, false) // 另一个进程的心跳
	for i := 0; i < 5; i++ {
		h.clock.step(t, 30*time.Second)
	}
	if h.probes.Load() != 0 {
		t.Fatalf("还在宽限内，探测了 %d 次", h.probes.Load())
	}
	h.clock.stepUntil(t, 30*time.Second, "宽限过后的探测", func() bool { return h.probes.Load() > 0 })
	c.exit(0)
	h.wait()
}

// 电脑睡过（两次检查之间隔了很久）：醒来这一刻起重新算，不拿睡着的时间判卡死
func TestSleepGapDoesNotLookStuck(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := h.child()
	h.beat(c.pid, false)
	h.clock.step(t, 2*time.Hour) // 睡了两个小时
	for i := 0; i < 4; i++ {     // 醒来以后 2 分钟，心跳照理很快就会有，这里故意不更新，但还没到时限
		h.clock.step(t, 30*time.Second)
	}
	if h.probes.Load() != 0 || c.killed.Load() {
		t.Fatalf("睡眠以后不该立刻判卡死：探测 %d，killed=%v", h.probes.Load(), c.killed.Load())
	}
	c.exit(0)
	h.wait()
}

// 杀不掉（Kill 报错、进程也没退）：守护进程放弃，不无限等
func TestUnkillableStuckChild(t *testing.T) {
	h := newHarness(t)
	h.killFail = true
	h.start()
	c := h.child()
	h.beat(c.pid, false)
	h.clock.stepUntil(t, 30*time.Second, "守护进程放弃", func() bool { return len(h.result) > 0 })
	if r := h.wait(); r != ResultGaveUp || !c.killed.Load() {
		t.Fatalf("结果 %v，killed=%v", r, c.killed.Load())
	}
}

// 上一轮守护进程留下的最近一次异常退出，新一轮要带过来
func TestLastIncidentCarriedOver(t *testing.T) {
	h := newHarness(t)
	old := &IncidentRef{Time: h.clock.Now().Add(-time.Hour), Reason: "上一轮的崩溃", ExitCode: 3, File: "old.md"}
	if err := WriteState(h.dir, State{Pid: 1, State: StateStopped, LastIncident: old}); err != nil {
		t.Fatal(err)
	}
	h.start()
	c := h.child()
	c.exit(0)
	h.wait()
	st, _ := ReadState(h.dir)
	if st.LastIncident == nil || st.LastIncident.Reason != "上一轮的崩溃" {
		t.Fatalf("没带过来：%+v", st)
	}
}

func TestIncidentFileNamesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	in := Incident{Time: time.Date(2026, 11, 13, 19, 37, 0, 0, time.Local), Reason: "x"}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		name, err := writeIncident(dir, in)
		if err != nil || seen[name] {
			t.Fatalf("第 %d 次：%q %v", i, name, err)
		}
		seen[name] = true
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "logs", "incidents", "*.md")); len(m) != 3 {
		t.Fatalf("文件 %v", m)
	}
}
