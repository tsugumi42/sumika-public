package ctlproto

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

var pipeSeq atomic.Int64

// testPipeName 给测试管道取名：带进程号和进程内计数器。只靠 time.Now 不够：Windows 上它约 0.5 毫秒分辨率，
// 两个进程同时跑同一个测试会取到同一个名字，建管道报 Access is denied。绝不连真实的 sumika-ctl-<SID>。
func testPipeName(tag string) string {
	return fmt.Sprintf(`\\.\pipe\sumika-ctl-test-%s-p%d-%d-%d`, tag, os.Getpid(), time.Now().UnixNano(), pipeSeq.Add(1))
}

// 真的权限被拒：管道的安全描述只给 SYSTEM，当前用户打开会得到 ERROR_ACCESS_DENIED。
// Dial 要把它变成 *DeniedError（客户端报 denied、退出码 3），不是原样的系统错误（客户端会当成"没在运行"）。
// 管道名带测试名和时间，不连真实的 sumika-ctl-<SID>。
func TestDialAccessDeniedIsDeniedError(t *testing.T) {
	name := testPipeName("denied")
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := Dial(ctx, name)
	if err == nil {
		conn.Close()
		t.Fatal("安全描述不给当前用户，Dial 应该失败")
	}
	var de *DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("权限被拒应是 *DeniedError，得到 %T：%v", err, err)
	}
}

// 管道不存在不是被拒：是"没在运行"（客户端 2 秒内给结论）
func TestDialMissingPipeIsNotDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Dial(ctx, `\\.\pipe\sumika-ctl-test-nobody-listens-here-2`)
	var de *DeniedError
	if err == nil || errors.As(err, &de) {
		t.Fatalf("不存在的管道应是普通错误：%v", err)
	}
}
