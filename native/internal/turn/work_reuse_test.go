package turn

// 开启 Work 后每个新输入有新的 RootID。换根时实例要接着用（消息原样，请求前缀不断，ADR-022），
// 只换属于 root 的工作状态；该重建的情形（压缩、闲置、换模型、Scope 变、工具对不上）照旧重建。

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/tools"
)

// reuseClock 是会往前走的时钟：每次读都多走 1 毫秒，另外测试可以整段拨快
type reuseClock struct {
	base   time.Time
	ticks  atomic.Int64
	offset atomic.Int64
}

func (c *reuseClock) Now() time.Time {
	return c.base.Add(time.Duration(c.offset.Load()) + time.Duration(c.ticks.Add(1))*time.Millisecond)
}
func (c *reuseClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

const reuseToolText = "made-tool-output-"

func newReuseHarness(t *testing.T, replies string, extra func(*Config)) (*harness, *reuseClock) {
	t.Helper()
	clock := &reuseClock{}
	h := newHarnessOpts(t, harnessOpts{replies: replies, mutate: func(cfg *Config) {
		workLimits(cfg)
		clock.base = cfg.Now()
		cfg.Now = clock.Now
		cfg.ExecTool = func(_ context.Context, _, name string, _ jsjson.Value) (tools.Result, error) {
			return tools.Result{Text: reuseToolText + name}, nil
		}
		if extra != nil {
			extra(cfg)
		}
	}})
	return h, clock
}

func rootInput(h *harness, text, root string) Input {
	in := workInput(h, text)
	in.Identity.RootID = root
	return in
}

func (h *harness) bodyCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func (h *harness) liveFor(in Input) *Live {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	return h.hub.live[liveKey(in)]
}

// assertChain：从第 from 次请求起，每一次请求的 messages 都以前一次的全部 messages 开头（逐字节）
func assertChain(t *testing.T, h *harness, from int) {
	t.Helper()
	n := h.bodyCount()
	if n < from+2 {
		t.Fatalf("请求只有 %d 次，没法比较", n)
	}
	for i := from + 1; i < n; i++ {
		prev, next := h.messages(i-1), h.messages(i)
		if !isPrefix(prev, next) {
			k := 0
			for k < len(prev) && k < len(next) && prev[k] == next[k] {
				k++
			}
			t.Fatalf("第 %d 次请求没有以第 %d 次开头，从第 %d 条分叉（前 %d 条、后 %d 条）", i+1, i, k, len(prev), len(next))
		}
	}
}

func respondAll(t *testing.T, h *harness, ins ...Input) {
	t.Helper()
	for _, in := range ins {
		wait(t, h.hub.Respond(in))
	}
}

func replyList(parts ...string) string { return "[" + strings.Join(parts, ",") + "]" }

// 两轮、三轮连续请求（含工具调用，每轮一个新 RootID，时钟每轮拨快 20 分钟）：后一次请求以前一次的全部 messages 开头，
// 实例没有换，每个 root 在库里有自己的工作记录
func TestWorkRootSwitchKeepsPrefix(t *testing.T) {
	t.Parallel()
	h, clock := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), textReply, callReply("read", `{"path":"made2.txt"}`), textReply, textReply), nil)
	a := rootInput(h, "第一轮：看一下 made.txt", "")
	b := rootInput(h, "第二轮：再看 made2.txt", "")
	c := rootInput(h, "第三轮：随便聊一句", "")
	respondAll(t, h, a)
	first := h.liveFor(a)
	clock.advance(20 * time.Minute)
	respondAll(t, h, b)
	if h.liveFor(b) != first {
		t.Fatal("换根后换了实例")
	}
	assertChain(t, h, 0)
	two := h.bodyCount()
	clock.advance(20 * time.Minute)
	respondAll(t, h, c)
	if h.liveFor(c) != first {
		t.Fatal("第三个 root 又换了实例")
	}
	assertChain(t, h, 0)
	if h.bodyCount() <= two {
		t.Fatal("第三轮没有发请求")
	}
	// 20 分钟的间隔让每一轮都多一块报时参考信息：它接在末尾，旧的原样留着
	if got := strings.Count(h.body(h.bodyCount()-1), "现在是"); got < 3 {
		t.Fatalf("旧参考块没有留在历史里：%d 块", got)
	}
	var roots int
	if err := h.st.Trace.QueryRow(`SELECT count(*) FROM work_roots`).Scan(&roots); err != nil || roots != 3 {
		t.Fatalf("每个输入一个 root：%d %v", roots, err)
	}
}

