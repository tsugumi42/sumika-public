package watchdog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// 规格-日志分级 5.1：守护进程放弃拉起、判定卡死、对账发现上一个守护进程没留结束记录，这些是异常，
// 要能被 --level warn / --errors 看到；正常结束仍是 INFO。以前这几行全是 INFO（Logf）。
type splitLog struct {
	mu                sync.Mutex
	info, warn, error []string
}

func (l *splitLog) attach(s *Supervisor) {
	add := func(to *[]string) func(string, ...any) {
		return func(f string, a ...any) { l.mu.Lock(); *to = append(*to, fmt.Sprintf(f, a...)); l.mu.Unlock() }
	}
	info := add(&l.info)
	prev := s.Logf // harness 的 Logf 还要接着调：child() 靠它知道启动时刻记完了
	s.Logf = func(f string, a ...any) { info(f, a...); prev(f, a...) }
	s.Warnf, s.Errorf = add(&l.warn), add(&l.error)
}

func (l *splitLog) has(level, frag string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := map[string][]string{"info": l.info, "warn": l.warn, "error": l.error}[level]
	return strings.Contains(strings.Join(lines, "\n"), frag)
}

func TestSupervisorEndLineLevel(t *testing.T) {
	t.Run("放弃拉起是 ERROR", func(t *testing.T) {
		h := newHarness(t)
		var lg splitLog
		lg.attach(h.sup)
		h.start()
		h.child().exit(1)
		h.clock.stepUntil(t, 3*time.Second, "第二次拉起", func() bool { return h.count.Load() >= 2 })
		h.child().exit(1)
		h.clock.stepUntil(t, 3*time.Second, "第三次拉起", func() bool { return h.count.Load() >= 3 })
		h.child().exit(1)
		if r := h.wait(); r != ResultGaveUp {
			t.Fatalf("结果 %v", r)
		}
		if !lg.has("error", "守护进程结束：连续崩溃停止拉起") || lg.has("info", "守护进程结束") {
			t.Errorf("放弃拉起该记 ERROR：%+v", &lg)
		}
	})
	t.Run("正常退出仍是 INFO", func(t *testing.T) {
		h := newHarness(t)
		var lg splitLog
		lg.attach(h.sup)
		h.start()
		c := h.child()
		h.clock.advance(time.Hour)
		c.exit(0)
		h.wait()
		if !lg.has("info", "守护进程结束：正常退出") || lg.has("error", "守护进程结束") || lg.has("warn", "守护进程结束") {
			t.Errorf("正常退出该是 INFO：%+v", &lg)
		}
	})
}

func TestStuckDetectionIsWarn(t *testing.T) {
	h := newHarness(t)
	var lg splitLog
	lg.attach(h.sup)
	h.start()
	c1 := h.child()
	h.beat(c1.pid, false)
	h.clock.stepUntil(t, 30*time.Second, "杀掉卡死的进程", func() bool { return c1.killed.Load() })
	h.waitIncidents(1)
	for _, want := range []string{"控制接口也没回话", "判定卡死，结束进程"} {
		if !lg.has("warn", want) || lg.has("info", want) {
			t.Errorf("%q 该记 WARN：%+v", want, &lg)
		}
	}
	h.clock.stepUntil(t, 3*time.Second, "重新拉起", func() bool { return h.count.Load() >= 2 })
	h.child().exit(0)
	h.wait()
}

func TestReconcileExternalKillIsWarn(t *testing.T) {
	e := newReconcileEnv(t)
	var lg splitLog
	lg.attach(e.sup)
	e.oldState(StateRunning, nil)
	e.runOnce()
	if !lg.has("warn", "上一个守护进程没留结束记录") || lg.has("info", "上一个守护进程没留结束记录") {
		t.Errorf("对账发现外部结束该记 WARN：%+v", &lg)
	}
}
