package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
	"strconv"
	"sync"
)

// ReactionImageKind describes the host's prepared pixels, independently of
// their source resolution. See paint/reaction-size.md#绘制合同.
type ReactionImageKind uint8

const (
	ReactionStaticImage ReactionImageKind = iota // Unpadded artwork, drawn at half the image canvas.
	ReactionCenterFrame                          // Complete center-icon canvas; retain its transparent border.
	ReactionCustomEmoji                          // Prepared custom frame, including its mask and tint.
)

// Reaction.ID and input order belong to the host. CountLabel is its localized
// short count (e.g. 1.2K); Count is used when no prepared label is supplied.
type Reaction struct {
	ID, Emoji  string
	Image      *ui.Bitmap
	Count      int
	CountLabel string
	Own        bool
	ImageKind  ReactionImageKind
	Userpics   []ReactionUserpic
}
type ReactionUserpic struct {
	ID    string
	Image *ui.Bitmap
}
type ReactionOptions struct {
	Appearance
	InBubble, Outside bool
	Trailing          ui.Span
	OnActivate        func(id string)
	OnContextMenu     func(id string, m *ui.Menu)
}

type reactionGlyphFrame struct {
	text          string
	font          ui.Font
	width, height float32
}

// Reactions transfers S×K to the final wrap item. The tail's negative top
// margin cancels Wrap's row gap only when it gets its own line, so overflow
// adds K rather than a whole reaction row. paint/message-bottom.md#有回应时最后一行回应让时间.
func Reactions(c *ui.Context, items []Reaction, o ReactionOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Reaction()
	root := ui.Row(c).Wrap().Gap(m.Gap()).AlignItems(ui.Start).MinWidth(0)
	shift := float32(0)
	if o.InBubble {
		shift = -m.InBubbleShift()
	}
	if o.Outside && o.Outgoing {
		root.Direction(ui.RightToLeft)
	} else {
		root.Direction(ui.LeftToRight)
	}
	root.Children(func() {
		for _, item := range items {
			c.Key(item.ID)
			count := item.CountLabel
			if count == "" && item.Count != 0 {
				count = strconv.Itoa(item.Count)
			}
			var fn func()
			if o.OnActivate != nil {
				fn = func() { o.OnActivate(item.ID) }
			}
			style := t.Typography().ContentTitle()
			cw, ch := c.MeasureText(0, ui.Span{Text: count, Font: t.Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
			height := m.PaddingTop() + m.IconSize() + m.PaddingBottom()
			width := m.PaddingLeft() + m.IconSize() + m.PaddingRight() - m.EmptySkip()
			if count != "" {
				width = m.PaddingLeft() + m.IconSize() + m.LabelGap() + cw + m.PaddingRight()
			}
			if len(item.Userpics) > 0 {
				width = m.PaddingLeft() + m.IconSize() + 2*m.UserpicPadding() + m.UserpicSize() + float32(len(item.Userpics)-1)*(m.UserpicSize()-m.UserpicOverlap())
			}
			// paint/message-bottom.md#形状与内容矩形: glyphs/avatars cannot enlarge 22 DIP.
			e := action(c, item.Emoji+" "+count, o.Disabled, fn).Size(width, height).Shrink(0).Radius(height/2).Translate(shift, 0).Direction(ui.LeftToRight)
			bg, fg := reactionColors(t, item, o)
			e.Background(bg).Children(func() {
				ui.Box(c).Absolute().Left(m.PaddingLeft()).Top(m.PaddingTop()).Size(m.IconSize(), m.IconSize()).Children(func() {
					if item.Image != nil {
						// paint/reaction-size.md#绘制合同: 32 is a resource canvas,
						// not the size of unpadded host artwork.
						size := m.ImageSize() / 2
						switch item.ImageKind {
						case ReactionCenterFrame:
							size = m.ImageSize()
						case ReactionCustomEmoji:
							// The source's custom-resource conversion, not a second layout slot.
							size = float32(math.Round(float64(m.IconSize()) * 1.12))
						}
						ui.Image(c, item.Image).Size(size, size).Attach(ui.AnchorCenter, ui.AnchorCenter).Fit(ui.Contain)
					} else {
						// paint/reaction-size.md#绘制合同: system glyphs are a resource
						// substitute; fit their full line frame into the fixed emoji slot.
						font := ui.Font{Family: t.Typography().Font(), Size: m.IconSize(), Weight: t.Typography().Message().Weight()}
						frame := ui.Local(e, "emoji-frame", func() reactionGlyphFrame { return reactionGlyphFrame{} })
						if frame.text != item.Emoji || frame.font != font {
							frame.text, frame.font = item.Emoji, font
							// Rich-span measurement also seeds a line with the theme's scaled font.
							// Shape only on resource changes so text accessibility cannot shrink emoji.
							frame.width, frame.height = ui.ShapeText(item.Emoji, font, 0).Size()
						}
						k := min(1, m.IconSize()/max(frame.width, frame.height))
						ui.Text(c, item.Emoji).Font(font.Family).FontSize(font.Size).FontWeight(font.Weight).TextColor(fg).
							Attach(ui.AnchorCenter, ui.AnchorCenter).Scale(k, k)
					}
				})
				if len(item.Userpics) > 0 {
					// paint/message-bottom.md#形状与内容矩形: erase the overlap to transparency,
					// preserving service/wallpaper pixels instead of painting a bubble border.
					for i := len(item.Userpics) - 1; i >= 0; i-- {
						pic := item.Userpics[i]
						c.Key(pic.ID)
						avatar := ui.Box(c).Absolute().Left(m.PaddingLeft()+m.IconSize()+m.UserpicPadding()+float32(i)*(m.UserpicSize()-m.UserpicOverlap())).Top(m.UserpicPadding()).Size(m.UserpicSize(), m.UserpicSize())
						reactionAvatar(avatar, pic.Image, m, i < len(item.Userpics)-1)
					}
				} else if count != "" {
					text(c, count, style, fg).Absolute().Left(m.PaddingLeft()+m.IconSize()+m.LabelGap()).Top(float32(math.Floor(float64((height-ch)/2)))).Size(cw, ch).SingleLine().Label("Reaction count " + item.ID)
				}
			})
			if fn != nil {
				ripple(c, e, fg, height/2, m.RippleOpacity())
			}
			if o.OnContextMenu != nil {
				e.ContextMenu(func(menu *ui.Menu) { o.OnContextMenu(item.ID, menu) })
			}
		}
		if o.Trailing.ObjectWidth > 0 {
			c.Key("bottom-info-reservation")
			// L affects only the final e+S<=W condition; capsule wrapping uses full W.
			ui.Box(c).Width(max(0, o.Trailing.ObjectWidth+shift-m.Gap())).Height(o.Trailing.ObjectHeight).Shrink(0).Margin(-m.Gap(), 0, 0).Label("Information reservation")
		}
	})
	return root
}

// paint/message-bottom.md#颜色角色: direction, message selection and own
// reaction are independent; outside pills use service colors directly.
func reactionColors(t *theme.Theme, item Reaction, o ReactionOptions) (ui.Color, ui.Color) {
	m := t.Metrics().Reaction()
	p := t.Colors().Reaction()
	bg, fg := p.In().Background(), p.In().Text()
	opacity := m.InOpacity()
	if o.Outgoing {
		bg, fg = p.Out().Background(), p.Out().Text()
		opacity = m.OutOpacity()
	}
	if o.Selected {
		bg, fg = p.In().SelectedBackground(), p.In().SelectedText()
		if o.Outgoing {
			bg, fg = p.Out().SelectedBackground(), p.Out().SelectedText()
		}
	}
	if item.Own {
		opacity = 1
		bg, fg = p.In().OwnBackground(), p.In().OwnText()
		if o.Outgoing {
			bg, fg = p.Out().OwnBackground(), p.Out().OwnText()
		}
		if o.Selected {
			bg, fg = p.In().SelectedBackground(), p.In().SelectedOwnText()
			if o.Outgoing {
				bg, fg = p.Out().SelectedBackground(), p.Out().SelectedOwnText()
			}
		}
	}
	if o.Outside {
		bg, fg = t.Colors().Service().Background(), t.Colors().Service().Text()
		opacity = 1
		if item.Own {
			bg, fg = fg, bg
			if fg.A < uint8(m.OutsideOwnAlpha()*255) {
				fg.A = uint8(m.OutsideOwnAlpha() * 255)
			}
		}
	}
	return bg.Alpha(opacity), fg
}

var avatarMask = sync.OnceValue(func() *shader.Shader {
	return shader.MustCompile(`package main
var Size float
var Shift float
var Stroke float
var Cut float
func Fragment(dst vec4,pos vec2)vec4 {
 s:=image0Size()
 scale:=min(s.x,s.y)/Size
 sample:=image0Sample((pos-vec2(Size/2))*scale+s/2)
 edge:=clamp(Size/2-length(pos-vec2(Size/2))+0.5,0.0,1.0)
 hole:=clamp(length(pos-vec2(Size/2+Shift,Size/2))-(Size/2+Stroke)+0.5,0.0,1.0)
 return sample*edge*(1.0-Cut+Cut*hole)
}`)
})

type avatarMaskState struct {
	values                   *shader.Values
	size, shift, stroke, cut float32
}

func reactionAvatar(e ui.Element, image *ui.Bitmap, m theme.MetricsReaction, cut bool) {
	program := avatarMask()
	s := ui.Local(e, "avatar-mask", func() avatarMaskState { return avatarMaskState{values: program.NewValues()} })
	v := float32(0)
	if cut {
		v = 1
	}
	if s.size != m.UserpicSize() || s.shift != m.UserpicSize()-m.UserpicOverlap() || s.stroke != m.UserpicStroke() || s.cut != v {
		s.size, s.shift, s.stroke, s.cut = m.UserpicSize(), m.UserpicSize()-m.UserpicOverlap(), m.UserpicStroke(), v
		s.values.Float(program.Uniform("Size"), s.size)
		s.values.Float(program.Uniform("Shift"), s.shift)
		s.values.Float(program.Uniform("Stroke"), s.stroke)
		s.values.Float(program.Uniform("Cut"), s.cut)
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		program.Draw(p, r, shader.Options{Values: s.values, Images: [4]*ui.Bitmap{image}})
	})
}
