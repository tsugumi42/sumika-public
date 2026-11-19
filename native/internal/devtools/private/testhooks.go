package private

import "testing"

// testHooksAllowed：SUMIKA_SCAN_DATA、SUMIKA_SCAN_MAIBOT 这两个能把名单改指到别处的环境变量只在测试构建里认——go test，或者 go build -tags scantest。
// 生产构建里设了它们，Run 拒绝扫描（红）。做成变量，测试里能把它改成 false 来试“生产构建”。
var testHooksAllowed = func() bool { return testing.Testing() || scanTagTest }
