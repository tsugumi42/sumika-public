package chatui

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 图片消息各种组合的几何不变量（组合清单见 imagemsg_combos_test.go）：
// 图始终贴着气泡左右边；引用卡、表情回应条自己缩进一个竖内边距；时间牌子在图里；
// 加载前、加载后、加载失败三种状态气泡矩形一样（虚拟列表不跳）。

func comboLayoutRender(t *testing.T, c imgCombo, res MediaResult) (Host, *ui.Tester) {
	t.Helper()
	h := comboHost(Derive(DefaultSettings()), "", c, res)
	return h, mbRender(t, h, c.Msg)
}

func within(in, out ui.Rect) bool {
	return in.X >= out.X-0.6 && in.Y >= out.Y-0.6 && in.X+in.W <= out.X+out.W+0.6 && in.Y+in.H <= out.Y+out.H+0.6
}

func isBareSticker(c imgCombo) bool { return c.Name == "sticker" }

func quoteLabel(c imgCombo) string {
	if len(c.Msg.Parts) > 0 {
		return "引用 乙：点一下回到原消息"
	}
	return "引用"
}

func hasReactions(c imgCombo) bool { return len(c.Msg.Reactions) > 0 }

func hasQuote(c imgCombo) bool {
	if c.Msg.Quote != nil {
		return true
	}
	return len(c.Msg.Parts) > 0 && c.Msg.Parts[0].Quote != nil
}

// 有没有文字或标签接在图后面（决定图是不是贴着气泡下边）。
func hasTextAfterImage(c imgCombo) bool {
	m := c.Msg
	if len(m.Parts) > 0 {
		p := m.Parts[0]
		return p.Text != "" || len(p.Atts) > 1 && !p.Atts[1].Image || len(m.Parts) > 1
	}
	return len(m.Blocks) > 1
}

