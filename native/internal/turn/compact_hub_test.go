package turn

// 规格 8.2 A8（压缩）的 C2a：压缩本身、压缩前钩子、手动入口、下一轮换上新快照。对着写明的预期测

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// 摘要的假回复：二十多个字、不带路径、不说"我是 AI"
const summaryReply = `{"content":["主人这几天在准备考试，每天复习到很晚，答应了周末一起去看电影。"],` + usage + `}`

// seedRounds 往主窗口的会话里存 n 轮（主人一句、她一句，每句 300 个字），返回每轮主人那句的 seq
func (h *harness) seedRounds(n int) []int64 {
	h.t.Helper()
	conv := consoleConv(h.c.CharacterID)
	var owners []int64
	for i := range n {
		for _, who := range []string{"owner", "character"} {
			id := "owner"
			if who == "character" {
				id = h.c.CharacterID
			}
			seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: who, ActorID: id, Origin: who, Kind: "message",
				Text: strings.Repeat(string([]rune("甲乙丙丁戊己庚辛壬癸")[i]), 300), CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
			if err != nil {
				h.t.Fatal(err)
			}
			if who == "owner" {
				owners = append(owners, seq)
			}
		}
	}
	return owners
}

type epochRow struct {
	id, status, trigger, summary, err string
	from                              int64
}

func (h *harness) epochs() []epochRow {
	h.t.Helper()
	rows, err := h.st.Events.Query(`SELECT id, status, trigger, summary, coalesce(error, ''), from_seq FROM context_epochs ORDER BY created_at, rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []epochRow
	for rows.Next() {
		var r epochRow
		rows.Scan(&r.id, &r.status, &r.trigger, &r.summary, &r.err, &r.from)
		out = append(out, r)
	}
	return out
}

func smallKeep(c *Config) { c.Context.KeepTokens = 50 }

// 手动压缩走通：摘要请求和实例同一个前缀、只在末尾加一句整理的请求；写一行成功的快照、一行 stage compact 的模型调用；
// 下一轮换上新快照（摘要在最前、切点那条起的原文接着带），model_runs 记 rebuild compact
func TestCompactManual(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `]`, mutate: smallKeep})
	owners := h.seedRounds(6)
	conv := consoleConv(h.c.CharacterID)
	c, err := h.hub.Compact(h.c.CharacterID, conv, "manual", 0)
	if err != nil || !c.OK || c.EpochID == "" {
		t.Fatalf("压缩应该成功：%+v %v", c, err)
	}
	eps := h.epochs()
	if len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "manual" || eps[0].from != owners[5] || !strings.Contains(eps[0].summary, "看电影") {
		t.Fatalf("快照那一行：%+v", eps)
	}
	compactReq := h.messages(0)
	last := compactReq[len(compactReq)-1]
	if !strings.Contains(last, "整理上下文的请求") || !strings.Contains(last, strings.Repeat("己", 10)) {
		t.Fatalf("摘要请求末尾应该是整理的请求、带着切点那句的开头：%s", last)
	}
	var stage, epochID, ctxJSON string
	var turnID *string
	h.st.Trace.QueryRow(`SELECT stage, epoch_id, context_json, turn_id FROM model_runs ORDER BY rowid LIMIT 1`).Scan(&stage, &epochID, &ctxJSON, &turnID)
	var cj struct {
		Compact struct {
			Trigger       string
			Dropped, Kept int
			Before, After int
		}
	}
	json.Unmarshal([]byte(ctxJSON), &cj)
	if stage != "compact" || epochID != c.EpochID || turnID != nil || cj.Compact.Trigger != "manual" || cj.Compact.Dropped != 10 || cj.Compact.Kept != 2 || cj.Compact.After >= cj.Compact.Before {
		t.Fatalf("模型调用那一行：%s %s %v %s", stage, epochID, turnID, ctxJSON)
	}
	r := h.send("接着说")
	next := h.messages(1)
	// 下一轮：系统提示、摘要、切点那一轮的两句、这一轮的参考信息和这句话
	if !strings.Contains(next[1], "前面的聊天已经整理成下面的摘要") || !strings.Contains(next[1], "看电影") || strings.Contains(strings.Join(next, ""), strings.Repeat("甲", 10)) ||
		!strings.Contains(next[2], strings.Repeat("己", 10)) {
		t.Fatalf("下一轮应该换上新快照：\n%s", strings.Join(next, "\n"))
	}
	var rebuild string
	h.st.Trace.QueryRow(`SELECT rebuild FROM model_runs WHERE stage = 'reply' ORDER BY rowid DESC LIMIT 1`).Scan(&rebuild)
	if rebuild != "compact" || r.Built.EpochID != c.EpochID {
		t.Fatalf("换上新快照的那一轮记 rebuild compact：%q，用的快照 %q", rebuild, r.Built.EpochID)
	}
}

// 两个请求的 messages 从头起有几条一样
func sharedPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// A13：摘要只从她看到的话里来——插件给的参考信息、工具结果都不进摘要请求
func TestCompactSourceSkipsContextAndTools(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["我看看"],"toolCalls":[{"id":"r1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},{"content":["看完了"],` + usage + `},` + textReply + `,` + summaryReply + `]`,
		mutate: func(c *Config) {
			c.Context.KeepTokens = 1 // 这几句都短：只留最近一轮
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				return tools.Result{Text: "工具输出里的秘密"}, nil
			}
		},
	})
	h.use(contextPlugin("secret", func(context.Context, ContextIn) (*Contribution, error) {
		return &Contribution{Lines: []string{"只有主人能看的秘密"}}, nil
	}))
	h.send("列一下")
	h.send("再说一句")
	if _, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0); err != nil {
		t.Fatal(err)
	}
	body := h.body(3)
	if strings.Contains(body, "只有主人能看的秘密") || strings.Contains(body, "工具输出里的秘密") || !strings.Contains(body, "看完了") || !strings.Contains(body, "再说一句") {
		t.Fatalf("摘要请求不该带参考信息和工具结果，要带她说过的话：%s", body)
	}
}

