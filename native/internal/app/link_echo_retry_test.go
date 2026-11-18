package app

import (
	"strings"
	"testing"
)

// ---- 跨计划的回声归属（复核问题 1）：原段等过回声、放弃了（库里仍是结果未知），转图重发的那一段其实到了 ----
//
// 重发段的回声只认重发段：不能按“先发先认”被已经放弃的原段抢走（抢走后重发段等不到回声，被当成被拒、再补一份文字版）

func retryLandsRule(f sentFrame) string {
	switch {
	case len(f.Images) == 0:
		return replyOK
	case !f.Images[0].LeftBlue:
		return replyUnconfirmed // 原图：没确认、也没进群
	}
	return replyUnconfirmedLanded // 转图：没确认，但进群了、回声推回来
}

// 直接发、回声里图的标识读不出（NT 的 fileId）：只能凭形状比，原段和重发段形状一样
func TestRetryEchoNotStolenByGaveUpPartOpaque(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), retryLandsRule)
	peer.echoes, peer.echoOpaque = true, true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "981", "https://x.com/paper/status/72081")
	waitImages(t, peer, 2)
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 2 || strings.Contains(peer.text(), "没发出去") {
		t.Fatalf("转图其实到了，不该再补文字版：%d 帧带图、%+v", got, peer.sent())
	}
}

// 合并转发：回声里只有转发的 id，不比内容
func TestRetryEchoNotStolenByGaveUpPartForward(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, pics("w2.png", "w3.png"), retryLandsRule, longPostForward)
	peer.echoes = true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "982", "https://x.com/paper/status/72082")
	waitImages(t, peer, 2)
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 2 || strings.Contains(peer.text(), "没发出去") {
		t.Fatalf("转图的转发其实到了，不该再补文字版：%d 帧带图、%+v", got, peer.sent())
	}
}

// 对照：图标识读得出时 md5 就分得开
func TestRetryEchoReadableIDsClaimRetry(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), retryLandsRule)
	peer.echoes = true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "983", "https://x.com/paper/status/72083")
	waitImages(t, peer, 2)
	peer.settle(t)
	if strings.Contains(peer.text(), "没发出去") {
		t.Fatalf("对照组不该补文字版：%+v", peer.sent())
	}
}
