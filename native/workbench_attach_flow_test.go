package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 只有图、没有字也能发：后台收到一块，文字空、带一张图附件；发完附件清空。
func TestWorkbenchImageOnlyMessageSendsAndClears(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "存好的图.png")
	f := &fakeInputs{savePath: saved}
	r := newWbRig(t, f)
	_, data := tinyPNGFile(t, t.TempDir(), "x.png")
	r.pastePNG(t, data)
	r.until(t, "图存好", func() bool { return len(r.p.Main.Atts) == 1 && !r.p.Main.Atts[0].Pending })
	if r.p.Main.Text != "" {
		t.Fatalf("粘贴图片不该往输入框里塞字：%q", r.p.Main.Text)
	}
	if err := r.tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	r.until(t, "后台收到提交", func() bool { return len(f.submissions()) == 1 })
	got := f.submissions()[0]
	if got.Text != "" || len(got.Parts) != 1 || got.Parts[0].Text != "" || got.Parts[0].Quote != nil {
		t.Fatalf("只有图时应发一块没有字的分块：%+v", got)
	}
	if a := got.Parts[0].Attachments; len(a) != 1 || a[0].Kind != "image" || a[0].Path != saved {
		t.Fatalf("图片附件不对：%+v", a)
	}
	r.settle()
	if len(r.p.Main.Atts) != 0 || r.p.Main.Text != "" {
		t.Fatalf("发送后该清空：%+v", r.p.Main.Atts)
	}
}

// 粘贴普通文字不产生附件，也不调后台存图。
func TestWorkbenchPasteTextAddsNoAttachment(t *testing.T) {
	f := &fakeInputs{savePath: "x"}
	r := newWbRig(t, f)
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.SetClipboard("看这个 https://example.com/a?b=1")
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if r.p.Main.Text != "看这个 https://example.com/a?b=1" || len(r.p.Main.Atts) != 0 || len(f.saved) != 0 {
		t.Fatalf("纯文字照常粘贴：%q 附件 %d 存图 %d", r.p.Main.Text, len(r.p.Main.Atts), len(f.saved))
	}
}

// 引用卡、卡上的图、普通块的图一起发：分块顺序不变，图在各自的块里，后台收到的就是这个样子。
func TestWorkbenchQuoteCardAndImagesGoOutTogether(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	dir := t.TempDir()
	one, _ := tinyPNGFile(t, dir, "一.png")
	two, _ := tinyPNGFile(t, dir, "二.png")
	card := r.quote("m1", "甲")
	card.Text = "这一段的图"
	r.a.attachPaths(r.p, card, []string{one})
	r.a.attachPaths(r.p, r.p.Main, []string{two})
	r.settle()
	if err := r.tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	r.until(t, "后台收到提交", func() bool { return len(f.submissions()) == 1 })
	got := f.submissions()[0]
	if got.Text != "" || len(got.Parts) != 2 {
		t.Fatalf("提交不对：%+v", got)
	}
	q := got.Parts[0]
	if q.Quote == nil || q.Quote.Ref.Event.EventID != "m1" || q.Text != "这一段的图" || len(q.Attachments) != 1 || q.Attachments[0].Path != one || q.Attachments[0].Kind != "image" {
		t.Fatalf("引用块不对：%+v", q)
	}
	m := got.Parts[1]
	if m.Quote != nil || m.Text != "" || len(m.Attachments) != 1 || m.Attachments[0].Path != two || m.Attachments[0].Kind != "image" {
		t.Fatalf("普通块不对：%+v", m)
	}
	r.settle()
	if len(r.p.Cards) != 0 || len(r.p.Main.Atts) != 0 {
		t.Fatal("发送后卡和附件该清空")
	}
}

// 拖进 17 张图：第 17 张被拒，输入区上方说明，已挂的 16 张不受影响；删掉一张后又能加。
func TestWorkbenchDropBeyondSixteenImagesShowsNotice(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	dir := t.TempDir()
	var paths []string
	for i := range chatui.MaxImages + 1 {
		p, _ := tinyPNGFile(t, dir, fmt.Sprintf("图%02d.png", i))
		paths = append(paths, p)
	}
	if !r.tt.DropFiles(450, 300, paths...) {
		t.Fatal("窗口没有接下拖进来的图")
	}
	r.settle()
	if len(r.p.Main.Atts) != chatui.MaxImages || !strings.Contains(r.p.Notice, "最多 16 张图片") {
		t.Fatalf("该挂 16 张并说明：%d %q", len(r.p.Main.Atts), r.p.Notice)
	}
	if !r.tt.HasText(r.p.Notice) {
		t.Fatalf("提示该画在输入区上方：%v", r.tt.Texts())
	}
	if err := r.tt.Click("移除附件 图00.png"); err != nil {
		t.Fatal(err)
	}
	r.a.attachPaths(r.p, nil, paths[chatui.MaxImages:])
	r.settle()
	if len(r.p.Main.Atts) != chatui.MaxImages || r.p.Main.Atts[len(r.p.Main.Atts)-1].Name != fmt.Sprintf("图%02d.png", chatui.MaxImages) {
		t.Fatalf("删掉一张后第 17 张该能加上：%d", len(r.p.Main.Atts))
	}
}

// 两个以上附件时点"全部移除"：只清这一块。
func TestWorkbenchClearAllAttachments(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	dir := t.TempDir()
	one, _ := tinyPNGFile(t, dir, "一.png")
	two, _ := tinyPNGFile(t, dir, "二.png")
	r.a.attachPaths(r.p, nil, []string{one, two, dir})
	r.settle()
	if len(r.p.Main.Atts) != 3 {
		t.Fatalf("先挂上三个：%d", len(r.p.Main.Atts))
	}
	if err := r.tt.Click("移除这一块的全部附件"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.p.Main.Atts) != 0 {
		t.Fatalf("全部移除后该清空：%d", len(r.p.Main.Atts))
	}
	if _, err := os.Stat(one); err != nil {
		t.Fatal("移除附件不该动原文件")
	}
	if len(f.submissions()) != 0 || r.p.Sendable() {
		t.Fatal("清空后没有可发的东西")
	}
}
