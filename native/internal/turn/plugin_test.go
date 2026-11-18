package turn

// 插件接口的契约（规格-Go后台 8.2 A10）和听众按回合算（A13，外部审计 F02）。都是对着写明的预期测，不和 Bun 比（Bun 没有插件接口、
// 实例不分档：A13 在 Bun 上会红）。钩子时限调短，"停掉以后结果丢掉"用停掉以后才返回的假插件，不靠睡眠

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type harness struct {
	t      *testing.T
	hub    *Hub
	st     *store.Store
	c      testCase
	mu     sync.Mutex
	bodies []string
	logs   []string
	steers []SteerEvent // 插话走到了哪一步（OnSteer）
	wd     string
	// approvals：等主人回答的审批卡片（和对拍一样接在 gate 上、记进 steps）
	approvals *approval.Approvals
	client    *llm.Client        // 假网关的客户端（A7 的测试改它的 Idle）
	posts     int                // 假网关收到过几次 POST（有 intercept 时才数，拦下的也算）
	cancel    context.CancelFunc // 取消 Hub 的 ctx（后台在关）
	data      paths.Paths        // 数据目录（角色卡、配置、库）
}

// harnessOpts：fail 为真时假网关一律回 500；replies 是每一步模型回什么（JSON 数组，格式同场景文件的 replies），空的照 live-second；
// onRequest 在假网关收到第 n 次请求、回复之前调（看那时库里是什么样）；mode 是审批模式（空的照默认）
type harnessOpts struct {
	fail      bool
	mutate    func(*Config)
	replies   string
	onRequest func(nth int)
	mode      string
	// hold：第几次请求吐完第一段正文就停住、等这边中止连接（假网关看场景的 interject，这里只用它的"停住"）
	hold int
	// onBody 在假网关回复之前看请求体（按内容卡住某一个会话的请求用）；ctx 是这次请求的，她那边中止时取消
	onBody func(ctx context.Context, body string)
	// intercept 在假网关回复之前拦第 n 次 POST（n 从 1 数，拦下的也算）：自己回了（出错、断开）返回真，这一次不算进 bodies、
	// 不用掉 replies；body 是这次的请求体（A7 的重试测试用）
	intercept func(n int, body string, w http.ResponseWriter, r *http.Request) bool
	// data、wd：用现成的数据目录（里面已经有角色卡和库）、工作目录，不新建（模拟被杀以后再起来的进程，见 crash_test.go 的 restart）
	data *paths.Paths
	wd   string
	// model、models：换角色的模型和网关的模型清单（线上字节对拍要看图片消息，用能看图的模型）
	model  string
	models []json.RawMessage
}

// newHarness 照 refresh_test 搭一个 Hub：live-second 的角色、模型、假网关（回固定的几句）
func newHarness(t *testing.T, fail bool, mutate func(*Config)) *harness {
	return newHarnessOpts(t, harnessOpts{fail: fail, mutate: mutate})
}

