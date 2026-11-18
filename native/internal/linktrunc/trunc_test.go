package linktrunc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCutPrefersNearestPunctuation(t *testing.T) {
	// 第 30 个字落在“开”和“始”之间；往前最近的标点是“，”（在 window 内），截在它后面
	s := strings.Repeat("字", 20) + "，然后又过了一会儿，天开始下雨了。" + strings.Repeat("后", 20)
	got := Body(s, 30)
	want := strings.Repeat("字", 20) + "，然后又过了一会儿，……\n完整内容见原文"
	if got != want {
		t.Fatalf("得到 %q\n想要 %q", got, want)
	}
	if Inline(s, 30) != strings.Repeat("字", 20)+"，然后又过了一会儿，……" {
		t.Fatalf("行内不写提示: %q", Inline(s, 30))
	}
}

func TestCutWithoutNearbyPunctuationCutsAtLimit(t *testing.T) {
	// 标点离上限太远（超出 window）就在上限处截，不退成半截
	s := "。" + strings.Repeat("字", 100)
	got := Inline(s, 40)
	if utf8.RuneCountInString(got) != 40+len([]rune(Ellipsis)) {
		t.Fatalf("应在上限处截: %d 字 %q", utf8.RuneCountInString(got), got)
	}
}

func TestCutKeepsClosingQuotesAndPunctuationRun(t *testing.T) {
	s := strings.Repeat("字", 20) + "他说：“走吧！”然后" + strings.Repeat("后", 30)
	got, cut := Cut(s, 20+len([]rune("他说：“走吧！”然")))
	if !cut || !strings.HasSuffix(got, "走吧！”") {
		t.Fatalf("引号要跟着句读留下: %q", got)
	}
	got, _ = Cut(strings.Repeat("字", 20)+"真的吗？！然后"+strings.Repeat("后", 30), 20+5)
	if !strings.HasSuffix(got, "吗？！") {
		t.Fatalf("连着的句读一起留下: %q", got)
	}
}

func TestCutASCIIPunctuationNeedsSpaceOrCJK(t *testing.T) {
	// 3.5 和 example.com 里的点不是句读；“. ”才是
	s := "Version 3.5 is out at example.com now. We hope you like it a lot, really."
	got, cut := Cut(s, 42)
	if !cut || got != "Version 3.5 is out at example.com now." {
		t.Fatalf("应截在 now. 后面: %q", got)
	}
}

func TestCutLatinWordNotSplit(t *testing.T) {
	s := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda"
	got, cut := Cut(s, 24) // 第 24 个字在 epsilon 中间
	if !cut || got != "alpha beta gamma delta" {
		t.Fatalf("不切在词中间: %q", got)
	}
}

func TestCutDoesNotSplitLink(t *testing.T) {
	link := "https://example.com/article/" + strings.Repeat("q", 50)
	s := strings.Repeat("字", 95) + link
	got := Inline(s, 100)
	if strings.Contains(got, "https://") || got != strings.Repeat("字", 95)+Ellipsis {
		t.Fatalf("链接跨过截点应整条去掉: %q", got)
	}
	if Body("短文本", 100) != "短文本" {
		t.Fatal("没超长不应动")
	}
}

func TestCutTrimsTrailingSeparatorAndNoDoubleEllipsis(t *testing.T) {
	// 合集列表里“│”隔开的标题：不让省略号贴在孤零零的竖线后面
	got := Inline("第一课 │ 第二课 │ 第三课 │ 第四课", 10)
	if strings.Contains(got, "│……") {
		t.Fatalf("省略号不贴在竖线后: %q", got)
	}
	// 截在已有的省略号后面不再补第二个
	s := strings.Repeat("字", 20) + "……" + strings.Repeat("后", 30)
	if got := Inline(s, 22); strings.Count(got, "…") != 2 {
		t.Fatalf("不重复补省略号: %q", got)
	}
}

