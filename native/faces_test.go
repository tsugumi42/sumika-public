package main

// 第四、五步的测试：T-face、T-empty-faces、T-fail（小黄脸部分）、T-face-size、T-dpi、T-copy、T-close，
// 以及实际文字对象框中的图片位置和复制。
// 数据全是编的：名字表、PNG 都是测试里现造的；不连 7720，不读 data/。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// ───────────── 测试用的假网络和假的界面线程 ─────────────

type fakeNet struct {
	mu       sync.Mutex
	tables   []map[string]faceInfo // 依次返回；用完了重复最后一个
	tableErr error
	tableN   int
	png      map[string][]byte // 有的返回图；没有的 404
	pngErr   map[string]error  // 这些 id 返回网络错误
	pngN     map[string]int
	gate     chan struct{} // 非 nil：取图先等它，或者 ctx 取消
	ignore   bool          // 取图时不看 ctx（模拟取到了才发现窗口关了）
}

func (n *fakeNet) fetchTable(ctx context.Context) (map[string]faceInfo, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tableN++
	if n.tableErr != nil {
		return nil, n.tableErr
	}
	if len(n.tables) == 0 {
		return nil, nil
	}
	i := min(n.tableN-1, len(n.tables)-1)
	return n.tables[i], nil
}

func (n *fakeNet) fetchPNG(ctx context.Context, id string) ([]byte, error) {
	n.mu.Lock()
	if n.pngN == nil {
		n.pngN = map[string]int{}
	}
	n.pngN[id]++
	gate, ignore := n.gate, n.ignore
	n.mu.Unlock()
	if gate != nil {
		if ignore {
			<-gate
		} else {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	// 放行以后再读图：测试在放行前删掉、换掉的图才算数（先读的话，goroutine 跑得早晚决定拿到哪一版）
	n.mu.Lock()
	b, e := n.png[id], n.pngErr[id]
	n.mu.Unlock()
	if e != nil {
		return nil, e
	}
	if b == nil {
		return nil, errNotFound
	}
	return b, nil
}

func (n *fakeNet) pngCalls(id string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.pngN[id]
}

// uiQueue 假的界面线程：post 进来的先存着，测试里 drain 才执行
type uiQueue struct {
	mu   sync.Mutex
	fns  []func()
	wake chan struct{} // 有新活排进来时通知一下（容量 1），让等待的一方不用靠睡眠轮询
}

func (q *uiQueue) post(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	w := q.wake
	q.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

// waitPost 等到有新活排进来，或最多 d；只用来替代“睡一毫秒再看”，不改变等什么
func (q *uiQueue) waitPost(d time.Duration) {
	q.mu.Lock()
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	w, n := q.wake, len(q.fns)
	q.mu.Unlock()
	if n > 0 {
		return
	}
	select {
	case <-w:
	case <-time.After(d):
	}
}

func (q *uiQueue) drain() int {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	return len(fns)
}

func (q *uiQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.fns)
}

// facePNG 一个纯色的 PNG
func facePNG(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

var (
	red  = color.RGBA{255, 0, 0, 255}
	blue = color.RGBA{0, 0, 255, 255}
)

var testTable = map[string]faceInfo{
	"14":  {Name: "微笑", Kind: "classic"},
	"21":  {Name: "可爱", Kind: "classic"},
	"333": {Name: "骰子", Kind: "super"},
	"900": {Name: "笑哭", Kind: "emoji", Char: "😂"},
}

// faceApp 一个带假网络的 app：名字表、图都走 net，回界面线程的活进 q
func faceApp(t *testing.T, n *fakeNet, msgs ...*msg) (*app, *uiQueue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := chatApp(msgs, false)
	q := &uiQueue{}
	a.ctx, a.cancel, a.postFn = ctx, cancel, q.post
	f := a.fs()
	f.fetchTable, f.fetchPNG, f.retry = n.fetchTable, n.fetchPNG, time.Millisecond
	return a, q
}

// faceMsg 一条带小黄脸的 QQ 消息：mine 为真是她说的（右边、自己的气泡），否则是群友（左边，有边框）。
// parts 里 "#14" 是编号 14 的脸，其余是文字
func faceMsg(mine bool, id string, parts ...string) *msg {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString(",")
		}
		if id, ok := strings.CutPrefix(p, "#"); ok {
			fmt.Fprintf(&sb, `{"type":"face","id":%q}`, id)
		} else {
			fmt.Fprintf(&sb, `{"type":"text","text":%q}`, p)
		}
	}
	seg := `{"segs":[` + sb.String() + `]}`
	e := event{ID: id, ActorKind: "human", ActorID: "qq:42", SegmentsJSON: &seg}
	if mine {
		e.ActorKind, e.ActorID = "character", "aoi"
	}
	m := msgFrom("qq:private:1", e, map[string]person{"qq:42": {Name: "豆沙包"}})
	m.md = false // 她的话在 QQ 里也按行内块画（Markdown 路径装不下行内框）
	return m
}

func bubbleView(a *app, m *msg) func(c *ui.Context) {
	return func(c *ui.Context) {
		page(c, func() {
			a.envFor(c)
			a.messageBody(c, m, true, true, time.Now())
		})
	}
}

// ───────────── T-face：一个脸怎么显示 ─────────────

func TestResolveFace(t *testing.T) {
	t.Parallel()
	missing := func(id string) int { return faceMissing }
	cases := []struct {
		name  string
		ref   segview.FaceRef
		table map[string]faceInfo
		state func(string) int
		want  faceRes
	}{
		{"编号为空", segview.FaceRef{}, testTable, nil, faceRes{faceAsText, "[表情]", "[表情]"}},
		{"经典：命中有图", segview.FaceRef{ID: "14"}, testTable, nil, faceRes{Kind: faceAsImage, Copy: "[微笑]"}},
		{"超级表情：命中有图", segview.FaceRef{ID: "333", Kind: "superface"}, testTable, nil, faceRes{Kind: faceAsImage, Copy: "[骰子]"}},
		{"emoji 类：用字符", segview.FaceRef{ID: "900"}, testTable, nil, faceRes{faceAsChar, "😂", "😂"}},
		{"emoji 类没有字符：文字名字", segview.FaceRef{ID: "901"}, map[string]faceInfo{"901": {Name: "怪", Kind: "emoji"}}, nil, faceRes{faceAsText, "[怪]", "[怪]"}},
		{"命中但图确认没有：退成文字名字", segview.FaceRef{ID: "14"}, testTable, missing, faceRes{faceAsText, "[微笑]", "[微笑]"}},
		{"命中但没有名字：兜底字", segview.FaceRef{ID: "5"}, map[string]faceInfo{"5": {Kind: "classic"}}, nil, faceRes{Kind: faceAsImage, Copy: "[表情5]"}},
		{"名字表没有的数字：先试图，复制按编号", segview.FaceRef{ID: "99"}, testTable, nil, faceRes{Kind: faceAsImage, Copy: "[表情99]"}},
		{"名字表没有的超级表情：兜底名字带“超级”", segview.FaceRef{ID: "99", Kind: "superface"}, testTable, nil, faceRes{Kind: faceAsImage, Copy: "[超级表情99]"}},
		{"名字表没有、图也确认没有：文字", segview.FaceRef{ID: "99"}, testTable, missing, faceRes{faceAsText, "[表情99]", "[表情99]"}},
		{"超级表情图确认没有：文字", segview.FaceRef{ID: "99", Kind: "superface"}, testTable, missing, faceRes{faceAsText, "[超级表情99]", "[超级表情99]"}},
		{"不是数字：文字", segview.FaceRef{ID: "abc"}, testTable, nil, faceRes{faceAsText, "[表情abc]", "[表情abc]"}},
		{"七位数字：接口不收，文字", segview.FaceRef{ID: "1234567"}, testTable, nil, faceRes{faceAsText, "[表情1234567]", "[表情1234567]"}},
		{"名字表没就绪（nil）：和没命中一样先试图", segview.FaceRef{ID: "14"}, nil, nil, faceRes{Kind: faceAsImage, Copy: "[表情14]"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveFace(c.ref, c.table, c.state); got != c.want {
				t.Fatalf("得到 %+v，想要 %+v", got, c.want)
			}
		})
	}
}

// 名字表没就绪时一律先当图试；表到了以后同一个脸的复制文字从编号变成名字（T-empty-faces 的另一半）
func TestEmptyTableRetry(t *testing.T) {
	t.Parallel()
	n := &fakeNet{tables: []map[string]faceInfo{{}, nil, testTable}}
	a, q := faceApp(t, n)
	f := a.fs()
	f.start()
	f.wg.Wait() // 第一次 {}、第二次 nil、第三次非空，之后不再取
	if got := resolveFace(segview.FaceRef{ID: "14"}, f.table, f.state); got.Copy != "[表情14]" {
		t.Fatalf("表还没进来，复制文字应是编号：%+v", got)
	}
	ver0 := f.ver
	q.drain()
	n.mu.Lock()
	calls := n.tableN
	n.mu.Unlock()
	if calls != 3 || len(f.table) != len(testTable) || f.ver != ver0+1 {
		t.Fatalf("取了 %d 次（应 3），表 %d 项，版本 %d→%d", calls, len(f.table), ver0, f.ver)
	}
	if got := resolveFace(segview.FaceRef{ID: "14"}, f.table, f.state); got.Copy != "[微笑]" {
		t.Fatalf("表进来以后应是名字：%+v", got)
	}
}

// 取不到（后台没起来）和返回 {} 一样隔一会儿再取
func TestTableErrorRetry(t *testing.T) {
	t.Parallel()
	n := &fakeNet{tableErr: errors.New("连不上")}
	a, q := faceApp(t, n)
	f := a.fs()
	// 第三次失败后服务恢复；响应顺序由假服务控制，不用墙钟猜重试进度。
	f.fetchTable = func(ctx context.Context) (map[string]faceInfo, error) {
		table, err := n.fetchTable(ctx)
		n.mu.Lock()
		if n.tableN == 3 {
			n.tableErr, n.tables = nil, []map[string]faceInfo{testTable}
		}
		n.mu.Unlock()
		return table, err
	}
	f.start()
	f.wg.Wait()
	q.drain()
	if len(f.table) != len(testTable) {
		t.Fatalf("恢复以后没取到表：%v", f.table)
	}
}

// ───────────── T-fail（小黄脸部分）─────────────

func TestFaceFetchOnceAnd404(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(40, 40, red)}}
	m := faceMsg(false, "a", "你好", "#14", "再见", "#99")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	tt := testerOf(500, 300, bubbleView(a, m))
	for i := 0; i < 5; i++ { // 重建很多次，请求不能叠
		tt.Frame()
	}
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	if n.pngCalls("14") != 1 || n.pngCalls("99") != 1 {
		t.Fatalf("请求次数 14：%d，99：%d（都应是 1）", n.pngCalls("14"), n.pngCalls("99"))
	}
	if a.fs().peek("14") == nil {
		t.Fatal("14 的图没取到")
	}
	if a.fs().state("99") != faceMissing {
		t.Fatalf("99 应是确认没有，状态 %d", a.fs().state("99"))
	}
	// 确认没有以后框换成文字：显示 [表情99]，14 还是框
	if !tt.HasText("再见[表情99]") && !strings.Contains(strings.Join(tt.Texts(), "|"), "[表情99]") {
		t.Fatalf("404 的脸没有显示成文字：%q", tt.Texts())
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	a.fs().wg.Wait()
	if n.pngCalls("99") != 1 {
		t.Fatalf("404 以后又发了请求：%d 次", n.pngCalls("99"))
	}
	// 切会话：确认没有的清掉，用到时再取一次；有图的不重取
	a.switchTo("qq:private:2")
	tt.Frame()
	a.fs().wg.Wait()
	q.drain()
	if n.pngCalls("99") != 2 || n.pngCalls("14") != 1 {
		t.Fatalf("切会话后 99：%d 次（应 2），14：%d 次（应 1）", n.pngCalls("99"), n.pngCalls("14"))
	}
}

