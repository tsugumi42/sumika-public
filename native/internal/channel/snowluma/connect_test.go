package snowluma

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 测试里的号、令牌、名字一律编的（AGENTS「验收」）。

const testToken = "编的令牌-不进日志"

type testSink struct {
	inbound  chan types.Inbound
	notices  chan types.Notice
	statuses chan types.Status
}

func newTestSink() *testSink {
	return &testSink{
		inbound:  make(chan types.Inbound, 32),
		notices:  make(chan types.Notice, 32),
		statuses: make(chan types.Status, 32),
	}
}

func (s *testSink) Inbound(_ context.Context, in types.Inbound) error {
	s.inbound <- in
	return nil
}
func (s *testSink) Notice(_ context.Context, n types.Notice) error {
	s.notices <- n
	return nil
}
func (s *testSink) Status(_ context.Context, st types.Status) error {
	s.statuses <- st
	return nil
}

// fail 是等待超时的兜底：先把所有协程的栈打出来再让测试失败（通用规矩"超时兜底要打协程栈"）。
// 卡住时只有一行 Fatal 看不出谁堵着；栈里能看出 runWS 停在哪个 select、假协议端的读协程还在不在。
func fail(t *testing.T, format string, args ...any) {
	t.Helper()
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	t.Fatalf(format+"\n\n协程栈：\n%s", append(args, buf[:n])...)
}

func recvInbound(t *testing.T, s *testSink) types.Inbound {
	t.Helper()
	select {
	case in := <-s.inbound:
		return in
	case <-time.After(testPatience):
		fail(t, "等不到消息")
		return types.Inbound{}
	}
}

// testPatience 是等连接、等消息、等状态的上限，只防卡死：连接要真走回环网络，协程在机器忙时停几秒很常见。
const testPatience = time.Minute

func recvStatus(t *testing.T, s *testSink, kind string) types.Status {
	t.Helper()
	deadline := time.After(testPatience)
	for {
		select {
		case st := <-s.statuses:
			if st.Kind == kind {
				return st
			}
		case <-deadline:
			fail(t, "等不到 %s 状态", kind)
			return types.Status{}
		}
	}
}

type testAlerts struct {
	mu      sync.Mutex
	raised  []alert.Alert
	cleared []string
	notes   []alert.Note
	ch      chan struct{}
}

func newTestAlerts() *testAlerts { return &testAlerts{ch: make(chan struct{}, 32)} }

func (a *testAlerts) Raise(x alert.Alert) {
	a.mu.Lock()
	a.raised = append(a.raised, x)
	a.mu.Unlock()
	select {
	case a.ch <- struct{}{}:
	default:
	}
}

func (a *testAlerts) Clear(key string, note alert.Note) {
	a.mu.Lock()
	a.cleared = append(a.cleared, key)
	a.notes = append(a.notes, note)
	a.mu.Unlock()
	select {
	case a.ch <- struct{}{}:
	default:
	}
}

func (a *testAlerts) counts() (raises, clears int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.raised), len(a.cleared)
}

func (a *testAlerts) waitRaises(t *testing.T, n int) alert.Alert {
	t.Helper()
	deadline := time.NewTimer(testPatience)
	defer deadline.Stop()
	for {
		a.mu.Lock()
		if len(a.raised) >= n {
			x := a.raised[n-1]
			a.mu.Unlock()
			return x
		}
		a.mu.Unlock()
		select {
		case <-a.ch:
		case <-deadline.C:
			r, _ := a.counts()
			fail(t, "5 秒内只 Raise 了 %d 次，要 %d 次", r, n)
		}
	}
}

func (a *testAlerts) waitClears(t *testing.T, n int) (string, alert.Note) {
	t.Helper()
	deadline := time.NewTimer(testPatience)
	defer deadline.Stop()
	for {
		a.mu.Lock()
		if len(a.cleared) >= n {
			k, note := a.cleared[n-1], a.notes[n-1]
			a.mu.Unlock()
			return k, note
		}
		a.mu.Unlock()
		select {
		case <-a.ch:
		case <-deadline.C:
			_, c := a.counts()
			fail(t, "5 秒内只 Clear 了 %d 次，要 %d 次", c, n)
		}
	}
}

