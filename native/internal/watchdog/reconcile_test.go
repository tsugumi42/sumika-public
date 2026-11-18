package watchdog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 启动对账：上一个守护进程没留结束记录、守护进程和桌面程序都不在了，就记一行日志和一份事故记录。
// 每个用例都走真的 Supervisor.Run，旧状态文件造在临时数据目录里，"谁还活着"用假的

const (
	oldDaemonPid = 4242
	oldChildPid  = 4243
)

type reconcileEnv struct {
	*harness
	mu    sync.Mutex
	lines []string
	alive map[int]bool
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	e := &reconcileEnv{harness: newHarness(t), alive: map[int]bool{}}
	prev := e.sup.Logf // harness 的 Logf 打日志，也告诉 child() 启动时刻记完了
	e.sup.Logf = func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		prev(f, a...)
		e.mu.Lock()
		e.lines = append(e.lines, line)
		e.mu.Unlock()
	}
	e.sup.Alive = func(pid int) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.alive[pid]
	}
	e.sup.SessionStart = func() time.Time { return e.clock.Now().Add(-48 * time.Hour) }
	return e
}

func (e *reconcileEnv) log() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.lines, "\n")
}

// oldState 造上一轮留下的状态和心跳：最后更新是 5 分钟前，心跳是 6 分钟前
func (e *reconcileEnv) oldState(state string, mod func(*State, *Heartbeat)) (State, Heartbeat) {
	e.t.Helper()
	now := e.clock.Now()
	st := State{Pid: oldDaemonPid, StartedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-5 * time.Minute), State: state, ChildPid: oldChildPid}
	hb := Heartbeat{Pid: oldChildPid, StartedAt: now.Add(-2 * time.Hour), Time: now.Add(-6 * time.Minute), Version: "old", Phase: "archive"}
	if mod != nil {
		mod(&st, &hb)
	}
	if err := WriteState(e.dir, st); err != nil {
		e.t.Fatal(err)
	}
	if err := WriteHeartbeat(e.dir, hb); err != nil {
		e.t.Fatal(err)
	}
	return st, hb
}

// runOnce 跑到桌面程序被拉起，再让它正常退出收尾
func (e *reconcileEnv) runOnce() {
	e.t.Helper()
	e.start()
	e.child().exit(0)
	e.wait()
}

func TestReconcileReportsExternalKill(t *testing.T) {
	e := newReconcileEnv(t)
	e.writeLog("desk-2026-11-13.log", "09:54:00 桌面程序还在转\n", false)
	_, oldHb := e.oldState(StateRunning, nil)
	e.runOnce()

	log := e.log()
	for _, want := range []string{"上一个守护进程没留结束记录", "最后更新 09:55:00", "桌面程序 pid 4243", "最后心跳 09:54:00", "被外部一起结束", "Claude"} {
		if !strings.Contains(log, want) {
			t.Errorf("日志里缺 %q：\n%s", want, log)
		}
	}
	inc := e.incidents()
	if len(inc) != 1 {
		t.Fatalf("该有一份事故记录，实际 %v", inc)
	}
	md := e.readIncident(inc[0])
	for _, want := range []string{"external_kill_suspected", "4243", "桌面程序还在转", "09:55:00", "09:54:00"} {
		if !strings.Contains(md, want) {
			t.Errorf("事故记录里缺 %q：\n%s", want, md)
		}
	}
	if strings.Contains(md, "第 0 次") {
		t.Errorf("没有窗口计数时不该写“第 0 次”：\n%s", md)
	}
	// 状态里的最近一次异常退出指向它；旧的状态和心跳各留一份 .prev
	st, err := ReadState(e.dir)
	if err != nil || st.LastIncident == nil || !strings.Contains(st.LastIncident.Reason, "external_kill_suspected") || st.LastIncident.File != filepath.Base(inc[0]) {
		t.Errorf("状态里的最近一次异常退出不对：%+v %v", st.LastIncident, err)
	}
	var prevSt State
	if err := readJSON(statePath(e.dir)+".prev", &prevSt); err != nil || prevSt.Pid != oldDaemonPid || prevSt.State != StateRunning {
		t.Errorf("watchdog.json.prev 不对：%+v %v", prevSt, err)
	}
	var prevHb Heartbeat
	if err := readJSON(heartbeatPath(e.dir)+".prev", &prevHb); err != nil || prevHb.Pid != oldChildPid || !prevHb.Time.Equal(oldHb.Time) {
		t.Errorf("heartbeat.json.prev 不对：%+v %v", prevHb, err)
	}
}

