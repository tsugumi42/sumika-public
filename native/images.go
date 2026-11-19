package main

// 静态图的取、解、存（规格-原生界面第二版 3.8、4.1、3.5 的统一入口里静态的那一半）：
//   1. 取：api.Reader.ReadMedia（后台给的凭据 + 档），档 = 显示宽 × 屏幕缩放比，取第一个不小于它的档（128 到 1536）。
//      后台按档复用磁盘缩略图；tier=0 读取原件（灯箱和动图），响应按字节头认格式。
//   2. 解：decodeStatic 按字节头分派，每种格式调自己的包（png.Decode、jpeg.Decode、gif.Decode、webp.Decode），不用 image.Decode：
//      规格第五节第 12 条，第八步加 pngn 以后注册表里会有两个 PNG；
//   3. 存：按 (平台, 哈希, 档, 静/动) 存，换了数据组整个丢掉（app.attach），总字节有上限，超了从最久没用的扔；视图最近两帧用过的不扔（扔了每次构建都重新请求）。
// 状态只在界面线程读写；取和解在后台线程，解码同时最多 imgDecoders 个，结果经 post 回来，窗口关了（ctx 取消）就不再回写。
// 取不到（网络不通、404、别的 HTTP 错误）不自动重试，点一下才再取一次；解不开（imgBad）重取也一样，所以点了不动。

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gen2brain/pngn"
	vpxwebp "github.com/gen2brain/vpx/webp"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"sumika-native/internal/api"
	"sumika-native/internal/mediastore"
)

const (
	lightboxTier = 0         // ADR-260：灯箱请求原文件，解码仍受像素预算约束。
	imgBudget    = 128 << 20 // 图缓存的字节上限（解码后的像素），规格 4.1
	imgDecoders  = 2         // 同时读取原文件并解码的条数，排队时不持有原图字节
	imgMaxPixels = 100e6     // 解码前先看尺寸，超过这个像素数的当解不开（6 万 × 6 万的 PNG 头只有几十字节，解开要几个 G）
	imgMaxH      = 4096      // 缩小以后的高度上限：很长的截图宽度够了，高度也不留超过这个
	jpegKeepEXIF = 2048      // JPEG 不大于这个就交给 mygo 解（它按 EXIF 转正）；更大的只能缩小，不转向
)

// thumbTiers 是 daemon/util/thumb.ts 的 THUMB_WIDTHS
var thumbTiers = []int{128, 256, 384, 512, 768, 1024, 1536}

// tierFor 按实际物理像素挑第一档够宽的缩略图；超过最大档就取最大档。
func tierFor(need float32) int {
	for _, t := range thumbTiers {
		if float32(t) >= need {
			return t
		}
	}
	return thumbTiers[len(thumbTiers)-1]
}

// coverCSS 铺满方格时图要显示多宽（DIP）：短边铺满，所以长边方向更宽
func coverCSS(cell float32, s seg) float32 {
	if s.Width > 0 && s.Height > 0 {
		return cell * max(1, float32(s.Width)/float32(s.Height))
	}
	return cell * 2
}

const (
	// 动图的帧缩到多少像素 = 显示宽 × 这个数（规格 4.3）：表情包按 2 倍（128 DIP 的框就是 256 像素，每帧约 256 KB）；
	// 普通动图按 1 倍（296 DIP 宽、4:3 约 263 KB 一帧；按 2 倍是 1 MB 一帧，20 张同屏每秒 10 帧超过 96 MB/s 的暂定线）。
	// 1 倍在 125% 缩放的屏上会有一点软，换上传量；真窗口看了不行再调
	animScaleSticker = 2.0
	animScalePlain   = 1.0
)

// animPx 动图的帧要缩到多宽（像素）：显示宽乘上面的倍数，不超过最大的一档
func animPx(cssW float32, sticker bool) int {
	scale := animScalePlain
	if sticker {
		scale = animScaleSticker
	}
	return min(int(math.Ceil(float64(cssW)*scale)), thumbTiers[len(thumbTiers)-1])
}

// keyOf 一张图（显示宽 cssW，DIP）要的那条缓存记录：动图按显示像素、静态图按档
func keyOf(s seg, cssW float32, sticker bool, scale float32) imgKey {
	if s.Animated {
		return imgKey{mediaID(s), animPx(cssW, sticker), true}
	}
	if scale == 0 {
		return imgKey{mediaID(s), -1, false}
	}
	return imgKey{mediaID(s), tierFor(cssW * scale), false}
}

