package main

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 输入框底行的“工作目录”药丸（规格-按会话选工作目录）。照 Hermes 桌面版的状态栏药丸：显示项目名，点开换目录。
// 只在本地私聊画；后台不提供 api.LocalWorkspaces 就不画，不摆假按钮。
// 读目录状态会碰文件系统，所以不在每帧读：换会话、改完、点开菜单时各读一次。

var iconFolder = chatui.Icon("folder", false)

type wbCwdState struct {
	gen    uint64
	key    api.ConversationKey
	asked  bool // 这个会话读过没有
	busy   bool
	info   api.WorkspaceInfo
	recent []string
	open   bool
	note   string // 没改成的原因，悬停可见
}

func (a *app) wbCwdPill(c *ui.Context, kit *winshell.Kit, k api.ConversationKey) {
	ws, ok := a.reader.(api.LocalWorkspaces)
	if !ok {
		return
	}
	st := &a.workbench.shell.cwd
	if st.gen != a.srcGen || st.key != k {
		*st = wbCwdState{gen: a.srcGen, key: k}
	}
	if !st.asked && !st.busy {
		a.cwdLoad(ws, k)
	}
	if st.info.CWD == "" {
		return
	}
	label, hint := st.info.Name, st.info.CWD
	if !st.info.Bound {
		hint += "（全局默认；点开可以给这个会话单独选）"
	} else {
		hint += "（这个会话选的）"
	}
	if st.info.Problem != "" {
		label, hint = "目录用不了："+label, st.info.Problem
	}
	if st.note != "" {
		hint = st.note
	}
	hint += "。改了从下一轮生效"
	// 这个菜单整个不放图标：最近用过的目录没有自然的图标，只给第一项一个会让文字左边不齐（菜单规则：图标要么都有要么都没有）
	items := []winshell.MenuItem{{Label: "选择文件夹…", Action: func() { a.cwdPick(ws, k) }}}
	for _, dir := range st.recent {
		dir := dir
		items = append(items, winshell.MenuItem{Label: filepath.Base(dir) + "  " + dir, Group: "最近用过", Checked: st.info.Bound && strings.EqualFold(dir, st.info.CWD), Action: func() { a.cwdSet(ws, k, dir) }})
	}
	if st.info.Bound {
		items = append(items, winshell.MenuItem{Label: "恢复默认目录", Group: "默认", Action: func() { a.cwdClear(ws, k) }})
	}
	was := st.open
	kit.PillMenu(c, "composer-cwd", label, hint, iconFolder, &st.open, items)
	if st.open && !was { // 点开时重读一次：/cd 或别处改过，药丸和菜单要跟上
		st.asked = false
	}
}

// cwdLoad 读这个会话的目录和最近目录；回来时会话换了就丢掉。
func (a *app) cwdLoad(ws api.LocalWorkspaces, k api.ConversationKey) {
	st := &a.workbench.shell.cwd
	st.asked, st.busy = true, true
	ctx, generation := a.base(), a.srcGen
	go func() {
		info, err := ws.ReadLocalWorkspace(ctx, k)
		recent, rerr := ws.RecentWorkspaces(ctx)
		a.post(func() {
			st := &a.workbench.shell.cwd
			if a.srcGen != generation || st.key != k {
				return
			}
			st.busy = false
			switch {
			case err != nil:
				st.info, st.note = api.WorkspaceInfo{}, "读不到工作目录："+err.Error()
			case rerr != nil:
				st.info, st.note = info, "读不到最近用过的目录："+rerr.Error()
			default:
				st.info, st.recent, st.note = info, recent, ""
			}
		})
	}()
}

func (a *app) cwdApply(k api.ConversationKey, call func() (api.WorkspaceInfo, error)) {
	st := &a.workbench.shell.cwd
	if st.busy {
		return
	}
	st.busy = true
	ctx, generation := a.base(), a.srcGen
	go func() {
		info, err := call()
		_ = ctx
		a.post(func() {
			st := &a.workbench.shell.cwd
			if a.srcGen != generation || st.key != k {
				return
			}
			st.busy = false
			if err != nil {
				st.note = "没有改成：" + err.Error() // 目录不变，不回成功的话
				return
			}
			st.info, st.note, st.asked = info, "", false // 重读一次最近目录
		})
	}()
}

func (a *app) cwdSet(ws api.LocalWorkspaces, k api.ConversationKey, dir string) {
	ctx := a.base()
	a.cwdApply(k, func() (api.WorkspaceInfo, error) { return ws.SetLocalWorkspace(ctx, k, dir) })
}

func (a *app) cwdClear(ws api.LocalWorkspaces, k api.ConversationKey) {
	ctx := a.base()
	a.cwdApply(k, func() (api.WorkspaceInfo, error) { return ws.ClearLocalWorkspace(ctx, k) })
}

// cwdPick 弹系统的选文件夹窗口；选完走和菜单里最近目录同一条路径。
func (a *app) cwdPick(ws api.LocalWorkspaces, k api.ConversationKey) {
	window := a.win
	start := a.workbench.shell.cwd.info.CWD
	go func() {
		dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: window, Title: "选这个会话的工作目录", DefaultPath: start, Directory: true})
		if err != nil || len(dirs) == 0 {
			if err != nil {
				a.post(func() { a.workbench.shell.cwd.note = "没有打开选择窗口：" + err.Error() })
			}
			return
		}
		a.post(func() { a.cwdSet(ws, k, dirs[0]) })
	}()
}
