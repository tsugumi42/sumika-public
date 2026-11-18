package policy

import (
	"runtime"
	"testing"
)

// windowsPathsOnly 让按 Windows 路径语义写的用例在别的系统上跳过。
// 这些用例的数据是盘符、反斜杠、UNC 写法（D:\work\x、\\server\share），策略层按 Windows 路径比大小写和分隔符，
// 对拍基线也是在 Windows 上录的；Linux 上这些写法不是绝对路径，会全判成“工作目录外”，没有可比的意义。
// 与路径语义无关的用例（inside_test.go 的跨系统写法等）仍在两个系统上都跑。
func windowsPathsOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("原因：用例按 Windows 路径语义写（盘符、反斜杠、UNC），对拍基线在 Windows 录，Linux 上这些写法不是同一件事")
	}
}
