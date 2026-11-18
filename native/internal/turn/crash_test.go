package turn

// 表一 A9"进程被杀以后"：在出字、跑工具、等审批、压缩时杀掉再起。过线：每个工具、每个回合有唯一结局；不重复执行副作用；
// 主人取消过的不复活。对着写明的预期测（Bun 启动时不收拾，这几项在 Bun 上会红）

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// restart 模拟进程被杀再起来：拍下这一刻的数据，在拍下的数据上起一个新 Hub（新进程）。
// 两个库用 VACUUM INTO 拷：拷到的是已经提交的内容，WAL 里已提交、还没合进主文件的也在里面——和进程死了、WAL 还留在磁盘上、
// 下次打开自动恢复的样子一样；被杀的进程没提交的，本来就不会留下。数据目录里库以外的文件（角色卡、配置、授权）照拷。
// 工作目录不拷、接着用：被杀之前做下的副作用本来就留在那里。旧的 Hub 还卡在原来的库上，不再管它（测试收尾时取消）
func restart(t *testing.T, old *harness, o harnessOpts) *harness {
	t.Helper()
	np, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := old.data.SharedDir()
	err = filepath.WalkDir(old.data.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == shared && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(old.data.Root, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(np.Root, rel)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(np.SharedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := old.st.Events.Exec(`VACUUM INTO ?`, np.BunEventsDB()); err != nil {
		t.Fatal(err)
	}
	if _, err := old.st.Trace.Exec(`VACUUM INTO ?`, np.BunTraceDB()); err != nil {
		t.Fatal(err)
	}
	o.data, o.wd = &np, old.wd
	return newHarnessOpts(t, o)
}

type turnOutcome struct{ outcome, unknown string }

// turnsInDB 是库里的回合，按开始的先后
func (h *harness) turnsInDB() []turnOutcome {
	h.t.Helper()
	rs, err := h.st.Trace.Query(`SELECT COALESCE(outcome, ''), COALESCE(unknown_tools, '') FROM turns ORDER BY started_at, rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rs.Close()
	var out []turnOutcome
	for rs.Next() {
		var x turnOutcome
		rs.Scan(&x.outcome, &x.unknown)
		out = append(out, x)
	}
	return out
}

type stepState struct{ status, preview, approval string }

func (h *harness) stepState(id string) stepState {
	h.t.Helper()
	var s stepState
	h.st.Trace.QueryRow(`SELECT status, COALESCE(preview, ''), COALESCE(approval_json, '') FROM steps WHERE provider_call_id = ? OR id = ? ORDER BY started_at DESC LIMIT 1`, id, id).Scan(&s.status, &s.preview, &s.approval)
	return s
}

// countingExec 是数执行次数的工具（新进程里一次都不该跑：内核从不重跑）
func countingExec(n *atomic.Int32) func(*Config) {
	return func(c *Config) {
		c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
			n.Add(1)
			return tools.Result{Text: "写好了"}, nil
		}
	}
}

const writeCall = `{"path":"a.txt","content":"x"}`

// 出字时被杀：这一轮没有结局，新进程记成被打断；她那句没存下，下一句照常回，库里两轮各一个结局
func TestCrashWhileStreaming(t *testing.T) {
	t.Parallel()
	delta := make(chan string, 4)
	h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["我先说","后半句"],` + usage + `}]`, hold: 1,
		mutate: func(c *Config) { c.OnDelta = func(s string) { delta <- s } }})
	h.hub.Send(h.c.CharacterID, "第一句")
	recv(t, delta, "前半句")
	if got := h.turnsInDB(); len(got) != 1 || got[0].outcome != "" {
		t.Fatalf("跑着的回合先记了一行、还没有结局：%+v", got)
	}
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `]`})
	if got := r.turnsInDB(); len(got) != 1 || got[0] != (turnOutcome{"interrupted", ""}) {
		t.Fatalf("新进程把它记成被打断：%+v", got)
	}
	var said int
	r.st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind = 'character'`).Scan(&said)
	if said != 0 {
		t.Fatal("出字时被杀，她那半句没存下")
	}
	r.send("第二句")
	if b := r.body(0); !strings.Contains(b, "第一句") || !strings.Contains(b, "第二句") || strings.Contains(b, interruptedNote) {
		t.Fatalf("两句主人的话都在；没有工具不知道结果，不提醒：%s", b)
	}
	if got := r.turnsInDB(); len(got) != 2 || got[0].outcome != "interrupted" || got[1].outcome != "done" {
		t.Fatalf("两轮各一个结局：%+v", got)
	}
}

// 跑工具时被杀：工具停在执行中，新进程记成出错、不知道结果，回合记成被打断、带上这个工具名；新进程不重跑它
func TestCrashWhileToolRuns(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return tools.Result{Text: "停了"}, nil
			}
		}})
	h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "工具开始执行")
	if s := h.stepState("c1"); s.status != "running" {
		t.Fatalf("执行之前先记了开始：%+v", s)
	}
	var ran atomic.Int32
	r := restart(t, h, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`, mutate: countingExec(&ran)})
	if s := r.stepState("c1"); s.status != "error" || !strings.Contains(s.preview, "不知道结果") {
		t.Fatalf("新进程把它记成出错、不知道结果：%+v", s)
	}
	if got := r.turnsInDB(); len(got) != 1 || got[0] != (turnOutcome{"interrupted", `["write"]`}) {
		t.Fatalf("回合被打断、带上不知道结果的工具：%+v", got)
	}
	r.send("刚才写好了吗")
	if ran.Load() != 0 {
		t.Fatalf("新进程不重跑：执行了 %d 次", ran.Load())
	}
	if s := r.stepState("c1"); s.status != "error" {
		t.Fatalf("结局只有一个：%+v", s)
	}
	// 她的历史里没有工具调用（只从 events 重建），提醒她一次：哪个工具不知道结果、先确认再继续
	if b := r.body(0); !strings.Contains(b, "上一轮还没做完，后台就退出了：write跑到一半，不知道结果") {
		t.Fatalf("提醒她一次：%s", b)
	}
	r.send("好")
	if n := strings.Count(r.body(1), interruptedNote); n != 1 {
		t.Fatalf("只提一次（历史里留着第一次的那条）：%d", n)
	}
}

