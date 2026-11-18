package main

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// 分组仍按可信发送人和时间计算；日期、名字、头像及逐条时间由组件库排版。
func (a *app) message(c *ui.Context, i int) {
	m := a.msgs[i]
	a.rowAt = i
	var prev, next *msg
	if i > 0 {
		prev = a.msgs[i-1]
	} else if b := a.topPrev; b != nil && b.id != m.id && b.at <= m.at {
		prev = b
	}
	if i+1 < len(a.msgs) {
		next = a.msgs[i+1]
	}
	ui.Column(c).FillWidth().Children(func() {
		if prev == nil || !sameDay(prev.at, m.at) {
			chat.DateMarker(c, time.UnixMilli(m.at).Format("1 月 2 日"), chat.DateOptions{})
		}
		a.messageBody(c, m, prev == nil || !sameRun(prev, m), next == nil || !sameRun(m, next), time.UnixMilli(m.at))
	})
}

func (a *app) messageBody(c *ui.Context, m *msg, first, last bool, at time.Time) {
	selected := false
	if a.flashID == m.id {
		if a.flashAt.IsZero() {
			a.flashAt = c.Now()
		}
		selected = c.Now().Sub(a.flashAt) < flashSpan
		if selected {
			c.After(flashSpan - c.Now().Sub(a.flashAt))
		}
	}
	draw := func() ui.Element {
		return a.messageRows(c, m, docOf(m), first, last, a.group, selected, at, true, 0)
	}
	if !a.arr.enter(m, c.Now(), c.Preferences().ReduceMotion) {
		draw()
		return
	}
	entry := a.arr.reveal(m.id)
	if !a.list.AtEnd() {
		entry.state.Finish()
		draw()
		return
	}
	// 完整行在裁切容器内自然测量；下一构建读取句柄，不按文字行数猜高度。
	height := entry.row.Bounds(c).H
	reveal := chat.Reveal(c, &entry.state, chat.RevealOptions{Height: height}, func() {
		draw().Shrink(0).Bind(&entry.row)
	}).Label("新消息展开")
	a.probeOf("reveal:"+m.id, reveal)
	c.After(a.arr.left(m.id, c.Now()))
}

// probeOf 只在测试绑定持久句柄，不在状态里保留本轮 Element。
func (a *app) probeOf(name string, e ui.Element) {
	if a.probe != nil {
		e.Bind(a.probe(name))
	}
}

// 组首采用库自己的行顶；连接行上方已有完整行，夹取下界可以放在视口顶之前。
func (a *app) avatarGroupTop(c *ui.Context, first bool) *float32 {
	if first {
		return nil
	}
	if layer := a.forward.rendering; layer != nil {
		a.forward.avatarTop = layer.list.Bounds(c).Y - theme.T(c).Metrics().Message().AvatarSize()
		return &a.forward.avatarTop
	}
	a.avatarTop = a.list.Bounds(c).Y - theme.T(c).Metrics().Message().AvatarSize()
	return &a.avatarTop
}

func sameDay(a, b int64) bool {
	x, y := time.UnixMilli(a), time.UnixMilli(b)
	return x.YearDay() == y.YearDay() && x.Year() == y.Year()
}
