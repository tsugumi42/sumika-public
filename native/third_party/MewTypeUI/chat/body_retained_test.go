package chat

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func TestRetainedBodyCrossParagraphCopy(t *testing.T) {
	var first, second BodyState
	var selection BodySelection
	tt := ui.NewTester(func(c *ui.Context) {
		root := ui.Column(c).Padding(8).Gap(10)
		selection.Attach(c, root, BodySelectionOptions{PaddingTop: 8, PaddingBottom: 8, CopyData: func(parts []ui.SelectedText) transfer.Data {
			var values []string
			for _, p := range parts {
				values = append(values, string([]rune(p.Text)[p.Start:p.End]))
			}
			return transfer.TextData(strings.Join(values, "\n\n"))
		}})
		root.Children(func() {
			RetainedBody(c, &first, []ui.Span{{Text: "甲😀a\u0301"}}, BodyTextOptions{Width: 180, Font: ui.Font{Size: 14}, Selection: &selection, Label: "first", Color: ui.RGB(0, 0, 0)})
			RetainedBody(c, &second, []ui.Span{{Text: "乙后段"}}, BodyTextOptions{Width: 180, Font: ui.Font{Size: 14}, Selection: &selection, Label: "second", Color: ui.RGB(0, 0, 0)})
		})
		selection.Finish()
		root.ContextMenu(selection.EditItems)
	}, 220, 160)
	tt.Frame()
	tt.Frame()
	a, _ := tt.Find("first")
	b, _ := tt.Find("second")
	tt.Press(a.X-2, a.Y+2)
	tt.Move(b.X+b.W+3, b.Y+b.H-2)
	tt.Release(b.X+b.W+3, b.Y+b.H-2)
	tt.Key(ui.Cmd, ui.KeyC)
	const want = "甲😀a\u0301\n\n乙后段"
	if tt.Clipboard() != want {
		t.Fatalf("跨段复制=%q", tt.Clipboard())
	}
	tt.ClickAt(a.X, a.Y+a.H/2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != want {
		t.Fatalf("全选复制=%q", tt.Clipboard())
	}
	if err := tt.RightClick("second"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != want {
		t.Fatalf("菜单复制=%q", tt.Clipboard())
	}
	// 字素导航不能拆开代理对或组合字符。
	tt.Key(0, ui.KeyHome)
	tt.Key(ui.Shift, ui.KeyRight)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != "乙" {
		t.Fatalf("键盘选区=%q", tt.Clipboard())
	}
}

func TestRetainedBodyReflowAndLinks(t *testing.T) {
	for _, direction := range []ui.Direction{ui.LeftToRight, ui.RightToLeft} {
		var state BodyState
		var selection BodySelection
		width := float32(220)
		font := ui.Font{Size: 14}
		spans := []ui.Span{{Text: "甲😀a\u0301 "}, {Text: "https://example.com/path", Underline: true}, {Text: "\uFFFC", ObjectWidth: 19, ObjectHeight: 15}}
		tail := ui.Span{Text: "\uFFFC", Size: 0.0001, ObjectWidth: 54, ObjectHeight: 12}
		links := []BodyLink{{Start: 5, End: 29, Label: "link", URL: "https://example.com/path"}}
		var opened string
		var objects []ui.TextObject
		tt := ui.NewTester(func(c *ui.Context) {
			root := ui.Column(c).Padding(8)
			selection.Attach(c, root, BodySelectionOptions{PaddingTop: 8, PaddingBottom: 8})
			root.Children(func() {
				objects = objects[:0]
				RetainedBody(c, &state, spans, BodyTextOptions{Width: width, Font: font, Direction: c.Direction(), Selection: &selection, Trailing: tail, Label: "body", Links: links, DrawObject: func(_ *ui.Painter, at int, r ui.Rect) { objects = append(objects, ui.TextObject{Index: at, Rect: r}) }})
				if v := state.ClickedLink(); v != "" {
					opened = v
				}
			})
			selection.Finish()
		}, 260, 300)
		tt.SetDirection(direction)
		tt.Frame()
		tt.Frame()
		if err := tt.Click("link"); err != nil {
			t.Fatal(err)
		}
		if opened != "https://example.com/path" {
			t.Fatalf("方向 %v 链接动作=%q", direction, opened)
		}
		opened = ""
		tt.Key(0, ui.KeyEnter)
		if opened != "https://example.com/path" {
			t.Fatalf("方向 %v 键盘链接动作=%q", direction, opened)
		}
		before, _ := tt.Find("body")
		width = 96
		tt.Frame()
		tt.Frame()
		after, _ := tt.Find("body")
		if after.W > 96 || after.H <= before.H {
			t.Fatalf("窄宽重排：%+v -> %+v", before, after)
		}
		// 排版的 UTF-16 对象索引必须转换成 rune；尾槽不能冒充正文对象。
		wantTail := 30
		if direction == ui.RightToLeft {
			wantTail = 31
		} // LTR 末段在 RTL 容器里先换行再放时间。
		if len(objects) != 2 || objects[0].Index != 29 || objects[1].Index != wantTail {
			t.Fatalf("对象下标=%+v", objects)
		}
		tt.ClickAt(after.X, after.Y+2)
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Key(ui.Cmd, ui.KeyC)
		if want := "甲😀a\u0301 https://example.com/path\uFFFC"; tt.Clipboard() != want {
			t.Fatalf("方向 %v 复制=%q", direction, tt.Clipboard())
		}
		font.Size = 20
		tt.Frame()
		tt.Frame()
		larger, _ := tt.Find("body")
		if larger.H <= after.H {
			t.Fatalf("字体变化未重排：%+v -> %+v", after, larger)
		}
		// 替换正文必须去掉旧选区，而 append 允许原选区保持。
		spans = []ui.Span{{Text: "替换后的正文"}}
		links = nil
		tt.Frame()
		tt.Frame()
		if len(selection.SelectedTexts()) != 0 {
			t.Fatal("正文替换还保留旧选区")
		}
	}
}

func TestRetainedBodyWordAndLineCopy(t *testing.T) {
	var state BodyState
	var selection BodySelection
	tt := ui.NewTester(func(c *ui.Context) {
		root := ui.Column(c).Padding(8)
		selection.Attach(c, root, BodySelectionOptions{PaddingTop: 8, PaddingBottom: 8})
		root.Children(func() {
			RetainedBody(c, &state, []ui.Span{{Text: "alpha 😀a\u0301 omega\n下一行"}}, BodyTextOptions{Width: 220, Font: ui.Font{Size: 14}, Selection: &selection, Label: "body"})
		})
		selection.Finish()
	}, 260, 180)
	tt.Frame()
	r, _ := tt.Find("body")
	caret := state.layout.CaretAt(ui.TextCaretPosition{Index: 2})
	x, y := r.X+caret.X, r.Y+caret.Y+caret.H/2
	tt.ClickAt(x, y)
	tt.ClickAt(x, y)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != "alpha" {
		t.Fatalf("双击复制=%q", tt.Clipboard())
	}
	tt.ClickAt(x, y)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != "alpha 😀a\u0301 omega" {
		t.Fatalf("三击复制=%q", tt.Clipboard())
	}
}
