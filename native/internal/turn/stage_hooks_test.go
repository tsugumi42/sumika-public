package turn

// 用户钩子的两个挂点（规格-用户钩子 第三节、第十一节）：工具后、回合要结束时。
// 内核只管"什么时候问、问完怎么接"；这里用假插件对着写明的预期测

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
)

// stagePlugin 注册两个挂点；回调可以是 nil
type stagePlugin struct {
	id     string
	result ToolResultFn
	stop   StopFn
	max    int
}

func (p stagePlugin) ID() string { return p.id }

func (p stagePlugin) Setup(r *Registry) (func(), error) {
	if p.result != nil {
		r.OnToolResult(p.result)
	}
	if p.stop != nil {
		r.OnStop(p.max, p.stop)
	}
	return nil, nil
}

func stageRecall(t *testing.T, ran *int) toolPlugin {
	return toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
		*ran++
		return tools.Result{Text: "原话：周三看牙医"}, nil
	}}}}
}

func toolThenText() string {
	return `[` + callReply("recall_raw", `{"keywords":"牙医"}`) + `,` + textReply + `]`
}

// 尾注接在工具结果后面，只进模型看到的消息；工具摘要和库里的结果不变
func TestToolResultHookNoteGoesToNextRequest(t *testing.T) {
	t.Parallel()
	var events []ToolResultEvent
	var mu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{replies: toolThenText()})
	ran := 0
	h.use(stageRecall(t, &ran))
	h.use(stagePlugin{id: "u", result: func(_ context.Context, e ToolResultEvent) ([]string, error) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
		return []string{"[用户钩子：gofmt]\na.go"}, nil
	}})
	r := h.send("我上次说什么来着")
	if len(events) != 1 || events[0].Name != "recall_raw" || events[0].Text != "原话：周三看牙医" || events[0].Audience.Channel != "console" || events[0].TurnID != r.TurnID {
		t.Fatalf("%+v", events)
	}
	body := h.body(1)
	if !strings.Contains(body, "原话：周三看牙医") || !strings.Contains(body, "[用户钩子：gofmt]") {
		t.Fatalf("尾注要进下一次请求：%s", body)
	}
	if !strings.Contains(strings.ReplaceAll(body, `\n`, "\n"), "周三看牙医\n\n\n[用户钩子") {
		t.Fatalf("尾注接在结果后面，隔两个空行：%s", body)
	}
	if len(r.Tools) != 1 || strings.Contains(r.Tools[0].Result, "用户钩子") {
		t.Fatalf("工具摘要里不该有尾注：%+v", r.Tools)
	}
}

// 钩子出错、慌了、返回空：当没意见，工具结果原样，回合照常
func TestToolResultHookFailureIsHarmless(t *testing.T) {
	t.Parallel()
	for name, fn := range map[string]ToolResultFn{
		"返回错误": func(context.Context, ToolResultEvent) ([]string, error) {
			return []string{"不该用"}, errors.New("炸了")
		},
		"慌了":   func(context.Context, ToolResultEvent) ([]string, error) { panic("boom") },
		"没有尾注": func(context.Context, ToolResultEvent) ([]string, error) { return nil, nil },
	} {
		h := newHarnessOpts(t, harnessOpts{replies: toolThenText()})
		ran := 0
		h.use(stageRecall(t, &ran))
		h.use(stagePlugin{id: "u", result: fn})
		if r := h.send("看看"); r.Reply == "" || len(r.Tools) != 1 || !r.Tools[0].OK {
			t.Fatalf("%s：回合要照常：%+v", name, r)
		}
		if strings.Contains(h.body(1), "不该用") {
			t.Fatalf("%s：出错时的尾注不能用", name)
		}
	}
}

