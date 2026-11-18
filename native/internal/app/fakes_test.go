package app

// 整包测试的假环境（规格-Go后台装配 第八节第 4 步）：假协议端（OneBot 正向 WS）、假网关（llmtest）、临时数据目录和编的号。
// 号、令牌、名字一律是编的；数据目录只用 t.TempDir()，不碰真的 data/。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel"
	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/turn"
)

// 测试里的号、令牌、模型名都是编的（AGENTS「验收」）
const (
	testAccount = "100" // 要连的小号
	testOwner   = "200" // 主人
	testGroup   = "300" // 群
	testToken   = "编的令牌"
	testModel   = "编的模型"
	testChar    = "example"
)

// fakePeer 是假协议端：OneBot 正向 WS 的服务端。记下客户端发来的帧。核验用的 get_login_info / get_status 会回，发送类 action 仍不该出现。
type fakePeer struct {
	srv      *httptest.Server
	accepted chan *websocket.Conn
	actions  chan string
	onAction func(*websocket.Conn, []byte) // 测试启动前设置；nil 沿用只回核验的假端

	mu     sync.Mutex
	dones  []chan struct{}
	conns  []*websocket.Conn
	closed bool
}

func newFakePeer(t *testing.T) *fakePeer {
	t.Helper()
	p := &fakePeer{accepted: make(chan *websocket.Conn, 4), actions: make(chan string, 32)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("协议端握手的 Authorization 不是预期的编的令牌")
			http.Error(w, "访问令牌不对", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		done := make(chan struct{})
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			c.CloseNow()
			return
		}
		p.conns = append(p.conns, c)
		p.dones = append(p.dones, done)
		p.mu.Unlock()
		defer close(done)
		select {
		case p.accepted <- c:
		default:
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			select {
			case p.actions <- string(data):
			default:
			}
			if p.onAction != nil {
				p.onAction(c, data)
			} else {
				answerInfo(c, data)
			}
		}
	}))
	t.Cleanup(func() {
		p.srv.Close()
		p.mu.Lock()
		p.closed = true
		conns := append([]*websocket.Conn(nil), p.conns...)
		p.mu.Unlock()
		for _, c := range conns {
			c.CloseNow()
		}
		p.waitReaders(t)
	})
	return p
}

func (p *fakePeer) url() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

func (p *fakePeer) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case c := <-p.accepted:
		return c
	case <-time.After(10 * time.Second):
		t.Fatalf("10 秒内没收到适配器的连接")
		return nil
	}
}

func (p *fakePeer) send(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("假协议端发帧：%v", err)
	}
}

// waitReaders 必须等读协程把断开前的帧全记下来，才能断言没有 action。
func (p *fakePeer) waitReaders(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	dones := append([]chan struct{}(nil), p.dones...)
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, done := range dones {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("假协议端的读协程没有退出")
		}
	}
}

// assertNoActions 说没有发送类 action。登录号核验的查询帧不算。
func (p *fakePeer) assertNoActions(t *testing.T) {
	t.Helper()
	p.waitReaders(t)
	for {
		select {
		case f := <-p.actions:
			if isSendAction(f) {
				t.Fatalf("假协议端收到了发送 action：%s", f)
			}
		default:
			return
		}
	}
}

func isSendAction(frame string) bool {
	var o map[string]json.RawMessage
	if json.Unmarshal([]byte(frame), &o) != nil {
		return false
	}
	var action string
	_ = json.Unmarshal(o["action"], &action)
	switch action {
	case "send_group_msg", "send_private_msg", "send_msg", "send_group_forward_msg", "send_private_forward_msg", "set_msg_emoji_like", "group_poke", "friend_poke":
		return true
	default:
		return false
	}
}

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
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil || m == nil {
		return
	}
	eb, _ := json.Marshal(echo)
	m["echo"] = eb
	out, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, out)
}

// testEnv 是一整套假环境
type testEnv struct {
	t    *testing.T
	root string // 项目根（读提示词、示例卡用）
	data string // 临时数据目录
	now  time.Time
	peer *fakePeer
	llm  *httptest.Server

	// callTimeout：协议端不回包要等多久（QQ 动作回包）；0 是 3 秒。链接用例的“不回包”靠它到期
	callTimeout time.Duration

	goSend      bool                      // 写进配置的 qq.goSend（默认 false，发送权还是 Bun 的）
	extraConfig string                    // 接在配置末尾的几行
	linkFetcher linkreader.ContentFetcher // 换掉链接取数；nil 用真的（测试里的消息不带链接就不联网）
	// 日历、天气的联网：不设就是一个一律报错的客户端（测试不联网）；设了就引到假服务器
	lifeClient                      *http.Client
	lifeGeocodeURL, lifeForecastURL string

	nowMu      sync.RWMutex // 测试线程拨钟、后台各 goroutine 读钟：拨钟走 setNow，后台读走 nowAt
	mu         sync.Mutex
	bodies     []string // 假网关收到的请求体，一次一个
	linkSource string   // 最近一次脚本发的链接消息的平台消息号（假协议端的历史里要有它）

	logMu sync.Mutex
	logs  []string
}

