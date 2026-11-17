package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

type menuRig struct {
	bar     SelectionBar
	replies []Selected
	viewed  []int
	tt      *ui.Tester
	m       *Msg
}

func newMenuRig(t *testing.T, md, canReply bool, imageOnly bool) *menuRig {
	t.Helper()
	r := &menuRig{}
	tokens := Derive(DefaultSettings())
	host := Host{T: tokens, Column: 600, NoEnter: true, Selection: &r.bar, MD: NewMD(),
		OnViewImage: func(_ string, pos int) { r.viewed = append(r.viewed, pos) },
		Media:       &blockMedia{state: MediaResult{State: MediaLoading}},
	}
	if canReply {
		host.OnReply = func(s Selected, _ bool) { r.replies = append(r.replies, s) }
	}
	r.m = &Msg{ID: "m1", Sender: "她", Name: "她", Text: selMsgText, Markdown: md, Blocks: segview.TextBlocks(selMsgText), At: time.UnixMilli(1000)}
	if imageOnly {
		r.m.Text = ""
		r.m.Blocks = []segview.Block{{Kind: segview.Images, Imgs: []segview.Segment{{Position: 2, Width: 100, Height: 100}}}}
	}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(100, 24, 24, 24).Children(func() { host.Message(c, r.m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func (r *menuRig) texts() string { return strings.Join(r.tt.Texts(), "|") }

func (r *menuRig) selectMiddle(t *testing.T) (x, y float32) {
	t.Helper()
	rect, ok := findText(r.tt, "第一句在这")
	if !ok {
		t.Fatalf("找不到正文：%v", r.tt.Texts())
	}
	y = rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*0.2, y)
	r.tt.Move(rect.X+rect.W*0.4, y)
	r.tt.Move(rect.X+rect.W*0.6, y)
	r.tt.Release(rect.X+rect.W*0.6, y)
	r.tt.Frame()
	r.tt.Advance(selectionSettle + 10*time.Millisecond)
	r.tt.Frame()
	return rect.X + rect.W*0.4, y
}

func noDefaultMenu(t *testing.T, got string) {
	t.Helper()
	for _, bad := range []string{"Copy", "Select All", "Select all"} {
		if strings.Contains(got, bad) {
			t.Fatalf("还是系统自带的英文菜单 %q：%s", bad, got)
		}
	}
	if menu := ""; menu != "" {
		t.Fatal(menu)
	}
}

// 有选区时右键：复制（选中的）、回复这一段；点复制后剪贴板是选中的字；点回复走选区条同一个动作。
func TestMessageMenuWithSelection(t *testing.T) {
	for _, md := range []bool{false, true} {
		r := newMenuRig(t, md, true, false)
		x, y := r.selectMiddle(t)
		sel := r.bar.Current().Text
		if sel == "" {
			t.Fatalf("md=%v 没选中文字", md)
		}
		r.tt.RightClickAt(x, y)
		r.tt.Frame()
		got := r.texts()
		for _, want := range []string{"复制", "回复这一段"} {
			if !strings.Contains(got, want) {
				t.Fatalf("md=%v 菜单缺 %q：%s", md, want, got)
			}
		}
		noDefaultMenu(t, got)
		if err := r.tt.Click("复制"); err != nil {
			t.Fatal(err)
		}
		if got := r.tt.Clipboard(); got != sel {
			t.Fatalf("md=%v 复制到 %q，应是选中的 %q", md, got, sel)
		}
		r.tt.RightClickAt(x, y)
		r.tt.Frame()
		if err := r.tt.Click("回复这一段"); err != nil {
			t.Fatal(err)
		}
		if len(r.replies) != 1 || r.replies[0].Text != sel || r.replies[0].MessageID != "m1" {
			t.Fatalf("md=%v 回复 %+v，应带着选中的 %q", md, r.replies, sel)
		}
	}
}

// 没有选区时右键：复制整条、回复（引用整条）。
func TestMessageMenuWithoutSelection(t *testing.T) {
	for _, md := range []bool{false, true} {
		r := newMenuRig(t, md, true, false)
		rect, ok := findText(r.tt, "第一句在这")
		if !ok {
			t.Fatal("找不到正文")
		}
		r.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
		r.tt.Frame()
		got := r.texts()
		if !strings.Contains(got, "复制") || !strings.Contains(got, "回复") || strings.Contains(got, "回复这一段") {
			t.Fatalf("md=%v 无选区的菜单不对：%s", md, got)
		}
		noDefaultMenu(t, got)
		if err := r.tt.Click("复制"); err != nil {
			t.Fatal(err)
		}
		if got := r.tt.Clipboard(); got != selMsgText {
			t.Fatalf("md=%v 复制到 %q，应是整条 %q", md, got, selMsgText)
		}
		r.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
		r.tt.Frame()
		if err := r.tt.Click("回复"); err != nil {
			t.Fatal(err)
		}
		if len(r.replies) != 1 || r.replies[0].Text != selMsgText {
			t.Fatalf("md=%v 回复 %+v，应引用整条", md, r.replies)
		}
	}
}

// 不能回复的会话（本地群聊、QQ）：菜单只有复制，没有回复，也没有灰掉的项。
func TestMessageMenuCopyOnlyWhenCannotReply(t *testing.T) {
	for _, md := range []bool{false, true} {
		r := newMenuRig(t, md, false, false)
		rect, _ := findText(r.tt, "第一句在这")
		r.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
		r.tt.Frame()
		got := r.texts()
		if !strings.Contains(got, "复制") || strings.Contains(got, "回复") {
			t.Fatalf("md=%v 只应有复制：%s", md, got)
		}
		noDefaultMenu(t, got)
	}
}

// 图片上右键：查看图片（旧界面只有这个动作）、回复；不是系统菜单。
func TestMessageMenuOnImage(t *testing.T) {
	r := newMenuRig(t, false, true, true)
	r.m.Text = "配文"
	r.tt.Frame()
	img, ok := r.tt.Find("查看图片")
	if !ok {
		t.Fatalf("找不到图片：%s", r.texts())
	}
	r.tt.RightClickAt(img.X+img.W/2, img.Y+img.H/2)
	r.tt.Frame()
	got := r.texts()
	if strings.Count(got, "查看图片") < 1 || !strings.Contains(got, "回复") {
		t.Fatalf("图片菜单不对：%s", got)
	}
	noDefaultMenu(t, got)
	r.tt.Frame()
}
