package linkreader

// 连接健康（规格-链接发送改版 C3 的“全超时重试”）：代理换节点或隧道断掉后，池里的 HTTP/2 连接已经死了，
// 但传输层发现不了；响应头超时只重置流、不断连接，下一轮取数（包括 5 秒后的整条重试）会继续往死连接里塞请求。
// 这里用本机 HTTP/2 服务复现：第一条连接处理完第一个请求后变成黑洞（读进来不回、不关），之后新拨的连接正常。

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type connIndexKey struct{}

const connHealthIssueBody = `{"html_url":"https://github.com/example-lab/widget/issues/1","title":"示例问题","user":{"login":"example"}}`

// startConnHealthServer 起本地 HTTP/2 服务并返回已接受的连接数（ConnContext 按接受顺序编号）。
// 第一条连接服务完第一个请求后，后续请求都停在处理函数里：请求读进来了，不回也不关。
func startConnHealthServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	var accepted, servedFirst atomic.Int32
	var sawH2 atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			sawH2.Store(true)
		}
		if r.Context().Value(connIndexKey{}) == int32(1) && servedFirst.Add(1) > 1 {
			<-r.Context().Done()
			return
		}
		io.WriteString(w, connHealthIssueBody)
	})
	s := httptest.NewUnstartedServer(handler)
	s.EnableHTTP2 = true
	s.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connIndexKey{}, accepted.Add(1))
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s, &accepted, &sawH2
}

// connHealthTransport 拨向本地测试服务。old = 修复前的写法：没有 IdleConnTimeout，也没开 HTTP/2 探活。
func connHealthTransport(server *httptest.Server, old bool, responseHeaderTimeout time.Duration) *http.Transport {
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	var tr *http.Transport
	if old {
		tr = &http.Transport{DialContext: dial, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: responseHeaderTimeout, ForceAttemptHTTP2: true}
	} else {
		tr = newLinkTransport(dial, time.Second)
		tr.ResponseHeaderTimeout = responseHeaderTimeout
		tr.ForceAttemptHTTP2 = true
	}
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return tr
}

// TestConnHealthRetryUsesNewConn：第二次取数先撞上黑洞连接、全超时；重试前丢掉池里的连接，第二轮从新连接成功。
func TestConnHealthRetryUsesNewConn(t *testing.T) {
	server, conns, sawH2 := startConnHealthServer(t)
	tr := connHealthTransport(server, false, 200*time.Millisecond)
	defer tr.CloseIdleConnections()
	f, err := NewFetcher(FetchOptions{Transport: tr, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.roundTime = 2 * time.Second
	f.retry = retryTiming{delay: 20 * time.Millisecond, minRound: 100 * time.Millisecond, maxRound: time.Second}
	l := link(t, "https://github.com/example-lab/widget/issues/1")

	if _, err := f.Fetch(context.Background(), l); err != nil {
		t.Fatalf("第一次取数：%v", err)
	}
	if !sawH2.Load() {
		t.Fatal("测试服务没走 HTTP/2，死连接复用的问题复现不了")
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("第一次取数应只有一条连接，实际 %d", got)
	}

	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatalf("全超时后的第二轮应换新连接成功：%v", err)
	}
	if !r.RetriedAfterTimeout {
		t.Fatal("第二轮应标记 RetriedAfterTimeout")
	}
	if got := conns.Load(); got != 2 {
		t.Fatalf("第二轮应新拨一条连接；实际 %d 条", got)
	}
}

// TestConnHealthOldTransportReusesDeadConn 是改坏对照：旧参数（不丢连接）下，超时后重试仍落在同一条死连接上。
func TestConnHealthOldTransportReusesDeadConn(t *testing.T) {
	server, conns, sawH2 := startConnHealthServer(t)
	tr := connHealthTransport(server, true, 200*time.Millisecond)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	get := func() error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/repos/example-lab/widget/issues/1", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}
	if err := get(); err != nil {
		t.Fatalf("第一次请求：%v", err)
	}
	if !sawH2.Load() {
		t.Fatal("测试服务没走 HTTP/2，死连接复用的问题复现不了")
	}
	// 修复前：响应头超时只重置流、连接仍留在池里；重试和重试之后的请求都还是同一条连接、还是超时。
	for i := 2; i <= 3; i++ {
		err := get()
		var ne net.Error
		if err == nil || !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("第 %d 次请求应超时（旧写法复用死连接）；实际 %v", i, err)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("旧写法不该换连接；实际拨了 %d 条", got)
	}
}

// TestConnHealthTransportSettings 盯住探活参数：15/5 秒没法在短测试里等出来，只能核配置本身。
func TestConnHealthTransportSettings(t *testing.T) {
	f, err := NewFetcher(FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*http.Client{"main": f.client, "bili": f.biliClient} {
		tr, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("%s：传输类型 %T", name, client.Transport)
		}
		if tr.IdleConnTimeout != 30*time.Second {
			t.Fatalf("%s：IdleConnTimeout = %v，想要 30s", name, tr.IdleConnTimeout)
		}
		if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != 15*time.Second || tr.HTTP2.PingTimeout != 5*time.Second {
			t.Fatalf("%s：HTTP/2 探活 = %+v，想要 15s/5s", name, tr.HTTP2)
		}
	}
}
