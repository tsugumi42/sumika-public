package tools

// read 的 PDF 路线（规格-读文件和文档第三节）。引擎由 internal/app 注入：
// PDFium 的 WASM 有 5.8MB，几十个导入 tools 的测试二进制不该带上它。

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync/atomic"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/media"
)

const (
	pdfMaxFileBytes = 512 << 20 // 文件上限：按需读，内存不随文件变大
	pdfMaxPages     = 2000
	pdfMaxTextBytes = 16 << 20
)

// PDFPageText 是一页提取出的文字。NeedsImage 为真表示这一页文字层不可用（扫描件、字体没有映射），要看图。
type PDFPageText struct {
	Text       string
	NeedsImage bool
}

// PDFExtract 是整份文件的提取结果。Pages 可能比 Total 少：到了页数、文字量或时间上限就停，Stopped 写原因。
type PDFExtract struct {
	Pages   []PDFPageText
	Total   int
	Stopped string
}

// PDFEngine 是 PDF 引擎。出错时 error 的文字就是给模型看的话（有密码、损坏、引擎起不来）。
type PDFEngine interface {
	Extract(ctx context.Context, path string) (PDFExtract, error)
	// RenderPage 把第 page 页（从 1 数）渲染成 JPEG，同时返回这一页的文字和总页数。
	RenderPage(ctx context.Context, path string, page int) (jpeg []byte, text string, total int, err error)
}

var pdfEngine atomic.Pointer[PDFEngine]

// ClearPDFEngine 只在装的还是 e 时撤掉：别的 App 实例已换了引擎就不动。
func ClearPDFEngine(e PDFEngine) {
	if p := pdfEngine.Load(); p != nil && *p == e {
		pdfEngine.CompareAndSwap(p, nil)
	}
}

// SetPDFEngine 在装配时调用；传 nil 撤掉。
func SetPDFEngine(e PDFEngine) {
	if e == nil {
		pdfEngine.Store(nil)
		return
	}
	pdfEngine.Store(&e)
}

func readPDF(ctx context.Context, abs, path string, size int64, args jsjson.Value) (string, []media.Image, error) {
	ep := pdfEngine.Load()
	if ep == nil {
		return "", nil, fmt.Errorf("%s 是 PDF 文件，但这个程序没装 PDF 引擎，读不了。", path)
	}
	eng := *ep
	if size > pdfMaxFileBytes {
		return "", nil, fmt.Errorf("%s 是 PDF 文件，%s，超过 %s 的上限，不读。", path, formatSize(int(size)), formatSize(pdfMaxFileBytes))
	}
	if pg, ok := argNum(args, "page"); ok {
		n := int(pg)
		if float64(n) != pg || n < 1 {
			return "", nil, fmt.Errorf("page 要是从 1 开始的整数，收到 %s。", jsjsonNum(pg))
		}
		return readPDFPage(ctx, eng, abs, path, n)
	}
	doc, err := eng.Extract(ctx, abs)
	if err != nil {
		return "", nil, pdfError(ctx, err, path)
	}
	text, err := windowText(pdfBody(doc, path), path, args)
	if err != nil {
		return "", nil, err
	}
	return text, nil, nil
}

func jsjsonNum(f float64) string { return jsjson.Number_(f) }

func pdfError(ctx context.Context, err error, path string) error {
	if ctx.Err() != nil {
		return errAborted
	}
	return fmt.Errorf("读不了 PDF %s：%w", path, err)
}

// pdfBody 拼出可以走行窗口的整份文字：开头一行说明，之后按页分段。
func pdfBody(doc PDFExtract, path string) string {
	var b strings.Builder
	var imagePages []int
	usable := 0
	for i, p := range doc.Pages {
		if p.NeedsImage {
			imagePages = append(imagePages, i+1)
		} else if strings.TrimSpace(p.Text) != "" {
			usable++
		}
	}
	if usable == 0 && len(doc.Pages) > 0 && len(imagePages) > 0 {
		return fmt.Sprintf("[PDF 共 %d 页，没有可用的文字层（多半是扫描件）。用 read 加 page=%d 把那一页转成图片看，一次一页。]", doc.Total, imagePages[0])
	}
	fmt.Fprintf(&b, "[PDF 共 %d 页。", doc.Total)
	if doc.Stopped != "" {
		fmt.Fprintf(&b, "%s。", doc.Stopped)
	}
	if len(imagePages) > 0 {
		fmt.Fprintf(&b, "第 %s 页没有可用的文字（扫描图或字体没有映射），用 read 加 page=N 把那一页转成图片看，一次一页。", joinInts(imagePages, 20))
	}
	b.WriteString("]\n")
	for i, p := range doc.Pages {
		fmt.Fprintf(&b, "\n--- 第 %d 页 ---\n", i+1)
		b.WriteString(p.Text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinInts(v []int, max int) string {
	var parts []string
	for i, n := range v {
		if i == max {
			parts = append(parts, fmt.Sprintf("……（共 %d 页）", len(v)))
			break
		}
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, "、")
}

func readPDFPage(ctx context.Context, eng PDFEngine, abs, path string, page int) (string, []media.Image, error) {
	jpg, text, total, err := eng.RenderPage(ctx, abs, page)
	if err != nil {
		return "", nil, pdfError(ctx, err, path)
	}
	if page > total {
		return "", nil, fmt.Errorf("%s 只有 %d 页，没有第 %d 页。", path, total, page)
	}
	head := fmt.Sprintf("[PDF 第 %d 页（共 %d 页），已转成图片附在后面；如果看不到图，就是当前模型不能看图。下面是这一页的文字层。]\n", page, total)
	if strings.TrimSpace(text) == "" {
		head += "（这一页没有文字层。）"
	} else {
		head += text
	}
	out, err := cleanWindow(head, path)
	if err != nil {
		return "", nil, err
	}
	return out, []media.Image{{DataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)}}, nil
}
