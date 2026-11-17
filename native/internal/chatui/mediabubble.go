package chatui

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 图片气泡（照 Telegram Desktop）：气泡里最前面是图（名字、引用卡不算）就铺满，下面几种都是同一套，
// 名字、引用卡、配文、表情回应条自己缩进一个竖内边距，图始终贴着气泡左右边，圆角由气泡裁：
//   - 只有图片：图铺满气泡、不留内边距，四角随气泡的圆角裁切，时间是叠在图右下角的半透明小牌子；
//   - 图 + 配文：图在上、铺满气泡宽度，文字区照常有内边距，时间跟在文字最后一行；
//   - 多张：按张数分行拼成相册，图间留 albumGap 的细缝；
//   - 引用卡在图上面：卡离气泡上、左、右、下都是竖内边距，图接在卡下面（和群聊名字在图上面是同一个道理）；
//   - 表情回应条在图下面：条离气泡左、右、下是竖内边距，离图也是竖内边距；有它时时间挂在条的行尾，不叠在图上；
//   - 注释（已编辑这类）写在时间旁边，叠在图上时和时间同一块牌子；
//   - 贴纸：什么都不带时不进气泡、没有底，只画贴纸本身，时间牌子叠在贴纸右下；带引用或表情回应时进气泡，贴纸完整显示不裁。
//   - 图不在最前面（文字在前，图文混排）：图缩在内边距里，仍用 albumLayout 排，圆角取最小内层圆角（imageGroup）。
//
// 圆角不另算：气泡自己 Clip，图是方的，气泡的圆角裁出外侧四角；
// 配文接在图下面时图的下边是直的，群聊名字在图上面时图的上边也是直的，天然同心。
// 占位阶段和加载完用同一个尺寸（只看 Segment 里的宽高），加载完不跳。
//
// 尺寸上限取 Telegram Desktop 的量级（原型没有图片气泡）：单张最大 320×360，窄的竖图最窄 120；
// 宽度最后还要受气泡最大宽限制。
const (
	mediaMaxW  = 320
	mediaMaxH  = 360
	mediaMinW  = 120
	mediaMinH  = 60
	albumGap   = 2
	stickerMax = 180
)

type mediaMode int

const (
	mediaNone    mediaMode = iota // 图不在最前面（或没有图）：图缩在内边距里面
	mediaFlush                    // 图铺满气泡
	mediaSticker                  // 贴纸：没有气泡
)

func isStickerSeg(s segview.Segment) bool { return s.Sticker || s.Kind == "emoji" }

// isMediaBlock 图片和视频封面是"媒体块"：铺满气泡、时间叠在上面。
func isMediaBlock(b segview.Block) bool { return b.Kind == segview.Images || b.Kind == segview.Video }

// mediaModeOf 一条消息用哪种媒体排法：最前面的块是图或视频封面就铺满，有没有引用、表情回应、注释都一样。
func mediaModeOf(m *Msg) mediaMode {
	if len(m.Parts) > 0 {
		return partsMediaMode(m)
	}
	if len(m.Blocks) == 0 || !isMediaBlock(m.Blocks[0]) {
		return mediaNone
	}
	first := m.Blocks[0]
	bare := m.Quote == nil && len(m.Reactions) == 0 && len(m.Notes) == 0 // 带了东西的贴纸要有气泡托着
	if bare && first.Kind == segview.Images && len(m.Blocks) == 1 && len(first.Imgs) == 1 && isStickerSeg(first.Imgs[0]) {
		return mediaSticker
	}
	return mediaFlush
}

// albumRow 相册的一行：每格的宽，行高。
type albumRow struct {
	W []float32
	H float32
}

type album struct {
	Rows []albumRow
	W, H float32
}