func TestFaceNetworkErrorIsMissingOnce(t *testing.T) {
	n := &fakeNet{pngErr: map[string]error{"21": errors.New("连接被重置")}}
	m := faceMsg(false, "a", "#21")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	tt := testerOf(500, 300, bubbleView(a, m))
	a.fs().wg.Wait()
	q.drain()
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	a.fs().wg.Wait()
	if n.pngCalls("21") != 1 || a.fs().state("21") != faceMissing {
		t.Fatalf("网络错误：请求 %d 次，状态 %d（应 1 次、missing，不自动重试）", n.pngCalls("21"), a.fs().state("21"))
	}
	if !tt.HasText("[可爱]") {
		t.Fatalf("取不到的脸应显示名字：%q", tt.Texts())
	}
}

// 坏图（取到的不是 PNG）也是确认没有，不 panic
func TestFaceBadPNG(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": []byte("not a png")}}
	m := faceMsg(false, "a", "#14")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	tt := testerOf(500, 300, bubbleView(a, m))
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	if a.fs().state("14") != faceMissing || !tt.HasText("[微笑]") {
		t.Fatalf("坏图：状态 %d，文字 %q", a.fs().state("14"), tt.Texts())
	}
}

// ───────────── T-face-size、T-dpi ─────────────

func TestFaceSize(t *testing.T) {
	t.Parallel()
	for _, size := range []float32{12, 14, 16, 20.5} {
		w, h := faceBox(size)
		if w != size*1.375 || faceSize(size) != size*1.375 {
			t.Fatalf("字号 %v：框宽 %v，脸边长 %v，应都是 %v", size, w, faceSize(size), size*1.375)
		}
		if h >= w || h <= 0 {
			t.Fatalf("字号 %v：框高 %v 应比脸矮（脸往基线下沉）", size, h)
		}
		doc := inlineOf(segview.Block{Kind: segview.Inline, Runs: []segview.Run{{Text: "好"}, {Text: "[表情14]", Face: &segview.FaceRef{ID: "14"}}}}, size, nil, nil)
		if len(doc.imgs) != 1 || doc.spans[1].ObjectWidth != size*1.375 {
			t.Fatalf("字号 %v：span 的框宽 %v", size, doc.spans[1].ObjectWidth)
		}
		objs := ui.ShapeRichText(doc.spans, ui.Font{Size: size}, 300).Objects()
		if len(objs) != 1 || objs[0].Rect.W != size*1.375 {
			t.Fatalf("字号 %v：排出来的框 %+v", size, objs)
		}
	}
}

