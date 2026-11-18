package app

// 链接插件关着时，渠道入库的链接不排队（规格-插件两层 第八节第一步；审查项：渠道入口漏了开关，本地路径有）。
// 用 expand 开着的白名单群，一条带 X 链接的群消息入库：插件关着时，链接插件的在途表里一条都没有；
// 插件开着时同样的消息会排队并展开（对照组，证明这条路径确实会走到插件）。
// 放回旧代码（流水线直接挂 a.links.OnStored）就红：关着也排了队。

import (
	"testing"
)

func TestLinkIngestSkipsWhenPluginOff(t *testing.T) {
	t.Parallel()
	conv := "qq:group:" + testGroup
	link := "https://x.com/paper/status/76001"

	t.Run("on", func(t *testing.T) {
		t.Parallel()
		e, a, wire, _ := newExpandHost(t, "off", nil)
		conn := startSend(t, e, a)
		e.peer.send(t, conn, expandFrame(e, "901", testOwner, link))
		waitFor(t, "插件开着：群里收到展开的回应", func() bool { return len(wire.snapshot()) > 0 })
	})

	t.Run("off", func(t *testing.T) {
		t.Parallel()
		e, a, _, _ := newExpandHost(t, "off", nil, "plugins:\n  link-reader:\n    enabled: false\n")
		conn := startSend(t, e, a)
		e.peer.send(t, conn, expandFrame(e, "902", testOwner, link))
		expandStored(t, a, conv, "902")
		if n := len(a.links.Active()); n != 0 {
			t.Fatalf("链接插件关着，入库的链接不该排队，实际在途 %d 个", n)
		}
	})
}
