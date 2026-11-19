package main

// 新界面的窗口外壳接线：internal/winshell 画导航、会话列表、标题栏、右栏，这里只把真实数据和动作递给它。
// 这个文件和 workbench_*.go 一样不许直接 import 任何组件库、不许调旧桥（archcheck/newui_test.go）。

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/loadui"
	"sumika-native/internal/winshell"
)

// wbNav 是新界面导航里的五项：原型的本地、QQ、设置，加记忆、运行（主人 2026-11-08 定）。
// 顺序和下标对应 a.rail：0 本地 1 QQ 2 记忆 3 运行 4 设置。
var wbNav = []winshell.NavItem{
	{ID: "local", Label: "本地", Icon: winshell.IconHome, IconOn: chatui.Icon("home", true)},
	{ID: "qq", Label: "QQ", Icon: winshell.IconChat, IconOn: chatui.Icon("qq", true)},
	{ID: "memory", Label: "记忆", Icon: winshell.IconMemory, IconOn: chatui.Icon("brain", true), Bottom: true},
	{ID: "runtime", Label: "运行", Icon: winshell.IconRuntime, IconOn: chatui.Icon("dashboard", true), Bottom: true},
	{ID: "settings", Label: "设置", Icon: winshell.IconSettings, IconOn: chatui.Icon("settings", true), Bottom: true},
}

const (
	segPrivate = 0
	segGroup   = 1
)

// wbShell 是外壳在根包里的账：令牌、状态、列表缓存。
type wbShell struct {
	kit          *winshell.Kit
	tools        wbToolsState // 输入框底行的药丸
	cwd          wbCwdState   // 输入框底行的工作目录药丸
	railSel      [2]string    // 本地、QQ 两页各自上次看的会话
	st           winshell.State
	seg          [2]int // 本地、QQ 各自当前的分段：私聊或群聊
	sideTab      int    // 右栏当前的标签
	sideContext  sideContextState
	sideContact  sideContactState
	sideLog      sideLogState
	forwardSide  forwardSideState
	lastSel      string
	panesSaved   winshell.Panes // 已经存进桌面设置的（或读到的）分栏偏好
	panesPending winshell.Panes
	panesDirty   bool
	panesAt      time.Time
	panesTrack   saveTrack[winshell.Panes]
	lookTrack    saveTrack[desksettings.Look]

	settingsSearch string     // 设置页左栏的“搜索设置”
	widthPct       [2]float32 // 聊天区宽度百分比：[0] 私聊 [1] 群聊（桌面设置的 look.privateWidth、groupWidth）
	lookDirty      bool       // 外观改了还没写进桌面设置
	lookAt         time.Time
	fontOpen       bool            // 字体下拉开着
	material       string          // 面板材质：liquid、blur、solid（读桌面设置，坏值是 liquid）
	look           chatui.Settings // 新界面的外观设置（字号、圆角、疏密、冷暖、背景图案、玻璃）；深浅每帧由窗口带进来
	lookInit       bool
	list           []*conv // 当前分段过滤后的会话
	listKey        wbListKey
	listInit       bool
}

type wbListKey struct {
	rail, seg int
	rev       uint64
	query     string
}

// wbKit 返回外壳令牌，外观设置变了就重算。深浅取窗口当前的明暗（随系统或用户选的亮色、夜间）；
// 字号取桌面外观里存的正文字号（legacyTextSize，默认 15，ADR-350），圆角、疏密先用 chatui 的默认值，等设置页的新外观设置（原型外壳迁移 H）接上再换这一处来源。
func (a *app) wbKit(dark bool, font string) *winshell.Kit {
	w := &a.workbench.shell
	if !w.lookInit {
		w.look, w.lookInit = chatui.DefaultSettings(), true
	}
	s := w.look
	s.Dark = dark
	s.Text = a.legacyTextSize() // 字号档沿用桌面外观里存的正文字号；圆角、疏密先用默认
	if w.kit == nil {
		w.kit = winshell.NewKit(s, font)
	} else {
		w.kit.Set(s, font)
	}
	w.kit.SetMaterial(winshell.ParseMaterial(w.material))
	w.kit.Win = a.winControls
	return w.kit
}

// setPanelMaterial 改面板材质：马上生效，再异步存进桌面设置。
func (a *app) setPanelMaterial(m string) {
	w := &a.workbench.shell
	if w.material == m || winshell.ParseMaterial(m).String() != m {
		return
	}
	w.material = m
	if a.desktop == nil {
		return
	}
	st := a.desktop
	a.deskSaves.add(func() {
		if err := st.SetPanelMaterial(m); err != nil && a.postFn != nil {
			a.post(func() { a.status = "面板材质没写下：" + err.Error() })
		}
	})
}