// peer 是假协议端：OneBot 正向 WS 的服务端。记下收到的帧（用来断言客户端没发 action）。
type peer struct {
	srv      *httptest.Server
	token    string
	fail     atomic.Bool
	silent   atomic.Bool   // 接下来接的连接装半开：不读、不回关闭帧，一直挂到测试结束
	release  chan struct{} // 测试结束时关掉，放开装半开的处理函数（不然 srv.Close 一直等）
	accepted chan *websocket.Conn
	auth     chan string
	actions  chan string
	accepts  atomic.Int32

	mu    sync.Mutex
	dones []chan struct{}
}

func newPeer(t *testing.T, token string) *peer {
	p := &peer{
		token:    token,
		accepted: make(chan *websocket.Conn, 4),
		auth:     make(chan string, 4),
		actions:  make(chan string, 64),
		release:  make(chan struct{}),
	}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	t.Cleanup(func() { close(p.release) }) // 后注册的先跑：先放开半开的处理函数，再关服务
	return p
}

func (p *peer) url() string {
	return "ws" + strings.TrimPrefix(p.srv.URL, "http")
}

func (p *peer) serve(w http.ResponseWriter, r *http.Request) {
	p.accepts.Add(1)
	// 每次拨号都会进来：通道满了就丢，别把握手卡死（29 秒边界那个测试要拨三十来次）
	select {
	case p.auth <- r.Header.Get("Authorization"):
	default:
	}
	if p.fail.Load() {
		http.Error(w, "编的：协议端不在线", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	done := make(chan struct{})
	p.mu.Lock()
	p.dones = append(p.dones, done)
	p.mu.Unlock()
	defer close(done)
	p.accepted <- c
	if p.silent.Load() {
		<-p.release
		return
	}
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		p.actions <- string(data)
		answerInfo(c, data)
	}
}

func (p *peer) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case c := <-p.accepted:
		return c
	case <-time.After(testPatience):
		fail(t, "等不到适配器的连接")
		return nil
	}
}

func (p *peer) send(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testPatience)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("假协议端发帧：%v", err)
	}
}

// waitReads 等每条已接连接上的读协程退出（客户端关连接以后）。
func (p *peer) waitReads(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	dones := append([]chan struct{}(nil), p.dones...)
	p.mu.Unlock()
	for _, d := range dones {
		select {
		case <-d:
		case <-time.After(testPatience):
			fail(t, "客户端的连接没关，假协议端的读协程没退出")
		}
	}
}

// assertNoActions 说没有发送类 action。连上以后的 get_login_info / get_status 不算发送。
func (p *peer) assertNoActions(t *testing.T) {
	t.Helper()
	for {
		select {
		case f := <-p.actions:
			if isOutboundAction(frameAction(f)) {
				t.Fatalf("假协议端收到了发送 action：%s", f)
			}
		default:
			return
		}
	}
}

func frameAction(frame string) string {
	var o map[string]json.RawMessage
	if json.Unmarshal([]byte(frame), &o) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(o["action"], &s)
	return s
}

// answerInfo 把核验回成配置的号、收发健康，免得连接测试被 10 秒查询超时拆掉。
func answerInfo(c *websocket.Conn, data []byte) {
	var o map[string]json.RawMessage
	if json.Unmarshal(data, &o) != nil {
		return
	}
	var action, echo string
	_ = json.Unmarshal(o["action"], &action)
	_ = json.Unmarshal(o["echo"], &echo)
	var body string
	switch action {
	case "get_login_info":
		body = `{"status":"ok","retcode":0,"data":{"user_id":100}}`
	case "get_status":
		body = `{"status":"ok","retcode":0,"data":{"online":true,"good":true}}`
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, withEcho(body, echo))
}

// 协议端收了连接却一直不回握手：到上限就返回错误，不一直挂着（不然既不重连也不提醒）。
func TestDialGivesUpWhenHandshakeHangs(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // 先放掉卡着的处理函数，srv.Close 才不会等它
	start := time.Now()
	c, err := dialWithin(context.Background(), Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Token: testToken}, 200*time.Millisecond)
	if err == nil {
		c.Close()
		t.Fatal("握手卡住时 Dial 应该返回错误")
	}
	if d := time.Since(start); d > testPatience { // 处理函数一直卡到测试结束：没有上限的改法这里过不去
		t.Fatalf("握手上限 200ms，实际等了 %v", d)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("错误里带了令牌")
	}
}

