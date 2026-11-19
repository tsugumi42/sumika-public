package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// ───────────── 心跳 ─────────────

func TestHeartbeatRoundTripAndAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadHeartbeat(dir); err == nil {
		t.Fatal("没有文件应当报错")
	}
	at := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ { // 反复覆盖：目录里不能留下临时文件
		want := Heartbeat{Pid: 42, StartedAt: at, Time: at.Add(time.Duration(i) * time.Minute), Version: "v", Phase: "receiving", RunningTurns: i}
		if err := WriteHeartbeat(dir, want); err != nil {
			t.Fatal(err)
		}
		got, err := ReadHeartbeat(dir)
		if err != nil || !got.Time.Equal(want.Time) || got.Pid != 42 || got.Phase != "receiving" || got.RunningTurns != i || got.Quitting {
			t.Fatalf("读回 %+v %v", got, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, RunDir))
	if len(entries) != 1 {
		t.Fatalf("run/ 里应当只有心跳文件：%v", entries)
	}
}

func TestReadHeartbeatBrokenFileIsError(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, RunDir), 0o755)
	os.WriteFile(heartbeatPath(dir), []byte(`{"pid":1,"time":"20`), 0o644)
	if _, err := ReadHeartbeat(dir); err == nil {
		t.Fatal("半截的文件应当报错，不能当成有效心跳")
	}
}

func TestBeaterWritesAtStartAndMarksQuitting(t *testing.T) {
	dir := t.TempDir()
	var turns atomic.Int32
	turns.Store(2)
	b := NewBeater(dir, time.Hour, func() Snapshot { return Snapshot{Version: "v1", Phase: "archive", RunningTurns: int(turns.Load())} }, t.Logf)
	fixed := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return fixed }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.Run(ctx) // ctx 已经取消：Run 先写一次再返回
	hb, err := ReadHeartbeat(dir)
	if err != nil || hb.Pid != os.Getpid() || hb.Phase != "archive" || hb.RunningTurns != 2 || hb.Version != "v1" || !hb.Time.Equal(fixed) || hb.Quitting {
		t.Fatalf("开头那次心跳 %+v %v", hb, err)
	}
	b.MarkQuitting()
	if hb, _ = ReadHeartbeat(dir); !hb.Quitting {
		t.Fatalf("MarkQuitting 要立刻写进去：%+v", hb)
	}
	turns.Store(0)
	b.Beat()
	if hb, _ = ReadHeartbeat(dir); !hb.Quitting || hb.RunningTurns != 0 {
		t.Fatalf("退出标记要一直带着：%+v", hb)
	}
}

func TestBeaterLogsFailureOnceAndRecovery(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, RunDir)
	os.WriteFile(blocker, []byte("x"), 0o644) // run 是个文件：写不进去
	var lines []string
	b := NewBeater(dir, time.Hour, func() Snapshot { return Snapshot{} }, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	for i := 0; i < 3; i++ {
		if b.Beat() == nil {
			t.Fatal("应当失败")
		}
	}
	if len(lines) != 1 {
		t.Fatalf("同样的失败只记一次：%v", lines)
	}
	os.Remove(blocker)
	if err := b.Beat(); err != nil || len(lines) != 2 || !strings.Contains(lines[1], "又写进去") {
		t.Fatalf("恢复要记一行：%v %v", lines, err)
	}
}

// ───────────── 状态文件 ─────────────

func TestStateRunning(t *testing.T) {
	now := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }
	cases := []struct {
		name string
		s    State
		f    func(int) bool
		want bool
	}{
		{"在跑", State{State: StateRunning, UpdatedAt: now.Add(-time.Minute), Pid: 5}, alive, true},
		{"状态文件太旧", State{State: StateRunning, UpdatedAt: now.Add(-10 * time.Minute), Pid: 5}, alive, false},
		{"进程已经没了", State{State: StateRunning, UpdatedAt: now.Add(-time.Minute), Pid: 5}, dead, false},
		{"已经停了", State{State: StateStopped, UpdatedAt: now.Add(-time.Minute), Pid: 5}, alive, false},
		{"放弃了", State{State: StateGaveUp, UpdatedAt: now.Add(-time.Minute), Pid: 5}, alive, false},
	}
	for _, c := range cases {
		if got := c.s.Running(now, c.f); got != c.want {
			t.Errorf("%s：%v", c.name, got)
		}
	}
}

