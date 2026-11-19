// Package rpcclient 是界面协议的客户端（规格-Bun后台第1批 第五节）：连上 Go 或 Bun 后台，实现 api.Reader。
//
// 后台没实现的方法（initialize 回的 methods 里没有）回 Unsupported；没实现订阅但实现了一次性读的，
// 退回"读一次、当成只有一个快照的订阅"，界面照样能看会话和消息（Bun 第 1 批就是这样）。
// 这个客户端只读：不提供任何写接口，界面对它做写接口的类型断言都会落空，按"只读"降级。
package rpcclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcwire"
)

// Transport 和 rpcserve.Transport 同形；这里再声明一次，免得客户端链接服务端
type Transport interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// ErrClosed 是连接已经断了
var ErrClosed = errors.New("和后台的连接断了")

type call struct {
	ch   chan rpcwire.Message
	hook func(rpcwire.Message) // 在读循环里、交出结果之前跑：订阅在这里登记，保证随后的通知认得订阅号
}

// Client 是一条到后台的连接
type Client struct {
	t      Transport
	ctx    context.Context
	cancel context.CancelFunc

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]call
	subs    map[string]*subscription
	err     error

	nextStream int                     // 本地私聊发送的草稿流号（full.go）
	streams    map[string]*draftStream // 流号 → 那次发送的草稿队列

	Server  rpcwire.ServerInfo
	methods map[string]bool
	done    chan struct{}
}

// New 在一条已经连上的传输上握手。client 是写进 initialize 的客户端名字
func New(ctx context.Context, t Transport, client string) (*Client, error) {
	cctx, cancel := context.WithCancel(context.Background())
	c := &Client{t: t, ctx: cctx, cancel: cancel, pending: map[int64]call{}, subs: map[string]*subscription{}, methods: map[string]bool{}, done: make(chan struct{})}
	go c.readLoop()
	var res rpcwire.InitializeResult
	if err := c.Call(ctx, rpcwire.MethodInitialize, rpcwire.InitializeParams{ProtocolVersion: rpcwire.ProtocolVersion, Client: client}, &res); err != nil {
		c.Close()
		return nil, fmt.Errorf("和后台握手：%w", err)
	}
	c.Server = res.Server
	for _, m := range res.Methods {
		c.methods[m] = true
	}
	return c, nil
}

// Dial 连一个 ws://127.0.0.1:<端口>/rpc。只接受回环地址：令牌不能发到别的机器上
func Dial(ctx context.Context, rawURL, token, client string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "ws" {
		return nil, fmt.Errorf("后台地址要是 ws://127.0.0.1:<端口>/rpc")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("后台地址只能是本机回环（127.0.0.1），不是 %s", u.Hostname())
	}
	// 不跟随重定向：令牌只发给这一个回环地址
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, _, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{HTTPClient: noRedirect, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, fmt.Errorf("连后台：%w", err)
	}
	conn.SetReadLimit(rpcwire.MaxResponseBytes)
	return New(ctx, &wsTransport{c: conn}, client)
}

// DialRunFile 读数据目录里的 run/rpc.json 再连；进程已经不在了就当没有后台
func DialRunFile(ctx context.Context, path, client string) (*Client, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("没有开着的后台（读不到 %s）", path)
	}
	var rf rpcwire.RunFile
	if err := json.Unmarshal(b, &rf); err != nil {
		return nil, fmt.Errorf("后台的地址文件坏了：%w", err)
	}
	if !rpcwire.ProcessAlive(rf.PID) {
		return nil, fmt.Errorf("地址文件里的后台（进程 %d）已经不在了", rf.PID)
	}
	return Dial(ctx, rf.URL, rf.Token, client)
}

// Done 在连接断了（或 Close）以后关闭
func (c *Client) Done() <-chan struct{} { return c.done }

// Has 是后台有没有实现这个方法
func (c *Client) Has(method string) bool { return c.methods[method] }

// Close 断开；正在等的请求都回 ErrClosed，订阅的通道都关掉
func (c *Client) Close() error {
	c.cancel()
	err := c.t.Close()
	<-c.done
	return err
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	pending := c.pending
	c.pending = map[int64]call{}
	subs := c.subs
	c.subs = map[string]*subscription{}
	c.mu.Unlock()
	for _, p := range pending {
		close(p.ch)
	}
	for _, s := range subs {
		s.end()
	}
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		raw, err := c.t.Read(c.ctx)
		if err != nil {
			c.fail(ErrClosed)
			return
		}
		var m rpcwire.Message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.Method != "" && len(m.ID) == 0 {
			c.onNotice(m)
			continue
		}
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			continue
		}
		c.mu.Lock()
		p, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if !ok {
			continue
		}
		if p.hook != nil && m.Error == nil {
			p.hook(m)
		}
		p.ch <- m
	}
}

