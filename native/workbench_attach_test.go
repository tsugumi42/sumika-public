package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

func tinyPNGFile(t *testing.T, dir, name string) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, b.Bytes()
}

func (r *wbRig) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		r.settle()
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("没等到：%s；界面文字 %v", what, r.tt.Texts())
}

func (r *wbRig) pastePNG(t *testing.T, data []byte) {
	t.Helper()
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, data))))
	r.tt.Key(ui.Cmd, ui.KeyV)
}

func TestWorkbenchDropFilesFoldersAndImagesOnTheFocusedBlock(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	card := r.quote("m1", "甲")
	r.settle()
	if err := r.tt.Click("回复引用 1"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	dir := t.TempDir()
	note := filepath.Join(dir, "笔记.txt")
	if err := os.WriteFile(note, []byte("编的内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "资料夹")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	img, _ := tinyPNGFile(t, dir, "图.png")
	missing := filepath.Join(dir, "不存在.txt")
	if !r.tt.DropFiles(450, 300, note, folder, img, missing) {
		t.Fatal("窗口没有接下拖进来的文件")
	}
	r.settle()
	if len(card.Atts) != 3 || len(r.p.Main.Atts) != 0 {
		t.Fatalf("拖入的文件该挂在有焦点的卡上：卡 %d，主 %d", len(card.Atts), len(r.p.Main.Atts))
	}
	for i, want := range []struct {
		kind chatui.AttKind
		name string
	}{{chatui.AttFile, "笔记.txt"}, {chatui.AttFolder, "资料夹"}, {chatui.AttImage, "图.png"}} {
		if a := card.Atts[i]; a.Kind != want.kind || a.Name != want.name || a.Path == "" {
			t.Fatalf("第 %d 个附件不对：%+v", i+1, a)
		}
	}
	if !strings.Contains(r.p.Notice, "找不到") || !strings.Contains(r.p.Notice, "不存在.txt") {
		t.Fatalf("不存在的路径该说明：%q", r.p.Notice)
	}
	r.until(t, "缩略图解出来", func() bool { return card.Atts[2].Thumb != nil })
	if card.Atts[0].Thumb != nil {
		t.Fatal("非图片不该有缩略图")
	}
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	other, _ := tinyPNGFile(t, dir, "另一张.png")
	r.tt.DropFiles(450, 300, other)
	r.settle()
	if len(r.p.Main.Atts) != 1 || len(card.Atts) != 3 {
		t.Fatalf("焦点回主输入框后拖入该挂在普通块上：主 %d 卡 %d", len(r.p.Main.Atts), len(card.Atts))
	}
}

func TestWorkbenchAttachmentsGoOutWithTheirBlocks(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	dir := t.TempDir()
	note := filepath.Join(dir, "笔记.txt")
	if err := os.WriteFile(note, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	img, _ := tinyPNGFile(t, dir, "图.png")
	card := r.quote("m1", "甲")
	r.a.attachPaths(r.p, card, []string{note, dir})
	r.a.attachPaths(r.p, r.p.Main, []string{img})
	card.Text = "看这个"
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || len(got[0].Parts) != 2 || got[0].Text != "" {
		t.Fatalf("提交不对：%+v", got)
	}
	want0 := []api.LocalAttachment{{Kind: "file", Path: note, Name: "笔记.txt"}, {Kind: "file", Path: dir, Name: filepath.Base(dir)}}
	if a := got[0].Parts[0].Attachments; len(a) != 2 || a[0] != want0[0] || a[1] != want0[1] {
		t.Fatalf("引用块的附件不对：%+v", a)
	}
	if a := got[0].Parts[1].Attachments; len(a) != 1 || a[0].Kind != "image" || a[0].Path != img || got[0].Parts[1].Text != "" {
		t.Fatalf("普通块的附件不对：%+v", got[0].Parts[1])
	}
	if len(r.p.Cards) != 0 || len(r.p.Main.Atts) != 0 {
		t.Fatal("收下后附件该清掉")
	}
}

func TestWorkbenchPasteImageSavesThenSends(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "存好的图.png")
	f := &fakeInputs{savePath: saved, saveGate: make(chan struct{})}
	r := newWbRig(t, f)
	_, data := tinyPNGFile(t, t.TempDir(), "x.png")
	r.pastePNG(t, data)
	r.settle()
	if len(r.p.Main.Atts) != 1 || !r.p.Main.Atts[0].Pending {
		t.Fatalf("粘贴后该先挂一个保存中的图：%+v", r.p.Main.Atts)
	}
	r.p.Main.Text = "看这张"
	r.a.submitComposer(f, r.key, r.p)
	r.settle()
	if len(f.submissions()) != 0 || !strings.Contains(r.p.Notice, "还在保存") {
		t.Fatalf("图还在保存时不能发：%q", r.p.Notice)
	}
	close(f.saveGate)
	r.until(t, "图存好", func() bool { return !r.p.Main.Atts[0].Pending })
	att := r.p.Main.Atts[0]
	if att.Path != saved || att.Thumb == nil || att.Kind != chatui.AttImage {
		t.Fatalf("存好后附件不对：%+v", att)
	}
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].Text != "看这张" || got[0].Parts[0].Attachments[0].Path != saved {
		t.Fatalf("提交不对：%+v", got)
	}
}

func TestWorkbenchPasteImageFailureRemovesTheChip(t *testing.T) {
	f := &fakeInputs{saveErr: os.ErrPermission}
	r := newWbRig(t, f)
	_, data := tinyPNGFile(t, t.TempDir(), "x.png")
	r.pastePNG(t, data)
	r.until(t, "保存失败撤掉图", func() bool { return len(r.p.Main.Atts) == 0 && r.p.Notice != "" })
	if !strings.Contains(r.p.Notice, "图片没存下来") {
		t.Fatalf("原因没说：%q", r.p.Notice)
	}
}

func TestWorkbenchPasteImageDeletedWhileSavingIsDropped(t *testing.T) {
	f := &fakeInputs{savePath: "x", saveGate: make(chan struct{})}
	r := newWbRig(t, f)
	_, data := tinyPNGFile(t, t.TempDir(), "x.png")
	r.pastePNG(t, data)
	r.settle()
	if err := r.tt.Click("移除附件 粘贴的图片.png"); err != nil {
		t.Fatal(err)
	}
	close(f.saveGate)
	r.until(t, "后台把图存完", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.saved) == 1 }) // 先等迟到的结果真的发出，再看它有没有被丢掉
	for range 20 {
		r.settle()
		time.Sleep(time.Millisecond)
	}
	if len(r.p.Main.Atts) != 0 || r.p.Notice != "" {
		t.Fatalf("删掉的图不该被迟到的结果找回来：%+v %q", r.p.Main.Atts, r.p.Notice)
	}
}

