package main

import (
	"os"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/desksettings"
)

// workbenchColumnMax 是消息列、输入框的最大宽度（照原型 cmd/sumika-proto 的 contentWidth）。
const workbenchColumnMax = 752

type workbenchState struct {
	other          ui.ListState
	generation     uint64
	row            ui.Handle
	rowID, savedID string
	savedOffset    float32
	anchorID       string
	anchorOffset   float32
	shell          wbShell // 新界面外壳（workbench_shell.go）
}

func (a *app) workbenchEnabled() bool {
	return a.mainView == desksettings.MainViewWorkbench
}

func (a *app) applyMainViewEnv() {
	switch os.Getenv("SUMIKA_MAIN_VIEW") {
	case desksettings.MainViewWorkbench, desksettings.MainViewLegacy:
		a.mainView = os.Getenv("SUMIKA_MAIN_VIEW")
		a.mainViewLocked = true
	}
}

func (a *app) switchMainView(c *ui.Context, value string) {
	if value != desksettings.MainViewWorkbench && value != desksettings.MainViewLegacy {
		return
	}
	if (value == desksettings.MainViewWorkbench) == a.workbenchEnabled() {
		return
	}
	w := &a.workbench
	w.anchorID, w.anchorOffset = w.savedID, w.savedOffset
	if w.generation != a.gen {
		w.other = a.newList()
		w.generation = a.gen
	}
	a.list, w.other = w.other, a.list
	if w.anchorID != "" {
		if i := a.indexOf(w.anchorID); i >= 0 {
			a.list.ScrollTo(i, ui.Start)
		}
	} else if len(a.msgs) > 0 {
		a.list.ScrollToEnd()
	}
	a.mainView = value
	if a.mainViewLocked || a.desktop == nil {
		return
	}
	st, view := a.desktop, value
	a.deskSaves.add(func() {
		if err := st.SetMainView(view); err != nil && a.postFn != nil {
			a.post(func() { a.status = "新界面开关没写下：" + err.Error() })
		}
	})
	_ = c
}

func (a *app) workbenchAnchor(c *ui.Context, i int, row ui.Element) {
	w := &a.workbench
	if i < 0 || i >= len(a.msgs) {
		return
	}
	first, last := a.list.Visible()
	if i == first && first <= last {
		if w.rowID == a.msgs[i].id {
			bounds, list := w.row.Bounds(c), a.list.Bounds(c)
			if bounds.H > 0 && list.H > 0 {
				w.savedID, w.savedOffset = w.rowID, max(0, list.Y-bounds.Y)
			}
		} else {
			w.rowID = a.msgs[i].id
			c.AnimationFrame()
		}
		row.Bind(&w.row)
	}
	if w.anchorID != "" && a.msgs[i].id == w.anchorID {
		ui.Box(c.Key("switch-anchor")).Absolute().Top(w.anchorOffset).Size(1, 1).ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollStart})
		w.anchorID = ""
	}
}

func (s *shell) loadMainView(st *desksettings.Store) {
	view, err := st.MainView()
	if err != nil {
		s.logf("读界面开关出错，按旧界面：%v", err)
	}
	material, merr := st.PanelMaterial()
	if merr != nil {
		s.logf("读面板材质出错，按液态玻璃：%v", merr)
	}
	look, lerr := st.Look()
	if lerr != nil {
		s.logf("读新界面外观出错，按默认：%v", lerr)
	}
	panes, perr := st.ShellPanes()
	if perr != nil {
		s.logf("读新界面栏位偏好出错，按默认：%v", perr)
	}
	s.a.post(func() {
		s.a.desktop = st
		s.a.workbench.shell.adoptPanes(panesFromDesk(panes))
		s.a.workbench.shell.adoptLook(look)
		s.a.workbench.shell.material = material
		if s.a.mainViewLocked {
			return
		}
		s.a.mainView = view
	})
}