func panesFromDesk(p desksettings.ShellPanes) winshell.Panes {
	return winshell.Panes{ListRatio: p.ListRatio, Compact: p.Compact, SideWidth: p.SideWidth, SideOpen: p.SideOpen}
}

func panesToDesk(p winshell.Panes) desksettings.ShellPanes {
	return desksettings.ShellPanes{ListRatio: p.ListRatio, Compact: p.Compact, SideWidth: p.SideWidth, SideOpen: p.SideOpen}
}

// syncWbPanes 把外壳里改过的分栏偏好防抖写进桌面设置：偏好 500 毫秒没再变才写，拖分隔条不会一帧写一次。
// 新壳用自己的 shellPanes 键，不读旧界面的 panes（旧键里的 compact=true 会让新壳一打开就是头像列）。
func (a *app) syncWbPanes(c *ui.Context) {
	w := &a.workbench.shell
	if w.st.Panes == w.panesSaved {
		w.panesDirty = false
		return
	}
	if w.panesTrack.skip(w.st.Panes) {
		w.panesDirty = false // 这一份刚写失败过，不原样重试；用户再动了才试
		return
	}
	if !w.panesDirty || w.st.Panes != w.panesPending {
		w.panesDirty, w.panesPending, w.panesAt = true, w.st.Panes, c.Now()
		w.panesTrack.rearm()
		c.After(wbLookSaveDelay)
		return
	}
	if rest := wbLookSaveDelay - c.Now().Sub(w.panesAt); rest > 0 {
		c.After(rest)
		return
	}
	if a.desktop == nil {
		return // 桌面设置还没读回来：留着“改过”，读回来后接着写
	}
	cur := w.st.Panes
	w.panesSaved, w.panesDirty = cur, false
	p, st := panesToDesk(cur), a.desktop
	seq := w.panesTrack.begin()
	a.deskSaves.add(func() {
		err := st.SetShellPanes(p)
		a.post(func() { // 回界面线程对账：序号不是最新的旧结果不改账；最新一次失败就退回磁盘上的值，用户再改时重写
			if w.panesTrack.finish(seq, cur, err) {
				w.panesSaved = w.panesTrack.disk
				a.status = "栏位偏好没写下：" + err.Error()
			}
		})
	})
}

// adoptPanes 收下读盘读到的分栏偏好。读盘回来前用户已经动过（防抖里有待写的改动）就留着用户的，
// 只把“磁盘上现在是什么”记准，用户的改动照常写下去；没动过就用读到的。
func (w *wbShell) adoptPanes(p winshell.Panes) {
	touched := w.panesDirty
	w.panesSaved = p
	w.panesTrack.adopt(p)
	if !touched {
		w.st.Panes = p
	}
}

// adoptLook 同 adoptPanes：读盘回来前用户已经改过外观就不覆盖。
func (w *wbShell) adoptLook(l desksettings.Look) {
	w.lookTrack.adopt(l)
	w.lookInit = true
	if w.lookDirty {
		return
	}
	w.look = lookFromDesk(l)
	w.widthPct = [2]float32{l.PrivateWidth, l.GroupWidth}
}

// workbenchView 画新界面整个窗口。dark 是窗口当前明暗，由根视图在应用主题之后给出。
func (a *app) workbenchView(c *ui.Context, dark bool) {
	w := &a.workbench.shell
	kit := a.wbKit(dark, c.Theme().Font)
	a.syncSegment()
	a.syncConvList()
	sp := &winshell.Spec{
		Nav: wbNav, Page: wbNav[a.rail].ID, OnPage: a.wbGoPage,
		Chat:    a.rail <= 1,
		OnPanes: func() {},
	}
	if sp.Chat {
		sp.Sidebar = a.wbSidebar(kit)
		sp.Conv = a.wbChatSpec(c, kit)
		sp.Side = a.wbSideSpec()
		a.wbSideTabs(c, kit, &sp.Side)
		a.wbForwardSide(c, kit, &sp.Side)
	} else if a.rail == 4 {
		sp.Settings = a.wbSettingsSpec(c, kit)
	} else {
		sp.Content = func() { a.legacyPage(c) }
	}
	sp.Overlays = func() { a.wbGroupDialog(c, kit); a.wbPasteDialog(c, kit) }
	kit.Frame(c, &w.st, sp)
	a.syncWbPanes(c)
}

func (a *app) wbGoPage(id string) {
	for i, it := range wbNav {
		if it.ID == id {
			a.rail = i
		}
	}
	a.workbench.shell.seg = [2]int{}
	a.wbSyncRailConversation()
}

