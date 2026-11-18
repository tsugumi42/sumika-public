package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/segview"
)

// shellApp 是外壳测试的夹具：编造的本地私聊、本地群聊、QQ 私聊、QQ 群聊各一个，选中第一个本地私聊。
// 不连后台、不读真实数据。
// shellAppFlat 是 shellApp 换成纯色面板：液态玻璃每帧都要对整个窗口取样模糊，是 Tester 里一帧最贵的部分。
// 只给只量几何和状态、不读像素的测试用（拖动分隔条、列宽、折叠）；面板材质的外观由 look 和截图类测试用默认材质测。
func shellAppFlat() *app {
	a := shellApp()
	a.workbench.shell.material = "solid"
	return a
}

func shellApp() *app {
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}, started: time.Now(), demoMode: true, keys: map[string]api.ConversationKey{}}
	a.local = []conv{{Key: demoKey("local:r1"), Kind: "local", Title: "白羽", Last: lastLine{Text: "今晚的月色真好", CreatedAt: t0}, Observers: []string{"r1"}}}
	a.homes = []conv{{Key: demoKey("home:g1"), Kind: "home", Title: "纸灯小屋", Last: lastLine{Text: "大家早", CreatedAt: t0}, Observers: []string{"r1", "r2"}}}
	a.convs = []conv{
		{Key: demoKey("qq:private:80001:90001"), Kind: "private", Title: "编造的朋友", Last: lastLine{Text: "在吗", CreatedAt: t0}, Unread: 3},
		{Key: demoKey("qq:group:90002"), Kind: "group", Title: "编造的群", Last: lastLine{Text: "开会了", CreatedAt: t0}},
	}
	a.chars = []character{{ID: "r1", Name: "白羽"}, {ID: "r2", Name: "夜莺"}}
	a.sel, a.title = "local:r1", "白羽"
	a.msgs = []*msg{{id: "m1", who: "白羽", text: "今晚的月色真好", blocks: segview.TextBlocks("今晚的月色真好"), at: 60000}}
	a.list = a.newList()
	a.mainView = "workbench"
	// 外壳几何测试按令牌的基准比例（字号 16 = 系数 1）写期望值，不跟着默认字号（15）走
	a.initLook()
	a.setTextSizeLive(16)
	a.look.textDirty = false
	return a
}

// shellTester 在 2048×1030、像素比 1.25 的窗口里画整个新界面（减少动态效果，入场动画直接到位）。
func shellTester(a *app, w, h int) *ui.Tester {
	tt := ui.NewTester(a.view, w, h) // 走根视图：连 windowView 里的分栏读写一起测
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	return tt
}

var _ = fmt.Sprint
var _ = chatui.TextDefault

func TestShellSmoke(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	t.Logf("texts=%q", tt.Texts())
	for _, n := range []string{"本地", "QQ", "记忆", "运行", "设置", "私聊", "群聊", "白羽", "搜索会话"} {
		r, ok := tt.Find(n)
		t.Logf("%s: %v %v", n, ok, r)
	}
}

// 没选会话时聊天窗也画图案，不是一块纯色。
func TestShellWallpaperDrawnWithoutConversation(t *testing.T) {
	a := shellApp()
	a.sel, a.title = "", ""
	tt := shellTester(a, 2048, 1030)
	tt.Frame()
	if k := a.workbench.shell.kit; k.S.Wallpaper && k.Wall.Hits == 0 {
		t.Fatal("没选会话时聊天窗没画背景图案")
	}
}

func TestShellWallpaperDrawn(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	tt.Image() // 画（Draw）在出图时才执行
	k := a.workbench.shell.kit
	t.Logf("wallpaper=%v hits=%d", k.S.Wallpaper, k.Wall.Hits)
	if k.S.Wallpaper && k.Wall.Hits == 0 {
		t.Fatal("聊天背景图案没画")
	}
}

// nodeMaterial 读一个元素用的材质类型名（没有材质是空串）；找不到元素返回 found=false。
// 和 uiBoxes 一样按固定版本 mygo 的字段读，升级后读不到会 panic，要跟着改。
func nodeMaterial(tt *ui.Tester, key string) (typ string, found bool) {
	root := reflect.ValueOf(tt).Elem().FieldByName("rt").Elem().FieldByName("c").FieldByName("root")
	var walk func(reflect.Value)
	walk = func(p reflect.Value) {
		v := p.Elem()
		if k := v.FieldByName("key"); !k.IsNil() && k.Elem().Kind() == reflect.String && k.Elem().String() == key && !found {
			found = true
			if m := v.FieldByName("material"); !m.IsNil() {
				typ = m.Elem().Type().String()
			}
		}
		for ch := v.FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
			walk(ch)
		}
	}
	walk(root)
	return
}

// 面板材质三档：标题栏和输入框外框各用对应的材质（设计语言第二节第 2 层）；滚动边缘只在液态玻璃和磨砂档画。
func TestShellPanelMaterialThreeLevels(t *testing.T) {
	cases := []struct {
		material, want string
		edges          bool
	}{
		{"liquid", "glass.Glass", true},
		{"blur", "glass.Blur", true},
		{"solid", "", false},
		{"", "glass.Glass", true},        // 没设：液态玻璃
		{"frosted", "glass.Glass", true}, // 坏值：液态玻璃
	}
	for _, tc := range cases {
		a := shellApp()
		a.workbench.shell.material = tc.material
		tt := shellTester(a, 2048, 1030)
		for _, key := range []string{"chat-header-fill", "composer"} {
			got, ok := nodeMaterial(tt, key)
			if !ok {
				t.Fatalf("%q：找不到 %s", tc.material, key)
			}
			if got != tc.want {
				t.Errorf("%q：%s 的材质是 %q，应为 %q", tc.material, key, got, tc.want)
			}
		}
		_, edge := nodeMaterial(tt, "edge-top1")
		if edge != tc.edges {
			t.Errorf("%q：滚动边缘 画了=%v，应为 %v", tc.material, edge, tc.edges)
		}
	}
}

// 系统开了“降低透明度”：选了液态玻璃或磨砂也退回实色，滚动边缘不画。
func TestShellReduceTransparencyFallsBackToSolid(t *testing.T) {
	for _, material := range []string{"liquid", "blur"} {
		a := shellApp()
		a.workbench.shell.material = material
		tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchView(c, a.look.dark) }, 2048, 1030)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, ReduceTransparency: true})
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		for _, key := range []string{"chat-header-fill", "composer"} {
			if got, _ := nodeMaterial(tt, key); got != "" {
				t.Errorf("%s 降低透明度时 %s 还在用 %s", material, key, got)
			}
		}
		if _, edge := nodeMaterial(tt, "edge-top1"); edge {
			t.Errorf("%s 降低透明度时还画了滚动边缘", material)
		}
	}
}

// 改面板材质马上生效并存进桌面设置；坏值不接受。
func TestShellSetPanelMaterialPersists(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	a := shellApp()
	a.desktop = st
	a.postFn = func(f func()) { f() }
	a.setPanelMaterial("blur")
	a.setPanelMaterial("frosted") // 坏值：不接受
	if a.workbench.shell.material != "blur" {
		t.Fatalf("界面里的材质 %q", a.workbench.shell.material)
	}
	a.deskSaves.wait()
	if got, err := st.PanelMaterial(); err != nil || got != "blur" {
		t.Fatalf("没存进设置：%q %v", got, err)
	}
}