// 自检没过（摘要太短）：记一行失败的，实例照旧（下一轮不重建，历史还是原来那些）
func TestCompactFailedKeepsInstance(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,{"content":["好"],` + usage + `},` + textReply + `]`, mutate: smallKeep})
	h.seedRounds(3)
	h.send("先说一句")
	c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if err != nil || c.OK || c.EpochID == "" || !strings.Contains(c.Err, "太短") {
		t.Fatalf("摘要太短应该记失败：%+v %v", c, err)
	}
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" || !strings.Contains(eps[0].err, "太短") {
		t.Fatalf("失败那一行：%+v", eps)
	}
	// 摘要请求和上一轮的请求同一个前缀（系统提示、历史），只是去掉了那一轮的参考信息：上一轮最后两条是参考信息和那句话
	if turn, comp := h.messages(0), h.messages(1); sharedPrefix(turn, comp) != len(turn)-2 {
		t.Fatalf("摘要请求应该和实例同一个前缀：一样的只有 %d 条（上一轮共 %d 条）", sharedPrefix(turn, comp), len(turn))
	}
	h.send("接着说")
	if !isPrefix(h.messages(0), h.messages(2)) {
		t.Fatal("压缩没成，下一轮接着用原来的实例（前缀照旧）")
	}
}

// 前面只有一轮：没有可以压的，不写库、不请求
func TestCompactNothingToCut(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, mutate: smallKeep})
	h.send("你好")
	c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if err != nil || c.EpochID != "" || !strings.Contains(c.Err, "还不多") || len(h.epochs()) != 0 || len(h.bodies) != 1 {
		t.Fatalf("没有可以压的：%+v %v，请求了 %d 次", c, err, len(h.bodies))
	}
}

// 渠道的会话不压（满了只带最近的话）
func TestCompactChannelRefused(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{})
	if _, err := h.hub.Compact(h.c.CharacterID, groupConv, "manual", 0); err == nil {
		t.Fatal("渠道的会话应该报错")
	}
}

// 压缩前的钩子：给的上限限住切点；超时不等（钩子的 ctx 不跟着取消）、出错照常压、等的时候插件停掉不算；WaitUntil 是开始等 + 时限
func TestCompactBeforeHook(t *testing.T) {
	t.Parallel()
	t.Run("限住切点", func(t *testing.T) {
		h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
		owners := h.seedRounds(6)
		var got BeforeCompact
		h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
			r.OnBeforeCompact(func(_ context.Context, e BeforeCompact) (int64, bool, error) {
				got = e
				return owners[3] - 1, true, nil // 整理到第 4 轮主人那句之前
			})
			return nil, nil
		}})
		start := time.Now()
		h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if eps := h.epochs(); len(eps) != 1 || eps[0].from != owners[3] {
			t.Fatalf("切点不越过整理到的下一条（%d）：%+v", owners[3], eps)
		}
		if got.Trigger != "manual" || got.Conversation != consoleConv(h.c.CharacterID) || got.WaitUntil.Sub(start) < 80*time.Second {
			t.Fatalf("钩子收到的：%+v", got)
		}
	})
	t.Run("超时、出错照常压", func(t *testing.T) {
		h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: func(c *Config) {
			smallKeep(c)
			c.Hooks.Compact = 200 * time.Millisecond
		}})
		owners := h.seedRounds(6)
		hookCtx := make(chan context.Context, 1)
		release := make(chan struct{})
		defer func() {
			close(release)
			ctx, cancel := context.WithTimeout(t.Context(), barrierLimit)
			defer cancel()
			if err := h.hub.Drain(ctx); err != nil {
				t.Fatal(err)
			}
		}()
		h.use(fakePlugin{"slow", func(r *Registry) (func(), error) {
			r.OnBeforeCompact(func(ctx context.Context, e BeforeCompact) (int64, bool, error) {
				hookCtx <- ctx
				<-release
				return 1, true, nil
			})
			return nil, nil
		}})
		h.use(fakePlugin{"broken", func(r *Registry) (func(), error) {
			r.OnBeforeCompact(func(context.Context, BeforeCompact) (int64, bool, error) { return 1, true, context.DeadlineExceeded })
			return nil, nil
		}})
		start := time.Now()
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if took := time.Since(start); !c.OK || took > barrierLimit {
			t.Fatalf("钩子超时不等、出错照常压：%+v，用了 %v", c, took)
		}
		if eps := h.epochs(); eps[0].from != owners[5] {
			t.Fatalf("超时的、出错的都不限制切点：%+v", eps)
		}
		if ctx := <-hookCtx; ctx.Err() != nil {
			t.Fatal("内核不等了，钩子的 ctx 不跟着取消（整理接着做完）")
		}
		if !h.logged("不等了") || !h.logged("broken") {
			t.Fatalf("超时、出错都记日志：%v", h.logs)
		}
	})
	t.Run("等的时候插件停掉", func(t *testing.T) {
		h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
		owners := h.seedRounds(6)
		entered, release := make(chan struct{}), make(chan struct{})
		h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
			r.OnBeforeCompact(func(context.Context, BeforeCompact) (int64, bool, error) {
				close(entered)
				<-release
				return 1, true, nil
			})
			return nil, nil
		}})
		done := make(chan Compacted, 1)
		go func() {
			c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
			done <- c
		}()
		<-entered
		h.hub.StopPlugin("mem")
		close(release)
		<-done
		if eps := h.epochs(); len(eps) != 1 || eps[0].from != owners[5] {
			t.Fatalf("停掉的插件给的上限不算：%+v", eps)
		}
	})
}

// storeQueued 存一句主人的话、记成排着队（Send 存了库、它那一轮还没轮到时的样子），返回 seq
func (h *harness) storeQueued(text string) int64 {
	h.t.Helper()
	seq := h.storeOwner(text)
	h.hub.markQueued(consoleInput(h.c.CharacterID, seq, text))
	return seq
}

func (h *harness) storeOwner(text string) int64 {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: consoleConv(h.c.CharacterID), ActorKind: "owner", ActorID: "owner", Origin: "owner",
		Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return seq
}

// runQueued 跑排着队的那一句（轮到它了）
func (h *harness) runQueued(seq int64, text string) *Result {
	h.t.Helper()
	in := consoleInput(h.c.CharacterID, seq, text)
	h.hub.unmarkQueued(in)
	return wait(h.t, h.hub.Respond(in))
}

// 审查查出：压缩时已经存进库、还排着队的主人话她还没看到——不进摘要、切点不越过它们（连着两句时前一句不会两头都不在）、
// 冷建实例时不读进历史（压缩没成时那一句在它自己那一轮里只出现一次）
func TestCompactSkipsQueued(t *testing.T) {
	t.Parallel()
	t.Run("压缩成功：排着队的两句都留着原文", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`, mutate: smallKeep})
		owners := h.seedRounds(6)
		first := h.storeQueued(strings.Repeat("前", 300))
		second := h.storeQueued("后一句")
		c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if err != nil || !c.OK {
			t.Fatalf("%+v %v", c, err)
		}
		if eps := h.epochs(); eps[0].from != owners[5] || strings.Contains(h.body(0), strings.Repeat("前", 10)) || strings.Contains(h.body(0), "后一句") {
			t.Fatalf("切点不越过她看到的最后一条（%d）、排着队的不进摘要：%+v", owners[5], eps)
		}
		h.runQueued(first, strings.Repeat("前", 300))
		h.runQueued(second, "后一句")
		h.hub.Restart(h.c.CharacterID)
		h.send("重建以后")
		if b := h.body(3); !strings.Contains(b, strings.Repeat("前", 10)) || !strings.Contains(b, "后一句") {
			t.Fatalf("重建以后排过队的两句都还在：%s", b)
		}
	})
	t.Run("存了库、还没记成排队的也不越过", func(t *testing.T) {
		t.Parallel()
		// Send 先存库、后记排队，中间那一下压缩看到的是"库里有、排队里没有"：照样只切在她看到的范围里
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, mutate: smallKeep})
		h.seedRounds(6)
		h.send("先建实例")
		h.storeOwner(strings.Repeat("前", 300))
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		eps := h.epochs()
		if !c.OK || len(eps) != 1 || strings.Contains(h.body(1), strings.Repeat("前", 10)) {
			t.Fatalf("没看到的那句不进摘要：%+v %+v", c, eps)
		}
		var cutText string
		h.st.Events.QueryRow(`SELECT text FROM events WHERE seq = ?`, eps[0].from).Scan(&cutText)
		if cutText != "先建实例" {
			t.Fatalf("切点应该在她看到的最后一轮（先建实例那句），实际切在「%.20s」", cutText)
		}
	})
	t.Run("压缩没成：那一句只出现一次", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["好"],` + usage + `},` + textReply + `]`, mutate: smallKeep})
		h.seedRounds(6)
		q := h.storeQueued("排着队的那句")
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if c.OK {
			t.Fatal("摘要太短，压缩应该没成")
		}
		h.runQueued(q, "排着队的那句")
		if n := strings.Count(h.body(1), "排着队的那句"); n != 1 {
			t.Fatalf("排着队的那句在它自己那一轮里应该只出现一次，实际 %d", n)
		}
	})
}

// 审查查出（规格第七节 A4 一起修）："新话题"分隔之前的不压：分隔之后没有话就不压（不请求、不等插件），
// 有话就只在分隔之后找切点；快照之后、最近几条之外的分隔也让快照不用
func TestCompactContextBreak(t *testing.T) {
	t.Parallel()
	addBreak := func(h *harness) int64 {
		seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: consoleConv(h.c.CharacterID), ActorKind: "system", ActorID: "system", Origin: "system",
			Kind: "notice", Text: "从这里开始新话题", Segments: map[string]any{"notice": history.ContextBreak}, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
		if err != nil {
			h.t.Fatal(err)
		}
		return seq
	}
	t.Run("分隔之后没有话", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
		h.seedRounds(3)
		addBreak(h)
		called := false
		h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
			r.OnBeforeCompact(func(context.Context, BeforeCompact) (int64, bool, error) { called = true; return 0, false, nil })
			return nil, nil
		}})
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if c.EpochID != "" || len(h.bodies) != 0 || called {
			t.Fatalf("分隔之后没有话，不压、不请求、不等插件：%+v，请求了 %d 次，插件调了 %v", c, len(h.bodies), called)
		}
	})
	t.Run("实例里分隔前后都有话", func(t *testing.T) {
		t.Parallel()
		// 实例建好以后才插进分隔（没有重建），接着又说了两句：分隔之后只有这两轮，切在第二句上前面就没东西了——不压。
		// 不管分隔的话，留着的够装下分隔前最近的一轮，会切到分隔之前
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + summaryReply + `]`, mutate: func(c *Config) { c.Context.KeepTokens = 1000 }})
		h.seedRounds(3)
		h.send("先说一句")
		addBreak(h)
		h.send("分隔以后一")
		h.send("分隔以后二")
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		if c.EpochID != "" || len(h.bodies) != 3 {
			t.Fatalf("只在分隔之后找切点（那里前面没东西可压）：%+v，请求了 %d 次", c, len(h.bodies))
		}
	})
	t.Run("快照之后、最近几条之外的分隔", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, mutate: func(c *Config) { c.RecentMessages = 2 }})
		owners := h.seedRounds(2)
		conv := consoleConv(h.c.CharacterID)
		h.st.AddEpoch(store.Epoch{ID: "01EPOCHOLD", Conversation: conv, CharacterID: h.c.CharacterID, FromSeq: owners[0], Summary: "分隔之前的旧摘要，二十多个字，不该再给她看到了。",
			Kind: "chat", Trigger: "manual", Model: "m", Status: "ok", CreatedAt: 1})
		addBreak(h)
		h.seedRounds(3)
		h.send("新话题里说")
		if b := h.body(0); strings.Contains(b, "分隔之前的旧摘要") {
			t.Fatalf("分隔在最近几条之外，快照也不该用：%s", b)
		}
	})
}

// 审查查出：她没说出口的（试运行、看完不说）不算进切点和计数
func TestCompactSkipsNotSaid(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
	h.seedRounds(3)
	conv := consoleConv(h.c.CharacterID)
	for range 3 {
		h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "character", ActorID: h.c.CharacterID, Origin: "automation:self-skip", Kind: "message",
			Text: strings.Repeat("不", 300), CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	}
	h.seedRounds(3)
	h.hub.Compact(h.c.CharacterID, conv, "manual", 0)
	var ctxJSON string
	h.st.Trace.QueryRow(`SELECT context_json FROM model_runs WHERE stage = 'compact'`).Scan(&ctxJSON)
	if !strings.Contains(ctxJSON, `"dropped":10`) || !strings.Contains(ctxJSON, `"kept":2`) {
		t.Fatalf("没说出口的不算：%s", ctxJSON)
	}
}

// 审查查出：前面没有可以压的不等插件；记忆挡住切点、全留着也装得下时，说是记忆挡住的（不说"还不多"），记一行日志
func TestCompactHookOrderAndReason(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
	owners := h.seedRounds(3)
	calls := 0
	h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
		r.OnBeforeCompact(func(context.Context, BeforeCompact) (int64, bool, error) { calls++; return owners[0] - 1, true, nil })
		return nil, nil
	}})
	c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if calls != 1 || c.EpochID != "" || !strings.Contains(c.Err, "记忆只整理到") || !h.logged("切到那里前面没有可以压的") || len(h.bodies) != 0 {
		t.Fatalf("记忆挡住、全留着也装得下：%+v，插件调了 %d 次，请求了 %d 次", c, calls, len(h.bodies))
	}
}

// A8"压缩被取消"：手动压缩时按停止，马上停、不写成快照，也不留"上一轮被停止"的提醒（什么命令都没执行）；新的一句也打断它
func TestCompactStopped(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"停止", "新的一句"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{}, 1)
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, mutate: smallKeep,
				intercept: func(n int, _ string, _ http.ResponseWriter, r *http.Request) bool {
					if n == 1 {
						entered <- struct{}{}
						<-r.Context().Done()
						return true
					}
					return false
				}})
			h.seedRounds(6)
			done := make(chan Compacted, 1)
			go func() {
				c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
				done <- c
			}()
			<-entered
			var next <-chan Outcome
			if how == "停止" {
				if !h.hub.Stop(h.c.CharacterID) {
					t.Fatal("压缩的时候按停止应该停得下")
				}
			} else {
				next = h.hub.Send(h.c.CharacterID, "新的一句")
			}
			var c Compacted
			select {
			case c = <-done:
			case <-time.After(barrierLimit):
				t.Fatal("停下以后压缩一直没结束")
			}
			if !c.Stopped || c.OK {
				t.Fatalf("压缩应该算被停下、没成：%+v", c)
			}
			if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" || !strings.Contains(eps[0].err, "被停下") {
				t.Fatalf("记一行被停下的：%+v", eps)
			}
			if next == nil {
				next = h.hub.Send(h.c.CharacterID, "接着说")
			}
			wait(t, next)
			// 被拦住的摘要请求不进 bodies：下一轮的请求是第 0 个
			if b := h.body(0); strings.Contains(b, "上一轮被所有者手动停止") || strings.Contains(b, "前面的聊天已经整理成下面的摘要") {
				t.Fatalf("停在压缩上不提醒、实例照旧：%s", b)
			}
		})
	}
}

// 审查查出：按过停止、还没提醒，实例因为有了新快照重建了——提醒带到新实例上（Bun 会丢，7.1 A42）
func TestStopReminderSurvivesRebuild(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	stopped := make(chan struct{})
	h = newHarnessOpts(t, harnessOpts{
		hold:    1,
		replies: `[{"content":["我先说","后半句"],` + usage + `},` + summaryReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			smallKeep(c)
			c.OnDelta = func(string) { once.Do(func() { go func() { h.hub.Stop(h.c.CharacterID); close(stopped) }() }) }
		},
	})
	h.seedRounds(6)
	h.send("说点什么")
	<-stopped
	if c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0); !c.OK {
		t.Fatalf("压缩应该成功：%+v", c)
	}
	h.send("接着说")
	if b := h.body(2); !strings.Contains(b, "前面的聊天已经整理成下面的摘要") || !strings.Contains(b, "上一轮被所有者手动停止") {
		t.Fatalf("换上新快照的那一轮照样提醒被停止：%s", b)
	}
}

// 审查查出：自检对路径用的原文不带系统提示（系统提示里的路径不能替摘要里编出来的路径作证）
func TestCompactChatTextSkipsSystem(t *testing.T) {
	t.Parallel()
	src := compactSource{msgs: []Msg{{Role: "system", Text: "说明在 docs/规格.md"}, {Role: "user", Text: "你好"}, {Role: "assistant", Text: "好呀"}}}
	if got := src.chatText(); strings.Contains(got, "docs/规格.md") || !strings.Contains(got, "你好") {
		t.Fatalf("%q", got)
	}
}

// 安全阀（和记忆线对过）：照记忆整理到的地方切，留下的原文压不到硬门槛的八成以下，就不管它照常压，记一行日志
func TestCompactSafetyValve(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, mutate: smallKeep})
	owners := h.seedRounds(8)
	h.send("先建实例")
	l := h.hub.liveOf(h.c.CharacterID)
	hist := sumikaMessages(conversationTexts(l.msgs)...)
	// 硬门槛的八成落在"固定前缀 + 一半历史"：只留最近一轮能压下来，从第 1 轮起全留着压不下来。
	// 这时没有回合在跑（改 Cfg 不和谁抢）
	target := float64(l.b.Fixed) + float64(hist)/2
	h.hub.Cfg.Context = ContextLimits{KeepTokens: 50, Reserve: 1, HardRatio: 1, Window: int(target/0.8) + 1}
	h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
		r.OnBeforeCompact(func(context.Context, BeforeCompact) (int64, bool, error) { return owners[0], true, nil })
		return nil, nil
	}})
	h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if eps := h.epochs(); len(eps) != 1 || eps[0].from <= owners[7] {
		t.Fatalf("按记忆切压不下来，应该照常压（切在最近一轮、种下的八轮之后）：%+v", eps)
	}
	if !h.logged("压不下来") {
		t.Fatalf("要记一行日志：%v", h.logs)
	}
}

// 快照之后有"新话题"分隔时快照不用；下一轮不能因为"用的快照"和"最新的快照"不一样就每轮重建（比的是建的时候看到的那份）
func TestEpochWithLaterBreakNoRebuildLoop(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	owners := h.seedRounds(3)
	conv := consoleConv(h.c.CharacterID)
	if err := h.st.AddEpoch(store.Epoch{ID: "01EPOCHX", Conversation: conv, CharacterID: h.c.CharacterID, FromSeq: owners[1], Summary: "旧的摘要，二十多个字，说过了很多事情很多事情。",
		Kind: "chat", Trigger: "manual", Model: "m", Status: "ok", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "system", ActorID: "system", Origin: "system", Kind: "notice",
		Text: "从这里开始新话题", Segments: map[string]any{"notice": history.ContextBreak}, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID}); err != nil {
		t.Fatal(err)
	}
	h.send("一")
	h.send("二")
	h.send("三")
	var rebuilds int
	h.st.Trace.QueryRow(`SELECT count(*) FROM model_runs WHERE rebuild IS NOT NULL`).Scan(&rebuilds)
	if rebuilds != 1 || !isPrefix(h.messages(0), h.messages(2)) {
		t.Fatalf("只有第一轮建实例，之后接着用：重建了 %d 次", rebuilds)
	}
}