func TestImageComboGeometry(t *testing.T) {
	for _, c := range imageCombos() {
		if c.Inset || isBareSticker(c) {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			h, tt := comboLayoutRender(t, c, mbReady)
			pv := h.T.BubblePadV
			bub, album := mustFind(t, tt, "气泡"), mustFind(t, tt, "相册")
			if !mbNear(album.X, bub.X) || !mbNear(album.W, bub.W) {
				t.Fatalf("图 %+v 应贴着气泡 %+v 的左右边", album, bub)
			}
			if album.W > mediaMaxW+0.5 || bub.W > h.Column*h.T.BubbleShare+0.5 {
				t.Fatalf("宽度超限：图 %.1f 气泡 %.1f", album.W, bub.W)
			}
			if !within(album, bub) {
				t.Fatalf("图 %+v 溢出气泡 %+v", album, bub)
			}
			if q := c.Msg.Quote; q != nil || len(c.Msg.Parts) > 0 && c.Msg.Parts[0].Quote != nil {
				card := mustFind(t, tt, quoteLabel(c))
				if !mbNear(card.X-bub.X, pv) || !mbNear(bub.X+bub.W-(card.X+card.W), pv) {
					t.Fatalf("引用卡 %+v 离气泡 %+v 左右该各 %.1f", card, bub, pv)
				}
				topGap := pv
				if c.Group {
					topGap = pv + h.T.NameLine + h.T.S1
				}
				if !mbNear(card.Y-bub.Y, topGap) {
					t.Fatalf("引用卡离气泡顶 %.1f，应是 %.1f", card.Y-bub.Y, topGap)
				}
				if !mbNear(album.Y-(card.Y+card.H), pv) {
					t.Fatalf("引用卡和图之间 %.1f，应是 %.1f", album.Y-(card.Y+card.H), pv)
				}
				// 圆角同心：卡的左上角是圆的（角上的像素和气泡内边距的颜色一样），不是直角
				if px := tt.Image(); px != nil && !c.Group {
					pad := px.RGBAAt(int(card.X+card.W/2), int(card.Y-pv/2))
					if got := px.RGBAAt(int(card.X)+1, int(card.Y)+1); got != pad {
						t.Fatalf("引用卡左上角 %v 应被圆角裁成气泡底色 %v", got, pad)
					}
					if got := px.RGBAAt(int(card.X+card.W/2), int(card.Y+card.H/2)); got == pad {
						t.Fatalf("引用卡中心 %v 不该和气泡底色一样", got)
					}
				}
			} else if !c.Group && !mbNear(album.Y, bub.Y) {
				t.Fatalf("图 %+v 应贴着气泡 %+v 的上边", album, bub)
			}
			// 配文离气泡左右至少一个竖内边距，不越过气泡右边
			for _, text := range tt.Texts() {
				if text == c.Caption {
					r := mustFind(t, tt, text)
					if r.X < bub.X+pv-0.6 || r.X+r.W > bub.X+bub.W-pv+0.6 {
						t.Fatalf("配文 %+v 越出气泡 %+v 的内边距", r, bub)
					}
				}
			}
			chip, hasChip := tt.Find("时间牌子")
			react, hasReact := tt.Find("表情回应")
			switch {
			case hasReactions(c):
				if !hasReact {
					t.Fatal("没找到表情回应条")
				}
				if hasChip {
					t.Fatal("有表情回应条时时间挂在条的行尾，不该叠在图上")
				}
				if !mbNear(react.X-bub.X, pv) || !mbNear(bub.X+bub.W-(react.X+react.W), pv) {
					t.Fatalf("表情回应条 %+v 离气泡 %+v 左右该各 %.1f", react, bub, pv)
				}
				if bottom := bub.Y + bub.H - (react.Y + react.H); bottom < pv-0.6 {
					t.Fatalf("表情回应条离气泡底只有 %.1f，应不小于 %.1f", bottom, pv)
				}
				if !hasTextAfterImage(c) && !mbNear(react.Y-(album.Y+album.H), pv) {
					t.Fatalf("表情回应条和图之间 %.1f，应是 %.1f", react.Y-(album.Y+album.H), pv)
				}
			case hasTextAfterImage(c):
				if hasChip {
					t.Fatal("有配文时时间在文字行尾，不该是图上的牌子")
				}
			default:
				if !hasChip {
					t.Fatal("纯图（含引用、注释）的时间该叠在图上")
				}
				if !within(chip, album) || !within(chip, bub) {
					t.Fatalf("时间牌子 %+v 溢出图 %+v", chip, album)
				}
				if !mbNear(album.Y+album.H, bub.Y+bub.H) {
					t.Fatalf("图 %+v 应贴着气泡 %+v 的下边", album, bub)
				}
			}
		})
	}
}

// 加载前、加载好、加载失败：气泡矩形和图的矩形都一样（占位高度 = 最终高度）。
func TestImageComboSizeStableWhileLoading(t *testing.T) {
	for _, c := range imageCombos() {
		t.Run(c.Name, func(t *testing.T) {
			var bubs, albums []ui.Rect
			for _, res := range []MediaResult{{State: MediaLoading}, mbReady, {State: MediaFailed}} {
				_, tt := comboLayoutRender(t, c, res)
				bubs = append(bubs, mustFind(t, tt, "气泡"))
				if r, ok := tt.Find("相册"); ok {
					albums = append(albums, r)
				}
			}
			for i := 1; i < len(bubs); i++ {
				if bubs[i] != bubs[0] {
					t.Fatalf("状态 %d 的气泡 %+v 和加载前 %+v 不一样", i, bubs[i], bubs[0])
				}
			}
			for i := 1; i < len(albums); i++ {
				if albums[i] != albums[0] {
					t.Fatalf("状态 %d 的图 %+v 和加载前 %+v 不一样", i, albums[i], albums[0])
				}
			}
		})
	}
}

