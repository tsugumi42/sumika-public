package main

// 小黄脸（规格-原生界面第二版 3.1～3.3、4.5）：
//   1. 名字表（api.Reader.ReadFaceIndex，后台只读 cache/qq/faces/index.json）：空或取不到当没就绪，每 30 秒重取直到非空；
//   2. 图（ReadFacePNG）：用到时才取，结果按 id 缓存，状态 loading / ok / missing，不设上限（索引只有几百项）；
//      404 或取不到记 missing，同一个 id 只发一次请求，切会话时清掉 missing 才再取一次；不自动重试；
//   3. 一个脸怎么显示（resolveFace，纯函数）：图 / 字符 / 文字 [名字]。
// 状态只在界面线程读写；取数据的 goroutine 带 ctx，窗口关了（app.ctx 取消）就不再回写。

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"golang.org/x/image/draw"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// faceInfo 是名字表里的一项：{name, kind, char?}。kind 是 classic / super / emoji 之类，
// 索引决定用图片还是字符（规格 3.3）；没有索引时按 DTO Kind 生成回退文字。
type faceInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Char string `json:"char"`
}

const (
	faceLoading = iota + 1
	faceOK
	faceMissing
)

type faceEntry struct {
	state int
	bmp   *ui.Bitmap
	anim  *animation // 动图的帧；静态图是 nil
}

const (
	faceMaxPx    = 64 // 图缩到长边 64 像素：框是 14 × 1.375 ≈ 19 DIP，按 2 倍约 39 像素，64 留余量（规格 3.2）
	faceFetchers = 4  // 同时取图的上限，礼貌一点
)

// errNotFound 是 404：这个 id 没有图
var errNotFound = errors.New("没有这个小黄脸")

type faceStore struct {
	ctx  context.Context
	post func(func()) // 回界面线程；窗口关了之后不会再执行

	fetchTable func(ctx context.Context) (map[string]faceInfo, error)
	fetchPNG   func(ctx context.Context, id string) ([]byte, error)
	retry      time.Duration // 名字表是 {} 或取不到时隔多久再取

	// 以下只在界面线程读写
	table map[string]faceInfo
	ver   int // 名字表换了、某个图确认没有（文字替换了框）、missing 被清掉：排版缓存按它判断要不要重排
	imgs  map[string]*faceEntry
	// ticking：已经排了一次重画，动的脸共用一个定时器
	ticking bool

	sem chan struct{}
	wg  sync.WaitGroup // 测试用：等 goroutine 都退出，查泄漏
}

func newFaceStore(ctx context.Context, post func(func())) *faceStore {
	return &faceStore{ctx: ctx, post: post, retry: 30 * time.Second,
		imgs: map[string]*faceEntry{}, sem: make(chan struct{}, faceFetchers)}
}

// start 取名字表。返回 {} 视为没就绪（后台还没挑 QQ 目录或同步没完时返回 200 和 {}，不是失败），
// 取不到也一样，隔 retry 再取，直到拿到非空的
func (f *faceStore) start() {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			tbl, err := f.fetchTable(f.ctx)
			if f.ctx.Err() != nil {
				return
			}
			if err == nil && len(tbl) > 0 {
				f.post(func() {
					if f.ctx.Err() == nil {
						f.table = tbl
						f.ver++
					}
				})
				return
			}
			t := time.NewTimer(f.retry)
			select {
			case <-t.C:
			case <-f.ctx.Done():
				t.Stop()
				return
			}
		}
	}()
}

// want 要用这个 id 的图：没有记录就发起一次取。界面线程里调（视图构建时，不在绘制回调里）
func (f *faceStore) want(id string) {
	if _, ok := f.imgs[id]; ok {
		return
	}
	f.imgs[id] = &faceEntry{state: faceLoading}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		select {
		case f.sem <- struct{}{}:
			defer func() { <-f.sem }()
		case <-f.ctx.Done():
			return
		}
		var bmp *ui.Bitmap
		var an *animation
		data, err := f.fetchPNG(f.ctx, id)
		if err == nil {
			bmp, an = decodeFaceAnimated(data)
		}
		if f.ctx.Err() != nil {
			return
		}
		f.post(func() {
			e := f.imgs[id]
			if f.ctx.Err() != nil || e == nil || e.state != faceLoading {
				return
			}
			if bmp != nil {
				e.state, e.bmp, e.anim = faceOK, bmp, an
				return
			}
			e.state = faceMissing
			f.ver++ // 框换成了文字，用到这个脸的排版要重排
		})
	}()
}

// decodeFaceAnimated 解一个脸：APNG 解出全部帧（循环播，不管文件里写的遍数）；别的只有一张。
func decodeFaceAnimated(data []byte) (*ui.Bitmap, *animation) {
	if kind, n := sniffAnim(data); kind == animAPNG && n > 1 {
		if an, bmp, err := decodeAnimated(data, faceMaxPx); err == nil && an != nil && bmp != nil {
			an.plays = 0
			return bmp, an
		}
	}
	return decodeFace(data), nil
}

// animate 视图构建到一个脸时调：动的就推到当前帧，排下一次重画。滚出屏幕不再构建，定时器就不再续。
func (f *faceStore) animate(id string, still bool) {
	e := f.imgs[id]
	if e == nil || e.anim == nil {
		return
	}
	an := e.anim
	want, wait := 0, time.Duration(0)
	if !still {
		now := time.Now()
		if an.startedAt.IsZero() {
			an.startedAt = now
		}
		want, wait = an.frameAt(now.Sub(an.startedAt))
	}
	if want != an.cur {
		e.bmp.Update(an.frames[want])
		an.cur = want
	}
	if wait > 0 && !f.ticking && f.post != nil {
		f.ticking = true
		time.AfterFunc(wait, func() { f.post(func() { f.ticking = false }) })
	}
}

