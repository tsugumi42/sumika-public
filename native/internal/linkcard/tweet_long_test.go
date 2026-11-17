package linkcard

import (
	"context"
	"strings"
	"testing"
)

func longTweet() Tweet {
	tw := sampleTweet()
	var paras []string
	for i := 0; i < 14; i++ {
		paras = append(paras, strings.Repeat("这是一段比较长的正文，用来把卡片撑高，中英混排 mixed text，也要排得下。", 3))
	}
	tw.Text = strings.Join(paras, "\n\n")
	tw.Media = []TweetMedia{{Image: swatch(300, 200, sky, leaf)}, {Image: swatch(300, 200, peach, plum)}}
	tw.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯", Account: "paper_lamp"}, Text: strings.Repeat("引用的正文也很长。", 20)}
	return tw
}

// 封顶值（图上像素）：设 960 按 960 封顶并截断，设 0（默认）不封顶，长推文整张画出来，正文不截断、不写“完整内容见原文”。
func TestTweetCardMaxHeightSetting(t *testing.T) {
	tw := longTweet()
	capped := testRenderer(t, Options{MaxHeight: func() int { return 960 }})
	out, err := capped.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Height > 960 || !strings.Contains(fieldOf(t, out, "body").Text, "完整内容见原文") {
		t.Fatalf("封顶 960 应截断：高 %d", out.Height)
	}

	h1500 := testRenderer(t, Options{MaxHeight: func() int { return 1500 }})
	out1500, err := h1500.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if out1500.Height <= out.Height || out1500.Height > 1500 {
		t.Fatalf("封顶 1500：高 %d（默认 %d）", out1500.Height, out.Height)
	}

	free := testRenderer(t, Options{MaxHeight: func() int { return 0 }})
	if !free.Uncapped() || capped.Uncapped() {
		t.Fatalf("Uncapped 判断不对")
	}
	outFree, err := free.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	body := fieldOf(t, outFree, "body")
	if outFree.Height <= 1500 || outFree.Height > TweetHardMaxHeight {
		t.Fatalf("不封顶应整张画出来：高 %d", outFree.Height)
	}
	if strings.Contains(body.Text, "完整内容见原文") || strings.Contains(body.Text, "……") || !strings.HasSuffix(body.Text, "也要排得下。") {
		t.Fatalf("不封顶不该截断：…%q", body.Text[max(0, len(body.Text)-40):])
	}
	if got := strings.Count(body.Text, "这是一段"); got != 14*3 {
		t.Fatalf("14 段正文该全在：%d", got)
	}
	// 引用框、时间、互动排都在，且在正文和图的下面
	for _, name := range []string{"quote", "time", "action-reply", "action-like"} {
		if f := fieldOf(t, outFree, name); f.Y < body.Y+body.H {
			t.Fatalf("%s 该在正文下面：%+v 正文 %+v", name, f, body)
		}
	}
}

// 段落之间留半行（12 逻辑像素），不是一整行空行（24）：同样 14 段，不封顶比“空一整行”的排法矮。
func TestTweetCardLongParagraphGap(t *testing.T) {
	r := testRenderer(t, Options{MaxHeight: func() int { return 0 }})
	one := sampleTweet()
	one.Text = "第一段。\n\n第二段。"
	out, err := r.RenderTweet(context.Background(), one)
	if err != nil {
		t.Fatal(err)
	}
	m := r.fonts.measurer()
	_, lineH, _ := m.block("国", bodySize, bodyLine, 400, 400, 0)
	body := fieldOf(t, out, "body")
	// 两段各一行，中间只有半行的空隙：总高 = 两行 + 12 逻辑像素（按 720/550 放大），而不是三行
	want := int((2*lineH + paraGap) * tweetScale)
	if d := body.H - want; d < -2 || d > 2 {
		t.Fatalf("正文高 %d，要 %d（两行加半行间距）", body.H, want)
	}
}

