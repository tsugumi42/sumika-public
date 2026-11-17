package chatui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

var mdCorpus = []struct{ name, text string }{
	{"单行", "好呀。"},
	{"长段中文", "窗边的纸灯亮起来了，今天想聊些什么？可以从早上发生的小事说起，也可以随便聊聊别的，我都在听，不着急，慢慢说。"},
	{"粗体和行内代码", "这里有**加粗的几个字**、*斜体*，还有 `go test ./...` 这样的行内代码，以及一个[链接](https://example.com)。"},
	{"单个换行", "第一行\n第二行稍微长一点\n第三行"},
	{"两段", "第一段。\n\n第二段要长一些，长到会换行才好看出来段距和行高对不对得上，再多写几个字。"},
	{"英文长词", "A long URL https://example.com/a/very/long/path/that/keeps/going/and/going wraps somewhere."},
}

// layoutOf 在一帧里量这条消息的排版（measure 要 Context）。
func layoutOf(h Host, m *Msg) layout {
	var l layout
	tt := ui.NewTester(func(c *ui.Context) { l = h.measure(c, m, nil, nil) }, 100, 100)
	tt.Frame()
	return l
}

// markdown 正文画出来的高、宽要和 chatui 量出来的一致（几何、单行胶囊、时间挂行尾都靠它），三档外观。
func TestMarkdownMatchesMeasure(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		tk := Derive(s)
		for _, cs := range mdCorpus {
			h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
			m := &Msg{ID: "m", Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local), Text: cs.text, Markdown: true}
			l := layoutOf(h, m)
			view := func(c *ui.Context) {
				ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
			}
			tt := ui.NewTester(view, 900, 900)
			tt.Frame()
			tt.Frame()
			root := tree(tt)
			txt, bubble := root.find("text"), root.find("bubble")
			if txt == nil || bubble == nil {
				t.Fatalf("%s 找不到正文", cs.name)
			}
			name := fmt.Sprintf("外观 %v/%v/%v %s", s.Text, s.Radius, s.Density, cs.name)
			if math.Abs(float64(txt.Box.H-l.TextH)) > 1 {
				t.Errorf("%s 正文实际高 %.1f，量的 %.1f（%d 行 %d 段）", name, txt.Box.H, l.TextH, l.Lines, l.Paras)
			}
			// 最长一行没被挤到下一行：用量出来的宽画，行数和量的一样（高度对上就说明没多折行）；宽度按量的收紧
			if want := l.ContentW + 2*tk.BubblePadH; math.Abs(float64(bubble.Box.W-want)) > 1 {
				t.Errorf("%s 气泡宽 %.1f，期望 %.1f", name, bubble.Box.W, want)
			}
		}
	}
}

// 纯文字（没有 markdown 语法）的消息，走 markdown 量出来的和原来的纯文字量法逐项相等：换了渲染，几何不变。
func TestMarkdownPlainSameAsBefore(t *testing.T) {
	tk := Derive(DefaultSettings())
	for _, cs := range []struct{ name, text string }{mdCorpus[0], mdCorpus[1], mdCorpus[3], mdCorpus[4]} {
		m := &Msg{ID: "m", Sender: "r1", Name: "晴枝", At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local), Text: cs.text}
		plain := layoutOf(Host{T: tk, Column: 720}, m)
		m.Markdown = true
		md := layoutOf(Host{T: tk, Column: 720, MD: NewMD()}, m)
		if plain.Lines != md.Lines || plain.Paras != md.Paras || !near(plain.Fit, md.Fit) || !near(plain.LastW, md.LastW) || !near(plain.TextH, md.TextH) || plain.SingleLine != md.SingleLine || plain.InfoInline != md.InfoInline {
			t.Errorf("%s：纯文字 %+v\nmarkdown %+v", cs.name, brief(plain), brief(md))
		}
	}
}

func brief(l layout) string {
	return fmt.Sprintf("行%d 段%d 宽%.1f 末行%.1f 高%.1f 单行%v 时间挂行尾%v", l.Lines, l.Paras, l.Fit, l.LastW, l.TextH, l.SingleLine, l.InfoInline)
}

// 有列表、代码块的：宽度铺满、时间另起一行、不当单行胶囊。
func TestMarkdownBlocky(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, MD: NewMD()}
	for _, text := range []string{"要做的：\n\n- 第一件\n- 第二件", "看这个：\n\n```go\nx := 1\n```"} {
		l := layoutOf(h, &Msg{ID: text, Sender: "r1", At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local), Text: text, Markdown: true})
		if !l.Blocky || !near(l.Fit, l.TextW) || l.InfoInline || l.SingleLine {
			t.Errorf("%q：%s 块状=%v", text, brief(l), l.Blocky)
		}
	}
}

// 量宽要按真实字形：一串加粗的字正好"普通粗细放得下一行、加粗就折到第二行"，画出来的高要和量的一样。
func TestMarkdownBoldEdge(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
	width := layoutOf(h, &Msg{ID: "w", Sender: "r1", Text: "x"}).TextW
	font := ui.Font{Family: h.Font, Size: tk.Body, LineHeight: tk.BodyLeading()}
	lines := func(s string, w int) int {
		return len(ui.ShapeRichText([]ui.Span{{Text: s, Size: tk.Body, Weight: w}}, font, width).Lines())
	}
	s := ""
	for i := 0; i < 400; i++ {
		s += []string{"Lamp ", "glow ", "warm "}[i%3] // 拉丁字母加粗比普通宽（中文在测试字体里加粗不变宽）
		if w := strings.TrimSpace(s); lines(w, 0) == 1 && lines(w, 600) == 2 {
			break
		}
	}
	s = strings.TrimSpace(s) // "**…空格**" 不算粗体，末尾不能有空格
	if !(lines(s, 0) == 1 && lines(s, 600) == 2) {
		t.Skip("这套字体里加粗和普通一样宽，造不出边界")
	}
	m := &Msg{ID: "m", Sender: "r1", Text: "**" + s + "**", Markdown: true}
	l := layoutOf(h, m)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
	}, 900, 400)
	tt.Frame()
	tt.Frame()
	if txt := tree(tt).find("text"); txt == nil || math.Abs(float64(txt.Box.H-l.TextH)) > 1 || l.Lines != 2 {
		t.Errorf("加粗边界：量了 %d 行、高 %.1f，画出来高 %v", l.Lines, l.TextH, txt.Box.H)
	}
}

// 代码块：外框自己画（没有边框、圆角同心），复制按钮只有图标，点了两秒内显示"已复制"，之后变回来。
func TestCodeBlockCopy(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
	m := &Msg{ID: "m", Sender: "r1", Text: "看这个：\n\n```go\nx := 1\n```", Markdown: true}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
	}, 900, 600)
	tt.Frame()
	tt.Frame()
	box := tree(tt).find("code")
	if box == nil {
		t.Fatal("代码块没走钩子")
	}
	if want := max(tk.Inner(tk.R1, tk.BubblePadH), tk.RInnerMin); !near(box.Radius[0], want) || !near(box.Pad[0], box.Pad[1]) {
		t.Errorf("代码块圆角 %v（期望 %.1f）、内距 %v", box.Radius, want, box.Pad)
	}
	if tt.HasText("Copy") {
		t.Error("复制按钮还写着英文")
	}
	if err := tt.Click("复制代码"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("已复制"); !ok {
		t.Error("点了复制以后没显示已复制")
	}
	tt.Advance(2100 * time.Millisecond)
	if _, ok := tt.Find("复制代码"); !ok {
		t.Error("两秒后没变回复制")
	}
}
