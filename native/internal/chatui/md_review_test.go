package chatui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// renderOne 画一条消息，返回节点树和这条的排版。
func renderOne(h Host, m *Msg) (*node, layout) {
	l := layoutOf(h, m)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
	}, 900, 900)
	tt.Frame()
	tt.Frame()
	return tree(tt), l
}

// 画出来一样的两种写法，量出来要一样宽（mygo 怎么画就怎么量：行内 HTML 标签不出字、公式是等宽小字、图片是 "[说明]"）。
func TestMarkdownEquivalentWidths(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, MD: NewMD()}
	for _, p := range [][2]string{
		{"这是<b>粗</b>字和<code>x := 1</code>", "这是**粗**字和`x := 1`"},
		{"公式 \\(x^2+y^2\\) 结束", "公式 `x^2+y^2` 结束"}, // 默认扩展里单个 $ 不算公式（要另开 ExtDollarMath），\( \) 和 $$ $$ 才是
		{"公式 $x^2+y^2$ 结束", "公式 $x^2+y^2$ 结束"},     // 单个 $ 是普通文字：mygo 按正文画，量也按正文
		{"看这个 ![图](https://example.com/a.png) 字", "看这个 [图] 字"},
		{"看这个 ![](https://example.com/a.png) 字", "看这个 [image] 字"},
	} {
		a := layoutOf(h, &Msg{ID: p[0], Sender: "r1", Text: p[0], Markdown: true})
		b := layoutOf(h, &Msg{ID: p[1], Sender: "r1", Text: p[1], Markdown: true})
		if math.Abs(float64(a.Fit-b.Fit)) > 1 {
			t.Errorf("%q 量宽 %.1f，和画法一样的 %q（%.1f）不一样", p[0], a.Fit, p[1], b.Fit)
		}
	}
}

// <br> 是换行：量成两行、不当单行胶囊，画出来的高和量的一样。
func TestMarkdownBrIsLineBreak(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
	root, l := renderOne(h, &Msg{ID: "m", Sender: "r1", At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local), Text: "第一行<br>第二行", Markdown: true})
	if l.Lines != 2 || l.SingleLine {
		t.Errorf("<br>：量了 %d 行、单行=%v", l.Lines, l.SingleLine)
	}
	if txt := root.find("text"); math.Abs(float64(txt.Box.H-l.TextH)) > 1 {
		t.Errorf("<br>：画出来高 %.1f，量的 %.1f", txt.Box.H, l.TextH)
	}
}

// 还没有字（刚开始出、只有思考段）时画一行空的占位，和量的一样，第一个字来时气泡不跳。
func TestMarkdownEmptyStreaming(t *testing.T) {
	tk := Derive(DefaultSettings())
	for _, text := range []string{"", "<think>想想看"} {
		h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
		root, l := renderOne(h, &Msg{ID: "m", Sender: "r1", Text: text, State: Streaming, Markdown: true})
		if txt := root.find("text"); txt == nil || math.Abs(float64(txt.Box.H-l.TextH)) > 1 || math.Abs(float64(l.TextH-tk.BodyLine)) > 0.5 {
			t.Errorf("%q：画出来高 %v，量的 %.1f（期望一行 %.1f）", text, txt.Box.H, l.TextH, tk.BodyLine)
		}
	}
}

// 四个反引号的围栏里，三个反引号那一行不算结束；结束以后的单个换行照常补成硬换行。
func TestHardBreaksLongFence(t *testing.T) {
	in := "````\n```\nx\ny\n````\nz\nw"
	want := "````\n```\nx\ny\n````\nz  \nw"
	if got := hardBreaks(in); got != want {
		t.Errorf("hardBreaks(%q) = %q，期望 %q", in, got, want)
	}
}

// 代码块标题行：语言名离框上边和离框左边一样远（复制按钮浮在右上角，不撑高这一行）；复制按钮离框上、右也一样远。
func TestCodeBlockHeadInset(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		tk := Derive(s)
		h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
		root, _ := renderOne(h, &Msg{ID: "m", Sender: "r1", Text: "看：\n\n```go\nx := 1\n```", Markdown: true})
		box, head, btn := root.find("code"), root.find("code-head"), root.find("copy")
		if box == nil || head == nil || btn == nil {
			t.Fatal("找不到代码块的框、标题行或复制按钮")
		}
		var label *node
		for _, ch := range head.Children {
			if ch.Text == "go" {
				label = ch
			}
		}
		if label == nil {
			t.Fatal("标题行里没有语言名")
		}
		top, left := label.Box.Y-box.Box.Y, label.Box.X-box.Box.X
		if !near(top, left) {
			t.Errorf("外观 %v/%v/%v：语言名离上边 %.2f、离左边 %.2f", s.Text, s.Radius, s.Density, top, left)
		}
		if bt, br := btn.Box.Y-box.Box.Y, box.Box.X+box.Box.W-(btn.Box.X+btn.Box.W); !near(bt, br) {
			t.Errorf("外观 %v/%v/%v：复制按钮离上边 %.2f、离右边 %.2f", s.Text, s.Radius, s.Density, bt, br)
		}
	}
}