// singleMediaSize 单张图按比例缩到上限内；太窄的竖图撑到最小宽（再裁），太扁的横图撑到最小高。
func singleMediaSize(s segview.Segment, maxW float32) (float32, float32) {
	iw, ih := float32(s.Width), float32(s.Height)
	if iw <= 0 || ih <= 0 {
		iw, ih = mediaMaxW, mediaMaxW*3/4
	}
	if isStickerSeg(s) {
		k := min(stickerMax/iw, stickerMax/ih)
		return max(1, min(iw*k, maxW)), max(1, ih*k)
	}
	top := min(float32(mediaMaxW), maxW)
	// 小图不放大（设计语言：“单张按原比例缩进 320 × 360 以内，小图不放大”），只受最小宽高兜底。
	k := min(1, top/iw, mediaMaxH/ih)
	w, h := iw*k, ih*k
	w = max(w, min(mediaMinW, top))
	h = max(h, mediaMinH)
	return w, h
}

var albumShapes = [][]int{
	2: {2}, 3: {1, 2}, 4: {2, 2}, 5: {2, 3}, 6: {3, 3}, 7: {1, 3, 3}, 8: {2, 3, 3}, 9: {3, 3, 3},
}

// albumLayout 一块图片的排法：单张按比例，多张分行、每行格子等宽、格高等于格宽（单格行取宽的 0.65）。
func albumLayout(imgs []segview.Segment, maxW float32) album {
	if len(imgs) == 1 {
		w, h := singleMediaSize(imgs[0], maxW)
		return album{Rows: []albumRow{{W: []float32{w}, H: h}}, W: w, H: h}
	}
	w := min(float32(mediaMaxW), maxW)
	var shape []int
	if len(imgs) < len(albumShapes) {
		shape = albumShapes[len(imgs)]
	} else {
		for left := len(imgs); left > 0; left -= 3 {
			shape = append(shape, min(3, left))
		}
	}
	a := album{W: w}
	for _, k := range shape {
		cw := (w - float32(k-1)*albumGap) / float32(k)
		row := albumRow{H: cw}
		if k == 1 {
			row.H = w * 0.65
		}
		for i := 0; i < k; i++ {
			row.W = append(row.W, cw)
		}
		a.Rows = append(a.Rows, row)
		a.H += row.H
	}
	a.H += float32(len(a.Rows)-1) * albumGap
	return a
}

// mediaWidth 铺满的气泡有多宽：所有图片块里最宽的那块。
func mediaWidth(m *Msg, maxW float32) float32 {
	var w float32
	for _, b := range m.Blocks {
		switch b.Kind {
		case segview.Images:
			w = max(w, albumLayout(b.Imgs, maxW).W)
		case segview.Video:
			vw, _ := singleMediaSize(b.Video, maxW)
			w = max(w, vw)
		}
	}
	return w
}

// infoChip 是叠在图上的时间牌子：半透明深色底、白字，贴图右下角。平时不显示，鼠标放到这条消息上才淡入（主人 2026-11-09）。
// 字到气泡右边、下边和普通气泡的时间一样远，两种时间看起来是同一条线。
func (h Host) infoChip(c *ui.Context, l layout, hovered bool) {
	t := h.T
	const padX, padY = 7, 2
	// 窄图配较长的注释时牌子最宽到图宽减去两边的留白，放不下的字被裁掉，不撑出气泡
	maxW := max(1, l.ContentW+2*t.BubblePadH-2*(t.BubblePadH-padX))
	to := float32(0)
	if hovered {
		to = 1
	}
	chip := ui.Row(c.Key("time")).Label("时间牌子").Absolute().Right(t.BubblePadH-padX).Bottom(max(0, l.InfoBottom-padY)).Padding(padY, padX).AlignItems(ui.Center).MaxWidth(maxW).
		Background(ui.RGBA(0, 0, 0, 0.5)).Radius(999).PassThrough()
	chip.Opacity(chip.AnimateTo("shown", to, ui.Animation{Duration: t.Fast, NonMotion: true}))
	chip.Children(func() {
		ui.Text(c, l.Info).Font(h.Font).FontSize(t.FS[0]).FontFeatures(Tabular).TextColor(ui.RGB(255, 255, 255)).SingleLine().MinWidth(0)
	})
}
