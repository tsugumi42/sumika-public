// Package linkcard 只负责已过政策和文字守卫的链接卡片，不读网络或文件。
package linkcard

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	// Steam 创意工坊等站的封面是 WebP，常是 VP8X 扩展格式（x/image/webp 只认基础格式，会报 invalid format）；
	// 这个纯 Go 解码器认全部 WebP（含动图，取第一帧），导入后 image.Decode 才认。根包的动图解码也用它。
	_ "github.com/gen2brain/vpx/webp"
)

const Width = 720
const maxHeight = 960

// DefaultTweetMaxHeight 是推特卡片高度封顶的默认值（图上像素，linkReader.expand.tweetCard.maxHeight）：
// 0 不封顶（用户 2026-11-13 定，QQ 这边默认不封顶），长推文整张画出来，但仍有硬上限 TweetHardMaxHeight，到了再按截断规则截；
// 要封顶写 480～8000 的像素值，旧的封顶是 960。通用卡片（Render）的 maxHeight 960 是另一回事，不动。
const (
	DefaultTweetMaxHeight = 0
	TweetHardMaxHeight    = 8000
	// tweetMinMaxHeight 是封顶值的下限：再小连头像加一两行字都排不下
	TweetMinMaxHeight = 480
)
const maxEncoded = 2_000_000

// Data 中的文字须由宿主先检查；封面是完成解码的位图，绘制不做 I/O。
type Data struct {
	Title, Author, DateDuration string
	Stats                       []string
	Cover                       *ui.Bitmap
	// CoverIssue 非空说明有封面但没取到或解不开：画一块占位并写这句话。
	// Cover 和 CoverIssue 都空说明这个页面本来就没有封面（比如 Mastodon 帖子），不画封面区。
	CoverIssue string
}
type FieldLayout struct {
	Name, Text string
	X, Y, W, H int
}
type Output struct {
	Bytes         []byte
	MIME          string
	Width, Height int
	Layout        []FieldLayout
	// Theme、Coverage 只有推特卡片有：用的配色，和这张卡上的字由哪个字体画（fonts.go）
	Theme    string
	Coverage *Coverage
}

// Options 是渲染器的设置；零值能用（配色按北京时间、字体从 exe 旁的 fonts/ 和系统字体目录找）。
type Options struct {
	// Theme 每次画推特卡片时问一次：auto / light / dark（linkReader.expand.tweetCard.theme）；nil 当 auto
	Theme func() string
	// MaxHeight 每次画推特卡片时问一次：卡片高度封顶（图上像素，linkReader.expand.tweetCard.maxHeight）；
	// 0 不封顶（长推文整张画出来，硬上限 TweetHardMaxHeight）；nil 或负数当 DefaultTweetMaxHeight（0，不封顶）
	MaxHeight func() int
	// Now 是时钟，配色按它的北京时间；nil 用 time.Now
	Now func() time.Time
	// Logf 记字体没加载、缺字兜底这类事；nil 不记
	Logf func(string, ...any)
	// FontPaths 是文渊圆体的候选文件，取第一个读得到的；空就找 exe 旁边 fonts/ 里的
	FontPaths []string
	// FallbackFontPaths 是 Maple Mono NF CN 常规体的候选文件（同目录的 -Bold 一起注册）；nil 找系统和当前用户的字体目录，空切片表示不用
	FallbackFontPaths []string
	// EmojiDir 是 Twemoji 的 SVG 目录（文件名是码位，如 1f44d.svg）；空就找 exe 旁边的 twemoji/，再找本机开发用的 native/twemoji。
	// 都没有时表情交字体画，并写一条加载问题
	EmojiDir string
	// register 只给测试换：注册字体给 mygo；nil 用 uiFonts（同一份字体一个进程只注册一次）
	register func([]byte, string) error
}
type request struct {
	ctx   context.Context
	data  Data
	tweet *Tweet
	done  chan response
}
type response struct {
	out Output
	err error
}

// Renderer 独占离屏 Context；并发调用排入同一个 worker。
type Renderer struct {
	jobs  chan request
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	opt   Options
	fonts *fontSet // 第一次画推特卡片时在 worker 里加载
}

func New() *Renderer { return NewWith(Options{}) }

