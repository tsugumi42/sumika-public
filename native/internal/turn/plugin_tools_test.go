package turn

// 插件注册工具（规格-Go后台 8.1"插件贡献工具"、四点八 3d）：对着写明的预期测

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
)

// toolPlugin 注册给定的几个工具
type toolPlugin struct {
	id    string
	tools []Tool
}

func (p toolPlugin) ID() string { return p.id }

func (p toolPlugin) Setup(r *Registry) (func(), error) {
	for _, t := range p.tools {
		if _, err := r.OnTool(t); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// memDecl 是记忆的那三个里叫 name 的那个声明
func memDecl(t *testing.T, name string) tools.Declaration {
	t.Helper()
	for _, d := range tools.MemoryDecls() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("没有 %s 的声明", name)
	return tools.Declaration{}
}

// toolNames 是第 i 次请求里工具清单的名字，按先后
func (h *harness) toolNames(i int) []string {
	var r struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	json.Unmarshal([]byte(h.body(i)), &r)
	var out []string
	for _, t := range r.Tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func callReply(name, args string) string {
	b, _ := json.Marshal(args)
	return `{"toolCalls":[{"id":"c1","name":"` + name + `","args":[` + string(b) + `]}],` + usage + `}`
}

// 主窗口和平台群都能调插件的工具（policy 的那一类是 any）：调用带着这一回合是谁、在哪、谁在听、参数；
// 结果进下一次请求；清单里插件的在前、内置的在后
func TestPluginToolRuns(t *testing.T) {
	t.Parallel()
	t.Run("主窗口", func(t *testing.T) {
		t.Parallel()
		var got ToolCall
		h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"牙医"}`) + `,` + textReply + `]`})
		h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(_ context.Context, c ToolCall) (tools.Result, error) {
			got = c
			return tools.Result{Text: "原话：周三看牙医"}, nil
		}}}})
		r := h.send("我上次说什么来着")
		if got.CharacterID != h.c.CharacterID || got.Conversation != consoleConv(h.c.CharacterID) || got.TurnID != r.TurnID ||
			got.Audience.Channel != "console" || !got.Audience.PrivateOK || got.Args.Obj["keywords"].Str != "牙医" {
			t.Fatalf("调用带的东西不对：%+v", got)
		}
		if !strings.Contains(h.body(1), "原话：周三看牙医") {
			t.Fatalf("结果进下一次请求：%s", h.body(1))
		}
		if n := h.toolNames(0); len(n) != 14 || n[0] != "recall_raw" || n[1] != "read" {
			t.Fatalf("插件的在前、内置的在后：%v", n)
		}
	})
	t.Run("平台群", func(t *testing.T) {
		t.Parallel()
		var got ToolCall
		h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"牙医"}`) + `,` + textReply + `]`})
		h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(_ context.Context, c ToolCall) (tools.Result, error) {
			got = c
			return tools.Result{Text: "群里的原话"}, nil
		}}}})
		wait(t, h.inGroup("还记得吗", []string{"scene-group", "platform-qq"}))
		if got.Conversation != groupConv || got.Audience.Channel != "qq" || got.Audience.PrivateOK {
			t.Fatalf("群里那一轮的听众：%+v", got)
		}
		if n := h.toolNames(0); len(n) != 1 || n[0] != "recall_raw" || !strings.Contains(h.body(1), "群里的原话") {
			t.Fatalf("群里只有插件的工具（没有本地执行能力）：%v", n)
		}
	})
}

