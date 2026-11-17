package ctlproto

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// 客户端连上管道以后核对另一头：得是当前用户的进程。管道名里的 SID 不是秘密，同一台电脑上别的账号能抢先建一个同名的管道；
// 不核对的话，客户端会把命令交给它、相信它回的 ok:true。
//
// 形状表（行 = 管道另一头的进程，列 = 入口）：
//
//	当前用户自己的进程          Dial 通（下面的 TestDialAcceptsOurOwnServer）、checkServerPID 通
//	别的用户的进程（查得到 SID） checkServerPID 拒（TestCheckServerPIDShapes）、Dial 拒（TestDialRefusesForeignServer）
//	查不到 SID 的进程（别的账号的进程通常打不开、进程已经没了）  checkServerPID 拒、Dial 拒——宁可拒绝
//	取不到对方的进程号 / 连接没有句柄  verifyServer 拒
//	查不到自己是谁              verifyServer 拒（CurrentSID 失败时，不好造，靠代码走查）

func listenAsMe(t *testing.T) string {
	t.Helper()
	sid, err := CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	name := testPipeName("verify-" + t.Name())
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { // 接进来的连接放着不用，Dial 自己只管连上和核对
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return name
}

func dialWithin(name string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return Dial(ctx, name)
}

func TestDialAcceptsOurOwnServer(t *testing.T) {
	conn, err := dialWithin(listenAsMe(t))
	if err != nil {
		t.Fatalf("同一个用户的服务端应该连得上：%v", err)
	}
	conn.Close()
}

func TestDialRefusesForeignServer(t *testing.T) {
	name := listenAsMe(t)
	defer func(f func(net.Conn) (uint32, error), g func(uint32) (string, error)) { serverPIDOf, userSIDOf = f, g }(serverPIDOf, userSIDOf)

	// 别的用户的进程（SYSTEM），查得到它的 SID
	serverPIDOf = func(net.Conn) (uint32, error) { return 4, nil }
	userSIDOf = func(uint32) (string, error) { return "S-1-5-18", nil }
	conn, err := dialWithin(name)
	var de *DeniedError
	if err == nil || !errors.As(err, &de) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("别的用户的服务端应被拒（*DeniedError），得到 %v", err)
	}

	// 查不到对方是谁：也拒
	userSIDOf = func(uint32) (string, error) { return "", errors.New("拒绝访问") }
	if conn, err = dialWithin(name); err == nil || !errors.As(err, &de) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("查不到对方用户应被拒，得到 %v", err)
	}

	// 取不到对方的进程号：也拒
	serverPIDOf = func(net.Conn) (uint32, error) { return 0, errors.New("取不到") }
	if conn, err = dialWithin(name); err == nil || !errors.As(err, &de) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("取不到对方进程号应被拒，得到 %v", err)
	}
}

func TestCheckServerPIDShapes(t *testing.T) {
	me, err := CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkServerPID(uint32(os.Getpid()), me); err != nil {
		t.Errorf("自己的进程应通过：%v", err)
	}
	var de *DeniedError
	for _, c := range []struct {
		what string
		pid  uint32
	}{
		{"System 进程（4）：打不开", 4},
		{"不存在的进程号", 0xfffffff0},
		{"0", 0},
	} {
		if err := checkServerPID(c.pid, me); !errors.As(err, &de) {
			t.Errorf("%s：应被拒，得到 %v", c.what, err)
		}
	}
	// 查得到、但不是我：拒
	defer func(g func(uint32) (string, error)) { userSIDOf = g }(userSIDOf)
	userSIDOf = func(uint32) (string, error) { return me + "-1", nil }
	if err := checkServerPID(uint32(os.Getpid()), me); !errors.As(err, &de) {
		t.Errorf("SID 对不上应被拒：%v", err)
	}
}

// 连接没有句柄（go-winio 以后换了实现）：取不到进程号，Dial 拒绝，而不是跳过核对
func TestServerPIDNeedsAHandle(t *testing.T) {
	if _, err := serverPIDOf(struct{ net.Conn }{}); err == nil {
		t.Fatal("没有句柄的连接应报错")
	}
}
