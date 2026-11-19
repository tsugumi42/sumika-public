package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
)

// liveFetcher：X 帖照常回；B 站直播间 404 号取数失败，其余直播间成功；b23.tv 短链解到 505 号直播间。
type liveFetcher struct {
	pictureFetcher
	liveCalls atomic.Int32
}

func (f *liveFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	if l.Platform != "bilibili" {
		return f.pictureFetcher.Fetch(ctx, l)
	}
	f.liveCalls.Add(1)
	if l.ID == "404" {
		return linkreader.Result{}, errors.New("编的取数失败")
	}
	r, _ := f.pictureFetcher.Fetch(ctx, l)
	r.Platform, r.Kind, r.Media = "bilibili", "live", linkreader.Field[[]linkreader.Media]{State: "not_returned", Reason: "not_applicable"}
	return r, nil
}
func (f *liveFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	if l.Kind == "short" {
		out, _ := linkreader.Recognize("https://live.bilibili.com/505")
		return out, nil
	}
	return l, nil
}

// 关掉的页面类型不取数、不回失败文字：同一条消息里另一个开着的链接照常展开。
func TestDisabledKindFailureSendsNoFailureText(t *testing.T) {
	t.Parallel()
	f := &liveFetcher{}
	e, a, peer := newRejectHostWith(t, f)
	setLink(t, a, api.LinkSettingsChange{Key: config.LinkKindEnabled, Site: "bilibili", Text: "live", Bool: false})
	sendLink(t, e, a, "991", "https://live.bilibili.com/404 https://x.com/paper/status/76001")
	waitFor(t, "开着的 X 链接展开", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/76001") })
	time.Sleep(400 * time.Millisecond)
	if linkSawFrames(peer, "没解析出来") {
		t.Fatal("关掉的直播间取数失败也回了失败文字", peer.snapshot())
	}
	if f.liveCalls.Load() != 0 {
		t.Fatal("关掉的直播间不该取数", f.liveCalls.Load())
	}
}

// 私聊里短链解析后才发现类型已关：开始状态之后要有一句说明，不留一条没有下文的状态。
func TestPrivateShortLinkToDisabledKindExplains(t *testing.T) {
	t.Parallel()
	f := &liveFetcher{}
	e, a, peer := newRejectHostWith(t, f)
	setLink(t, a, api.LinkSettingsChange{Key: config.LinkKindEnabled, Site: "bilibili", Text: "live", Bool: false})
	conn := startSend(t, e, a)
	e.peer.send(t, conn, privateFrame(e.now, testAccount, "992", testOwner, "https://b23.tv/abcdef"))
	waitFor(t, "私聊说明这类页面已关", func() bool { return linkSawFrames(peer, "设置里关了") })
	if !strings.Contains(strings.Join(peer.snapshot(), "\n"), "开始解析链接") {
		t.Fatal("应当先发过开始状态", peer.snapshot())
	}
}

// 解析失败（取数失败）不发任何文字，只把开始表情换成失败表情，不能换成完成表情（用户 2026-11-10 定）。
func TestGroupFailureOnlySwapsEmojiNoText(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"404"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			f := &liveFetcher{}
			e, a, peer := newRejectHostWith(t, f)
			sendLink(t, e, a, "98"+id, "https://live.bilibili.com/"+id)
			waitFor(t, "失败表情", func() bool { return linkSawFrames(peer, `"emoji_id":"`+config.DefaultLinks().Expand.FailEmoji+`"`) })
			time.Sleep(400 * time.Millisecond)
			all := strings.Join(peer.snapshot(), "\n")
			if strings.Contains(all, "没解析出来") || len(peer.sent()) != 0 {
				t.Fatal("失败时不该发任何文字", peer.snapshot())
			}
			if strings.Contains(all, `"emoji_id":"`+config.DefaultLinks().Expand.DoneEmoji+`"`) {
				t.Fatal("失败不能贴完成表情", peer.snapshot())
			}
		})
	}
}

// 私聊协议端贴不了表情：解析失败回一句“没解析出来”，开始状态之后才有下文；群里的对照格见上一个测试（只有表情、没有文字）。
func TestPrivateFailureRepliesFixedText(t *testing.T) {
	t.Parallel()
	f := &liveFetcher{}
	e, a, peer := newRejectHostWith(t, f)
	conn := startSend(t, e, a)
	e.peer.send(t, conn, privateFrame(e.now, testAccount, "993", testOwner, "https://live.bilibili.com/404"))
	waitFor(t, "私聊失败说明", func() bool { return linkSawFrames(peer, "没解析出来") })
	all := strings.Join(peer.snapshot(), "\n")
	if !strings.Contains(all, "开始解析链接") {
		t.Fatal("应当先发过开始状态", peer.snapshot())
	}
	if strings.Contains(all, "set_msg_emoji_like") {
		t.Fatal("私聊不能调表情接口", peer.snapshot())
	}
}
