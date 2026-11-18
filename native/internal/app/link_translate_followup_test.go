package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// 2026-11-14 链接翻译的后续（规格-链接发送改版 B14）：被引推文也翻、投票选项也只写译文，端到端经 QQ 群、QQ 私聊发出去。

const (
	mainJa  = "冬コミ受かりました！スペースは東1ホールです"
	quoteJa = "こちらが参加サークルの一覧です。よろしくお願いします"
)

// quoteFetcher 回一条带被引推文的帖子，投票另算（有 poll 时加一个两选项的投票）。
type quoteFetcher struct {
	textFetcher
	quote string
	poll  []string
}

func (f quoteFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	r, err := f.textFetcher.Fetch(ctx, l)
	avail := func(s string) linkreader.Field[string] { return linkreader.Field[string]{State: "available", Value: s} }
	r.Blocks = []linkreader.Block{{Type: "text", Text: f.text}}
	if f.quote != "" {
		q := linkreader.Result{Platform: "x", Kind: "post", ID: "9", CleanURL: "https://x.com/quoted_one/status/9", Source: "fake", FetchedAt: time.Now().UTC(),
			Stats: map[string]linkreader.Field[int64]{}, Flags: map[string]linkreader.Field[any]{}}
		q.Author.Name, q.Author.Account = avail("被引的人"), avail("quoted_one")
		q.Text = avail(f.quote)
		q.Blocks = []linkreader.Block{{Type: "text", Text: f.quote}}
		q.Media = linkreader.Field[[]linkreader.Media]{State: "not_returned", Reason: "not_applicable"}
		r.Quote = linkreader.Field[*linkreader.Result]{State: "available", Value: &q}
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &q})
	}
	if len(f.poll) > 0 {
		p := &linkreader.PollInfo{Total: linkreader.Field[int64]{State: "available", Value: 3}}
		for i, label := range f.poll {
			p.Choices = append(p.Choices, linkreader.PollChoice{Label: label, Count: linkreader.Field[int64]{State: "available", Value: int64(i + 1)}})
		}
		r.Poll = p
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "poll"})
	}
	return r, err
}

// 被引推文和主推文各调一次模型，两次调用同时在途（并发）：假模型等两次调用都到了才放行，串行的话两次互相等，超时就红；
// 用闸门不用睡眠。译文回来后被引推文只写译文，下面带“翻译自日语”，原文一个字不留。
func TestLinkTranslateQuoteRunsConcurrentlyAndShowsOnlyTranslation(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		var calls atomic.Int32
		gate := make(chan struct{})
		model := func(ctx context.Context, _, user string) (string, error) {
			if calls.Add(1) == 2 {
				close(gate)
			}
			select {
			case <-gate:
			case <-time.After(5 * time.Second):
				return "", context.DeadlineExceeded
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if strings.Contains(user, "参加サークル") {
				return "这是参加社团的名单，请多关照", nil
			}
			return "冬季同人展上了！摊位在东1展厅", nil
		}
		e, a, peer := newRejectHostWith(t, quoteFetcher{textFetcher: textFetcher{picsFetcher: pics(), text: mainJa}, quote: quoteJa})
		a.opt.LinkTranslateCall = model
		conn := startSend(t, e, a)
		scriptLink(t, e, a, conn, entry, "9201", "https://x.com/paper/status/78001")
		waitFor(t, "两段译文发出", func() bool {
			return linkSawFrames(peer, "这是参加社团的名单") && linkSawFrames(peer, "冬季同人展上了")
		})
		all := strings.Join(peer.snapshot(), "\n")
		if strings.Contains(all, "参加サークル") || strings.Contains(all, "冬コミ受かりました") {
			t.Fatalf("被引推文和主推文都只放译文：%s", all)
		}
		if got := strings.Count(all, "（翻译自日语）"); got < 2 {
			t.Fatalf("主推文和被引推文各有一行翻译自：%d 处\n%s", got, all)
		}
		if calls.Load() != 2 {
			t.Fatalf("主推文一次、被引推文一次：%d 次", calls.Load())
		}
	})
}

// 被引推文翻不出来（模型报错）只有被引推文发原文，主推文的译文照旧；各退各的。
func TestLinkTranslateQuoteFailureOnlyFallsBackThatPart(t *testing.T) {
	t.Parallel()
	model := func(_ context.Context, _, user string) (string, error) {
		if strings.Contains(user, "参加サークル") {
			return "", context.DeadlineExceeded
		}
		return "冬季同人展上了！摊位在东1展厅", nil
	}
	e, a, peer := newRejectHostWith(t, quoteFetcher{textFetcher: textFetcher{picsFetcher: pics(), text: mainJa}, quote: quoteJa})
	a.opt.LinkTranslateCall = model
	sendLink(t, e, a, "9202", "https://x.com/paper/status/78002")
	waitFor(t, "主推文译文和被引原文发出", func() bool {
		return linkSawFrames(peer, "冬季同人展上了") && linkSawFrames(peer, "参加サークル")
	})
	all := strings.Join(peer.snapshot(), "\n")
	if strings.Contains(all, "冬コミ受かりました") {
		t.Fatalf("主推文译文成功，不该留原文：%s", all)
	}
}

// 投票选项只写译文：英文投票帖，选项跟着正文逐行翻，投票块写译文选项加票数，原文选项不出现；
// 译文行数对不上（模型把选项并成一行）就退回原文选项，正文译文不受牵连。
func TestLinkTranslatePollChoicesOnlyTranslation(t *testing.T) {
	t.Parallel()
	const post = "Which paper design do you like better for the next print run?"
	fetch := quoteFetcher{textFetcher: textFetcher{picsFetcher: pics(), text: post}, poll: []string{"Paper boat", "Paper kite"}}

	m := said("下一批印刷你更喜欢哪种纸张设计？\n纸船\n纸鸢")
	e, a, peer := newRejectHostWith(t, fetch)
	a.opt.LinkTranslateCall = m.call
	sendLink(t, e, a, "9203", "https://x.com/paper/status/78003")
	waitFor(t, "译文发出", func() bool { return linkSawFrames(peer, "纸鸢") })
	all := strings.Join(peer.snapshot(), "\n")
	if strings.Contains(all, "Paper boat") || strings.Contains(all, "Paper kite") || strings.Contains(all, "Which paper design") {
		t.Fatalf("选项和正文都只放译文：%s", all)
	}
	if !strings.Contains(m.user, "Paper boat\nPaper kite") || !strings.Contains(m.system, "exactly as many lines") {
		t.Fatalf("选项逐行送去，提示词要求行数不变：%q / %q", m.user, m.system)
	}

	bad := said("下一批印刷你更喜欢哪种纸张设计？纸船、纸鸢")
	e2, a2, peer2 := newRejectHostWith(t, fetch)
	a2.opt.LinkTranslateCall = bad.call
	sendLink(t, e2, a2, "9204", "https://x.com/paper/status/78004")
	waitFor(t, "行数对不上，选项退回原文", func() bool { return linkSawFrames(peer2, "Paper boat") })
	if all2 := strings.Join(peer2.snapshot(), "\n"); strings.Contains(all2, "纸鸢") {
		t.Fatalf("行数对不上不能把译文选项当选项：%s", all2)
	}
}