// 屏幕缩放比不改 DIP 排版：框的 DIP 尺寸不变，画出来的像素随比例变
func TestFaceBoxIsDIPUnderScale(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red)}}
	m := faceMsg(false, "a", "你好", "#14", "世界")
	for _, scale := range []float32{1, 1.25, 2} {
		a, q := faceApp(t, n, m)
		a.fs().table = testTable
		tt := ui.NewTester(bubbleView(a, m), 500, 300)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetScale(scale)
		tt.Frame()
		a.fs().wg.Wait()
		q.drain()
		tt.Frame()
		r := redBox(tt.Image())
		wantPx := faceSize(14) * scale
		if r.Dx() < int(wantPx)-2 || r.Dx() > int(wantPx)+2 || r.Dy() < int(wantPx)-2 || r.Dy() > int(wantPx)+2 {
			t.Fatalf("缩放 %v：画出来 %dx%d 像素，应约 %.1f", scale, r.Dx(), r.Dy(), wantPx)
		}
	}
}

// redBox 图里纯红像素的外框
func redBox(img *image.RGBA) image.Rectangle {
	r := image.Rectangle{Min: image.Pt(1<<30, 1<<30)}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.R > 200 && c.G < 60 && c.B < 60 {
				r.Min.X, r.Min.Y = min(r.Min.X, x), min(r.Min.Y, y)
				r.Max.X, r.Max.Y = max(r.Max.X, x+1), max(r.Max.Y, y+1)
			}
		}
	}
	if r.Empty() {
		return image.Rectangle{}
	}
	return r
}

