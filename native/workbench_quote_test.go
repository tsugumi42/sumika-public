package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// 在消息正文里拖选一段：返回正文的方框，松手后等工具条稳定。
func (r *wbRig) selectText(t *testing.T, text string, from, to float32) {
	t.Helper()
	rect, ok := r.tt.Find(text)
	if !ok {
		t.Fatalf("找不到消息正文 %q：%v", text, r.tt.Texts())
	}
	y := rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*from, y)
	r.tt.Move(rect.X+rect.W*(from+to)/2, y)
	r.tt.Move(rect.X+rect.W*to, y)
	r.tt.Release(rect.X+rect.W*to, y)
	r.tt.Frame()
	r.tt.Advance(200 * time.Millisecond)
	r.settle()
}

func TestWorkbenchSelectionReplyAddsCardWithOffsets(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	const text = "第一句在这。第二句在那。"
	r.selectText(t, text, 0.05, 0.15)
	if _, ok := r.tt.Find("回复"); !ok {
		t.Fatalf("选中文字后没有回复工具条：%v；选区 %+v 开着 %v；canQuote %v", r.tt.Texts(), r.a.selBar.Current(), r.a.selBar.Visible(), r.a.canQuote())
	}
	if err := r.tt.Click("回复"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.p.Cards) != 1 {
		t.Fatalf("点回复应当加一张卡：%d", len(r.p.Cards))
	}
	q := r.p.Cards[0].Quote
	rs := []rune(text)
	if q.MessageID != "m1" || q.Name != "编造角色" || q.Text == "" || q.End <= q.Start || string(rs[q.Start:q.End]) != q.Text {
		t.Fatalf("卡上的出处不对：%+v", q)
	}
	if !r.tt.HasText("回复 编造角色") || !r.tt.HasText(q.Text) {
		t.Fatalf("卡没画出来：%v", r.tt.Texts())
	}
	// 这张新卡拿到焦点，直接打字就是回复；再选一段按 Alt 加第二张，焦点不动
	r.tt.Type("这是我的回复")
	r.settle()
	if r.p.Cards[0].Text != "这是我的回复" {
		t.Fatalf("新卡没拿到焦点：%q", r.p.Cards[0].Text)
	}
	r.selectText(t, text, 0.22, 0.32)
	if err := r.tt.ClickWith(ui.Alt, "回复"); err != nil {
		t.Fatalf("%v；选区 %+v 开着 %v；%v", err, r.a.selBar.Current(), r.a.selBar.Visible(), r.tt.Texts())
	}
	r.settle()
	if len(r.p.Cards) != 2 {
		t.Fatalf("应当有两张卡：%d", len(r.p.Cards))
	}
	r.tt.Type("接着打")
	r.settle()
	if r.p.Cards[0].Text != "这是我的回复接着打" || r.p.Cards[1].Text != "" {
		t.Fatalf("Alt 加卡不该抢走焦点：%q %q", r.p.Cards[0].Text, r.p.Cards[1].Text)
	}
}

func TestWorkbenchSelectionHasNoReplyOutsideLocalPrivate(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	const text = "第一句在这。第二句在那。"
	for name, set := range map[string]func(){
		"演示数据": func() { r.a.demoMode = true },
		"QQ 会话": func() {
			r.a.demoMode = false
			r.a.keys[r.a.sel] = api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: r.a.sel}
		},
		"本地群聊": func() {
			r.a.keys[r.a.sel] = api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: r.a.sel}
		},
	} {
		set()
		r.settle()
		r.selectText(t, text, 0.1, 0.5)
		if _, ok := r.tt.Find("回复"); ok {
			t.Fatalf("%s 里不该摆回复", name)
		}
		if _, ok := r.tt.Find("复制"); !ok {
			t.Fatalf("%s 里复制仍然该有", name)
		}
	}
}