// 握手上限只管握手：连上以后过了上限，连接照样能收帧。
func TestDialConnectionOutlivesHandshakeLimit(t *testing.T) {
	p := newPeer(t, "")
	c, err := dialWithin(context.Background(), Config{URL: p.url()}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("拨号：%v", err)
	}
	defer c.Close()
	sc := p.accept(t)
	time.Sleep(400 * time.Millisecond)
	p.send(t, sc, `{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":5000}`)
	ctx, cancel := context.WithTimeout(context.Background(), testPatience)
	defer cancel()
	if _, err := c.Read(ctx); err != nil {
		t.Fatalf("过了握手上限以后读帧：%v", err)
	}
}

// 半开：协议端一帧都不回了，也不回关闭帧。看门狗判掉线以后要马上断开旧连接、报断开，不等关闭握手的 5 秒超时。
func TestWSWatchdogDropsHalfOpenWithoutWaiting(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	p.silent.Store(true)
	a := newWS(t, p, clk, newTestAlerts())
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	p.send(t, c, `{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":1000}`)
	recvStatus(t, sink, types.StatusHeartbeat)

	clk.WaitTimer(t, 3*time.Second)
	clk.Advance(3 * time.Second)
	recvStatus(t, sink, types.StatusDisconnected)
	// 不量多快（机器忙时 2、3 秒也常见），看对面收到什么：走关闭握手的话先发关闭帧，半开时干等 websocket 库写死的 5 秒；
	// 直接断开的话对面读到的是连接断了，没有关闭码。
	ctx, stop := context.WithTimeout(context.Background(), testPatience)
	defer stop()
	for { // 前面还有核验登录号的 action 帧没读（半开的对面一直没读），读到断开为止
		if _, _, err := c.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != -1 || ctx.Err() != nil {
				fail(t, "关旧连接走了关闭握手，要直接断开：对面读到 %v", err)
			}
			break
		}
	}
	cancel()
	waitRun(t, done)
	p.assertNoActions(t)
}

// chanConn 是假连接：Read 从 frames 取帧，取到一帧先在 got 上报一声再返回。
// got 只说明帧从连接上取出来了，不说明读协程已经把它交给收帧循环；lag 是在两者之间人为拖一段（机器忙时
// 调度自己会拖），要等"交出去"的测试用它稳定复现这个空档
type chanConn struct {
	adapter *Adapter
	frames  chan []byte
	got     chan struct{}
	closed  chan struct{}
	once    sync.Once
	lag     time.Duration
}

func newChanConn(a *Adapter) *chanConn {
	return &chanConn{adapter: a, frames: make(chan []byte), got: make(chan struct{}, 64), closed: make(chan struct{})}
}

func (c *chanConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case f := <-c.frames:
		c.got <- struct{}{}
		time.Sleep(c.lag)
		return f, nil
	case <-c.closed:
		return nil, errors.New("编的：连接关了")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *chanConn) Close() { c.once.Do(func() { close(c.closed) }) }

func (c *chanConn) Write(_ context.Context, b []byte) error {
	act := parseAction(b)
	var body string
	switch act.Action {
	case "get_login_info":
		body = `{"retcode":0,"data":{"user_id":100}}`
	case "get_status":
		body = `{"retcode":0,"data":{"online":true,"good":true}}`
	default:
		return errors.New("编的连接只回答核验查询")
	}
	c.adapter.takeReply(withEcho(body, act.Echo))
	return nil
}

// send 交一帧给读协程，等它拿到手再返回
func (c *chanConn) send(t *testing.T, frame string) {
	t.Helper()
	select {
	case c.frames <- []byte(frame):
	case <-time.After(testPatience):
		fail(t, "等不到读协程来取帧")
	}
	<-c.got
}

// gatedSink 的 Inbound 进来先报一声，再等测试放行：用来装"接收端卡住了"
type gatedSink struct {
	*testSink
	entered chan struct{}
	gate    chan struct{}
}

func (s *gatedSink) Inbound(ctx context.Context, in types.Inbound) error {
	s.entered <- struct{}{}
	<-s.gate
	return s.testSink.Inbound(ctx, in)
}

// waitReaderHolding 等读协程把读到的那一帧捧在手里：它已经从连接上读出了这一帧，正卡在"交给收帧循环"
// 这一步（收帧循环还陷在接收端里没回来）。chanConn.got 是在 Read 里、读协程拿到帧之前报的，到"交出去"之间
// 还隔着一段调度，机器忙时这一段会拖长；看门狗到点时帧不在手里，就和真断了没区别。
// 测试要等的是这个状态，拿栈认：读协程停在 readFrames 自己的闭包里（不在假连接的 Read 里）、状态是 select。
func waitReaderHolding(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(testPatience)
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			buf = make([]byte, 2*len(buf))
			continue
		}
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			lines := strings.Split(g, "\n")
			if len(lines) > 1 && strings.Contains(lines[0], "[select") && strings.Contains(lines[1], "(*Adapter).readFrames.func") {
				return
			}
		}
		if time.Now().After(deadline) {
			fail(t, "等不到读协程捧着读到的帧卡在交出去这一步（readFrames 的读协程换了写法的话，改这里的认法）")
		}
		time.Sleep(time.Millisecond)
	}
}

