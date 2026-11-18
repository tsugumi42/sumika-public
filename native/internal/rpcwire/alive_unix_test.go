//go:build unix

package rpcwire

import "testing"

// 1 号进程一定在；普通用户给它发信号会得到 EPERM，这时仍要算活着（别的用户的后台没有退出）
func TestProcessAliveOtherUsersProcess(t *testing.T) {
	if !ProcessAlive(1) {
		t.Fatal("1 号进程在，哪怕没有权限给它发信号也应当算活着")
	}
}
