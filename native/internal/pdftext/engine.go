// Package pdftext 用 PDFium 的 WebAssembly 版提取 PDF 文字、把单页渲染成图片（规格-读文件和文档第三节）。
// 不挂任何目录进沙箱，坏文件崩不了进程；每次读用新实例、用完即关。
package pdftext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	pdferrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"sumika-native/internal/tools"
)

const (
	maxPages     = 2000
	maxTextBytes = 16 << 20
	// 时限从拿到可用的实例以后才开始算：首次编译 WASM 在机器忙时可能几十秒，不能把它算进“读这份文件”的时限
	// （否则看门狗在文档打开中途杀掉实例，报“module closed with exit_code(0)”）。编译期间只受调用方自己的 ctx 约束。
	extractTimeout = 20 * time.Second
	renderTimeout  = 20 * time.Second
	memoryPages    = 32768 // 2GiB，WASM 页 64KiB
	emptyPageChars = 20    // 非空白字符少于这个数算没有文字（Hermes PDF_EMPTY_PAGE_CHARS）
	renderLongSide = 1568  // Anthropic 视觉文档建议的长边上限
	jpegQuality    = 85
	cacheEntries   = 4
	scanObjects    = 200 // 判断“页上有没有非文字对象”最多看这么多个
)

// Engine 实现 tools.PDFEngine。
type Engine struct {
	cacheDir string // 编译缓存目录，空则不落盘

	extractLimit, renderLimit time.Duration // 0 用上面的默认；测试才改
	slowStart                 time.Duration // 建池前多等一会儿，模拟机器忙时的冷启动；测试才改

	mu   sync.Mutex
	pool pdfium.Pool
	init *poolInit // 正在编译的那一次；编译不随单次调用取消，做完了留给下一次用

	cmu   sync.Mutex
	cache []cached
}

type cached struct {
	key string
	doc tools.PDFExtract
}

// poolInit 是一次后台的 WASM 编译和建池。
type poolInit struct {
	done      chan struct{}
	pool      pdfium.Pool
	err       error
	abandoned bool // 编译期间 Close 过，做完了也不要；e.mu 保护
}

// New 创建引擎；真正的 WASM 编译推迟到第一次读 PDF。cacheDir 是可再生的编译缓存，可以为空。
func New(cacheDir string) *Engine { return &Engine{cacheDir: cacheDir} }

var _ tools.PDFEngine = (*Engine)(nil)

// Close 关掉 WASM 运行时。
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.init != nil {
		e.init.abandoned = true // 还在编译的那一次做完后自己关掉池
		e.init = nil
	}
	if e.pool != nil {
		e.pool.Close()
		e.pool = nil
	}
}