// ───────────── 事件记录 ─────────────

func TestCrashLogSince(t *testing.T) {
	dir := t.TempDir()
	if crashLogSize(dir) != 0 || crashLogSince(dir, 0) != "" {
		t.Fatal("没有文件应当是空")
	}
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	p := filepath.Join(dir, "logs", crashLogName)
	os.WriteFile(p, []byte("旧\n"), 0o644)
	off := crashLogSize(dir)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("新的一段\n")
	f.Close()
	if got := crashLogSince(dir, off); got != "新的一段\n" {
		t.Fatalf("只抄新增的：%q", got)
	}
	os.WriteFile(p, []byte("清过了\n"), 0o644) // 文件比记下的偏移还小：被人清过，从头读
	if got := crashLogSince(dir, off+100); got != "清过了\n" {
		t.Fatalf("文件变小从头读：%q", got)
	}
	os.WriteFile(p, []byte(strings.Repeat("甲", crashDeltaMax)), 0o644) // 3 倍字节，远超上限
	got := crashLogSince(dir, 0)
	if !strings.HasPrefix(got, "（前面的省略了）") || len(got) > crashDeltaMax+100 {
		t.Fatalf("太长要只留尾巴：%d 字节", len(got))
	}
}

func TestIncidentMarkdownWithoutCrashLog(t *testing.T) {
	md := Incident{Time: time.Date(2026, 11, 13, 19, 37, 0, 0, time.Local), Reason: "进程退出，退出码 1", ExitCode: 1, Pid: 9, Uptime: time.Hour, Attempt: 1}.Markdown()
	for _, want := range []string{"2026-11-13 19:37:00", "没有 Go 运行时崩溃信息", "（没有读到）", "守护进程会重新拉起"} {
		if !strings.Contains(md, want) {
			t.Errorf("缺 %q：\n%s", want, md)
		}
	}
}

// ───────────── 探测控制接口 ─────────────

func serveOnce(t *testing.T, reply string) net.Conn {
	t.Helper()
	c, s := net.Pipe()
	t.Cleanup(func() { c.Close(); s.Close() })
	go func() {
		line, err := ctlproto.ReadLine(bufio.NewReader(s), 1<<16)
		if err != nil {
			return
		}
		var req ctlproto.Request
		if json.Unmarshal(line, &req) != nil || req.Cmd != ctlproto.CmdStatus || req.V != ctlproto.Version {
			s.Write([]byte(`{"ok":false,"error":"请求不对"}` + "\n"))
			return
		}
		if reply != "" {
			s.Write([]byte(reply + "\n"))
		}
	}()
	return c
}