// 换根时工作状态不串：旧 root 的目标、计划不会被新 root 当成自己的；实例上的工作状态属于新 root
func TestWorkRootSwitchIsolatesWorkState(t *testing.T) {
	t.Parallel()
	todo := callReply("todo_write", `{"todos":[{"content":"made-pending-a","status":"pending"}]}`)
	h, _ := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), todo, textReply, textReply), nil)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	respondAll(t, h, a, b)
	p := h.hub.Cfg.Work.(*workPort)
	sa, _, err := p.Load(context.Background(), a.Identity)
	if err != nil {
		t.Fatal(err)
	}
	sb, msgsB, err := p.Load(context.Background(), b.Identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgsB {
		if m.Role == "" {
			t.Fatal("导入旧计划清单的条目不该变成一条空消息放进历史")
		}
	}
	if sa.Goal != "made-goal-a" || len(sa.Pending) != 1 || sa.Pending[0] != "made-pending-a" || len(sa.Evidence) == 0 {
		t.Fatalf("root A 的状态不对：%+v", sa)
	}
	// 计划清单是整个会话一份（旧 todo 表），新 root 开出来时会照旧导入，所以 B 的 Pending 和 A 一样是会话计划；
	// 目标、证据、root 号是 B 自己的，A 的工具证据不会出现在 B 里
	if sb.Goal != "made-goal-b" || sb.RootID != "made-root-b" || len(sb.Evidence) != 0 {
		t.Fatalf("root B 读到了 A 的状态：%+v", sb)
	}
	if l := h.liveFor(b); l.workState.RootID != "made-root-b" || len(l.workState.Evidence) != 0 || l.workState.Goal == "made-goal-a" {
		t.Fatalf("实例上的工作状态还是旧 root 的：%+v", l.workState)
	}
	assertChain(t, h, 0)
}

