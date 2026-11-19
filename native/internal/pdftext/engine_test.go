package pdftext

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	sharedMu sync.Mutex
	shared   *Engine
)

// 所有测试共用一个引擎：WASM 编译只做一次。
func testEngine(t *testing.T) *Engine {
	t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		shared = New(filepath.Join(os.TempDir(), "sumika-pdftext-test-cache"))
	}
	return shared
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared != nil {
		shared.Close()
	}
	os.Exit(code)
}

func writePDF(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractShapes(t *testing.T) {
	e := testEngine(t)
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 3)
	cases := []struct {
		name      string
		data      []byte
		pages     int
		contains  []string
		needImage []bool
	}{
		{"英文明文", helloPDF(false, "Hello PDF world\nSecond line of text here"), 1, []string{"Hello PDF world", "Second line of text here"}, []bool{false}},
		{"Flate 压缩", helloPDF(true, "Compressed stream hello "+long), 1, []string{"Compressed stream hello", "lazy dog"}, []bool{false}},
		{"多页", helloPDF(true, "Page one body "+long, "Page two body "+long, "Page three body "+long), 3, []string{"Page one body", "Page two body", "Page three body"}, []bool{false, false, false}},
		{"中文 CID 带 ToUnicode", chinesePDF("今天的账本：龙井一百二十八斤", "第二行也是中文，普洱六十四斤。"), 1, []string{"今天的账本", "普洱六十四斤"}, []bool{false}},
		{"纯扫描页", scanPDF(2), 2, nil, []bool{true, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := e.Extract(context.Background(), writePDF(t, "a.pdf", c.data))
			if err != nil {
				t.Fatal(err)
			}
			if doc.Total != c.pages || len(doc.Pages) != c.pages {
				t.Fatalf("页数 %d/%d，想要 %d：%+v", len(doc.Pages), doc.Total, c.pages, doc)
			}
			all := ""
			for i, p := range doc.Pages {
				all += p.Text + "\n"
				if p.NeedsImage != c.needImage[i] {
					t.Errorf("第 %d 页 NeedsImage=%v，想要 %v（文字 %q）", i+1, p.NeedsImage, c.needImage[i], p.Text)
				}
			}
			for _, want := range c.contains {
				if !strings.Contains(all, want) {
					t.Errorf("缺 %q：%q", want, all)
				}
			}
			for _, bad := range []string{"\r", "\x00", "￾"} {
				if strings.Contains(all, bad) {
					t.Errorf("含 %q", bad)
				}
			}
		})
	}
}

func TestExtractMixedAndBlank(t *testing.T) {
	e := testEngine(t)
	d := newDoc()
	d.helvetica()
	d.image()
	long := strings.Repeat("Readable page text goes on and on. ", 3)
	d.addPage(textContent("F1", long), true, "")
	d.addPage(scanContent, true, "")
	d.addPage("", false, "") // 空白页：没有任何对象
	doc, err := e.Extract(context.Background(), writePDF(t, "m.pdf", d.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 3 {
		t.Fatalf("%+v", doc)
	}
	if doc.Pages[0].NeedsImage || !strings.Contains(doc.Pages[0].Text, "Readable page") {
		t.Errorf("文字页：%+v", doc.Pages[0])
	}
	if !doc.Pages[1].NeedsImage {
		t.Errorf("扫描页应该要看图：%+v", doc.Pages[1])
	}
	if doc.Pages[2].NeedsImage || strings.TrimSpace(doc.Pages[2].Text) != "" {
		t.Errorf("空白页不算要看图：%+v", doc.Pages[2])
	}
}

// 字体没有映射：ToUnicode 映射到私用区，文字不可用。
func TestExtractGarbledMapping(t *testing.T) {
	e := testEngine(t)
	d := newDoc()
	var chars []rune
	for i := 0; i < 40; i++ {
		chars = append(chars, rune(0xE000+i))
	}
	d.cidFont("F2", chars)
	var codes []rune
	for i := 1; i <= 40; i++ {
		codes = append(codes, rune(i))
	}
	d.addPage(fmt.Sprintf("BT /F2 14 Tf 72 720 Td %s Tj ET\n", hexOf(codes)), false, "")
	doc, err := e.Extract(context.Background(), writePDF(t, "g.pdf", d.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Pages[0].NeedsImage {
		t.Fatalf("乱码页应该要看图：%q", doc.Pages[0].Text)
	}
}

func TestExtractEncrypted(t *testing.T) {
	e := testEngine(t)
	build := func(user string) []byte {
		d := newDoc()
		d.helvetica()
		d.enc = &encSpec{user: user, owner: "owner-secret"}
		d.addPage(textContent("F1", "Encrypted but readable text content here"), false, "")
		return d.bytes()
	}
	t.Run("空用户密码能读", func(t *testing.T) {
		doc, err := e.Extract(context.Background(), writePDF(t, "e.pdf", build("")))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(doc.Pages[0].Text, "Encrypted but readable") {
			t.Fatalf("%q", doc.Pages[0].Text)
		}
	})
	t.Run("有用户密码说明密码", func(t *testing.T) {
		_, err := e.Extract(context.Background(), writePDF(t, "e2.pdf", build("user-secret")))
		if err == nil || !strings.Contains(err.Error(), "密码") {
			t.Fatalf("%v", err)
		}
	})
}

func TestExtractCorrupt(t *testing.T) {
	e := testEngine(t)
	good := helloPDF(true, "Some body text that is long enough to count "+strings.Repeat("x ", 30))
	for name, data := range map[string][]byte{
		"只有文件头":    good[:12],
		"中间截断":     good[:len(good)/2],
		"垃圾":       bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 500),
		"头对但内容是文本": []byte("%PDF-1.4\nthis is not really a pdf file at all\n"),
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				doc, err := e.Extract(context.Background(), writePDF(t, "c.pdf", data))
				if err == nil && len(doc.Pages) > 0 {
					t.Logf("引擎修复后读到了 %d 页", len(doc.Pages))
				}
				if err != nil && err.Error() == "" {
					t.Error("错误没有文字")
				}
			}()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("坏文件把引擎卡住了")
			}
		})
	}
	// 坏文件之后引擎仍然能用
	if _, err := e.Extract(context.Background(), writePDF(t, "ok.pdf", good)); err != nil {
		t.Fatalf("坏文件之后读好文件失败：%v", err)
	}
}

