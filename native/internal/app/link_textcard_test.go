package app

import (
	"strings"
	"testing"
)

// 纯文字推文画推特卡片（ADR-399）端到端：开关开着时群里收到一条带一张图（卡片）和干净链接的消息，正文不另发文字版。
func TestLinkTextOnlyTweetSendsCard(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHostRotate(t, pics(), true, func(sentFrame) string { return replyOK }, "    tweetCard: {textOnly: true}\n")
	sendLink(t, e, a, "9151", "https://x.com/fake_author/status/77901")
	waitFor(t, "卡片发出", func() bool { return len(peer.imageFrames()) > 0 })
	frames := peer.sent()
	if len(frames) != 1 || len(frames[0].Images) != 1 || frames[0].Forward {
		t.Fatalf("要一条直发消息、一张卡片：%+v", frames)
	}
	if text := strings.Join(frames[0].Texts, ""); !strings.Contains(text, "x.com/fake_author/status/77901") || strings.Contains(text, "编的推文正文") {
		t.Fatalf("文字只该有链接：%q", text)
	}
}
