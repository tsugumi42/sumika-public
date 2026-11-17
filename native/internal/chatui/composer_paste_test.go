package chatui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 大段粘贴折叠成附件：门槛两侧、删除、点开、发送时拼进正文。文字都是编的。

func TestPasteTextThresholdIsRunesNotBytes(t *testing.T) {
	p := NewComposer()
	if p.PasteText(nil, strings.Repeat("字", FoldPasteRunes)) || len(p.Main.Atts) != 0 {
		t.Fatal("恰好 3000 个字符不该折叠（中文 3000 字是 9000 字节）")
	}
	long := strings.Repeat("字", FoldPasteRunes+1)
	if !p.PasteText(nil, long) || len(p.Main.Atts) != 1 {
		t.Fatal("超过 3000 个字符该折叠")
	}
	if a := p.Main.Atts[0]; a.Kind != AttText || a.Text != long || a.Name == "" {
		t.Fatalf("附件不对：%+v", a)
	}
	if p.Main.Text != "" || !p.Sendable() {
		t.Fatal("折叠后输入框里不放字，但应当可以发送")
	}
}

func TestPasteTextFallsBackToInsertWhenAttachFails(t *testing.T) {
	p := NewComposer()
	for i := range MaxAtts {
		p.Attach(nil, attOf(AttFile, fmt.Sprintf("f%d.txt", i)))
	}
	if p.PasteText(nil, strings.Repeat("长", FoldPasteRunes+5)) {
		t.Fatal("附件满了挂不上，不能吞掉文字：该返回 false 让输入框照常插入")
	}
	if !strings.Contains(p.Notice, "最多") {
		t.Fatalf("该说明原因：%q", p.Notice)
	}
}

func TestPasteIntoFoldsOrAppends(t *testing.T) {
	p := NewComposer()
	p.Main.Text = "前面的话"
	if !p.PasteInto(nil, "短的") || p.Main.Text != "前面的话 短的" || len(p.Main.Atts) != 0 {
		t.Fatalf("短的接在末尾：%q", p.Main.Text)
	}
	if !p.PasteInto(nil, strings.Repeat("x", FoldPasteRunes+1)) || len(p.Main.Atts) != 1 || p.Main.Text != "前面的话 短的" {
		t.Fatal("长的折叠成附件，不动输入框文字")
	}
}

func TestSendTextKeepsOrderTypedThenPastes(t *testing.T) {
	p := NewComposer()
	p.Main.Text = "  帮我看看  "
	p.Attach(nil, Att{Kind: AttText, Name: pasteName, Text: "第一段"})
	p.Attach(nil, Att{Kind: AttFile, Name: "f.txt", Path: "D:/f.txt"})
	p.Attach(nil, Att{Kind: AttText, Name: pasteName, Text: "第二段"})
	got := p.Main.SendText()
	want := "帮我看看\n\n【粘贴的内容，3 字】\n第一段\n\n【粘贴的内容，3 字】\n第二段"
	if got != want {
		t.Fatalf("正文不对：\n%q\n想要\n%q", got, want)
	}
	p.Main.Text = ""
	p.Main.Atts = p.Main.Atts[:1]
	if got := p.Main.SendText(); got != "【粘贴的内容，3 字】\n第一段" {
		t.Fatalf("没打字时只有粘贴：%q", got)
	}
}

func pasteRig(t *testing.T) *composerRig {
	t.Helper()
	r := newComposerRig(t)
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	return r
}

func TestPasteInComposerFoldsLongAndKeepsShort(t *testing.T) {
	r := pasteRig(t)
	r.tester.SetClipboard("短短一句")
	r.tester.Key(ui.Cmd, ui.KeyV)
	r.tester.Frame()
	if r.p.Main.Text != "短短一句" || len(r.p.Main.Atts) != 0 {
		t.Fatalf("低于门槛照常插入：%q %d", r.p.Main.Text, len(r.p.Main.Atts))
	}
	long := strings.Repeat("长", FoldPasteRunes+1)
	r.tester.SetClipboard(long)
	r.tester.Key(ui.Cmd, ui.KeyV)
	r.tester.Frame()
	r.tester.Frame()
	if r.p.Main.Text != "短短一句" || len(r.p.Main.Atts) != 1 || r.p.Main.Atts[0].Text != long {
		t.Fatalf("超过门槛该折叠成附件：文字 %q 附件 %d", r.p.Main.Text, len(r.p.Main.Atts))
	}
	if !r.tester.HasText("粘贴的内容 · 3001 字") {
		t.Fatalf("药丸该写名字和字数：%v", r.tester.Texts())
	}
}

func TestPasteChipRemoveAndOpen(t *testing.T) {
	r := pasteRig(t)
	var opened []Att
	r.host.OnOpenText = func(a Att) { opened = append(opened, a) }
	long := strings.Repeat("甲", FoldPasteRunes+10)
	r.p.PasteText(nil, long)
	r.tester.Frame()
	if err := r.tester.Click(fmt.Sprintf("查看粘贴的内容，%d 字", FoldPasteRunes+10)); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(opened) != 1 || opened[0].Text != long {
		t.Fatalf("点药丸该交出全文：%d", len(opened))
	}
	if err := r.tester.Click("移除附件 粘贴的内容"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(r.p.Main.Atts) != 0 || r.p.Sendable() {
		t.Fatal("删掉药丸后附件和可发送状态都该清掉")
	}
}

func TestPasteAcceptClearsFoldedText(t *testing.T) {
	p := NewComposer()
	p.PasteText(nil, strings.Repeat("a", FoldPasteRunes+1))
	p.Accept([]*Block{p.Main})
	if len(p.Main.Atts) != 0 || p.Sendable() {
		t.Fatal("发送后折叠的粘贴也该清掉")
	}
}

func TestPreviewTextKeepsPlaceholderNotBody(t *testing.T) {
	p := NewComposer()
	p.Main.Text = " 看看 "
	p.Attach(nil, Att{Kind: AttText, Name: pasteName, Text: "第一段"})
	p.Attach(nil, Att{Kind: AttFile, Name: "f.txt", Path: "D:/f.txt"})
	if got, want := p.Main.PreviewText(), "看看\n\n【粘贴的内容，3 字】"; got != want {
		t.Fatalf("预览只留占位：%q", got)
	}
	p.Main.Text = ""
	if got := p.Main.PreviewText(); got != "【粘贴的内容，3 字】" {
		t.Fatalf("没打字时只有占位：%q", got)
	}
}
