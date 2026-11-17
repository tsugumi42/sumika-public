package linkplugin

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// 日志里要有各路耗时，备用线路也超时时不会只看到最后一路。
func TestReasonIncludesRouteTrail(t *testing.T) {
	got := reason(&linkreader.FetchError{Kind: "timeout", Source: "vx", Trail: "fx_v2 timeout 10.0秒 → vx timeout 10.0秒"})
	if !strings.Contains(got, "来源 vx") || !strings.Contains(got, "fx_v2 timeout 10.0秒 → vx timeout 10.0秒") {
		t.Fatal(got)
	}
}
