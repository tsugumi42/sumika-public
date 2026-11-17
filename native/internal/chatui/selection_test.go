package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

const selMsgText = "第一句在这。第二句在那。第三句收尾。"

type selRig struct {
	bar     SelectionBar
	replies []Selected
	keep    []bool
	tt      *ui.Tester
}

func newSelRig(t *testing.T, text string) *selRig {
	t.Helper()
	r := &selRig{}
	tokens := Derive(DefaultSettings())
	host := Host{T: tokens, Column: 600, NoEnter: true, Selection: &r.bar,
		OnReply: func(s Selected, keep bool) { r.replies, r.keep = append(r.replies, s), append(r.keep, keep) }}
	m := &Msg{ID: "m1", Sender: "她", Name: "她", Text: text, Blocks: segview.TextBlocks(text), At: time.UnixMilli(1000)}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

// dragAcross 在消息正文里从第 from 个字的位置拖到 to 的位置（按整行平均宽度估，只用来造一个选区）。
func (r *selRig) dragAcross(t *testing.T, from, to float32) {
	t.Helper()
	rect, ok := r.tt.Find(selMsgText)
	if !ok {
		t.Fatalf("找不到消息正文：%v", r.tt.Texts())
	}
	y := rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*from, y)
	r.tt.Move(rect.X+rect.W*(from+to)/2, y)
	r.tt.Move(rect.X+rect.W*to, y)
	r.tt.Release(rect.X+rect.W*to, y)
	r.tt.Frame()
}

func (r *selRig) settle() {
	r.tt.Advance(selectionSettle + 10*time.Millisecond)
	r.tt.Frame()
	r.tt.Frame()
}

func (r *selRig) hasBar() bool {
	_, ok := r.tt.Find("回复")
	return ok
}

func TestSelectionToolAppearsAfterSettleAndRepliesWithTheSelectedText(t *testing.T) {
	r := newSelRig(t, selMsgText)
	r.dragAcross(t, 0.3, 0.6)
	if r.hasBar() {
		t.Fatal("松手后要稳定一会儿才弹，拖选刚结束就弹会在双击三击时闪")
	}
	r.settle()
	if !r.hasBar() {
		t.Fatalf("选区稳定后没有工具条：%v", r.tt.Texts())
	}
	if _, ok := r.tt.Find("复制"); !ok {
		t.Fatal("工具条上没有复制")
	}
	reply, _ := r.tt.Find("回复")
	if reply.W < 24 || reply.H < 24 {
		t.Fatalf("回复键小于 24：%+v", reply)
	}
	if err := r.tt.Click("回复"); err != nil {
		t.Fatal(err)
	}
	if len(r.replies) != 1 || !r.keep[0] {
		t.Fatalf("点回复应当加一张卡并把焦点给它：%v %v", r.replies, r.keep)
	}
	got := r.replies[0]
	rs := []rune(selMsgText)
	if got.MessageID != "m1" || got.Text == "" || got.End <= got.Start || string(rs[got.Start:got.End]) != got.Text {
		t.Fatalf("选区文字和偏移对不上：%+v", got)
	}
	r.tt.Frame()
	r.settle()
	if r.hasBar() {
		t.Fatal("点了回复，工具条该收起，同一段选区不再弹")
	}
}

func TestSelectionToolStaysAwayWhileDraggingAndWithoutSelection(t *testing.T) {
	r := newSelRig(t, selMsgText)
	rect, _ := r.tt.Find(selMsgText)
	y := rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*0.2, y)
	r.tt.Move(rect.X+rect.W*0.5, y)
	r.settle()
	if r.hasBar() {
		t.Fatal("鼠标还按着在选的时候不该弹")
	}
	r.tt.Release(rect.X+rect.W*0.5, y)
	r.settle()
	if !r.hasBar() {
		t.Fatal("松手后该弹")
	}
	r.tt.ClickAt(rect.X+rect.W*0.9, rect.Y+rect.H*3) // 点空白处，选区没了
	r.settle()
	if r.hasBar() {
		t.Fatal("选区没了工具条该收起")
	}
}

func TestSelectionToolEscDismissesUntilTheSelectionChanges(t *testing.T) {
	r := newSelRig(t, selMsgText)
	r.dragAcross(t, 0.2, 0.5)
	r.settle()
	if !r.hasBar() {
		t.Fatal("前提：工具条该弹出来")
	}
	r.tt.Key(0, ui.KeyEscape)
	r.tt.Frame()
	r.settle()
	if r.hasBar() {
		t.Fatal("Esc 该收起工具条")
	}
	r.settle()
	if r.hasBar() {
		t.Fatal("Esc 收起后同一段选区不该再弹")
	}
	r.dragAcross(t, 0.5, 0.9)
	r.settle()
	if !r.hasBar() {
		t.Fatal("选区换了应当重新弹")
	}
}

func TestSelectionToolAltReplyDoesNotTakeFocusAndCopyWritesTheText(t *testing.T) {
	r := newSelRig(t, selMsgText)
	r.dragAcross(t, 0.1, 0.4)
	r.settle()
	if err := r.tt.ClickWith(ui.Alt, "回复"); err != nil {
		t.Fatal(err)
	}
	if len(r.replies) != 1 || r.keep[0] {
		t.Fatalf("按住 Alt 点回复应当只加卡、不抢焦点：%v %v", r.replies, r.keep)
	}
	r.dragAcross(t, 0.5, 0.8)
	r.settle()
	want := r.bar.Current().Text
	if want == "" {
		t.Fatal("没有选区")
	}
	if err := r.tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if got := r.tt.Clipboard(); got != want {
		t.Fatalf("复制的不是选中的那段：%q 想要 %q", got, want)
	}
}

func TestSelectionToolSkipsReplyWhenHostCannotQuote(t *testing.T) {
	r := newSelRig(t, selMsgText)
	r.tt = ui.NewTester(func(c *ui.Context) {
		host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Selection: &r.bar}
		m := &Msg{ID: "m1", Sender: "她", Text: selMsgText, Blocks: segview.TextBlocks(selMsgText)}
		ui.Column(c).Size(720, 480).Padding(24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 480)
	r.tt.Frame()
	r.dragAcross(t, 0.2, 0.5)
	r.settle()
	if r.hasBar() {
		t.Fatal("宿主没给 OnReply 时不该摆回复")
	}
	if _, ok := r.tt.Find("复制"); !ok {
		t.Fatal("复制仍然该有")
	}
}

func TestSelectedStringJoinsPartsAndRestoresFaces(t *testing.T) {
	faces := []inlineFace{{at: 1, copy: "[微笑]"}}
	parts := []ui.SelectedText{{Text: "我￼你", Start: 0, End: 3}, {Text: "下一段不选", Start: 0, End: 0}, {Text: "第三段结尾", Start: 3, End: 5}}
	if got := selectedString(parts, faces); got != "我[微笑]你\n结尾" {
		t.Fatalf("拼出来的选区文字不对：%q", got)
	}
	if got := selectedString([]ui.SelectedText{{Text: "  \n ", Start: 0, End: 4}}, nil); got != "" {
		t.Fatalf("只有空白不算选中：%q", got)
	}
	if got := selectedString([]ui.SelectedText{{Text: "短", Start: -3, End: 99}}, nil); got != "短" {
		t.Fatalf("越界的偏移该夹在文字范围内：%q", got)
	}
}
