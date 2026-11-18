package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

const mdSelText = "先说一段普通的话，里面有 **加粗** 的字。\n\n- 第一项内容在这\n- 第二项内容在那\n\n```go\nfmt.Println(\"代码块里的字\")\n```\n"

func newMDSelRig(t *testing.T, withBlocks bool) *selRig {
	t.Helper()
	r := &selRig{}
	tokens := Derive(DefaultSettings())
	host := Host{T: tokens, Column: 600, NoEnter: true, Selection: &r.bar, MD: NewMD(),
		OnReply: func(s Selected, keep bool) { r.replies, r.keep = append(r.replies, s), append(r.keep, keep) }}
	m := &Msg{ID: "m1", Sender: "她", Name: "她", Text: mdSelText, Markdown: true, At: time.UnixMilli(1000)}
	if withBlocks {
		m.Blocks = segview.TextBlocks(mdSelText)
	}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func TestMarkdownSelectionBarAppears(t *testing.T) {
	for _, withBlocks := range []bool{false, true} {
		for _, target := range []string{"先说一段普通的话", "第一项内容在这", "代码块里的字"} {
			r := newMDSelRig(t, withBlocks)
			rect, ok := findText(r.tt, target)
			if !ok {
				t.Fatalf("找不到 %q：%v", target, r.tt.Texts())
			}
			y := rect.Y + rect.H/2
			r.tt.Press(rect.X+2, y)
			r.tt.Move(rect.X+rect.W/2, y)
			r.tt.Move(rect.X+rect.W-2, y)
			r.tt.Release(rect.X+rect.W-2, y)
			r.tt.Frame()
			r.settle()
			t.Logf("blocks=%v %s: bar=%v cur=%q", withBlocks, target, r.hasBar(), r.bar.Current().Text)
		}
	}
}

// findText 找第一个文字里含 s 的元素的框（Tester.Find 只认整段相等）。
func findText(tt *ui.Tester, s string) (ui.Rect, bool) {
	for _, x := range tt.Texts() {
		if strings.Contains(x, s) {
			return tt.Find(x)
		}
	}
	return ui.Rect{}, false
}

// 拖过正文右边界、在气泡外松手，条也要弹（松手事件不一定落在正文元素里）。
func TestSelectionBarAppearsWhenReleasedOutsideText(t *testing.T) {
	for name, mk := range map[string]func() *selRig{
		"纯文字":          func() *selRig { return newSelRig(t, selMsgText) },
		"markdown":     func() *selRig { return newMDSelRig(t, true) },
		"markdown 无分块": func() *selRig { return newMDSelRig(t, false) },
	} {
		r := mk()
		rect, ok := findText(r.tt, "先说一段普通的话")
		if name == "纯文字" {
			rect, ok = r.tt.Find(selMsgText)
		}
		if !ok {
			t.Fatalf("%s：找不到正文", name)
		}
		y := rect.Y + rect.H/2
		r.tt.Press(rect.X+4, y)
		r.tt.Move(rect.X+rect.W/2, y)
		r.tt.Move(rect.X+rect.W+60, y)
		r.tt.Release(rect.X+rect.W+60, y)
		r.tt.Frame()
		r.settle()
		if !r.hasBar() {
			t.Errorf("%s：在正文外松手后没有选区条（选区 %q）", name, r.bar.Current().Text)
		}
	}
}

// 设 CHATUI_SHOT_MDSEL=目录 时，把"选中 Markdown 段落、选区条弹出"的组件区域存成 PNG；平时跳过。
func TestShotMarkdownSelection(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT_MDSEL")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT_MDSEL 才出图")
	}
	r := newMDSelRig(t, true)
	r.tt.SetScale(1.25)
	r.tt.Frame()
	rect, ok := findText(r.tt, "先说一段普通的话")
	if !ok {
		t.Fatal("找不到段落")
	}
	y := rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*0.1, y)
	r.tt.Move(rect.X+rect.W*0.4, y)
	r.tt.Move(rect.X+rect.W*0.7, y)
	r.tt.Release(rect.X+rect.W*0.7, y)
	r.tt.Frame()
	r.settle()
	if !r.hasBar() {
		t.Fatal("没有选区条")
	}
	img := r.tt.Image()
	b := img.Bounds()
	crop := image.Rect(20, 20, min(b.Max.X, 825), min(b.Max.Y, 420))
	f, err := os.Create(filepath.Join(dir, "md选区条.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img.SubImage(crop)); err != nil {
		t.Fatal(err)
	}
}
