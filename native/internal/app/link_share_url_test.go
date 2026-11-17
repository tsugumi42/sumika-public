package app

import (
	"strings"
	"testing"
)

// #30 端到端：用户发没账号的 /i/status/ 链接，群里收到的链接按取到的作者账号写；内容只发一次，不走补发。
// 回声和计划簿按计划号认，trace 里的 URL 仍是干净链接（键），所以链接改写不影响送达核对。
func TestLinkTweetShareURLEndToEnd(t *testing.T) {
	t.Parallel()
	e, a, peer := newRejectHostWith(t, pics())
	sendLink(t, e, a, "9141", "https://x.com/i/web/status/77901")
	waitFor(t, "展开发出", func() bool { return linkSawFrames(peer, "https://x.com/fake_author/status/77901") })
	all := strings.Join(peer.snapshot(), "\n")
	if strings.Contains(all, "x.com/i/status/77901") || strings.Contains(all, "x.com/i/web/status/77901") {
		t.Fatalf("发出去的还是没账号的链接：%s", all)
	}
	if n := strings.Count(all, "编的推文正文"); n != 1 {
		t.Fatalf("正文发了 %d 次：%s", n, all)
	}
}
