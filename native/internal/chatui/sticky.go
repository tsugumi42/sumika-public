package chatui

import (
	"github.com/egoist/mygo/ui"
)

// 粘性头像（照 Telegram）：一组连续消息很长、超过窗口高时，头像不跟着组的最后一条滚出去，
// 而是钉在可见区域底边，一路跟到组的上沿为止。
//
// 做法：头像画成一个盖在消息列上的层（Overlay），不放在最后一条的行里。原因：
//  1. 虚拟列表只构建可见的行，组的首尾行不一定在构建里；
//  2. 组由好几行组成，没有一个元素包着整组，mygo 的 Sticky 只能在单个父元素的盒子里粘，换行时头像会跳；
//  3. 每一行在自己画的时候登记自己的框（行比层先画），层最后画，用的是这一帧的位置，滚动时不差一帧。
//
// 组的边界不需要找不在场的行：画出来的行是列表里连续的一段，一段从"组的第一条"开始（Joined 为假）才知道组顶，
// 到"组的最后一条"结束才知道组底；组顶在可见区域上方、组底在下方时，那一边根本不限制头像。
// y = clamp(可见区域底边 − 头像高 − 内距, 组顶 + 内距, 组底 − 头像高)，最后一项优先（很短的组头像对齐组底）。
type StickyAvatars struct {
	recs   []avatarRec
	placed []AvatarPlacement
	// Visible 把头像层的盒子换算成"真正看得见的消息区"：底部有提示栏、玻璃输入框叠在消息上面时，
	// 把它们盖住的部分扣掉，头像钉在剩下那块的底边。nil 时整个盒子都算。头像永远裁在盒子（列表视口）里。
	Visible func(view ui.Rect) ui.Rect
}

// AvatarPlacement 是头像这一帧画在哪（测试和诊断用）。
type AvatarPlacement struct {
	MessageID             string
	X, Y, Size            float32
	GroupTop, GroupBottom float32 // 组顶、组底；未知（组的那一头不在画出来的行里）时是 ±大数
}

type avatarRec struct {
	id          string
	x, top, bot float32
	first, last bool
	size, pad   float32
	draw        func(p *ui.Painter, r ui.Rect)
}

const farAway = 1e9

// Placed 是上一次画出来的头像位置。
func (s *StickyAvatars) Placed() []AvatarPlacement { return s.placed }

func (s *StickyAvatars) note(r avatarRec) { s.recs = append(s.recs, r) }

// Overlay 画头像层：放在消息列表后面、和它同一块区域里（绝对定位、铺满）。必须在列表之后构建，才在它上面画。
func (s *StickyAvatars) Overlay(c *ui.Context) ui.Element {
	e := ui.Box(c.Key("sticky-avatars")).Absolute().Left(0).Top(0).Right(0).Bottom(0).PassThrough() // 只画，不拦指针
	e.Draw(func(p *ui.Painter, view ui.Rect) {
		s.paint(p, view)
	})
	return e
}

func (s *StickyAvatars) paint(p *ui.Painter, box ui.Rect) {
	// 钉住的位置按可见区算（不钉到玻璃输入框底下）；裁剪只按盒子，和消息同一个裁剪：
	// 头像滚出去时和消息一起从玻璃底下过，不在输入框上沿被切掉（主人 2026-11-09）
	view := box
	if s.Visible != nil {
		if v := s.Visible(box); v.W > 0 && v.H > 0 {
			view = v
		}
	}
	p.Clip(box, 0, func() { s.paintIn(p, view) })
}

func (s *StickyAvatars) paintIn(p *ui.Painter, view ui.Rect) {
	recs := s.recs
	s.recs, s.placed = s.recs[:0], s.placed[:0]
	for i := 0; i < len(recs); {
		j := i
		for j+1 < len(recs) && !recs[j].last {
			j++
		}
		top, bot := float32(-farAway), float32(farAway)
		if recs[i].first {
			top = recs[i].top
		}
		if recs[j].last {
			bot = recs[j].bot
		}
		rec := recs[j]
		y := view.Y + view.H - rec.pad - rec.size
		y = max(y, top+rec.pad)
		y = min(y, bot-rec.size)
		r := ui.Rect{X: rec.x, Y: y, W: rec.size, H: rec.size}
		rec.draw(p, r)
		s.placed = append(s.placed, AvatarPlacement{MessageID: rec.id, X: r.X, Y: r.Y, Size: rec.size, GroupTop: top, GroupBottom: bot})
		i = j + 1
	}
}

// avatarDraw 是一个头像怎么画：有图画图（按头像形状裁切），没有就是首字加角色色实底。
func (h Host) avatarDraw(m *Msg, acc Accent, img ui.ImageSource) func(p *ui.Painter, r ui.Rect) {
	t := h.T
	name := []rune(m.Name)
	initial := " "
	if len(name) > 0 {
		initial = string(name[0])
	}
	return func(p *ui.Painter, r ui.Rect) {
		if img != nil {
			p.Clip(r, h.avatarRadius(r.W), func() { p.Image(img, r, ui.Cover) })
			return
		}
		p.Fill(r, acc.Face, h.avatarRadius(r.W))
		size := round1(r.W * AvatarInitial)
		w, hgt := p.MeasureText(1e6, ui.Span{Text: initial, Font: h.Font, Size: size, Weight: t.Weight[3]})
		p.RichText(r.X+(r.W-w)/2, r.Y+(r.H-hgt)/2, w+1, ui.Span{Text: initial, Font: h.Font, Size: size, Weight: t.Weight[3], Color: acc.On})
	}
}
