package turn

// 插话、排队、停止和几件事碰在一起（规格-Go后台 8.2 A4、A5、A11）：对着写明的预期测，不和 Bun 比。
// 交错用现成的回调（OnDelta、OnRequest、假工具、审批卡片）和 Hub.testHook 卡住，不靠睡眠

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

const textReply = `{"content":["好的"],` + usage + `}`

// lastUser 是第 i 次请求里最后一条 user 的内容
func (h *harness) lastUser(i int) string {
	ms := h.messages(i)
	for k := len(ms) - 1; k >= 0; k-- {
		if strings.Contains(ms[k], `"role":"user"`) {
			return ms[k]
		}
	}
	return ""
}

// respondAsync 是平台会话里有人说了一句：存进库、排上队，不等她回
func (h *harness) respondAsync(conv, text string) <-chan Outcome {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "human", ActorID: "p1", Origin: "human", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: text,
		Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "public"})
}

// diagnose 是卡住时的样子：发了几次请求、等着的审批卡片、库里每个工具调用记到哪一步、记下的日志
func (h *harness) diagnose(extra string) string {
	var b strings.Builder
	h.mu.Lock()
	fmt.Fprintf(&b, "%s；发了 %d 次请求；日志 %v\n", extra, len(h.bodies), h.logs)
	h.mu.Unlock()
	for _, p := range h.approvals.Pending() {
		fmt.Fprintf(&b, "等着的审批：%s %s %q 为什么：%s\n", p.ToolCallID, p.Tool, p.Subject, p.Why)
	}
	rows, err := h.st.Trace.Query(`SELECT id, conversation, status, ended_at IS NOT NULL, coalesce(substr(preview, 1, 120), '') FROM steps ORDER BY started_at`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, conv, status, preview string
			var ended bool
			rows.Scan(&id, &conv, &status, &ended, &preview)
			fmt.Fprintf(&b, "步骤 %s（%s）%s 结束了=%v %q\n", id, conv, status, ended, preview)
		}
	}
	return b.String()
}

func wait(t *testing.T, o <-chan Outcome) *Result {
	t.Helper()
	select {
	case r := <-o:
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		return r.Result
	case <-time.After(2 * time.Minute): // 留足 -race 和机器忙的余量；真卡住了照样红
		t.Fatal("这一轮 2 分钟没回完")
		return nil
	}
}