// 接收端卡得比看门狗还久，这期间协议端照常发帧：看门狗和读到的帧同时就绪，不能判掉线。
// 不修时每一轮有一半的机会判掉线，跑 20 轮
func TestWSWatchdogIgnoresSlowSink(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	a := newWS(t, newPeer(t, testToken), clk, newTestAlerts())
	conn := newChanConn(a) // 假连接：知道读协程什么时候拿到了帧，不用真实时间去猜（第二轮复核）
	// 帧拿到手到交出去之间拖一段：不等"交出去"就推钟的写法每一轮都会判掉线
	conn.lag = 5 * time.Millisecond
	a.dial = func(context.Context, Config) (Conn, error) { return conn, nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sink := &gatedSink{testSink: newTestSink(), entered: make(chan struct{}), gate: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()

	recvStatus(t, sink.testSink, types.StatusConnected)
	heartbeat := `{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":1000}`
	conn.send(t, heartbeat)
	recvStatus(t, sink.testSink, types.StatusHeartbeat)
	waitWSReady(t, a)
	for i := 0; i < 20; i++ {
		conn.send(t, `{"post_type":"message","message_type":"private","time":100,"self_id":100,"user_id":200,"message_id":300,"message":[]}`)
		select {
		case <-sink.entered: // 消息交到接收端，卡住了；看门狗已经按这一帧重新计了 3 秒
		case <-time.After(testPatience):
			fail(t, "第 %d 轮：消息没交到接收端", i+1)
		}
		conn.send(t, heartbeat) // 读协程拿到了这一帧，接下来只是等着交出去
		waitReaderHolding(t)    // 等到它真的捧在手里，再让看门狗到点
		clk.WaitTimer(t, 3*time.Second)
		clk.Advance(3 * time.Second) // 看门狗到点
		sink.gate <- struct{}{}
		recvInbound(t, sink.testSink)
		deadline := time.After(testPatience)
	wait:
		for {
			select {
			case st := <-sink.statuses:
				if st.Kind == types.StatusDisconnected {
					fail(t, "第 %d 轮：接收端慢、连接是好的，却判了掉线", i+1)
				}
				if st.Kind == types.StatusHeartbeat {
					break wait
				}
			case <-deadline:
				fail(t, "第 %d 轮：卡住期间读到的心跳没交出去", i+1)
			}
		}
	}
	cancel()
	waitRun(t, done)
}

func newWS(t *testing.T, p *peer, clk types.Clock, alerts Alerter) *Adapter {
	t.Helper()
	a, err := NewWS(Config{URL: p.url(), Token: p.token, Account: "100"}, clk, alerts)
	if err != nil {
		t.Fatalf("NewWS：%v", err)
	}
	a.jitter = func(d time.Duration) time.Duration { return d }
	return a
}

func runWS(t *testing.T, a *Adapter) (*testSink, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sink := newTestSink()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	return sink, cancel, done
}

// 核验查询也是 10 秒，拨过很长时间前先等它处理完，避免把查询超时误当看门狗行为。
func waitWSReady(t *testing.T, a *Adapter) {
	t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		if a.sending() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	fail(t, "连接没有完成核验")
}

func waitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回 %v，ctx 取消后应当返回 nil", err)
		}
	case <-time.After(testPatience):
		fail(t, "Run 一直没返回")
	}
}

func privateFrame(id, uid, text string) string {
	return fmt.Sprintf(`{"post_type":"message","time":100,"self_id":100,"user_id":%s,"message_id":%q,"sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":%q}}]}`, uid, id, text)
}

