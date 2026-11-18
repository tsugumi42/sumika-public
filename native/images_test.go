package main

// 第六步的测试：T-geom（图的框、窄窗口、不跳）、T-fail（图部分）、T-cache-key、T-灯箱（静态部分）、T-count（图部分）、
// 网格规则、解码分派、缓存的字节上限、同时解码上限、关窗口。
// 数据全是编的：图是测试里现造的纯色 PNG / JPEG / GIF；取图走假网络或假的只读接口，不读 data/。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// ───────────── 测试用的假网络 ─────────────

// imgNet 假的后台：orig 记每个哈希的原图宽高，取某一档时像后台一样——原图比档宽就缩到档宽，否则给原图。files 里有的原样给（坏图、HEIC 用）
type imgNet struct {
	mu      sync.Mutex
	orig    map[string][2]int
	files   map[string][]byte
	errs    map[string]error
	calls   map[string][]int // 哈希 → 每次请求的档
	gate    chan struct{}
	onFetch func() // 记下请求后通知测试，避免用画帧次数猜后台是否已经运行
}

func (n *imgNet) fetch(ctx context.Context, ref api.MediaRef, tier int) ([]byte, error) {
	hash := ref.Hash
	n.mu.Lock()
	if n.calls == nil {
		n.calls = map[string][]int{}
	}
	n.calls[hash] = append(n.calls[hash], tier)
	gate, f, e, o := n.gate, n.files[hash], n.errs[hash], n.orig[hash]
	n.mu.Unlock()
	if n.onFetch != nil {
		n.onFetch()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch {
	case e != nil:
		return nil, e
	case f != nil:
		return f, nil
	case o[0] == 0:
		return nil, errNotFound
	}
	w, h := o[0], o[1]
	if tier > 0 && w > tier {
		w, h = tier, max(h*tier/w, 1)
	}
	return facePNG(w, h, red), nil
}

func (n *imgNet) tiers(hash string) []int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]int(nil), n.calls[hash]...)
}

// imgApp 一个带假网络的 app：取图走 net，回界面线程的活进 q
func imgApp(t *testing.T, n *imgNet, msgs ...*msg) (*app, *uiQueue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := chatApp(msgs, false)
	q := &uiQueue{}
	a.ctx, a.cancel, a.postFn = ctx, cancel, q.post
	a.imgFetch = n.fetch
	return a, q
}

// settleImgs 等取图的 goroutine 都回来、把结果交给界面线程，再画一帧
func settleImgs(a *app, q *uiQueue, tt *ui.Tester) {
	tt.Frame() // 首次绘制取得真实像素比例，下一轮构建才发起静态图读取。
	// 转发构建结束后 is() 已回到正文域，必须等窗口自己持有的缓存。
	for _, cache := range []*imageStore{a.imgs, a.forward.imgs} {
		if cache != nil {
			cache.wg.Wait()
		}
	}
	q.drain()
	tt.Frame()
}

func imgJSON(hash string, w, h int) string {
	if w == 0 {
		return fmt.Sprintf(`{"type":"image","hash":%q}`, hash)
	}
	return fmt.Sprintf(`{"type":"image","hash":%q,"width":%d,"height":%d}`, hash, w, h)
}

// imgMsg 一条消息：parts 里以 "{" 开头的是整段 JSON，其余是文字
func imgMsg(mine bool, id string, parts ...string) *msg {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString(",")
		}
		if strings.HasPrefix(p, "{") {
			sb.WriteString(p)
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
	m.md = false
	return m
}

// ───────────── 解码分派 ─────────────

func jpegBytes(w, h int, exif int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		panic(err)
	}
	b := buf.Bytes()
	if exif == 0 {
		return b
	}
	// APP1：Exif\0\0 + 小端 TIFF 头 + 一个 IFD 项（0x0112 方向，SHORT）
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(exif), 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(append(append([]byte{}, b[:2]...), append(seg, payload...)...), b[2:]...)
}

func gifBytes(w, h int) []byte {
	pal := color.Palette{color.White, color.Black}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, w, h), pal), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// 把 PNG 头里的宽高改掉（并重算 IHDR 的校验），造一个"头声称很大"的图