const interruptedNote = "后台就退出了"

// killDuringTool 是"跑 write 时被杀"的第一个进程（工具卡在执行里）
func killDuringTool(t *testing.T) *harness {
	t.Helper()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return tools.Result{Text: "停了"}, nil
			}
		}})
	h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "工具开始执行")
	return h
}

// 连着被杀两次：第二个进程把提醒交给她了、还没回完又被杀，第三个进程照样提醒（实例跟着第二个进程没了）
func TestCrashTwiceStillReminded(t *testing.T) {
	t.Parallel()
	h := killDuringTool(t)
	delta := make(chan string, 4)
	r1 := restart(t, h, harnessOpts{mode: "yolo", replies: `[{"content":["我看看","后半句"],` + usage + `}]`, hold: 1,
		mutate: func(c *Config) { c.OnDelta = func(s string) { delta <- s } }})
	r1.hub.Send(r1.c.CharacterID, "刚才写好了吗")
	recv(t, delta, "第二个进程开始回")
	if !strings.Contains(r1.body(0), "write跑到一半") {
		t.Fatalf("第二个进程提醒了：%s", r1.body(0))
	}
	var ran atomic.Int32
	r2 := restart(t, r1, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`, mutate: countingExec(&ran)})
	r2.send("在吗")
	if !strings.Contains(r2.body(0), "write跑到一半") || ran.Load() != 0 {
		t.Fatalf("第三个进程照样提醒、不重跑：%d 次\n%s", ran.Load(), r2.body(0))
	}
}

// 提醒还没交给她，这一轮就出错了（找不到模型，没拼参考信息）：下一轮照样提醒
func TestCrashReminderSurvivesEarlyError(t *testing.T) {
	t.Parallel()
	h := killDuringTool(t)
	r := restart(t, h, harnessOpts{mode: "yolo", replies: `[` + textReply + `]`})
	model := r.hub.Cfg.Models[r.c.CharacterID]
	r.hub.Cfg.Models[r.c.CharacterID] = "没有这个模型"
	if o := recv(t, r.hub.Send(r.c.CharacterID, "找不到模型的这一句"), "找不到模型的那一轮"); o.Err == nil {
		t.Fatal("找不到模型应该出错")
	}
	r.hub.Cfg.Models[r.c.CharacterID] = model
	r.send("模型回来了")
	if !strings.Contains(r.body(0), "write跑到一半") {
		t.Fatalf("出错的那一轮没交到她手上，这一轮还要提醒：%s", r.body(0))
	}
}

// 等审批时被杀：卡片没了，这一步记成取消、没有执行；迟到的批准不执行；新进程不重跑
func TestCrashWhileWaitingApproval(t *testing.T) {
	t.Parallel()
	asked := make(chan approval.Request, 1)
	var oldRan atomic.Int32
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: countingExec(&oldRan)})
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) { asked <- req })
	h.hub.Send(h.c.CharacterID, "写个文件")
	req := recv(t, asked, "审批卡片")
	var ran atomic.Int32
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `]`, mutate: countingExec(&ran)})
	if s := r.stepState("c1"); s.status != "cancelled" || !strings.Contains(s.preview, "没有执行") || !strings.Contains(s.approval, `"decision":"cancelled"`) {
		t.Fatalf("等审批的记成取消、没有执行：%+v", s)
	}
	if got := r.turnsInDB(); len(got) != 1 || got[0] != (turnOutcome{"interrupted", ""}) {
		t.Fatalf("回合被打断，没有不知道结果的工具：%+v", got)
	}
	if len(r.approvals.Pending()) != 0 {
		t.Fatal("新进程里没有等着的卡片")
	}
	if ok, _ := r.approvals.Decide(req.ID, "approved", ""); ok {
		t.Fatal("迟到的批准不收")
	}
	r.send("算了")
	if ran.Load() != 0 || oldRan.Load() != 0 {
		t.Fatalf("没批准就没有执行：新进程 %d 次、旧进程 %d 次", ran.Load(), oldRan.Load())
	}
	if strings.Contains(r.body(0), interruptedNote) {
		t.Fatal("等审批的没有执行，不用提醒")
	}
}

// 本地群聊（渠道也是 console）的回合被打断：提醒在这个会话里给，不跑到主窗口
func TestCrashLocalGroupReminded(t *testing.T) {
	t.Parallel()
	const conv = "console:group:demo"
	entered := make(chan struct{}, 1)
	aud := audience.Audience{Channel: "console", Scene: "group", PersonIDs: []string{"owner"}}
	plugin := func(exec func(context.Context, ToolCall) (tools.Result, error)) Plugin {
		return toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: exec}}}
	}
	say := func(h *harness, text string) <-chan Outcome {
		seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: 1}, []string{h.c.CharacterID})
		if err != nil {
			t.Fatal(err)
		}
		return h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Scope: "local-group", Seqs: []int64{seq}, Text: text, Audience: aud, Addressed: true})
	}
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`})
	h.use(plugin(func(ctx context.Context, _ ToolCall) (tools.Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return tools.Result{}, nil
	}))
	say(h, "大家还记得吗")
	recv(t, entered, "插件的工具开始执行")
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	r.use(plugin(func(context.Context, ToolCall) (tools.Result, error) { return tools.Result{Text: "好"}, nil }))
	r.send("主窗口说一句")
	wait(t, say(r, "接着说"))
	if strings.Contains(r.body(0), interruptedNote) || !strings.Contains(r.body(1), "recall_raw跑到一半") {
		t.Fatalf("提醒在本地群聊里给、不在主窗口：\n%s\n----\n%s", r.body(0), r.body(1))
	}
}

