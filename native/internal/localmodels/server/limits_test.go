package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// 验收 8c：监听的限额。令牌检查在读完请求头之后，不设上限的话本机任何程序都能慢速占住连接。

func TestDefaultLimits(t *testing.T) {
	l := DefaultLimits()
	if l.ReadHeaderTimeout != 10*time.Second || l.MaxConns != 32 {
		t.Errorf("生产值该是请求头 10 秒、同时 32 个连接：%+v", l)
	}
}

// startLimited 用真的 http.Server 加连接数上限起一个监听，返回地址。
func startLimited(t *testing.T, r *rig, l Limits) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := NewHTTPServer(r.srv.Handler(), l, nil)
	go func() { _ = Serve(hs, ln, l) }()
	t.Cleanup(func() { _ = hs.Close() })
	return ln.Addr().String()
}

func healthRequest(conn net.Conn) {
	fmt.Fprintf(conn, "GET /health HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n\r\n", testToken)
}

func TestSlowHeadersAreClosed(t *testing.T) {
	r := newRig(t, rigOpts{})
	addr := startLimited(t, r, Limits{ReadHeaderTimeout: time.Second, IdleTimeout: time.Minute, MaxConns: 4})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /health HTTP/1.1\r\nHost: x\r\n") // 请求头只发了一半，不发结尾的空行
	t0 := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("慢速发请求头的连接该被服务关掉，实际 %v（%v 后）", err, time.Since(t0))
	}
	if took := time.Since(t0); took < 800*time.Millisecond || took > 3*time.Second {
		t.Errorf("该在 ReadHeaderTimeout（1 秒）前后被关，实际 %v", took)
	}
}

func TestConnectionCap(t *testing.T) {
	r := newRig(t, rigOpts{})
	const cap = 4
	addr := startLimited(t, r, Limits{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxConns: cap})
	var held []net.Conn
	for i := 0; i < cap; i++ {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		healthRequest(conn)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("第 %d 个连接：%v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		held = append(held, conn) // 保持连接不关，占着名额
	}
	extra, err := net.Dial("tcp", addr) // 第 cap+1 个：TCP 层连得上，但服务不会接，请求排着
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	healthRequest(extra)
	br := bufio.NewReader(extra)
	_ = extra.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if resp, err := http.ReadResponse(br, nil); err == nil {
		resp.Body.Close()
		t.Fatalf("前 %d 个连接都还占着，第 %d 个不该有响应", cap, cap+1)
	}
	held[0].Close() // 走掉一个，排着的那个就该被接起来
	_ = extra.SetReadDeadline(time.Now().Add(3 * time.Second))
	// 刚才的读超时之后，bufio 里可能带着错误状态：换一个新的读缓冲
	resp, err := http.ReadResponse(bufio.NewReader(extra), nil)
	if err != nil {
		t.Fatalf("有名额放出来以后，排着的请求该得到响应：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("想要 200，实际 %d", resp.StatusCode)
	}
}

// 慢速滴灌的正文：声明了 100 字节，只给 10 字节就不动了。读正文的期限跟 --max-request-seconds，到点回 400，不一直占着连接
// （令牌检查在读正文之前，所以这只是持令牌的调用方才碰得到的，但一个卡死的客户端不该永远占着 32 个名额里的一个）。
func TestSlowBodyIsCutOff(t *testing.T) {
	r := newRig(t, rigOpts{maxRequest: 400 * time.Millisecond})
	conn, err := net.Dial("tcp", r.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /v1/embeddings HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"model\":\"x\"", testToken)
	t0 := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("正文卡住 400 毫秒的期限过了还没有响应：%v（%v 后）", err, time.Since(t0))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("想要 400，实际 %d", resp.StatusCode)
	}
	if took := time.Since(t0); took < 300*time.Millisecond || took > 2*time.Second {
		t.Errorf("该在 --max-request-seconds（400 毫秒）前后回，实际 %v", took)
	}
}