// ───────────── 真实对象框与绘图 ─────────────

const longFaceText = "今天天气很好，我们去公园散步，看到了很多鸫在树上唱歌，晚上想吃火锅还是烧烤，这个原型能不能用，要先看中文输入法和长列表顺不顺"

// ───────────── 图画在框里 ─────────────

// 红色的脸画在排版给出的框里；含脸的行不比没有脸的行高
func TestFaceDrawnInsideBox(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red)}}
	with := faceMsg(false, "a", "你好", "#14", "世界")
	without := faceMsg(false, "b", "你好", "世界")
	a, q := faceApp(t, n, with, without)
	a.fs().table = testTable
	var rcs []ui.Rect
	var objs [][]ui.TextObject
	drawCheck = func(rc ui.Rect, spans []ui.Span, o []ui.TextObject) {
		rcs, objs = append(rcs[:0], rc), append(objs[:0], o)
	}
	defer func() { drawCheck = nil }()
	tt := testerOf(500, 300, bubbleView(a, with))
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	px := redBox(tt.Image())
	if px.Empty() || len(objs) == 0 || len(objs[0]) != 1 {
		t.Fatalf("没画出脸：红像素框 %v，框位置 %v", px, objs)
	}
	rc, o := rcs[0], objs[0][0].Rect
	wantX, wantY := rc.X+o.X, rc.Y+o.Y
	if d := float32(px.Min.X) - wantX; d < -1 || d > 1 {
		t.Fatalf("脸的左边 %d，框在 %.1f", px.Min.X, wantX)
	}
	if d := float32(px.Min.Y) - wantY; d < -1 || d > 1 {
		t.Fatalf("脸的上边 %d，框在 %.1f", px.Min.Y, wantY)
	}
	// 行高：有脸和没脸的一行一样高（脸往基线下沉，不撑高行）
	_, lineWith := ui.ShapeRichText(inlineOf(with.blocks[0], 14, testTable, nil).spans, ui.Font{Size: 14}, 294).Size()
	_, lineWithout := ui.ShapeRichText(inlineOf(without.blocks[0], 14, testTable, nil).spans, ui.Font{Size: 14}, 294).Size()
	if lineWith != lineWithout {
		t.Fatalf("含脸的一行 %.2f 高，没有脸的 %.2f", lineWith, lineWithout)
	}
}

// 图没到时不画（框还在，行高不变），到了以后位置不跳
func TestFaceBoxStaysWhenImageArrives(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red)}, gate: make(chan struct{})}
	m := faceMsg(false, "a", "你好", "#14", "世界")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	var before, after image.Rectangle
	var rcB, rcA ui.Rect
	var oB, oA []ui.TextObject
	drawCheck = func(rc ui.Rect, spans []ui.Span, o []ui.TextObject) { rcB, oB = rc, append([]ui.TextObject(nil), o...) }
	tt := testerOf(500, 300, bubbleView(a, m))
	tt.Frame()
	before = redBox(tt.Image())
	if !before.Empty() {
		t.Fatal("图还没到就画了")
	}
	close(n.gate)
	a.fs().wg.Wait()
	q.drain()
	drawCheck = func(rc ui.Rect, spans []ui.Span, o []ui.TextObject) { rcA, oA = rc, append([]ui.TextObject(nil), o...) }
	defer func() { drawCheck = nil }()
	tt.Frame()
	after = redBox(tt.Image())
	if after.Empty() {
		t.Fatal("图到了以后没画")
	}
	if rcA != rcB || len(oA) != 1 || len(oB) != 1 || oA[0] != oB[0] {
		t.Fatalf("图到了以后框跳了：%+v %+v → %+v %+v", rcB, oB, rcA, oA)
	}
}

// ───────────── T-copy ─────────────

func TestRestoreFaces(t *testing.T) {
	t.Parallel()
	const f = "￼"
	copies := []string{"[微笑]", "[可爱]", "[骰子]"}
	cases := []struct {
		name       string
		text       string
		start, end int
		want       string
	}{
		{"整段，两个脸", "你好" + f + "世界" + f + "再见", 0, 8, "你好[微笑]世界[可爱]再见"},
		{"只选文字", "你好" + f + "世界" + f + "再见", 0, 2, "你好"},
		{"只选一个脸", "你好" + f + "世界" + f + "再见", 2, 3, "[微笑]"},
		{"选区不从头开始：前面的脸要先数过去", "你好" + f + "世界" + f + "再见", 3, 8, "世界[可爱]再见"},
		{"选区从第二个脸开始", "你好" + f + "世界" + f + "再见", 5, 8, "[可爱]再见"},
		{"连着三个脸", f + f + f, 0, 3, "[微笑][可爱][骰子]"},
		{"连着三个脸，选中间一个", f + f + f, 1, 2, "[可爱]"},
		{"脸比复制文字多：写 [表情]，不 panic", f + f + f + f, 0, 4, "[微笑][可爱][骰子][表情]"},
		{"没有脸", "纯文字", 0, 3, "纯文字"},
		{"空选区", "你好" + f, 1, 1, ""},
		{"越界的选区被夹住", "你好" + f, -3, 99, "你好[微笑]"},
		{"终点在起点前", "你好", 2, 1, ""},
		{"一个字符占两个 UTF-16 单元也按字数", "😂" + f + "好", 1, 3, "[微笑]好"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restoreFaces(c.text, c.start, c.end, copies); got != c.want {
				t.Fatalf("得到 %q，想要 %q", got, c.want)
			}
		})
	}
}