// A4：排队的几批按先后一批一轮；还没轮到的那几句已经存进库，但不会被提前塞进前一轮（复用实例追上别的档时只追跑完的回合用过的）
func TestQueuedBatchesInOrder(t *testing.T) {
	t.Parallel()
	arrived, release := make(chan struct{}), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`,
		onRequest: func(n int) {
			if n == 1 {
				close(arrived)
				<-release
			}
		},
	})
	conv := "group:queue"
	oA := h.respondAsync(conv, "第一批")
	<-arrived
	oB, oC := h.respondAsync(conv, "第二批"), h.respondAsync(conv, "第三批")
	close(release)
	wait(t, oA)
	wait(t, oB)
	wait(t, oC)
	for i, want := range []string{"第一批", "第二批", "第三批"} {
		if !strings.Contains(h.lastUser(i), want) {
			t.Fatalf("第 %d 轮最后一条应该是「%s」：%s", i+1, want, h.lastUser(i))
		}
	}
	if strings.Contains(h.body(1), "第三批") {
		t.Fatalf("第二轮的请求里提前出现了还没轮到的「第三批」：%s", h.body(1))
	}
	if !isPrefix(h.messages(0), h.messages(1)) || !isPrefix(h.messages(1), h.messages(2)) {
		t.Fatalf("排队的几轮不是一轮接一轮追加的")
	}
}

// 每一句收下的插话只有一个结局：送到一次（delivered），或者丢掉一次（dropped；回合收尾时还没送到的带 requeue 退回去）
func checkSteerOnce(t *testing.T, h *harness, text, want string) {
	t.Helper()
	got := strings.Join(h.steerPhases(text), ",")
	if got != "accepted,"+want {
		t.Fatalf("插话「%s」走过的是 %s，应该是 accepted,%s", text, got, want)
	}
}

// A4：插话在她出字时到——这一步说完再送，下一次请求里有、只有一次，库里只存一次
func TestSteerDuringText(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["一","二"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) { once.Do(func() { h.hub.Steer(h.c.CharacterID, "顺便看下天气") }) }
		},
	})
	h.send("你好")
	if len(h.bodies) != 2 || strings.Contains(h.body(0), "顺便看下天气") || strings.Count(h.body(1), "顺便看下天气") != 1 {
		t.Fatalf("插话应该只在第二次请求里出现一次（一共 %d 次请求）", len(h.bodies))
	}
	checkSteerOnce(t, h, "顺便看下天气", "delivered")
	if n := h.ownerSaid("console:"+h.c.CharacterID, "顺便看下天气"); n != 1 {
		t.Fatalf("插话在库里存了 %d 次", n)
	}
}

// 持久受理逐条提交；第二句写库失败须在 Steer 回执拒绝，不能撤销第一句或污染原回合。
func TestSteerStoreFailsMidway(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["一","二"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				once.Do(func() {
					if _, ok := h.hub.Steer(h.c.CharacterID, "先存进去的那句"); !ok {
						t.Error("第一句必须受理")
					}
					if _, ok := h.hub.Steer(h.c.CharacterID, "存不进去的那句"); ok {
						t.Error("写库失败不能受理")
					}
				})
			}
		},
	})
	// 第二句一写库就报错（库里的触发器）
	if _, err := h.st.Events.Exec(`CREATE TRIGGER steer_fail BEFORE INSERT ON events WHEN NEW.text = '存不进去的那句' BEGIN SELECT RAISE(ABORT, '坏了'); END`); err != nil {
		t.Fatal(err)
	}
	if o := recv(t, h.hub.Send(h.c.CharacterID, "你好"), "这一轮回完"); o.Err != nil {
		t.Fatalf("被拒绝的新输入不能污染已受理回合：%v", o.Err)
	}
	if a, b := strings.Join(h.steerPhases("先存进去的那句"), ","), strings.Join(h.steerPhases("存不进去的那句"), ","); a != "accepted,delivered" || b != "" {
		t.Fatalf("第一句送到、第二句未受理：%s / %s", a, b)
	}
	h.send("接着说")
	if n := strings.Count(h.body(1), "先存进去的那句"); n != 1 {
		t.Fatalf("存进库的那句在下一轮的请求里出现一次，实际 %d：%s", n, h.body(1))
	}
}

// C2b 审查查出：这一轮没跑起来就出错——这一批已经存进库，记成跑完的回合用过的，
// 下一轮接着用这个实例时追进来（以库为准；Bun 要等实例重建才有，7.1 A45）
func TestFailedBeforeRunKeepsBatch(t *testing.T) {
	t.Parallel()
	t.Run("找不到模型", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
		h.send("先建实例")
		model := h.hub.Cfg.Models[h.c.CharacterID]
		h.hub.Cfg.Models[h.c.CharacterID] = "没有这个模型"
		if o := recv(t, h.hub.Send(h.c.CharacterID, "找不到模型的那句"), "这一轮回完"); o.Err == nil {
			t.Fatal("找不到模型，这一轮应该报错")
		}
		h.hub.Cfg.Models[h.c.CharacterID] = model
		h.send("接着说")
		if n := strings.Count(h.body(1), "找不到模型的那句"); n != 1 || !isPrefix(h.messages(0), h.messages(1)) {
			t.Fatalf("出错那一句追进来一次（%d）、实例照旧：%s", n, h.body(1))
		}
	})
	t.Run("建实例出错（场合段读不到）", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
		scenes := []string{"scene-group", "platform-qq"}
		wait(t, h.inGroup("群里第一句", scenes))
		if o := recv(t, h.inGroup("场合段读不到的那句", []string{"没有这一段"}), "这一轮回完"); o.Err == nil {
			t.Fatal("场合段读不到，这一轮应该报错")
		}
		wait(t, h.inGroup("接着说", scenes))
		if n := strings.Count(h.body(1), "场合段读不到的那句"); n != 1 {
			t.Fatalf("出错那一句追进来一次（%d）：%s", n, h.body(1))
		}
	})
}

// #10：同一会话两档都已有活实例，一档在硬门槛检查前失败，另一档追上失败批次一次，复用时也不重复。
func TestFailedBeforeRunKeepsBatchAcrossScopes(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"找不到模型", "场合段读不到"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`})
			h.say(groupConv, "human", "200", "另一档先建实例", false, "B")
			h.say(groupConv, "owner", "owner", "失败档先建实例", true, "A")
			const missed = "早退批次里的独特原话"
			in := Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{h.storeGroup(missed)}, Text: missed,
				Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"owner"}, OwnerAsked: true}, Addressed: true, Scope: "A"}
			model := h.hub.Cfg.Models[h.c.CharacterID]
			wantError := "模型列表里没有"
			if how == "找不到模型" {
				h.hub.Cfg.Models[h.c.CharacterID] = "编的不存在模型"
			} else {
				in.Scenes = []string{"编的不存在场合段"}
				wantError = "编的不存在场合段"
			}
			o := recv(t, h.hub.Respond(in), "失败档提前返回")
			h.hub.Cfg.Models[h.c.CharacterID] = model
			requestsAfterFailure := len(h.bodies)
			h.say(groupConv, "human", "200", "另一档下一轮短句", false, "B")
			h.say(groupConv, "human", "200", "另一档再下一轮", false, "B")
			first, again := strings.Count(h.body(2), missed), strings.Count(h.body(3), missed)
			if o.Err == nil || !strings.Contains(o.Err.Error(), wantError) || requestsAfterFailure != 2 || first != 1 || again != 1 ||
				strings.Count(h.body(3), "另一档下一轮短句") != 1 || !isPrefix(h.messages(0), h.messages(2)) || !isPrefix(h.messages(2), h.messages(3)) {
				t.Fatalf("跨档早退应可追踪、未请求模型，另一档复用且注入一次：err=%v 请求=%d 失败批次=%d/%d 短句=%d",
					o.Err, requestsAfterFailure, first, again, strings.Count(h.body(3), "另一档下一轮短句"))
			}
		})
	}
}

