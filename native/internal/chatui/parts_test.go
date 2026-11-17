package chatui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func TestHighlightRangeInPrefersOffsetsThenFirstMatch(t *testing.T) {
	const orig = "第一句在这。第二句在那。第二句在那。"
	hl := &Highlight{Text: "第二句在那", Original: orig, Start: 12, End: 17}
	if lo, hi, ok := hl.rangeIn(orig, true); !ok || lo != 12 || hi != 17 {
		t.Fatalf("偏移对得上原文时该按偏移（第二处）：%d %d %v", lo, hi, ok)
	}
	if lo, hi, ok := hl.rangeIn(orig, false); !ok || lo != 6 || hi != 11 {
		t.Fatalf("多个块时不信偏移，找第一处：%d %d %v", lo, hi, ok)
	}
	if lo, hi, ok := (&Highlight{Text: "没有这句"}).rangeIn(orig, true); ok {
		t.Fatalf("找不到就不高亮：%d %d", lo, hi)
	}
	if lo, hi, ok := (&Highlight{Whole: true, Text: orig}).rangeIn(orig, true); !ok || lo != 0 || hi != 18 {
		t.Fatalf("整条引用高亮整段：%d %d %v", lo, hi, ok)
	}
	if _, _, ok := hl.rangeIn("", true); ok {
		t.Fatal("空文字不高亮")
	}
}

func TestMarkRunsSplitsAtRuneOffsetsAndKeepsFacesWhole(t *testing.T) {
	runs := []segview.Run{{Text: "你好世界", Bold: false}, {Text: "[微笑]", Face: &segview.FaceRef{ID: "14", Kind: "face"}}, {Text: "再见"}}
	got := markRuns(runs, 1, 3)
	var texts []string
	var marks []bool
	for _, r := range got {
		texts, marks = append(texts, r.Text), append(marks, r.mark)
	}
	if strings.Join(texts, "|") != "你|好世|界|[微笑]|再见" || !(marks[1] && !marks[0] && !marks[2] && !marks[3] && !marks[4]) {
		t.Fatalf("切得不对：%v %v", texts, marks)
	}
	got = markRuns(runs, 3, 6) // 区间碰到表情的一端：表情整个高亮
	if !got[len(got)-2].mark || got[len(got)-2].Face == nil {
		t.Fatalf("表情碰到区间该整个高亮：%+v", got)
	}
	if plain := func() string {
		var b strings.Builder
		for _, r := range got {
			b.WriteString(r.Text)
		}
		return b.String()
	}(); plain != "你好世界[微笑]再见" {
		t.Fatalf("切开后拼回去该和原文一样：%q", plain)
	}
	for _, r := range markRuns(runs, -1, 0) {
		if r.mark {
			t.Fatal("lo < 0 不该高亮")
		}
	}
}

type partsRig struct {
	host    Host
	msg     *Msg
	tt      *ui.Tester
	clicked []PartQuote
}

func newPartsRig(t *testing.T, parts []Part) *partsRig {
	t.Helper()
	r := &partsRig{}
	r.host = Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, SelfChar: CharColors[0],
		OnQuotePart: func(_ string, q PartQuote) { r.clicked = append(r.clicked, q) }}
	r.msg = &Msg{ID: "me1", Sender: "owner", Self: true, Name: "我", Parts: parts, At: time.UnixMilli(1000)}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 640).Padding(24).Justify(ui.End).Children(func() { r.host.Message(c, r.msg, nil, nil) })
	}, 720, 640)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func samplePartsMsg() []Part {
	return []Part{
		{Quote: &PartQuote{Target: "t1", Name: "编造角色", Snippet: "第二句在那", Original: "第一句在这。第二句在那。", Start: 6, End: 11}, Text: "这句是什么意思", Atts: []PartAtt{{Name: "截图.png", Image: true}}},
		{Quote: &PartQuote{Target: "t2", Name: "编造角色", Snippet: "第三句收尾", Original: "第三句收尾。", End: 5}, Text: "这句我懂了", Atts: []PartAtt{{Name: "笔记.txt"}}},
		{Text: "另外今晚吃什么"},
	}
}

func TestPartsBubbleDrawsBlocksInOrder(t *testing.T) {
	r := newPartsRig(t, samplePartsMsg())
	order := []string{"第二句在那", "这句是什么意思", "截图.png", "第三句收尾", "这句我懂了", "笔记.txt", "另外今晚吃什么"}
	lastY := float32(-1)
	for _, s := range order {
		rect, ok := r.tt.Find(s)
		if !ok {
			t.Fatalf("缺 %q：%v", s, r.tt.Texts())
		}
		if rect.Y <= lastY {
			t.Fatalf("%q 的位置不在前一块下面：%v <= %v", s, rect.Y, lastY)
		}
		lastY = rect.Y
	}
	if _, ok := r.tt.Find("另外今晚吃什么"); !ok {
		t.Fatal("普通块没画")
	}
	// 普通块没有引用卡：最后一个引用卡的名字之后不该再有"编造角色"
	names := 0
	for _, s := range r.tt.Texts() {
		if s == "编造角色" {
			names++
		}
	}
	if n := names; n != 2 {
		t.Fatalf("两张引用卡该各写一次名字：%d", n)
	}
}

