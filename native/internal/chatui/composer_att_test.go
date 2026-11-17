package chatui

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func attOf(kind AttKind, name string) Att {
	return Att{Kind: kind, Name: name, Path: filepath.Join("D:/假目录", name)}
}

func TestComposerAttachGoesToTheBlockWithFocus(t *testing.T) {
	r := newComposerRig(t)
	a := r.p.AddQuote(quoted("m1", "甲"), false)
	b := r.p.AddQuote(quoted("m2", "乙"), false)
	r.tester.Frame()
	if err := r.tester.Click("回复引用 2"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if got := r.p.Active(); got != b {
		t.Fatalf("焦点在第二张卡里，附件该挂它：%+v", got)
	}
	if _, ok := r.p.Attach(nil, attOf(AttFile, "笔记.txt")); !ok || len(b.Atts) != 1 || len(a.Atts) != 0 || len(r.p.Main.Atts) != 0 {
		t.Fatal("拖入的文件该挂在有焦点的卡上")
	}
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if _, ok := r.p.Attach(nil, attOf(AttImage, "图.png")); !ok || len(r.p.Main.Atts) != 1 {
		t.Fatal("焦点回到主输入框后附件该挂在普通块上")
	}
	if _, ok := r.p.Attach(a, attOf(AttFolder, "资料")); !ok || len(a.Atts) != 1 {
		t.Fatal("指定了块就挂在指定的块上，粘贴就是这样")
	}
	r.p.Remove(r.p.Cards[1].ID)
	if got := r.p.Active(); got != a {
		t.Fatalf("卡被删以后附件该挂前一张卡：%+v", got)
	}
	gone := r.p.Cards[0]
	r.p.Remove(gone.ID)
	if id, ok := r.p.Attach(gone, attOf(AttFile, "迟到.txt")); !ok || id == 0 || len(r.p.Main.Atts) != 2 {
		t.Fatal("目标卡已经被删，附件该落到现在的活动块（主输入框）")
	}
}

func TestComposerAttachRules(t *testing.T) {
	p := NewComposer()
	first, _ := p.Attach(nil, attOf(AttFile, "同一个.txt"))
	if again, ok := p.Attach(nil, attOf(AttFile, "同一个.txt")); !ok || again != first || len(p.Main.Atts) != 1 {
		t.Fatal("同一块里同一路径只挂一次")
	}
	for i := 1; i < MaxAtts; i++ {
		if _, ok := p.Attach(nil, attOf(AttFile, fmt.Sprintf("f%d.txt", i))); !ok {
			t.Fatalf("第 %d 个就挂不上了", i+1)
		}
	}
	if _, ok := p.Attach(nil, attOf(AttFile, "多的.txt")); ok || len(p.Main.Atts) != MaxAtts || !strings.Contains(p.Notice, "最多") {
		t.Fatalf("满了该拒绝并说明：%d %q", len(p.Main.Atts), p.Notice)
	}
	if !p.Detach(first) || len(p.Main.Atts) != MaxAtts-1 || p.Detach(first) {
		t.Fatal("删附件：第一次成功，再删没有")
	}
	if !p.Sendable() {
		t.Fatal("只有附件、没有字也算可以发")
	}
	p.Main.Sending = true
	if _, ok := p.Attach(nil, attOf(AttFile, "发送中.txt")); ok || !strings.Contains(p.Notice, "正在发送") {
		t.Fatalf("正在发送的块不收新附件：%q", p.Notice)
	}
}

func TestComposerPendingImageBlocksSendUntilSaved(t *testing.T) {
	p := NewComposer()
	id, _ := p.Attach(nil, Att{Kind: AttImage, Name: "粘贴的图片.png", Pending: true})
	p.Main.Text = "看图"
	if p.Check() || !strings.Contains(p.Notice, "还在保存") {
		t.Fatalf("图还没存好不能发：%q", p.Notice)
	}
	p.Update(id, func(a *Att) { a.Pending, a.Path = false, "D:/库/x" })
	if !p.Check() {
		t.Fatal("存好以后该能发")
	}
	if p.Detach(id); p.Update(id, func(*Att) {}) {
		t.Fatal("附件删了，迟到的结果该被丢掉")
	}
}

func TestComposerAcceptClearsAttachmentsOfTheMainBlock(t *testing.T) {
	p := NewComposer()
	p.Attach(nil, attOf(AttFile, "a.txt"))
	sent := p.Blocks()
	p.Begin(sent)
	p.Accept(sent)
	if len(p.Main.Atts) != 0 {
		t.Fatal("发出去以后主输入框的附件该清掉")
	}
}

func TestComposerDrawsAttachmentChipsAndRemoves(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(quoted("m1", "甲"), false)
	r.p.Attach(r.p.Cards[0], attOf(AttImage, "截图.png"))
	r.p.Attach(r.p.Cards[0], attOf(AttFile, "一个名字特别特别特别特别特别特别特别特别特别特别特别特别特别长的文件.txt"))
	r.p.Attach(r.p.Main, attOf(AttFolder, "项目目录"))
	r.tester.Frame()
	for _, want := range []string{"项目目录"} {
		if !r.tester.HasText(want) {
			t.Fatalf("附件条缺 %q：%v", want, r.tester.Texts())
		}
	}
	for _, label := range []string{"图片：截图.png", "移除附件 截图.png", "移除附件 项目目录"} {
		if rect, ok := r.tester.Find(label); !ok || rect.W < 24 || rect.H < 24 {
			t.Fatalf("%s 不可达或小于 24：%+v %v", label, rect, ok)
		}
	}
	if rect, ok := r.tester.Find("移除附件 一个名字特别特别特别特别特别特别特别特别特别特别特别特别特别长的文件.txt"); !ok || rect.X+rect.W > 640 {
		t.Fatalf("长文件名把删除键挤出了窗口：%+v %v", rect, ok)
	}
	if err := r.tester.Click("移除附件 项目目录"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(r.p.Main.Atts) != 0 || len(r.p.Cards[0].Atts) != 2 {
		t.Fatal("点删除没删对那一个")
	}
}

func clipboardFiles(paths ...string) transfer.Data {
	var uris []string
	for _, p := range paths {
		uris = append(uris, (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(p)}).String())
	}
	return transfer.New(transfer.NewItem(transfer.Bytes(transfer.FileList, []byte(strings.Join(uris, "\r\n")))))
}

func TestComposerPasteImageAndFilesLandOnTheFocusedBlock(t *testing.T) {
	r := newComposerRig(t)
	var gotBlock *Block
	var gotPNG []byte
	var gotFiles []string
	r.host.OnPasteImage = func(b *Block, png []byte) { gotBlock, gotPNG = b, png }
	r.host.OnFiles = func(b *Block, paths []string) { gotBlock, gotFiles = b, paths }
	card := r.p.AddQuote(quoted("m1", "甲"), false)
	r.tester.Frame()
	if err := r.tester.Click("回复引用 1"); err != nil {
		t.Fatal(err)
	}
	r.tester.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, []byte("\x89PNG假图")))))
	r.tester.Key(ui.Cmd, ui.KeyV)
	r.tester.Frame()
	if gotBlock != card || string(gotPNG) != "\x89PNG假图" {
		t.Fatalf("粘贴的图该交给有焦点的那张卡：%v %q", gotBlock == card, gotPNG)
	}
	if card.Text != "" {
		t.Fatalf("图片粘贴不该往输入框里塞字：%q", card.Text)
	}
	dir := t.TempDir()
	one, two := filepath.Join(dir, "一.txt"), filepath.Join(dir, "二.txt")
	r.tester.SetClipboardData(clipboardFiles(one, two))
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Key(ui.Cmd, ui.KeyV)
	r.tester.Frame()
	if gotBlock != r.p.Main || len(gotFiles) != 2 {
		t.Fatalf("粘贴的文件该交给主输入框：%v %v", gotBlock == r.p.Main, gotFiles)
	}
}

