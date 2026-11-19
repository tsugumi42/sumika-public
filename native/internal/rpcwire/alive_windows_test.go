//go:build windows

package rpcwire

import (
	"os"
	"os/exec"
	"testing"
)

// 4 号进程是 System，永远在，普通用户打不开它的 SYNCHRONIZE 句柄（访问被拒）：被拒要算活着，
// 不然别的用户或受保护的后台进程会被误判成不在，第二个后台就去抢数据目录。
// 本机是普通权限时走的就是“被拒绝”这一支；以管理员跑时能打开，则走“等 0 毫秒还没触发”那一支，两支结论都是活着。
func TestProcessAliveSystemProcess(t *testing.T) {
	if !ProcessAlive(4) {
		t.Fatal("System 进程（4 号）一直在，打不开也要算活着")
	}
}

// 以 259（STILL_ACTIVE）为退出码退出的进程：用退出码判会说它还活着，等进程对象能看出它已经退出了。
func TestProcessAliveExitCode259(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExit259$")
	cmd.Env = append(os.Environ(), "RPCWIRE_EXIT259=1")
	_ = cmd.Run() // 退出码 259 算错误，不管
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 259 {
		t.Fatalf("辅助进程没按 259 退出：%v", cmd.ProcessState)
	}
	if ProcessAlive(cmd.ProcessState.Pid()) {
		t.Fatalf("以 259 退出的子进程（号 %d）已经退出，不该算活着", cmd.ProcessState.Pid())
	}
}

func TestHelperExit259(t *testing.T) {
	if os.Getenv("RPCWIRE_EXIT259") == "1" {
		os.Exit(259)
	}
}