func pngClaiming(w, h uint32) []byte {
	b := facePNG(2, 2, red)
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

func TestDecodeStatic(t *testing.T) {
	t.Parallel()
	// 1×1 的无损 WebP
	webp1, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		tier int
		w, h int // 要的位图大小；0 是应该失败
	}{
		{"PNG 原样", facePNG(40, 20, red), 768, 40, 20},
		{"JPEG 原样", jpegBytes(40, 20, 0), 768, 40, 20},
		{"GIF 取首帧", gifBytes(30, 10), 768, 30, 10},
		{"WebP", webp1, 768, 1, 1},
		{"PNG 比档宽：缩到档宽", facePNG(3000, 100, red), 768, 768, 26},
		{"PNG 很长：高度不超过 4096", facePNG(100, 9000, red), 1536, 46, 4096},
		{"JPEG 带 EXIF 方向 6：转正，宽高对调", jpegBytes(40, 20, 6), 768, 20, 40},
		{"JPEG 大于 2048：只缩小", jpegBytes(2100, 40, 0), 768, 768, 15},
		{"HEIC 认不出", append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 40)...), 768, 0, 0},
		{"空数据", nil, 768, 0, 0},
		{"PNG 被截断", facePNG(40, 20, red)[:30], 768, 0, 0},
		{"头声称 7 万 × 7 万：不去解", pngClaiming(70000, 70000), 768, 0, 0},
		{"头声称 0 × 0", pngClaiming(0, 0), 768, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := decodeStatic(c.data, c.tier)
			if c.w == 0 {
				if err == nil {
					t.Fatalf("应解不开，得到 %v", b)
				}
				return
			}
			if err != nil {
				t.Fatalf("解不开：%v", err)
			}
			if w, h := b.Size(); w != c.w || h != c.h {
				t.Fatalf("位图 %d×%d，要 %d×%d", w, h, c.w, c.h)
			}
		})
	}
}

// ───────────── T-geom：框、窄窗口、到了不跳 ─────────────

// 窄窗口仍保持正文顺序、图片比例，并把整张图限制在窗口内。
func TestImageGeometryNarrow(t *testing.T) {
	for _, w := range []int{821, 845, 900} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			n := &imgNet{orig: map[string][2]int{"big": {1200, 600}}}
			m := imgMsg(false, "a", "看这张", imgJSON("big", 1200, 600))
			a, q := imgApp(t, n, m)
			tt := testerOf(w, 900, func(c *ui.Context) { a.chatView(c) })
			settleImgs(a, q, tt)
			r, ok := tt.Find("图片")
			txt, ok2 := tt.Find("看这张")
			if !ok || !ok2 {
				t.Fatalf("图或文字没画出来：%q", tt.Texts())
			}
			if r.W <= 0 || r.W > 430.5 || r.X+r.W > float32(w) || !approx(r.W/r.H, 2, .02) {
				t.Fatalf("图片越界或比例改变：%+v，窗口%d", r, w)
			}
			if r.Y < txt.Y+txt.H {
				t.Fatalf("图在字上面：字 %+v 图 %+v（按原序，字在前）", txt, r)
			}
		})
	}
}

// 图到之前和到之后框完全一样：存了宽高的按比例，没存的锁成 4:3（边界 ⑧）
func TestImageBoxDoesNotJumpWhenImageArrives(t *testing.T) {
	cases := []struct {
		name string
		w, h int // 存的宽高，0 是没存
		natW int // 图真实的宽（没存宽高时真实比例和 4:3 不同）
		natH int
	}{
		{"存了宽高", 600, 300, 600, 300},
		{"没存宽高，真图是方的", 0, 0, 500, 500},
		{"没存宽高，真图很长", 0, 0, 100, 900},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &imgNet{orig: map[string][2]int{"h": {c.natW, c.natH}}}
			m := imgMsg(false, "a", imgJSON("h", c.w, c.h), "后面的字")
			a, q := imgApp(t, n, m)
			tt := testerOf(900, 900, func(c *ui.Context) { a.chatView(c) })
			r1, _ := tt.Find("图片")
			t1, _ := tt.Find("后面的字")
			settleImgs(a, q, tt)
			r2, ok := tt.Find("图片")
			t2, _ := tt.Find("后面的字")
			if !ok || r2.W <= 0 || r2.H <= 0 {
				t.Fatalf("图没显示：%q", tt.Texts())
			}
			if r1 != r2 || t1 != t2 {
				t.Fatalf("图到了以后版面跳了：框 %+v → %+v，后面的字 %+v → %+v", r1, r2, t1, t2)
			}
		})
	}
}