func TestComposerPasteTextAndLinksStayPlainText(t *testing.T) {
	r := newComposerRig(t)
	called := false
	r.host.OnPasteImage = func(*Block, []byte) { called = true }
	r.host.OnFiles = func(*Block, []string) { called = true }
	r.tester.Frame()
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.SetClipboard("看这个 https://example.com/a?b=1")
	r.tester.Key(ui.Cmd, ui.KeyV)
	r.tester.Frame()
	if called || r.p.Main.Text != "看这个 https://example.com/a?b=1" {
		t.Fatalf("链接该当普通文字放进去：%q 误调 %v", r.p.Main.Text, called)
	}
}

func TestComposerDropHotShowsWhereTheFilesWillGo(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(quoted("m1", "甲"), true)
	r.tester.Frame()
	r.tester.Frame()
	r.host.DropHot = true
	r.tester.Frame()
	if !r.tester.HasText("松手加到引用 1") {
		t.Fatalf("没写明松手加到哪：%v", r.tester.Texts())
	}
	r.host.DropHot = false
	r.tester.Frame()
	if r.tester.HasText("松手加到引用 1") {
		t.Fatal("拖放结束后提示该消失")
	}
}

func TestComposerImagesAreCappedPerMessageAcrossBlocks(t *testing.T) {
	p := NewComposer()
	card := p.AddQuote(quoted("m1", "甲"), false)
	for i := range MaxImages {
		to := p.Main
		if i%2 == 1 {
			to = card
		}
		if _, ok := p.Attach(to, Att{Kind: AttImage, Name: fmt.Sprintf("图%d.png", i), Path: fmt.Sprintf("D:/图%d.png", i)}); !ok {
			t.Fatalf("第 %d 张就挂不上了：%q", i+1, p.Notice)
		}
	}
	if _, ok := p.Attach(card, Att{Kind: AttImage, Name: "多的.png", Path: "D:/多的.png"}); ok || !strings.Contains(p.Notice, "最多 16 张图片") {
		t.Fatalf("各块合计满 16 张后该拒绝并说明：%q", p.Notice)
	}
	if len(p.Main.Atts)+len(card.Atts) != MaxImages {
		t.Fatalf("被拒的图不该留下：%d", len(p.Main.Atts)+len(card.Atts))
	}
	if _, ok := p.Attach(card, Att{Kind: AttFile, Name: "文档.txt", Path: "D:/文档.txt"}); !ok {
		t.Fatal("图满了，非图片文件仍可挂")
	}
	if !p.Detach(p.Main.Atts[0].ID) {
		t.Fatal("删图失败")
	}
	if _, ok := p.Attach(card, Att{Kind: AttImage, Name: "补.png", Path: "D:/补.png"}); !ok {
		t.Fatal("删掉一张后又能挂了")
	}
}