func TestWSReceivesAndTranslates(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	select {
	case got := <-p.auth:
		if got != "Bearer "+testToken {
			t.Fatalf("握手带的是 %q，要 Bearer <令牌>", got)
		}
	case <-time.After(testPatience):
		fail(t, "没收到握手")
	}

	p.send(t, c, privateFrame("300", "200", "第一句"))
	in := recvInbound(t, sink)
	if in.Kind != types.InboundMessage || in.Account != "100" || in.PlatformID != "300" ||
		in.Conv.Scene != types.ScenePrivate || in.Conv.Self != "100" || in.Conv.ID != "200" ||
		in.From.ID != "200" || in.From.Nick != "虚构甲" || len(in.Segs) != 1 || in.Segs[0].Text != "第一句" {
		t.Fatalf("第一帧翻译成 %#v", in)
	}

	p.send(t, c, `{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_id":"301","message":[{"type":"at","data":{"qq":"100"}},{"type":"text","data":{"text":" 在吗"}}]}`)
	in = recvInbound(t, sink)
	if in.Conv.Scene != types.SceneGroup || in.Conv.ID != "400" || in.Conv.Self != "" ||
		in.PlatformID != "301" || len(in.Segs) != 2 || in.Segs[0].Kind != types.SegAt || in.Segs[0].Target != "100" {
		t.Fatalf("第二帧翻译成 %#v", in)
	}

	p.send(t, c, `{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":30000}`)
	if st := recvStatus(t, sink, types.StatusHeartbeat); st.Interval != 30*time.Second || st.Account != "100" {
		t.Fatalf("心跳翻译成 %#v", st)
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
}

func TestWSReconnectsAndKeepsDelivering(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c1 := p.accept(t)
	p.send(t, c1, privateFrame("300", "200", "断线前"))
	if in := recvInbound(t, sink); in.PlatformID != "300" {
		t.Fatalf("断线前的消息翻译成 %#v", in)
	}

	c1.CloseNow()
	recvStatus(t, sink, types.StatusDisconnected)
	clk.WaitTimer(t, time.Second)
	clk.Advance(time.Second)

	c2 := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	p.send(t, c2, privateFrame("302", "200", "重连以后"))
	if in := recvInbound(t, sink); in.PlatformID != "302" || in.Segs[0].Text != "重连以后" {
		t.Fatalf("重连后的消息翻译成 %#v", in)
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
	if raises, _ := alerts.counts(); raises != 0 {
		t.Fatalf("1 秒内连回来了，不该提醒，实际 Raise %d 次", raises)
	}
}

func TestWSAlertAfter30SecondsThenClear(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	p.fail.Store(true) // 断线以后协议端一直不在线
	c.CloseNow()
	recvStatus(t, sink, types.StatusDisconnected)
	clk.WaitTimer(t, 30*time.Second)
	clk.Advance(30 * time.Second)

	x := alerts.waitRaises(t, 1)
	if x.Key != "qq:100:conn" || x.Class != "账号掉线" || x.Title != "QQ 连接断了" ||
		!strings.Contains(x.Body, "自动重连") || !strings.Contains(x.Body, "30 秒") {
		t.Fatalf("提醒内容是 %#v", x)
	}

	// 一直断着：再过 2 分钟也只在满 30 秒时提醒那一次
	clk.WaitTimer(t, 2*time.Second) // 等第一次退避处理完再拨下一段，避免丢掉正在创建的计时器。
	clk.Advance(90 * time.Second)
	clk.WaitTimer(t, 4*time.Second) // 等循环处理完发出去的退避、造出下一个计时器
	if raises, _ := alerts.counts(); raises != 1 {
		t.Fatalf("一直断着应当只提醒一次，实际 %d 次", raises)
	}

	p.fail.Store(false)
	clk.Advance(4 * time.Second)
	recvStatus(t, sink, types.StatusConnected)
	clk.WaitTimer(t, stableAfter)
	clk.Advance(stableAfter) // 只握手还不能清掉线提醒；这条连接没有收到第一帧，靠稳定门槛恢复。
	key, note := alerts.waitClears(t, 1)
	if key != "qq:100:conn" || note.Title != "QQ 连接恢复了" || !strings.Contains(note.Body, "断了") {
		t.Fatalf("恢复的内容是 %q %#v", key, note)
	}
	if raises, _ := alerts.counts(); raises != 1 {
		t.Fatalf("恢复不该再 Raise，实际 %d 次", raises)
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
}

func TestWSStartupFailureAlertsAfter30Seconds(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	p.fail.Store(true) // 一开机协议端就不在
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	recvStatus(t, sink, types.StatusDisconnected)
	clk.WaitTimer(t, 30*time.Second)
	clk.Advance(30 * time.Second)
	x := alerts.waitRaises(t, 1)
	if x.Key != "qq:100:conn" || !strings.Contains(x.Body, "自动重连") {
		t.Fatalf("启动时连不上的提醒是 %#v", x)
	}

	cancel()
	waitRun(t, done)
	p.assertNoActions(t)
}

func TestWSNoAlertWhenBackWithin29Seconds(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	a.backoffMax = time.Second // 每秒试一次，好在 30 秒边界上刚好 29 秒时连回来
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	p.fail.Store(true)
	c.CloseNow()
	recvStatus(t, sink, types.StatusDisconnected)

	// 第 1 到第 28 秒各试一次，都连不上
	for range 28 {
		clk.WaitTimer(t, time.Second)
		clk.Advance(time.Second)
	}
	// 先等第 28 秒那次拨号失败、建出下一个计时器，再让协议端回来：
	// 反过来的话，还在路上的那次拨号会被当成第 29 秒的成功（测试里量到过）
	clk.WaitTimer(t, time.Second)
	p.fail.Store(false)
	clk.Advance(time.Second) // 第 29 秒连上
	recvStatus(t, sink, types.StatusConnected)

	// 第 29 秒连回来的连接先完成核验，才算在提醒门槛内恢复。
	c2 := p.accept(t)
	p.send(t, c2, privateFrame("303", "200", "恢复以后"))
	if in := recvInbound(t, sink); in.PlatformID != "303" {
		t.Fatalf("恢复后的消息翻译成 %#v", in)
	}
	waitWSReady(t, a)
	clk.Advance(2 * time.Minute)
	if raises, clears := alerts.counts(); raises != 0 || clears != 0 {
		t.Fatalf("29 秒内恢复不该提醒，实际 Raise %d 次、Clear %d 次", raises, clears)
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
}

func TestWSCancelStopsReconnecting(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	p.send(t, c, privateFrame("300", "200", "在"))
	recvInbound(t, sink)

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)

	before := p.accepts.Load()
	clk.Advance(2 * time.Minute)
	if after := p.accepts.Load(); after != before {
		t.Fatalf("ctx 取消以后又拨了 %d 次号", after-before)
	}
}

// 令牌写在地址里的配置不收；错误里不带地址
func TestNewWSRejectsTokenInURL(t *testing.T) {
	for _, u := range []string{"ws://127.0.0.1:1/?access_token=" + url.QueryEscape(testToken), "ws://user:" + url.QueryEscape(testToken) + "@127.0.0.1:1/"} {
		_, err := NewWS(Config{URL: u, Account: "100"}, nil, newTestAlerts())
		if err == nil {
			t.Fatalf("地址里带令牌的配置收下了")
		}
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), url.QueryEscape(testToken)) {
			t.Fatalf("错误里带了地址：%v", err)
		}
	}
}

// 连不上时的错误不带地址的路径和参数（库的 url.Error 里有完整地址）
func TestDialErrorOmitsURL(t *testing.T) {
	_, err := dialWithin(context.Background(), Config{URL: "ws://127.0.0.1:1/bianDeLuJing?x=bianDeCanShu"}, 3*time.Second)
	if err == nil {
		t.Fatal("连一个没人听的端口居然连上了")
	}
	if strings.Contains(err.Error(), "bianDeLuJing") || strings.Contains(err.Error(), "bianDeCanShu") {
		t.Fatalf("错误里带了地址：%v", err)
	}
}

// 拨号错误里万一带着令牌：日志和报给中间件的断开原因里都换成 ***
func TestTokenScrubbedFromLogsAndReason(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	a := newWS(t, p, clk, newTestAlerts())
	a.dial = func(context.Context, Config) (Conn, error) {
		return nil, errors.New("编的拨号错误，里面带着 " + testToken)
	}
	sink, cancel, done := runWS(t, a)
	st := recvStatus(t, sink, types.StatusDisconnected)
	cancel()
	waitRun(t, done)
	if strings.Contains(st.Reason, testToken) || !strings.Contains(st.Reason, "***") {
		t.Fatalf("断开原因 = %q，令牌要换成 ***", st.Reason)
	}
	if logs := buf.String(); strings.Contains(logs, testToken) || !strings.Contains(logs, "***") {
		t.Fatalf("日志 = %q，令牌要换成 ***", logs)
	}
}

// 每次重拨都报告本次错误；不能沿用旧断线原因，也不能跳过令牌脱敏。
func TestWSReconnectReason(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	a := newWS(t, newPeer(t, testToken), clk, newTestAlerts())
	attempt := 0
	a.dial = func(context.Context, Config) (Conn, error) {
		attempt++
		return nil, fmt.Errorf("编的拨号失败 %d：%s", attempt, testToken)
	}
	sink, cancel, done := runWS(t, a)
	defer func() { cancel(); waitRun(t, done) }()
	recvStatus(t, sink, types.StatusDisconnected)
	clk.WaitTimer(t, time.Second)
	clk.Advance(time.Second)
	st := recvStatus(t, sink, types.StatusReconnecting)
	if st.Reason != "编的拨号失败 2：***" {
		t.Fatalf("重拨原因应来自本次错误并脱敏：%q", st.Reason)
	}
}

func TestTokenNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	p.fail.Store(true)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	recvStatus(t, sink, types.StatusDisconnected)
	clk.WaitTimer(t, time.Second)
	clk.Advance(time.Second)
	cancel()
	waitRun(t, done)

	if logs := buf.String(); !strings.Contains(logs, "连不上协议端") { // 模块标签（snowluma）由装配处加，这里的出口只有正文
		t.Fatalf("没有日志可查：%q", logs)
	} else if strings.Contains(logs, testToken) {
		t.Fatalf("日志里出现了令牌：%q", logs)
	}
}