// mediaID 缓存键里的图身份：平台 + 哈希。同一个哈希在 local 和 qq 是两份文件，不能共用（规格 5.1）
func mediaID(s seg) string {
	if s.Media != nil && s.Media.Source.Event.Platform != "" {
		return s.Media.Source.Event.Platform + "/" + s.mediaHash()
	}
	return s.mediaHash()
}

// refOf 取图的凭据来自展示段的媒体引用。
func refOf(s seg) api.MediaRef {
	if s.Media != nil {
		return *s.Media
	}
	return api.MediaRef{}
}

// stillBitmap 动图的第一帧位图（看大图用，规格 T-灯箱：和列表同一份产物，不另解码）。bmp 是正在播的，会被改写，所以另做一个，用到才建。
// 不是动图（或超预算只有首帧）就是 bmp 自己
func (e *imgEntry) stillBitmap() *ui.Bitmap {
	if e.anim == nil {
		return e.bmp
	}
	if e.still == nil {
		e.still = ui.NewBitmap(e.anim.frames[0])
	}
	return e.still
}

// playMode 这一帧里一个动图该怎么办
type playMode int

const (
	playLive  playMode = iota // 在屏幕上：按时间推帧，排下一次重画
	playHold                  // 构建了但不在可见范围（上下各 2 行的预构建、焦点行）：不推帧不排重画，画面停在原处
	playStill                 // 减少动态效果：只显示第一帧
)

// advance 视图里每次构建到一个动图时调：算该显示哪一帧，变了才 Update，还没到最后一帧就排下一次重画。界面线程。
// 多个动图各排各的 After，引擎只留最早的一个（第零步 h），所以同屏帧数看各自间隔的并集，不随张数线性增长
func (s *imageStore) advance(c *ui.Context, e *imgEntry, mode playMode) {
	an := e.anim
	if an == nil || mode == playHold {
		return
	}
	want := 0
	if mode == playLive {
		now := c.Now()
		if an.startedAt.IsZero() {
			an.startedAt = now
		}
		idx, wait := an.frameAt(now.Sub(an.startedAt))
		want = idx
		if wait > 0 {
			c.After(max(wait, time.Millisecond))
		}
	}
	if want != an.cur {
		e.bmp.Update(an.frames[want]) // 帧不可变，同尺寸，Update 不拷贝、复用纹理
		an.cur = want
		s.updates++
		s.uploaded += len(an.frames[want].Pix)
	}
}

type imgKey struct {
	hash string // 平台/哈希（mediaID）
	tier int    // 静态图是档（取的宽）；动图是缩放后帧的宽（像素），取的永远是原文件
	anim bool   // 静或动（规格 4.1）：同一个哈希的静态档和动图是两条记录
}

const (
	imgLoading = iota + 1
	imgOK
	imgFailed // 没取到：网络不通、404、别的 HTTP 错误。点一下再取一次
	imgBad    // 取到了但解不开（HEIC、AVIF、坏图、太大）。重取也一样，不重取
)

type imgEntry struct {
	state int
	bmp   *ui.Bitmap
	bytes int          // 解码后的字节数，只有 imgOK 才有（动图是所有帧加起来）
	anim  *animation   // 动图的帧；静态图、单帧、超预算只留首帧的都是 nil。bmp 是它正在播的那个位图
	still *ui.Bitmap   // 动图的第一帧，看大图用；用到才建（bmp 会被播放改写）
	touch uint64       // 最近一次 get 的序号，扔的时候先扔小的
	ref   api.MediaRef // 取图的凭据（第一次要这张图的那条消息给的）
	seen  uint64       // 最近一次 get 时的帧号，最近两帧用过的不扔
	job   *imgJob      // 加载任务；调度字段只在 imageStore.jobsMu 下访问
}

var errBad = errors.New("图片打不开")

type imgJob struct {
	k       imgKey
	e       *imgEntry
	seen    uint64
	touch   uint64
	visible bool
}

