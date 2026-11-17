package memplugin

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

// 把屏障放在真实记忆钩子返回后、内核接纳前；不改记忆召回或接纳逻辑。
type contextFence struct {
	setup func(*turn.Registry)
}

func (contextFence) ID() string { return "context-fence" }
func (p contextFence) Setup(r *turn.Registry) (func(), error) {
	p.setup(r)
	return nil, nil
}

func awaitContext[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("5 秒没等到：%s", what)
		var zero T
		return zero
	}
}

func sendContext(t *testing.T, k *kernel, text string) {
	t.Helper()
	if o := awaitContext(t, k.hub.Send(character, text), "回合结束"); o.Err != nil {
		t.Fatal(o.Err)
	}
}

// 旧贡献超时不接纳；Outcome 等它真实退出，新轮或插话仍只能接纳同一条记忆一次。
func TestLateContextAndNewAcceptanceGiveMemoryOnce(t *testing.T) {
	t.Parallel()
	for _, steer := range []bool{false, true} {
		t.Run(map[bool]string{false: "下一轮", true: "开头和插话"}[steer], func(t *testing.T) {
			t.Parallel()
			k := newKernel(t)
			lf, _ := newLife(t, only(openMemory(t, fixedNow, standardFacts(), nil)), nil)
			ready := make(chan context.Context, 1)
			release, returned := make(chan struct{}), make(chan struct{})
			var free sync.Once
			t.Cleanup(func() { free.Do(func() { close(release) }) })
			calls := 0
			modelStarted := make(chan struct{}, 1)
			k.hub.Cfg.OnDelta = func(string) {
				select {
				case modelStarted <- struct{}{}:
				default:
				}
			}
			err := k.hub.Use(contextFence{setup: func(r *turn.Registry) {
				r.OnContext(lf.inputContext)
				r.OnContext(func(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
					c, err := lf.context(ctx, in)
					calls++
					if calls == 1 {
						if c == nil || count(c.Lines, textGuitar) != 1 {
							t.Error("旧调用没有先算出待接纳的记忆")
						}
						if steer {
							k.hub.Steer(character, "吉他练得怎么样了")
						}
						ready <- ctx
						<-ctx.Done()
						<-release
						close(returned)
					}
					return c, err
				})
			}})
			if err != nil {
				t.Fatal(err)
			}
			out := k.hub.Send(character, "吉他练得怎么样了")
			// 正常召回沿用夹具的预算；旧调用才故意等到超时，不让后续查库抢 50 毫秒。
			oldCtx := awaitContext(t, ready, "旧调用返回前屏障")
			<-oldCtx.Done()
			awaitContext(t, modelStarted, "超时贡献未接纳，模型仍可执行")
			select {
			case <-out:
				t.Fatal("旧回调尚未真实退出，Outcome 不得声称排空")
			default:
			}
			free.Do(func() { close(release) })
			awaitContext(t, returned, "旧结果实际退出")
			if o := awaitContext(t, out, "真实退出后交回"); o.Err != nil {
				t.Fatal(o.Err)
			}
			if !steer {
				sendContext(t, k, "吉他练得怎么样了")
			}
			last := k.requests() - 1
			if n, _ := inMessages(k.request(last), textGuitar); n != 1 {
				t.Fatalf("新贡献应接纳同一条记忆一次：%d", n)
			}
			sendContext(t, k, "再说说吉他")
			if n, _ := inMessages(k.request(last+1), textGuitar); n != 1 {
				t.Fatalf("迟到后不应重复接纳：%d", n)
			}
		})
	}
}