func newHarnessOpts(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	fail, mutate := o.fail, o.mutate
	h := &harness{t: t}
	b, err := os.ReadFile(filepath.Join("testdata", "cases", "live-second.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &h.c); err != nil {
		t.Fatal(err)
	}
	if o.model != "" {
		m := h.c.Characters[h.c.CharacterID]
		m.Model = o.model
		h.c.Characters[h.c.CharacterID] = m
	}
	if o.models != nil {
		h.c.Models = o.models
	}
	if o.replies != "" {
		h.c.Replies = nil
		if err := json.Unmarshal([]byte(`{"replies":`+o.replies+`}`), &h.c); err != nil {
			t.Fatal(err)
		}
	}
	h.c.Interject = nil
	if o.hold > 0 {
		json.Unmarshal([]byte(fmt.Sprintf(`{"interject":[{"on":"delta","request":%d,"n":1,"do":"hold"}]}`, o.hold)), &h.c)
	}
	var p paths.Paths
	if o.data != nil {
		p = *o.data
	} else {
		if p, err = paths.Init(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		for rel, text := range h.c.Files {
			f := filepath.Join(p.Root, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(f), 0o755)
			os.WriteFile(f, []byte(text), 0o644)
		}
	}
	h.data = p
	if h.st, err = store.OpenFresh(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.st.Close() })
	var srv *httptest.Server
	if fail {
		srv = memServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(map[string]any{"data": h.c.Models})
				return
			}
			http.Error(w, "坏了", http.StatusInternalServerError)
		}))
	} else {
		handler := gatewayHandler(t, h.c, &h.bodies, &h.mu)
		srv = memServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o.intercept != nil && r.Method == http.MethodPost {
				b, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(b))
				h.mu.Lock()
				h.posts++
				n := h.posts
				h.mu.Unlock()
				if o.intercept(n, string(b), w, r) {
					return
				}
			}
			if o.onRequest != nil && r.Method == http.MethodPost {
				h.mu.Lock()
				n := len(h.bodies) + 1
				h.mu.Unlock()
				o.onRequest(n)
			}
			if o.onBody != nil && r.Method == http.MethodPost {
				b, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(b))
				o.onBody(r.Context(), string(b))
				if r.Context().Err() != nil {
					return // 她那边已经中止了，不再回
				}
			}
			handler(w, r)
		}))
	}
	t.Cleanup(srv.Close)
	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	h.client = client
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, h.c.Now)
	h.wd = o.wd
	if h.wd == "" {
		h.wd = t.TempDir()
	}
	clock := func() time.Time { return now }
	gate := testGate(p.Root)
	gate.Now = clock
	if o.mode != "" {
		gate.Mode = o.mode
	}
	h.approvals = approval.NewApprovals(gate)
	WireApprovalSteps(h.approvals, h.st)
	cfg := Config{Root: factoryRoot(t), Data: p, Thinking: "low", RecentMessages: 30, Workdir: h.wd, Models: map[string]string{h.c.CharacterID: h.c.Characters[h.c.CharacterID].Model},
		Now: clock, Gate: gate, RefScope: memory.ScopeKey,
		Hooks: HookTimeouts{Context: 2 * time.Second, Notify: 2 * time.Second},
		// 重试等的时间调到毫秒（假网关一律回 500 的那些测试不多等），次数照默认
		Retry: RetryPolicy{Base: time.Millisecond, Cap: 2 * time.Millisecond},
		Logf: func(f string, a ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(f, a...))
			h.mu.Unlock()
		},
		OnSteer: func(e SteerEvent) {
			h.mu.Lock()
			h.steers = append(h.steers, e)
			h.mu.Unlock()
		}}
	if mutate != nil {
		mutate(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	// 一个收尾里先取消再等后台的提前压缩（分开注册的话等在取消之前，卡在假网关上的要等满时限）；库在这之后才关
	t.Cleanup(func() {
		cancel()
		h.hub.Idle()
	})
	h.hub = NewHub(ctx, cfg, anyConv{h.st}, client, models)
	h.approvals.OnResolved = append(h.approvals.OnResolved, func(req approval.Request, _ approval.Decision) { h.hub.ApprovalResolved(req.CharacterID) })
	return h
}

// steerPhases 是某一句插话走过的几步（queued 不记：Push 时由 Steering 发，这里只看结局）
func (h *harness) steerPhases(text string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.steers {
		if e.Text == text {
			p := e.Phase
			if e.Requeue {
				p += "+requeue"
			}
			out = append(out, p)
		}
	}
	return out
}

// ownerSaid 是库里这个会话主人说过多少次这句
func (h *harness) ownerSaid(conv, text string) int {
	var n int
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_kind = 'owner' AND text = ?`, conv, text).Scan(&n)
	return n
}

func (h *harness) body(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.bodies) {
		h.t.Fatalf("只发了 %d 次请求，要看第 %d 次", len(h.bodies), i+1)
	}
	return h.bodies[i]
}

// messages 是第 i 次请求的 messages，每条一个 JSON 串（好比前缀）
func (h *harness) messages(i int) []string {
	var r struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(h.body(i)), &r); err != nil {
		h.t.Fatal(err)
	}
	out := make([]string, len(r.Messages))
	for k, m := range r.Messages {
		out[k] = string(m)
	}
	return out
}

func (h *harness) logged(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func (h *harness) send(text string) *Result {
	h.t.Helper()
	return wait(h.t, h.hub.Send(h.c.CharacterID, text)) // 带时限：卡住时红，不等到 go test 的总超时
}

// say 是平台会话里有人说了一句：存进库，再请她回（渠道中间件以后做的事）
func (h *harness) say(conv, actorKind, actorID, text string, ownerAsked bool, scope string) *Result {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: actorKind, Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	o := <-h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: text,
		Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{actorID}, OwnerAsked: ownerAsked}, Addressed: true, Scope: scope})
	if o.Err != nil {
		h.t.Fatal(o.Err)
	}
	h.commitSaid(conv, o.Result, nil)
	return o.Result
}

// commitSaid 扮演渠道中间件（ADR-184 第 3 条）：把这一回合她说的每一句（rewrite 不是 nil 就先改写）存进库，再用 Commit 交回内核
func (h *harness) commitSaid(conv string, r *Result, rewrite func(string) string) []Committed {
	h.t.Helper()
	var lines []Committed
	for _, s := range r.Said {
		text := s.Text
		if rewrite != nil {
			text = rewrite(text)
		}
		seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "character", ActorID: h.c.CharacterID, Origin: "character", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
		if err != nil {
			h.t.Fatal(err)
		}
		lines = append(lines, Committed{Step: s.Step, Seq: seq, Text: text})
	}
	if len(lines) > 0 {
		if err := h.hub.Commit(h.c.CharacterID, conv, r.TurnID, lines); err != nil {
			h.t.Fatal(err)
		}
	}
	return lines
}

func isPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 用函数拼一个插件
type fakePlugin struct {
	id    string
	setup func(r *Registry) (func(), error)
}

func (p fakePlugin) ID() string                        { return p.id }
func (p fakePlugin) Setup(r *Registry) (func(), error) { return p.setup(r) }
func contextPlugin(id string, fn ContextFn) fakePlugin {
	return fakePlugin{id, func(r *Registry) (func(), error) { r.OnContext(fn); return nil, nil }}
}

func (h *Hub) usePlugin(t *testing.T, p Plugin) {
	t.Helper()
	if err := h.Use(p); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) use(p Plugin) {
	h.t.Helper()
	if err := h.hub.Use(p); err != nil {
		h.t.Fatal(err)
	}
}

// A10：参考信息每个回合只拼一次、只追加在末尾；同一个实例里插件的存储接着用，实例重建就是新的
func TestPluginContextOncePerTurnAppendOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	calls := 0
	h.use(contextPlugin("count", func(_ context.Context, in ContextIn) (*Contribution, error) {
		calls++
		n, _ := in.State["n"].(int)
		in.State["n"] = n + 1
		return &Contribution{Lines: []string{fmt.Sprintf("记忆：这个实例第 %d 次给", n+1)}}, nil
	}))
	h.send("第一句")
	h.send("第二句")
	if calls != 2 {
		t.Fatalf("两个回合调了插件 %d 次，应该是 2 次", calls)
	}
	m0, m1 := h.messages(0), h.messages(1)
	if !isPrefix(m0, m1) {
		t.Fatalf("第二轮的请求不是在第一轮后面追加的：\n第一轮 %v\n第二轮 %v", m0, m1)
	}
	if !strings.Contains(h.body(0), "这个实例第 1 次给") || !strings.Contains(h.body(1), "这个实例第 2 次给") {
		t.Fatalf("插件的存储没在同一个实例里接着用：\n%s\n%s", h.body(0), h.body(1))
	}
	if strings.Count(h.body(1), "这个实例第 1 次给") != 1 {
		t.Fatalf("第二轮里第一轮那几行应该还在、只出现一次：%s", h.body(1))
	}
	h.hub.Restart(h.c.CharacterID)
	h.send("第三句")
	if !strings.Contains(h.body(2), "这个实例第 1 次给") || strings.Contains(h.body(2), "这个实例第 2 次给") {
		t.Fatalf("实例重建以后插件的存储应该是新的：%s", h.body(2))
	}
}

// A10：插件出错只跳过它自己；别的插件照常、这一轮照常
func TestPluginErrorIsolated(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.use(contextPlugin("broken", func(context.Context, ContextIn) (*Contribution, error) { return nil, errors.New("记忆库打不开") }))
	h.use(contextPlugin("panicky", func(context.Context, ContextIn) (*Contribution, error) { panic("炸了") }))
	h.use(contextPlugin("fine", func(context.Context, ContextIn) (*Contribution, error) {
		return &Contribution{Lines: []string{"正常的那一行"}}, nil
	}))
	h.send("你好")
	if b := h.body(0); !strings.Contains(b, "正常的那一行") {
		t.Fatalf("出错的插件不该挡住正常插件的那一行：%s", b)
	}
	for _, id := range []string{"broken", "panicky"} {
		if !h.logged("插件 " + id) {
			t.Errorf("插件 %s 出错没记日志：%v", id, h.logs)
		}
	}
}

// A10：插件超时只跳过它自己，它晚到的结果丢掉；别的插件照常。
// 时限用 synctest 的虚拟时钟：墙钟 100ms 在负载下会让正常插件也超时；虚拟时钟只在所有协程都阻塞后才走，
// 正常插件一定先返回，慢插件一定等到时限。网络 I/O 会阻止虚拟时钟推进，所以不起假网关，直接收集贡献
func TestPluginTimeoutIsolated(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var logs []string
		base, cancelBase := context.WithCancel(context.Background())
		defer cancelBase()
		h := NewHub(base, Config{Data: p, WorkSettings: config.DefaultWork(),
			Logf: func(f string, a ...any) {
				mu.Lock()
				logs = append(logs, fmt.Sprintf(f, a...))
				mu.Unlock()
			},
			Hooks: HookTimeouts{Context: 100 * time.Millisecond}}, anyConv{st}, nil, nil)
		h.usePlugin(t, contextPlugin("slow", func(ctx context.Context, _ ContextIn) (*Contribution, error) {
			<-ctx.Done() // 一直等到超时才回
			return &Contribution{Lines: []string{"迟到的那一行"}}, nil
		}))
		h.usePlugin(t, contextPlugin("fine", func(context.Context, ContextIn) (*Contribution, error) {
			return &Contribution{Lines: []string{"正常的那一行"}}, nil
		}))
		got := h.plugins.contextContributions(base, ContextIn{CharacterID: "c", Conversation: "console:c"}, func(string) map[string]any { return nil })
		if len(got) != 1 || got[0].hook.plugin != "fine" || len(got[0].value.Lines) != 1 || got[0].value.Lines[0] != "正常的那一行" {
			t.Fatalf("应该只有正常插件的那一行：%+v", got)
		}
		mu.Lock()
		logged := strings.Join(logs, "；")
		mu.Unlock() // 先放锁：下面取消会让 Seal 写日志，日志函数也要这把锁
		if !strings.Contains(logged, "插件 slow") {
			t.Errorf("插件 slow 超时没记日志：%v", logged)
		}
		cancelBase()
		synctest.Wait()
	})
}

// A10：插件在调用途中停掉了，它晚到的结果丢掉；停掉以后不再被调
func TestPluginStoppedMidCallDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	h.use(contextPlugin("late", func(context.Context, ContextIn) (*Contribution, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		}
		return &Contribution{Lines: []string{"停掉以后才回的那一行"}}, nil
	}))
	go func() {
		<-entered
		h.hub.StopPlugin("late")
		close(release)
	}()
	h.send("第一句")
	h.send("第二句")
	if strings.Contains(h.body(0), "停掉以后才回的那一行") {
		t.Fatalf("停掉以后才回的结果不该用上：%s", h.body(0))
	}
	if calls != 1 {
		t.Fatalf("停掉以后又被调了（一共 %d 次）", calls)
	}
}

// A10：插话送到时插件再拼一次参考信息，追加在插话前面
func TestPluginContextOnSteer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	var seen []bool
	h.use(contextPlugin("steer", func(_ context.Context, in ContextIn) (*Contribution, error) {
		seen = append(seen, in.Steer)
		if !in.Steer {
			if _, ok := h.hub.Steer(h.c.CharacterID, "上次说的那个呢"); !ok {
				return nil, errors.New("插话没收下")
			}
			return nil, nil
		}
		return &Contribution{Lines: []string{"插话时召回的：" + in.Text}}, nil
	}))
	h.send("第一句")
	if len(seen) != 2 || seen[0] || !seen[1] {
		t.Fatalf("应该回合开头调一次、插话送到再调一次，实际 %v", seen)
	}
	ms := h.messages(0)
	ctxAt, steerAt := -1, -1
	for i, m := range ms {
		if strings.Contains(m, "插话时召回的：上次说的那个呢") {
			ctxAt = i
		}
		if strings.Contains(m, "上次说的那个呢") && !strings.Contains(m, "插话时召回的") {
			steerAt = i
		}
	}
	if ctxAt < 0 || steerAt != ctxAt+1 {
		t.Fatalf("插话时的参考信息应该紧挨在插话前面（参考 %d，插话 %d）：%v", ctxAt, steerAt, ms)
	}
}

// A10："回合定了"一个回合一次，带回合 id 和结局；停掉的订阅者不再收到
func TestPluginSettledOncePerTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	got := make(chan Settled, 10)
	h.use(fakePlugin{"memo", func(r *Registry) (func(), error) {
		r.OnSettled(func(_ context.Context, s Settled) { got <- s })
		return nil, nil
	}})
	r1, r2 := h.send("第一句"), h.send("第二句")
	for _, want := range []string{r1.TurnID, r2.TurnID} {
		select {
		case s := <-got:
			if s.TurnID != want || s.Outcome != OutcomeDone || s.Conversation != "console:"+h.c.CharacterID {
				t.Fatalf("回合定了的内容不对：%+v，应该是回合 %s、done", s, want)
			}
		case <-time.After(barrierLimit):
			t.Fatal("没收到回合定了")
		}
	}
	h.hub.StopPlugin("memo")
	other := make(chan Settled, 1)
	h.use(fakePlugin{"other", func(r *Registry) (func(), error) {
		r.OnSettled(func(_ context.Context, s Settled) { other <- s })
		return nil, nil
	}})
	h.send("第三句")
	<-other // 后订阅的收到了，说明已经发过
	select {
	case s := <-got:
		t.Fatalf("停掉的插件还收到了：%+v", s)
	default:
	}
}

// A10：出错的回合也定一次，结局是 error
func TestPluginSettledOnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, nil)
	got := make(chan Settled, 1)
	h.use(fakePlugin{"memo", func(r *Registry) (func(), error) {
		r.OnSettled(func(_ context.Context, s Settled) { got <- s })
		return nil, nil
	}})
	if o := <-h.hub.Send(h.c.CharacterID, "你好"); o.Err == nil {
		t.Fatal("假网关回 500，这一轮应该出错")
	}
	select {
	case s := <-got:
		if s.Outcome != OutcomeError || s.Err == nil {
			t.Fatalf("结局应该是 error：%+v", s)
		}
	case <-time.After(barrierLimit):
		t.Fatal("出错的回合没定")
	}
}

// A10：Setup 出错的插件不启用，它已经注册的撤掉；同一个 id 不能启用两次
func TestPluginSetupFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	err := h.hub.Use(fakePlugin{"half", func(r *Registry) (func(), error) {
		r.OnContext(func(context.Context, ContextIn) (*Contribution, error) {
			return &Contribution{Lines: []string{"半截注册的"}}, nil
		})
		return nil, errors.New("配置不对")
	}})
	if err == nil {
		t.Fatal("Setup 出错应该报出来")
	}
	h.use(contextPlugin("ok", func(context.Context, ContextIn) (*Contribution, error) { return nil, nil }))
	if h.hub.Use(contextPlugin("ok", nil)) == nil {
		t.Fatal("同一个 id 启用两次应该报错")
	}
	h.send("你好")
	if strings.Contains(h.body(0), "半截注册的") {
		t.Fatalf("Setup 出错的插件注册的钩子应该撤掉：%s", h.body(0))
	}
}

// A13（外部审计 F02）：同一个平台群，主人问 → 群友问 → 主人问。只有主人能看的那条事实只进主人那一档的实例；
// 群友那一档看得到主人公开说的话和她公开回的，看不到那条事实；两档各自只追加、前缀都保得住；工具一个都没有
func TestAudiencePerTurnScopes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	const secret = "主人私事：周三下午看牙医"
	h.use(contextPlugin("memory", func(_ context.Context, in ContextIn) (*Contribution, error) {
		if in.Scope == "owner-asked" && in.Audience.OwnerAsked {
			return &Contribution{Lines: []string{secret}}, nil
		}
		return &Contribution{Lines: []string{"群里的公开事：周五聚餐"}}, nil
	}))
	conv := "group:test-1"
	h.say(conv, "owner", "owner", "我那件事是周几来着", true, "owner-asked")
	h.say(conv, "human", "m1", "她这周哪天有空", false, "public")
	h.say(conv, "owner", "owner", "那周五呢", true, "owner-asked")

	if !strings.Contains(h.body(0), secret) {
		t.Fatalf("主人问的那一轮应该给那条事实：%s", h.body(0))
	}
	member := h.body(1)
	if strings.Contains(member, "周三下午看牙医") {
		t.Fatalf("群友那一轮的请求里出现了只有主人能看的事实：%s", member)
	}
	for _, want := range []string{"我那件事是周几来着", "在呢", "群里的公开事：周五聚餐"} {
		if !strings.Contains(member, want) {
			t.Errorf("群友那一轮应该看得到「%s」：%s", want, member)
		}
	}
	owner2 := h.body(2)
	for _, want := range []string{secret, "她这周哪天有空"} {
		if !strings.Contains(owner2, want) {
			t.Errorf("主人再问的那一轮应该有「%s」（自己那档的事实、追上来的群友那轮）：%s", want, owner2)
		}
	}
	if !isPrefix(h.messages(0), h.messages(2)) {
		t.Errorf("主人那一档的第二轮不是在第一轮后面追加的（缓存会断）")
	}
	for i := 0; i < 3; i++ {
		if strings.Contains(h.body(i), `"powershell"`) || strings.Contains(h.body(i), `"write"`) {
			t.Errorf("平台群里不该有本地工具：第 %d 次请求 %s", i+1, h.body(i))
		}
	}
	// 群友那一档的实例重建（从库里读历史）以后，那条事实照样没有：参考信息本来就不进库
	h.hub.mu.Lock()
	for k := range h.hub.live {
		if strings.Contains(k, "|public|") {
			delete(h.hub.live, k)
		}
	}
	h.hub.mu.Unlock()
	h.say(conv, "human", "m2", "我也问问", false, "public")
	if strings.Contains(h.body(3), "周三下午看牙医") {
		t.Fatalf("重建的群友实例里出现了只有主人能看的事实：%s", h.body(3))
	}
	if !strings.Contains(h.body(3), "那周五呢") {
		t.Errorf("重建的群友实例应该看得到主人公开说的：%s", h.body(3))
	}
}

// A13：换了听众绝不复用旧实例（工具清单跟着实例走）——同一个会话、同一档，听众不同就是两个实例
func TestAudienceChangeNewInstance(t *testing.T) {
	t.Parallel()
	a := Input{CharacterID: "x", Conversation: "c", Scope: "s", Audience: audience.Audience{Channel: "console", PrivateOK: true}}
	b := a
	b.Audience.PrivateOK = false
	if liveKey(a) == liveKey(b) {
		t.Fatal("听众不同，实例的键应该不同")
	}
	p := newPlugins(context.Background(), HookTimeouts{}, nil)
	ownerTools, _ := p.toolsFor(a.Audience)
	otherTools, _ := p.toolsFor(b.Audience)
	if len(ownerTools) == 0 || len(otherTools) != 0 {
		t.Fatal("只有主人在本地主窗口时有内置工具")
	}
	if p := policyAudience(b.Audience); p.Channel != "console" || p.PrivateOk {
		t.Fatalf("工具的关看到的听众不对：%+v", p)
	}
}

// 听众、Addressed 原样交给插件（记忆按它算能看什么）；会话以 Input 的为准；主窗口给的是 Console 听众
func TestPluginGetsAudience(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	var got []ContextIn
	h.use(contextPlugin("aud", func(_ context.Context, in ContextIn) (*Contribution, error) {
		got = append(got, in)
		return nil, nil
	}))
	h.send("你好")
	h.say("qq:group:g9", "human", "p7", "在吗", false, "public")
	if len(got) != 2 {
		t.Fatalf("插件调了 %d 次", len(got))
	}
	want0 := audience.Console("console:" + h.c.CharacterID)
	if c := got[0]; !reflect.DeepEqual(c.Audience, want0) || !c.Addressed {
		t.Fatalf("主窗口给插件的听众不对：%+v，应该是 %+v", c, want0)
	}
	want1 := audience.Audience{Channel: "qq", Conversation: "qq:group:g9", Scene: "group", PersonIDs: []string{"p7"}}
	if c := got[1]; !reflect.DeepEqual(c.Audience, want1) || !c.Addressed || c.Scope != "public" {
		t.Fatalf("平台群给插件的听众不对：%+v，应该是 %+v", c, want1)
	}
}

func TestPluginActivitiesDrainRealExit(t *testing.T) {
	for _, kind := range []string{"context", "compact", "seen", "settled"} {
		t.Run(kind, func(t *testing.T) {
			p, err := paths.Init(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.OpenFresh(p)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := st.Trace.Exec(`CREATE TABLE made_hook_probe(value TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			// 只测钩子与排空，不启动假网关；网络 I/O 会阻止 synctest 推进时钟。
			// 保留真实准入落盘和原来的 10ms，回调进入屏障后才让虚拟时钟到期。
			synctest.Test(t, func(t *testing.T) {
				base, cancelBase := context.WithCancel(context.Background())
				defer cancelBase()
				h := NewHub(base, Config{Data: p, WorkSettings: config.DefaultWork(), Logf: t.Logf,
					Hooks: HookTimeouts{Context: 10 * time.Millisecond, Notify: 10 * time.Millisecond, Compact: 10 * time.Millisecond}}, anyConv{st}, nil, nil)
				const character, conversation = "made-hook-character", "console:made-hook-character"
				entered, released, written := make(chan context.Context, 1), make(chan struct{}), make(chan error, 1)
				release := sync.OnceFunc(func() { close(released) })
				defer release()
				lateWrite := func(ctx context.Context) {
					entered <- ctx
					<-released
					_, err := st.Trace.Exec(`INSERT INTO made_hook_probe(value) VALUES ('made-real-exit')`)
					written <- err
				}
				if err := h.Use(fakePlugin{"tracked-hook", func(r *Registry) (func(), error) {
					switch kind {
					case "context":
						r.OnContext(func(ctx context.Context, _ ContextIn) (*Contribution, error) { lateWrite(ctx); return nil, nil })
					case "compact":
						r.OnBeforeCompact(func(ctx context.Context, _ BeforeCompact) (int64, bool, error) { lateWrite(ctx); return 0, false, nil })
					case "seen":
						r.OnSeen(func(ctx context.Context, _ Seen) { lateWrite(ctx) })
					case "settled":
						r.OnSettled(func(ctx context.Context, _ Settled) { lateWrite(ctx) })
					}
					return nil, nil
				}}); err != nil {
					t.Fatal(err)
				}
				returned := make(chan struct{})
				go func() {
					defer close(returned)
					switch kind {
					case "context":
						h.plugins.contextContributions(base, ContextIn{CharacterID: character, Conversation: conversation}, func(string) map[string]any { return nil })
					case "compact":
						h.plugins.beforeCompact(base, BeforeCompact{CharacterID: character, Conversation: conversation})
					case "seen":
						h.plugins.notifySeen(Seen{CharacterID: character, Conversation: conversation, Seq: 1})
					case "settled":
						h.plugins.settle(Settled{CharacterID: character, Conversation: conversation, Outcome: OutcomeDone})
					}
				}()
				callbackCtx := receive(t, entered)
				if kind != "compact" {
					receive(t, callbackCtx.Done())
				}
				receive(t, returned)
				if kind == "compact" && callbackCtx.Err() != nil {
					t.Fatal("停止等待不该取消压缩钩子的真实执行")
				}
				h.Seal()
				cancelBase()
				receive(t, callbackCtx.Done())
				short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
				err := h.Drain(short)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("超时后的实际回调未记入排空：%v", err)
				}
				release()
				if err := receive(t, written); err != nil {
					t.Fatalf("真实回调退出前库已不可写：%v", err)
				}
				drain, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if err := h.Drain(drain); err != nil {
					t.Fatal(err)
				}
				var rows int
				if err := st.Trace.QueryRow(`SELECT COUNT(*) FROM made_hook_probe`).Scan(&rows); err != nil || rows != 1 {
					t.Fatalf("迟到写入丢失 rows=%d err=%v", rows, err)
				}
			})
		})
	}
}