type imageStore struct {
	ctx  context.Context
	post func(func())

	fetch      func(ctx context.Context, ref api.MediaRef, tier int) ([]byte, error) // tier 为 0 是原文件（动图）
	decode     func(data []byte, tier int) (*ui.Bitmap, error)
	decodeAnim func(data []byte, tw int) (*animation, *ui.Bitmap, error)
	budget     int
	visible    func() bool // 界面线程查询当前行；独立缓存默认可见

	// 以下只在界面线程读写
	m           map[imgKey]*imgEntry
	bytes       int
	frame       uint64
	clock       uint64
	scale       float32 // 只从 Painter.Scale 取得；第一次绘制前不猜测缩略图档位。
	pruneQueued bool

	// 工人和界面共用的调度字段只在 jobsMu 下访问。
	jobsMu  sync.Mutex
	jobs    []*imgJob // 只排凭据，读取和解码共用两条流水线
	workers int
	wg      sync.WaitGroup // 测试用：等 goroutine 都退出
	decoded atomic.Int64   // 解码做了几次，测试用（视图每帧不该解码）

	// 播放的计数（界面线程）：推了几次帧、往 GPU 上传了多少字节（帧的 Pix 长度）。第十步量同屏动图的成本用
	updates, uploaded int
}

func newImageStore(ctx context.Context, post func(func())) *imageStore {
	return &imageStore{ctx: ctx, post: post, decode: decodeStatic, decodeAnim: decodeAnimated, budget: imgBudget,
		m: map[imgKey]*imgEntry{}}
}

// beginFrame 视图每帧开头调一次：这一帧和上一帧用过的图不会被扔
func (s *imageStore) beginFrame() { s.frame++ }

var scalePlaceholder = imgEntry{state: imgLoading}

// mygo 只在绘制时公开真实像素比例；记录比例后请求下一帧，取图仍由构建阶段调度。
// before 是同一个元素要先画的东西（一个元素只有一个 Draw，聊天底的壁纸借这里画）。
func (s *imageStore) observeScale(c *ui.Context, e ui.Element, before ...func(*ui.Painter, ui.Rect)) {
	services := c.Services()
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		for _, fn := range before {
			fn(p, r)
		}
		if scale := p.Scale(); scale != s.scale {
			s.scale = scale
			services.Invalidate()
		}
	})
}

// get 要这张图：没有记录就发起一次取；一定返回一个记录。界面线程里调（视图构建时）
func (s *imageStore) get(k imgKey) *imgEntry { return s.getRef(k, api.MediaRef{Hash: k.hash}) }

// getRef 同 get，带上取图的凭据（生产的段都有）
func (s *imageStore) getRef(k imgKey, ref api.MediaRef) *imgEntry {
	if k.tier < 0 {
		return &scalePlaceholder
	}
	e := s.m[k]
	fresh := e == nil
	if fresh {
		e = &imgEntry{state: imgLoading, ref: ref}
		s.m[k] = e
	}
	s.clock++
	e.touch, e.seen = s.clock, s.frame
	if e.state != imgLoading {
		return e
	}
	visible := s.visible == nil || s.visible()
	if fresh {
		s.start(k, e, visible)
	} else {
		s.jobsMu.Lock()
		j := e.job
		if j.seen != s.frame {
			j.visible = false
		}
		j.seen, j.touch, j.visible = s.frame, s.clock, j.visible || visible
		s.jobsMu.Unlock()
	}
	return e
}

// peek 看一眼，不发起取也不算用过。没有记录返回 nil
func (s *imageStore) peek(k imgKey) *imgEntry { return s.m[k] }

// retry 取不到的图再取一次（点击）。其余状态不动：还在取的不叠请求，解不开的重取也一样
func (s *imageStore) retry(k imgKey) {
	if e := s.m[k]; e != nil && e.state == imgFailed {
		e.state = imgLoading
		s.start(k, e, true)
	}
}

func (s *imageStore) start(k imgKey, e *imgEntry, visible bool) {
	j := &imgJob{k: k, e: e, seen: s.frame, touch: s.clock, visible: visible}
	e.job = j
	s.jobsMu.Lock()
	s.jobs = append(s.jobs, j)
	if s.workers < imgDecoders {
		s.workers++
		s.wg.Add(1)
		go s.worker()
	}
	s.jobsMu.Unlock()
	// 列表为测高度会暂建很多行。等这一轮布局稳定后再清队列，不让这些行占住可见图的工人。
	if !s.pruneQueued {
		s.pruneQueued = true
		s.post(func() {
			s.pruneQueued = false
			s.discardUnseen()
		})
	}
}