func TestPartsBubbleClickQuoteReportsTheSnippet(t *testing.T) {
	r := newPartsRig(t, samplePartsMsg())
	rect, ok := r.tt.Find("引用 编造角色：点一下回到原消息")
	if !ok || rect.W < 24 || rect.H < 24 {
		t.Fatalf("引用块不可点：%+v %v", rect, ok)
	}
	if err := r.tt.Click("引用 编造角色：点一下回到原消息"); err != nil {
		t.Fatal(err)
	}
	if len(r.clicked) != 1 || r.clicked[0].Target != "t1" || r.clicked[0].Snippet != "第二句在那" || r.clicked[0].Start != 6 || r.clicked[0].End != 11 {
		t.Fatalf("点的是第一张引用卡，该报它的出处和片段：%+v", r.clicked)
	}
}

func TestPartsBubbleGoneQuoteKeepsOnlyReplyText(t *testing.T) {
	r := newPartsRig(t, []Part{{Quote: &PartQuote{Target: "t1", Gone: true}, Text: "我当时说的是这个"}})
	if !r.tt.HasText("我当时说的是这个") || !r.tt.HasText("引用的消息已经看不到了") {
		t.Fatalf("看不到的引用只留回复文字和一句说明：%v", r.tt.Texts())
	}
	if _, ok := r.tt.Find("引用 对方：点一下回到原消息"); ok {
		t.Fatal("看不到的引用不该能点")
	}
}

func TestPartsBubbleLongSnippetAndAttachmentsStayInsideTheBubble(t *testing.T) {
	long := strings.Repeat("很长很长的被引片段没有空格", 60) + strings.Repeat("b", 400)
	r := newPartsRig(t, []Part{{
		Quote: &PartQuote{Target: "t", Name: "名字特别特别特别特别特别特别特别特别长的人", Snippet: long},
		Text:  strings.Repeat("回复也很长", 80),
		Atts:  []PartAtt{{Name: strings.Repeat("长文件名", 40) + ".txt"}, {Name: "b.png", Image: true}, {Name: "c.txt"}},
	}})
	rect, ok := r.tt.Find("引用 名字特别特别特别特别特别特别特别特别长的人：点一下回到原消息")
	if !ok || rect.X < 0 || rect.X+rect.W > 720 || rect.H > 160 {
		t.Fatalf("长片段把引用块撑出窗口或太高：%+v %v", rect, ok)
	}
}

func diffPixels(a, b *image.RGBA) int {
	n := 0
	for i := 0; i+3 < len(a.Pix) && i+3 < len(b.Pix); i += 4 {
		if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] {
			n++
		}
	}
	return n
}

func TestHighlightColorsTheQuotedRangeThenFades(t *testing.T) {
	const text = "第一句在这。第二句在那。第三句收尾。"
	hl := &Highlight{}
	host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, SelfChar: CharColors[0], Highlight: hl}
	m := &Msg{ID: "t1", Sender: "她", Text: text, Blocks: segview.TextBlocks(text)}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 240).Padding(24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 240)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	copyOf := func() *image.RGBA {
		img := tt.Image()
		out := image.NewRGBA(img.Bounds())
		copy(out.Pix, img.Pix)
		return out
	}
	base := copyOf()

	*hl = Highlight{MessageID: "t1", Text: "第二句在那", Original: text, Start: 6, End: 11}
	tt.Frame()
	tt.Frame()
	lit := copyOf()
	if n := diffPixels(base, lit); n < 200 {
		t.Fatalf("被引的那一段没有高亮：只差 %d 个像素", n)
	}
	*hl = Highlight{MessageID: "t1", Text: "没有这句话"}
	tt.Frame()
	tt.Frame()
	if n := diffPixels(base, copyOf()); n != 0 {
		t.Fatalf("文字对不上原文时不该高亮：差 %d 个像素", n)
	}
	*hl = Highlight{MessageID: "别的消息", Text: "第二句在那"}
	tt.Frame()
	if n := diffPixels(base, copyOf()); n != 0 {
		t.Fatalf("别的消息的高亮不该画在这条上：差 %d 个像素", n)
	}
	*hl = Highlight{MessageID: "t1", Text: "第二句在那", Original: text, Start: 6, End: 11}
	tt.Frame()
	tt.Advance(highlightSpan + 100*time.Millisecond)
	tt.Frame()
	tt.Frame()
	if n := diffPixels(base, copyOf()); n != 0 {
		t.Fatalf("高亮该在 %v 后退去：还差 %d 个像素", highlightSpan, n)
	}
}
