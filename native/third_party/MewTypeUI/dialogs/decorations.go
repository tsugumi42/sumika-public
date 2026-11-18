package dialogs

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// arc appends cubic segments of at most a quarter turn, retaining path storage.
func arc(path *ui.Path, cx, cy, rx, ry, start, sweep float64) {
	x, y := cx+rx*math.Cos(start), cy+ry*math.Sin(start)
	path.MoveTo(float32(x), float32(y))
	n := max(1, int(math.Ceil(math.Abs(sweep)/(math.Pi/2))))
	step := sweep / float64(n)
	for range n {
		end := start + step
		k := 4.0 / 3 * math.Tan(step/4)
		x2, y2 := cx+rx*math.Cos(end), cy+ry*math.Sin(end)
		path.CubeTo(float32(x-k*rx*math.Sin(start)), float32(y+k*ry*math.Cos(start)), float32(x2+k*rx*math.Sin(end)), float32(y2-k*ry*math.Cos(end)), float32(x2), float32(y2))
		start, x, y = end, x2, y2
	}
}

type storyDrawing struct {
	stories      Stories
	size         float32
	read, unread ui.Path
}

func storyRing(c *ui.Context, host ui.Element, stories Stories, selected, forum bool) {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	size := m.AvatarSize()
	cache := ui.Local(host, "story-paths", func() storyDrawing { return storyDrawing{} })
	if cache.stories != stories || cache.size != size {
		cache.stories, cache.size = stories, size
		cache.read.Reset()
		cache.unread.Reset()
		count := min(stories.Count, 50) // outline_segments.h:12, source cap.
		if stories.Live {
			count = 1
		}
		gap := math.Min(math.Pi/18, 2*math.Pi/(float64(count)*1.1))
		if count == 1 {
			gap = 0
		}
		span := 2 * math.Pi / float64(count)
		for i := range count {
			path := &cache.read
			if i < min(stories.Unread, 50) || stories.Live {
				path = &cache.unread
			}
			arc(path, float64(size/2), float64(size/2), float64(size/2), float64(size/2), -math.Pi/2+float64(i)*span+gap/2, span-gap)
		}
	}
	host.DrawOver(func(p *ui.Painter, r ui.Rect) {
		read := t.Colors().Conversation().UnreadBackgroundMuted()
		if selected {
			read = t.Colors().Conversation().UnreadBackgroundMutedActive()
		}
		p.Transform(ui.Translation(r.X, r.Y), 0, 0, func() {
			if forum {
				color, width := read, m.StoryReadWidth()
				if stories.Unread > 0 {
					color, width = t.Colors().GroupCall().Live1(), m.StoryWidth()
				}
				if selected {
					color = read
				}
				p.Stroke(ui.Rect{W: size, H: size}, color, size*m.ForumRadiusRatio(), width)
				return
			}
			p.StrokePath(&cache.read, m.StoryReadWidth(), read)
			if selected {
				p.StrokePath(&cache.unread, m.StoryWidth(), read)
			} else if stories.Live {
				p.StrokePath(&cache.unread, m.StoryWidth(), t.Colors().Semantic().Danger())
			} else {
				p.StrokePathGradient(&cache.unread, m.StoryWidth(), ui.LinearGradient{From: t.Colors().GroupCall().Live1(), To: t.Colors().GroupCall().Muted1(), Angle: 225})
			}
		})
	})
}

