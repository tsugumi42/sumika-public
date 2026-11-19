package linkreader

import "testing"

// 默认关掉访客标识：别的测试的假服务器不用认 finger/spi。要测它的用 visitorOn。
func init() { noVisitorCookie = true }

// visitorOn 在这个测试里打开访客标识（只影响之后新建的 Fetcher）。
func visitorOn(t *testing.T) {
	t.Helper()
	noVisitorCookie = false
	t.Cleanup(func() { noVisitorCookie = true })
}