func (s *imageStore) discardUnseen() {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	kept := s.jobs[:0]
	for _, j := range s.jobs {
		if j.seen < s.frame {
			delete(s.m, j.k)
			continue
		}
		kept = append(kept, j)
	}
	clear(s.jobs[len(kept):])
	s.jobs = kept
}

func (s *imageStore) worker() {
	defer s.wg.Done()
	for {
		s.jobsMu.Lock()
		if len(s.jobs) == 0 || s.ctx.Err() != nil {
			s.workers--
			s.jobsMu.Unlock()
			return
		}
		best := 0
		for i, j := range s.jobs {
			b := s.jobs[best]
			if j.visible && !b.visible || j.visible == b.visible && (j.seen > b.seen || j.seen == b.seen && j.touch > b.touch) {
				best = i
			}
		}
		j := s.jobs[best]
		last := len(s.jobs) - 1
		s.jobs[best], s.jobs[last] = s.jobs[last], nil
		s.jobs = s.jobs[:last]
		s.jobsMu.Unlock()
		s.load(j)
	}
}

func (s *imageStore) load(j *imgJob) {
	k, e := j.k, j.e
	fetchTier := k.tier
	if k.anim {
		fetchTier = 0 // 动图取原文件：后台的缩略图只给静态图
	}
	data, err := s.fetch(s.ctx, e.ref, fetchTier)
	if s.ctx.Err() != nil {
		return
	}
	var bmp *ui.Bitmap
	var an *animation
	var derr error
	if err == nil {
		s.decoded.Add(1)
		if k.anim {
			an, bmp, derr = s.decodeAnim(data, k.tier)
		} else {
			tier := k.tier
			if tier == 0 {
				tier = int(imgMaxPixels) // 原图不按缩略宽缩小，仍受总像素和高度预算限制。
			}
			bmp, derr = s.decode(data, tier)
		}
		if s.ctx.Err() != nil {
			return
		}
	}
	s.post(func() { s.land(k, e, bmp, an, err, derr) })
}

// land 取回来的结果写进记录。记录已经被换掉了（扔了又重建）或不在取了就丢
func (s *imageStore) land(k imgKey, e *imgEntry, bmp *ui.Bitmap, an *animation, fetchErr, decodeErr error) {
	if s.ctx.Err() != nil || s.m[k] != e || e.state != imgLoading {
		return
	}
	e.job = nil
	switch {
	case fetchErr != nil:
		e.state = imgFailed
	case decodeErr != nil || bmp == nil:
		e.state = imgBad
	default:
		w, h := bmp.Size()
		e.state, e.bmp, e.bytes, e.anim = imgOK, bmp, w*h*4, an
		if an != nil {
			e.bytes = an.bytes()
		}
		s.bytes += e.bytes
		s.evict()
	}
}

// evict 总字节超了就从最久没用的扔，最近两帧用过的跳过（都在用就超着，下一帧再说）
func (s *imageStore) evict() {
	for s.bytes > s.budget {
		var worst imgKey
		var found *imgEntry
		for k, e := range s.m {
			if e.state != imgOK || s.frame-e.seen < 2 {
				continue
			}
			if found == nil || e.touch < found.touch {
				worst, found = k, e
			}
		}
		if found == nil {
			return
		}
		s.bytes -= found.bytes
		delete(s.m, worst)
	}
}

func (s *imageStore) count() (n int) {
	for _, e := range s.m {
		if e.state == imgOK {
			n++
		}
	}
	return
}

// is 共用解码、预算和播放规则；正文缓存跟随数据源，转发缓存跟随查看窗口。
func (a *app) is() *imageStore {
	slot := &a.imgs
	if a.forward.rendering != nil {
		slot = &a.forward.imgs
	}
	if *slot == nil {
		ctx := a.viewCtx
		if ctx == nil {
			ctx = a.base()
		}
		if a.forward.rendering != nil {
			ctx, a.forward.cancelImages = context.WithCancel(ctx)
		}
		st := newImageStore(ctx, a.post)
		st.visible = a.rowVisible
		r := a.reader
		st.fetch = func(ctx context.Context, ref api.MediaRef, tier int) ([]byte, error) {
			return loadImage(ctx, r, ref, tier)
		}
		if a.imgFetch != nil {
			st.fetch = a.imgFetch
		}
		st.fetch = withLocalFiles(st.fetch) // 输入框、气泡里的本地图片附件直接读文件（attach_images.go）
		*slot = st
	}
	return *slot
}