// wbSyncRailConversation 让聊天区跟着导航走：切到本地或 QQ 时，聊天区不能还停在另一页的会话上。
// 回到这一页上次看的会话；没看过或它不在了就是空的，等人选。
func (a *app) wbSyncRailConversation() {
	if a.rail > 1 {
		return
	}
	railOf := func(id string) int {
		if a.keyOf(id).Platform == "local" {
			return 0
		}
		return 1
	}
	w := &a.workbench.shell
	if a.sel != "" {
		if railOf(a.sel) == a.rail {
			return
		}
		w.railSel[railOf(a.sel)] = a.sel
	}
	if next := w.railSel[a.rail]; next != "" && a.convByID(next) != nil {
		a.open(next)
		return
	}
	if a.sel != "" {
		a.switchTo("")
		a.title, a.group, a.loading = "", false, false
	}
}

// 分段随选中的会话走：从别处（点链接、新建群聊）打开了另一类会话，分段跟过去，不然选中的那一行看不见。
func (a *app) syncSegment() {
	w := &a.workbench.shell
	if a.sel == w.lastSel || a.rail > 1 {
		return
	}
	w.lastSel = a.sel
	if a.sel == "" {
		return
	}
	if cv := a.convByID(a.sel); cv != nil {
		w.seg[a.rail] = segOf(cv)
	}
}

func segOf(cv *conv) int {
	if cv.Key.Scene == "group" || cv.Kind == "home" || cv.Kind == "group" {
		return segGroup
	}
	return segPrivate
}

func (a *app) convByID(id string) *conv {
	for _, list := range [][]conv{a.local, a.homes, a.convs} {
		for i := range list {
			if list[i].Key.Conversation == id {
				return &list[i]
			}
		}
	}
	return nil
}

// syncConvList 在概览、搜索、分段变了才重建当前分段的会话表，普通帧复用。
func (a *app) syncConvList() {
	w := &a.workbench.shell
	if a.rail > 1 {
		return
	}
	k := wbListKey{rail: a.rail, seg: w.seg[a.rail], rev: a.dialogRevision, query: a.search[a.rail]}
	if w.listInit && k == w.listKey {
		return
	}
	query := strings.ToLower(strings.TrimSpace(k.query))
	var src []conv
	switch {
	case a.rail == 0 && k.seg == segPrivate:
		src = a.local
	case a.rail == 0:
		src = a.homes
	default:
		src = a.convs
	}
	out := w.list[:0]
	for i := range src {
		cv := &src[i]
		if a.rail == 1 && segOf(cv) != k.seg {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(convName(*cv)), query) && !strings.Contains(strings.ToLower(cv.Last.Text), query) && !strings.Contains(strings.ToLower(cv.Project), query) {
			continue
		}
		if _, visible := conversationItem(cv); !visible {
			continue
		}
		out = append(out, cv)
	}
	w.list, w.listKey, w.listInit = out, k, true
}

// wbConversation 把一个会话变成列表里的一行：预览是最后一条消息；空的本地会话写“还没有消息”，不编时间和内容。
func (a *app) wbConversation(cv *conv) winshell.Conversation {
	preview := strings.ReplaceAll(cv.Last.Text, "\n", " ")
	if preview == "" {
		preview = "还没有消息"
	}
	if cv.Project != "" { // 选了项目目录的会话在预览前写项目名：列表里一眼分出是哪个项目
		preview = cv.Project + " · " + preview
	}
	if n := len(a.heldBy[cv.Key.Conversation]); n > 0 { // 重启时没来得及处理的话：不在前台的会话也要看得到
		preview = fmt.Sprintf("[%d 句没处理] ", n) + preview
	}
	name := convName(*cv)
	return winshell.Conversation{
		ID: cv.Key.Conversation, Name: name, Preview: preview, Unread: max(cv.Unread, 0), When: wbWhen(cv.Last.CreatedAt, time.Now()),
		AvatarIndex: avatarIndexOf(name), Avatar: a.avs().peek(avatarKeyOfConv(cv.Key)), Square: cv.Key.Platform == "local",
	}
}

// wbWhen 是会话行右上角的时间：今天写时分，今年写月日，更早带年。没有消息不写，不编时间。
func wbWhen(ms int64, now time.Time) string {
	if ms == 0 {
		return ""
	}
	t := time.UnixMilli(ms).In(now.Location())
	switch {
	case t.Year() != now.Year():
		return t.Format("2006年1月2日")
	case t.YearDay() == now.YearDay():
		return t.Format("15:04")
	}
	return t.Format("1月2日")
}

// avatarIndexOf 按名字稳定地选一个头像色。
func avatarIndexOf(name string) int {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int(h.Sum32() % 8)
}