// 文字里自带的 U+FFFC 在进块时就去掉：不然数量和脸对不上，复制会错位
func TestStrayObjectCharStripped(t *testing.T) {
	t.Parallel()
	bs := segview.BlocksOf(segmentViews(displaySegs(api.DisplaySegment{Kind: "text", Text: "a￼b"}, api.DisplaySegment{Kind: "face", Ref: "14"}, api.DisplaySegment{Kind: "text", Text: "c￼d"})))
	if len(bs) != 1 || bs[0].Plain() != "ab[表情14]cd" {
		t.Fatalf("得到 %v", descr(bs))
	}
	doc := inlineOf(bs[0], 14, testTable, nil)
	n := 0
	for _, sp := range doc.spans {
		n += strings.Count(sp.Text, "￼")
	}
	if n != len(doc.imgs) || n != 1 {
		t.Fatalf("U+FFFC 有 %d 个，脸 %d 个（应各 1）", n, len(doc.imgs))
	}
	if got := segview.TextBlocks("x￼y"); len(got) != 1 || got[0].Plain() != "xy" {
		t.Fatalf("没有段的文字：%v", descr(got))
	}
}

// 脸在块两头、连着、前后有空白：块修剪不丢脸
func TestBlocksKeepFaces(t *testing.T) {
	t.Parallel()
	bs := segview.BlocksOf(segmentViews(displaySegs(api.DisplaySegment{Kind: "text", Text: " \n"}, api.DisplaySegment{Kind: "face", Ref: "14"}, api.DisplaySegment{Kind: "face", Ref: "21"}, api.DisplaySegment{Kind: "text", Text: "\n"})))
	if len(bs) != 1 || len(bs[0].Runs) != 2 || bs[0].Runs[0].Face == nil || bs[0].Runs[1].Face == nil {
		t.Fatalf("得到 %+v", bs)
	}
	if bs[0].Runs[0].Face.ID != "14" || bs[0].Runs[1].Face.ID != "21" {
		t.Fatalf("顺序错了：%+v", bs[0].Runs)
	}
}

// 她的话按 Markdown 画，装不下行内框：脸用名字表里的名字
func TestMarkdownFacesNamed(t *testing.T) {
	t.Parallel()
	b := segview.Block{Kind: segview.Inline, Runs: []segview.Run{{Text: "看"}, {Text: "[表情14]", Face: &segview.FaceRef{ID: "14"}}, {Text: "和"}, {Text: "[表情900]", Face: &segview.FaceRef{ID: "900"}}}}
	if got := plainNamed(b, testTable); got != "看[微笑]和😂" {
		t.Fatalf("得到 %q", got)
	}
	if got := plainNamed(b, nil); got != "看[表情14]和[表情900]" {
		t.Fatalf("名字表没到：%q", got)
	}
}

// 三层真的拖选 + Ctrl+C / 右键复制：脸还原成 [名字]，不能有 U+FFFC
func TestCopyFacesOnScreen(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red), "21": facePNG(64, 64, blue)}}
	m := faceMsg(false, "a", "你好", "#14", "世界", "#21", "再见")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	var rc ui.Rect
	drawCheck = func(r ui.Rect, _ []ui.Span, _ []ui.TextObject) { rc = r }
	defer func() { drawCheck = nil }()
	tt := testerOf(500, 300, bubbleView(a, m))
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	mid := rc.Y + rc.H/2
	drag := func(x0, x1 float32) {
		tt.Press(x0, mid)
		tt.Move((x0+x1)/2, mid)
		tt.Move(x1, mid)
		tt.Release(x1, mid)
		tt.Frame()
	}
	text := func() string { return clipText(tt) }
	// 整段：从左边外面拖到右边外面
	drag(rc.X-2, rc.X+rc.W+3)
	tt.Key(ui.Cmd, ui.KeyC)
	got := text()
	if got != "你好[微笑]世界[可爱]再见" {
		t.Fatalf("Ctrl+C 整段得到 %q", got)
	}
	if strings.ContainsRune(got, '￼') {
		t.Fatal("剪贴板里有 U+FFFC")
	}
	// 右键菜单的复制走同一个 CopyData
	tt.SetClipboard("")
	tt.RightClickAt(rc.X+rc.W/2, mid)
	if err := tt.ChooseMenuItem("Copy"); err != nil {
		t.Fatalf("右键菜单：%v（菜单项 %q）", err, tt.Menu())
	}
	if got := text(); got != "你好[微笑]世界[可爱]再见" {
		t.Fatalf("右键复制得到 %q", got)
	}
}

// 选区不从头开始：拖的起点在第一个脸后面，复制出来前面的脸不算
func TestCopyFacesPartialSelection(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red), "21": facePNG(64, 64, blue)}}
	m := faceMsg(false, "a", "你好", "#14", "世界", "#21", "再见")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	var rc ui.Rect
	var objs []ui.TextObject
	drawCheck = func(r ui.Rect, _ []ui.Span, o []ui.TextObject) { rc, objs = r, o }
	defer func() { drawCheck = nil }()
	tt := testerOf(500, 300, bubbleView(a, m))
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	if len(objs) != 2 {
		t.Fatalf("框 %v", objs)
	}
	mid := rc.Y + rc.H/2
	x0 := rc.X + objs[0].Rect.X + objs[0].Rect.W + 1 // 第一个脸的右边，“世”的左边
	tt.Press(x0, mid)
	tt.Move(rc.X+rc.W, mid)
	tt.Move(rc.X+rc.W+3, mid)
	tt.Release(rc.X+rc.W+3, mid)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	s := clipText(tt)
	if s != "世界[可爱]再见" {
		t.Fatalf("从第一个脸后面选到末尾得到 %q", s)
	}
}

