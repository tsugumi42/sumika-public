package mcpclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
)

// Journal 是 MCP 调用留痕（store.EcosystemStore 实现，状态含义见 store/mcp_calls.go）。
type Journal interface {
	MCPBlocked(ctx context.Context, server string) (bool, error)
	// MCPDispatch 记下派发；transport 是 stdio 或 http，写进这一行，启动收尾按它判断旧调用还会不会在跑。
	MCPDispatch(ctx context.Context, id, server, binding, transport string) error
	// MCPEnd 记下结局；known 为假时 cause 是原因（超时、被停止、出错）。
	MCPEnd(ctx context.Context, id string, known bool, cause string) error
	MCPTerminated(ctx context.Context, id string) error
	// MCPRelease 解除一个服务的隔离，每一行记 actor、时间、reason；返回解除了几行。
	MCPRelease(ctx context.Context, server, actor, reason string) (int, error)
}
type ServerConfig struct {
	ID                      string `json:"-"`
	Type, URL, Command, CWD string
	Args                    []string
	Env, Headers            map[string]string
	Sumika                  ServerPolicy
}
type ServerPolicy struct {
	Enabled                                           bool
	Include, Exclude                                  []string
	QQEnabled                                         bool
	StartupTimeoutMs, CatalogTimeoutMs, CallTimeoutMs int
}
type Client struct {
	mu      sync.Mutex
	servers map[string]*server
	secret  [32]byte
	journal Journal
	jobs    map[string]*Job
	logDir  string // stdio 服务的 stderr 日志放哪；空 = 不写文件
	// onChange 在某个服务的隔离、连接状态可能变了之后调用（不持任何锁、同步调，所以回调里可以读 Client 的状态，但要快）；空 = 不通知
	onChange func(server string)
	logf     func(format string, args ...any) // 自动重连的日志；空 = 不写
	// 本机服务自动重连（recovery.go）：closed 之后不再起新的重连；ctx 在 Close 时取消，掐断正在进行的重连
	recoverMu sync.Mutex
	closed    bool
	recoverWG sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}
type server struct {
	mu          sync.Mutex
	config      ServerConfig
	binding     string
	generation  uint64
	session     *mcp.ClientSession
	cancels     *cancelTracker
	entries     []Entry
	slot        chan struct{}
	pending     *ResolvedCall
	pendingDone chan struct{} // pending 清掉时关闭；连接监视协程等它
	active      bool
	recovery    recovery // 本机服务断开后的自动重连状态
	// unconfirmed 非空（存的是 string）：关旧连接时没能确认本机进程树已退出。规格要求无法确认就不起新代，所以任何连接（自动、显式）都先查它；
	// 进程内没有办法再确认，只有重启 Sumika（进程树随程序退出）才清。reset() 不碰它。
	// 用原子值、不用 s.mu：它在关连接的回调里被写（process.go 的 onTree），而关连接可能发生在持着 s.mu 的路径上，再取 s.mu 会自己等自己
	unconfirmed atomic.Value
}

func New(j Journal) *Client {
	c := &Client{servers: map[string]*server{}, journal: j}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if _, err := rand.Read(c.secret[:]); err != nil {
		panic(err)
	}
	return c
}

// SetOnChange 登记状态变化通知：某个服务被隔离、解除隔离后调用（见 onChange）。
func (c *Client) SetOnChange(f func(server string)) {
	c.mu.Lock()
	c.onChange = f
	c.mu.Unlock()
}

func (c *Client) changed(server string) {
	c.mu.Lock()
	f := c.onChange
	c.mu.Unlock()
	if f != nil {
		f(server)
	}
}

// SetLogDir 指定 stdio 服务的 stderr 日志目录（每个服务一个文件，按大小轮转，见 stderrlog.go）；要在 Connect 之前调
func (c *Client) SetLogDir(dir string) {
	c.mu.Lock()
	c.logDir = dir
	c.mu.Unlock()
}

func defaults(c *ServerConfig) error {
	if c.ID == "" {
		return fmt.Errorf("MCP 缺 serverID")
	}
	if c.Sumika.Enabled && c.Sumika.Include == nil {
		c.Sumika.Include = []string{"*"} // 不写 include 就是全部工具（mcp_search 先搜、调用要审批、写错了也能撤）
	}
	for _, p := range []*int{&c.Sumika.StartupTimeoutMs, &c.Sumika.CatalogTimeoutMs} {
		if *p == 0 {
			*p = 30000
		}
		if *p < 1 || *p > 600000 {
			return fmt.Errorf("MCP 时限越界")
		}
	}
	if c.Sumika.CallTimeoutMs == 0 {
		c.Sumika.CallTimeoutMs = 60000
	}
	if c.Sumika.CallTimeoutMs < 1 || c.Sumika.CallTimeoutMs > 600000 {
		return fmt.Errorf("MCP 调用时限越界")
	}
	if c.Type == "http" {
		u, err := url.Parse(c.URL)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("MCP URL 非法")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
			return fmt.Errorf("MCP 只允许 HTTPS 或显式回环 HTTP")
		}
	}
	return nil
}