// 渠道的回合被打断（平台群里她在跑插件的工具）：照样记成被打断、带上工具名，但不提醒（只给主窗口，和停止提醒一样）
func TestCrashChannelTurnNotReminded(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`})
	h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return tools.Result{}, nil
	}}}})
	scenes := []string{"scene-group", "platform-qq"}
	h.inGroup("还记得吗", scenes)
	recv(t, entered, "插件的工具开始执行")
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	r.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
		return tools.Result{Text: "好"}, nil
	}}}})
	if got := r.turnsInDB(); len(got) != 1 || got[0] != (turnOutcome{"interrupted", `["recall_raw"]`}) {
		t.Fatalf("渠道的回合照样记成被打断：%+v", got)
	}
	wait(t, r.inGroup("接着说", scenes))
	r.send("主窗口说一句")
	if strings.Contains(r.body(0), interruptedNote) || strings.Contains(r.body(1), interruptedNote) {
		t.Fatal("渠道的回合被打断不提醒，主窗口也不提别的会话的")
	}
}

// 压缩时被杀：摘要请求还没回，什么都没留下（没有半截的快照、没有没结局的回合）；新进程照常能压、压成一份
func TestCrashWhileCompacting(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, intercept: holdCompact(entered, release), mutate: smallKeep})
	h.seedRounds(4)
	go h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	recv(t, entered, "摘要请求")
	r := restart(t, h, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `]`, mutate: smallKeep})
	if eps := r.epochs(); len(eps) != 0 {
		t.Fatalf("没压成的不留下快照：%+v", eps)
	}
	if got := r.turnsInDB(); len(got) != 0 {
		t.Fatalf("压缩不是回合：%+v", got)
	}
	if c, err := r.hub.Compact(r.c.CharacterID, consoleConv(r.c.CharacterID), "manual", 0); err != nil || !c.OK {
		t.Fatalf("新进程照常能压：%+v %v", c, err)
	}
	if eps := r.epochs(); len(eps) != 1 || eps[0].status != "ok" {
		t.Fatalf("压成一份：%+v", eps)
	}
}

// 主人取消过的持久输入不自动复活；聊天原文保留，重启后没有旧审批、旧执行或自动模型请求。
func TestCrashAfterOwnerCancelled(t *testing.T) {
	t.Parallel()
	asked := make(chan approval.Request, 1)
	var oldRan atomic.Int32
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: countingExec(&oldRan)})
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) { asked <- req })
	out := h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, asked, "审批卡片")
	h.hub.Steer(h.c.CharacterID, "顺便再写一个")
	h.hub.Stop(h.c.CharacterID)
	// 主窗口被停下的这一轮带着 "The operation was aborted."（7.1 A36），结局是 stopped
	if o := recv(t, out, "被停下的那一轮"); o.Result == nil || !o.Result.Stopped {
		t.Fatalf("这一轮被停下了：%+v", o)
	}
	var ran atomic.Int32
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `]`, mutate: countingExec(&ran)})
	if got := r.turnsInDB(); len(got) != 1 || got[0].outcome != "stopped" {
		t.Fatalf("被停下的还是被停下的，不记成被打断：%+v", got)
	}
	if s := r.stepState("c1"); s.status != "cancelled" || strings.Contains(s.preview, "后台") || !strings.Contains(s.approval, `"decision":"cancelled"`) {
		t.Fatalf("取消的卡片原样：%+v", s)
	}
	if len(r.approvals.Pending()) != 0 || len(r.bodies) != 0 {
		t.Fatal("新进程不会自己接着跑、也没有卡片")
	}
	r.send("新的一句")
	if len(r.bodies) != 1 || len(r.turnsInDB()) != 2 {
		t.Fatalf("取消输入不能重启为额外工作：请求 %d 次、运行 %d 条", len(r.bodies), len(r.turnsInDB()))
	}
	if ran.Load() != 0 || oldRan.Load() != 0 {
		t.Fatalf("工具一次都没执行：新进程 %d 次、旧进程 %d 次", ran.Load(), oldRan.Load())
	}
}
