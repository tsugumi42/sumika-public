package snowluma

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 发送测试用的号、群、句子都是编的。文字里带一个不会出现在日志模板里的片段，用来抓「原文进了日志」。
const (
	sendAccount = "90001"
	sendGroup   = "90002"
	sendFriend  = "90003"
	sendText    = "在的-编的一句"
	sendToken   = "编的令牌-发送"
)

var echoRe = regexp.MustCompile(`^s[1-9][0-9]*$`)

func groupConv() types.Conversation {
	return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: sendGroup}
}

func privateConv() types.Conversation {
	return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: sendFriend, Self: sendAccount}
}

// sentAction 是假协议端收到的一次 action。
type sentAction struct {
	Action string
	Echo   string
	Params map[string]json.RawMessage
	Raw    string
}

// replyFunc 决定怎么回这一次 action。respond 为 false 就不回（用来测超时）。
// body 是一个 JSON 对象，可以不带 echo，假协议端会把这次的 echo 填进去。
type replyFunc func(sentAction) (body string, respond bool)

// actionPeer 是会按脚本回 action 的假协议端。
type actionPeer struct {
	srv   *httptest.Server
	reply replyFunc

	mu    sync.Mutex
	got   []sentAction
	conns []*websocket.Conn

	metaMu        sync.Mutex
	loginQ        []string // 逐次弹出，最后一项一直用
	statusQ       []string // data 对象原文，同样最后一项一直用
	loginBody     string   // 非空则整段当 get_login_info 的回包（不再包 user_id）
	loginSilence  bool
	statusSilence bool
	accepts       atomic.Int32
}

func newActionPeer(t *testing.T, reply replyFunc) *actionPeer {
	t.Helper()
	p := &actionPeer{reply: reply}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(func() {
		p.srv.Close()
		p.mu.Lock()
		conns := append([]*websocket.Conn(nil), p.conns...)
		p.mu.Unlock()
		for _, c := range conns {
			c.CloseNow()
		}
	})
	return p
}

func (p *actionPeer) url() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

func (p *actionPeer) serve(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+sendToken {
		http.Error(w, "令牌不对", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.accepts.Add(1)
	p.mu.Unlock()
	p.writeHeartbeat(c)
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		act := parseAction(data)
		p.mu.Lock()
		p.got = append(p.got, act)
		reply := p.reply
		p.mu.Unlock()
		if body, ok, handled := p.metaReply(act.Action); handled {
			if !ok {
				continue
			}
			p.write(c, withEcho(body, act.Echo))
			continue
		}
		if reply == nil {
			continue
		}
		body, ok := reply(act)
		if !ok {
			continue
		}
		p.write(c, withEcho(body, act.Echo))
	}
}

func (p *actionPeer) writeHeartbeat(c *websocket.Conn) {
	body := fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":1761643200,"self_id":%s,"interval":30000}`, sendAccount)
	p.write(c, []byte(body))
}

func (p *actionPeer) write(c *websocket.Conn, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), testPatience)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, body)
}

func (p *actionPeer) metaReply(action string) (string, bool, bool) {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	switch action {
	case "get_login_info":
		if p.loginSilence {
			return "", false, true
		}
		if p.loginBody != "" {
			return p.loginBody, true, true
		}
		user := sendAccount
		if len(p.loginQ) > 0 {
			user = p.loginQ[0]
			if len(p.loginQ) > 1 {
				p.loginQ = p.loginQ[1:]
			}
		}
		return `{"status":"ok","retcode":0,"data":{"user_id":` + user + `}}`, true, true
	case "get_status":
		if p.statusSilence {
			return "", false, true
		}
		body := `{"online":true,"good":true}`
		if len(p.statusQ) > 0 {
			body = p.statusQ[0]
			if len(p.statusQ) > 1 {
				p.statusQ = p.statusQ[1:]
			}
		}
		return `{"status":"ok","retcode":0,"data":` + body + `}`, true, true
	default:
		return "", false, false
	}
}

func (p *actionPeer) setLoginQ(ids ...string) {
	p.metaMu.Lock()
	p.loginQ = append([]string(nil), ids...)
	p.metaMu.Unlock()
}