// recordingClock 在假钟之上记下每次 NewTimer 要多久：退避的翻倍和封顶只有看请求的时长才断言得准
// （计时器到点就被删掉，拨完钟再问就问不到了）。
type recordingClock struct {
	*fake.Clock
	made chan time.Duration
}

func newRecordingClock(clk *fake.Clock) *recordingClock {
	return &recordingClock{Clock: clk, made: make(chan time.Duration, 64)}
}

func (c *recordingClock) NewTimer(d time.Duration) types.Timer {
	// 先造计时器再记账：反过来测试可能先看到时长就拨钟，计时器在拨过之后才造出来，要再拨一次才响
	t := c.Clock.NewTimer(d)
	select {
	case c.made <- d:
	default:
	}
	return t
}

// nextBackoff 取下一个退避计时器的时长，跳过提醒计时器（跳过哪些由调用方按已经拨过的时间给出：
// 提醒要的时长是离断线满 30 秒还差多少，和退避的时长是不同的量）和警报以后补造的 0 秒计时器。
func (c *recordingClock) nextBackoff(t *testing.T, skip func(time.Duration) bool) time.Duration {
	t.Helper()
	deadline := time.NewTimer(testPatience)
	defer deadline.Stop()
	for {
		select {
		case d := <-c.made:
			if d <= 0 || skip(d) {
				continue
			}
			return d
		case <-deadline.C:
			fail(t, "5 秒内没造出下一个退避计时器")
			return 0
		}
	}
}

