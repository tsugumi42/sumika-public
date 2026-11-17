// Package rpcserve 把 api.Reader 开成界面协议的服务端（规格-Bun后台第1批 第四、五节）。
//
// 只开 Reader 的 8 个方法（加 initialize 和订阅的收发），不开任何写接口。一条连接一个 conn：
// 第一个请求必须是 initialize；每个请求各起一个任务（看图慢不挡别的），回复和通知共用一把写锁；
// 连接断了收掉这条连接的全部订阅。传输（WebSocket、进程内）实现 Transport 就能接。
package rpcserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcconv"
	"sumika-native/internal/rpcwire"
)

// Transport 是一条连接：一次读一整条消息、一次写一整条消息
type Transport interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Server 是一个后台的协议服务端；可以同时服务多条连接
type Server struct {
	reader  api.Reader
	version string
	writes  bool // 开不开第 2 批的写方法（规格-Bun后台第2批 4.3）
}

// Option 配置服务端
type Option func(*Server)

// WithWrites 打开第 2 批的 35 个方法（本地私聊、审批、检查点……）。默认不开：-rpc-listen 开到 WebSocket 上的只有 Reader 的 8 个。
// 只给进程内管道（主窗口默认路径）用，那里没有令牌也不开端口。
func WithWrites() Option { return func(s *Server) { s.writes = true } }

// New 包一个 Reader。version 写进 initialize 的 server.version
func New(r api.Reader, version string, opts ...Option) *Server {
	s := &Server{reader: r, version: version}
	for _, o := range opts {
		o(s)
	}
	return s
}

// 每条连接同时在处理的请求上限：超了就排队读，不无限起任务
const maxInflight = 16

// callTimeout 是单个请求的上限；Reader 自己默认 5 秒，这里给读原图等慢请求留余量
const callTimeout = 30 * time.Second

// Methods 是这个服务端实际开了的请求方法（initialize 回给客户端）：第 1 批的 Reader 方法；
// 带 WithWrites 时再加上后台对象真实现了对应接口的第 2 批方法
func (s *Server) Methods() []string {
	var out []string
	for _, m := range rpcwire.Methods {
		if m.Notification {
			continue
		}
		if readMethods[m.Name] {
			out = append(out, m.Name)
		} else if s.writes {
			if has := writeCaps[m.Name]; has != nil && has(s.reader) {
				out = append(out, m.Name)
			}
		}
	}
	return out
}

// enabled 是这个方法在这个服务端上开没开（不看后台对象实现了没有：没实现回 1001）
func (s *Server) enabled(name string) bool {
	if readMethods[name] {
		return true
	}
	return s.writes && writeCaps[name] != nil
}

type conn struct {
	s      *Server
	t      Transport
	ctx    context.Context
	cancel context.CancelFunc

	wmu sync.Mutex // 写

	mu          sync.Mutex
	initialized bool
	subs        map[string]func()
	nextSub     int

	wg  sync.WaitGroup
	sem chan struct{}
}

// Serve 服务一条连接，直到读出错（对方断开）或 ctx 取消；返回前收掉全部订阅、等在跑的请求结束
func (s *Server) Serve(ctx context.Context, t Transport) error {
	ctx, cancel := context.WithCancel(ctx)
	c := &conn{s: s, t: t, ctx: ctx, cancel: cancel, subs: map[string]func(){}, sem: make(chan struct{}, maxInflight)}
	defer func() {
		cancel()
		c.closeSubs()
		c.wg.Wait()
		t.Close()
	}()
	for {
		raw, err := t.Read(ctx)
		if err != nil {
			return err
		}
		// 进程内管道不限帧（界面发的图可能超过 32 MiB 的 base64，规格第七节）；WebSocket 本来就由 SetReadLimit 卡着，这里再核一次
		if _, inproc := t.(*chanTransport); !inproc && len(raw) > rpcwire.MaxRequestBytes {
			return fmt.Errorf("请求超过 %d 字节，断开", rpcwire.MaxRequestBytes)
		}
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		c.wg.Add(1)
		go func() {
			defer func() { <-c.sem; c.wg.Done() }()
			c.handle(raw)
		}()
	}
}

func (c *conn) closeSubs() {
	c.mu.Lock()
	subs := c.subs
	c.subs = map[string]func(){}
	c.mu.Unlock()
	for _, close := range subs {
		close()
	}
}

func (c *conn) send(m rpcwire.Message) {
	m.JSONRPC = rpcwire.Version
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(rpcwire.Message{JSONRPC: rpcwire.Version, ID: m.ID, Error: &rpcwire.Error{Code: rpcwire.CodeInternal, Message: "回复编码失败"}})
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.t.Write(c.ctx, b); err != nil {
		c.cancel() // 写不出去就当连接断了
	}
}

func (c *conn) notify(method string, params any) {
	b, err := json.Marshal(params)
	if err != nil {
		return
	}
	c.send(rpcwire.Message{Method: method, Params: b})
}

func errMsg(code int, msg string) *rpcwire.Error { return &rpcwire.Error{Code: code, Message: msg} }