// NewWith 按设置建渲染器（推特卡片要配色、时钟、字体）。
func NewWith(o Options) *Renderer {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.register == nil {
		o.register = uiFonts // 同一份字体一个进程只交给 mygo 一次（fontonce.go）
	}
	r := &Renderer{jobs: make(chan request), stop: make(chan struct{}), done: make(chan struct{}), opt: o}
	go func() {
		defer close(r.done)
		for {
			select {
			case <-r.stop:
				return
			case j := <-r.jobs:
				if err := j.ctx.Err(); err != nil {
					j.done <- response{err: err}
					continue
				}
				var out Output
				var err error
				if j.tweet != nil {
					out, err = r.renderTweet(*j.tweet)
				} else {
					out, err = render(j.data)
				}
				j.done <- response{out: out, err: err}
			}
		}
	}()
	return r
}

// tweetCap 是这一刻推特卡片的高度封顶（图上像素）：0 是不封顶；超过硬上限的按硬上限，太小的按下限。
func (r *Renderer) tweetCap() int {
	v := DefaultTweetMaxHeight
	if r.opt.MaxHeight != nil {
		if c := r.opt.MaxHeight(); c >= 0 {
			v = c
		}
	}
	if v > 0 {
		v = min(max(v, TweetMinMaxHeight), TweetHardMaxHeight)
	}
	return v
}

// Uncapped 说现在推特卡片是不是不封顶（宿主据此决定要不要把长文里的配图按段落位置交进来）。
func (r *Renderer) Uncapped() bool { return r.tweetCap() == 0 }

func (r *Renderer) Close() { r.once.Do(func() { close(r.stop) }); <-r.done }
func (r *Renderer) Render(ctx context.Context, d Data) (Output, error) {
	return r.submit(ctx, request{ctx: ctx, data: d, done: make(chan response, 1)})
}

// RenderTweet 画一张推特卡片（tweet.go）；文字须由宿主先过文字守卫（Tweet.Semantic）。
func (r *Renderer) RenderTweet(ctx context.Context, t Tweet) (Output, error) {
	return r.submit(ctx, request{ctx: ctx, tweet: &t, done: make(chan response, 1)})
}

// renderTweet 在 worker 里跑：字体第一次用时加载，加载问题和缺字写日志
func (r *Renderer) renderTweet(t Tweet) (Output, error) {
	if r.fonts == nil {
		r.fonts = loadFonts(r.opt, r.opt.register, theme.Light().Typography().Font())
		for _, s := range r.fonts.issues {
			r.logf("推特卡片：%s", s)
		}
	}
	setting := ThemeAuto
	if r.opt.Theme != nil {
		setting = r.opt.Theme()
	}
	th := ThemeAt(setting, r.opt.Now())
	texts := []string{t.Author.Name, t.Author.Account, t.Text, beijingStamp(t.Published)}
	if t.Quote != nil {
		texts = append(texts, t.Quote.Author.Name, t.Quote.Author.Account, t.Quote.Text)
	}
	cov := r.fonts.check(texts...)
	if len(cov.Missing) > 0 {
		r.logf("推特卡片：%s", Coverage{Missing: cov.Missing, ByFallback: cov.ByFallback, Uncovered: cov.Uncovered}.Summary())
	}
	out, err := renderTweet(t, r.fonts, th, r.tweetCap())
	if err != nil {
		return Output{}, err
	}
	out.Theme, out.Coverage = th, &cov
	return out, nil
}

func (r *Renderer) logf(format string, args ...any) {
	if r.opt.Logf != nil {
		r.opt.Logf(format, args...)
	}
}

func (r *Renderer) submit(ctx context.Context, j request) (Output, error) {
	select {
	case <-ctx.Done():
		return Output{}, ctx.Err()
	case <-r.stop:
		return Output{}, errors.New("卡片渲染已关闭")
	case r.jobs <- j:
	}
	select {
	case <-ctx.Done():
		return Output{}, ctx.Err()
	case v := <-j.done:
		return v.out, v.err
	}
}

// DecodeCover 在分配像素之前检查实际容器尺寸，不把封面尺寸当视频尺寸。
func DecodeCover(b []byte) (*ui.Bitmap, error) { return DecodeCoverReader(bytes.NewReader(b)) }
func DecodeCoverReader(r io.ReadSeeker) (*ui.Bitmap, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, errors.New("封面像素超限")
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	return ui.NewBitmap(img), nil
}