func TestCutZeroLimit(t *testing.T) {
	if got := Body("正文", 0); !strings.HasSuffix(got, Notice) {
		t.Fatalf("0 个字也要成句: %q", got)
	}
	if got := Body("正文", -1); got != "正文" {
		t.Fatalf("负数不截: %q", got)
	}
}

func TestFitBodyFindsLongestFittingPrefixAtPunctuation(t *testing.T) {
	s := strings.Repeat("字", 30) + "。" + strings.Repeat("好", 30) + "，" + strings.Repeat("坏", 60)
	// 假排版：显示文字总字数不超过 80 才排得下（含省略号和提示）
	fits := func(d string) bool { return utf8.RuneCountInString(d) <= 80 }
	got, cut := FitBody(s, fits)
	if !cut || !fits(got) || !strings.HasSuffix(got, "\n"+Notice) {
		t.Fatalf("应排得下并带提示: %d %q", utf8.RuneCountInString(got), got)
	}
	if !strings.Contains(got, "，……\n") {
		t.Fatalf("应截在最近的标点后: %q", got)
	}
	if got, cut := FitBody("短", fits); cut || got != "短" {
		t.Fatalf("排得下不动: %q", got)
	}
	if got, cut := FitBody(s, func(string) bool { return false }); !cut || got != Ellipsis+"\n"+Notice {
		t.Fatalf("什么都排不下也成句: %q", got)
	}
}

func TestDisplayFitsWidthAndCutsAtPunctuation(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
	}{
		{"短", 5},
		{"abcdefghijklmnop", 4},
		{"今天去了，看了很多风景拍了很多照片", 8},
		{strings.Repeat("长", 50), 16},
	} {
		got := Display(c.in, c.limit)
		if displayWidth(got) > c.limit*2 {
			t.Errorf("%q 截到 %d 超宽：%q", c.in, c.limit, got)
		}
		if again := Display(got, c.limit); again != got {
			t.Errorf("截两遍不等于截一遍：%q → %q", got, again)
		}
	}
	if got := Display("短", 5); got != "短" {
		t.Errorf("不超就原样：%q", got)
	}
	if got := Display("今天去了，看了很多风景拍了很多照片", 8); got != "今天去了，……" {
		t.Errorf("该截在逗号后：%q", got)
	}
	if got := Display("abcdefghijklmnop", 4); got != "abcd……" {
		t.Errorf("没标点也没空格就在上限处截：%q", got)
	}
	if got := Display(strings.Repeat("😀", 40), 16); displayWidth(got) > 32 || !strings.HasSuffix(got, Ellipsis) {
		t.Errorf("表情截不对：%q", got)
	}
}

// X-15：补的“……”接在最后一个字后面排不下、被挤到下一行，就成了单独一行的两个点；要往前多收几个字让它跟在字后面。
func TestFitBodyLinesNoLoneEllipsis(t *testing.T) {
	// 一行 10 个字（半角字母每个算 1），超出换行
	lines := func(d string) int {
		n := 0
		for _, seg := range strings.Split(d, "\n") {
			n += max(1, (utf8.RuneCountInString(seg)+9)/10)
		}
		return n
	}
	s := "aaaaaaaaa，" + strings.Repeat("bbbbbbbbb，", 10)
	got, cut := FitBodyLines(s, 3, lines)
	if !cut || !strings.HasSuffix(got, Ellipsis+"\n"+Notice) {
		t.Fatalf("应截断并写提示：%q", got)
	}
	head := strings.TrimSuffix(got, "\n"+Notice)
	if lines(head) != lines(strings.TrimSuffix(head, Ellipsis)) {
		t.Fatalf("“……”单独占了一行：%q", got)
	}
	if lines(got) > 3 {
		t.Fatalf("超过 3 行：%q", got)
	}
	// 排得下的全文原样
	if got, cut := FitBodyLines("短短一句", 3, lines); cut || got != "短短一句" {
		t.Fatalf("全文排得下该原样：%q %v", got, cut)
	}
}
