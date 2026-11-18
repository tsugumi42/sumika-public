//go:build !windows

package watchdog

import (
	"os"
	"testing"
)

// 1 号进程一定在；普通用户给它发信号得到 EPERM，这时要算活着（和 rpcwire.ProcessAlive 一致：宁可不重启，也不起第二个后台）。
// 以 root 跑时发信号直接成功，同样是活着。
func TestProcessAliveEPERMMeansAlive(t *testing.T) {
	if !ProcessAlive(1) {
		t.Fatal("1 号进程在，哪怕没有权限给它发信号也应当算活着")
	}
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("自己的进程应当算活着")
	}
	if ProcessAlive(0) || ProcessAlive(-3) {
		t.Fatal("0 和负数不是进程号")
	}
	if ProcessAlive(999999999) {
		t.Fatal("不存在的进程号应当算不在")
	}
}