func render(d Data) (Output, error) {
	var out Output
	var layoutErr error
	t := theme.Light()
	h := 0
	var bg ui.Color
	img := ui.Render(func(c *ui.Context) {
		theme.Apply(c, t)
		bg = c.Theme().Background
		c.Root().Background(ui.Transparent) // 根节点默认铺主题底色（不透明），文字落在上面会按子像素画
		out.Layout = out.Layout[:0]
		y := 24
		// 不给整张画布铺不透明底：文字落在不透明背景上会按子像素（LCD）抗锯齿画，一像素宽的竖线和字边带红蓝彩边（新 B-3）。
		// 先画在透明画布上（灰度抗锯齿），出图前再合到背景色上（render 末尾）。
		if d.Cover != nil || d.CoverIssue != "" {
			ui.Box(c).Absolute().Left(24).Top(float32(y)).Size(672, 378).Background(c.Theme().Surface).Radius(16).Clip().Children(func() {
				if d.Cover != nil {
					ui.Image(c, d.Cover).Fill().Fit(ui.Contain)
				} else {
					ui.Text(c, "封面不可用").FontSize(24).TextColor(c.Theme().TextMuted)
				}
			})
			out.Layout = append(out.Layout, FieldLayout{Name: "cover", X: 24, Y: y, W: 672, H: 378})
			y += 378 + 16
		}
		add := func(name, text string, size, line float32, limit int, gap int) {
			if strings.TrimSpace(text) == "" {
				return // 没有的字段不占一行（以前卡片下面空出一块，Steam、Spotify、TikTok 样张）
			}
			_, measured := c.MeasureText(672, ui.Span{Text: text, Font: t.Typography().Font(), Size: size})
			// 只用实际字体度量确定行数；固定行高与绘制使用同一字阶。
			_, one := c.MeasureText(0, ui.Span{Text: "国Ag", Font: t.Typography().Font(), Size: size})
			n := max(1, int(math.Ceil(float64(measured/max(one, 1)))))
			if limit > 0 {
				n = min(n, limit)
			}
			height := n * int(line)
			if y+height+26 > maxHeight {
				layoutErr = errors.New("卡片关键数据无法在960像素内排完")
				return
			}
			ui.Text(c, text).Absolute().Left(24).Top(float32(y)).Size(672, float32(height)).Font(t.Typography().Font()).FontSize(size).FixedLineHeight(line).MaxLines(n).TextColor(c.Theme().Text)
			out.Layout = append(out.Layout, FieldLayout{Name: name, Text: text, X: 24, Y: y, W: 672, H: height})
			y += height + gap
		}
		add("title", d.Title, 30, 40, 2, 16)
		add("author", d.Author, 28, 36, 1, 16)
		add("date-duration", d.DateDuration, 24, 32, 0, 0)
		for _, s := range d.Stats {
			add("stats", s, 24, 32, 0, 0)
		}
		if d.CoverIssue != "" {
			add("cover-issue", d.CoverIssue, 24, 32, 0, 0)
		}
		h = y + 26
	}, Width, maxHeight, 1)
	if layoutErr != nil {
		return Output{}, layoutErr
	}
	if h > maxHeight {
		return Output{}, errors.New("卡片高度超限")
	}
	flat := image.NewRGBA(img.Bounds())
	draw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.RGBA{R: bg.R, G: bg.G, B: bg.B, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
	if err := encode(&out, flat, h); err != nil {
		return Output{}, err
	}
	return out, nil
}

// encode 把渲染出的图裁到 h 高、编成 PNG，超过 2 MB 改 JPEG 90，再超就报错
func encode(out *Output, img *image.RGBA, h int) error {
	out.Width, out.Height = Width, h
	cropped := img.SubImage(image.Rect(0, 0, Width, h))
	var b bytes.Buffer
	if err := png.Encode(&b, cropped); err != nil {
		return err
	}
	out.MIME = "image/png"
	// 超过 2 MB 改 JPEG 90；很高的推特卡片（不封顶的长推文）还可能超，再降到 80、70
	for _, q := range []int{90, 80, 70} {
		if b.Len() <= maxEncoded {
			break
		}
		b.Reset()
		if err := jpeg.Encode(&b, cropped, &jpeg.Options{Quality: q}); err != nil {
			return err
		}
		out.MIME = "image/jpeg"
	}
	if b.Len() > maxEncoded {
		return errors.New("卡片编码超过2 MB")
	}
	out.Bytes = b.Bytes()
	return nil
}

func (d Data) Semantic() string {
	var lines []string
	for _, s := range append([]string{d.Title, d.Author, d.DateDuration}, d.Stats...) {
		if strings.TrimSpace(s) != "" {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n")
}