// 给不给某个听众：policy 那一类先卡（只给主人 × 主窗口的不进群里的清单），再看 For（remember 那种只给私聊的）；
// 清单里没有的，模型调了也是"没有这个工具"
func TestPluginToolAudience(t *testing.T) {
	// 不加 t.Parallel()：下面往全局的 policy.Specs 里登记、再删掉一条测试用的声明，并行的测试同时读这张表会竞争（-race 报过）
	policy.Specs["test_owner_only"] = policy.Spec{Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 5_000}
	t.Cleanup(func() { delete(policy.Specs, "test_owner_only") })
	ran := atomic.Int32{}
	exec := func(context.Context, ToolCall) (tools.Result, error) {
		ran.Add(1)
		return tools.Result{Text: "做了"}, nil
	}
	private := func(a audience.Audience) bool { return a.PrivateOK && a.Scene != "group" }
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("remember", `{"kind":"fact","text":"x"}`) + `,` + textReply + `]`})
	h.use(toolPlugin{"mem", []Tool{
		{Decl: tools.Declaration{Name: "test_owner_only", Description: "只给主人", Parameters: `{"type":"object","properties":{}}`}, Exec: exec},
		{Decl: memDecl(t, "remember"), For: private, Exec: exec},
	}})
	console, _ := h.hub.plugins.toolsFor(audience.Console(consoleConv(h.c.CharacterID)))
	group, _ := h.hub.plugins.toolsFor(groupAud)
	if len(console) != 15 || console[0].Name != "test_owner_only" || console[1].Name != "remember" || len(group) != 0 {
		t.Fatalf("主窗口两个都有、群里一个都没有：%d %d", len(console), len(group))
	}
	wait(t, h.inGroup("记一下", []string{"scene-group", "platform-qq"}))
	if ran.Load() != 0 || !strings.Contains(h.body(1), "Tool remember not found") {
		t.Fatalf("群里调了清单里没有的：执行了 %d 次：%s", ran.Load(), h.body(1))
	}
	for _, tc := range []struct {
		name    string
		aud     audience.Audience
		allowed bool
	}{
		{"本地私聊", audience.Console("console:invented"), true},
		{"本地群聊", audience.Audience{Channel: "console", Conversation: "console:group:invented", Scene: "group", PrivateOK: true}, false},
		{"QQ私聊", audience.Audience{Channel: "qq", Scene: "private", PrivateOK: true}, false},
		{"QQ群", groupAud, false},
		{"群键伪造私聊", audience.Audience{Channel: "console", Conversation: "console:group:invented", Scene: "private", PrivateOK: true}, false},
		{"缺场合的群键", audience.Audience{Channel: "console", Conversation: "console:group:invented", PrivateOK: true}, false},
		{"未知场合", audience.Audience{Channel: "console", Scene: "invented", PrivateOK: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decls, _ := h.hub.plugins.toolsFor(tc.aud)
			for _, name := range []string{"test_owner_only", "read", "powershell", "clipboard", "open", "notify", "process_output", "process_stop"} {
				found := false
				for _, d := range decls {
					found = found || d.Name == name
				}
				if found != tc.allowed {
					t.Errorf("工具 %s 的可见性=%v，期望=%v", name, found, tc.allowed)
				}
			}
			reg := h.hub.plugins.tool("test_owner_only")
			before := ran.Load()
			result := h.hub.plugins.runTool(context.Background(), reg, ToolCall{Audience: tc.aud})
			if result.IsError == tc.allowed || (ran.Load() > before) != tc.allowed {
				t.Errorf("插件执行越界：%+v", result)
			}
			decision := policy.Decide("write", policy.Specs["write"], jsjson.Value{}, policy.Env{Audience: policyAudience(tc.aud)})
			if (decision.Kind != "deny") != tc.allowed {
				t.Errorf("审批门场合判断不一致：%+v", decision)
			}
			// 后台命令的取输出和停止不审批（self），场合只靠 Spec 的 owner-console 挡：QQ 和群聊不能碰
			for _, name := range []string{"process_output", "process_stop"} {
				d := policy.Decide(name, policy.Specs[name], jsjson.Value{}, policy.Env{Audience: policyAudience(tc.aud)})
				if (d.Kind != "deny") != tc.allowed || (tc.allowed && d.Kind != "allow") {
					t.Errorf("%s 的场合判断：%+v", name, d)
				}
			}
		})
	}
}