// 一条消息里有两个带脸的段落，中间隔着灰字占位：整条复制各段还原自己的脸，段与段空一行；
// 只从第二段拖起，复制不带第一段的脸（按元素 id 找，不按出现顺序）
func TestCopyFacesTwoParagraphs(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red), "21": facePNG(64, 64, blue)}}
	m := faceMsg(false, "a", "甲")
	m.segs = displaySegs(api.DisplaySegment{Kind: "text", Text: "甲"}, api.DisplaySegment{Kind: "face", Ref: "14"}, api.DisplaySegment{Kind: "voice"}, api.DisplaySegment{Kind: "text", Text: "乙"}, api.DisplaySegment{Kind: "face", Ref: "21"})
	m.blocks = segview.BlocksOf(segmentViews(m.segs))
	if len(m.blocks) != 3 {
		t.Fatalf("块 %v", descr(m.blocks))
	}
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	tt := testerOf(500, 400, bubbleView(a, m))
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	all, ok := tt.Find("乙[可爱]")
	if !ok {
		t.Fatalf("没画出第二段：%q", tt.Texts())
	}
	first, ok := tt.Find("甲[微笑]")
	if !ok {
		t.Fatalf("没画出第一段：%q", tt.Texts())
	}
	drag := func(x0, y0, x1, y1 float32) {
		tt.Press(x0, y0)
		tt.Move((x0+x1)/2, (y0+y1)/2)
		tt.Move(x1, y1)
		tt.Release(x1, y1)
		tt.Frame()
	}
	// 整条：从第一段左上外面拖到第二段右下外面
	drag(first.X-2, first.Y-1, all.X+200, all.Y+all.H+30)
	tt.Key(ui.Cmd, ui.KeyC)
	got := clipText(tt)
	if strings.ContainsRune(got, '￼') {
		t.Fatalf("剪贴板里有 U+FFFC：%q", got)
	}
	if !strings.Contains(got, "甲[微笑]") || !strings.Contains(got, "乙[可爱]") {
		t.Fatalf("整条复制得到 %q", got)
	}
	if strings.Index(got, "[微笑]") > strings.Index(got, "[可爱]") || strings.Count(got, "[微笑]") != 1 || strings.Count(got, "[可爱]") != 1 {
		t.Fatalf("脸的顺序或个数不对：%q", got)
	}
	// 只从第二段拖起
	tt.SetClipboard("")
	drag(all.X-2, all.Y+all.H/2, all.X+200, all.Y+all.H+30)
	tt.Key(ui.Cmd, ui.KeyC)
	got = clipText(tt)
	if strings.Contains(got, "[微笑]") || !strings.Contains(got, "[可爱]") || strings.ContainsRune(got, '￼') {
		t.Fatalf("只选第二段得到 %q", got)
	}
}

// 选着字的时候脸的图确认没有（404）：脸从一个占位字符变成 [名字] 好几个字，选区的下标跟着变。
// 前后各复制一次，都不能 panic、不能有 U+FFFC、不能丢掉脸
func TestCopyFacesWhenImageGoesMissing(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red)}, gate: make(chan struct{})}
	m := faceMsg(false, "a", "你好", "#14", "世界")
	a, q := faceApp(t, n, m)
	a.fs().table = testTable
	var rc ui.Rect
	drawCheck = func(r ui.Rect, _ []ui.Span, _ []ui.TextObject) { rc = r }
	defer func() { drawCheck = nil }()
	tt := testerOf(500, 300, bubbleView(a, m))
	tt.Frame()
	mid := rc.Y + rc.H/2
	tt.Press(rc.X-2, mid)
	tt.Move(rc.X+rc.W/2, mid)
	tt.Move(rc.X+rc.W+3, mid)
	tt.Release(rc.X+rc.W+3, mid)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	if got := clipText(tt); got != "你好[微笑]世界" {
		t.Fatalf("图还在路上时复制得到 %q", got)
	}
	// 这张图确认没有：脸退成文字
	n.mu.Lock()
	delete(n.png, "14")
	n.mu.Unlock()
	close(n.gate)
	a.fs().wg.Wait()
	q.drain()
	tt.Frame()
	if a.fs().state("14") != faceMissing {
		t.Fatalf("图删了以后状态是 %d，不是确认没有", a.fs().state("14"))
	}
	// 段落的字变了（框换成 [微笑]），mygo 会清掉原来的选区；重新选一遍再复制
	r, ok := tt.Find("你好[微笑]世界")
	if !ok {
		t.Fatalf("脸没退成文字：%q", tt.Texts())
	}
	mid = r.Y + r.H/2
	tt.Press(r.X-2, mid)
	tt.Move(r.X+r.W/2, mid)
	tt.Move(r.X+r.W+3, mid)
	tt.Release(r.X+r.W+3, mid)
	tt.Frame()
	tt.SetClipboard("")
	tt.Key(ui.Cmd, ui.KeyC)
	got := clipText(tt)
	if strings.ContainsRune(got, '￼') {
		t.Fatalf("图没了以后剪贴板里有 U+FFFC：%q", got)
	}
	if got != "你好[微笑]世界" {
		t.Fatalf("图没了以后复制得到 %q", got)
	}
}