// 注释写在窄图上：牌子不超出图，长了就裁，不撑宽气泡。
func TestImageNarrowNotesStayInsideImage(t *testing.T) {
	var c imgCombo
	for _, x := range imageCombos() {
		if x.Name == "narrow-notes" {
			c = x
		}
	}
	c.Msg.Notes = []string{"已编辑", "来自一个很长很长的来源说明"}
	_, tt := comboLayoutRender(t, c, mbReady)
	bub, album, chip := mustFind(t, tt, "气泡"), mustFind(t, tt, "相册"), mustFind(t, tt, "时间牌子")
	if !within(chip, album) || !within(album, bub) || !mbNear(album.W, bub.W) {
		t.Fatalf("牌子 %+v 图 %+v 气泡 %+v", chip, album, bub)
	}
}

// 带引用的长引用文字不撑宽气泡：气泡宽仍等于图宽。
func TestImageQuoteLongTextDoesNotWidenBubble(t *testing.T) {
	for _, name := range []string{"quote", "parts-quote"} {
		var c imgCombo
		for _, x := range imageCombos() {
			if x.Name == name {
				c = x
			}
		}
		long := "这是一段很长很长的被引文字，要是气泡跟着它变宽，图就铺不满了，所以只能在引用卡里被裁掉。"
		if c.Msg.Quote != nil {
			c.Msg.Quote.Text = long
		} else {
			c.Msg.Parts[0].Quote.Snippet = long
		}
		_, tt := comboLayoutRender(t, c, mbReady)
		bub, album := mustFind(t, tt, "气泡"), mustFind(t, tt, "相册")
		card := mustFind(t, tt, quoteLabel(c))
		if !mbNear(bub.W, album.W) || !within(card, bub) {
			t.Fatalf("%s：气泡 %+v 图 %+v 引用卡 %+v", name, bub, album, card)
		}
	}
}

// 贴纸：什么都不带时没有气泡底；带引用或表情回应时有气泡托着，贴纸完整显示（不裁）。
func TestStickerWithDecorationGetsBubble(t *testing.T) {
	for _, c := range imageCombos() {
		if c.Name != "sticker-quote" && c.Name != "sticker-reactions" {
			continue
		}
		h, tt := comboLayoutRender(t, c, mbReady)
		bub, album := mustFind(t, tt, "气泡"), mustFind(t, tt, "相册")
		if album.W > stickerMax+0.5 || !mbNear(album.W, bub.W) {
			t.Fatalf("%s：贴纸 %+v 气泡 %+v", c.Name, album, bub)
		}
		if px := tt.Image(); px != nil {
			if got := px.RGBAAt(int(bub.X+bub.W/2), int(bub.Y+bub.H)-2); got == (color.RGBA{240, 240, 240, 255}) {
				t.Fatalf("%s：气泡底边中间是页面背景 %v，应有气泡底", c.Name, got)
			}
		}
		_ = h
	}
}

// 图不在最前面（图文混排）：图缩在内边距里，离气泡左边一个竖内边距，外框四角是最小内层圆角（不是直角）。
func TestImageInsetCombos(t *testing.T) {
	for _, c := range imageCombos() {
		if !c.Inset {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			h, tt := comboLayoutRender(t, c, mbReady)
			pv := h.T.BubblePadV
			bub, pics := mustFind(t, tt, "气泡"), mustFind(t, tt, "图片")
			if !mbNear(pics.X-bub.X, pv) || !within(pics, bub) {
				t.Fatalf("图 %+v 该缩在气泡 %+v 里，离左边 %.1f", pics, bub, pv)
			}
			if px := tt.Image(); px != nil {
				if got := px.RGBAAt(int(pics.X), int(pics.Y)); got == mbRed {
					t.Fatalf("图的角上 %v 没有被最小圆角裁掉", got)
				}
			}
			// 加载前后一样
			_, loading := comboLayoutRender(t, c, MediaResult{State: MediaLoading})
			if r := mustFind(t, loading, "图片"); r != pics {
				t.Fatalf("加载前 %+v 和加载后 %+v 不一样", r, pics)
			}
		})
	}
}