func (p *actionPeer) setStatusQ(bodies ...string) {
	p.metaMu.Lock()
	p.statusQ = append([]string(nil), bodies...)
	p.metaMu.Unlock()
}

func (p *actionPeer) setLoginBody(body string) {
	p.metaMu.Lock()
	p.loginBody = body
	p.metaMu.Unlock()
}

func (p *actionPeer) count(name string) int {
	n := 0
	for _, act := range p.rawActions() {
		if act.Action == name {
			n++
		}
	}
	return n
}

func (p *actionPeer) nConns() int { return int(p.accepts.Load()) }

func isOutboundAction(action string) bool {
	switch action {
	case "send_group_msg", "send_private_msg", "send_msg", "group_poke", "friend_poke", "send_group_forward_msg", "send_private_forward_msg", "set_msg_emoji_like", "delete_msg":
		return true
	default:
		return false
	}
}

func parseAction(data []byte) sentAction {
	var o map[string]json.RawMessage
	_ = json.Unmarshal(data, &o)
	act := sentAction{Raw: string(data), Params: map[string]json.RawMessage{}}
	_ = json.Unmarshal(o["action"], &act.Action)
	_ = json.Unmarshal(o["echo"], &act.Echo)
	_ = json.Unmarshal(o["params"], &act.Params)
	return act
}

func withEcho(body, echo string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	eb, _ := json.Marshal(echo)
	m["echo"] = eb
	out, _ := json.Marshal(m)
	return out
}

func (p *actionPeer) rawActions() []sentAction {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sentAction(nil), p.got...)
}

func (p *actionPeer) actions() []sentAction {
	var out []sentAction
	for _, act := range p.rawActions() {
		if isOutboundAction(act.Action) {
			out = append(out, act)
		}
	}
	return out
}

func (p *actionPeer) waitActions(t *testing.T, n int) []sentAction {
	t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		got := p.actions()
		if len(got) >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	fail(t, "5 秒内只收到 %d 次 action，要 %d 次", len(p.actions()), n)
	return nil
}

func (p *actionPeer) send(t *testing.T, frame string) {
	t.Helper()
	p.mu.Lock()
	var c *websocket.Conn
	if len(p.conns) > 0 {
		c = p.conns[len(p.conns)-1]
	}
	p.mu.Unlock()
	if c == nil {
		t.Fatal("假协议端还没有连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testPatience)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("假协议端发帧：%v", err)
	}
}

func (p *actionPeer) dropCurrent() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) == 0 {
		return
	}
	p.conns[len(p.conns)-1].CloseNow()
}

// sendEnv 是一条假连接加一个真出口。档位不设，出口默认说话，才会真的调适配器。
type sendEnv struct {
	t      *testing.T
	clk    *fake.Clock
	peer   *actionPeer
	ad     *Adapter
	st     *store.Store
	out    *channel.Outlet
	sink   *testSink
	hook   *hookSink
	alerts *testAlerts

	mu   sync.Mutex
	logs []string
}

// hookSink 把后来才接上的入库一站插到入站前面。Status 仍进 testSink，测试靠它等「连上」。
// intakeSink 补上入库一站没有的 Notice、Status：回声测试只关心消息。
type intakeSink struct{ *channel.Intake }

func (intakeSink) Notice(context.Context, types.Notice) error { return nil }
func (intakeSink) Status(context.Context, types.Status) error { return nil }

type hookSink struct {
	*testSink
	mu sync.Mutex
	in types.Sink
}

func (h *hookSink) Inbound(ctx context.Context, in types.Inbound) error {
	h.mu.Lock()
	extra := h.in
	h.mu.Unlock()
	if extra != nil {
		return extra.Inbound(ctx, in)
	}
	return h.testSink.Inbound(ctx, in)
}

func (h *hookSink) set(in types.Sink) {
	h.mu.Lock()
	h.in = in
	h.mu.Unlock()
}