// result_read 的权限不因复用放宽：仍然只认同一个 Scope 自己的结果，别的场合、别的角色、别的发言人都读不到
func TestWorkRootSwitchResultReadScope(t *testing.T) {
	t.Parallel()
	h, _ := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), textReply, textReply), nil)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	respondAll(t, h, a, b)
	var id string
	if err := h.st.Trace.QueryRow(`SELECT id FROM result_refs`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	read := func(scope kernel.Scope) tools.Result {
		args, _ := jsjson.Parse(fmt.Sprintf(`{"id":%q,"offset":0,"limit":2048}`, id))
		res, err := readResult(context.Background(), h.hub.Cfg.Results, scope, args)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := read(b.Identity.Scope); res.IsError || !strings.Contains(res.Text, reuseToolText) {
		t.Fatalf("同一个 Scope 在新 root 里应该能续读上一个 root 的结果：%+v", res)
	}
	for _, tc := range []struct{ name, channel, scene, conv, character, actorKind, actorID string }{
		{"本地私聊另角色", "console", "private", a.Conversation, "made-other", "owner", "made-owner"},
		{"本地群聊", "console", "group", "console:group:made-g", a.CharacterID, "owner", "made-owner"},
		{"QQ私聊", "qq", "private", "qq:private:made-bot:made-user", a.CharacterID, "owner", "made-owner"},
		{"QQ群", "qq", "group", "qq:group:made-bot:made-g", a.CharacterID, "owner", "made-owner"},
		{"同会话别的发言人", "console", "private", a.Conversation, a.CharacterID, "member", "made-member"},
	} {
		scope := b.Identity.Scope
		scope.Channel, scope.Scene, scope.Conversation, scope.CharacterID, scope.ActorKind, scope.ActorID = tc.channel, tc.scene, tc.conv, tc.character, tc.actorKind, tc.actorID
		if tc.channel == "qq" {
			scope.Platform = "qq"
		}
		if res := read(scope); !res.IsError || strings.Contains(res.Text, reuseToolText) {
			t.Fatalf("%s 读到了别人的工具结果：%+v", tc.name, res)
		}
	}
}

// 换根时上一轮被停止的提醒照样带到下一轮，只说一次，接在末尾
func TestWorkRootSwitchStopReminder(t *testing.T) {
	t.Parallel()
	h, _ := newReuseHarness(t, replyList(textReply, textReply, textReply), nil)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	c := rootInput(h, "made-goal-c", "made-root-c")
	respondAll(t, h, a)
	l := h.liveFor(a)
	half := "made-half-sentence"
	l.mu.Lock()
	l.stopped = &half
	l.mu.Unlock()
	respondAll(t, h, b)
	if !strings.Contains(h.body(1), "上一轮被所有者手动停止了") || !strings.Contains(h.body(1), half) {
		t.Fatal("停止提醒没有带到换根后的第一个请求")
	}
	respondAll(t, h, c)
	if n := strings.Count(h.body(2), "上一轮被所有者手动停止了"); n != 1 {
		t.Fatalf("停止提醒应该只说一次，现在 %d 次", n)
	}
	assertChain(t, h, 0)
}

// 主人发过的图：换根后仍在原来那条消息上，只有一份；闲置重建后从事件取回
func TestWorkRootSwitchImages(t *testing.T) {
	t.Parallel()
	const payload = "data:image/png;base64,uniqueworkimage"
	h, clock := newReuseHarness(t, replyList(textReply, textReply, textReply), func(c *Config) {
		c.IdleAfter = 30 * time.Minute
		c.PrepareImages = func(_ context.Context, in Input, _ llm.Model) (string, []llm.Image) {
			if len(in.Parts) > 0 {
				return in.Text, []llm.Image{{DataURL: payload}}
			}
			return in.Text, nil
		}
		c.RestoreImages = func(_ context.Context, _ Input, seq int64, _ llm.Model) []llm.Image {
			if seq == 1 {
				return []llm.Image{{DataURL: payload}}
			}
			return nil
		}
	})
	a := rootInput(h, "made-first-image", "made-root-a")
	a.Parts = []history.Part{{Text: "made-first-image", Attachments: []history.Attachment{{Kind: "image", Hash: strings.Repeat("a", 64)}}}}
	b := rootInput(h, "made-second", "made-root-b")
	c := rootInput(h, "made-third", "made-root-c")
	respondAll(t, h, a)
	if !strings.Contains(h.body(0), payload) {
		t.Fatal("第一轮请求没有带图")
	}
	respondAll(t, h, b)
	if strings.Count(h.body(1), payload) != 1 || strings.Count(h.body(1), "made-first-image") != 1 {
		t.Fatal("换根后图不在原来那条消息上，或者重复了")
	}
	assertChain(t, h, 0)
	clock.advance(31 * time.Minute)
	respondAll(t, h, c)
	if strings.Count(h.body(2), payload) != 1 || strings.Count(h.body(2), "made-first-image") != 1 {
		t.Fatal("闲置重建后图没有取回，或者重复了")
	}
}

// 压缩后重建：新快照出来以后下一轮换新实例、只带摘要和后面的话；之后接着复用，前缀又连上
func TestWorkRootSwitchCompactRebuild(t *testing.T) {
	t.Parallel()
	h, _ := newReuseHarness(t, replyList(textReply, textReply, summaryReply, textReply, textReply), nil)
	h.seedRounds(8)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	c := rootInput(h, "made-goal-c", "made-root-c")
	d := rootInput(h, "made-goal-d", "made-root-d")
	respondAll(t, h, a, b)
	old := h.liveFor(a)
	h.setHard(1000000)
	if r, err := h.hub.Compact(a.CharacterID, a.Conversation, "manual", 0); err != nil || !r.OK {
		t.Fatalf("压缩没成：%+v %v", r, err)
	}
	next := h.bodyCount()
	respondAll(t, h, c)
	fresh := h.liveFor(c)
	if fresh == old {
		t.Fatal("有了新快照还接着用旧实例")
	}
	if !strings.Contains(h.body(next), "前面的聊天已经整理成下面的摘要") || fresh.workState.RootID != "made-root-c" {
		t.Fatalf("重建后没有换上摘要或工作状态不是新 root 的：%+v", fresh.workState)
	}
	respondAll(t, h, d)
	if h.liveFor(d) != fresh {
		t.Fatal("重建后下一个 root 又换了实例")
	}
	assertChain(t, h, next)
}

// 闲置超时和换模型：照旧重建；重建后工作状态是新 root 的，之后接着复用
func TestWorkRootSwitchIdleAndModelRebuild(t *testing.T) {
	t.Parallel()
	h, clock := newReuseHarness(t, replyList(textReply, textReply, textReply, textReply), func(c *Config) { c.IdleAfter = 30 * time.Minute })
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	c := rootInput(h, "made-goal-c", "made-root-c")
	d := rootInput(h, "made-goal-d", "made-root-d")
	respondAll(t, h, a)
	first := h.liveFor(a)
	clock.advance(31 * time.Minute)
	respondAll(t, h, b)
	second := h.liveFor(b)
	if second == first || second.workState.RootID != "made-root-b" {
		t.Fatalf("闲置超时没有重建：%t %+v", second != first, second.workState)
	}
	respondAll(t, h, c)
	if h.liveFor(c) != second {
		t.Fatal("重建后下一个 root 没有接着用")
	}
	assertChain(t, h, 1)
	other := h.hub.Models[0]
	other.ID = "test/think-other"
	h.hub.Models = append(h.hub.Models, other)
	h.hub.Cfg.Models[a.CharacterID] = other.ID
	respondAll(t, h, d)
	third := h.liveFor(d)
	if third == second || third.model != other.ID || third.workState.RootID != "made-root-d" {
		t.Fatalf("换模型没有重建：%t %s %+v", third != second, third.model, third.workState)
	}
}

// 实例里的工具调用和结果对不上：换根时也先重建（保护照常生效），请求里没有悬空的结果
func TestWorkRootSwitchToolPairGuard(t *testing.T) {
	t.Parallel()
	h, _ := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), textReply, textReply), nil)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	respondAll(t, h, a)
	l := h.liveFor(a)
	kept := l.msgs[:0]
	for _, m := range l.msgs {
		if m.Role != "toolResult" {
			kept = append(kept, m)
		}
	}
	l.msgs = kept
	if !toolPairProblems(l.msgs) {
		t.Fatal("夹具没造出对不上的工具调用")
	}
	respondAll(t, h, b)
	if toolPairProblems(h.liveFor(b).msgs) || h.liveFor(b).workState.RootID != "made-root-b" {
		t.Fatal("对不上的工具调用没有在换根时修好")
	}
}