// 纯图占媒体槽，图文占正文槽；新壳正文比裸媒体内缩 11 DIP。
func TestNakedVersusShell(t *testing.T) {
	n := &imgNet{orig: map[string][2]int{"h": {200, 100}}}
	only := imgMsg(false, "a", `{"type":"image","hash":"h","width":200,"height":100,"summary":"甲图"}`)
	with := imgMsg(false, "b", `{"type":"image","hash":"h","width":200,"height":100,"summary":"乙图"}`, "带字")
	with.at += 3600_000 // 隔开，不并成一组
	a, q := imgApp(t, n, only, with)
	tt := testerOf(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	r1, ok1 := tt.Find("甲图")
	r2, ok2 := tt.Find("乙图")
	if !ok1 || !ok2 {
		t.Fatalf("图没画出来：%q", tt.Texts())
	}
	if d := r2.X - r1.X; !approx(d, 11, 0.5) {
		t.Fatalf("正文与媒体槽间距=%.2f，应为11 DIP", d)
	}
}

// 多于十张时拆相册；最后一张仍可打开，不能因组件组数上限丢内容。
func TestAlbumKeepsAllImageActions(t *testing.T) {
	n := &imgNet{orig: map[string][2]int{}}
	var parts []string
	for i := range 21 {
		h := fmt.Sprintf("g%d", i)
		n.orig[h] = [2]int{200, 200}
		parts = append(parts, imgJSON(h, 200, 200))
	}
	a, q := imgApp(t, n, imgMsg(false, "a", parts...))
	tt := testerOf(900, 1200, a.chatView)
	settleImgs(a, q, tt)
	click(t, tt, "图片 21")
	if _, ok := tt.Find("图片·大图 21/21"); !ok {
		t.Fatalf("最后一张不可达：%q", tt.Texts())
	}
}

// 图片与表情包混排时均可显示，图片仍可打开。
func TestImageAndStickerMix(t *testing.T) {
	n := &imgNet{orig: map[string][2]int{"a": {200, 100}, "s": {120, 120}}}
	st := `{"type":"image","hash":"s","width":120,"height":120,"sticker":true}`
	m := imgMsg(false, "a", imgJSON("a", 200, 100), st, imgJSON("a", 200, 100))
	a, q := imgApp(t, n, m)
	tt := testerOf(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if _, ok := tt.Find("表情包"); !ok {
		t.Fatalf("表情包没画：%q", tt.Texts())
	}
	click(t, tt, "图片")
	if _, ok := tt.Find("关闭"); !ok {
		t.Fatalf("混排中的图片无法打开：%q", tt.Texts())
	}
	click(t, tt, "关闭")
	if _, ok := tt.Find("关闭"); ok {
		t.Fatal("关闭后图片查看器仍在")
	}
}

// ───────────── T-fail：图（假的只读接口）─────────────

// blobSrv 假的 api.Reader：只换掉 ReadMedia，记下每次收到的凭据哈希和档；down 时像后台没在运行，请求到不了它
type blobSrv struct {
	demoReader
	mode  atomic.Int32 // 0 正常，1 文件缺失，3 返回 HEIC 样子的字节
	down  atomic.Bool
	mu    sync.Mutex
	paths []string
}

func (b *blobSrv) ReadMedia(_ context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	if b.down.Load() {
		err := errors.New("后台没在运行")
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	}
	b.mu.Lock()
	b.paths = append(b.paths, fmt.Sprintf("%s?w=%d", ref.Hash, tier))
	b.mu.Unlock()
	var data []byte
	switch b.mode.Load() {
	case 1:
		err := errors.New("本地媒体文件缺失")
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	case 3:
		data = append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 60)...)
	default:
		data = facePNG(100, 50, blue)
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: data}}, nil
}

func (b *blobSrv) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.paths)
}