// 没有脸的消息、占位和小字也能整条选中复制，不丢字
func TestCopyPlainBubble(t *testing.T) {
	seg := `{"segs":[{"type":"text","text":"只有文字"},{"type":"record"}],"unsent":"自检：已撤回"}`
	m := msgFrom("qq:private:1", event{ID: "a", ActorKind: "human", ActorID: "qq:42", SegmentsJSON: &seg}, nil)
	a := chatApp([]*msg{m}, false)
	tt := testerOf(500, 400, bubbleView(a, m))
	r, ok := tt.Find("只有文字")
	if !ok {
		t.Fatalf("文字没画出来：%q", tt.Texts())
	}
	tt.Press(r.X-2, r.Y+r.H/2)
	tt.Move(r.X+r.W, r.Y+r.H/2)
	tt.Move(r.X+r.W+2, r.Y+r.H/2)
	tt.Release(r.X+r.W+2, r.Y+r.H/2)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	s := clipText(tt)
	if !strings.Contains(s, "只有文字") {
		t.Fatalf("复制得到 %q", s)
	}
}

// ───────────── T-close ─────────────

// 取图的 goroutine 卡着时窗口关了：goroutine 退出，不回写，post 里的活不执行
func TestCloseStopsFaceFetch(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(8, 8, red)}, gate: make(chan struct{})}
	m := faceMsg(false, "a", "#14", "#21")
	a, q := faceApp(t, n, m)
	f := a.fs()
	f.table = testTable
	tt := testerOf(500, 300, bubbleView(a, m))
	tt.Frame()
	a.cancel() // 关窗口
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(uiWaitLimit):
		t.Fatal("窗口关了以后取图的 goroutine 还没退出")
	}
	if q.pending() != 0 {
		t.Fatalf("关窗口以后还有 %d 个活要回界面线程", q.pending())
	}
	a.post(func() { t.Error("关窗口以后 post 还执行了") })
	if q.pending() != 0 {
		t.Fatal("关窗口以后 post 还入了队")
	}
}

// 取图不看 ctx（取到了才发现窗口关了）：取到的结果也不能写进去
func TestCloseDropsLateFaceResult(t *testing.T) {
	n := &fakeNet{png: map[string][]byte{"14": facePNG(8, 8, red)}, gate: make(chan struct{}), ignore: true}
	m := faceMsg(false, "a", "#14")
	a, q := faceApp(t, n, m)
	f := a.fs()
	f.table = testTable
	tt := testerOf(500, 300, bubbleView(a, m))
	tt.Frame()
	a.cancel()
	close(n.gate)
	f.wg.Wait()
	q.drain()
	if f.state("14") == faceOK || f.peek("14") != nil {
		t.Fatal("窗口关了以后取到的图还写进了缓存")
	}
}

// 小黄脸存储自己也守着 ctx，不指望外面的 post 帮它挡：取图时窗口关了，哪怕 post 照常执行也不写
func TestFaceStoreGuardsCtxItself(t *testing.T) {
	t.Parallel()
	n := &fakeNet{png: map[string][]byte{"14": facePNG(8, 8, red)}, gate: make(chan struct{}), ignore: true}
	ctx, cancel := context.WithCancel(context.Background())
	q := &uiQueue{}
	f := newFaceStore(ctx, q.post) // 裸的 post：不看 ctx
	f.fetchPNG = n.fetchPNG
	f.want("14")
	cancel()
	close(n.gate)
	f.wg.Wait()
	q.drain()
	if f.state("14") != faceLoading || f.peek("14") != nil {
		t.Fatalf("窗口关了以后取到的图还写进了缓存：状态 %d", f.state("14"))
	}
	if q.pending() != 0 {
		t.Fatal("窗口关了以后还有活要回界面线程")
	}
}

// 名字表在两次重取之间窗口关了：不再取
func TestCloseStopsTableRetry(t *testing.T) {
	t.Parallel()
	n := &fakeNet{tables: []map[string]faceInfo{{}}}
	a, _ := faceApp(t, n)
	f := a.fs()
	f.retry = time.Hour // 卡在等待里
	started := make(chan struct{})
	f.fetchTable = func(ctx context.Context) (map[string]faceInfo, error) {
		table, err := n.fetchTable(ctx)
		close(started)
		return table, err
	}
	f.start()
	select {
	case <-started:
	case <-time.After(uiWaitLimit):
		t.Fatal("名字表请求没有启动")
	}
	a.cancel()
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(uiWaitLimit):
		t.Fatal("窗口关了以后名字表的 goroutine 还没退出")
	}
}

// ───────────── 取数据：只读接口 ─────────────

// faceReader 假的只读接口：名字表两项，14 有图，500 读出错，别的没有；记下收到的编号
type faceReader struct {
	demoReader
	mu   sync.Mutex
	hits []string
}

func (r *faceReader) ReadFaceIndex(ctx context.Context) (api.Result[api.List[api.FaceView]], error) {
	if err := ctx.Err(); err != nil {
		return api.Result[api.List[api.FaceView]]{State: api.Failed, Err: err}, err
	}
	return api.Result[api.List[api.FaceView]]{State: api.Ready, Value: api.List[api.FaceView]{Items: []api.FaceView{{ID: "14", Name: "微笑", Kind: "classic"}, {ID: "900", Name: "笑哭", Kind: "emoji", Char: "😂"}}}}, nil
}