func TestRenderPage(t *testing.T) {
	e := testEngine(t)
	p := writePDF(t, "s.pdf", scanPDF(3))
	jpg, text, total, err := e.RenderPage(context.Background(), p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || strings.TrimSpace(text) != "" {
		t.Fatalf("total=%d text=%q", total, text)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if long := max(b.Dx(), b.Dy()); long != renderLongSide {
		t.Errorf("长边 %d，想要 %d", long, renderLongSide)
	}
	if b.Dx() >= b.Dy() {
		t.Errorf("A4 竖版应该是高大于宽：%v", b)
	}
	r, _, _, _ := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	if r < 0x4000 {
		t.Errorf("中心像素太黑：图像没渲染出来或背景没铺白 r=%#x", r)
	}
	t.Logf("扫描页 JPEG %d 字节，%dx%d", len(jpg), b.Dx(), b.Dy())

	// 越界：不报引擎错，total 给出来，由调用方说明
	if _, _, total, err := e.RenderPage(context.Background(), p, 9); err != nil || total != 3 {
		t.Errorf("越界：total=%d err=%v", total, err)
	}
}

// 文字页的 page：图和文字层一起给。
func TestRenderPageWithText(t *testing.T) {
	e := testEngine(t)
	p := writePDF(t, "t.pdf", helloPDF(true, "Layer text for the picture page "+strings.Repeat("y ", 20)))
	jpg, text, _, err := e.RenderPage(context.Background(), p, 1)
	if err != nil || len(jpg) == 0 || !strings.Contains(text, "Layer text") {
		t.Fatalf("%v %d %q", err, len(jpg), text)
	}
}

func TestExtractCachedAndInvalidated(t *testing.T) {
	e := New("")
	defer e.Close()
	p := writePDF(t, "c.pdf", helloPDF(false, "First version of the body text, long enough "+strings.Repeat("a ", 20)))
	d1, err := e.Extract(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	e.cmu.Lock()
	n := len(e.cache)
	e.cmu.Unlock()
	if n != 1 {
		t.Fatalf("缓存 %d 条", n)
	}
	start := time.Now()
	d2, _ := e.Extract(context.Background(), p)
	if d2.Pages[0].Text != d1.Pages[0].Text || time.Since(start) > 500*time.Millisecond {
		t.Errorf("第二次应该走缓存")
	}
	// 文件变了：缓存失效
	os.WriteFile(p, helloPDF(false, "Second version differs, and it is also long enough "+strings.Repeat("b ", 20)), 0o644)
	d3, err := e.Extract(context.Background(), p)
	if err != nil || !strings.Contains(d3.Pages[0].Text, "Second version") {
		t.Fatalf("%v %+v", err, d3)
	}
}

func TestExtractCancelled(t *testing.T) {
	e := testEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := e.Extract(ctx, writePDF(t, "x.pdf", helloPDF(false, "text")))
	if err == nil {
		t.Fatal("已取消应该报错")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("取消后没有马上返回")
	}
	// 取消之后引擎仍可用
	if _, err := e.Extract(context.Background(), writePDF(t, "y.pdf", helloPDF(false, "after cancel "+strings.Repeat("z ", 20)))); err != nil {
		t.Fatal(err)
	}
}

// 页数很多的文件在中途被取消：强杀实例，不挂住。
func TestExtractKilledMidway(t *testing.T) {
	e := testEngine(t)
	pages := make([]string, 400)
	for i := range pages {
		pages[i] = fmt.Sprintf("Page %d %s", i, strings.Repeat("filler words ", 30))
	}
	p := writePDF(t, "big.pdf", helloPDF(true, pages...))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	doc, err := e.Extract(ctx, p)
	if time.Since(start) > 15*time.Second {
		t.Fatalf("取消后 %v 才返回", time.Since(start))
	}
	t.Logf("err=%v 已提取 %d 页，耗时 %v", err, len(doc.Pages), time.Since(start))
	if _, err := e.Extract(context.Background(), p); err != nil {
		t.Fatalf("被强杀之后引擎不能用：%v", err)
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	for _, s := range []string{
		"a\r\nb\r\n\r\n\r\n\r\nc  \t\r\n", "x\x00y￾z­", "\n\n\n", "", "中文\r\n\r\n\r\n文字", "tab\tkept\tinside\x07bell",
	} {
		once := Normalize(s)
		if twice := Normalize(once); twice != once {
			t.Errorf("%q 清两遍 %q 和一遍 %q 不同", s, twice, once)
		}
		if strings.ContainsAny(once, "\r\x00￾") {
			t.Errorf("%q 没清干净：%q", s, once)
		}
	}
}

// 冷启动：没有磁盘缓存时第一次读要编译 WASM，有缓存后很快。只记录耗时，不设阈值（机器忙时会波动）。
func TestColdStartTiming(t *testing.T) {
	dir := t.TempDir()
	p := writePDF(t, "a.pdf", helloPDF(false, "cold start text, long enough to count "+strings.Repeat("c ", 20)))
	for _, round := range []string{"无缓存首次", "有缓存首次"} {
		e := New(dir)
		start := time.Now()
		if _, err := e.Extract(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s：%v", round, time.Since(start).Round(time.Millisecond))
		e.Close()
	}
}

// 冷启动比读文件的时限还慢（机器忙时编译 WASM 要几十秒）：时限从拿到实例以后才算，第一次读也要成功。
// 以前时限从调用开始算，看门狗在打开文档中途杀掉实例，报“module closed with exit_code(0)”。
func TestColdStartNotCountedInReadLimit(t *testing.T) {
	e := New(t.TempDir())
	defer e.Close()
	e.slowStart = 3 * time.Second
	e.extractLimit = 2 * time.Second
	p := writePDF(t, "a.pdf", helloPDF(false, "cold start text, long enough to count "+strings.Repeat("c ", 20)))
	start := time.Now()
	doc, err := e.Extract(context.Background(), p)
	if err != nil {
		t.Fatalf("冷启动慢于时限时第一次读失败：%v", err)
	}
	if time.Since(start) < e.slowStart || len(doc.Pages) != 1 || !strings.Contains(doc.Pages[0].Text, "cold start text") {
		t.Fatalf("结果不对：%v %+v", time.Since(start), doc)
	}
}

func TestColdStartNotCountedInRenderLimit(t *testing.T) {
	e := New(t.TempDir())
	defer e.Close()
	e.slowStart = 3 * time.Second
	e.renderLimit = 2 * time.Second
	p := writePDF(t, "a.pdf", helloPDF(false, "render after slow cold start "+strings.Repeat("c ", 20)))
	jpg, _, total, err := e.RenderPage(context.Background(), p, 1)
	if err != nil || total != 1 || len(jpg) == 0 {
		t.Fatalf("冷启动慢于时限时渲染失败：err=%v total=%d len=%d", err, total, len(jpg))
	}
}

// 调用方等不及放弃（超时、点停止）：马上返回，编译留在后台做完，下一次读直接用；中途 Close 也不留下没人关的池。
func TestColdStartCallerGivesUpThenNextReadWorks(t *testing.T) {
	e := New(t.TempDir())
	defer e.Close()
	e.slowStart = 2 * time.Second
	p := writePDF(t, "a.pdf", helloPDF(false, "give up then retry "+strings.Repeat("c ", 20)))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.Extract(ctx, p); err == nil {
		t.Fatal("调用方超时了却读成功了")
	}
	if d := time.Since(start); d > e.slowStart {
		t.Fatalf("调用方放弃以后没有马上返回：%v", d)
	}
	doc, err := e.Extract(context.Background(), p)
	if err != nil || len(doc.Pages) != 1 {
		t.Fatalf("放弃以后的下一次读失败：%v", err)
	}
}

func TestCloseDuringColdStart(t *testing.T) {
	e := New(t.TempDir())
	e.slowStart = time.Second
	p := writePDF(t, "a.pdf", helloPDF(false, "close during start "+strings.Repeat("c ", 20)))
	errc := make(chan error, 1)
	go func() { _, err := e.Extract(context.Background(), p); errc <- err }()
	time.Sleep(200 * time.Millisecond)
	e.Close()
	if err := <-errc; err == nil {
		t.Fatal("编译中途 Close，这次读应当报错而不是用上被丢弃的池")
	}
	defer e.Close()
	if _, err := e.Extract(context.Background(), p); err != nil {
		t.Fatalf("Close 以后再读应当重新建池：%v", err)
	}
}