// getPool 等引擎就绪。编译在后台做、不随这次调用的 ctx 取消：调用方放弃（超时、点停止）时编译照常做完，
// 下一次读直接用，不必从头再编一遍。
func (e *Engine) getPool(ctx context.Context) (pdfium.Pool, error) {
	e.mu.Lock()
	if e.pool != nil {
		p := e.pool
		e.mu.Unlock()
		return p, nil
	}
	in := e.init
	if in == nil {
		in = &poolInit{done: make(chan struct{})}
		e.init = in
		go e.build(in)
	}
	e.mu.Unlock()
	select {
	case <-in.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if in.err != nil {
		return nil, in.err
	}
	return in.pool, nil
}

func (e *Engine) build(in *poolInit) {
	defer close(in.done)
	p, err := e.newPool()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.init == in {
		e.init = nil
	}
	if err != nil {
		in.err = err
		return
	}
	if in.abandoned {
		p.Close()
		in.err = errors.New("PDF 引擎已关闭")
		return
	}
	e.pool = p
	in.pool = p
}

func (e *Engine) newPool() (pdfium.Pool, error) {
	time.Sleep(e.slowStart)
	rc := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithMemoryLimitPages(memoryPages).
		WithCloseOnContextDone(true)
	if e.cacheDir != "" {
		if err := os.MkdirAll(e.cacheDir, 0o755); err == nil {
			if cc, err := wazero.NewCompilationCacheWithDir(e.cacheDir); err == nil {
				rc = rc.WithCompilationCache(cc)
			}
		}
	}
	p, err := webassembly.Init(webassembly.Config{
		MinIdle:       0,
		MaxIdle:       1,
		MaxTotal:      2,
		FSConfig:      wazero.NewFSConfig(), // 空的：不挂任何目录
		RuntimeConfig: rc,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("PDF 引擎起不来：%w", err)
	}
	return p, nil
}

// session 是一次用完即关的实例，带取消时强杀的看门狗。
type session struct {
	inst   pdfium.Pdfium
	stop   chan struct{}
	done   chan struct{}
	killed bool // 只由看门狗写，stop 之后读
}

// open 等引擎就绪、拿到实例，然后才开始计时（limit）：返回的 ctx 带时限，调用方用它做之后的事；
// 等编译、建实例的时候只受传进来的 ctx 约束。cancel 要在用完 session 后调。
func (e *Engine) open(parent context.Context, limit time.Duration) (*session, context.Context, context.CancelFunc, error) {
	pool, err := e.getPool(parent)
	if err != nil {
		return nil, nil, nil, err
	}
	inst, err := pool.GetInstanceWithContext(parent)
	if err != nil {
		if parent.Err() != nil {
			return nil, nil, nil, parent.Err()
		}
		return nil, nil, nil, fmt.Errorf("PDF 引擎起不来：%w", err)
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	s := &session{inst: inst, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		select {
		case <-ctx.Done():
			s.killed = true
			s.inst.Kill()
		case <-s.stop:
		}
	}()
	return s, ctx, cancel, nil
}

func (e *Engine) limit(set, def time.Duration) time.Duration {
	if set > 0 {
		return set
	}
	return def
}

// finish 先停看门狗，再决定关还是已被杀，两者不并发。
func (s *session) finish() {
	close(s.stop)
	<-s.done
	if !s.killed {
		s.inst.Close()
	}
}

// openDoc 用文件读取器打开，内存不随文件变大。
func openDoc(s *session, f *os.File, size int64) (references.FPDF_DOCUMENT, int, error) {
	doc, err := s.inst.OpenDocument(&requests.OpenDocument{FileReader: f, FileReaderSize: size})
	if err != nil {
		return "", 0, openError(err)
	}
	n, err := s.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", 0, fmt.Errorf("读不出页数：%w", err)
	}
	return doc.Document, n.PageCount, nil
}

func openError(err error) error {
	switch {
	case errors.Is(err, pdferrors.ErrPassword):
		return errors.New("这份 PDF 有密码，打不开。请用户提供无密码的版本（工具不接受密码）。")
	case errors.Is(err, pdferrors.ErrFormat), errors.Is(err, pdferrors.ErrFile):
		return errors.New("文件损坏或不是有效的 PDF，打不开。")
	case errors.Is(err, pdferrors.ErrSecurity):
		return errors.New("这份 PDF 用了不支持的加密方式，打不开。请用户提供无密码的版本。")
	}
	return fmt.Errorf("打不开：%w", err)
}

func statKey(path string) (os.FileInfo, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	return fi, fmt.Sprintf("%s|%d|%d", strings.ToLower(filepath.Clean(path)), fi.Size(), fi.ModTime().UnixNano()), nil
}

// Extract 提取全部文字；同一文件（路径、大小、修改时间都没变）连续分段不重新提取。
func (e *Engine) Extract(ctx context.Context, path string) (tools.PDFExtract, error) {
	fi, key, err := statKey(path)
	if err != nil {
		return tools.PDFExtract{}, err
	}
	e.cmu.Lock()
	for _, c := range e.cache {
		if c.key == key {
			e.cmu.Unlock()
			return c.doc, nil
		}
	}
	e.cmu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return tools.PDFExtract{}, err
	}
	defer f.Close()
	s, ctx, cancel, err := e.open(ctx, e.limit(e.extractLimit, extractTimeout))
	if err != nil {
		return tools.PDFExtract{}, err
	}
	defer cancel()
	defer s.finish()
	doc, err := extractAll(ctx, s, f, fi.Size())
	if err != nil {
		if ctx.Err() != nil && len(doc.Pages) > 0 {
			// 超时但已有内容：交回已提取的部分并写明
			doc.Stopped = fmt.Sprintf("读取超时，只提取到第 %d 页，后面的用 page 逐页看", len(doc.Pages))
			return doc, nil
		}
		return tools.PDFExtract{}, err
	}
	e.cmu.Lock()
	e.cache = append(e.cache, cached{key, doc})
	if len(e.cache) > cacheEntries {
		e.cache = e.cache[1:]
	}
	e.cmu.Unlock()
	return doc, nil
}

func extractAll(ctx context.Context, s *session, f *os.File, size int64) (out tools.PDFExtract, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PDF 引擎出错：%v", r)
		}
	}()
	doc, total, err := openDoc(s, f, size)
	if err != nil {
		return out, err
	}
	out.Total = total
	bytesSoFar := 0
	for i := 0; i < total; i++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if i >= maxPages {
			out.Stopped = fmt.Sprintf("只提取了前 %d 页，后面的用 page 逐页看", maxPages)
			break
		}
		pt, err := pageText(s, doc, i)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("第 %d 页读不出：%w", i+1, err)
		}
		if bytesSoFar+len(pt.Text) > maxTextBytes {
			out.Stopped = fmt.Sprintf("文字量到了 %d MiB 上限，只提取到第 %d 页，后面的用 page 逐页看", maxTextBytes>>20, i)
			break
		}
		bytesSoFar += len(pt.Text)
		out.Pages = append(out.Pages, pt)
	}
	return out, nil
}

