package rpcwire

import (
	"os"
	"os/exec"
	"testing"
)

// 后台进程是否还在：自己在、正常退出并收掉的子进程不在、不存在的号不在、非正数不在。
// run 文件里记着死进程时，rpcclient 靠它不去连、rpcserve 靠它不把残留文件当成别的后台
func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("自己的进程应当算活着")
	}
	if ProcessAlive(0) || ProcessAlive(-5) {
		t.Fatal("0 和负数不是进程号，应当算不在")
	}
	if ProcessAlive(999999999) {
		t.Fatal("不存在的进程号应当算不在")
	}
	// 起一个马上退出的子进程（同一个测试二进制，不跑任何用例），等它结束被回收，它的号就是一个刚死的进程
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("起子进程：%v", err)
	}
	if ProcessAlive(cmd.ProcessState.Pid()) {
		t.Fatalf("已退出并回收的子进程（号 %d）应当算不在", cmd.ProcessState.Pid())
	}
}

// 进程退出以后还有人握着它的句柄（父进程、杀毒软件）：Windows 上进程对象还在，只“能不能打开”会一直说活着（CI 的 Windows 机器上出过）。
// 这里在子进程退出前先拿一个句柄并一直握到断言完，ProcessAlive 仍要说不在。Unix 上 FindProcess 不握任何东西，等于再测一遍已退出的情形。
func TestProcessAliveWhileSomeoneHoldsHandleOfExited(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("起子进程：%v", err)
	}
	pid := cmd.Process.Pid
	held, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("握句柄：%v", err)
	}
	defer held.Release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("等子进程：%v", err)
	}
	if ProcessAlive(pid) {
		t.Fatalf("已退出的子进程（号 %d）还有人握着句柄，也应当算不在", pid)
	}
}