// setNow 拨假钟（测试线程调）；后台 goroutine 同时在读，所以要加锁
func (e *testEnv) setNow(t time.Time) {
	e.nowMu.Lock()
	e.now = t
	e.nowMu.Unlock()
}

// nowAt 是给后台用的钟
func (e *testEnv) nowAt() time.Time {
	e.nowMu.RLock()
	defer e.nowMu.RUnlock()
	return e.now
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{t: t, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)}
	root, err := config.FindRoot("../..")
	if err != nil {
		t.Fatalf("找项目根（在 native/internal/app 里跑）：%v", err)
	}
	e.root = root
	e.data = t.TempDir()
	if _, err := paths.Init(e.data); err != nil {
		t.Fatalf("初始化临时数据目录：%v", err)
	}
	if err := copyCard(root, e.data); err != nil {
		t.Fatalf("拷示例卡：%v", err)
	}
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	e.llm = httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("嗯，在的。")}, &e.bodies, &e.mu))
	t.Cleanup(e.llm.Close)
	e.peer = newFakePeer(t)
	// 数据目录由 Options 显式给；不让环境变量掩盖配置和数据目录分离的问题。
	// SUMIKA_DATA_DIR、SUMIKA_CONFIG 在 TestMain 里一次清空（t.Setenv 不能和 t.Parallel 同用）
	e.writeConfig(testToken)
	// 失败时把收到的日志打出来：断在哪一步（连上、入库、闸门、回合）看日志最快
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		e.logMu.Lock()
		defer e.logMu.Unlock()
		for _, l := range e.logs {
			t.Log(l)
		}
	})
	return e
}

// newLocalEnv 是“只开本地”的假环境：配置里没有 qq.accounts，数据目录不 Init、只放 config/sumika.yml 和一张卡，
// 不连假协议端（没有适配器要连）。假网关照旧起，本地回合要它回话。
func newLocalEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{t: t, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)}
	root, err := config.FindRoot("../..")
	if err != nil {
		t.Fatalf("找项目根（在 native/internal/app 里跑）：%v", err)
	}
	e.root = root
	e.data = t.TempDir()
	if err := copyCard(root, e.data); err != nil {
		t.Fatalf("拷示例卡：%v", err)
	}
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	e.llm = httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("嗯，在的。")}, &e.bodies, &e.mu))
	t.Cleanup(e.llm.Close)
	e.writeConfigNoQQ()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		e.logMu.Lock()
		defer e.logMu.Unlock()
		for _, l := range e.logs {
			t.Log(l)
		}
	})
	return e
}

// writeConfigNoQQ 写没有 qq.accounts 的配置：本地照样能用，QQ 单元不开。不写令牌、不写协议端地址。
func (e *testEnv) writeConfigNoQQ() {
	e.t.Helper()
	cfg := fmt.Sprintf(`# 只开本地的测试配置（号、模型都是编的）
magpie:
  baseUrl: http://127.0.0.1:1/v1
qq:
  owner: %s
  groups:
    - %s
  privateAllow: []
group:
  frequency: 0.3
  jitter: 0
  mode: dry
characters:
  %s:
    model: %s
embed:
  enabled: false
agent:
  recentMessages: 5
%s`, testOwner, testGroup, testChar, testModel, e.extraConfig)
	dir := filepath.Join(e.data, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("建 config 目录：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sumika.yml"), []byte(cfg), 0o644); err != nil {
		e.t.Fatalf("写配置：%v", err)
	}
}

// localOnlyOptions 走只开本地：不给 Account，LocalOnly 为真。
func (e *testEnv) localOnlyOptions() Options {
	opt := e.options()
	opt.Account = ""
	opt.LocalOnly = true
	return opt
}