func (e *sendEnv) logf(format string, args ...any) {
	e.mu.Lock()
	e.logs = append(e.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	e.mu.Unlock()
}

// levelf 给 Warnf、Errorf 用：行首加级别词再进同一份日志，测试能断言级别，原有的子串断言不受影响
func (e *sendEnv) levelf(level string) func(string, ...any) {
	return func(format string, args ...any) { e.logf(level+" "+format, args...) }
}

func newSendEnv(t *testing.T, reply replyFunc) *sendEnv {
	return newSendEnvSetup(t, reply, nil)
}

func newSendEnvSetup(t *testing.T, reply replyFunc, setup func(*actionPeer)) *sendEnv {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	clk := fake.NewClock(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	peer := newActionPeer(t, reply)
	if setup != nil {
		setup(peer)
	}
	base := newTestSink()
	hook := &hookSink{testSink: base}
	alerts := newTestAlerts()
	e := &sendEnv{t: t, clk: clk, peer: peer, st: st, sink: base, hook: hook, alerts: alerts}
	cfg := Config{URL: peer.url(), Token: sendToken, Account: sendAccount, Logf: e.logf, Warnf: e.levelf("WARN"), Errorf: e.levelf("ERROR")}
	ad, err := NewWS(cfg, clk, alerts)
	if err != nil {
		t.Fatal(err)
	}
	ad.jitter = func(d time.Duration) time.Duration { return d }
	e.ad = ad
	e.out, err = channel.NewOutlet(channel.OutletConfig{
		Stores:   store.Single(st),
		Roster:   channel.Roster{Accounts: []channel.Account{{Channel: "qq", ID: sendAccount, Character: "ca"}}},
		Adapters: []types.Adapter{ad},
		Clock:    clk,
		Logf:     e.logf,
		GuardFor: func(string) channel.Guard { return mediaGuard{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ad.Run(ctx, e.hook) }()
	recvStatus(t, e.sink, types.StatusConnected)
	t.Cleanup(func() {
		cancel()
		waitRun(t, done)
		finished := make(chan struct{})
		go func() { e.out.Close(); close(finished) }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			select {
			case <-finished:
				st.Close()
				return
			default:
			}
			if time.Now().After(deadline) {
				clk.Advance(time.Minute)
				deadline = time.Now().Add(2 * time.Second)
			}
			time.Sleep(time.Millisecond)
		}
	})
	return e
}

func (e *sendEnv) sendParts(conv types.Conversation, parts []types.Part) string {
	e.t.Helper()
	id, err := e.out.Send(context.Background(), "ca", types.OriginCharacter, conv, parts)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *sendEnv) waitPart(id, state string, attempts int) store.Part {
	e.t.Helper()
	deadline := time.Now().Add(testPatience)
	var last store.Part
	for time.Now().Before(deadline) {
		p, err := e.st.GetPlan(id)
		if err == nil && len(p.Parts) == 1 {
			last = p.Parts[0]
			if last.State == state && last.Attempts == attempts {
				return last
			}
		}
		time.Sleep(time.Millisecond)
	}
	fail(e.t, "段停在 state=%s attempts=%d，要 state=%s attempts=%d", last.State, last.Attempts, state, attempts)
	return store.Part{}
}

func (e *sendEnv) waitPlan(id, state string) store.Plan {
	e.t.Helper()
	deadline := time.Now().Add(testPatience)
	var last store.Plan
	for time.Now().Before(deadline) {
		p, err := e.st.GetPlan(id)
		if err == nil {
			last = p
			if p.State == state {
				return p
			}
		}
		time.Sleep(time.Millisecond)
	}
	fail(e.t, "计划停在 %s，要 %s", last.State, state)
	return store.Plan{}
}

func (e *sendEnv) countMsgs(platformID string) int {
	e.t.Helper()
	var n int
	var err error
	if platformID == "" {
		err = e.st.Events.QueryRow(`SELECT count(*) FROM channel_msgs`).Scan(&n)
	} else {
		err = e.st.Events.QueryRow(`SELECT count(*) FROM channel_msgs WHERE platform_id = ?`, platformID).Scan(&n)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *sendEnv) logsText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.logs, "\n")
}

func fixedReply(body string) replyFunc {
	return func(sentAction) (string, bool) { return body, true }
}

func noReply(sentAction) (string, bool) { return "", false }

// assertOnce 检查这些 action 的 echo 不重复、不是计划号，并且拨过重试间隔以后条数不变。
func (e *sendEnv) assertNoResend(n int) {
	e.t.Helper()
	got := e.peer.waitActions(e.t, n)
	if len(got) != n {
		e.t.Fatalf("action %d 次，要 %d 次：%s", len(got), n, got[len(got)-1].Raw)
	}
	seen := map[string]bool{}
	for _, act := range got {
		if !echoRe.MatchString(act.Echo) {
			e.t.Errorf("echo %q 不是适配器自己的序号", act.Echo)
		}
		if seen[act.Echo] {
			e.t.Errorf("echo %q 重复了，重试必须换一个", act.Echo)
		}
		seen[act.Echo] = true
		if strings.Contains(act.Echo, ":") || strings.Contains(act.Raw, sendText) && act.Action != "send_group_msg" && act.Action != "send_private_msg" {
			e.t.Errorf("action 不像话：%s", act.Raw)
		}
	}
	e.clk.Advance(3 * time.Second)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := e.peer.actions(); len(got) != n {
			e.t.Fatalf("不该重发，action 变成了 %d 次", len(got))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertTextAction(t *testing.T, act sentAction, action, idKey, id string) {
	t.Helper()
	if act.Action != action {
		t.Fatalf("action = %s，要 %s；帧 %s", act.Action, action, act.Raw)
	}
	if string(act.Params[idKey]) != id {
		t.Fatalf("%s = %s，要 %s", idKey, act.Params[idKey], id)
	}
	var segs []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(act.Params["message"], &segs); err != nil || len(segs) != 1 || segs[0].Type != "text" || segs[0].Data.Text != sendText {
		t.Fatalf("message 段：%s err=%v", act.Params["message"], err)
	}
	if strings.Contains(act.Raw, "auto_escape") {
		t.Fatalf("不该带 auto_escape：%s", act.Raw)
	}
	if !echoRe.MatchString(act.Echo) {
		t.Fatalf("echo %q", act.Echo)
	}
}

func assertQuietLogs(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, sendToken) || strings.Contains(logs, sendText) || strings.Contains(logs, sendAccount) {
		t.Fatalf("日志里有令牌、原文或号：%s", logs)
	}
}

func TestTextConfirmWritesPlatformID(t *testing.T) {
	e := newSendEnv(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-7654321}}`))
	id := e.sendParts(groupConv(), []types.Part{types.Message(sendText)})
	p := e.waitPlan(id, store.PlanDone)
	act := e.peer.waitActions(t, 1)
	if len(act) != 1 {
		t.Fatalf("确认以后又写了 %d 次", len(act))
	}
	assertTextAction(t, act[0], "send_group_msg", "group_id", sendGroup)
	pt := p.Parts[0]
	if pt.State != store.PartConfirmed || pt.PlatformID != "-7654321" || pt.Via != types.ViaReply {
		t.Fatalf("段：%+v", pt)
	}
	if n := e.countMsgs("-7654321"); n != 1 {
		t.Fatalf("channel_msgs 里 -7654321 有 %d 行，要 1 行", n)
	}
	if e.countMsgs("") != 1 {
		t.Fatalf("channel_msgs 行数 %d", e.countMsgs(""))
	}
	assertQuietLogs(t, e.logsText())
}

func TestTextStringMessageID(t *testing.T) {
	e := newSendEnv(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":"-7654321"}}`))
	id := e.sendParts(privateConv(), []types.Part{types.Message(sendText)})
	p := e.waitPlan(id, store.PlanDone)
	act := e.peer.waitActions(t, 1)
	assertTextAction(t, act[0], "send_private_msg", "user_id", sendFriend)
	if p.Parts[0].PlatformID != "-7654321" {
		t.Fatalf("字符串消息号：%q", p.Parts[0].PlatformID)
	}
	if n := e.countMsgs("-7654321"); n != 1 {
		t.Fatalf("channel_msgs %d", n)
	}
}

// 协议端回 failed 是明确拒绝：只发一次、段失败、不重试（原样再发只会再被拒，还会让 QQ 多记几次）。
// 本地原因（没连上、没写出去）的重试在出口的测试里（TestRetryOnExplicitFailure）。
func TestTextRejectedIsNotRetried(t *testing.T) {
	e := newSendEnv(t, fixedReply(`{"status":"failed","retcode":0,"wording":"content rejected","data":{"message_id":-7654321}}`))
	id := e.sendParts(groupConv(), []types.Part{types.Message(sendText)})
	e.waitPart(id, store.PartFailed, 1)
	got := e.peer.waitActions(t, 1)
	assertTextAction(t, got[0], "send_group_msg", "group_id", sendGroup)
	p := e.waitPlan(id, store.PlanFailed)
	if p.Parts[0].PlatformID != "" {
		t.Fatalf("失败不该带消息号：%q", p.Parts[0].PlatformID)
	}
	if e.countMsgs("") != 0 {
		t.Fatalf("失败写了 channel_msgs：%d", e.countMsgs(""))
	}
	e.clk.Advance(3 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if len(e.peer.actions()) != 1 {
		t.Fatalf("拒绝后又发了：%d", len(e.peer.actions()))
	}
}

func TestTextUnknownWritesOnce(t *testing.T) {
	cases := []struct {
		name  string
		reply replyFunc
	}{
		{"retcode 缺省", fixedReply(`{"status":"ok","data":{"message_id":-7654321}}`)},
		{"只有 echo 和 message_id", fixedReply(`{"message_id":-7654321}`)},
		{"retcode 非 0", fixedReply(`{"status":"ok","retcode":1,"data":{"message_id":-7654321}}`)},
		{"retcode 是字符串 0", fixedReply(`{"status":"ok","retcode":"0","data":{"message_id":-7654321}}`)},
		{"retcode 是 null", fixedReply(`{"status":"ok","retcode":null,"data":{"message_id":-7654321}}`)},
		{"retcode 是 0.0", fixedReply(`{"status":"ok","retcode":0.0,"data":{"message_id":-7654321}}`)},
		{"retcode 是布尔", fixedReply(`{"status":"ok","retcode":true,"data":{"message_id":-7654321}}`)},
		{"message_id 是 0", fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":0}}`)},
		{"message_id 是浮点", fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":1.5}}`)},
		{"message_id 是空串", fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":""}}`)},
		{"message_id 是字符串 0", fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":"0"}}`)},
		{"message_id 缺字段", fixedReply(`{"status":"ok","retcode":0,"data":{}}`)},
		{"data 是 null", fixedReply(`{"status":"ok","retcode":0,"data":null}`)},
		{"写出后不回包", noReply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSendEnv(t, tc.reply)
			id := e.sendParts(groupConv(), []types.Part{types.Message(sendText)})
			if tc.reply == nil {
				t.Fatal("reply 是空的")
			}
			e.peer.waitActions(t, 1)
			if tc.name == "写出后不回包" {
				e.clk.WaitTimer(t, callTimeout)
				e.clk.Advance(callTimeout)
			}
			p := e.waitPlan(id, store.PlanUnknown)
			if p.Parts[0].State != store.PartUnknown || p.Parts[0].PlatformID != "" {
				t.Fatalf("段：%+v", p.Parts[0])
			}
			if e.countMsgs("") != 0 {
				t.Fatalf("未知写了 channel_msgs")
			}
			e.assertNoResend(1)
			assertTextAction(t, e.peer.actions()[0], "send_group_msg", "group_id", sendGroup)
		})
	}
}

func TestPokeReceipts(t *testing.T) {
	cases := []struct {
		name    string
		scene   string
		reply   replyFunc
		state   string
		plan    string
		actions int
		retries bool
	}{
		{"group_poke 确认", "group", fixedReply(`{"status":"ok","retcode":0,"data":null}`), store.PartConfirmed, store.PlanDone, 1, false},
		{"group_poke 失败", "group", fixedReply(`{"status":"failed","retcode":1,"wording":"rejected"}`), store.PartFailed, store.PlanFailed, 1, false},
		{"group_poke 不回包", "group", noReply, store.PartUnknown, store.PlanUnknown, 1, false},
		{"friend_poke 确认", "private", fixedReply(`{"status":"ok","retcode":0,"data":null}`), store.PartConfirmed, store.PlanDone, 1, false},
		{"friend_poke 失败", "private", fixedReply(`{"status":"failed","retcode":1,"wording":"rejected"}`), store.PartFailed, store.PlanFailed, 1, false},
		{"friend_poke 不回包", "private", noReply, store.PartUnknown, store.PlanUnknown, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSendEnv(t, tc.reply)
			conv := groupConv()
			wantAction := "group_poke"
			if tc.scene == "private" {
				conv = privateConv()
				wantAction = "friend_poke"
			}
			id := e.sendParts(conv, []types.Part{{Kind: types.PartPoke, Ref: sendFriend}})
			if tc.retries {
				for attempt := 1; attempt <= 3; attempt++ {
					if attempt > 1 {
						e.clk.WaitTimer(t, time.Second)
						e.clk.Advance(time.Second)
					}
					state := store.PartRetrying
					if attempt == 3 {
						state = store.PartFailed
					}
					e.waitPart(id, state, attempt)
				}
			} else if tc.name == "group_poke 不回包" || tc.name == "friend_poke 不回包" {
				e.peer.waitActions(t, 1)
				e.clk.WaitTimer(t, callTimeout)
				e.clk.Advance(callTimeout)
			}
			p := e.waitPlan(id, tc.plan)
			pt := p.Parts[0]
			if pt.State != tc.state || pt.PlatformID != "" {
				t.Fatalf("段：%+v", pt)
			}
			if e.countMsgs("") != 0 {
				t.Fatalf("戳一戳写了 channel_msgs：%d", e.countMsgs(""))
			}
			got := e.peer.waitActions(t, tc.actions)
			if len(got) != tc.actions {
				t.Fatalf("action %d 次，要 %d", len(got), tc.actions)
			}
			for _, act := range got {
				if act.Action != wantAction {
					t.Fatalf("action %s，要 %s；%s", act.Action, wantAction, act.Raw)
				}
				if string(act.Params["user_id"]) != sendFriend {
					t.Fatalf("user_id %s", act.Params["user_id"])
				}
				if tc.scene == "group" && string(act.Params["group_id"]) != sendGroup {
					t.Fatalf("group_id %s", act.Params["group_id"])
				}
				if tc.scene == "private" && len(act.Params["group_id"]) != 0 {
					t.Fatalf("私聊戳不该带 group_id：%s", act.Raw)
				}
			}
			if !tc.retries {
				e.assertNoResend(1)
			}
		})
	}
}

func TestPokeMessageIDStaysEmpty(t *testing.T) {
	e := newSendEnv(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-7654321}}`))
	id := e.sendParts(groupConv(), []types.Part{{Kind: types.PartPoke, Ref: sendFriend}})
	p := e.waitPlan(id, store.PlanDone)
	if p.Parts[0].State != store.PartConfirmed || p.Parts[0].PlatformID != "" {
		t.Fatalf("段：%+v", p.Parts[0])
	}
	if e.countMsgs("-7654321") != 0 || e.countMsgs("") != 0 {
		t.Fatalf("戳一戳的消息号不该进 channel_msgs")
	}
}

func TestWriteThenDisconnectIsUnknown(t *testing.T) {
	e := newSendEnv(t, noReply)
	id := e.sendParts(groupConv(), []types.Part{types.Message(sendText)})
	e.peer.waitActions(t, 1)
	e.peer.dropCurrent()
	p := e.waitPlan(id, store.PlanUnknown)
	if p.Parts[0].Err != "写出以后连接断了" {
		t.Fatalf("段错误：%q", p.Parts[0].Err)
	}
	e.assertNoResend(1)
}

func TestSendReturnsWhenCtxCancelled(t *testing.T) {
	peer := newActionPeer(t, noReply)
	// 不用假钟：取消没接上的话会真的等到 10 秒，测试才能在 2 秒内判红。
	ad, err := NewWS(Config{URL: peer.url(), Token: sendToken, Account: sendAccount}, nil, newTestAlerts())
	if err != nil {
		t.Fatal(err)
	}
	ad.jitter = func(d time.Duration) time.Duration { return d }
	sink := newTestSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ad.Run(ctx, sink) }()
	t.Cleanup(func() {
		cancel()
		waitRun(t, done)
	})
	recvStatus(t, sink, types.StatusConnected)

	callCtx, callCancel := context.WithCancel(context.Background())
	type result struct {
		r   types.Receipt
		err error
	}
	ch := make(chan result, 1)
	started := time.Now()
	go func() {
		r, err := ad.Send(callCtx, sendAccount, types.Part{
			ID: "p1", Conv: groupConv(), Kind: types.PartMessage, Body: types.TextBody(sendText),
		})
		ch <- result{r, err}
	}()
	peer.waitActions(t, 1)
	callCancel()
	select {
	case got := <-ch:
		if got.err != nil || got.r.Status != types.ReceiptUnknown || got.r.Err != "取消了等待" || got.r.PartID != "p1" || got.r.Validate() != nil {
			t.Fatalf("取消后的回执：%#v err=%v", got.r, got.err)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("取消后等了 %v 才返回", time.Since(started))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Send 没有返回")
	}
	if len(peer.actions()) != 1 {
		t.Fatalf("取消后又写了 action：%d", len(peer.actions()))
	}
}

func TestUnmatchedEchoIsDropped(t *testing.T) {
	e := newSendEnv(t, noReply)
	// 客户端握手成功时服务端可能还没登记连接；首帧在登记后发出，拿它作屏障。
	recvStatus(t, e.sink, types.StatusHeartbeat)
	e.peer.send(t, `{"echo":"不是这次","status":"ok","retcode":0,"message_type":"group","self_id":90001,"user_id":90003,"group_id":90002,"message_id":-1,"message":[{"type":"text","data":{"text":"在的-编的一句"}}]}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(e.logsText(), "对不上的 action 回包丢掉了") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(e.logsText(), "WARN 对不上的 action 回包丢掉了") { // 对不上的回包是丢数据但系统自己接上：WARN
		t.Fatalf("没有丢掉对不上的回包，日志：%s", e.logsText())
	}
	select {
	case in := <-e.sink.inbound:
		t.Fatalf("对不上的回包被当成消息了：%+v", in)
	case <-time.After(50 * time.Millisecond):
	}
	assertQuietLogs(t, e.logsText())
}

func TestEchoBeforeReplyWritesOnce(t *testing.T) {
	e := newSendEnv(t, noReply)
	in, err := channel.NewIntake(channel.Config{
		Stores: store.Single(e.st),
		Roster: channel.Roster{Accounts: []channel.Account{{Channel: "qq", ID: sendAccount, Character: "ca"}}},
		Echo:   e.out,
		Now:    e.clk.Now,
		Logf:   e.logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)
	e.hook.set(intakeSink{in})

	id := e.sendParts(groupConv(), []types.Part{types.Message(sendText)})
	act := e.peer.waitActions(t, 1)
	e.peer.send(t, `{"post_type":"message_sent","message_type":"group","time":1761643200,"self_id":90001,"user_id":90001,"group_id":90002,"message_id":"7654321","sender":{"user_id":90001,"nickname":"编"},"message":[{"type":"text","data":{"text":"在的-编的一句"}}]}`)
	p := e.waitPlan(id, store.PlanDone)
	pt := p.Parts[0]
	if pt.State != store.PartConfirmed || pt.Via != types.ViaEcho || pt.PlatformID != "7654321" {
		t.Fatalf("回声认领后的段：%+v", pt)
	}
	if n := e.countMsgs("7654321"); n != 1 {
		t.Fatalf("回声的号该进 channel_msgs：%d", n)
	}
	// 回包晚到：不能再写一次 action，也不能改成回包里的另一个号
	e.peer.send(t, string(withEcho(`{"status":"ok","retcode":0,"data":{"message_id":-7654321}}`, act[0].Echo)))
	time.Sleep(80 * time.Millisecond)
	if got := e.peer.actions(); len(got) != 1 {
		t.Fatalf("回声先到以后又写了 action：%d", len(got))
	}
	p = e.waitPlan(id, store.PlanDone)
	if p.Parts[0].PlatformID != "7654321" || p.Parts[0].Via != types.ViaEcho {
		t.Fatalf("迟到回包改了段：%+v", p.Parts[0])
	}
	if e.countMsgs("-7654321") != 0 {
		t.Fatalf("迟到回包的号不该进 channel_msgs")
	}
	assertQuietLogs(t, e.logsText())
}