func TestImageFetchFailureAndClickRetry(t *testing.T) {
	srv := &blobSrv{}
	h := strings.Repeat("ab", 32)
	m := imgMsg(false, "a", imgJSON(h, 100, 50), "后面")
	a, q := imgApp(t, &imgNet{}, m)
	a.imgFetch, a.reader = nil, srv // 走真的 loadImage → ReadMedia
	// 404：只发一次请求，写"图片没取到，点一下再试"，不写"已过期"
	srv.mode.Store(1)
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	for i := 0; i < 5; i++ { // 重建很多次，请求不能叠，也不自动重试
		tt.Frame()
		a.is().wg.Wait()
		q.drain()
	}
	if srv.count() != 1 {
		t.Fatalf("404 以后请求了 %d 次，应只有 1 次", srv.count())
	}
	if !tt.HasText("图片没取到，点一下再试") || tt.HasText("已过期") {
		t.Fatalf("失败的说明不对：%q", tt.Texts())
	}

	// 后台没在运行：点一下再取一次，请求到不了，还是失败，仍写同样的说明
	srv.down.Store(true)
	if err := tt.Click("图片"); err != nil {
		t.Fatal(err)
	}
	settleImgs(a, q, tt)
	if srv.count() != 1 || !tt.HasText("图片没取到，点一下再试") {
		t.Fatalf("网络不通时点击：服务端请求 %d 次（要仍是 1），文字 %q", srv.count(), tt.Texts())
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	a.is().wg.Wait()
	if q.pending() != 0 {
		t.Fatalf("没点就又去取了：%d 个结果等着回来", q.pending())
	}

	// 服务恢复：点一下取到了，图画出来，说明消失
	srv.down.Store(false)
	srv.mode.Store(0)
	if err := tt.Click("图片"); err != nil {
		t.Fatal(err)
	}
	settleImgs(a, q, tt)
	if srv.count() != 2 || tt.HasText("图片没取到") {
		t.Fatalf("恢复后请求 %d 次（要 2），文字 %q", srv.count(), tt.Texts())
	}
}

// 解不开的图（HEIC 等）：写"图片打不开"，点一下不再请求
func TestImageUndecodableDoesNotRefetch(t *testing.T) {
	srv := &blobSrv{}
	srv.mode.Store(3)

	h := strings.Repeat("cd", 32)
	m := imgMsg(false, "a", imgJSON(h, 100, 50))
	a, q := imgApp(t, &imgNet{}, m)
	a.imgFetch, a.reader = nil, srv
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if !tt.HasText("图片打不开") || tt.HasText("没取到") {
		t.Fatalf("应写“图片打不开”：%q", tt.Texts())
	}
	_ = tt.Click("图片")
	settleImgs(a, q, tt)
	if srv.count() != 1 {
		t.Fatalf("解不开的图点击后又请求了：%d 次", srv.count())
	}
}

// 还在取的时候重建很多次、再点击，也只请求一次（同哈希的 pending 防重复）
func TestImagePendingDedup(t *testing.T) {
	started := make(chan struct{})
	n := &imgNet{orig: map[string][2]int{"h": {100, 50}}, gate: make(chan struct{}),
		onFetch: sync.OnceFunc(func() { close(started) })}
	m := imgMsg(false, "a", imgJSON("h", 100, 50))
	a, q := imgApp(t, n, m)
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	tt.Frame() // 先取得真实缩放比例，再发起取图。
	select {
	case <-started:
	case <-time.After(uiWaitLimit):
		t.Fatal("取图请求没有启动")
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if err := tt.Click("图片"); err != nil { // 还在加载：点击不重取也不开看大图
		t.Fatal(err)
	}
	tt.Frame()
	if got := n.tiers("h"); len(got) != 1 {
		t.Fatalf("还在取的时候请求了 %d 次", len(got))
	}
	close(n.gate)
	settleImgs(a, q, tt)
	if got := n.tiers("h"); len(got) != 1 {
		t.Fatalf("取完请求了 %d 次", len(got))
	}
}

// ───────────── T-count：缓存命中时视图不再取、不再解 ─────────────

func TestNoFetchNoDecodeWhenCached(t *testing.T) {
	n := &imgNet{orig: map[string][2]int{"h": {600, 300}, "g": {200, 200}}}
	m := imgMsg(false, "a", imgJSON("h", 600, 300), "字", imgJSON("g", 200, 200), imgJSON("g", 200, 200))
	a, q := imgApp(t, n, m)
	tt := testerOf(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	decoded, h, g := a.is().decoded.Load(), len(n.tiers("h")), len(n.tiers("g"))
	if decoded == 0 {
		t.Fatal("断言会空过：一次都没解码")
	}
	for i := 0; i < 20; i++ {
		tt.Frame()
	}
	a.is().wg.Wait()
	if q.pending() != 0 || a.is().decoded.Load() != decoded || len(n.tiers("h")) != h || len(n.tiers("g")) != g {
		t.Fatalf("缓存命中后又有活：解码 %d → %d，请求 %d/%d → %d/%d，等着回界面线程的 %d 个",
			decoded, a.is().decoded.Load(), h, g, len(n.tiers("h")), len(n.tiers("g")), q.pending())
	}
}

// ───────────── T-cache-key、T-灯箱（静态部分）─────────────

// 同一个哈希两档各一条记录，看大图要的 1536 不会被小档顶替
func TestCacheKeyTiersAreSeparate(t *testing.T) {
	t.Parallel()
	n := &imgNet{orig: map[string][2]int{"h": {3000, 1500}}}
	a, q := imgApp(t, n)
	st := a.is()
	small, big := st.get(imgKey{"h", 512, false}), st.get(imgKey{"h", 1536, false})
	if small == big {
		t.Fatal("两档是同一条记录")
	}
	st.wg.Wait()
	q.drain()
	if small.state != imgOK || big.state != imgOK {
		t.Fatalf("状态 %d / %d", small.state, big.state)
	}
	sw, _ := small.bmp.Size()
	bw, _ := big.bmp.Size()
	if sw != 512 || bw != 1536 {
		t.Fatalf("两档的位图宽 %d / %d，要 512 / 1536", sw, bw)
	}
	if got := n.tiers("h"); len(got) != 2 {
		t.Fatalf("请求 %v，应是各一次", got)
	}
}

// 125% 与 200% 屏幕请求不同物理档；灯箱必须读取并显示超过缩略上限的原件。
func TestImagePhysicalScaleAndOriginalLightbox(t *testing.T) {
	n := &imgNet{orig: map[string][2]int{"h": {3000, 1500}}}
	m := imgMsg(false, "a", imgJSON("h", 3000, 1500))
	a, q := imgApp(t, n, m)
	tt := ui.NewTester(func(c *ui.Context) { a.chatView(c) }, 900, 700)
	tt.SetScale(1.25)
	settleImgs(a, q, tt)
	frame, _ := tt.Find("图片")
	if got := n.tiers("h"); len(got) == 0 || got[len(got)-1] < int(frame.W*1.25) {
		t.Fatalf("125%% 屏实际图宽%.1f，取图档不足：%v", frame.W, got)
	}
	tt.SetScale(2)
	settleImgs(a, q, tt)
	if got := n.tiers("h"); len(got) == 0 || got[len(got)-1] < int(frame.W*2) {
		t.Fatalf("200%% 屏实际图宽%.1f，取图档不足：%v", frame.W, got)
	}
	if err := tt.Click("图片"); err != nil {
		t.Fatal(err)
	}
	settleImgs(a, q, tt)
	e := a.is().peek(imgKey{"qq/h", 0, false})
	if e == nil || e.state != imgOK {
		t.Fatal("原图没有加载")
	}
	if w, h := e.bmp.Size(); w != 3000 || h != 1500 {
		t.Fatalf("灯箱原图尺寸 %dx%d", w, h)
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(time.Second)
	if _, ok := tt.Find("关闭"); ok {
		t.Fatal("灯箱未关闭")
	}
}

// 看大图给的图：当前和前后各一张用 1536 档，没取到的用显示档顶着，什么都没有用 1×1 空图（Lightbox 不许 nil）
func TestLargeOfPicksBest(t *testing.T) {
	t.Parallel()
	n := &imgNet{orig: map[string][2]int{}}
	a, q := imgApp(t, n)
	st := a.is()
	imgs := []seg{
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "a"}}),
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "b"}}),
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "c"}}),
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "d"}}),
	}
	put := func(k imgKey) *ui.Bitmap {
		b := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 4, 4)))
		st.m[k] = &imgEntry{state: imgOK, bmp: b}
		return b
	}
	smallA, smallB := put(imgKey{"a", 768, false}), put(imgKey{"b", 768, false})
	bigB := put(imgKey{"b", lightboxTier, false})
	display := []*imgEntry{st.m[imgKey{"a", 768, false}], st.m[imgKey{"b", 768, false}], {state: imgLoading}, nil}
	got := a.largeOf(imgs, display, true, 0) // 看第 0 张：0、1 要大档，2 之后不要
	st.wg.Wait()
	if got[0] != smallA || got[1] != bigB || got[2] != emptyBitmap || got[3] != emptyBitmap {
		t.Fatalf("选图不对：%v", got)
	}
	if len(n.tiers("a")) != 1 || n.tiers("a")[0] != lightboxTier || len(n.tiers("b")) != 0 || len(n.tiers("c")) != 0 || len(n.tiers("d")) != 0 {
		t.Fatalf("预取不对：a %v b %v c %v d %v（要 a 取大档，b 已有，c、d 离当前超过一张，不取）", n.tiers("a"), n.tiers("b"), n.tiers("c"), n.tiers("d"))
	}
	_ = smallB
	_ = q
}

