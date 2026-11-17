package turn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// 假网关不走真 TCP：每个夹具起一个 httptest 服务器、客户端连几次，都是真连接，关掉以后每条连接在 Windows 上留一个
// TIME_WAIT（120 秒），临时端口池只有 16384 个。并行以后连接的速率涨了十倍，连跑几遍或和别的包的测试一起跑就把端口耗尽，
// 报“Only one usage of each socket address”，所有要连假网关的测试一起红。
// 这里把假网关挂在内存里的监听器上：服务端仍是真的 http.Server（流式、取消照旧），连接是 net.Pipe，不占端口。
// 客户端走 http.DefaultTransport（llm.New 创建时取它），TestMain 里换成认识“*.invalid”主机名的版本，其余主机照旧走真网络。

var (
	memHosts   sync.Map // "mem-N.invalid:80" → *memListener
	memCounter atomic.Int64
	memDials   atomic.Int64
)

type memAddr string

func (a memAddr) Network() string { return "tcp" }
func (a memAddr) String() string  { return string(a) }

type memListener struct {
	addr memAddr
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newMemListener() *memListener {
	return &memListener{
		addr: memAddr(fmt.Sprintf("mem-%d.invalid:80", memCounter.Add(1))),
		ch:   make(chan net.Conn),
		done: make(chan struct{}),
	}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		memHosts.Delete(string(l.addr))
	})
	return nil
}

func (l *memListener) Addr() net.Addr { return l.addr }

func (l *memListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.ch <- server:
		memDials.Add(1)
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, errors.New("假网关已经关了")
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}

// memServer 和 httptest.NewServer 一样返回已经启动的服务器，URL 是 http://mem-N.invalid:80
func memServer(h http.Handler) *httptest.Server {
	l := newMemListener()
	memHosts.Store(string(l.addr), l)
	s := &httptest.Server{Listener: l, Config: &http.Server{Handler: h}}
	s.Start()
	return s
}

// installMemTransport 把 http.DefaultTransport 换成认识 *.invalid 的版本；要在任何测试创建客户端之前调用
func installMemTransport() {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	tr := base.Clone()
	realDial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if v, ok := memHosts.Load(addr); ok {
			return v.(*memListener).dial(ctx)
		}
		if strings.HasSuffix(strings.Split(addr, ":")[0], ".invalid") {
			return nil, errors.New("假网关已经关了：" + addr)
		}
		return realDial(ctx, network, addr)
	}
	realProxy := tr.Proxy
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		if strings.HasSuffix(r.URL.Hostname(), ".invalid") || realProxy == nil {
			return nil, nil
		}
		return realProxy(r)
	}
	http.DefaultTransport = tr
}
