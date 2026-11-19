package turn

// 工具在跑时程序退出：正常退出（封口、排空）和被直接杀两条路，重启以后这次工具调用都要看得见、状态写清“程序退出时中断”，
// 下一轮她被提醒一次。规格-停止三段 + 规格-本地回合显示。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

type surfaced struct {
	notices int
	text    string
	steps   []store.DisplayStep
}

// surfaceOf 读这个库里“这一轮被中断”的系统行和挂在它上面的步骤（聊天区读的就是这个）
func (h *harness) surfaceOf() surfaced {
	h.t.Helper()
	var out surfaced
	var seq int64
	rs, err := h.st.Events.Query(`SELECT seq, text FROM events WHERE kind = 'notice' AND origin = 'system:turn' ORDER BY seq`)
	if err != nil {
		h.t.Fatal(err)
	}
	for rs.Next() {
		out.notices++
		rs.Scan(&seq, &out.text)
	}
	rs.Close()
	if out.notices > 0 {
		steps, err := h.st.ReadSteps(consoleConv(h.c.CharacterID), seq)
		if err != nil {
			h.t.Fatal(err)
		}
		out.steps = steps
	}
	return out
}

func stuckWrite(entered chan struct{}) func(*Config) {
	return func(c *Config) {
		// 封口以后给在跑的回合的宽限默认 5 秒；这些用例的工具只在被取消时才返回，不陪真实的 5 秒（取消的两段照样走）
		c.Lifecycle.StopGrace, c.Lifecycle.StopGraceSet = 100*time.Millisecond, true
		c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return tools.Result{Text: "停了"}, nil
		}
	}
}

// quitDuringTool 是“跑 write 时正常退出”的第一个进程：封口、排空，工具被切断，回合收尾
func quitDuringTool(t *testing.T) *harness {
	t.Helper()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: stuckWrite(entered)})
	h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "工具开始执行")
	h.hub.Seal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

// 正常退出：回合记成 stopped 并带上被切断的工具名；步骤标成程序退出时中断；聊天区有一行系统行挂着它；
// 重启后新进程不重跑，下一轮提醒她一次。
func TestQuitDuringToolSurfacesAndReminds(t *testing.T) {
	t.Parallel()
	h := quitDuringTool(t)
	if got := h.turnsInDB(); len(got) != 1 || got[0] != (turnOutcome{"stopped", `["write"]`}) {
		t.Fatalf("回合是被停下的，带上被切断的工具名：%+v", got)
	}
	var ran atomic.Int32
	r := restart(t, h, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`, mutate: countingExec(&ran)})
	sf := r.surfaceOf()
	if sf.notices != 1 || !strings.Contains(sf.text, "程序退出") || !strings.Contains(sf.text, "1 个工具没有跑完") {
		t.Fatalf("重启后留得住一行系统行：%+v", sf)
	}
	if len(sf.steps) != 1 || sf.steps[0].Name != "write" || sf.steps[0].Interrupted != store.InterruptedByExit || !sf.steps[0].IsError {
		t.Fatalf("被切断的 write 挂在系统行上，标成程序退出时中断：%+v", sf.steps)
	}
	if !strings.Contains(sf.steps[0].Args, "a.txt") {
		t.Fatalf("命令的参数还在：%+v", sf.steps[0])
	}
	r.send("刚才写好了吗")
	if ran.Load() != 0 {
		t.Fatalf("新进程不重跑：%d 次", ran.Load())
	}
	if b := r.body(0); !strings.Contains(b, "上一轮还没做完，后台就退出了：write跑到一半，不知道结果") {
		t.Fatalf("正常退出也要提醒她一次：%s", b)
	}
	r.send("好")
	if n := strings.Count(r.body(1), interruptedNote); n != 1 {
		t.Fatalf("只提一次：%d", n)
	}
	if again := r.surfaceOf(); again.notices != 1 {
		t.Fatalf("没有新的中断，不多出系统行：%+v", again)
	}
}

// 被直接杀：新进程收拾时同样留系统行、步骤标成程序退出时中断；系统行不进她的历史（只提醒一次，历史里没有这行字）。
func TestCrashDuringToolSurfaces(t *testing.T) {
	t.Parallel()
	h := killDuringTool(t)
	r := restart(t, h, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`})
	sf := r.surfaceOf()
	if sf.notices != 1 || len(sf.steps) != 1 || sf.steps[0].Interrupted != store.InterruptedByExit || sf.steps[0].Status != "error" {
		t.Fatalf("被杀也留得住：%+v", sf)
	}
	r.send("刚才写好了吗")
	if b := r.body(0); strings.Contains(b, "这一轮被中断了") || !strings.Contains(b, "write跑到一半") {
		t.Fatalf("系统行不进她的历史，提醒走原来那条路：%s", b)
	}
	// 再收拾一次（再重启）不重复出行
	r2 := restart(t, r, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`})
	if again := r2.surfaceOf(); again.notices != 1 {
		t.Fatalf("重启两次也只有一行：%+v", again)
	}
}

// 已经完成的工具也留下：先跑完一个 read，再在 write 上被杀，两个都挂在系统行上，read 不是中断的。
func TestCrashKeepsFinishedToolBeforeInterruptedOne(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("read", `{"path":"a.txt"}`) + `,` + callReply("write", writeCall) + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(ctx context.Context, _, name string, _ jsjson.Value) (tools.Result, error) {
				if name == "read" {
					return tools.Result{Text: "内容"}, nil
				}
				entered <- struct{}{}
				<-ctx.Done()
				return tools.Result{Text: "停了"}, nil
			}
		}})
	h.hub.Send(h.c.CharacterID, "看看再写")
	recv(t, entered, "write 开始执行")
	r := restart(t, h, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`})
	sf := r.surfaceOf()
	if len(sf.steps) != 2 || sf.steps[0].Name != "read" || sf.steps[0].Interrupted != "" || sf.steps[0].IsError || sf.steps[1].Name != "write" || sf.steps[1].Interrupted != store.InterruptedByExit {
		t.Fatalf("已完成的 read 照样显示，write 是中断的：%+v", sf.steps)
	}
	if !strings.Contains(sf.text, "1 个工具没有跑完") {
		t.Fatalf("只数没跑完的：%q", sf.text)
	}
}

// 没有工具在跑时退出（只是在出字）：没有要挂的步骤，不写系统行。
func TestQuitWithoutToolWritesNoNotice(t *testing.T) {
	t.Parallel()
	delta := make(chan string, 4)
	h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["我先说","后半句"],` + usage + `}]`, hold: 1,
		mutate: func(c *Config) { c.OnDelta = func(s string) { delta <- s } }})
	h.hub.Send(h.c.CharacterID, "第一句")
	recv(t, delta, "前半句")
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `]`})
	if sf := r.surfaceOf(); sf.notices != 0 {
		t.Fatalf("没有工具步骤不留行：%+v", sf)
	}
}

// RunningTurns：工具卡住时算一个在跑，退出收尾后是零（sumika-ctl status 的“正在跑的回合”）。
func TestRunningTurnsCountsLiveTurn(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: stuckWrite(entered)})
	if n := h.hub.RunningTurns(); n != 0 {
		t.Fatalf("还没开始：%d", n)
	}
	h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "工具开始执行")
	if n := h.hub.RunningTurns(); n != 1 {
		t.Fatalf("工具在跑算一个回合：%d", n)
	}
	h.hub.Seal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.hub.RunningTurns(); n != 0 {
		t.Fatalf("排空以后没有在跑的：%d", n)
	}
}