// QQ 群里的回合：钩子零调用（这道门在内核，插件摘不掉）
func TestStageHooksNotCalledOnQQ(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"牙医"}`) + `,` + textReply + `]`})
	ran := 0
	h.use(stageRecall(t, &ran))
	calls := 0
	h.use(stagePlugin{id: "u", max: 3,
		result: func(context.Context, ToolResultEvent) ([]string, error) { calls++; return []string{"不该有"}, nil },
		stop: func(context.Context, StopEvent) (StopDecision, error) {
			calls++
			return StopDecision{Continue: true, Reason: "不该有"}, nil
		}})
	wait(t, h.inGroup("还记得吗", []string{"scene-group", "platform-qq"}))
	if ran != 1 {
		t.Fatalf("工具要真跑：%d", ran)
	}
	if calls != 0 || strings.Contains(h.body(1), "不该有") {
		t.Fatalf("QQ 场合钩子被调了 %d 次", calls)
	}
}

// 没有注册钩子：时间线不多一段，请求次数和以前一样
func TestNoHookNoSpan(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: toolThenText(), mutate: func(c *Config) { c.Now = time.Now }})
	ran := 0
	h.use(stageRecall(t, &ran))
	h.send("看看")
	for _, r := range h.spans(t) {
		if strings.HasPrefix(r.kind, "hook.") {
			t.Fatalf("没钩子也记了：%+v", r)
		}
	}
	if _, ok := spanOf(h.spans(t), "tool.exec"); !ok {
		t.Fatal("时间线本身要在（否则上面的检查什么也没证明）")
	}
}

// 结束前：钩子说继续，反馈追加到末尾再请求一次；不写聊天库；第二次起 Continued 记数
func TestStopHookContinuesWithFeedback(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	var seen []StopEvent
	h.use(stagePlugin{id: "u", max: 3, stop: func(_ context.Context, e StopEvent) (StopDecision, error) {
		seen = append(seen, e)
		if len(seen) == 1 {
			return StopDecision{Continue: true, Reason: "[用户钩子 Stop：go vet]\nvet 报错：a.go:1"}, nil
		}
		return StopDecision{}, nil
	}})
	r := h.send("改一下")
	if len(seen) != 2 || seen[0].Continued != 0 || seen[1].Continued != 1 || seen[0].Last || seen[0].Answer == "" {
		t.Fatalf("%+v", seen)
	}
	last := h.messages(1)
	tail := last[len(last)-1]
	if !strings.Contains(tail, "vet 报错：a.go:1") || !strings.Contains(tail, "不是对方说的话") || !strings.Contains(tail, "第 1 次，最多 3 次") {
		t.Fatalf("反馈要追加在末尾：%s", tail)
	}
	if h.ownerSaid(consoleConv(h.c.CharacterID), "vet 报错") != 0 {
		t.Fatal("反馈不是主人说的话，不能写聊天库")
	}
	if r.Stopped {
		t.Fatal("续跑后正常结束")
	}
}

// 防死循环：钩子永远说继续，续跑次数由内核数；到了上限还问一次（Last 为真）留记录，但这一轮结束
func TestStopHookLimit(t *testing.T) {
	t.Parallel()
	for _, max := range []int{0, 1, 3} {
		replies := `[` + strings.Repeat(textReply+`,`, 8) + textReply + `]`
		h := newHarnessOpts(t, harnessOpts{replies: replies})
		var mu sync.Mutex
		var seen []StopEvent
		h.use(stagePlugin{id: "u", max: max, stop: func(_ context.Context, e StopEvent) (StopDecision, error) {
			mu.Lock()
			seen = append(seen, e)
			mu.Unlock()
			return StopDecision{Continue: true, Reason: "还是不行"}, nil
		}})
		r := h.send("改一下")
		if len(seen) != max+1 {
			t.Fatalf("max=%d：钩子被问了 %d 次，要 %d 次（续跑 %d 次加上限那一次留记录）", max, len(seen), max+1, max)
		}
		for i, e := range seen {
			if e.Continued != i || e.Last != (i == max) {
				t.Fatalf("max=%d 第 %d 次：Continued=%d Last=%v", max, i, e.Continued, e.Last)
			}
		}
		if n := len(h.messages(max)); n == 0 || r.Reply == "" {
			t.Fatalf("max=%d：这一轮要照常结束并有回复", max)
		}
		// 到了上限的那次说继续也不会再请求：请求体数量就是 max+1
		h.mu.Lock()
		got := len(h.bodies)
		h.mu.Unlock()
		if got != max+1 {
			t.Fatalf("max=%d：请求体 %d 个", max, got)
		}
	}
}

// Halt 压过别的钩子的继续；钩子出错当没意见
func TestStopHookHaltAndError(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	h.use(stagePlugin{id: "a", max: 3, stop: func(context.Context, StopEvent) (StopDecision, error) {
		return StopDecision{Continue: true, Reason: "继续"}, nil
	}})
	h.use(stagePlugin{id: "b", max: 3, stop: func(context.Context, StopEvent) (StopDecision, error) { return StopDecision{Halt: true}, nil }})
	h.use(stagePlugin{id: "c", max: 3, stop: func(context.Context, StopEvent) (StopDecision, error) { return StopDecision{}, errors.New("炸了") }})
	h.send("改一下")
	h.mu.Lock()
	n := len(h.bodies)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("Halt 之后不该再请求：%d", n)
	}
	h2 := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	h2.use(stagePlugin{id: "c", max: 3, stop: func(context.Context, StopEvent) (StopDecision, error) { panic("boom") }})
	if r := h2.send("改一下"); r.Reply == "" {
		t.Fatal("钩子慌了回合要照常")
	}
}

// 没注册结束前钩子的回合：一次请求，和以前一样
func TestNoStopHookNoExtraRequest(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	h.send("你好")
	h.mu.Lock()
	n := len(h.bodies)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("请求 %d 次", n)
	}
}

// 结束前钩子拿到这一轮调过的工具（成功与否、参数）
func TestStopHookSeesToolFacts(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: toolThenText()})
	ran := 0
	h.use(stageRecall(t, &ran))
	var got StopEvent
	h.use(stagePlugin{id: "u", max: 1, stop: func(_ context.Context, e StopEvent) (StopDecision, error) { got = e; return StopDecision{}, nil }})
	h.send("看看")
	if len(got.Tools) != 1 || got.Tools[0].Name != "recall_raw" || !got.Tools[0].OK || got.Tools[0].Args.Obj["keywords"].Str != "牙医" {
		t.Fatalf("%+v", got.Tools)
	}
}

// 子运行（子代理）和 QQ 都不是"本地回合"：门在内核，钩子插件摘不掉
func TestStageHooksLocalGate(t *testing.T) {
	t.Parallel()
	console := audience.Audience{Channel: "console", PrivateOK: true}
	for name, c := range map[string]struct {
		in   Input
		want bool
	}{
		"本地私聊":  {Input{Audience: console}, true},
		"本地群聊":  {Input{Audience: audience.Audience{Channel: "console", Scene: "group", PrivateOK: true}}, true},
		"QQ 私聊": {Input{Audience: audience.Audience{Channel: "qq", Scene: "private", PrivateOK: true}}, false},
		"QQ 群":  {Input{Audience: audience.Audience{Channel: "qq", Scene: "group"}}, false},
		"子运行":   {Input{Audience: console, Identity: kernel.Invocation{Kind: kernel.KindChild}}, false},
		"空渠道":   {Input{}, false},
	} {
		if got := c.in.local(); got != c.want {
			t.Errorf("%s：local()=%v", name, got)
		}
	}
}

// 工具后钩子跑的时候来源失效或回合被停：受控资料按钩子跑完后的状态判，不接纳（旧写法钩子夹在判定和接纳之间，会照常接纳）
func TestToolResultHookRunsBeforeAcceptDecision(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"revoked-during-hook", "stopped-during-hook", "accepted-with-note"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"编造资料"}`) + `,` + textReply + `]`})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var revoked atomic.Bool
			var disposition countedDisposition
			h.use(toolPlugin{"controlled-source", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
				return tools.Result{Text: "made-controlled-source-proof", Controlled: true, Acceptable: func() bool { return !revoked.Load() }, Disposition: &disposition}, nil
			}}}})
			h.use(stagePlugin{id: "u", result: func(context.Context, ToolResultEvent) ([]string, error) {
				switch mode {
				case "revoked-during-hook":
					revoked.Store(true)
				case "stopped-during-hook":
					cancel()
				}
				return []string{"[用户钩子：尾注]"}, nil
			}})
			out := h.hub.RespondContext(ctx, consoleInput(h.c.CharacterID, 0, "取编造资料"))
			receive(t, out)
			if mode == "accepted-with-note" {
				if disposition.accepted.Load() != 1 || disposition.discarded.Load() != 0 {
					t.Fatalf("没出事就要接纳 accepted=%d discarded=%d", disposition.accepted.Load(), disposition.discarded.Load())
				}
				if b := h.body(1); !strings.Contains(b, "made-controlled-source-proof") || strings.Contains(b, "用户钩子") {
					t.Fatalf("受控资料后面不接尾注（会让 slim 按后缀估字数对不上）：%s", b)
				}
				return
			}
			if disposition.accepted.Load() != 0 || disposition.discarded.Load() != 1 {
				t.Fatalf("钩子期间出事就不能接纳 accepted=%d discarded=%d", disposition.accepted.Load(), disposition.discarded.Load())
			}
			if h.bodyCount() > 1 && strings.Contains(h.body(1), "made-controlled-source-proof") {
				t.Fatal("没接纳的资料进了下一次请求")
			}
		})
	}
}