// ───────────── 缓存的字节上限、同时解码上限、关窗口 ─────────────

func TestEvictionKeepsFramesInUse(t *testing.T) {
	t.Parallel()
	n := &imgNet{orig: map[string][2]int{}, errs: map[string]error{"x": errors.New("连不上")}}
	for _, h := range []string{"a", "b", "c", "d"} {
		n.orig[h] = [2]int{100, 100} // 每张 100×100×4 = 40000 字节
	}
	a, q := imgApp(t, n)
	st := a.is()
	st.budget = 100000 // 放得下两张
	load := func(h string) {
		st.get(imgKey{h, 128, false})
		st.wg.Wait()
		q.drain()
	}
	st.beginFrame()
	load("a")
	st.beginFrame()
	load("b")
	st.beginFrame()
	load("c") // 三张 120000 > 100000：a 最久，且离现在已有两帧 → 扔 a
	if st.peek(imgKey{"a", 128, false}) != nil || st.peek(imgKey{"b", 128, false}) == nil || st.peek(imgKey{"c", 128, false}) == nil {
		t.Fatalf("该扔最久没用的 a：a=%v b=%v c=%v", st.peek(imgKey{"a", 128, false}), st.peek(imgKey{"b", 128, false}), st.peek(imgKey{"c", 128, false}))
	}
	if st.bytes != 80000 {
		t.Fatalf("总字节 %d，要 80000", st.bytes)
	}
	// 同一帧里 b、c、d 都在用：超了也不扔
	st.beginFrame()
	st.get(imgKey{"b", 128, false})
	st.get(imgKey{"c", 128, false})
	load("d")
	if st.peek(imgKey{"b", 128, false}) == nil || st.peek(imgKey{"c", 128, false}) == nil || st.peek(imgKey{"d", 128, false}) == nil || st.bytes != 120000 {
		t.Fatalf("正在用的被扔了：b=%v c=%v d=%v 字节 %d", st.peek(imgKey{"b", 128, false}), st.peek(imgKey{"c", 128, false}), st.peek(imgKey{"d", 128, false}), st.bytes)
	}
	// 被扔的图再用到，重新请求一次
	st.beginFrame()
	st.beginFrame()
	e := st.get(imgKey{"a", 128, false})
	st.wg.Wait()
	if e.state != imgLoading || len(n.tiers("a")) != 2 {
		t.Fatalf("被扔的图再用应重取：状态 %d，请求 %v", e.state, n.tiers("a"))
	}
	st.wg.Wait()
	q.drain()
	// 失败的记录不被扔（扔了就会每次构建重取）
	st.get(imgKey{"x", 128, false})
	st.wg.Wait()
	q.drain()
	st.beginFrame()
	st.beginFrame()
	st.budget = 1
	st.evict()
	if e := st.peek(imgKey{"x", 128, false}); e == nil || e.state != imgFailed {
		t.Fatalf("失败的记录被扔了：%+v", e)
	}
}