// 注册时校验：policy 没登记的、和内置重名的、两个插件重名的、没有执行的都不收，插件不启用
func TestPluginToolRegisterChecks(t *testing.T) {
	t.Parallel()
	ok := func(context.Context, ToolCall) (tools.Result, error) { return tools.Result{}, nil }
	h := newHarnessOpts(t, harnessOpts{})
	for name, tl := range map[string]Tool{
		"没登记":   {Decl: tools.Declaration{Name: "no_such_spec"}, Exec: ok},
		"和内置重名": {Decl: tools.Declaration{Name: "read"}, Exec: ok},
		"没有执行":  {Decl: tools.Declaration{Name: "recall_raw"}},
	} {
		if err := h.hub.Use(toolPlugin{"bad-" + name, []Tool{tl}}); err == nil {
			t.Errorf("%s：应该不收", name)
		}
	}
	if err := h.hub.Use(toolPlugin{"first", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: ok}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.Use(toolPlugin{"second", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: ok}}}); err == nil {
		t.Fatal("两个插件注册同一个名字，后一个应该不收")
	}
	if n, _ := h.hub.plugins.toolsFor(audience.Console(consoleConv(h.c.CharacterID))); len(n) != 14 {
		t.Fatalf("没启用的插件不留下工具：%d 个", len(n))
	}
}

// 执行出错、慌了：变成一条出错的结果给她看，这一轮照常；插件停掉：执行到一半的结果不要，下一轮的清单里没有它（实例重建）
func TestPluginToolFailures(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"出错", "慌了", "插件停掉"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{}, 1)
			var h *harness
			h = newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `,` + textReply + `]`})
			h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) {
				switch how {
				case "出错":
					return tools.Result{}, errors.New("记忆库打不开")
				case "慌了":
					panic("坏了")
				}
				entered <- struct{}{}
				<-ctx.Done()
				return tools.Result{Text: "不该看到的结果"}, nil
			}}}})
			out := h.hub.Send(h.c.CharacterID, "找一下")
			if how == "插件停掉" {
				recv(t, entered, "工具开始执行")
				h.hub.StopPlugin("mem")
			}
			wait(t, out)
			want := map[string]string{"出错": "记忆库打不开", "慌了": "出错了，没有执行完", "插件停掉": "提供它的插件停掉了"}[how]
			if b := h.body(1); !strings.Contains(b, want) || strings.Contains(b, "不该看到的结果") {
				t.Fatalf("结果应该是「%s」：%s", want, b)
			}
			if how == "慌了" && !h.logged("出错：坏了") {
				t.Fatalf("慌了记一行：%v", h.logs)
			}
			if how == "插件停掉" {
				h.send("接着说")
				if n := h.toolNames(2); len(n) != 13 || h.lastRebuild() != "fresh" {
					t.Fatalf("插件停掉以后清单里没有它、实例重建：%v %q", n, h.lastRebuild())
				}
			}
		})
	}
}

