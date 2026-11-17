package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

// paint/message-bottom.md#形状与内容矩形, #图片与状态矩阵.
func TestMessageBottomCapsuleStates(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Reaction()
	pic := contentBitmap()
	items := []content.Reaction{{ID: "zero", Emoji: "Z"}, {ID: "short", Emoji: "S", Count: 1200, CountLabel: "1.2K"}, {ID: "one", Emoji: "A", Count: 1, Userpics: []content.ReactionUserpic{{ID: "a", Image: pic}}}, {ID: "two", Emoji: "B", Count: 2, Userpics: []content.ReactionUserpic{{ID: "a", Image: pic}, {ID: "b", Image: pic}}}, {ID: "three", Emoji: "C", Count: 3, Userpics: []content.ReactionUserpic{{ID: "a", Image: pic}, {ID: "b", Image: pic}, {ID: "c", Image: pic}}}}
	var countWidth float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		countWidth, _ = c.MeasureText(0, ui.Span{Text: "1.2K", Size: th.Typography().ContentTitle().Size(), Weight: th.Typography().ContentTitle().Weight()})
		ui.Column(c).Padding(20).Children(func() { content.Reactions(c, items, content.ReactionOptions{}).Label("list") })
	}, 600, 300)
	zero := bottomBox(t, tt, "Z ")
	short := bottomBox(t, tt, "S 1.2K")
	bottomAssert(t, "zero count omits number and shrinks pill", bottomNear(zero.W, m.PaddingLeft()+m.IconSize()+m.PaddingRight()-m.EmptySkip()))
	bottomAssert(t, "localized short count drives measured width", bottomNear(short.W, m.PaddingLeft()+m.IconSize()+m.LabelGap()+countWidth+m.PaddingRight()))
	for i, label := range []string{"A 1", "B 2", "C 3"} {
		p := bottomBox(t, tt, label)
		bottomAssert(t, "avatar pill height remains twenty two", bottomNear(p.H, m.PaddingTop()+m.IconSize()+m.PaddingBottom()))
		bottomAssert(t, "avatar pill uses dedicated padding and overlap", bottomNear(p.W, m.PaddingLeft()+m.IconSize()+2*m.UserpicPadding()+m.UserpicSize()+float32(i)*(m.UserpicSize()-m.UserpicOverlap())))
	}
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	bottomAssert(t, "large font does not resize reaction pill", bottomNear(bottomBox(t, tt, "S 1.2K").H, zero.H))
	tt.Image()
}

func TestMessageBottomInformationStates(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Message()
	info := chat.Info{Label: "18:42", Delivery: chat.Sent, Views: "12", Replies: "3"}
	side := chat.Outgoing
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		ui.Column(c).Padding(20).Children(func() { chat.BottomInfo(c, info, chat.BodyOptions{Side: side}).Label("info") })
	}, 500, 200)
	before := bottomBox(t, tt, "info")
	sent := bottomBox(t, tt, "Sent")
	date := bottomBox(t, tt, "Date text")
	views := bottomBox(t, tt, "Views")
	bottomAssert(t, "receipt uses source canvas", bottomNear(sent.W, m.SentWidth()) && bottomNear(sent.H, m.ReceiptHeight()))
	bottomAssert(t, "receipt uses date bottom anchor", bottomNear(sent.Y, date.Y+date.H-m.ReceiptTop()))
	bottomAssert(t, "view slot uses independent canvas", bottomNear(views.W, m.ViewsIconWidth()) && bottomNear(views.Y, date.Y+date.H-m.ReceiptTop()))
	info.Delivery = chat.Read
	tt.Frame()
	bottomAssert(t, "single and double receipt reserve equal width", bottomNear(bottomBox(t, tt, "info").W, before.W) && bottomNear(bottomBox(t, tt, "Read").W, m.ReadWidth()))
	info.Delivery = chat.Failed
	tt.Frame()
	bottomAssert(t, "failed uses clock and hides counters", !tt.HasText("12") && !tt.HasText("3") && bottomNear(bottomBox(t, tt, "Failed").W, m.SendingSize()))
	info = chat.Info{Label: "18:42", Signature: "A very long prepared author signature which must be elided"}
	side = chat.Incoming
	tt.Frame()
	bottomAssert(t, "signature limit preserves complete date", bottomNear(bottomBox(t, tt, "info").W, m.SignatureMaxWidth()) && bottomNear(bottomBox(t, tt, "Date text").W, date.W))
	info.Prefix = "已编辑"
	tt.Frame()
	bottomAssert(t, "edited prefix preserves information height", bottomNear(bottomBox(t, tt, "info").H, before.H))
}

func TestMessageBottomPhotoOwnership(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Media()
	ready := content.Media{ID: "photo", Label: "photo", Bitmap: contentBitmap()}
	show := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		ui.Column(c).Padding(20).Children(func() {
			content.Photo(c, ready, content.MediaOptions{Width: 200, Preview: show, BottomInfo: func() {
				chat.ImageInfo(c, chat.Info{Label: "18:42", Delivery: chat.Read}, chat.BodyOptions{Side: chat.Outgoing}).Label("image badge")
			}})
		})
	}, 400, 300)
	_, visible := tt.Find("image badge")
	bottomAssert(t, "static photo hides information", !visible)
	photo := bottomBox(t, tt, "photo")
	tt.Move(photo.X+photo.W/2, photo.Y+photo.H/2)
	badge := bottomBox(t, tt, "image badge")
	date := bottomBox(t, tt, "18:42")
	bottomAssert(t, "hover badge inset matches image", bottomNear(photo.X+photo.W-badge.X-badge.W, m.BadgeInset()) && bottomNear(photo.Y+photo.H-badge.Y-badge.H, m.BadgeInset()))
	bottomAssert(t, "image information padding is eight by two", bottomNear(date.X-badge.X, m.BadgePaddingX()) && bottomNear(date.Y-badge.Y, m.BadgePaddingY()))
	tt.Move(0, 0)
	show = true
	tt.Frame()
	bottomAssert(t, "preview holds information without hover", tt.HasText("18:42"))
	for _, scale := range []float32{1, 1.25, 1.5} {
		tt.SetScale(scale)
		tt.Image()
	}
}