func (c *conn) handle(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		c.send(rpcwire.Message{ID: json.RawMessage("null"), Error: errMsg(rpcwire.CodeInvalidRequest, "不支持批量请求")})
		return
	}
	var m rpcwire.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		c.send(rpcwire.Message{ID: json.RawMessage("null"), Error: errMsg(rpcwire.CodeParse, "不是合法的 JSON")})
		return
	}
	if m.JSONRPC != rpcwire.Version || m.Method == "" || m.Result != nil || m.Error != nil {
		if len(m.ID) == 0 {
			return // 客户端发来的通知或回复：第 1 批没有，忽略
		}
		c.send(rpcwire.Message{ID: m.ID, Error: errMsg(rpcwire.CodeInvalidRequest, "不是合法的 JSON-RPC 2.0 请求")})
		return
	}
	if !validID(m.ID) {
		c.send(rpcwire.Message{ID: json.RawMessage("null"), Error: errMsg(rpcwire.CodeInvalidRequest, "id 只能是数字或字符串")})
		return
	}
	result, rerr := c.dispatch(m.Method, m.Params)
	var start func()
	if sub, ok := result.(subscribed); ok {
		result, start = sub.res, sub.start
		defer start() // 先回订阅号，再推第一条快照：客户端收到通知时一定已经认得这个订阅号
	}
	if len(m.ID) == 0 {
		return // 通知不回
	}
	if rerr != nil {
		c.send(rpcwire.Message{ID: m.ID, Error: rerr})
		return
	}
	b, err := json.Marshal(result)
	if err != nil {
		c.send(rpcwire.Message{ID: m.ID, Error: errMsg(rpcwire.CodeInternal, "结果编码失败")})
		return
	}
	c.send(rpcwire.Message{ID: m.ID, Result: b})
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	switch id[0] {
	case '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

// decode 严格解参数：不认识的字段、类型不对都是参数错；没有 params 当空对象
func decode(raw json.RawMessage, v any) *rpcwire.Error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	if raw[0] != '{' {
		return errMsg(rpcwire.CodeInvalidParams, "params 必须是对象")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errMsg(rpcwire.CodeInvalidParams, "参数不对："+err.Error())
	}
	return nil
}

// callErr 把 Reader 返回的 error 变成协议错误；取消和超时单独说
func callErr(err error) *rpcwire.Error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errMsg(rpcwire.CodeFailed, "后台查询超时")
	case errors.Is(err, context.Canceled):
		return errMsg(rpcwire.CodeFailed, "请求已取消")
	}
	return errMsg(rpcwire.CodeFailed, err.Error())
}

// unsubscribe 收掉这条连接上的一个订阅
func (c *conn) unsubscribe(id string) (any, *rpcwire.Error) {
	c.mu.Lock()
	close, ok := c.subs[id]
	delete(c.subs, id)
	c.mu.Unlock()
	if !ok {
		return nil, errMsg(rpcwire.CodeInvalidParams, "没有这个订阅")
	}
	close()
	return rpcwire.Empty{}, nil
}