func TestProbeConn(t *testing.T) {
	okReply := func(pid int) string {
		b, _ := json.Marshal(ctlproto.Reply{OK: true, Data: json.RawMessage(fmt.Sprintf(`{"pid":%d,"phase":"archive"}`, pid))})
		return string(b)
	}
	ctx := context.Background()
	if err := probeConn(ctx, serveOnce(t, okReply(77)), 77); err != nil {
		t.Fatalf("pid 对得上应当通：%v", err)
	}
	if err := probeConn(ctx, serveOnce(t, okReply(78)), 77); err == nil || !strings.Contains(err.Error(), "不是我们拉起的") {
		t.Fatalf("别的进程回话不算：%v", err)
	}
	if err := probeConn(ctx, serveOnce(t, `{"ok":false,"error":"正在退出","code":"failed"}`), 77); err == nil {
		t.Fatal("回错误不算通")
	}
	if err := probeConn(ctx, serveOnce(t, `这不是 JSON`), 77); err == nil {
		t.Fatal("读不懂的回话不算通")
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := probeConn(short, serveOnce(t, ""), 77); err == nil { // 对面收了请求但不回话：到时限就是不通
		t.Fatal("不回话应当超时")
	}
}

// ───────────── 真子进程 ─────────────

// TestHelperChild 是假的"桌面程序"：测试用自己的二进制重新执行它（环境变量 WATCHDOG_HELPER 决定做什么），不用现编 exe
func TestHelperChild(t *testing.T) {
	switch os.Getenv("WATCHDOG_HELPER") {
	case "":
		t.Skip("只有被 StartExec 拉起时才跑")
	case "exit0":
		os.Exit(0)
	case "exit3":
		os.Exit(3)
	case "block":
		select {}
	case "crash-then-ok": // 第一次运行退出码 3，之后退出码 0；用计数文件记次数
		p := os.Getenv("WATCHDOG_COUNTER")
		n, _ := os.ReadFile(p)
		os.WriteFile(p, append(n, 'x'), 0o644)
		if len(n) == 0 {
			os.Exit(3)
		}
		os.Exit(0)
	}
}

func helperEnv(mode string, extra ...string) []string {
	return append([]string{"WATCHDOG_HELPER=" + mode}, extra...)
}

func helperArgs() []string { return []string{"-test.run=^TestHelperChild$"} }

func TestStartExecExitCodesAndKill(t *testing.T) {
	for mode, want := range map[string]int{"exit0": 0, "exit3": 3} {
		c, err := StartExec(os.Args[0], helperArgs(), helperEnv(mode))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.Done():
		case <-time.After(guard):
			t.Fatalf("%s：子进程没退出", mode)
		}
		if c.ExitCode() != want || ProcessAlive(c.Pid()) {
			t.Fatalf("%s：退出码 %d（想要 %d），进程还在=%v", mode, c.ExitCode(), want, ProcessAlive(c.Pid()))
		}
	}
	c, err := StartExec(os.Args[0], helperArgs(), helperEnv("block"))
	if err != nil {
		t.Fatal(err)
	}
	if !ProcessAlive(c.Pid()) {
		t.Fatal("刚拉起的进程应当在")
	}
	if err := c.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(guard):
		t.Fatal("Kill 以后进程没退出")
	}
	if c.ExitCode() == 0 {
		t.Fatal("被杀的进程退出码不该是 0")
	}
	if _, err := StartExec(filepath.Join(t.TempDir(), "没有这个.exe"), nil, nil); err == nil {
		t.Fatal("找不到 exe 要报错")
	}
}

// 真进程 + 真时钟走一遍：第一次退出码 3（崩溃），守护进程拉起第二次，第二次退出码 0（正常），守护进程结束
func TestSupervisorWithRealProcesses(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	n := &fakeNotifier{}
	var spawns atomic.Int32
	s := &Supervisor{
		DataDir:  dir,
		Notifier: n,
		Params: func() Params {
			p := testParams()
			p.RestartDelay = 0
			return p
		},
		Spawn: func(context.Context) (Child, error) {
			spawns.Add(1)
			return StartExec(os.Args[0], helperArgs(), helperEnv("crash-then-ok", "WATCHDOG_COUNTER="+counter))
		},
		Logf: t.Logf,
	}
	done := make(chan Result, 1)
	go func() { done <- s.Run(context.Background()) }()
	select {
	case r := <-done:
		if r != ResultNormal || spawns.Load() != 2 {
			t.Fatalf("结果 %v，拉起 %d 次", r, spawns.Load())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("守护进程没有结束")
	}
	inc, _ := filepath.Glob(filepath.Join(dir, "logs", "incidents", "*.md"))
	if len(inc) != 1 || len(n.list()) != 1 {
		t.Fatalf("事件记录 %v，通知 %v", inc, n.list())
	}
	b, _ := os.ReadFile(inc[0])
	if !strings.Contains(string(b), "退出码：3") {
		t.Fatalf("事件记录里要有真实的退出码：\n%s", b)
	}
}