func (c *Client) onNotice(m rpcwire.Message) {
	if m.Method == rpcwire.MethodLocalDraft {
		c.onDraft(m.Params)
		return
	}
	if m.Method == rpcwire.MethodLocalGroupProcess {
		c.onGroupProcess(m.Params)
		return
	}
	var head struct {
		Subscription string `json:"subscription"`
	}
	if json.Unmarshal(m.Params, &head) != nil {
		return
	}
	c.mu.Lock()
	s := c.subs[head.Subscription]
	c.mu.Unlock()
	if s != nil {
		s.push(m.Params)
	}
}

// Call 发一个请求等回复，结果解到 out（可以是 nil）
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.call(ctx, method, params, out, nil)
}

func (c *Client) call(ctx context.Context, method string, params, out any, hook func(rpcwire.Message)) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan rpcwire.Message, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = call{ch: ch, hook: hook}
	c.mu.Unlock()
	b, _ := json.Marshal(rpcwire.Message{JSONRPC: rpcwire.Version, ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: p})
	c.wmu.Lock()
	err = c.t.Write(ctx, b)
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("发请求：%w", err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// ───── 订阅 ─────

type subscription struct {
	mu     sync.Mutex
	ch     chan json.RawMessage
	closed bool
}

// push 只留最新的一个：快照是整份状态，合并不丢已提交的事实（api.Subscription 的约定允许合并）
func (s *subscription) push(b json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- b:
	default:
		select {
		case <-s.ch:
		default:
		}
		s.ch <- b
	}
}

func (s *subscription) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

type remoteSub[T any] struct {
	out  chan api.Snapshot[T]
	stop func()
	once sync.Once
}

func (r *remoteSub[T]) Updates() <-chan api.Snapshot[T] { return r.out }
func (r *remoteSub[T]) Close()                          { r.once.Do(r.stop) }

// watch 开一个远端订阅；snapshot 把通知解成 api 的快照
func watch[W, T any](ctx context.Context, c *Client, subscribe, unsubscribe string, params any, conv func(rpcwire.Result[W]) api.Result[T]) (api.Subscription[T], error) {
	s := &subscription{ch: make(chan json.RawMessage, 1)}
	var id string
	hook := func(m rpcwire.Message) {
		var res rpcwire.SubscribeResult
		if json.Unmarshal(m.Result, &res) == nil && res.Subscription != "" {
			c.mu.Lock()
			c.subs[res.Subscription] = s
			c.mu.Unlock()
		}
	}
	var res rpcwire.SubscribeResult
	if err := c.call(ctx, subscribe, params, &res, hook); err != nil {
		return nil, err
	}
	id = res.Subscription
	sctx, cancel := context.WithCancel(ctx)
	out := make(chan api.Snapshot[T], 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		for {
			select {
			case raw, ok := <-s.ch:
				if !ok {
					return
				}
				var n struct {
					Version int64             `json:"version"`
					Result  rpcwire.Result[W] `json:"result"`
				}
				if json.Unmarshal(raw, &n) != nil {
					continue
				}
				select {
				case out <- api.Snapshot[T]{Version: uint64(n.Version), Result: conv(n.Result)}:
				case <-sctx.Done():
					return
				}
			case <-sctx.Done():
				return
			}
		}
	}()
	rs := &remoteSub[T]{out: out}
	rs.stop = func() {
		cancel()
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
		s.end()
		<-done
		go func() { // 告诉后台收掉；连接断了也无妨
			uctx, ucancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer ucancel()
			c.Call(uctx, unsubscribe, rpcwire.UnsubscribeParams{Subscription: id}, nil)
		}()
	}
	return rs, nil
}

// once 用一次性读方法假装成订阅：发一个快照就不再发，Close 时关通道
func onceSub[T any](ctx context.Context, read func(context.Context) (api.Result[T], error)) (api.Subscription[T], error) {
	sctx, cancel := context.WithCancel(ctx)
	out := make(chan api.Snapshot[T], 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := read(sctx)
		if err != nil && res.State == "" {
			res = api.Result[T]{State: api.Failed, Err: err}
		}
		select {
		case out <- api.Snapshot[T]{Version: 1, Result: res}:
		case <-sctx.Done():
		}
		<-sctx.Done()
	}()
	rs := &remoteSub[T]{out: out}
	rs.stop = func() { cancel(); <-done; close(out) }
	return rs, nil
}

type wsTransport struct{ c *websocket.Conn }

func (t *wsTransport) Read(ctx context.Context) ([]byte, error) {
	_, b, err := t.c.Read(ctx)
	return b, err
}
func (t *wsTransport) Write(ctx context.Context, b []byte) error {
	return t.c.Write(ctx, websocket.MessageText, b)
}
func (t *wsTransport) Close() error { return t.c.Close(websocket.StatusNormalClosure, "") }