// 长文里的小标题、配图、分隔线按顺序夹在段落中间，图在对应段落后面。
func TestTweetCardLongFlowKeepsOrder(t *testing.T) {
	r := testRenderer(t, Options{MaxHeight: func() int { return 0 }})
	tw := sampleTweet()
	tw.Text = "https://x.com/i/article/1"
	tw.Flow = []FlowBlock{
		{Kind: FlowHeading, Text: "文章标题"},
		{Kind: FlowImage, Media: TweetMedia{Image: swatch(600, 300, sky, plum), Ratio: 0.5}},
		{Kind: FlowPara, Text: "第一段正文。\n\n第二段正文。"},
		{Kind: FlowImage, Media: TweetMedia{Image: swatch(400, 400, peach, leaf), Ratio: 1}},
		{Kind: FlowRule},
		{Kind: FlowPara, Text: "结尾一段。"},
	}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	var ys []int
	for _, f := range out.Layout {
		if strings.HasPrefix(f.Name, "flow-") {
			ys = append(ys, f.Y)
		}
	}
	if len(ys) != 2 { // 两张图（分隔线不记字段）
		t.Fatalf("flow 块 %d 个：%+v", len(ys), out.Layout)
	}
	for i := 1; i < len(ys); i++ {
		if ys[i] <= ys[i-1] {
			t.Fatalf("图和线该从上到下：%v", ys)
		}
	}
	body := fieldOf(t, out, "body")
	if !strings.Contains(body.Text, "文章标题") || !strings.Contains(body.Text, "第二段正文。") || !strings.Contains(body.Text, "结尾一段。") {
		t.Fatalf("文字块都该在：%q", body.Text)
	}
	if fieldOf(t, out, "time").Y < ys[1] {
		t.Fatalf("时间该在内容下面")
	}
	// 封顶时不用 Flow：和以前一样，正文是 Text
	capped := testRenderer(t, Options{})
	out, err = capped.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldOf(t, out, "body").Text; got != tw.Text {
		t.Fatalf("封顶时正文该是 Text：%q", got)
	}
}

// 不封顶也有硬上限：长到超过 TweetHardMaxHeight 就按硬上限收（正文截在标点后）。
func TestTweetCardUncappedHasHardLimit(t *testing.T) {
	r := testRenderer(t, Options{MaxHeight: func() int { return 0 }})
	tw := sampleTweet()
	tw.Text = strings.Repeat("很长很长的一段正文，中英混排，mixed text，也要排得下。\n\n", 400)
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Height > TweetHardMaxHeight || !strings.HasSuffix(fieldOf(t, out, "body").Text, "……\n完整内容见原文") {
		t.Fatalf("超过硬上限该截断：高 %d", out.Height)
	}
}

func TestTweetCapClamp(t *testing.T) {
	for in, want := range map[int]int{-1: DefaultTweetMaxHeight, 0: 0, 100: TweetMinMaxHeight, 960: 960, 5000: 5000, 9000: TweetHardMaxHeight} {
		r := NewWith(Options{MaxHeight: func() int { return in }})
		if got := r.tweetCap(); got != want {
			t.Errorf("封顶 %d → %d，要 %d", in, got, want)
		}
		r.Close()
	}
	r := New()
	defer r.Close()
	if r.tweetCap() != DefaultTweetMaxHeight || DefaultTweetMaxHeight != 0 || !r.Uncapped() {
		t.Errorf("没设置当默认值，默认不封顶（用户 2026-11-13）：%d", r.tweetCap())
	}
}

// #39：投票画在卡片上：每个选项一条横条，领先的加粗，条下一行小字；字过文字守卫（Semantic）；有投票比没有投票高。
func TestTweetCardPoll(t *testing.T) {
	r := testRenderer(t, Options{})
	base := sampleTweet()
	out0, err := r.RenderTweet(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	tw := base
	tw.Poll = &TweetPoll{Choices: []PollChoice{{Label: "甲", Percent: 60, OK: true}, {Label: "乙", Percent: 40, OK: true}, {Label: "丙"}}, Footer: "共 100 票 · 已结束"}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	poll := fieldOf(t, out, "poll")
	if poll.Text != "3" || out.Height <= out0.Height {
		t.Fatalf("投票块：%+v 高 %d（无投票 %d）", poll, out.Height, out0.Height)
	}
	if body := fieldOf(t, out, "body"); poll.Y < body.Y+body.H {
		t.Fatalf("投票该在正文下面：%+v %+v", poll, body)
	}
	if tm := fieldOf(t, out, "time"); tm.Y < poll.Y+poll.H {
		t.Fatalf("时间该在投票下面")
	}
	sem := tw.Semantic()
	for _, want := range []string{"甲", "乙", "丙", "共 100 票 · 已结束"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("投票里的 %q 没进 Semantic：%q", want, sem)
		}
	}
	if fmtPercent(60) != "60%" || fmtPercent(33.3) != "33.3%" {
		t.Fatalf("百分比写法")
	}
}