// A4：插话在工具跑着时到——工具跑完、下一次请求里送到一次
func TestSteerDuringTool(t *testing.T) {
	t.Parallel()
	var h *harness
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"t1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				h.hub.Steer(h.c.CharacterID, "先别列了")
				return tools.Result{Text: "列好了"}, nil
			}
		},
	})
	h.send("列一下")
	if len(h.bodies) != 2 || strings.Count(h.body(1), "先别列了") != 1 {
		t.Fatalf("插话应该在工具跑完后的那次请求里出现一次")
	}
	checkSteerOnce(t, h, "先别列了", "delivered")
}

// A4：插话在等审批时到——主人批了、工具跑完，下一次请求里送到一次
func TestSteerDuringApproval(t *testing.T) {
	t.Parallel()
	ran := 0
	var h *harness
	h = newHarnessOpts(t, harnessOpts{
		mode:    "manual",
		replies: `[{"toolCalls":[{"id":"w1","name":"write","args":["{\"path\":\"new.txt\",\"content\":\"x\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				ran++
				return tools.Result{Text: "写好了"}, nil
			}
		},
	})
	cards := 0
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) {
		cards++
		h.hub.Steer(h.c.CharacterID, "写完告诉我")
		go h.approvals.Decide(req.ID, "once", "")
	})
	h.send("写个文件")
	if cards != 1 || ran != 1 {
		t.Fatalf("应该弹一张卡片（%d）、批了以后执行一次（%d）", cards, ran)
	}
	if strings.Count(h.body(1), "写完告诉我") != 1 {
		t.Fatalf("插话应该在批完、执行完以后的那次请求里出现一次")
	}
	checkSteerOnce(t, h, "写完告诉我", "delivered")
}

// 收尾时已持久受理的本地插话转成下一工作；不重复落事件，下一请求只带一次该输入。
func TestSteerAtSettlingRequeued(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	var once sync.Once
	h.hub.testHook = func(p string) {
		if p == "settling" {
			once.Do(func() {
				if _, ok := h.hub.Steer(h.c.CharacterID, "还有一件事"); !ok {
					t.Error("收尾之前这一轮还在跑，插话应该收下")
				}
			})
		}
	}
	h.send("你好")
	if err := h.hub.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(h.bodies) != 2 || h.ownerSaid("console:"+h.c.CharacterID, "还有一件事") != 1 || strings.Count(h.body(1), "还有一件事") != 1 {
		t.Fatalf("持久插话必须恰好进入下一轮：请求 %d 次、事件 %d 条", len(h.bodies), h.ownerSaid("console:"+h.c.CharacterID, "还有一件事"))
	}
}

// A5：她出字时主人按停止——不再发下一次请求；说出口的半句存成 aborted；没送到的插话丢掉（不退回）；这一轮的结局是 stopped
func TestStopDuringText(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	stopped := make(chan struct{})
	h = newHarnessOpts(t, harnessOpts{
		// 第一次请求吐完一段就停住，等这边中止连接
		hold:    1,
		replies: `[{"content":["我先说","后半句"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				once.Do(func() {
					h.hub.Steer(h.c.CharacterID, "没送到的插话")
					go func() { h.hub.Stop(h.c.CharacterID); close(stopped) }()
				})
			}
		},
	})
	r := h.send("说点什么")
	<-stopped
	if !r.Stopped || len(h.bodies) != 1 {
		t.Fatalf("停下以后不该再请求（%d 次），结局应该是 stopped（%v）", len(h.bodies), r.Stopped)
	}
	checkSteerOnce(t, h, "没送到的插话", "dropped")
}

// A5：工具跑着时主人按停止——工具收到取消；这个调用只有一个结局（记了一次结束）；不再请求
func TestStopDuringTool(t *testing.T) {
	t.Parallel()
	var h *harness
	cancelled := make(chan bool, 1)
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"t1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.Lifecycle.StopGrace = 50 * time.Millisecond
			c.Lifecycle.StopGraceSet = true
			c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
				go h.hub.Stop(h.c.CharacterID)
				select {
				case <-ctx.Done():
					cancelled <- true
				case <-time.After(barrierLimit):
					cancelled <- false
				}
				return tools.Result{Text: "Command aborted", IsError: true}, nil
			}
		},
	})
	// 停在工具上以后下一步请求发不出去，照 7.1 A36 记成出错（Bun 的做法，没改），所以这里不要求这一轮没报错
	<-h.hub.Send(h.c.CharacterID, "列一下")
	if !<-cancelled {
		t.Fatal("停止以后工具没收到取消")
	}
	if n := h.endedSteps("t1"); n != 1 {
		t.Fatalf("这个调用记了 %d 次结束", n)
	}
	if len(h.bodies) != 1 {
		t.Fatalf("停下以后不该再请求（%d 次）", len(h.bodies))
	}
}