// 抖动在 [d/2, d] 里，而且真的散开了（不是总取一头）
func TestJitterRange(t *testing.T) {
	for _, d := range []time.Duration{time.Second, maxBackoff} {
		lo, hi := d, time.Duration(0)
		for i := 0; i < 2000; i++ {
			j := Jitter(d)
			if j < d/2 || j > d {
				t.Fatalf("Jitter(%v) = %v，不在 [%v, %v] 里", d, j, d/2, d)
			}
			lo, hi = min(lo, j), max(hi, j)
		}
		if hi-lo < d/4 {
			t.Fatalf("Jitter(%v) 2000 次只落在 %v～%v，没散开", d, lo, hi)
		}
	}
	if Jitter(0) != 0 || Jitter(time.Nanosecond) != time.Nanosecond {
		t.Fatalf("Jitter(0) = %v、Jitter(1ns) = %v，太短的不抖", Jitter(0), Jitter(time.Nanosecond))
	}
}

func TestWSBackoffDoublesAndCapsAt60Seconds(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	rec := newRecordingClock(clk)
	p := newPeer(t, testToken)
	p.fail.Store(true) // 一直连不上，退避一路走到封顶
	alerts := newTestAlerts()
	a := newWS(t, p, rec, alerts)
	sink, cancel, done := runWS(t, a)

	recvStatus(t, sink, types.StatusDisconnected)

	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second,
	}
	var elapsed time.Duration
	// 提醒计时器要的时长：每次退避开头都会补造一个"离断线满 30 秒还差多少"，之前那些也还在队列里
	alertLeft := map[time.Duration]bool{}
	skipAlert := func(d time.Duration) bool { return alertLeft[d] }
	for i, d := range want {
		alertLeft[defaultAlert-elapsed] = true
		if got := rec.nextBackoff(t, skipAlert); got != d {
			fail(t, "第 %d 次重连等了 %v，要 %v（断线 %v 时）", i+1, got, d, elapsed)
		}
		clk.Advance(d)
		elapsed += d
	}
	// 屏障：再读一个计时器，证明上一轮处理完了（拨完钟还没造出下一个计时器就查提醒会漏）。
	alertLeft[defaultAlert-elapsed] = true
	if got := rec.nextBackoff(t, skipAlert); got != 60*time.Second {
		fail(t, "封顶以后等了 %v，要 60s", got)
	}

	cancel()
	waitRun(t, done)
	p.assertNoActions(t)
	if raises, clears := alerts.counts(); raises != 1 || clears != 0 {
		fail(t, "一直断着、封顶以后应当只提醒一次，实际 Raise %d 次、Clear %d 次", raises, clears)
	}
}