func TestDecodeConcurrencyCap(t *testing.T) {
	t.Parallel()
	n := &imgNet{orig: map[string][2]int{}}
	a, q := imgApp(t, n)
	st := a.is()
	var cur, peak atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { unblock(); st.wg.Wait() })
	st.decode = func(data []byte, tier int) (*ui.Bitmap, error) {
		c := cur.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		started <- struct{}{}
		<-release
		cur.Add(-1)
		return decodeStatic(data, tier)
	}
	for i := 0; i < 8; i++ {
		n.orig[fmt.Sprintf("h%d", i)] = [2]int{50, 50} // 先填好再取：取图的 goroutine 读这张表
	}
	for i := 0; i < 8; i++ {
		st.get(imgKey{fmt.Sprintf("h%d", i), 128, false})
	}
	// 工人在 start 里同步计数；先核对它，避免第三个工人尚未被调度时误判为没有超限。
	st.jobsMu.Lock()
	workers := st.workers
	st.jobsMu.Unlock()
	if workers != 2 {
		t.Fatalf("读取解码工人 %d 个，要正好 2", workers)
	}
	deadline := time.After(2 * time.Second)
	for range 2 {
		select {
		case <-started:
		case <-deadline:
			t.Fatalf("同时在解的 %d 个，要正好 2（其余请求仍在排队）", cur.Load())
		}
	}
	unblock()
	st.wg.Wait()
	q.drain()
	if peak.Load() != 2 || st.count() != 8 {
		t.Fatalf("峰值 %d（要 2），解好 %d 张（要 8）", peak.Load(), st.count())
	}
}

