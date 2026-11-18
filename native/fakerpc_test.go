package main

// 测试用的假协议传输：只记收到了哪个协议方法，按表回固定结果；initialize 回全部方法。不起真后台。
// 桥的分流测试（bunbridge_test.go）和 Bun 子进程监护的测试（bunchild_test.go）共用。

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
)

type fakeRPC struct {
	mu      sync.Mutex
	calls   []string
	seen    chan string
	out     chan []byte
	done    chan struct{}
	once    sync.Once
	onCall  func(f *fakeRPC, m rpcwire.Message) // 对某个请求额外做点事（推通知）
	methods []string
	results map[string]any             // 某个方法的固定回复；没写的回空对象
	errs    map[string]*rpcwire.Error  // 某个方法回的协议错误（优先于 results）
	params  map[string]json.RawMessage // 某个方法最近一次收到的参数
}

func newFakeRPC() *fakeRPC {
	f := &fakeRPC{out: make(chan []byte, 256), done: make(chan struct{}), seen: make(chan string, 256)}
	for _, m := range rpcwire.Methods {
		if !m.Notification {
			f.methods = append(f.methods, m.Name)
		}
	}
	return f
}

func (f *fakeRPC) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-f.out:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return nil, io.EOF
	}
}

func (f *fakeRPC) push(v any) {
	b, _ := json.Marshal(v)
	select {
	case f.out <- b:
	case <-f.done:
	}
}

func (f *fakeRPC) Write(_ context.Context, msg []byte) error {
	var m rpcwire.Message
	if json.Unmarshal(msg, &m) != nil || len(m.ID) == 0 {
		return nil
	}
	var result any = struct{}{}
	if m.Method == rpcwire.MethodInitialize {
		result = rpcwire.InitializeResult{ProtocolVersion: rpcwire.ProtocolVersion, Server: rpcwire.ServerInfo{Impl: "fake"}, Methods: f.methods}
	} else {
		f.mu.Lock()
		f.calls = append(f.calls, m.Method)
		if f.params == nil {
			f.params = map[string]json.RawMessage{}
		}
		f.params[m.Method] = m.Params
		f.mu.Unlock()
		select {
		case f.seen <- m.Method:
		default:
		}
		if strings.HasSuffix(m.Method, ".subscribe") {
			result = rpcwire.SubscribeResult{Subscription: "s-" + m.Method}
		}
		if r, ok := f.results[m.Method]; ok {
			result = r
		}
		if f.onCall != nil {
			f.onCall(f, m)
		}
	}
	if e, ok := f.errs[m.Method]; ok {
		f.push(rpcwire.Message{JSONRPC: rpcwire.Version, ID: m.ID, Error: e})
		return nil
	}
	raw, _ := json.Marshal(result)
	f.push(rpcwire.Message{JSONRPC: rpcwire.Version, ID: m.ID, Result: raw})
	return nil
}

func (f *fakeRPC) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

// paramsOf 是某个方法最近一次收到的参数
func (f *fakeRPC) paramsOf(method string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.params[method]
}

func (f *fakeRPC) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRPC) full(t *testing.T) *rpcclient.Full {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := rpcclient.New(ctx, f, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return rpcclient.NewFull(c)
}