// ───────────── 取 ─────────────

// loadImage 取某一档的图。取不到一律返回 error，界面写"没取到，点一下再试"；文件缺失不等于过期，不在这里下结论
func loadImage(ctx context.Context, r api.Reader, ref api.MediaRef, tier int) ([]byte, error) {
	if r == nil {
		return nil, errors.New("后台没在运行")
	}
	res, err := r.ReadMedia(ctx, ref, api.MediaTier(tier))
	if err != nil {
		return nil, err
	}
	return res.Value.Bytes, nil
}

// ───────────── 解（按字节头分派）─────────────

// decodeStatic 一份图的字节 → 位图，长边缩到 tier 档需要的大小以内。认不出或解不开返回 errBad。
// 分派表（规格 3.5，T-reg 盯着它）：PNG 用 png.Decode，APNG 用 pngn.Decode（给默认图），JPEG 用 jpeg.Decode，GIF 用 gif.Decode（首帧），
// 静态 WebP 用 x/image/webp，动态 WebP 用 vpx 的 webp.Decode（合成后的首帧；x/image/webp 不认 ANMF，会报错），其余（BMP 等）交给 mygo。
// 加了 pngn 和 vpx 以后 image.Decode 对 PNG 和 WebP 会换人，所以消息字节一律不走 image.Decode；ui.DecodeBitmap 内部走它，
// 这里只有两处用（JPEG 不大于 2048、BMP 这类），喂给它的字节在前面的分支里已经排除了 PNG 和 WebP（T-reg）
func decodeStatic(data []byte, tier int) (*ui.Bitmap, error) {
	r := bytes.NewReader(data)
	var cfg image.Config
	var err error
	var dec func(*bytes.Reader) (image.Image, error)
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		if apngFrameCount(data) > 0 {
			cfg, err = pngn.DecodeConfig(r)
			dec = func(r *bytes.Reader) (image.Image, error) { return pngn.Decode(r) }
			break
		}
		cfg, err = png.DecodeConfig(r)
		dec = func(r *bytes.Reader) (image.Image, error) { return png.Decode(r) }
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		cfg, err = jpeg.DecodeConfig(r)
		if err == nil && max(cfg.Width, cfg.Height) <= jpegKeepEXIF && float64(cfg.Width)*float64(cfg.Height) <= imgMaxPixels {
			b, err := ui.DecodeBitmap(data) // 照片按 EXIF 转正
			if err != nil {
				return nil, errBad
			}
			return b, nil
		}
		dec = func(r *bytes.Reader) (image.Image, error) { return jpeg.Decode(r) }
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		cfg, err = gif.DecodeConfig(r)
		dec = func(r *bytes.Reader) (image.Image, error) { return gif.Decode(r) }
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		if _, anim := mediastore.WebPFrameCount(bytes.NewReader(data), int64(len(data))); anim {
			cfg, err = vpxwebp.DecodeConfig(r)
			dec = func(r *bytes.Reader) (image.Image, error) { return vpxwebp.Decode(r) }
			break
		}
		cfg, err = webp.DecodeConfig(r)
		dec = func(r *bytes.Reader) (image.Image, error) { return webp.Decode(r) }
	default:
		b, err := ui.DecodeBitmap(data)
		if err != nil {
			return nil, errBad
		}
		return b, nil
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || float64(cfg.Width)*float64(cfg.Height) > imgMaxPixels {
		return nil, errBad
	}
	r.Reset(data)
	img, err := dec(r)
	if err != nil {
		return nil, errBad
	}
	return ui.NewBitmap(fitPx(img, tier)), nil
}

// fitPx 太大的缩小：宽不超过 tier，高不超过 imgMaxH，比例不变。不大就原样
func fitPx(img image.Image, tier int) image.Image {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	nw, nh := fitSize(w, h, tier)
	if nw == w && nh == h {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

// fitSize w×h 的图缩到宽不超过 tier、高不超过 imgMaxH 以后的大小，比例不变，不放大（fitPx 和动图的帧共用）
func fitSize(w, h, tier int) (int, int) {
	k := min(1, float64(tier)/float64(w), float64(imgMaxH)/float64(h))
	if k >= 1 {
		return w, h
	}
	return max(int(math.Round(float64(w)*k)), 1), max(int(math.Round(float64(h)*k)), 1)
}