// 模型还在回的时候插件停掉了，回完调它的工具：这一轮的清单是建实例时定的、还有它；给一条出错的结果，这一轮照常
// （不掉到内置执行报"没有内置工具"、整轮出错）
func TestPluginToolStoppedBeforeRun(t *testing.T) {
	t.Parallel()
	var h *harness
	h = newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`,
		onRequest: func(n int) {
			if n == 1 {
				h.hub.StopPlugin("mem")
			}
		}})
	h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
		t.Error("插件停掉以后不该再执行")
		return tools.Result{}, nil
	}}}})
	h.send("找一下")
	var failures int
	if err := h.st.Trace.QueryRow(`SELECT count(*) FROM steps WHERE kind='tool' AND name='recall_raw' AND is_error=1`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("停用的工具必须有配对失败结果：%d", failures)
	}
}

// remember 是独占屏障，recall_raw 可并行；持住真实执行直到观测并行/独占结局，不靠执行耗时碰重叠。
func TestPluginToolScheduling(t *testing.T) {
	t.Parallel()
	batch := func(names ...string) string {
		var calls []string
		for i, n := range names {
			b, _ := json.Marshal(`{"keywords":"x","kind":"fact","text":"x"}`)
			calls = append(calls, `{"id":"c`+string(rune('a'+i))+`","name":"`+n+`","args":[`+string(b)+`]}`)
		}
		return `{"toolCalls":[` + strings.Join(calls, ",") + `],` + usage + `}`
	}
	for _, c := range []struct {
		name    string
		calls   []string
		overlap bool
	}{
		{"都能并行", []string{"recall_raw", "recall_raw"}, true},
		{"有一个不能并行", []string{"remember", "recall_raw"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			running, most := 0, 0
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			defer free()
			exec := func(context.Context, ToolCall) (tools.Result, error) {
				mu.Lock()
				running++
				most = max(most, running)
				mu.Unlock()
				entered <- struct{}{}
				<-release
				mu.Lock()
				running--
				mu.Unlock()
				return tools.Result{Text: "好"}, nil
			}
			h := newHarnessOpts(t, harnessOpts{replies: `[` + batch(c.calls...) + `,` + textReply + `]`})
			h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: exec}, {Decl: memDecl(t, "remember"), Exec: exec}}})
			out := h.hub.Send(h.c.CharacterID, "找一下")
			recv(t, entered, "第一个插件工具进入")
			if c.overlap {
				recv(t, entered, "并行插件工具进入")
			} else {
				select {
				case <-entered:
					t.Fatal("独占工具未退出，同行已经执行")
				case <-time.After(100 * time.Millisecond):
				}
			}
			free()
			wait(t, out)
			if (most > 1) != c.overlap {
				t.Fatalf("同时在跑的最多 %d 个", most)
			}
		})
	}
	t.Run("总时限", func(t *testing.T) {
		h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`,
			mutate: func(c *Config) {
				c.ToolTimeout = func(name string, d time.Duration) time.Duration {
					if name == "recall_raw" && d == 30*time.Second { // 照 policy 登记的 30 秒
						return 50 * time.Millisecond
					}
					return d
				}
			}})
		h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) {
			<-ctx.Done()
			return tools.Result{Text: "停了"}, nil
		}}}})
		h.send("找一下")
		if b := h.body(1); !strings.Contains(b, "超时") {
			t.Fatalf("到了总时限给超时的那句：%s", b)
		}
	})
}

type countedDisposition struct{ accepted, discarded atomic.Int32 }

func (d *countedDisposition) Accept()  { d.accepted.Add(1) }
func (d *countedDisposition) Discard() { d.discarded.Add(1) }