// 心跳是别的进程的（对不上旧桌面程序）：照样报，但日志里如实说，不把它当成旧桌面程序的心跳
func TestReconcileHeartbeatOfAnotherPid(t *testing.T) {
	e := newReconcileEnv(t)
	e.oldState(StateRunning, func(_ *State, hb *Heartbeat) { hb.Pid = 999 })
	e.runOnce()
	if !strings.Contains(e.log(), "上一个守护进程没留结束记录") || !strings.Contains(e.log(), "心跳不是它的") {
		t.Fatalf("日志不对：\n%s", e.log())
	}
	if len(e.incidents()) != 1 {
		t.Fatalf("事故记录 %v", e.incidents())
	}
}

// 没有心跳文件：照样报，最后心跳写"没有"
func TestReconcileNoHeartbeatFile(t *testing.T) {
	e := newReconcileEnv(t)
	e.oldState(StateRunning, nil)
	os.Remove(heartbeatPath(e.dir))
	e.runOnce()
	if !strings.Contains(e.log(), "最后心跳 没有") || len(e.incidents()) != 1 {
		t.Fatalf("日志：\n%s\n事故记录 %v", e.log(), e.incidents())
	}
}

func TestReconcileQuietCases(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *reconcileEnv)
	}{
		{"没有旧状态", func(e *reconcileEnv) {}},
		{"正常结束过（stopped）", func(e *reconcileEnv) { e.oldState(StateStopped, func(s *State, _ *Heartbeat) { s.ChildPid = 0 }) }},
		{"连续崩溃停止拉起过（gave-up）", func(e *reconcileEnv) { e.oldState(StateGaveUp, nil) }},
		{"另一个守护进程正在跑", func(e *reconcileEnv) {
			e.oldState(StateRunning, nil)
			e.alive[oldDaemonPid] = true
		}},
		{"守护进程没了但旧桌面程序还活着", func(e *reconcileEnv) {
			e.oldState(StateRunning, nil)
			e.alive[oldChildPid] = true
		}},
		{"旧状态是上次登录之前的（重启或注销）", func(e *reconcileEnv) {
			e.oldState(StateRunning, nil)
			e.sup.SessionStart = func() time.Time { return e.clock.Now().Add(-time.Minute) }
		}},
		{"桌面程序已经在退出（心跳带退出标记）", func(e *reconcileEnv) {
			e.oldState(StateRunning, func(_ *State, hb *Heartbeat) { hb.Quitting = true })
		}},
		{"旧状态的进程号就是自己", func(e *reconcileEnv) { e.oldState(StateRunning, func(s *State, _ *Heartbeat) { s.Pid = e.sup.Pid }) }},
		{"旧状态文件读坏了", func(e *reconcileEnv) {
			os.MkdirAll(filepath.Join(e.dir, RunDir), 0o755)
			os.WriteFile(statePath(e.dir), []byte("{坏的"), 0o644)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newReconcileEnv(t)
			c.setup(e)
			e.runOnce()
			if strings.Contains(e.log(), "没留结束记录") {
				t.Errorf("不该报：\n%s", e.log())
			}
			if inc := e.incidents(); len(inc) != 0 {
				t.Errorf("不该有事故记录：%v", inc)
			}
		})
	}
}

// 另一个守护进程正在跑时，旧状态文件不是"上一个"的遗物，.prev 也不该去动它
func TestReconcileLeavesPrevAloneWhenAnotherRuns(t *testing.T) {
	e := newReconcileEnv(t)
	e.oldState(StateRunning, nil)
	e.alive[oldDaemonPid] = true
	e.runOnce()
	for _, p := range []string{statePath(e.dir) + ".prev", heartbeatPath(e.dir) + ".prev"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("不该生成 %s", p)
		}
	}
}

// 正常结束的旧状态也留 .prev（覆盖前先备份），但不报
func TestReconcileKeepsPrevOfCleanExit(t *testing.T) {
	e := newReconcileEnv(t)
	e.oldState(StateStopped, func(s *State, _ *Heartbeat) { s.ChildPid = 0 })
	e.runOnce()
	var prevSt State
	if err := readJSON(statePath(e.dir)+".prev", &prevSt); err != nil || prevSt.State != StateStopped {
		t.Fatalf("watchdog.json.prev 不对：%+v %v", prevSt, err)
	}
}

// 带过来的 LastIncident 不能因为这次对账丢了：没报的时候原样带着，报了的时候换成这次的
func TestReconcileKeepsOlderLastIncidentWhenQuiet(t *testing.T) {
	e := newReconcileEnv(t)
	ref := &IncidentRef{Time: e.clock.Now().Add(-24 * time.Hour), Reason: "旧的崩溃", ExitCode: 2, File: "old.md"}
	e.oldState(StateStopped, func(s *State, _ *Heartbeat) { s.ChildPid, s.LastIncident = 0, ref })
	e.runOnce()
	st, _ := ReadState(e.dir)
	if st.LastIncident == nil || st.LastIncident.File != "old.md" {
		t.Fatalf("LastIncident 丢了：%+v", st.LastIncident)
	}
}