// afterCatalogHook 只给测试用：握手和目录好了、发布之前调一次。
var afterCatalogHook func(s *server)

// Connect 在完整目录构建后才发布。调用方在 K1 Lifetime 登记此活动。
// 这是主人（或宿主按配置）显式的连接：会让这个服务正在进行的自动重连作废，重连预算清零。
func (c *Client) Connect(ctx context.Context, cfg ServerConfig) error {
	return c.connect(ctx, cfg, nil)
}

// connect 的 auto 不为空时是自动重连：只有它拿到的“重连代次”还是当前的才继续（主人改配置、停用、再连之后作废）。
func (c *Client) connect(ctx context.Context, cfg ServerConfig, auto *uint64) error {
	if err := defaults(&cfg); err != nil {
		return err
	}
	if !cfg.Sumika.Enabled {
		return c.Disable(cfg.ID)
	}
	blocked, err := c.journal.MCPBlocked(ctx, cfg.ID)
	if err != nil {
		return err
	}
	if blocked {
		return fmt.Errorf("%w，不能重新连接：%s", ErrQuarantined, cfg.ID)
	}
	if cfg.Type == "http" {
		// 绑定与 transport 必须拥有同一份凭据，不能保留调用方之后还能修改的 map。
		cfg.Headers = maps.Clone(cfg.Headers)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, c.secret[:])
	mac.Write(raw)
	binding := hex.EncodeToString(mac.Sum(nil))
	c.mu.Lock()
	s := c.servers[cfg.ID]
	if s == nil {
		s = &server{slot: make(chan struct{}, 1)}
		c.servers[cfg.ID] = s
	}
	logDir := c.logDir
	c.mu.Unlock()
	s.mu.Lock()
	if auto != nil && s.recovery.epoch != *auto {
		s.mu.Unlock()
		return errSuperseded
	}
	if why := s.unconfirmedWhy(); why != "" {
		s.mu.Unlock()
		return fmt.Errorf("%w：%s", ErrOldProcessUnconfirmed, why)
	}
	if auto == nil {
		s.recovery.reset()
	}
	old, oldCancels := s.session, s.cancels
	s.active = false
	s.generation++
	generation := s.generation
	s.session = nil
	s.cancels = nil
	s.entries = nil
	s.config = cfg
	s.binding = binding
	s.mu.Unlock()
	if old != nil {
		if err := c.closeOld(cfg.ID, old, oldCancels); err != nil {
			return fmt.Errorf("旧 MCP 连接关闭失败")
		}
	}
	cancels := &cancelTracker{}
	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Sumika.StartupTimeoutMs)*time.Millisecond)
	defer cancel()
	var t mcp.Transport
	var diag *startDiag
	if cfg.Type == "http" {
		hc := &http.Client{Transport: headerTransport{headers: cfg.Headers, base: http.DefaultTransport, s: s, journal: c.journal, cancels: cancels}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		t = &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: hc, MaxRetries: -1, MaxEventSize: 16 << 20}
	} else {
		diag = &startDiag{}
		t = &checkedTransport{base: &processTransport{config: cfg, diag: diag, logDir: logDir, onTree: func(err error) { c.markUnconfirmed(s, cfg.ID, err) }}, s: s, journal: c.journal, cancels: cancels}
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "sumika", Version: "kernel-k4"}, nil).Connect(connectCtx, t, nil)
	if err != nil {
		timedOut := errors.Is(connectCtx.Err(), context.DeadlineExceeded)
		switch {
		case diag != nil: // 本机程序：写明是没找到、没启动起来、超时，还是启动了没回话，加上它 stderr 的末尾（凭据抹掉）
			return fmt.Errorf("MCP 握手失败（%s）：%s", cfg.ID, diag.reason(cfg, timedOut))
		case timedOut:
			return fmt.Errorf("MCP 握手失败（%s）：等了 %d 秒还没连上，地址不通或服务没开", cfg.ID, cfg.Sumika.StartupTimeoutMs/1000)
		}
		return fmt.Errorf("MCP 握手失败（%s）", cfg.ID)
	}
	listCtx, stop := context.WithTimeout(ctx, time.Duration(cfg.Sumika.CatalogTimeoutMs)*time.Millisecond)
	defer stop()
	entries, err := catalog(listCtx, session, cfg, binding)
	if err != nil {
		session.Close()
		return err
	}
	if afterCatalogHook != nil {
		afterCatalogHook(s) // 测试里用：握手和目录都好了、发布之前，插进一次“代次变化”
	}
	s.mu.Lock()
	// 关连接都放在锁外：关本机进程要几秒，还会回调进来（进程树没确认退出时记标记），不能占着 s.mu
	if s.binding != binding || s.generation != generation {
		s.mu.Unlock()
		session.Close()
		return fmt.Errorf("MCP 配置或连接代次已变")
	}
	// 握手期间旧调用可能变成 unknown，发布目录前仍需复核持久隔离。
	blocked, err = c.journal.MCPBlocked(ctx, cfg.ID)
	if err != nil || blocked {
		s.mu.Unlock()
		session.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("%w：%s", ErrQuarantined, cfg.ID)
	}
	s.session = session
	s.cancels = cancels
	s.entries = entries
	s.active = true
	c.recoverMu.Lock()
	if !c.closed {
		c.recoverWG.Add(1)
		go func() {
			defer c.recoverWG.Done()
			c.watch(s, session, generation)
		}()
	}
	c.recoverMu.Unlock()
	s.mu.Unlock()
	return nil
}

