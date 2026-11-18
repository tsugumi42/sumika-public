package rpcserve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/rpcwire"
)

// Listener 是开在本机回环上的 WebSocket 协议入口（规格第三节）
type Listener struct {
	URL     string
	token   string
	runFile string
	srv     *http.Server
	ln      net.Listener
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	once    sync.Once
}

// Listen 在 127.0.0.1 的随机端口上开 WebSocket，生成令牌，把地址和令牌写进 runFile（0600，先写临时文件再改名）。
// 地址写死回环，不读配置。runFile 为空只开不写（测试用）
func Listen(ctx context.Context, s *Server, runFile string) (*Listener, error) {
	// 同一个数据目录同一时刻只跑一个后台（规格第六节）：run 文件里的进程还活着、又不是自己，就不开
	if runFile != "" {
		if b, err := os.ReadFile(runFile); err == nil {
			var old rpcwire.RunFile
			if json.Unmarshal(b, &old) == nil && old.PID > 0 && old.PID != os.Getpid() && processAlive(old.PID) {
				return nil, fmt.Errorf("数据目录里已经有一个后台开着协议入口（%s，进程 %d）；同一时刻只跑一个后台", old.Impl, old.PID)
			}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("开协议端口：%w", err)
	}
	var tok [32]byte
	if _, err := rand.Read(tok[:]); err != nil {
		ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &Listener{URL: "ws://" + ln.Addr().String() + "/rpc", token: hex.EncodeToString(tok[:]), runFile: runFile, ln: ln, cancel: cancel}
	l.srv = &http.Server{Handler: l.handler(ctx, s), ReadHeaderTimeout: 5 * time.Second}
	if runFile != "" {
		if err := writeRunFile(runFile, rpcwire.RunFile{URL: l.URL, Token: l.token, PID: os.Getpid(), Impl: "go"}); err != nil {
			cancel()
			ln.Close()
			return nil, err
		}
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.srv.Serve(ln)
	}()
	return l, nil
}

// Token 只给测试和同进程的客户端；不要写日志
func (l *Listener) Token() string { return l.token }

func (l *Listener) handler(ctx context.Context, s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		// 浏览器发 WebSocket 一定带 Origin，本机程序不带：带了一律拒，网页就连不进来
		if _, ok := r.Header["Origin"]; ok {
			http.Error(w, "不接受浏览器连接", http.StatusForbidden)
			return
		}
		if !checkToken(r.Header.Get("Authorization"), l.token) {
			http.Error(w, "令牌不对", http.StatusUnauthorized)
			return
		}
		if !isLoopback(r.RemoteAddr) {
			http.Error(w, "只接受本机连接", http.StatusForbidden)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			return
		}
		c.SetReadLimit(rpcwire.MaxRequestBytes)
		s.Serve(ctx, &wsTransport{c: c})
	})
}

func checkToken(header, want string) bool {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Close 停止接新连接、断开已有连接，删掉 run 文件（只删还是自己写的那份：里面的令牌对得上才删）
func (l *Listener) Close() error {
	l.once.Do(func() {
		l.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		l.srv.Shutdown(ctx)
		l.srv.Close()
		l.wg.Wait()
		if l.runFile != "" {
			if b, err := os.ReadFile(l.runFile); err == nil {
				var rf rpcwire.RunFile
				if json.Unmarshal(b, &rf) == nil && rf.Token == l.token {
					os.Remove(l.runFile)
				}
			}
		}
	})
	return nil
}

func writeRunFile(path string, rf rpcwire.RunFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rf)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// processAlive：Windows 上 FindProcess 会去开进程句柄，进程不在就失败；别的系统 FindProcess 总成功，只当活着
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

type wsTransport struct{ c *websocket.Conn }

func (t *wsTransport) Read(ctx context.Context) ([]byte, error) {
	typ, b, err := t.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errors.New("只收文本帧")
	}
	return b, nil
}

func (t *wsTransport) Write(ctx context.Context, b []byte) error {
	return t.c.Write(ctx, websocket.MessageText, b)
}

func (t *wsTransport) Close() error { return t.c.Close(websocket.StatusNormalClosure, "") }

// Pipe 是进程内的一对传输：一头给 Server.Serve，一头给客户端。用来测试和量"经过协议编码但不走网络"的开销
func Pipe() (server, client Transport) {
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	done := make(chan struct{})
	var once sync.Once
	closeFn := func() error { once.Do(func() { close(done) }); return nil }
	return &chanTransport{in: a, out: b, done: done, close: closeFn}, &chanTransport{in: b, out: a, done: done, close: closeFn}
}

type chanTransport struct {
	in, out chan []byte
	done    chan struct{}
	close   func() error
}

func (t *chanTransport) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-t.in:
		return b, nil
	case <-t.done:
		return nil, errors.New("管道已关")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *chanTransport) Write(ctx context.Context, b []byte) error {
	select {
	case t.out <- b:
		return nil
	case <-t.done:
		return errors.New("管道已关")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *chanTransport) Close() error { return t.close() }
