package turn

import (
	"os"
	"testing"

	"sumika-native/internal/lineend"
)

func TestMain(m *testing.M) {
	installMemTransport()
	// 回合对拍的基线是 Bun 原样写文件；换行跟系统会让同一份对拍在 Windows 和 Linux 上不一样，
	// 这里固定成 LF。换行本身的规则在 lineend、tools 的测试里
	lineend.SetDefault("lf")
	code := m.Run()
	if factoryDir != "" {
		os.RemoveAll(factoryDir)
	}
	os.Exit(code)
}