// Disable 是显式停用（配置关掉、重载、关机）：这个服务正在进行的自动重连作废。
func (c *Client) Disable(id string) error { return c.disable(id, true) }

// disable 的 explicit 为假是调用失败后宿主自己关连接：保留重连状态，之后由 recoverAfterFailure 决定要不要重连。
func (c *Client) disable(id string, explicit bool) error {
	c.mu.Lock()
	s := c.servers[id]
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if explicit {
		s.recovery.reset()
	}
	s.active = false
	s.generation++
	old, oldCancels := s.session, s.cancels
	s.session = nil
	s.cancels = nil
	s.entries = nil
	s.mu.Unlock()
	if old != nil {
		return c.closeOld(id, old, oldCancels)
	}
	return nil
}
func (c *Client) Close() error {
	c.recoverMu.Lock()
	c.closed = true
	c.recoverMu.Unlock()
	c.cancel() // 掐断正在进行的重连
	c.cancelAllJobs()
	c.mu.Lock()
	ids := make([]string, 0, len(c.servers))
	for id := range c.servers {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	var errs []error
	for _, id := range ids {
		errs = append(errs, c.Disable(id))
	}
	c.recoverWG.Wait() // 重连循环都已见到停用或取消并退出，关机后不会再起子进程
	return errors.Join(errs...)
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
	s       *server
	journal Journal
	cancels *cancelTracker
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	if r.Method == http.MethodPost && r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(body, (16<<20)+1))
		body.Close()
		if err != nil {
			return nil, err
		}
		if len(raw) > 16<<20 {
			return nil, fmt.Errorf("MCP 请求过大")
		}
		msg, err := jsonrpc.DecodeMessage(raw)
		if err != nil {
			return nil, err
		}
		if request, ok := msg.(*jsonrpc.Request); ok && request.Method == cancelledMethod {
			defer t.cancels.note()
		}
		if request, ok := msg.(*jsonrpc.Request); ok && request.Method == "tools/call" {
			s := t.s
			s.mu.Lock()
			p := s.pending
			if p == nil || !p.validLocked(r.Context()) {
				s.mu.Unlock()
				return nil, ecosystem.ErrDenied
			}
			err = t.journal.MCPDispatch(r.Context(), p.id, s.config.ID, s.binding, dispatchTransport(s.config))
			if err == nil {
				p.dispatched = true
			}
			// 锁只管“校验 + 记下派发意图”。RoundTrip 要等到远端返回响应头，工具慢时能等整个调用时限，
			// 这段时间不能占着 s.mu，否则同服务的搜索、解析、停用乃至全部服务的 Resolve 都被它堵住。
			// 记下 dispatched 之后再被停用，请求最多发不出去或落在正在关的连接上，结局照 unknown 记。
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return t.base.RoundTrip(r)
		}
	}
	return t.base.RoundTrip(r)
}

type checkedTransport struct {
	base    mcp.Transport
	s       *server
	journal Journal
	cancels *cancelTracker
}

func (t *checkedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &checkedConnection{Connection: conn, s: t.s, journal: t.journal, cancels: t.cancels}, nil
}

type checkedConnection struct {
	mcp.Connection
	s       *server
	journal Journal
	cancels *cancelTracker
}

func (c *checkedConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	r, ok := msg.(*jsonrpc.Request)
	if ok && r.Method == cancelledMethod {
		defer c.cancels.note()
	}
	if !ok || r.Method != "tools/call" {
		return c.Connection.Write(ctx, msg)
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	if p == nil || !p.validLocked(ctx) {
		return ecosystem.ErrDenied
	}
	// 先持久化派发意图；写入失败也保留 unknown，禁止自动再调。
	if err := c.journal.MCPDispatch(ctx, p.id, s.config.ID, s.binding, dispatchTransport(s.config)); err != nil {
		return err
	}
	p.dispatched = true
	return c.Connection.Write(ctx, msg)
}
func visible(a ecosystem.Access, s *server) bool {
	return s.active && a.Check(context.Background(), "mcp:"+s.config.ID) == nil
}
func included(c ServerConfig, raw string) bool {
	return (slices.Contains(c.Sumika.Include, raw) || slices.Contains(c.Sumika.Include, "*")) && !slices.Contains(c.Sumika.Exclude, raw)
}
func publicID(server, tool string) string {
	raw := "mcp__" + server + "__" + tool
	var b strings.Builder
	loss := false
	for _, r := range raw {
		if r > 127 || !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			b.WriteByte('_')
			loss = true
		} else {
			b.WriteRune(r)
		}
	}
	id := b.String()
	if len(id) > 64 {
		loss = true
	}
	if loss {
		if len(id) > 51 {
			id = id[:51]
		}
		id += "_" + ecosystem.Digest([]string{server, tool})[:12]
	}
	return id
}