func (r *faceReader) ReadFacePNG(_ context.Context, id string) (api.Result[api.MediaView], error) {
	r.mu.Lock()
	r.hits = append(r.hits, id)
	r.mu.Unlock()
	switch id {
	case "14":
		return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: facePNG(4, 4, red)}}, nil
	case "500":
		err := errors.New("读取出错")
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	}
	err := errors.New("没有这个小黄脸的图")
	return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
}

func TestFaceReader(t *testing.T) {
	t.Parallel()
	r := &faceReader{}
	tbl, err := readFaceTable(context.Background(), r)
	if err != nil || len(tbl) != 2 || tbl["900"].Char != "😂" || tbl["14"].Name != "微笑" {
		t.Fatalf("名字表：%v %v", tbl, err)
	}
	if b, err := readFacePNG(context.Background(), r, "14"); err != nil || bmpOf(b) == nil {
		t.Fatalf("取图：%v", err)
	}
	for _, id := range []string{"99", "500"} { // 没有的、读出错的都当没有这个图，界面退成文字
		if _, err := readFacePNG(context.Background(), r, id); !errors.Is(err, errNotFound) {
			t.Fatalf("%s 应是 errNotFound：%v", id, err)
		}
	}
	// 编号只收 1 到 6 位数字：别的不往后台问
	for _, id := range []string{"", "abc", "1234567", "../x"} {
		if _, err := readFacePNG(context.Background(), r, id); !errors.Is(err, errNotFound) {
			t.Fatalf("%q 应直接 errNotFound：%v", id, err)
		}
	}
	if len(r.hits) != 3 {
		t.Fatalf("问到后台的编号：%v", r.hits)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readFaceTable(ctx, r); err == nil {
		t.Fatal("ctx 取消了还取到了")
	}
	if _, err := readFaceTable(context.Background(), nil); err == nil {
		t.Fatal("没有数据源还取到了")
	}
}

func bmpOf(b []byte) *ui.Bitmap { return decodeFace(b) }

// 解码：大图缩到长边 64，小图不放大，不是 PNG 返回 nil
func TestDecodeFace(t *testing.T) {
	t.Parallel()
	if decodeFace([]byte("xx")) != nil || decodeFace(nil) != nil {
		t.Fatal("坏数据应返回 nil")
	}
	if decodeFace(facePNG(300, 150, red)) == nil || decodeFace(facePNG(3, 3, red)) == nil {
		t.Fatal("好图解不出来")
	}
}

// clipText 剪贴板里的纯文本
func clipText(tt *ui.Tester) string {
	b, err := tt.ClipboardData().Read(transfer.Text)
	if err != nil {
		return ""
	}
	return string(b)
}

// 模拟数据里的脸：名字表、图都对得上，名字表之外的编号是 404；模拟消息里带脸的段能解出来
func TestMockFaces(t *testing.T) {
	t.Parallel()
	for id, info := range mockFaceTable {
		b, err := mockFacePNG(id)
		if info.Kind == "emoji" {
			if !errors.Is(err, errNotFound) {
				t.Fatalf("emoji 类 %s 不该有图：%v", id, err)
			}
			continue
		}
		if err != nil || decodeFace(b) == nil {
			t.Fatalf("%s 的图：%v", id, err)
		}
	}
	if _, err := mockFacePNG("5"); !errors.Is(err, errNotFound) {
		t.Fatalf("名字表之外应是 404：%v", err)
	}
	es, err := mockLocal("aoi", 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		for _, s := range segsOf(e) {
			if s.Kind == "face" || s.Kind == "superface" {
				n++
				if _, ok := mockFaceTable[s.Ref]; !ok {
					t.Fatalf("模拟消息里的脸 %q 不在模拟名字表里", s.Ref)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("模拟消息里没有带小黄脸的")
	}
}

// 第零步 e：小黄脸 PNG 的解码和缩放耗时。真实的尺寸分布没量（不读 data/，第八节第 38 条），
// 用几种常见边长的合成 PNG；每个尺寸解 200 次取均值（QueryPerformanceCounter 的分辨率下）
func TestDecodeFaceCost(t *testing.T) {
	t.Parallel()
	for _, side := range []int{32, 64, 128, 160, 256} {
		data := facePNG(side, side, red)
		decodeFace(data) // 预热
		const n = 200
		t0 := qpcNow()
		for i := 0; i < n; i++ {
			if decodeFace(data) == nil {
				t.Fatal("解不出来")
			}
		}
		per := qpcMs(t0, qpcNow()) / n
		t.Logf("%d×%d：PNG %d 字节，解码加缩放均值 %.3f ms", side, side, len(data), per)
		if per > 20 {
			t.Errorf("%d×%d 解一张要 %.1f ms，超过 20 ms 的宽松上限", side, side, per)
		}
	}
}

// 复核发现：boot 的后台线程和界面线程同时第一次调 fs()，各建一份，名字表写进一份、界面读另一份。
// 要求：并发调用拿到同一个指针；-race 下无数据竞争。
func TestFsConcurrentSingleStore(t *testing.T) {
	t.Parallel()
	a := &app{}
	const n = 16
	got := make([]*faceStore, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = a.fs()
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("第 %d 个调用拿到另一份 faceStore", i)
		}
	}
}
