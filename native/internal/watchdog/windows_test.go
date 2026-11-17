//go:build windows

// 文件名 windows_test.go 不算平台后缀（要 _windows_test.go 才算），不写这行 Linux 上也会编译它、引不到注册表包。

package watchdog

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestAcquireLock(t *testing.T) {
	name := fmt.Sprintf("SumikaWatchdogTest-%d-%d", os.Getpid(), time.Now().UnixNano())
	release, ok, err := AcquireLock(name)
	if err != nil || !ok {
		t.Fatalf("第一次应当拿到：%v %v", ok, err)
	}
	if _, ok2, err := AcquireLock(name); err != nil || ok2 {
		t.Fatalf("还握着的时候第二次不该拿到：%v %v", ok2, err)
	}
	release()
	release2, ok3, err := AcquireLock(name)
	if err != nil || !ok3 {
		t.Fatalf("释放以后应当又能拿到：%v %v", ok3, err)
	}
	release2()
}

func TestProcessAliveOwnPid(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("自己应当活着")
	}
	if ProcessAlive(0) || ProcessAlive(-5) {
		t.Fatal("非法进程号不算活着")
	}
}

// 注册表的读写走一对临时子键（HKCU\Software\SumikaWatchdogTest-xxx\…），测完整棵删掉；不碰真的 Run 项
func TestRegistryRunStore(t *testing.T) {
	root := fmt.Sprintf(`Software\SumikaWatchdogTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, root+`\Run`)
		registry.DeleteKey(registry.CURRENT_USER, root+`\Approved`)
		registry.DeleteKey(registry.CURRENT_USER, root)
	})
	st := registryRunStoreAt(root+`\Run`, root+`\Approved`)
	if _, ok := st.Get("X"); ok || st.Disabled("X") {
		t.Fatal("什么都没写时应当读不到")
	}
	if err := st.Delete("X"); err != nil || st.ClearDisabled("X") != nil {
		t.Fatal("删不存在的值不算错")
	}
	if err := st.Set("X", `"C:\a b\sumika.exe" -watch`); err != nil {
		t.Fatal(err)
	}
	if v, ok := st.Get("X"); !ok || v != `"C:\a b\sumika.exe" -watch` {
		t.Fatalf("读回 %q %v", v, ok)
	}
	// 任务管理器关掉一项：StartupApproved 里写一个首字节为奇数的二进制值
	k, _, err := registry.CreateKey(registry.CURRENT_USER, root+`\Approved`, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	k.SetBinaryValue("X", []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	k.Close()
	if !st.Disabled("X") {
		t.Fatal("首字节是奇数应当读成被关掉")
	}
	// 用 Autostart 走完整流程
	a := Autostart{Store: st, Name: "X", Exe: `C:\a b\sumika.exe`}
	if a.On() {
		t.Fatal("被关掉了不该读成开着")
	}
	if err := a.Set(true); err != nil || !a.On() || st.Disabled("X") {
		t.Fatalf("Set(true) 要写入并解除禁用：on=%v disabled=%v %v", a.On(), st.Disabled("X"), err)
	}
	if err := a.Set(false); err != nil || a.On() {
		t.Fatalf("Set(false)：%v", err)
	}
	if _, ok := st.Get("X"); ok {
		t.Fatal("关掉要把值删了")
	}
	if !strings.Contains(a.command(), "-watch") {
		t.Fatal("写的是守护进程的写法")
	}
}
