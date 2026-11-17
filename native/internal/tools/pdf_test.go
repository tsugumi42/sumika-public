package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

type fakePDF struct {
	doc    PDFExtract
	err    error
	jpg    []byte
	text   string
	total  int
	asked  []int
	called int
}

func (f *fakePDF) Extract(context.Context, string) (PDFExtract, error) {
	f.called++
	return f.doc, f.err
}
func (f *fakePDF) RenderPage(_ context.Context, _ string, page int) ([]byte, string, int, error) {
	f.asked = append(f.asked, page)
	return f.jpg, f.text, f.total, f.err
}

func withPDF(t *testing.T, e PDFEngine) {
	t.Helper()
	SetPDFEngine(e)
	t.Cleanup(func() { SetPDFEngine(nil) })
}

func pdfFile(t *testing.T, size int) (string, string) {
	wd := t.TempDir()
	b := append([]byte("%PDF-1.4\n"), make([]byte, size)...)
	os.WriteFile(filepath.Join(wd, "a.pdf"), b, 0o644)
	return wd, "a.pdf"
}

func TestReadPDFWithoutEngine(t *testing.T) {
	SetPDFEngine(nil)
	wd, name := pdfFile(t, 10)
	r := runTool(t, wd, "read", map[string]any{"path": name})
	if !r.IsError || !strings.Contains(r.Text, "没装 PDF 引擎") {
		t.Fatalf("%+v", r)
	}
}

func TestReadPDFText(t *testing.T) {
	f := &fakePDF{doc: PDFExtract{Total: 2, Pages: []PDFPageText{{Text: "第一页的字"}, {Text: "第二页的字"}}}}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	r := runTool(t, wd, "read", map[string]any{"path": name})
	if r.IsError || !strings.Contains(r.Text, "PDF 共 2 页") || !strings.Contains(r.Text, "--- 第 2 页 ---") || !strings.Contains(r.Text, "第二页的字") {
		t.Fatalf("%+v", r)
	}
	// 走行窗口：limit 生效、续读提示
	r = runTool(t, wd, "read", map[string]any{"path": name, "limit": 2})
	if !strings.Contains(r.Text, "more lines in file. Use offset=3") {
		t.Fatalf("PDF 文字没有走行窗口：%q", r.Text)
	}
}

func TestReadPDFScannedSaysSo(t *testing.T) {
	f := &fakePDF{doc: PDFExtract{Total: 3, Pages: []PDFPageText{{NeedsImage: true}, {NeedsImage: true}, {NeedsImage: true}}}}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	r := runTool(t, wd, "read", map[string]any{"path": name})
	if r.IsError || !strings.Contains(r.Text, "没有可用的文字层") || !strings.Contains(r.Text, "page=1") || strings.TrimSpace(r.Text) == "" {
		t.Fatalf("扫描件要明说并给出看图办法：%+v", r)
	}
}

func TestReadPDFMixedPutsNoticeFirst(t *testing.T) {
	f := &fakePDF{doc: PDFExtract{Total: 3, Stopped: "只提取了前 2 页，后面的用 page 逐页看", Pages: []PDFPageText{{Text: "能读的字"}, {NeedsImage: true}}}}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	r := runTool(t, wd, "read", map[string]any{"path": name})
	first := strings.SplitN(r.Text, "\n", 2)[0]
	if !strings.Contains(first, "第 2 页没有可用的文字") || !strings.Contains(first, "只提取了前 2 页") {
		t.Fatalf("提示要放开头：%q", first)
	}
}