// peek 这个 id 的图，有就给，没有（还在取、没有这个图）返回 nil。绘制回调里读它，不发起请求
func (f *faceStore) peek(id string) *ui.Bitmap {
	if e := f.imgs[id]; e != nil && e.state == faceOK {
		return e.bmp
	}
	return nil
}

func (f *faceStore) state(id string) int {
	if e := f.imgs[id]; e != nil {
		return e.state
	}
	return 0
}

// clearMissing 切会话时调：确认没有的图下次用到再取一次（规格 3.2，不计时、不计次）
func (f *faceStore) clearMissing() {
	n := 0
	for id, e := range f.imgs {
		if e.state == faceMissing {
			delete(f.imgs, id)
			n++
		}
	}
	if n > 0 {
		f.ver++
	}
}

// decodeFace PNG → 缩到长边 faceMaxPx 的位图。用 png.Decode 不用 image.Decode（规格第五节第 12 条：注册撞车）。认不出返回 nil
func decodeFace(data []byte) *ui.Bitmap {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if w > faceMaxPx || h > faceMaxPx {
		if w >= h {
			w, h = faceMaxPx, max(h*faceMaxPx/w, 1)
		} else {
			w, h = max(w*faceMaxPx/h, 1), faceMaxPx
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = dst
	}
	return ui.NewBitmap(img)
}

// ───────────── 一个脸怎么显示（规格 3.3）─────────────

type faceKind int

const (
	faceAsText  faceKind = iota // 文字：[名字]、[表情<id>]
	faceAsChar                  // 字符：emoji 类，键就是字符
	faceAsImage                 // 行内框里画图
)

type faceRes struct {
	Kind faceKind
	Text string // 文字、字符：排进富文本的字；图：空
	Copy string // 复制出来的字：图是 [名字] 或 [表情<id>]，字符是字符本身，文字就是 Text
}

// numericID 小黄脸图的接口只接受 1 到 6 位数字（daemon/plugins/qq-console.ts:562-569）
func numericID(id string) bool {
	if len(id) < 1 || len(id) > 6 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resolveFace 按顺序，第一条命中即停（规格 3.3）：
//  1. id 为空：[表情]；
//  2. 名字表命中且 kind=emoji、有 char：用字符；
//  3. 命中、有图：画图，复制 [名字]；这个图确认没有（missing）时退成文字 [名字]；
//  4. 没命中，或 id 不是数字串：数字就先试图（复制按 [表情<id>]），missing 或不是数字显示 [表情<id>]（超级表情 [超级表情<id>]）。
//
// state 查图的状态（faceLoading / faceOK / faceMissing，没记录是 0）；后台线程预排时没有这些状态，传 nil 当作都没确认没有
func resolveFace(r segview.FaceRef, table map[string]faceInfo, state func(id string) int) faceRes {
	if r.ID == "" {
		return faceRes{Kind: faceAsText, Text: "[表情]", Copy: "[表情]"}
	}
	missing := state != nil && state(r.ID) == faceMissing
	if info, hit := table[r.ID]; hit {
		name := info.Name
		if name == "" {
			name = segview.FaceFallback(r.ID, r.Kind)
		} else {
			name = "[" + name + "]"
		}
		switch {
		case info.Kind == "emoji" && info.Char != "":
			return faceRes{Kind: faceAsChar, Text: info.Char, Copy: info.Char}
		case info.Kind == "emoji" || missing: // emoji 类没有 char 也没有图
			return faceRes{Kind: faceAsText, Text: name, Copy: name}
		}
		return faceRes{Kind: faceAsImage, Copy: name}
	}
	fb := segview.FaceFallback(r.ID, r.Kind)
	if !numericID(r.ID) || missing {
		return faceRes{Kind: faceAsText, Text: fb, Copy: fb}
	}
	return faceRes{Kind: faceAsImage, Copy: fb}
}

// ───────────── 取数据（只读 GET）─────────────

// readFaceTable 名字表：读后台的只读资源（<data>/cache/qq/faces/index.json）；没有数据源或索引缺失都是错误，界面退成文字
func readFaceTable(ctx context.Context, r api.Reader) (map[string]faceInfo, error) {
	if r == nil {
		return nil, errNotFound
	}
	res, err := r.ReadFaceIndex(ctx)
	if err != nil {
		return nil, err
	}
	t := make(map[string]faceInfo, len(res.Value.Items))
	for _, f := range res.Value.Items {
		t[f.ID] = faceInfo{Name: f.Name, Kind: f.Kind, Char: f.Char}
	}
	return t, nil
}

// readFacePNG 一个脸的图；取不到一律当没有这个图（状态 missing，换会话再取一次）
func readFacePNG(ctx context.Context, r api.Reader, id string) ([]byte, error) {
	if r == nil || !numericID(id) {
		return nil, errNotFound
	}
	res, err := r.ReadFacePNG(ctx, id)
	if err != nil {
		return nil, errNotFound
	}
	return res.Value.Bytes, nil
}

// plainNamed 给 Markdown 提供纯文字输入，小黄脸用名字表里的名字，保留可读与复制语义。
func plainNamed(b segview.Block, table map[string]faceInfo) string {
	var sb strings.Builder
	for _, r := range b.Runs {
		if r.Face != nil {
			res := resolveFace(*r.Face, table, nil)
			if res.Kind == faceAsImage {
				sb.WriteString(res.Copy)
			} else {
				sb.WriteString(res.Text)
			}
			continue
		}
		sb.WriteString(r.Text)
	}
	return sb.String()
}
