package linkexpand

import (
	"os"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// testNow 是包内测试用的“现在”：时间写法今年不带年份、往年带年份（stamp），钉住它，过了元旦基线和逐字对照也不变。
var testNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	nowFunc = func() time.Time { return testNow }
	linkreader.MallNow = nowFunc
	os.Exit(m.Run())
}
