package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/uitest"
)

// 正式窗口的结构和对齐检查。两层：
//  1. uitest.CheckTree 和原型共用：四边 padding、子不越内距、兄弟间距、圆角不过半高、贴边的角同心；
//  2. 内容边线：同一块里同级的内容要对齐，比真正画出来的字和图标，跨容器也比，不比外层盒子。
//
// 新加的外壳组件要进 alignAreas 或下面的边线表；消息列表（虚拟列表、气泡有自己的按像素测试）不在这里。
// 主人 2026-11-09：“老是出对齐问题，是结构不对所以没检测到还是压根没检测”。两样都有，这里补上。

// alignAreas 是走结构检查的外壳区域：每一块都从这个元素开始往下查。
var alignAreas = []string{"navigation", "sidebar", "chat-header", "window-capsule", "composer", "composer-note", "side-header", "side-content"}

// alignSkip 是不走结构检查的东西，每条写清为什么。外壳里新画了东西，不在 alignAreas 也不在这里，TestWindowCoverage 就红。
var alignSkip = map[string]string{
	"workbench-messages":  "消息列表：虚拟列表，气泡有 chatui 的按像素测试",
	"chat-header-fill":    "标题栏浮条的玻璃底，和 chat-header 同位，边线由 chat-header 查",
	"chat-card":           "聊天窗本身：只有底，里面的区域各自查",
	"side":                "右栏本身：只有底，顶行和内容各自查（和 chat-card 同一个道理）",
	"window-capsule-fill": "窗口按钮胶囊的玻璃底，和 window-capsule 同位，边线由 window-capsule 查",
}

type alignScene struct {
	name          string
	sel           string
	group         bool
	rail          int
	width, height int
	dark          bool
	text          float32
}

func alignScenes() []alignScene {
	var out []alignScene
	for _, place := range []struct {
		name  string
		sel   string
		group bool
		rail  int
	}{
		{"本地私聊", "local:r1", false, 0},
		{"本地群聊", "home:g1", true, 0},
		{"QQ私聊", "qq:private:80001:90001", false, 1},
		{"QQ群", "qq:group:90002", true, 1},
	} {
		// 深色不改几何，只留一个组合确认；宽度和字号才交叉
		for _, c := range []struct {
			w    int
			dark bool
			text float32
		}{{2048, false, chatui.TextDefault}, {2048, false, 20}, {1200, false, chatui.TextDefault}, {1200, false, 20}, {2048, true, chatui.TextDefault}} {
			out = append(out, alignScene{fmt.Sprintf("%s/宽%d/深%v/字%v", place.name, c.w, c.dark, c.text), place.sel, place.group, place.rail, c.w, 1030, c.dark, c.text})
		}
	}
	return out
}

func alignTester(t *testing.T, sc alignScene) (*app, *ui.Tester) {
	t.Helper()
	a := shellApp()
	a.sel, a.group, a.rail = sc.sel, sc.group, sc.rail
	a.chars[0].Model = "demo/model-00"
	a.workbench.shell.st.Panes.SideOpen = true
	a.winControls = (&fakeWindow{}).controls() // 窗口按钮也算外壳，一起查
	tt := shellTester(a, sc.width, sc.height)
	if sc.dark {
		a.look.settings.Mode = "night"
	}
	tt.Frame()
	a.setTextSizeLive(sc.text)
	tt.Frame()
	tt.Frame()
	// 场景真的生效了，才算查过这个组合
	if k := a.workbench.shell.kit; k.S.Dark != sc.dark || k.S.Text != sc.text {
		t.Fatalf("%s：场景没生效，深=%v 字=%v", sc.name, k.S.Dark, k.S.Text)
	}
	return a, tt
}

func TestWindowStructure(t *testing.T) {
	for _, sc := range alignScenes() {
		a, tt := alignTester(t, sc)
		root := uitest.Tree(tt)
		var pairs []uitest.Pair
		for _, key := range alignAreas {
			if n := root.Find(key); n != nil {
				uitest.CheckTree(t, n, sc.name, a.workbench.shell.kit.T.RInnerMin, &pairs)
			}
		}
		if len(pairs) == 0 {
			t.Fatalf("%s：一块都没查到，检查走空了", sc.name)
		}
	}
}