func TestComposerClearAllRemovesOneBlocksAttachments(t *testing.T) {
	r := newComposerRig(t)
	card := r.p.AddQuote(quoted("m1", "甲"), false)
	r.p.Attach(card, attOf(AttImage, "一.png"))
	r.p.Attach(card, attOf(AttFile, "二.txt"))
	r.p.Attach(r.p.Main, attOf(AttFolder, "目录"))
	r.tester.Frame()
	rect, ok := r.tester.Find("移除这一块的全部附件")
	if !ok || rect.H < 24 {
		t.Fatalf("两个以上附件的块该有全部移除键，且不小于 24：%+v %v", rect, ok)
	}
	n := 0
	for _, x := range r.tester.Texts() {
		if x == "全部移除" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("只有一个附件的块不摆全部移除键：共 %d 个", n)
	}
	if err := r.tester.Click("移除这一块的全部附件"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(card.Atts) != 0 || len(r.p.Main.Atts) != 1 {
		t.Fatalf("全部移除只清这一块：卡 %d 主 %d", len(card.Atts), len(r.p.Main.Atts))
	}
	if r.tester.HasText("全部移除") {
		t.Fatal("清完后按钮该消失")
	}
	r.p.Attach(card, attOf(AttFile, "三.txt"))
	r.p.Attach(card, attOf(AttFile, "四.txt"))
	card.Sending = true
	if r.p.DetachAll(card) != 0 || len(card.Atts) != 2 {
		t.Fatal("发送中的块不该被清")
	}
}