// A5：主窗口和另一个会话都在等审批，主窗口按停止——只取消这个会话的卡片；另一个会话的卡片还在，批了照常执行
// （Bun 447d97e7 修过的：按角色取消，把同一个角色另一个会话的审批一起取消了）
func TestStopCancelsOnlyThisConversationsApproval(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	ran := map[string]int{}
	h := newHarnessOpts(t, harnessOpts{
		mode: "manual",
		replies: `[{"toolCalls":[{"id":"w1","name":"write","args":["{\"path\":\"a.txt\",\"content\":\"x\"}"]}],` + usage + `},
			{"toolCalls":[{"id":"w2","name":"write","args":["{\"path\":\"b.txt\",\"content\":\"y\"}"]}],` + usage + `},` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(_ context.Context, _, _ string, args jsjson.Value) (tools.Result, error) {
				mu.Lock()
				ran[args.Obj["path"].Str]++
				mu.Unlock()
				return tools.Result{Text: "写好了"}, nil
			}
		},
	})
	cards := make(chan approval.Request, 2)
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) { cards <- req })
	mainOut := h.hub.Send(h.c.CharacterID, "写 a")
	first := <-cards
	side := "console:" + h.c.CharacterID + ":side"
	seq, _ := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: side, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "写 b", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	other := h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: side, Seqs: []int64{seq}, Text: "写 b", Audience: audience.Console(side), Addressed: true, Scope: ConsoleScope})
	second := <-cards
	// 审批绑定宿主执行号；会话身份明确区分主窗口和另一个会话。
	if first.ToolCallID == second.ToolCallID || first.Conversation != consoleConv(h.c.CharacterID) || second.Conversation != side {
		t.Fatalf("两张卡片应属于各自会话：%+v %+v", first, second)
	}
	h.hub.Stop(h.c.CharacterID)
	<-mainOut
	pending := h.approvals.Pending()
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("停止以后应该只剩另一个会话那张卡片：%+v", pending)
	}
	if ok, err := h.approvals.Decide(second.ID, "once", ""); !ok || err != nil {
		t.Fatalf("另一个会话的卡片应该还能批：%v %v", ok, err)
	}
	wait(t, other)
	if ran["a.txt"] != 0 || ran["b.txt"] != 1 {
		t.Fatalf("停掉的那个不该执行、另一个执行一次：%v", ran)
	}
}

