package main

// 栏位几何、拖动和窄窗切页都由 MewTypeUI 管；这里只保存用户偏好和异步存盘的防抖账。
import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
)

const panesSaveDelay = 500 * time.Millisecond

const (
	labelListFold   = `收起会话列表（Ctrl+B）`
	labelListUnfold = `展开会话列表（Ctrl+B）`
	labelSideFold   = `收起右侧栏（Ctrl+\）`
	labelSideUnfold = `展开右侧栏（Ctrl+\）`
)

type paneState struct {
	layout      layout.State
	ready       bool
	saved, pend desksettings.Panes
	rev         uint64 // 每次 load 加一：新界面据此知道桌面设置读到了新的栏位偏好
	dirty       bool
	dirtyAt     time.Time
}

func (p *paneState) load(st desksettings.Panes) {
	st = st.Clamp()
	p.layout.ListRatio, p.layout.Compact = st.ListRatio, st.Compact
	p.layout.ThirdWidth, p.layout.ThirdOpen = st.ThirdWidth, st.ThirdOpen
	p.saved, p.ready, p.dirty = st, true, false
	p.rev++
}

// setPreferences 把新界面里改过的栏位偏好放回来，persist 看到它和已存的不同就防抖存盘。
func (p *paneState) setPreferences(st desksettings.Panes) {
	p.layout.ListRatio, p.layout.Compact = st.ListRatio, st.Compact
	p.layout.ThirdWidth, p.layout.ThirdOpen = st.ThirdWidth, st.ThirdOpen
}

func (p *paneState) preferences() desksettings.Panes {
	return desksettings.Panes{ListRatio: p.layout.ListRatio, Compact: p.layout.Compact, ThirdWidth: p.layout.ThirdWidth, ThirdOpen: p.layout.ThirdOpen}
}

func (p *paneState) persist(now time.Time) (out *desksettings.Panes, wait time.Duration) {
	st := p.preferences()
	if !p.ready || st == p.saved {
		p.dirty = false
		return nil, 0
	}
	if !p.dirty || st != p.pend {
		p.dirty, p.pend, p.dirtyAt = true, st, now
		return nil, panesSaveDelay
	}
	if rest := panesSaveDelay - now.Sub(p.dirtyAt); rest > 0 {
		return nil, rest
	}
	p.saved, p.dirty = st, false
	return &st, 0
}

func (p *paneState) unsaved() *desksettings.Panes {
	st := p.preferences()
	if !p.ready || st == p.saved {
		return nil
	}
	p.saved, p.dirty = st, false
	return &st
}

func (p *paneState) toggleList() { p.layout.Compact = !p.layout.Compact }

func (a *app) paneKeys(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyBackslash, func() { a.pn.layout.SetThird(c, !a.pn.layout.ThirdOpen, nil) })
	c.OnShortcut(ui.Cmd, ui.KeyB, a.pn.toggleList)
}

func (a *app) panesView(c *ui.Context) {
	layout.Pane(c, &a.pn.layout, layout.Conversations, func() { a.convList(c) })
	layout.Pane(c, &a.pn.layout, layout.Chat, func() { a.chatView(c) })
	layout.Pane(c, &a.pn.layout, layout.Third, func() { a.sideView(c) })
}

func (a *app) persistPanes(c *ui.Context) {
	if a.savePanes != nil {
		if st, wait := a.pn.persist(c.Now()); st != nil {
			a.savePanes(*st)
		} else if wait > 0 {
			c.After(wait)
		}
	}
}

func paneHeader(c *ui.Context, name string) ui.Element {
	return ui.Row(c).FillWidth().Height(theme.T(c).Metrics().Header().Height()).Shrink(0).AlignItems(ui.Center).Background(theme.T(c).Materials().Header().Background()).Label(name + "顶栏")
}