// copyCard 把仓库里那张示例卡拷进临时数据目录（找卡的路子照 paths / persona：characters/<id>/card/）
func copyCard(root, data string) error {
	src := filepath.Join(root, "characters", "example")
	dst := filepath.Join(data, "characters", "example", "card")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeConfig 写临时数据目录里的 sumika.yml（默认频率 0.3）；token 给空串就是不写这一行（走密钥那一路）
func (e *testEnv) writeConfig(token string) { e.writeConfigFreq(token, "0.3") }

// writeConfigFreq 同 writeConfig，频率自己给（测 group.frequency: 0 那条规则）
func (e *testEnv) writeConfigFreq(token, freq string) {
	e.t.Helper()
	tokenLine := ""
	if token != "" {
		tokenLine = "\n      token: \"" + token + "\""
	}
	cfg := fmt.Sprintf(`# 测试用的配置（号、令牌、模型都是编的）
magpie:
  baseUrl: http://127.0.0.1:1/v1
qq:
  owner: %s
  accounts:
    - id: %s
      character: %s
      url: "%s"%s
  groups:
    - %s
  privateAllow: []
  goSend: %t
group:
  frequency: %s
  jitter: 0
  mode: dry
characters:
  %s:
    model: %s
embed:
  enabled: false
agent:
  recentMessages: 5
%s`, testOwner, testAccount, testChar, e.peer.url(), tokenLine, testGroup, e.goSend, freq, testChar, testModel, e.extraConfig)
	dir := filepath.Join(e.data, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("建 config 目录：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sumika.yml"), []byte(cfg), 0o644); err != nil {
		e.t.Fatalf("写配置：%v", err)
	}
}

func (e *testEnv) logf(format string, args ...any) {
	e.logMu.Lock()
	e.logs = append(e.logs, fmt.Sprintf(format, args...))
	e.logMu.Unlock()
}

func (e *testEnv) options() Options {
	callTimeout := e.callTimeout
	if callTimeout == 0 {
		callTimeout = 3 * time.Second
	}
	// BackupTempRoots 给空：数据目录在系统临时目录下时备份服务本会无视配置里的 backup.dir、一律放旁边；这里的测试要用配置里写的备份目录（都是临时目录里的）
	return Options{Root: e.root, DataDir: e.data, Mode: Receive, Account: testAccount, Logf: e.logf, DialContext: refuseLegacy, BackupTempRoots: []string{},
		SharedAnalyzer: testAnalyzer(), OutletPace: channel.Pace{PerChar: 20 * time.Microsecond, Max: 20 * time.Millisecond, Retry: 10 * time.Millisecond}, LLM: llm.New(llm.Magpie(e.llm.URL)), Now: e.nowAt, LinkFetcher: e.linkFetcher, LinkEchoWait: 50 * time.Millisecond, LinkVerifyTimeout: callTimeout,
		LifeClient: e.lifeHTTP(), LifeGeocodeURL: e.lifeGeocodeURL, LifeForecastURL: e.lifeForecastURL,
		RemoteApprovalTick: 20 * time.Millisecond,
		// 协议端不回包的用例要等这个时限；3 秒对进程内的假协议端足够宽，比默认 10 秒省下每次 7 秒
		QQCallTimeout: callTimeout, QQQueryGap: 10 * time.Millisecond,
		// 模型出错的用例不陪真实的 1+2+4+8 秒退避：重试照样发生，只是间隔短
		ModelRetry: turn.RetryPolicy{Base: 5 * time.Millisecond, Cap: 20 * time.Millisecond}}
}

// lifeHTTP：测试里日历、天气的联网客户端；没指定就一律报错，免得测试悄悄联网
func (e *testEnv) lifeHTTP() *http.Client {
	if e.lifeClient != nil {
		return e.lifeClient
	}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("测试里不联网：%s", r.URL.Host)
	})}
}

func refuseLegacy(context.Context, string, string) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

// open 只装配，不连网
func (e *testEnv) open() *App {
	e.t.Helper()
	a, err := Open(context.Background(), e.options())
	if err != nil {
		e.t.Fatalf("Open：%v", err)
	}
	return a
}

// run 起适配器收消息；测试结束前取消、等 Run 返回（收不干净的话假协议端的处理函数会一直挂着）
func (e *testEnv) run(a *App) *App {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	e.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				e.t.Errorf("Run 返回错误：%v", err)
			}
		case <-time.After(40 * time.Second):
			e.t.Errorf("40 秒内 Run 没返回")
		}
	})
	return a
}

func (e *testEnv) start() *App { return e.run(e.open()) }

func (e *testEnv) llmCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bodies)
}

// groupAtFrame 是一条 QQ 群消息：@ 她（testAccount），后面接一句文字
func groupAtFrame(now time.Time, self, msgID, uid, text string) string {
	return fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":%s,"user_id":%s,"group_id":%s,"message_id":%q,"sender":{"nickname":"编的甲"},"message":[{"type":"at","data":{"qq":"%s"}},{"type":"text","data":{"text":" %s"}}]}`,
		now.Unix(), self, uid, testGroup, msgID, testAccount, text)
}

// privateFrame 是一条 QQ 私聊消息
func privateFrame(now time.Time, self, msgID, uid, text string) string {
	return fmt.Sprintf(`{"post_type":"message","message_type":"private","time":%d,"self_id":%s,"user_id":%s,"message_id":%q,"sender":{"nickname":"编的甲"},"message":[{"type":"text","data":{"text":%q}}]}`,
		now.Unix(), self, uid, msgID, text)
}