func activityIndicator(c *ui.Context, kind ActivityKind, color ui.Color, height float32, since time.Time) {
	if kind == TypingActivity {
		typing(c, color, height, since)
		return
	}
	t := theme.T(c)
	m := t.Metrics().Activity()
	width, period := m.RecordLeft()+5*m.RecordGap(), t.Motion().RecordActivity().Duration()
	switch kind {
	case UploadingActivity:
		width, period = m.UploadLeft()+4*m.UploadGap(), t.Motion().UploadActivity().Duration()
	case ChoosingStickerActivity:
		width, period = m.EyeLeft()+2*m.EyeWidth()+5*m.EyeGap(), t.Motion().StickerActivity().Duration()
	}
	reduced := c.Preferences().ReduceMotion
	box := ui.Box(c).Size(width, height).Shrink(0).Label("Activity animation")
	path := ui.Local(box, "path", func() ui.Path { return ui.Path{} })
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		v := cycle(p.Now(), since, period, reduced)
		path.Reset()
		switch kind {
		case RecordingActivity:
			for i := range 4 {
				radius := m.RecordLeft() + m.RecordGap()*(float32(i)+v)
				path.Reset()
				arc(path, float64(r.X), float64(r.Y+r.H/2), float64(radius), float64(radius), -math.Pi/12, math.Pi/6)
				alpha := float32(1)
				if i == 0 {
					alpha = v
				} else if i == 3 {
					alpha = 1 - v
				}
				p.StrokePath(path, m.RecordStroke(), color.Alpha(alpha))
			}
		case UploadingActivity:
			for i := range 3 {
				x, y := r.X+m.UploadLeft()+m.UploadGap()*(float32(i)+v), r.Y+r.H/2
				path.Reset().MoveTo(x, y-m.UploadSize()).LineTo(x+m.UploadSize(), y).LineTo(x, y+m.UploadSize())
				alpha := float32(1)
				if i == 0 {
					alpha = v
				} else if i == 2 {
					alpha = 1 - v
				}
				p.StrokePath(path, m.UploadStroke(), color.Alpha(alpha))
			}
		case ChoosingStickerActivity:
			// A half-cycle moves the pupils, then holds; the outer eyes lag.
			forward := v < .5
			stage := float32(math.Mod(float64(v*4), 2))
			a := min(stage, 1)
			in := 1 - float32(math.Sqrt(float64(1-min(a/m.EyeStage(), 1)*min(a/m.EyeStage(), 1))))
			out := t.Motion().Typing().Easing().Curve()(max(float32(0), (a-m.EyeStage())/(1-m.EyeStage())))
			pupilX := m.EyeOffset()*in + (m.EyeWidth()-m.EyeRightInset())*(1-in)
			left := m.EyeOffset() * (1 - t.Motion().Typing().Easing().Curve()(stage/2))
			if !forward {
				pupilX = m.EyeOffset()*(1-in) + (m.EyeWidth()-m.EyeRightInset())*in
				left = m.EyeOffset() * theme.EaseOutQuint.Curve()(stage/2)
			}
			stretch := (in - out) * m.EyeOffset()
			for i := range 2 {
				x := r.X + m.EyeLeft() + left + float32(i)*(m.EyeWidth()+m.EyeGap())
				y := r.Y + (r.H-m.EyeHeight())/2
				path.Reset()
				arc(path, float64(x+m.EyeWidth()/2), float64(y+(m.EyeHeight()+stretch)/2), float64(m.EyeWidth()/2), float64((m.EyeHeight()-stretch)/2), 0, 2*math.Pi)
				p.StrokePath(path, m.EyeStroke(), color)
				p.Fill(ui.Rect{X: x + pupilX - m.PupilStroke()/2, Y: y + (m.EyeHeight()-m.PupilSize()-m.PupilStroke())/2, W: m.PupilSize() + m.PupilStroke(), H: m.PupilSize() + m.PupilStroke()}, color, (m.PupilSize()+m.PupilStroke())/2)
			}
		}
		if !reduced && period > 0 {
			p.AnimationFrame()
		}
	})
}

func cycle(now, since time.Time, period time.Duration, reduced bool) float32 {
	if reduced || period == 0 {
		return 0
	}
	return float32(now.Sub(since)%period) / float32(period)
}

func callSpectrum(c *ui.Context, box ui.Element, speaking bool, color ui.Color) {
	t := theme.T(c)
	phase := ui.Local(box, "spectrum-phase", func() time.Time { return c.Now() })
	amount := animate(box, "speaking", unit(speaking), t.Motion().SpeakingFade())
	reduced := c.Preferences().ReduceMotion
	box.DrawOver(func(p *ui.Painter, r ui.Rect) {
		v := cycle(p.Now(), *phase, t.Motion().Speaking().Duration(), reduced) * 8
		stage := int(v) % 8
		f := v - float32(stage)
		// Source's eight amplitude keyframes; interpolate adjacent targets.
		middle := [9]float32{1, 5, 1, 3, 1, 5, 1, 5, 1}
		sides := [9]float32{3, 1, 5, 1, 3, 1, 5, 1, 3}
		line := t.Metrics().Conversation().SpeakingStroke() / 2
		for i := range 3 {
			keys := &sides
			if i == 1 {
				keys = &middle
			}
			half := line * (2 + (keys[stage]+(keys[stage+1]-keys[stage])*f-2)*amount)
			p.Fill(ui.Rect{X: r.X + r.W/2 - 4*line + float32(i)*3*line, Y: r.Y + r.H/2 - half, W: 2 * line, H: 2 * half}, color, line)
		}
		if speaking && !reduced {
			p.AnimationFrame()
		}
	})
}