func TestWorkbenchDropOnConversationWithoutComposerIsIgnored(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	r.a.keys[r.a.sel] = api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: r.a.sel}
	r.settle()
	file := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	r.tt.DropFiles(450, 300, file)
	r.settle()
	if len(r.p.Main.Atts) != 0 {
		t.Fatal("QQ 会话没有输入区，拖进来的文件不该挂到本地草稿上")
	}
}

func TestWorkbenchOversizeImagesAreRefusedWithANotice(t *testing.T) {
	f := &fakeInputs{savePath: "x"}
	r := newWbRig(t, f)
	dir := t.TempDir()
	big := filepath.Join(dir, "太大.png")
	fh, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(maxImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	exact := filepath.Join(dir, "刚好.png")
	fh, _ = os.Create(exact)
	_ = fh.Truncate(maxImageBytes)
	fh.Close()
	txt := filepath.Join(dir, "大文件.txt")
	fh, _ = os.Create(txt)
	_ = fh.Truncate(maxImageBytes + 1)
	fh.Close()
	r.a.attachPaths(r.p, nil, []string{big, exact, txt})
	if len(r.p.Main.Atts) != 2 || r.p.Main.Atts[0].Name != "刚好.png" || r.p.Main.Atts[1].Name != "大文件.txt" {
		t.Fatalf("只有超过 20 MiB 的图被拒，刚好 20 MiB 的图和大的非图片文件照挂：%+v", r.p.Main.Atts)
	}
	if !strings.Contains(r.p.Notice, "太大.png") || !strings.Contains(r.p.Notice, "20 MiB") {
		t.Fatalf("被拒的图该在界面上说：%q", r.p.Notice)
	}
	r.p.Notice = ""
	r.pastePNG(t, make([]byte, maxImageBytes+1))
	r.settle()
	if len(r.p.Main.Atts) != 2 || !strings.Contains(r.p.Notice, "20 MiB") || len(f.saved) != 0 {
		t.Fatalf("粘贴的大图不该交给后台存：%d %q 存了 %d", len(r.p.Main.Atts), r.p.Notice, len(f.saved))
	}
}