func TestToolResultDisposition(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"accepted", "revoked", "save-error", "late", "cancelled-ready"} {
		t.Run(mode, func(t *testing.T) {
			finalized := make(chan struct{})
			h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"编造资料"}`) + `,` + textReply + `]`, mutate: func(c *Config) {
				c.Lifecycle.StopGrace = 20 * time.Millisecond
				c.Lifecycle.StopGraceSet = true
				if mode == "late" {
					c.OnToolFinalized = func(context.Context, kernel.Invocation, tools.Result) error { close(finalized); return nil }
				}
			}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			releaseTool := sync.OnceFunc(func() { close(release) })
			defer releaseTool()
			var disposition countedDisposition
			h.use(toolPlugin{"controlled-source", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(_ context.Context, _ ToolCall) (tools.Result, error) {
				if mode == "save-error" {
					if _, err := h.st.Trace.Exec(`CREATE TRIGGER reject_source_finish BEFORE UPDATE ON steps WHEN OLD.kind='tool' AND OLD.name='recall_raw' AND NEW.ended_at IS NOT NULL BEGIN SELECT RAISE(FAIL,'made-save-error'); END`); err != nil {
						return tools.Result{}, err
					}
				}
				if mode == "late" || mode == "cancelled-ready" {
					close(entered)
					<-release
				}
				return tools.Result{Text: "made-controlled-source-proof", Controlled: true, Acceptable: func() bool { return mode != "revoked" }, Disposition: &disposition}, nil
			}}}})
			in := consoleInput(h.c.CharacterID, 0, "取编造资料")
			out := h.hub.RespondContext(ctx, in)
			if mode == "late" || mode == "cancelled-ready" {
				receive(t, entered)
				cancel()
				if mode == "late" {
					// 先确认可见 abort 已保存，再让实际执行返回，不能靠 goroutine 的运气决定迟到。
					receive(t, finalized)
				}
				releaseTool()
			}
			result := receive(t, out)
			if mode == "save-error" && result.Err == nil {
				t.Fatal("结果写库失败被当成成功")
			}
			if mode == "accepted" {
				if result.Err != nil || disposition.accepted.Load() != 1 || disposition.discarded.Load() != 0 || !strings.Contains(h.body(1), "made-controlled-source-proof") {
					t.Fatalf("接纳处置错误 accepted=%d discarded=%d err=%v", disposition.accepted.Load(), disposition.discarded.Load(), result.Err)
				}
			} else {
				if disposition.accepted.Load() != 0 || disposition.discarded.Load() != 1 {
					t.Fatalf("丢弃处置错误 accepted=%d discarded=%d", disposition.accepted.Load(), disposition.discarded.Load())
				}
				if mode == "revoked" && strings.Contains(h.body(1), "made-controlled-source-proof") {
					t.Fatal("失效来源仍进入下一模型请求")
				}
			}
		})
	}
}

func TestToolInputsFreezeAtModelRequest(t *testing.T) {
	t.Parallel()
	var h *harness
	h = newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"first","input_seqs":[99999]}`) + `,` + callReply("recall_raw", `{"keywords":"second"}`) + `,` + textReply + `]`, onRequest: func(n int) {
		if n == 1 {
			if _, ok := h.hub.Steer(h.c.CharacterID, "稍后送达的第二句"); !ok {
				t.Error("steer rejected")
			}
		}
	}})
	var snapshots [][]int64
	h.use(toolPlugin{"bound-input", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(_ context.Context, c ToolCall) (tools.Result, error) {
		snapshots = append(snapshots, slices.Clone(c.InputSeqs))
		if len(c.InputSeqs) > 0 {
			c.InputSeqs[0] = -1
		} // 插件拿到的是副本，不影响下一次工具。
		return tools.Result{Text: "完成"}, nil
	}}}})
	h.send("先送达的第一句")
	if len(snapshots) != 2 || len(snapshots[0]) != 1 || len(snapshots[1]) != 2 || snapshots[0][0] != snapshots[1][0] || snapshots[1][1] <= snapshots[1][0] {
		t.Fatalf("admission snapshots: %v", snapshots)
	}
	for _, ids := range snapshots {
		for _, id := range ids {
			if id <= 0 || id == 99999 {
				t.Fatal("model/plugin mutated source binding")
			}
		}
	}
}

func TestResolverCannotRewriteBoundToolInputs(t *testing.T) {
	t.Parallel()
	called := false
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`, mutate: func(cfg *Config) {
		cfg.Resolver = resolverFunc(func(_ context.Context, c ToolCall) (PreparedCall, error) {
			called = true
			if len(c.InputSeqs) > 0 {
				c.InputSeqs[0] = 99999
			}
			return PreparedCall{}, nil
		})
	}})
	var ids []int64
	h.use(toolPlugin{"input-copy", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(_ context.Context, c ToolCall) (tools.Result, error) {
		ids = slices.Clone(c.InputSeqs)
		return tools.Result{Text: "完成"}, nil
	}}}})
	h.send("原始输入")
	if !called || len(ids) != 1 || ids[0] <= 0 || ids[0] == 99999 {
		t.Fatalf("resolver corrupted sources: %v", ids)
	}
}
