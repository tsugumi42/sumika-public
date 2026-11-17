package linkplugin

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// #30：详情里的“来源：”也按取到的作者账号写推文链接，不照抄 /i/status/。
func TestRenderSourceUsesShareURL(t *testing.T) {
	r := linkreader.Result{
		Platform: "x", Kind: "post", ID: "1900000000000000001", CleanURL: "https://x.com/i/status/1900000000000000001", FetchedAt: time.Unix(1, 0),
		Author: linkreader.Author{Account: linkreader.Field[string]{State: "available", Value: "paper_cloud"}},
		Stats:  map[string]linkreader.Field[int64]{}, Flags: map[string]linkreader.Field[any]{},
	}
	if got := Render(r, 0); !strings.Contains(got, "来源：https://x.com/paper_cloud/status/1900000000000000001") {
		t.Fatalf("\n%s", got)
	}
}