func TestReadPDFPage(t *testing.T) {
	f := &fakePDF{jpg: []byte("\xff\xd8fakejpeg"), text: "这一页的文字", total: 4}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	r := runTool(t, wd, "read", map[string]any{"path": name, "page": 2})
	if r.IsError || len(r.Images) != 1 || !strings.HasPrefix(r.Images[0].DataURL, "data:image/jpeg;base64,") || !strings.Contains(r.Text, "这一页的文字") || !strings.Contains(r.Text, "不能看图") {
		t.Fatalf("%+v", r)
	}
	if len(f.asked) != 1 || f.asked[0] != 2 {
		t.Fatalf("%v", f.asked)
	}
	for _, bad := range []any{0, -1, 1.5} {
		if r := runTool(t, wd, "read", map[string]any{"path": name, "page": bad}); !r.IsError || !strings.Contains(r.Text, "page 要是从 1 开始的整数") {
			t.Errorf("page=%v：%+v", bad, r)
		}
	}
	if r := runTool(t, wd, "read", map[string]any{"path": name, "page": 5}); !r.IsError || !strings.Contains(r.Text, "只有 4 页") {
		t.Errorf("越界：%+v", r)
	}
	// 没有文字层的页
	f.text = ""
	if r := runTool(t, wd, "read", map[string]any{"path": name, "page": 1}); !strings.Contains(r.Text, "没有文字层") || len(r.Images) != 1 {
		t.Errorf("%+v", r)
	}
}

func TestReadPDFErrorsAreSpoken(t *testing.T) {
	f := &fakePDF{err: errors.New("这份 PDF 有密码，打不开。")}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	for _, args := range []map[string]any{{"path": name}, {"path": name, "page": 1}} {
		if r := runTool(t, wd, "read", args); !r.IsError || !strings.Contains(r.Text, "有密码") || !strings.Contains(r.Text, name) {
			t.Errorf("%+v", r)
		}
	}
}

func TestReadPDFTooBig(t *testing.T) {
	f := &fakePDF{}
	withPDF(t, f)
	wd := t.TempDir()
	p := filepath.Join(wd, "huge.pdf")
	fh, _ := os.Create(p)
	fh.Write([]byte("%PDF-1.7\n"))
	fh.Truncate(pdfMaxFileBytes + 1) // 稀疏文件，不真占磁盘
	fh.Close()
	r := runTool(t, wd, "read", map[string]any{"path": "huge.pdf"})
	if !r.IsError || !strings.Contains(r.Text, "超过") || f.called != 0 {
		t.Fatalf("%+v called=%d", r, f.called)
	}
}

// 正文里提到 %PDF- 的文本文件不是 PDF；page 只给 PDF 用。
func TestReadNotPDF(t *testing.T) {
	f := &fakePDF{}
	withPDF(t, f)
	wd := t.TempDir()
	os.WriteFile(filepath.Join(wd, "note.go"), []byte("// 文件头是 %PDF- 的才是 PDF\npackage x\n"), 0o644)
	r := runTool(t, wd, "read", map[string]any{"path": "note.go"})
	if r.IsError || !strings.Contains(r.Text, "package x") || f.called != 0 {
		t.Fatalf("%+v", r)
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "note.go", "page": 1}); !r.IsError || !strings.Contains(r.Text, "page 只用于 PDF") {
		t.Fatalf("%+v", r)
	}
}

// 头部有杂字节（规范允许前面最多 1KB）的 PDF 仍认。
func TestReadPDFWithLeadingJunk(t *testing.T) {
	f := &fakePDF{doc: PDFExtract{Total: 1, Pages: []PDFPageText{{Text: "有内容"}}}}
	withPDF(t, f)
	wd := t.TempDir()
	os.WriteFile(filepath.Join(wd, "j.pdf"), append([]byte{0, 1, 2, 0xff, 0xfe}, []byte("%PDF-1.4\nrest")...), 0o644)
	if r := runTool(t, wd, "read", map[string]any{"path": "j.pdf"}); r.IsError || !strings.Contains(r.Text, "有内容") {
		t.Fatalf("%+v", r)
	}
}

func TestReadPDFCancelled(t *testing.T) {
	f := &fakePDF{err: context.Canceled}
	withPDF(t, f)
	wd, name := pdfFile(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = wd
	_ = name
	_, _, err := readPDF(ctx, filepath.Join(wd, name), name, 10, jsjson.Value{})
	if err == nil {
		t.Fatal("取消应该报错")
	}
}