func pageText(s *session, doc references.FPDF_DOCUMENT, i int) (tools.PDFPageText, error) {
	page := requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: i}}
	r, err := s.inst.GetPageText(&requests.GetPageText{Page: page})
	if err != nil {
		return tools.PDFPageText{}, err
	}
	text := Normalize(r.Text)
	pt := tools.PDFPageText{Text: text}
	visible, bad := 0, 0
	for _, c := range text {
		if unicode.IsSpace(c) {
			continue
		}
		visible++
		if c == unicode.ReplacementChar || (c >= 0xE000 && c <= 0xF8FF) {
			bad++
		}
	}
	switch {
	case visible >= emptyPageChars:
		pt.NeedsImage = bad*10 >= visible*3 // 三成以上是替换符、私用区：字体没有映射
	default:
		pt.NeedsImage = hasNonTextObjects(s, page)
	}
	return pt, nil
}

// hasNonTextObjects：页上有图片、路径等非文字对象（空白页没有，不算）。
func hasNonTextObjects(s *session, page requests.Page) bool {
	c, err := s.inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: page})
	if err != nil {
		return false
	}
	for j := 0; j < c.Count && j < scanObjects; j++ {
		o, err := s.inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: page, Index: j})
		if err != nil {
			return false
		}
		t, err := s.inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: o.PageObject})
		if err != nil {
			return false
		}
		if t.Type != enums.FPDF_PAGEOBJ_TEXT {
			return true
		}
	}
	return false
}

// Normalize 统一换行、去掉 NUL、U+FFFE 这类非字符，行尾空白去掉，连续空行压成一行。
// 清两遍等于清一遍。
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 0, r == 0xFFFE, r == 0xFFFF, r == 0x00AD:
			return -1
		case r < 0x20 && r != '\n' && r != '\t':
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// RenderPage 把第 page 页（从 1 数）渲染成白底 JPEG。
func (e *Engine) RenderPage(ctx context.Context, path string, page int) (jpg []byte, text string, total int, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", 0, err
	}
	defer f.Close()
	s, ctx, cancel, err := e.open(ctx, e.limit(e.renderLimit, renderTimeout))
	if err != nil {
		return nil, "", 0, err
	}
	defer cancel()
	defer s.finish()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PDF 引擎出错：%v", r)
		}
	}()
	doc, n, err := openDoc(s, f, fi.Size())
	if err != nil {
		return nil, "", 0, err
	}
	if page < 1 || page > n {
		return nil, "", n, nil // 页码越界交给调用方说明
	}
	idx := page - 1
	pt, err := pageText(s, doc, idx)
	if err != nil {
		return nil, "", n, err
	}
	pref := requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: idx}}
	sz, err := s.inst.GetPageSize(&requests.GetPageSize{Page: pref})
	if err != nil {
		return nil, "", n, err
	}
	w, h := fitSize(sz.Width, sz.Height)
	r, err := s.inst.RenderPageInPixels(&requests.RenderPageInPixels{Page: pref, Width: w, Height: h})
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", n, ctx.Err()
		}
		return nil, "", n, fmt.Errorf("渲染不了第 %d 页：%w", page, err)
	}
	defer r.Cleanup() // 像素缓冲只在 Cleanup 之前有效：先编码再清理
	data, err := encodeWhiteJPEG(r.Result.RenderedImage)
	if err != nil {
		return nil, "", n, err
	}
	return data, pt.Text, n, nil
}

func fitSize(wpt, hpt float64) (int, int) {
	if wpt <= 0 || hpt <= 0 {
		return renderLongSide, renderLongSide
	}
	k := float64(renderLongSide) / max(wpt, hpt)
	return max(1, int(wpt*k+0.5)), max(1, int(hpt*k+0.5))
}

// encodeWhiteJPEG 先铺白底：页面可能带透明背景，直接编 JPEG 会变黑。
func encodeWhiteJPEG(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, errors.New("渲染结果是空的")
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
