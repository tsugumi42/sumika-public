package ctlserver

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// fakeListener 按剧本回 Accept 的结果：先是几个错误，再是一个真的连接（net.Pipe 的一端），之后阻塞到 Close
type fakeListener struct {
	mu     sync.Mutex
	errs   []error
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	calls  int
}

func newFakeListener(errs ...error) *fakeListener {
	return &fakeListener{errs: errs, conns: make(chan net.Conn, 1), closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	if len(l.errs) > 0 {
		e := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, e
	}
	l.mu.Unlock()
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *fakeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *fakeListener) Addr() net.Addr { return pipeAddr{} }

func (l *fakeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "fake" }
func (pipeAddr) String() string  { return "fake" }

// 偶发的 Accept 错误（比如建下一个管道实例时资源不足）不能让控制接口从此没人接：退避后接着收，后面的请求照常服务
func TestServeSurvivesTransientAcceptErrors(t *testing.T) {
	ln := newFakeListener(errors.New("资源不足"), errors.New("资源不足"))
	f := &fakeBackend{}
	srv := New(f, Options{AcceptBackoff: time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	c, s := net.Pipe()
	ln.conns <- s
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := ctlproto.WriteJSON(c, ctlproto.Request{V: ctlproto.Version, Cmd: "window", Args: []string{"show"}}); err != nil {
		t.Fatal(err)
	}
	line, err := ctlproto.ReadLine(bufio.NewReader(c), 1<<20)
	if err != nil {
		t.Fatalf("两次 Accept 错误以后应该还在服务：%v", err)
	}
	var r ctlproto.Reply
	if json.Unmarshal(line, &r) != nil || !r.OK {
		t.Fatalf("回复：%s", line)
	}
	wantCalls(t, f, "window-show")
	c.Close()

	srv.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close 以后 Serve 应返回 nil：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 以后 Serve 没有返回")
	}
}

// 一直收不到（连着出错超过上限）：放弃，把监听器关掉再返回错误；不无限重试
func TestServeGivesUpAndClosesTheListener(t *testing.T) {
	var errs []error
	for i := 0; i < maxAcceptFailures+5; i++ {
		errs = append(errs, errors.New("一直坏"))
	}
	ln := newFakeListener(errs...)
	srv := New(&fakeBackend{}, Options{AcceptBackoff: time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("连着出错应该返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没有放弃")
	}
	if !ln.isClosed() {
		t.Error("放弃时应把监听器关掉")
	}
	if ln.calls != maxAcceptFailures+1 {
		t.Errorf("应尝试 %d 次，实际 %d", maxAcceptFailures+1, ln.calls)
	}
}

// 退避等待的时候 Close：马上返回，不等完整个退避
func TestCloseInterruptsAcceptBackoff(t *testing.T) {
	ln := newFakeListener(errors.New("坏一次"))
	srv := New(&fakeBackend{}, Options{AcceptBackoff: time.Hour})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	for { // 等它进了退避：Accept 被调过一次
		ln.mu.Lock()
		n := ln.calls
		ln.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	srv.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close 打断退避应返回 nil：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 没有打断退避")
	}
}