func (c *conn) dispatch(method string, params json.RawMessage) (any, *rpcwire.Error) {
	if method == rpcwire.MethodInitialize {
		var p rpcwire.InitializeParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if p.ProtocolVersion != rpcwire.ProtocolVersion {
			data, _ := json.Marshal(map[string]int{"protocol_version": rpcwire.ProtocolVersion})
			return nil, &rpcwire.Error{Code: rpcwire.CodeInvalidParams, Message: fmt.Sprintf("协议版本不一致：服务端是 %d", rpcwire.ProtocolVersion), Data: data}
		}
		c.mu.Lock()
		c.initialized = true
		c.mu.Unlock()
		return rpcwire.InitializeResult{ProtocolVersion: rpcwire.ProtocolVersion, Server: rpcwire.ServerInfo{Impl: "go", Version: c.s.version}, Methods: c.s.Methods()}, nil
	}
	c.mu.Lock()
	ok := c.initialized
	c.mu.Unlock()
	if !ok {
		if !c.s.enabled(method) {
			return nil, errMsg(rpcwire.CodeMethodNotFound, "没有这个方法："+method)
		}
		return nil, errMsg(rpcwire.CodeNotInitialized, "先调 initialize")
	}
	ctx, cancel := context.WithTimeout(c.ctx, callTimeout)
	if noTimeout[method] {
		// 整轮模型回复、检查点还原可能跑很久：只跟着连接的寿命走
		ctx, cancel = context.WithCancel(c.ctx)
	}
	defer cancel()
	r := c.s.reader
	switch method {
	case rpcwire.MethodRuntimeRead:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		return first(ctx, func(ctx context.Context) (api.Subscription[api.RuntimeView], error) { return r.WatchRuntime(ctx) }, rpcconv.RuntimeOut)
	case rpcwire.MethodOverviewRead:
		var p rpcwire.OverviewRequest
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return first(ctx, func(ctx context.Context) (api.Subscription[api.Overview], error) {
			return r.WatchOverview(ctx, rpcconv.OverviewRequestIn(p))
		}, rpcconv.OverviewOut)
	case rpcwire.MethodMessagesRead:
		var p rpcwire.MessagesReadParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		res, err := r.ReadMessages(ctx, rpcconv.ConversationRequestIn(p.Request), api.Cursor(p.Cursor))
		if err != nil {
			return nil, callErr(err)
		}
		return rpcconv.ResultOut(res, rpcconv.PageOut), nil
	case rpcwire.MethodMediaRead:
		var p rpcwire.MediaReadParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		res, err := r.ReadMedia(ctx, rpcconv.MediaRefIn(p.Ref), api.MediaTier(p.Tier))
		if err != nil {
			return nil, callErr(err)
		}
		return rpcconv.ResultOut(res, rpcconv.MediaOut), nil
	case rpcwire.MethodFacesIndex:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		res, err := r.ReadFaceIndex(ctx)
		if err != nil {
			return nil, callErr(err)
		}
		return rpcconv.ResultOut(res, rpcconv.FacesOut), nil
	case rpcwire.MethodFacesPNG:
		var p rpcwire.FacePNGParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		res, err := r.ReadFacePNG(ctx, p.ID)
		if err != nil {
			return nil, callErr(err)
		}
		return rpcconv.ResultOut(res, rpcconv.MediaOut), nil
	case rpcwire.MethodSettingsRead:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		res, err := r.ReadSettings(ctx)
		if err != nil {
			return nil, callErr(err)
		}
		return rpcconv.ResultOut(res, rpcconv.SettingsOut), nil
	case rpcwire.MethodRuntimeSubscribe:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodRuntimeSnapshot, func(ctx context.Context) (api.Subscription[api.RuntimeView], error) { return r.WatchRuntime(ctx) },
			func(id string, v uint64, res api.Result[api.RuntimeView]) any {
				return rpcwire.RuntimeSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.RuntimeOut)}
			})
	case rpcwire.MethodOverviewSubscribe:
		var p rpcwire.OverviewRequest
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodOverviewSnapshot, func(ctx context.Context) (api.Subscription[api.Overview], error) {
			return r.WatchOverview(ctx, rpcconv.OverviewRequestIn(p))
		}, func(id string, v uint64, res api.Result[api.Overview]) any {
			return rpcwire.OverviewSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.OverviewOut)}
		})
	case rpcwire.MethodConversationSubscribe:
		var p rpcwire.ConversationRequest
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodConversationSnapshot, func(ctx context.Context) (api.Subscription[api.ConversationSnapshot], error) {
			return r.WatchConversation(ctx, rpcconv.ConversationRequestIn(p))
		}, func(id string, v uint64, res api.Result[api.ConversationSnapshot]) any {
			return rpcwire.ConversationSnapshotNotice{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.SnapshotOut)}
		})
	case rpcwire.MethodRuntimeUnsubscribe, rpcwire.MethodOverviewUnsubscribe, rpcwire.MethodConversationUnsubscribe:
		var p rpcwire.UnsubscribeParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return c.unsubscribe(p.Subscription)
	}
	if c.s.writes && writeCaps[method] != nil {
		return c.dispatchWrite(ctx, method, params)
	}
	return nil, errMsg(rpcwire.CodeMethodNotFound, "没有这个方法："+method)
}

// first 订阅一下取初始快照就关：Reader 只有 Watch，没有一次性读概览和运行状态的方法
func first[A, B any](ctx context.Context, watch func(context.Context) (api.Subscription[A], error), out func(A) B) (any, *rpcwire.Error) {
	sub, err := watch(ctx)
	if err != nil {
		return nil, callErr(err)
	}
	defer sub.Close()
	select {
	case snap, ok := <-sub.Updates():
		if !ok {
			return nil, errMsg(rpcwire.CodeFailed, "订阅没给初始快照就关了")
		}
		return rpcconv.ResultOut(snap.Result, out), nil
	case <-ctx.Done():
		return nil, callErr(ctx.Err())
	}
}

// subscribe 在连接的寿命里开一个订阅，把每个快照推成通知；回订阅号
func subscribe[T any](c *conn, method string, watch func(context.Context) (api.Subscription[T], error), notice func(string, uint64, api.Result[T]) any) (any, *rpcwire.Error) {
	ctx, cancel := context.WithCancel(c.ctx)
	sub, err := watch(ctx)
	if err != nil {
		cancel()
		return nil, wireErr(err)
	}
	c.mu.Lock()
	c.nextSub++
	id := "s" + strconv.Itoa(c.nextSub)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			sub.Close()
			<-done
		})
	}
	c.subs[id] = stop
	c.mu.Unlock()
	gate := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
		for {
			select {
			case snap, ok := <-sub.Updates():
				if !ok {
					return
				}
				c.notify(method, notice(id, snap.Version, snap.Result))
			case <-ctx.Done():
				return
			}
		}
	}()
	return subscribed{res: rpcwire.SubscribeResult{Subscription: id}, start: func() { close(gate) }}, nil
}

// subscribed 是订阅方法的结果：handle 回完订阅号再调 start 放开转发
type subscribed struct {
	res   rpcwire.SubscribeResult
	start func()
}