func TestInputTextWaitsForAcceptance(t *testing.T) {
	t.Parallel()
	lf, _ := newLife(t, only(openMemory(t, fixedNow, standardFacts(), nil)), nil)
	st := map[string]any{}
	for _, text := range []string{"吉他练得怎么样了", "今天说点别的"} {
		x := in(character, consoleAud, text, st)
		s := lf.stateOf(st)
		_, before := s.snapshot()
		c, err := lf.context(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		input, err := lf.inputContext(context.Background(), x)
		if err != nil || input == nil || len(input.Lines) != 0 || input.OnAccepted == nil {
			t.Fatalf("应返回无 Lines 的输入贡献：%+v %v", input, err)
		}
		if _, after := s.snapshot(); !slices.Equal(before, after) {
			t.Fatalf("输入未接纳就记了最近的话：%v → %v", before, after)
		}
		input.OnAccepted()
		if _, after := s.snapshot(); len(after) == 0 || after[len(after)-1] != text {
			t.Fatalf("接纳后应记这句话：%v", after)
		}
		if c != nil {
			c.OnAccepted()
		}
	}
}

func TestCancelledInputStillUpdatesRecent(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	e := &fakeEmbed{}
	k.enable(openMemory(t, fixedNow, standardFacts(), e))
	t.Cleanup(func() { k.hub.StopPlugin(ID) })
	ready := make(chan struct{})
	var once sync.Once
	if err := k.hub.Use(contextFence{setup: func(r *turn.Registry) {
		r.OnContext(func(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
			if in.Text == "吉他练得怎么样了" {
				once.Do(func() { close(ready) })
				<-ctx.Done()
			}
			return nil, nil
		})
	}}); err != nil {
		t.Fatal(err)
	}
	out := k.hub.Send(character, "吉他练得怎么样了")
	awaitContext(t, ready, "记忆返回、追加之前")
	stopped := make(chan bool, 1)
	go func() { stopped <- k.hub.Stop(character) }()
	awaitContext(t, out, "取消回合退出")
	if !awaitContext(t, stopped, "停止完成") {
		t.Fatal("没有停下回合")
	}
	k.hub.StopPlugin("context-fence")
	e.reset()
	sendContext(t, k, "接着说吉他")
	want := []string{"接着说吉他", "吉他练得怎么样了\n接着说吉他"}
	if got := e.asked(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("取消后仍进入实例的输入必须参与下一轮召回：%q，应 %q", got, want)
	}
	if n, _ := inMessages(k.request(0), textGuitar); n != 1 {
		t.Fatalf("取消前资料没接纳，下一轮必须重新给一次：%d", n)
	}
}

func TestOverflowKeepsAcceptedMemory(t *testing.T) {
	t.Parallel()
	tooLarge := llmtest.Reply{Finish: "stop"}
	tooLarge.Usage = json.RawMessage(`{"prompt_tokens":100001,"completion_tokens":5,"total_tokens":100006}`)
	k := turntest.New(t, turntest.Options{Replies: []llmtest.Reply{
		tooLarge,
		llmtest.Text("主人这几天在准备考试，每天复习到很晚，答应了周末一起去看电影。"),
		llmtest.Text("恢复了"), llmtest.Text("接着说"),
	}, Mutate: func(c *turn.Config) { c.Context.KeepTokens = 50 }})
	conv := "console:" + character
	for i := range 8 {
		for _, who := range []string{"owner", "character"} {
			k.Append(conv, who, who, strings.Repeat(string(rune('甲'+i)), 300))
		}
	}
	p := New(Options{Memories: only(openMemory(t, fixedNow, standardFacts(), nil)), Now: func() time.Time { return fixedNow }})
	if err := k.Hub.Use(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Hub.StopPlugin(ID) })
	var ids []string
	if err := k.Hub.Use(contextFence{setup: func(r *turn.Registry) {
		r.OnContext(func(_ context.Context, in turn.ContextIn) (*turn.Contribution, error) {
			ids = append(ids, in.ContextID)
			return nil, nil
		})
	}}); err != nil {
		t.Fatal(err)
	}
	k.Send("吉他练得怎么样了")
	k.Send("再说说吉他")
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("溢出恢复必须换 ContextID：%v", ids)
	}
	text := ""
	for _, m := range k.Messages(3) {
		text += m.Text + "\n"
	}
	for _, memory := range []string{textGuitar, pinnedOwner, pinnedPublic} {
		if n := strings.Count(text, memory); n != 1 {
			t.Errorf("溢出恢复后 %q 应只保留一份，实际 %d", memory, n)
		}
	}
}
