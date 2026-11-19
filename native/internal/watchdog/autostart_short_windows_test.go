package watchdog

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// 注册表里存的是 8.3 短路径名、我们这边是长路径名（或反过来），指向同一个文件，也算开着。
// 真去文件系统里建一个长名字的文件，让系统给出短名字；这个卷没开 8.3 就跳过
func TestAutostartOnExpandsShortPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "A Rather Long Directory Name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(dir, "sumika-test-executable.exe")
	if err := os.WriteFile(long, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, _ := windows.UTF16PtrFromString(long)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("取不到短路径名：%v", err)
	}
	short := windows.UTF16ToString(buf[:n])
	if short == long {
		t.Skip("这个卷没有 8.3 短路径名")
	}

	f := newFakeRun()
	f.vals["Sumika"] = `"` + short + `" ` + WatchFlag
	if !(Autostart{Store: f, Name: "Sumika", Exe: long}).On() {
		t.Errorf("Run 项是短路径 %q，Exe 是长路径 %q，该算开着", short, long)
	}
	f.vals["Sumika"] = `"` + long + `" ` + WatchFlag
	if !(Autostart{Store: f, Name: "Sumika", Exe: short}).On() {
		t.Errorf("Run 项是长路径，Exe 是短路径 %q，该算开着", short)
	}
	// 文件不存在时展开不了，只能按字面认：不同的名字不会被误认成同一个
	f.vals["Sumika"] = `"` + filepath.Join(dir, "nope.exe") + `" ` + WatchFlag
	if (Autostart{Store: f, Name: "Sumika", Exe: long}).On() {
		t.Error("不同的文件不该算开着")
	}
}