// 窗口关了：还在路上的取图不再回界面线程，goroutine 都退出
func TestImageCloseWindow(t *testing.T) {
	started := make(chan struct{})
	n := &imgNet{orig: map[string][2]int{"h": {100, 50}}, gate: make(chan struct{}),
		onFetch: sync.OnceFunc(func() { close(started) })}
	m := imgMsg(false, "a", imgJSON("h", 100, 50))
	a, q := imgApp(t, n, m)
	a.is().post = q.post // 直接观察存储的取消保护，不让 app.post 替它挡住迟到结果。
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	tt.Frame()
	select {
	case <-started:
	case <-time.After(uiWaitLimit):
		t.Fatal("关窗前取图请求没有启动")
	}
	q.drain() // 窗口活着时先处理这一帧的队列清理，之后只观察取消后的迟到结果。
	a.cancel()
	done := make(chan struct{})
	go func() { a.is().wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(uiWaitLimit):
		t.Fatal("窗口关了，取图的 goroutine 没退出")
	}
	if q.pending() != 0 {
		t.Fatalf("关窗口以后还有 %d 个活要回界面线程", q.pending())
	}
}

// 晚到的结果：记录已经被换掉（扔了又重建）时，旧的结果不能写进新记录
func TestStaleLandingIgnored(t *testing.T) {
	t.Parallel()
	n := &imgNet{orig: map[string][2]int{"h": {50, 50}}}
	a, q := imgApp(t, n)
	st := a.is()
	k := imgKey{"h", 128, false}
	old := st.get(k)
	st.wg.Wait()
	q.drain() // old 落地
	delete(st.m, k)
	st.bytes -= old.bytes
	fresh := st.get(k) // 重建，又在取
	st.land(k, old, ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 9, 9))), nil, nil, nil)
	if fresh.state != imgLoading || fresh.bmp != nil || st.bytes != 0 {
		t.Fatalf("旧记录的结果写进了新记录：%+v，总字节 %d（要 0：旧结果不能记账）", fresh, st.bytes)
	}
	st.wg.Wait()
	q.drain()
	if fresh.state != imgOK {
		t.Fatalf("新记录没落地：%+v", fresh)
	}
}

// 模拟数据整页渲染一遍：走真的 mockImageBytes → decodeStatic，不 panic，图能到
func TestMockImagesRender(t *testing.T) {
	p, err := demoReader{}.ReadMessages(context.Background(), api.ConversationRequest{Key: demoKey("qq:group:1001"), Limit: 120}, "")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []*msg
	for _, v := range p.Value.Items {
		msgs = append(msgs, msgFromView(demoKey("qq:group:1001"), v, charOf))
	}
	a, q := imgApp(t, &imgNet{}, msgs...)
	a.imgFetch, a.reader = nil, demoReader{} // 演示的哈希（mock:…）走演示数据的 ReadMedia
	a.takePage(msgs, nil, false)
	tt := testerOf(1000, 800, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if a.is().count() == 0 {
		t.Fatal("一张模拟图都没到")
	}
}