func (a *app) wbSidebar(kit *winshell.Kit) *winshell.SidebarSpec {
	w := &a.workbench.shell
	rail := a.rail
	sp := &winshell.SidebarSpec{
		Segments: []string{"私聊", "群聊"}, Segment: &w.seg[rail],
		Count: len(w.list), At: func(i int) winshell.Conversation { return a.wbConversation(w.list[i]) },
		Selected: a.sel, OnSelect: a.wbOpen,
		Search: &a.search[rail], Hint: "搜索会话",
		OnVisible: func(first, last int) {
			for i := first; i <= last && i < len(w.list); i++ {
				a.avs().want(avatarKeyOfConv(w.list[i].Key))
			}
		},
	}
	switch {
	case strings.TrimSpace(a.search[rail]) != "":
		sp.Empty = "没有找到会话，换个词试试"
	case rail == 0 && w.seg[0] == segPrivate:
		sp.Empty = "还没有本地私聊"
	case rail == 0 && !a.canCreateGroup():
		sp.Empty = "群聊：" + a.capReason("local-group", "当前数据源不能建群")
	case rail == 0:
		sp.Empty = "还没有群聊，点右上角的“+”新建"
	case w.seg[1] == segGroup:
		sp.Empty = "还没有 QQ 群聊"
	default:
		sp.Empty = "还没有 QQ 私聊"
	}
	// 第一份概览还没到：什么都别写，读得慢了才写“取数据中…”（loadstate，占位和文字同高，出现前后不跳）
	empty := sp.Empty
	sp.Placeholder = func(c *ui.Context) {
		ov := a.ovLoad.View(c.Now())
		loadui.Wake(c, ov)
		if a.ovLoad.Loading() {
			loadui.Line(ov, "取数据中…", func(t string) ui.Element {
				return ui.Text(c.Key("conversation-loading"), t).TextColor(kit.P.Muted).Padding(kit.G.ListInset)
			})
			return
		}
		if empty != "" {
			ui.Text(c.Key("conversation-empty"), empty).TextColor(kit.P.Muted).Padding(kit.G.ListInset)
		}
	}
	if rail == 0 && a.canCreateGroup() {
		sp.MenuLabel = "更多"
		sp.Menu = []winshell.MenuItem{{Label: "新建群聊", Icon: winshell.IconGroup, Action: a.beginGroup}}
	}
	sp.RowMenu = a.convRowMenu
	return sp
}

func (a *app) wbOpen(id string) {
	a.workbench.shell.st.OpenChat()
	a.open(id)
}

// wbSideSpec 是右栏的“资料”：这个会话是什么、哪些角色看得到它。
func (a *app) wbSideSpec() winshell.SideSpec {
	sp := winshell.SideSpec{MembersEmpty: "还没有角色在看这个会话"}
	cv := a.convByID(a.sel)
	if cv == nil {
		return sp
	}
	sp.Facts = a.wbSideFacts(cv)
	for _, id := range cv.Observers {
		name := id
		if ch, ok := a.charOf(id); ok && ch.Name != "" {
			name = ch.Name
		}
		sp.Members = append(sp.Members, winshell.Member{Name: name, AvatarIndex: avatarIndexOf(name), Square: true})
	}
	return sp
}

// wbSubtitle 是标题栏的状态：只说后台真有的状态（在回复、在等批准、在取数据），没有就空着；演示数据模式写明是编的。
func (a *app) wbSubtitle() string {
	if a.loading {
		return "取数据中…"
	}
	if a.localStatus[a.sel].Typing {
		return "对方正在输入…"
	}
	if a.hasQuestion(a.keyOf(a.sel)) { // 等回答时一定也在忙，所以排在"正在回复"前面
		return "在等你回答"
	}
	if run := a.localRuns[a.sel]; run != nil && (run.busy || run.submitting) {
		return "正在回复"
	}
	k := a.keyOf(a.sel)
	if a.hasApproval(k) {
		return "在等你批准"
	}
	if a.demoMode {
		return "演示数据（全是编的）" // 旧界面的标题带上有这一行；新界面没有标题带，放在状态位上
	}
	return ""
}

// wbCharColor 是会话里第一个角色的角色色（后台给的十六进制），聊天背景的光晕和你的气泡用它；
// 没有角色（QQ 会话）或色值不合法，就用原型的默认角色色（青绿），不按名字随机取：同一套背景光晕不该每个会话换一个色相。
func (a *app) wbCharColor(cv *conv, name string) chatui.OKLCH {
	if cv != nil {
		for _, id := range cv.Observers {
			if ch, ok := a.charOf(id); ok {
				if col := chatColor(ch.Color); col != (chatui.OKLCH{}) {
					return col
				}
			}
		}
	}
	return chatui.DefaultChar
}

// wbOverlayInsets 是聊天区上下两个浮层各占多高（标题栏；输入区含引用卡、附件条，实测）。
// 消息列的可见区 = 列表框去掉这两块。别的线算粘性头像钉住的位置、“跳到最新”按钮、滚没滚到底时用它：
// 可见底边 = 列表框下沿 − bottom，可见顶边 = 列表框上沿 + top。
func (a *app) wbOverlayInsets() (top, bottom float32) { return a.workbench.shell.st.OverlayInsets() }