// 回到已经有工作记录的旧 root：不能把它的条目追加到末尾，整个重建，该 root 的工作材料从库里接回
func TestWorkRootSwitchBackToOldRootRebuilds(t *testing.T) {
	t.Parallel()
	h, _ := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), textReply, textReply, textReply), nil)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	a2 := rootInput(h, "made-goal-a-again", "made-root-a")
	respondAll(t, h, a, b)
	next := h.bodyCount()
	respondAll(t, h, a2)
	l := h.liveFor(a2)
	if l.workState.RootID != "made-root-a" || toolPairProblems(l.msgs) {
		t.Fatalf("回到旧 root 后工作状态不对：%+v", l.workState)
	}
	if !strings.Contains(h.body(next), reuseToolText+"read") {
		t.Fatal("旧 root 的工具证据没有从库里接回")
	}
}

// 估算算上保留下来的工具结果和旧参考块；超了软门槛旧工具结果换成引用占位，超了硬门槛压缩后重建
func TestWorkRootSwitchGatesCountRetained(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("甲", 900)
	h, clock := newReuseHarness(t, replyList(callReply("read", `{"path":"made.txt"}`), textReply, textReply, textReply, summaryReply, textReply), func(c *Config) {
		c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
			return tools.Result{Text: big}, nil
		}
	})
	h.seedRounds(8)
	a := rootInput(h, "made-goal-a", "made-root-a")
	b := rootInput(h, "made-goal-b", "made-root-b")
	c := rootInput(h, "made-goal-c", "made-root-c")
	d := rootInput(h, "made-goal-d", "made-root-d")
	respondAll(t, h, a)
	clock.advance(20 * time.Minute)
	respondAll(t, h, b)
	l := h.liveFor(b)
	var tool, ctx int
	var rest []Msg
	for _, m := range l.msgs {
		switch m.Kind {
		case KindTool:
			tool++
		case KindContext:
			ctx++
		}
		if m.Kind != KindTool && m.Kind != KindContext {
			rest = append(rest, m)
		}
	}
	if tool == 0 || ctx < 2 {
		t.Fatalf("实例里该保留工具结果和旧参考块：工具 %d 参考 %d", tool, ctx)
	}
	if conversationTokens(l.msgs)-conversationTokens(rest) < sumikaTokens(big) {
		t.Fatal("估算没有算上保留下来的工具结果")
	}
	// 软门槛：只改这一次请求，旧工具结果换成引用占位
	size := sizeOf(l)
	h.hub.Cfg.Context = ContextLimits{Window: 1000000, Reserve: 1, HardRatio: 1, SoftRatio: float64(size-100) / 1000000}
	next := h.bodyCount()
	respondAll(t, h, c)
	if body := h.body(next); strings.Contains(body, big) || !strings.Contains(body, "较早的工具结果已省略；用 result_read 读取") {
		t.Fatal("软门槛没有把旧 root 的长工具结果换成引用占位")
	}
	// 硬门槛：先压缩再换新实例
	l = h.liveFor(c)
	h.setHard(sizeOf(l) + 300)
	respondAll(t, h, d)
	if eps := h.epochs(); len(eps) != 1 || eps[0].trigger != "rule" {
		t.Fatalf("硬门槛没有触发压缩：%+v", eps)
	}
	if strings.Contains(h.body(h.bodyCount()-1), big) {
		t.Fatal("压缩后的请求还带着旧工具结果的正文")
	}
}
