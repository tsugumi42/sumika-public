package main

// 分栏状态的读和存（账在 panes.go 的 paneState，文件在 internal/desksettings 的 panes 一项）。
// 读、写文件都在后台线程；界面线程只收读出来的结果、交出要存的一份。

import "sumika-native/internal/desksettings"

// loadPanes 读出上次的分栏状态放进界面（后台线程，initSettings 里调）。读不懂的字段按默认值、写一行日志；
// 读完才接上存盘：之前界面还是默认值，不能把默认值当成"用户改过"写回去
func (s *shell) loadPanes(st *desksettings.Store) {
	p, err := st.Panes()
	if err != nil {
		s.logf("读分栏宽度出错，读不懂的按默认值：%v", err)
	}
	s.a.post(func() {
		s.a.pn.load(p)
		s.a.savePanes = func(p desksettings.Panes) { s.a.deskSaves.add(func() { s.writePanes(st, p) }) }
	})
}

// writePanes 写一份分栏状态。失败只记日志：宽度没存上不影响用，下次改了会再存
func (s *shell) writePanes(st *desksettings.Store, p desksettings.Panes) {
	if err := st.SetPanes(p); err != nil {
		s.logf("存分栏宽度失败：%v", err)
	}
}
