package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

const wbMDText = "先说一段普通的话，里面有 **加粗** 的字。\n\n- 第一项内容在这\n- 第二项内容在那\n\n```go\nfmt.Println(\"代码块里的字\")\n```\n"

// 她的 Markdown 回复里拖选普通段落、列表项、代码块，都弹出选区条，点“回复”后输入框多一张引用卡，
// 引的是选中的渲染后文字；一条消息可以连着引多段。
func TestWorkbenchSelectionReplyInMarkdown(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	r.a.msgs[0].text, r.a.msgs[0].md = wbMDText, true
	r.a.msgs[0].blocks = segview.TextBlocks(wbMDText)
	r.a.list = r.a.newList()
	r.settle()
	for i, want := range []struct{ find, quote string }{
		{"先说一段普通的话", "先说一段普通的话，里面有 加粗 的字。"},
		{"第一项内容在这", "第一项内容在这"},
		{"代码块里的字", "fmt.Println(\"代码块里的字\")"},
	} {
		var text string
		for _, x := range r.tt.Texts() {
			if strings.Contains(x, want.find) {
				text = x
				break
			}
		}
		if text == "" {
			t.Fatalf("找不到 %q：%v", want.find, r.tt.Texts())
		}
		r.selectText(t, text, 0.005, 0.995)
		if _, ok := r.tt.Find("回复"); !ok {
			t.Fatalf("%q：没有回复工具条：%v；选区 %+v", want.find, r.tt.Texts(), r.a.selBar.Current())
		}
		if err := r.tt.ClickWith(ui.Alt, "回复"); err != nil { // Alt：只加卡，不抢焦点，好连着引
			t.Fatal(err)
		}
		r.settle()
		if len(r.p.Cards) != i+1 {
			t.Fatalf("%q：应当有 %d 张卡：%d", want.find, i+1, len(r.p.Cards))
		}
		if q := r.p.Cards[i].Quote; q.MessageID != "m1" || q.Text != want.quote {
			t.Fatalf("%q：卡上引的不对：%+v", want.find, q)
		}
	}
}