// 规格-渠道中间件第十三节：超过 3 个心跳间隔没收到就判掉线。协议端给了间隔以后一帧都不来，满 3 个间隔
// 关连接、按退避重连；提醒的 30 秒还没到，不该 Raise。
func TestWSHeartbeatWatchdogDisconnectsWhenFramesStop(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	p.send(t, c, `{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":1000}`)
	recvStatus(t, sink, types.StatusHeartbeat)
	// 第一帧之后核验会发 get_login_info、get_status，假协议端的回包也是帧，每收到一帧看门狗就重新计 3 秒。
	// 回包晚于假钟推进才到的话，计时被重置、钟不会再推，等不到掉线；等核验做完，这以后才真的一帧都没有
	waitWSReady(t, a)

	// 看门狗按协议端给的 1 秒间隔计 3 秒；这以后一帧都没来
	clk.WaitTimer(t, 3*time.Second)
	clk.Advance(3 * time.Second)
	st := recvStatus(t, sink, types.StatusDisconnected)
	if !strings.Contains(st.Reason, "心跳") {
		fail(t, "断开原因是 %q，要说心跳和所有帧都停了", st.Reason)
	}

	// 重连前关掉旧连接，按退避 1 秒再拨一次
	clk.WaitTimer(t, time.Second)
	clk.Advance(time.Second)
	p.accept(t)
	recvStatus(t, sink, types.StatusConnected)

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	if raises, clears := alerts.counts(); raises != 0 || clears != 0 {
		fail(t, "3 秒就重连上了，不该提醒，实际 Raise %d 次、Clear %d 次", raises, clears)
	}
	p.assertNoActions(t)
}

// 一直有帧来（消息也算收到，不只看心跳）就不判掉线，哪怕总共过去的时间超过 3 个心跳间隔。
func TestWSHeartbeatWatchdogQuietWhileFramesArrive(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	p.send(t, c, `{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":1000}`)
	recvStatus(t, sink, types.StatusHeartbeat)

	// 每过一个心跳间隔多一点就来一条消息：4 次下来过了 8 秒，远超过 3 个间隔
	for i := range 4 {
		clk.WaitTimer(t, 3*time.Second)
		clk.Advance(2 * time.Second)
		id := fmt.Sprintf("%d", 400+i)
		p.send(t, c, privateFrame(id, "200", "还在"))
		if in := recvInbound(t, sink); in.PlatformID != id {
			fail(t, "第 %d 条消息翻译成 %#v", i+1, in)
		}
	}
	select {
	case st := <-sink.statuses:
		fail(t, "一直有帧来，却报了 %s（%s）", st.Kind, st.Reason)
	default:
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
}

// 没收到过心跳就不知道间隔，不计时（规格：心跳间隔用协议端给的值，不写死；第二十节第 5 条）。
func TestWSHeartbeatWatchdogAbsentWithoutHeartbeat(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)

	c := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	p.send(t, c, privateFrame("300", "200", "没有心跳"))
	recvInbound(t, sink)
	waitWSReady(t, a)

	clk.Advance(10 * time.Minute)
	p.send(t, c, privateFrame("301", "200", "十分钟以后还在"))
	if in := recvInbound(t, sink); in.PlatformID != "301" {
		fail(t, "十分钟以后的消息翻译成 %#v", in)
	}
	select {
	case st := <-sink.statuses:
		fail(t, "没收到过心跳却报了 %s（%s）", st.Kind, st.Reason)
	default:
	}

	cancel()
	waitRun(t, done)
	p.waitReads(t)
	p.assertNoActions(t)
}