// A5：来了新的一句打断正在说的——没送到的插话退回去（不是丢掉），新的一句照常存、照常回
func TestInterruptRequeuesSteer(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	var second <-chan Outcome
	h = newHarnessOpts(t, harnessOpts{
		hold:    1,
		replies: `[{"content":["我先说","后半句"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				once.Do(func() {
					h.hub.Steer(h.c.CharacterID, "插一句")
					second = h.hub.Send(h.c.CharacterID, "换个话题")
				})
			}
		},
	})
	r := h.send("说点什么")
	wait(t, second)
	if !r.Stopped {
		t.Fatal("被新的一句打断的那一轮结局应该是 stopped")
	}
	if err := h.hub.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := h.ownerSaid("console:"+h.c.CharacterID, "插一句"); n != 1 {
		t.Fatalf("已受理插话必须只落一条事件：%d", n)
	}
	if n := h.ownerSaid("console:"+h.c.CharacterID, "换个话题"); n != 1 {
		t.Fatalf("新的一句存了 %d 次", n)
	}
}

// A11：几种交错各跑 100 次，每次都对
// raceRuns 是竞态测试每个跑多少遍：整套（巡检）跑 100 遍；-short（合并前、推送前）跑 10 遍，只当冒烟（ADR-225）。
// 碰到审批、写用户文件的两个（谁能做什么、改真实数据）不用它，一直跑 fullRuns 遍
const fullRuns = 100

func raceRuns() int {
	if testing.Short() {
		return 10
	}
	return fullRuns
}

// 两句话同时到：两句都存一次、都没报错，实例里两句各出现一次
func TestRaceTwoSends(t *testing.T) {
	t.Parallel()
	for i := 0; i < raceRuns(); i++ {
		t.Run(fmt.Sprintf("第 %d 遍", i), func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
			var wg sync.WaitGroup
			outs := make([]<-chan Outcome, 2)
			for k, text := range []string{"甲", "乙"} {
				wg.Add(1)
				go func() { defer wg.Done(); outs[k] = h.hub.Send(h.c.CharacterID, text) }()
			}
			wg.Wait()
			// 后到的一句打断先到的：打断落在先到那句还没发请求时，先到那句照 7.1 A36 记成被停下、报 "The operation was aborted."
			// （Bun 里 fetch 拿到已中止的信号也是这句）；落在说到一半时是被停下、不报错。两句里至多一句被打断，另一句要正常说完
			done := 0
			for _, o := range outs {
				r := <-o
				switch {
				case r.Err == nil && r.Result != nil && !r.Result.Stopped:
					done++
				case r.Result != nil && r.Result.Stopped && (r.Err == nil || errors.Is(r.Err, errAbortedBeforeRequest)):
				default:
					t.Fatalf("第 %d 次：%v", i, r.Err)
				}
			}
			if done == 0 {
				t.Fatalf("第 %d 次：两句都被停下了，后到的那句应当说完", i)
			}
			conv := "console:" + h.c.CharacterID
			inst := 0
			for _, m := range h.hub.liveOf(h.c.CharacterID).msgs {
				if m.Role == "user" && (m.Text == "甲" || m.Text == "乙") {
					inst++
				}
			}
			if h.ownerSaid(conv, "甲") != 1 || h.ownerSaid(conv, "乙") != 1 || inst != 2 {
				t.Fatalf("第 %d 次：甲存了 %d 次、乙 %d 次，实例里一共 %d 句", i, h.ownerSaid(conv, "甲"), h.ownerSaid(conv, "乙"), inst)
			}
		})
	}
}

// 插话碰上收尾（真的赛跑，不卡时刻）：每一句插话都只有一个结局，送到的就进了库，退回的就没进库
func TestRaceSteerVsSettle(t *testing.T) {
	t.Parallel()
	for i := 0; i < raceRuns(); i++ {
		t.Run(fmt.Sprintf("第 %d 遍", i), func(t *testing.T) {
			t.Parallel()
			var h *harness
			var once sync.Once
			steered := make(chan struct{})
			h = newHarnessOpts(t, harnessOpts{
				replies: `[` + textReply + `,` + textReply + `]`,
				mutate: func(c *Config) {
					c.OnDelta = func(string) {
						once.Do(func() {
							go func() {
								defer close(steered)
								h.hub.Steer(h.c.CharacterID, "赛跑的插话")
							}()
						})
					}
				},
			})
			h.send("你好")
			// 先等插话调用退出，避免尚未开始就把空快照当成“没收下”。
			receive(t, steered)
			ph := h.steerPhases("赛跑的插话")
			saved := h.ownerSaid("console:"+h.c.CharacterID, "赛跑的插话")
			switch strings.Join(ph, ",") {
			case "":
			case "accepted":
				// 收下时这一轮已经过了最后一次取插话：本地的话改排成下一轮（requeueSteersLocked），不丢，也不多存
				deadline := time.Now().Add(30 * time.Second)
				for h.ownerSaid("console:"+h.c.CharacterID, "赛跑的插话") != 1 {
					if time.Now().After(deadline) {
						t.Fatalf("第 %d 次：改排成下一轮后库里一直没有这句", i)
					}
					runtime.Gosched()
				}
			case "accepted,delivered":
				if saved != 1 {
					t.Fatalf("第 %d 次：送到了但库里存了 %d 次", i, saved)
				}
			case "accepted,dropped+requeue":
				if saved != 0 {
					t.Fatalf("第 %d 次：退回了但库里存了 %d 次", i, saved)
				}
			default:
				t.Fatalf("第 %d 次：插话走过的是 %v", i, ph)
			}
		})
	}
}

// 批准碰上停止：执行了就一定是批准了的，取消了就一定没执行；每张卡片只有一个结局
func TestRaceApproveVsStop(t *testing.T) {
	t.Parallel()
	for i := 0; i < fullRuns; i++ {
		t.Run(fmt.Sprintf("第 %d 遍", i), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			ran := 0
			h := newHarnessOpts(t, harnessOpts{
				mode:    "manual",
				replies: `[{"toolCalls":[{"id":"w1","name":"write","args":["{\"path\":\"a.txt\",\"content\":\"x\"}"]}],` + usage + `},` + textReply + `]`,
				mutate: func(c *Config) {
					c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
						mu.Lock()
						ran++
						mu.Unlock()
						return tools.Result{Text: "写好了"}, nil
					}
				},
			})
			var decisions []string
			resolved := make(chan struct{})
			h.approvals.OnResolved = append(h.approvals.OnResolved, func(_ approval.Request, d approval.Decision) {
				mu.Lock()
				decisions = append(decisions, d.Kind)
				mu.Unlock()
				close(resolved)
			})
			h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) {
				go h.approvals.Decide(req.ID, "once", "")
				go h.hub.Stop(h.c.CharacterID)
			})
			<-h.hub.Send(h.c.CharacterID, "写个文件")
			// OnResolved 的通知异步送达；等回调真实记下结局后再读。
			receive(t, resolved)
			mu.Lock()
			d, r := strings.Join(decisions, ","), ran
			mu.Unlock()
			switch {
			case d == "approved" && r <= 1:
			case d == "cancelled" && r == 0:
			default:
				t.Fatalf("第 %d 次：卡片结局 %q、执行了 %d 次", i, d, r)
			}
		})
	}
}

// 两个会话同时改同一个文件（真的 write，不换假工具）：两个都写完、都没报错，文件是其中一份完整的内容，没有写成半截混在一起
func TestRaceTwoConversationsWriteSameFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("压力和规模测试，放在每日整套")
	}
	for i := 0; i < fullRuns; i++ {
		big := func(c string) string { return strings.Repeat(c, 16*1024) }
		var bmu sync.Mutex
		both, arrived := make(chan struct{}), 0
		h := newHarnessOpts(t, harnessOpts{
			mode: "yolo",
			onRequest: func(n int) {
				if n > 2 {
					return
				}
				bmu.Lock()
				arrived++
				if arrived == 2 {
					close(both)
				}
				bmu.Unlock()
				// 另一个会话的第一次请求一直不来（它在请求之前就卡住、出错了）也不要把这一个陪着卡死：10 秒后放行，诊断里看得到
				select {
				case <-both:
				case <-time.After(10 * time.Second):
				}
			},
			replies: `[{"toolCalls":[{"id":"w1","name":"write","args":["{\"path\":\"same.txt\",\"content\":\"` + big("a") + `\"}"]}],` + usage + `},
				{"toolCalls":[{"id":"w2","name":"write","args":["{\"path\":\"same.txt\",\"content\":\"` + big("b") + `\"}"]}],` + usage + `},` + textReply + `,` + textReply + `]`,
		})
		convs := []string{"console:" + h.c.CharacterID, "console:" + h.c.CharacterID + ":side"}
		var outs []<-chan Outcome
		for _, conv := range convs {
			seq, _ := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "写", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
			outs = append(outs, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "写", Audience: audience.Console(conv), Addressed: true, Scope: ConsoleScope}))
		}
		// 两个一起等，谁先出错先报谁；30 秒没回完就把当时的样子都打出来。2026-10-26 Linux 上靠它查出：审批的关的 IsInside 只认反斜杠，
		// 工作目录里的写也被判成"在外面"、yolo 下照样问，没人回答就一直等（不是 -race 慢）

		got := make([]bool, len(outs))
		deadline := time.After(30 * time.Second)
		for left := len(outs); left > 0; left-- {
			select {
			case r := <-outs[0]:
				got[0] = true
				outs[0] = nil
				if r.Err != nil {
					t.Fatalf("第 %d 次、主窗口那个会话：%v", i, r.Err)
				}
			case r := <-outs[1]:
				got[1] = true
				outs[1] = nil
				if r.Err != nil {
					t.Fatalf("第 %d 次、另一个会话：%v", i, r.Err)
				}
			case <-deadline:
				bmu.Lock()
				a := arrived
				bmu.Unlock()
				t.Fatalf("第 %d 次：30 秒没回完（回完了 %v）\n%s", i, got, h.diagnose(fmt.Sprintf("第一次请求到了 %d 个", a)))
			}
		}
		b, err := os.ReadFile(filepath.Join(h.wd, "same.txt"))
		if err != nil {
			t.Fatalf("第 %d 次：%v", i, err)
		}
		if s := string(b); s != big("a") && s != big("b") {
			t.Fatalf("第 %d 次：文件不是其中一份完整的内容（长 %d）", i, len(s))
		}
		if h.endedSteps("w1", "w2") != 2 {
			t.Fatalf("第 %d 次：两个写都应该记了结束", i)
		}
	}
}