func TestWindowContentEdges(t *testing.T) {
	for _, sc := range alignScenes() {
		_, tt := alignTester(t, sc)
		root := uitest.Tree(tt)
		same := func(what string, want float32, got ...float32) {
			t.Helper()
			for i, v := range got {
				if !uitest.Near(v, want) {
					t.Errorf("%s %s：第 %d 个是 %.2f，应和 %.2f 对齐", sc.name, what, i, v, want)
				}
			}
		}
		// 输入框是胶囊（ADR-351）：发送键到外框上、右、下等距（同心），底行药丸搬到了右栏
		if box, send := root.Find("composer"), root.Find("send"); box != nil && send != nil {
			d := uitest.Distances(box.Box, send.Box)
			same("发送键到外框右、下等距", d[1], d[2])
			same("发送键到外框上、下等距", d[0], d[2])
		} else if sc.rail == 0 && !sc.group {
			t.Errorf("%s：本地私聊没有输入框", sc.name)
		}
		// 聊天窗：标题栏浮条、消息列、输入框左右边线一条线（默认全宽）
		if head, frame := root.Find("chat-header"), root.Find("composer-frame"); head != nil && frame != nil {
			same("浮条和输入框左边线", head.Box.X, frame.Box.X)
			same("浮条和输入框右边线", head.Box.X+head.Box.W, frame.Box.X+frame.Box.W)
			if row := root.Find("message-row"); row != nil {
				same("消息列和浮条左边线", head.Box.X, row.Box.X)
				same("消息列和浮条右边线", head.Box.X+head.Box.W, row.Box.X+row.Box.W)
			}
		}
		// 标题栏：头像到浮条左、上、下等距
		if head, av := root.Find("chat-header"), root.Find("avatar-header-avatar"); head != nil && av != nil {
			d := uitest.Distances(head.Box, av.Box)
			same("标题头像到浮条上、下、左等距", d[3], d[0], d[2])
		}
		// 会话列表：搜索框、分段、会话行左右边线一条线
		if search, seg := root.Find("search-box"), root.Find("sidebar-tools"); search != nil && seg != nil && len(seg.Children) > 0 {
			left, right := search.Box.X, search.Box.X+search.Box.W
			track := seg.Children[0]
			same("分段和搜索框左边线", left, track.Box.X)
			same("分段和搜索框右边线", right, track.Box.X+track.Box.W)
			root.Walk(func(n *uitest.Node) {
				if strings.HasPrefix(n.Key, "conversation-") && !strings.HasPrefix(n.Key, "conversation-slot-") && !strings.HasPrefix(n.Key, "conversation-menu") && !strings.HasPrefix(n.Key, "conversation-list") {
					same("会话行和搜索框左边线 "+n.Key, left, n.Box.X)
					same("会话行和搜索框右边线 "+n.Key, right, n.Box.X+n.Box.W)
				}
			})
		}
		// 导航：logo 和每个图标在同一条竖中线上
		if nav := root.Find("navigation"); nav != nil {
			center := nav.Box.X + nav.Box.W/2
			if logo := root.Find("app-logo"); logo != nil {
				same("logo 在导航中线", center, logo.Box.X+logo.Box.W/2)
			}
			for _, id := range []string{"local", "qq", "memory", "runtime", "settings"} {
				if item := root.Find("nav-" + id); item != nil {
					same("导航项在中线 "+id, center, item.Box.X+item.Box.W/2)
				}
			}
		}
		// 右栏：顶行小胶囊和下面每一节的内容左边线一条线
		if pill, members := root.Find("side-tabs"), root.Find("members-section"); pill != nil && members != nil {
			same("右栏胶囊和成员左边线", pill.Box.X, members.ContentLeft())
		}
	}
}

// 每个画了底或边框的元素，要么在结构检查的区域里，要么在 alignSkip 里写明原因。
func TestWindowCoverage(t *testing.T) {
	areas := map[string]bool{}
	for _, k := range alignAreas {
		areas[k] = true
	}
	for _, sc := range alignScenes() {
		_, tt := alignTester(t, sc)
		var walk func(n *uitest.Node)
		walk = func(n *uitest.Node) {
			if n.Hidden || n.Box.W <= 0 || n.Box.H <= 0 {
				return
			}
			if areas[n.Key] {
				return
			}
			if _, ok := alignSkip[n.Key]; ok {
				return
			}
			if n.Painted && n.Parent != nil && n.Parent.Parent != nil && !strings.HasPrefix(n.Key, "chat-local") && !strings.HasPrefix(n.Key, "chat-home") && !strings.HasPrefix(n.Key, "chat-qq") {
				t.Errorf("%s：%s 画了底或边框 %+v，不在对齐检查范围：加进 alignAreas，或在 alignSkip 写明原因", sc.name, n.Name(), n.Box)
			}
			for _, ch := range n.Children {
				walk(ch)
			}
		}
		walk(uitest.Tree(tt))
	}
}
