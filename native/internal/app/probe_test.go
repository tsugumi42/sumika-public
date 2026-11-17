package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func expectStartupBlock(t *testing.T, opt Options, code StartupReason) error {
	t.Helper()
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	var blocked *StartupError
	if !errors.As(err, &blocked) || blocked.Code != code {
		t.Fatalf("预检没有按 %s 阻断：%v", code, err)
	}
	assertNoStores(t, opt.DataDir)
	lock, lockErr := datalock.LockData(opt.DataDir)
	if lockErr != nil {
		t.Fatalf("预检失败漏放锁：%v", lockErr)
	}
	lock.Close()
	return err
}

func TestLegacyListenerBlocksBeforeStores(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			bytesRead := make(chan int, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					bytesRead <- -1
					return
				}
				defer conn.Close()
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				data, err := io.ReadAll(conn)
				if err != nil {
					bytesRead <- -1
					return
				}
				bytesRead <- len(data)
			}()
			opt := e.options()
			opt.Mode, opt.DialContext = mode, nil
			opt.ProbePorts = []int{listener.Addr().(*net.TCPAddr).Port}
			err = expectStartupBlock(t, opt, LegacyPortOccupied)
			var blocked *StartupError
			errors.As(err, &blocked)
			if blocked.Endpoint != listener.Addr().String() {
				t.Fatalf("错误端点 %s", blocked.Endpoint)
			}
			select {
			case n := <-bytesRead:
				if n != 0 {
					t.Fatalf("探测发出了 %d 字节或未关闭连接", n)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("探测未关闭连接")
			}
		})
	}
}

func TestLegacyProbeTimeoutBlocksBeforeStores(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			opt := e.options()
			opt.Mode = mode
			opt.ProbePorts = []int{9101}
			opt.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 500*time.Millisecond {
					t.Error("探测没有每端点 500ms 截止时间")
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			err := expectStartupBlock(t, opt, LegacyPortUnknown)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("超时原因丢失：%v", err)
			}
		})
	}
}

func TestLegacyOtherErrorBlocksBeforeStores(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			opt := e.options()
			opt.Mode = mode
			opt.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, syscall.EACCES }
			err := expectStartupBlock(t, opt, LegacyPortUnknown)
			if !errors.Is(err, syscall.EACCES) {
				t.Fatalf("连接错误原因丢失：%v", err)
			}
		})
	}
}

func TestLegacyRefusedAllowsStartup(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			seed, err := store.OpenGo(paths.Paths{Root: e.data}, "local", "qq")
			if err != nil {
				t.Fatal(err)
			}
			seed.Close()
			opt := e.options()
			opt.Mode = mode
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatalf("明确拒绝连接未放行：%v", err)
			}
			defer a.Close()
			if _, err := a.set.For("console"); err != nil {
				t.Fatal(err)
			}
			if (a.hub != nil) != (mode == Receive) {
				t.Fatal("放行后的模式错误")
			}
		})
	}
}

// 不注入拨号：真去连一个刚关掉的本机端口，拿系统自己给的拒绝错误（Windows 上是 10061，不是造的 ECONNREFUSED）。
func TestLegacyRealRefusedAllowsStartup(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	e := newEnv(t)
	seed, err := store.OpenGo(paths.Paths{Root: e.data}, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	opt := e.options()
	opt.Mode, opt.DialContext, opt.ProbePorts = Archive, nil, []int{port}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("真实的拒绝连接未放行：%v", err)
	}
	a.Close()
}

func TestLegacyProbeRunsUnderDataLock(t *testing.T) {
	t.Parallel()
	for _, port := range []int{7720, 9102} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			p := paths.Paths{Root: e.data}
			file, err := os.OpenFile(p.ConfigFile(), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString("\nconsole:\n  port: " + strconv.Itoa(port) + "\n")
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			var addresses []string
			opt := e.options()
			opt.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				assertNoStores(t, e.data)
				lock, err := datalock.LockData(e.data)
				if lock != nil {
					lock.Close()
				}
				if !errors.Is(err, datalock.ErrDataInUse) {
					t.Errorf("探测时未持数据锁：%v", err)
				}
				if network != "tcp" {
					t.Errorf("探测协议 %s", network)
				}
				addresses = append(addresses, address)
				return refuseLegacy(ctx, network, address)
			}
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			want := []string{"127.0.0.1:7720"}
			if port != 7720 {
				want = append(want, "127.0.0.1:"+strconv.Itoa(port))
			}
			if !slices.Equal(addresses, want) {
				t.Fatalf("探测端点 %v，预期 %v", addresses, want)
			}
		})
	}
}

func TestLegacyProbeCancelReleasesDataLock(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			seed, err := store.OpenGo(paths.Paths{Root: e.data}, "local", "qq")
			if err != nil {
				t.Fatal(err)
			}
			seed.Close()
			opt := e.options()
			opt.Mode = mode
			entered := make(chan struct{})
			opt.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				a, err := Open(ctx, opt)
				if a != nil {
					a.Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("未进入启动探测")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消错误丢失：%v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("取消探测未返回")
			}
			opt.DialContext = refuseLegacy
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatalf("取消探测后不能重开：%v", err)
			}
			a.Close()
		})
	}
}
