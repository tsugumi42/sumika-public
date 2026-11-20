package linkcard

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// freshCanvas 丢掉共用画布里的 Tester，下一张卡等于在一块全新的画布上画（旧的那个收不回来，只在测试里这样用）。
func freshCanvas() {
	offscreen.mu.Lock()
	offscreen.tt = nil
	offscreen.mu.Unlock()
}

// 复用画布不能串内容：先画一张大的深色推特卡片，再画一张小的浅色卡，后者要和单独画时逐字节一致（PNG 逐像素）。
// 通用卡片、通用卡片和推特卡片交替也一样。
func TestCanvasReuseKeepsNothingFromPreviousCard(t *testing.T) {
	var theme atomic.Value
	theme.Store(ThemeDark)
	r := testRenderer(t, Options{Theme: func() string { return theme.Load().(string) }})
	ctx := context.Background()

	bigTweet := func() Tweet {
		tw := sampleTweet()
		tw.Text = strings.Repeat("深色的长推文，一行一行排下去。", 12)
		tw.Media = []TweetMedia{{Image: swatch(400, 300, sky, leaf)}, {Image: swatch(300, 400, peach, plum), Video: true}}
		tw.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯", Account: "paper_lamp", Verification: "business", Avatar: swatch(40, 40, leaf, sky)}, Text: "被引用的那条。"}
		tw.Poll = &TweetPoll{Choices: []PollChoice{{Label: "甲", Percent: 70, OK: true}, {Label: "乙", Percent: 30, OK: true}}}
		return tw
	}
	smallTweet := func() Tweet {
		tw := sampleTweet()
		tw.Text = "小卡片。"
		return tw
	}
	bigCard := Data{Title: strings.Repeat("大卡片的标题 ", 10), Author: "纸灯实验室", DateDuration: "2026-11-01 18:30｜02:15", Stats: []string{"播放 1｜点赞 2", "弹幕 3｜投币 4"}, Cover: swatch(320, 180, sky, leaf)}
	smallCard := Data{Title: "小卡片", Author: "作者"}

	tweetOut := func(th string, tw Tweet) Output {
		t.Helper()
		theme.Store(th)
		out, err := r.RenderTweet(ctx, tw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cardOut := func(d Data) Output {
		t.Helper()
		out, err := r.Render(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	same := func(name string, got, alone Output) {
		t.Helper()
		if got.Width != alone.Width || got.Height != alone.Height {
			t.Fatalf("%s：尺寸 %dx%d，单独画是 %dx%d", name, got.Width, got.Height, alone.Width, alone.Height)
		}
		if !bytes.Equal(got.Bytes, alone.Bytes) {
			t.Fatalf("%s：和单独画不是逐像素一致（%d 字节对 %d 字节）", name, len(got.Bytes), len(alone.Bytes))
		}
	}

	// 每块新画布的第一张当作“单独画”的样子，后面在同一块画布上接着画别的，再回来画同一张，要逐字节一致。
	// 新画布丢不掉旧 Tester（mygo 没有释放接口），所以只开三块。

	// 大的深色推特卡片之后画小的浅色推特卡片
	freshCanvas()
	alone := tweetOut(ThemeLight, smallTweet())
	big := tweetOut(ThemeDark, bigTweet())
	if big.Height <= alone.Height || big.Theme != ThemeDark || alone.Theme != ThemeLight {
		t.Fatalf("前提不对：大卡 %d 高 %s，小卡 %d 高 %s", big.Height, big.Theme, alone.Height, alone.Theme)
	}
	same("深色大推特卡之后的浅色小推特卡", tweetOut(ThemeLight, smallTweet()), alone)

	// 反过来：小的浅色卡之后画大的深色卡
	freshCanvas()
	bigAlone := tweetOut(ThemeDark, bigTweet())
	tweetOut(ThemeLight, smallTweet())
	same("浅色小推特卡之后的深色大推特卡", tweetOut(ThemeDark, bigTweet()), bigAlone)

	// 通用卡片：有封面的大卡之后画没封面的小卡；再和推特卡片交替
	freshCanvas()
	smallAlone := cardOut(smallCard)
	cardOut(bigCard)
	same("大通用卡之后的小通用卡", cardOut(smallCard), smallAlone)
	tweetOut(ThemeDark, bigTweet())
	same("深色大推特卡之后的通用卡", cardOut(smallCard), smallAlone)
	same("通用卡之后的浅色小推特卡", tweetOut(ThemeLight, smallTweet()), alone)
}
