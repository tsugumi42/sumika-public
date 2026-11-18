package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/uitest"
)

type (
	geometryNode = uitest.Node
	geometryPair = uitest.Pair
)

var (
	geometryExceptions = uitest.Exceptions
	geometryTree       = uitest.Tree
	near               = uitest.Near
	distances          = uitest.Distances
	contains           = uitest.Contains
	equal              = uitest.Equal
)

const exceptionFloating = uitest.ExceptionFloating

func checkTree(t *testing.T, root *geometryNode, scene string, pairs *[]geometryPair) {
	uitest.CheckTree(t, root, scene, chatui.Derive(chatui.DefaultSettings()).RInnerMin, pairs)
}

func TestGeometryHasNoBypass(t *testing.T) {
	for _, line := range strings.Split(geometryExceptions, "\n") {
		if !strings.HasPrefix(line, "已批|") {
			t.Fatal("布局例外必须是主人批准过的（已批），见 ADR-285 第 11 条")
		}
	}
	if len(strings.Split(geometryExceptions, "\n")) > 2 {
		t.Fatal("布局例外超过批准的 2 条")
	}
	typ := reflect.TypeOf(surfaceInfo{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.String && f.Name != "ID" && f.Name != "Kind" {
			t.Fatalf("禁止在界面登记检查豁免：%s", f.Name)
		}
	}
}
func checkSkeleton(t *testing.T, root *geometryNode, scene string) {
	for _, key := range []string{"chat-header", "side-header", "settings-header", "side-content", "settings-content"} {
		if n := root.Find(key); n != nil {
			for _, d := range n.Pad {
				equal(t, scene, key+" 骨架内边距", d, paneInset)
			}
		}
	}
	// 导航栏顶上一排和会话栏搜索行同高同位（主人 11-06："顶部空上和其他栏的顶部标题同高度"）
	if head, search := root.Find("nav-header"), root.Find("search-row"); head != nil && search != nil {
		equal(t, scene, "导航顶行上沿", head.Box.Y, search.Box.Y)
		equal(t, scene, "导航顶行高", head.Box.H, search.Box.H)
		logo := root.Find("app-logo")
		equal(t, scene, "logo 中线", logo.Box.Y+logo.Box.H/2, head.Box.Y+head.Box.H/2)
		equal(t, scene, "logo 大小", logo.Box.W, hitSmall)
	}
	if nav := root.Find("nav-body"); nav != nil {
		for _, d := range nav.Pad {
			equal(t, scene, "导航四边", d, navInset)
		}
		equal(t, scene, "导航块间距", nav.GapY, navInset)
		first := root.Find("nav-local")
		d := distances(nav.Box, first.Box)
		for _, side := range []int{0, 1, 3} {
			equal(t, scene, "首导航边距", d[side], navInset)
		}
		// 窗口圆角由系统画，外壳自己不画圆角（ADR-284）
		for _, r := range root.Find("prototype").Radius {
			equal(t, scene, "外壳圆角", r, 0)
		}
		for _, key := range []string{"nav-local", "nav-qq", "nav-settings"} {
			if item := root.Find(key); item != nil {
				for _, r := range item.Radius {
					equal(t, scene, key+" 圆角", r, radiusNavItem)
				}
			}
		}
	}
	if box := root.Find("composer"); box != nil {
		inset := spaceMedium // 窄窗宽窗一套（ADR-284）
		for _, d := range box.Pad {
			equal(t, scene, "输入四边", d, inset)
		}
		send := root.Find("send")
		d := distances(box.Box, send.Box)
		equal(t, scene, "发送右距", d[1], inset)
		equal(t, scene, "发送底距", d[2], inset)
		equal(t, scene, "发送直径", send.Box.W, sendSize) // 固定 32，不由输入框圆角推（ADR-284）
		equal(t, scene, "发送是正圆", send.Radius[2], sendSize/2)
		header := root.Find("chat-header")
		messages := root.Find("messages-" + strings.TrimPrefix(root.Find("chat-header").Parent.Key, "chat-"))
		equal(t, scene, "标题内容／输入竖线", header.Box.X+header.Pad[3], box.Box.X)
		if messages != nil {
			equal(t, scene, "消息／输入竖线", messages.Box.X+messages.Pad[3], box.Box.X)
		}
	}
	if title := root.Find("chat-title"); title != nil {
		// 同字体同20行高：文字首行基线由相同字形布局给出，而非仅比较控件中心。
		if side := root.Find("side-title"); side != nil {
			equal(t, scene, "各栏文字基线", title.Box.Y+title.Box.H, side.Box.Y+side.Box.H)
		}
		if search := root.Find("search"); search != nil {
			equal(t, scene, "搜索／标题基线", search.Box.Y+search.Pad[0]+metaLine, title.Box.Y+title.Box.H)
		}
	}
	// 会话栏一条左边线（审查 A5）：搜索行、分段排、会话行都用 listInset，头像左缘和搜索框、分段左缘对齐
	for _, key := range []string{"search-row", "sidebar-tools"} {
		if n := root.Find(key); n != nil {
			for _, d := range n.Pad {
				equal(t, scene, key+" 会话栏内距", d, listInset)
			}
		}
	}
	if search, avatar := root.Find("search"), root.Find("avatar-conversation-avatar-A"); search != nil && avatar != nil && root.Find("sidebar") != nil && avatar.Box.W == conversationAvatar {
		equal(t, scene, "搜索框和会话头像左缘", search.Box.X, avatar.Box.X)
	}
	var inspect func(*geometryNode)
	inspect = func(n *geometryNode) {
		// 按名字认会话行（conversation-A 这类）：mygo v0.7.4 的内部节点不再带 widget 名，原来按 "Button" 认一次也没跑到
		if id, ok := strings.CutPrefix(n.Key, "conversation-"); ok && len(id) == 1 {
			// 铺满列表整宽、直角（主人 11-06："会话列表好像大部分都是直接填充不做圆角的"，ADR-284）
			for _, r := range n.Radius {
				equal(t, scene, n.Key+" 直角", r, 0)
			}
			if sidebar := root.Find("sidebar"); sidebar != nil {
				equal(t, scene, n.Key+" 铺满列表宽", n.Box.W, sidebar.Box.W)
			}
			if len(n.Children) > 0 {
				d := distances(n.Box, n.Children[0].Box)
				equal(t, scene, "会话头像到上边", d[0], conversationInset)
				equal(t, scene, "会话头像到下边", d[2], conversationInset)
				equal(t, scene, "会话头像到左边", d[3], conversationInset)
			}
		}
		for _, ch := range n.Children {
			inspect(ch)
		}
	}
	inspect(root)
	for _, key := range []string{"venues", "target-popup", "actor-popup"} {
		if track := root.Find(key); track != nil {
			for _, d := range track.Pad {
				equal(t, scene, key+" 四边", d, spaceTight)
			}
			for _, child := range track.Children {
				if child.Box.W <= 0 || child.Box.H <= 0 {
					continue
				}
				ds := distances(track.Box, child.Box)
				sides := []int{0, 2}
				if strings.HasSuffix(key, "popup") {
					sides = []int{1, 3}
				}
				for _, s := range sides {
					equal(t, scene, key+" 轮廓内缩", ds[s], spaceTight)
				}
			}
		}
	}
}
func TestSurfaceGeometry(t *testing.T) {
	if testing.Short() {
		t.Skip("整窗矩阵，放在每日整套；新外壳的矩阵在根包 TestWindow*")
	}
	var pairs []geometryPair
	scenarios := 0
	// 外观三档（审查 B1：字号、圆角、疏密铺满整窗以后，拖到两头规矩也要成立）；非默认档只跑浅色的最宽、最窄两种窗口
	looks := []struct {
		name string
		s    chatui.Settings
	}{{"默认", chatui.DefaultSettings()},
		{"最小", chatui.Settings{Text: chatui.TextMin, Radius: chatui.RadiusMin, Density: chatui.DensityMin, Wallpaper: true, Glass: true}},
		{"最大", chatui.Settings{Text: chatui.TextMax, Radius: chatui.RadiusMax, Density: chatui.DensityMax, Wallpaper: true, Glass: true}}}
	for _, look := range looks {
		for _, w := range []int{2048, 1280, 1024, 480} {
			for _, dark := range []bool{false, true} {
				if look.name != "默认" && (dark || (w != 2048 && w != 480)) {
					continue
				}
				for _, sample := range []struct{ ID, Mode string }{{"A", "chat"}, {"B", "chat"}, {"C", "chat"}, {"D", "chat"}, {"A", "scenes"}, {"A", "script"}, {"A", "choice"}, {"A", "settings"}, {"A", "list"}, {"A", "streaming"}, {"A", "failed"}, {"A", "details"}, {"A", "collapsed"}, {"B", "unread"}, {"A", "process"}, {"B", "process"}, {"A", "markdown"}, {"B", "markdown"}, {"A", "plugins"}} {
					a := newApp()
					a.Close = func() { t.Fatal("几何演练意外关闭窗口") }
					a.Look = look.s
					a.Dark = dark
					a.Layer = 1
					if sample.ID == "C" || sample.ID == "D" {
						a.Page = "qq"
					}
					a.Selected[a.Page] = sample.ID
					a.Scenes = sample.Mode == "scenes" || sample.Mode == "script" || sample.Mode == "choice"
					a.Scenario.ShowScript = sample.Mode == "script"
					if sample.Mode == "settings" {
						a.Page = "settings"
					}
					if sample.Mode == "plugins" { // 插件页：展开链接解析（开关、分段、名单、数字框），技能改了要重启（提示条、小圆点）
						a.Page, a.SettingsCat = "settings", "plugins"
						ps := a.pluginState()
						ps.Open["link-reader"], ps.On["skills"] = true, true
					}
					if sample.Mode == "list" {
						a.Layer = 0
					}
					if sample.Mode == "details" {
						a.Layer = 2
					}
					if sample.Mode == "collapsed" {
						a.Right = false
					}
					if sample.Mode == "unread" { // 会话列表里有一条没选中、带未读的（审查 C5：未读角标进几何检查）
						a.Selected["local"], a.Group, a.Layer = "A", 1, 0
						a.E.conv("B").Unread = 3
					}
					if sample.Mode == "streaming" || sample.Mode == "failed" {
						a.cv().FailNext = sample.Mode == "failed"
						a.E.submit(a.cv(), "几何状态演练。")
					}
					a.E.conv(sample.ID).Messages = append(a.E.conv(sample.ID).Messages, &message{ID: "long", Actor: "r1", Text: "第一行占位消息\n第二行同样是很长的中文内容，用来检查多行圆角。", State: "complete"})
					if sample.Mode == "markdown" { // 她的回复用 markdown：列表、行内代码、代码块（自己画的外框、复制按钮）
						a.E.conv(sample.ID).Messages = append(a.E.conv(sample.ID).Messages, &message{ID: "md", Actor: "r1", State: "complete",
							Text: "我把**今天要做的**理一下：\n\n- 先把 `notes/纸灯.md` 补完\n- 再跑一遍测试\n\n```go\nfunc lamp() float64 { return 0.6 }\n```\n\n弄完我们接着聊。"})
					}
					tt := ui.NewTester(a.view, w, 1030)
					tt.SetScale(1.25)
					if sample.Mode == "process" { // 过程组件：失败展开的命令卡、读搜行、等批准的命令卡（审批块）
						cv := a.cv()
						a.E.start(processScene("cmd-fail", cv, cv.Actors[0]), "run.start", "")
						tt.Frame() // 先跑一帧，界面才按事件排下一帧（真窗口里是点按钮触发的）
						tt.Advance(5 * time.Second)
						a.E.start(processScene("approve-cmd", cv, cv.Actors[0]), "run.start", "")
						tt.Frame()
						tt.Advance(time.Second)
						for _, m := range cv.Messages { // 过程气泡全部展开，里面的块进几何检查（ADR-287 后默认收起）
							if len(m.Steps) > 0 {
								*a.stepOpen(m.ID) = true
							}
						}
						tt.Frame()
						tt.Frame()
					}
					if sample.Mode == "streaming" || sample.Mode == "failed" {
						tt.Advance(200 * time.Millisecond)
						if sample.Mode == "streaming" {
							tt.Advance(400 * time.Millisecond)
						}
					}
					if sample.Mode == "choice" {
						click(t, tt, "场景目标")
					}
					tt.Advance(motionFast)
					scene := fmt.Sprintf("%d/dark=%v/%s/%s", w, dark, sample.ID, sample.Mode)
					if look.name != "默认" {
						scene = look.name + "/" + scene
					}
					root := geometryTree(tt)
					if sample.Mode == "markdown" && (root.Find("code") == nil || root.Find("code-head") == nil) {
						t.Fatalf("%s 代码块没画出来，几何检查查不到它", scene)
					}
					if sample.Mode == "process" && (root.Find("approval") == nil || root.Find("output") == nil) {
						t.Fatalf("%s 过程卡没画出来（没找到审批块或输出块），几何检查查不到它们", scene)
					}
					checkTree(t, root, scene, &pairs)
					checkSkeleton(t, root, scene)
					scenarios++
				}
			}
		}
	}
	applyLook(chatui.Derive(chatui.DefaultSettings())) // 令牌是包级变量，跑完恢复默认，别影响后面的测试
	out := os.Getenv("PROTO_GEOMETRY_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "geometry.json")
	}
	data, err := json.MarshalIndent(pairs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("场景=%d 全绘制树表面／嵌套对=%d 输出=%s", scenarios, len(pairs), out)
}
