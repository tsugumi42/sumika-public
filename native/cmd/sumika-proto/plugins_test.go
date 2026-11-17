package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 插件页（docs/规格-插件页.md 第七节）。

func pluginTester(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a := newApp()
	a.Page, a.Layer = "settings", 1
	tt := tester(a)
	tt.Frame()
	click(t, tt, "插件")
	tt.Frame()
	tt.Frame()
	return a, tt
}

func frames(tt *ui.Tester) { tt.Frame(); tt.Frame() }

// clickKey 按元素的键点它的中心（名字和左边的说明文字一样时，按名字点会点到说明上）。
func clickKey(t *testing.T, tt *ui.Tester, key string) {
	t.Helper()
	n := geometryTree(tt).Find(key)
	if n == nil {
		t.Fatalf("找不到 %s", key)
	}
	tt.ClickAt(n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2)
}

// 1. 十个插件都画出来；点开关只切开关不展开，点行展开、再点收起；不能关的没有开关；核心插件在最上面、出错的接着、带角标；
// 箭头在最右边、开关在它左边，箭头右缘和展开区里控件的右缘对齐。
func TestPluginsCards(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	for _, p := range s.List {
		if geometryTree(tt).Find("plugin-"+p.ID) == nil {
			t.Errorf("没画出 %s", p.Name)
		}
	}
	if _, ok := tt.Find("启用记忆"); ok {
		t.Error("记忆是 Core，不该有开关")
	}
	if _, ok := tt.Find("启用渠道回复"); ok {
		t.Error("渠道回复是 Core，不该有开关")
	}
	// 排序：核心插件在最上面，然后是出错的（主人 11-07）
	var order []string
	for _, p := range s.List {
		order = append(order, p.ID)
	}
	y := func(id string) float32 { return geometryTree(tt).Find("plugin-" + id).Box.Y }
	sort.Slice(order, func(i, j int) bool { return y(order[i]) < y(order[j]) })
	if top := strings.Join(order[:3], ","); top != "memory,channel-responder,web" || geometryTree(tt).Find("plugin-web").Find("error-badge") == nil {
		t.Errorf("前三张应该是两个核心插件、然后是出错的联网（带角标）：%s", top)
	}
	// 点开关只切开关
	click(t, tt, "启用技能")
	frames(tt)
	if !s.On["skills"] || s.Open["skills"] || tt.HasText("一次最多读") {
		t.Errorf("点开关应该只切开关：开 %v，展开 %v", s.On["skills"], s.Open["skills"])
	}
	// 点行展开、再点收起
	click(t, tt, "技能")
	frames(tt)
	if !tt.HasText("一次最多读") || !tt.HasText("重启接收后生效") {
		t.Errorf("点行应该展开、写生效方式：%v", tt.Texts())
	}
	click(t, tt, "技能")
	frames(tt)
	if tt.HasText("一次最多读") {
		t.Error("再点应该收起")
	}
	// 箭头单独是个按钮，点它也能展开
	click(t, tt, "展开技能")
	frames(tt)
	if !s.Open["skills"] {
		t.Error("点箭头应该展开")
	}
	click(t, tt, "展开技能")
	frames(tt)
	// 箭头在开关右边；箭头右缘和展开区里控件（链接解析的数字框）右缘对齐
	s.Open["link-reader"] = true
	frames(tt)
	sw, ex := geometryTree(tt).Find("plugin-skills").Find("switch-slot"), geometryTree(tt).Find("plugin-skills").Find("expand")
	if ex.Box.X < sw.Box.X+sw.Box.W {
		t.Errorf("箭头（%.1f）应该在开关格子（右缘 %.1f）右边", ex.Box.X, sw.Box.X+sw.Box.W)
	}
	lr := geometryTree(tt).Find("plugin-link-reader")
	in := lr.Find("plugin-link-reader-maxLinks-field").Find("input")
	if e := lr.Find("expand"); !near(e.Box.X+e.Box.W, in.Box.X+in.Box.W) {
		t.Errorf("箭头右缘 %.1f 和展开区控件右缘 %.1f 没对齐", e.Box.X+e.Box.W, in.Box.X+in.Box.W)
	}
	s.Open["link-reader"] = false
	frames(tt)
	// 各卡的箭头对成一列（有开关的、写原因的都一样）
	cx := func(id string) float32 { n := geometryTree(tt).Find("plugin-" + id).Find("chevron"); return n.Box.X }
	if !near(cx("memory"), cx("skills")) || !near(cx("repeater"), cx("skills")) {
		t.Errorf("箭头没对齐：记忆 %.1f、复读 %.1f、技能 %.1f", cx("memory"), cx("repeater"), cx("skills"))
	}
}

// 2. 搜"超时"：链接解析和联网自动展开、只显示命中的项；搜不到显示空状态和清除；清除后回到原来的展开状态。
func TestPluginsSearch(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["memory"] = true // 搜索前手动展开的
	a.Search = "超时"
	frames(tt)
	if geometryTree(tt).Find("plugin-memory") != nil || geometryTree(tt).Find("plugin-link-reader") == nil || geometryTree(tt).Find("plugin-web") == nil {
		t.Fatalf("应该只剩链接解析和联网：%v", tt.Texts())
	}
	if !tt.HasText("取视频的超时") || tt.HasText("视频大小上限") || !tt.HasText("超时") {
		t.Errorf("展开区应该只显示命中的项：%v", tt.Texts())
	}
	a.Search = "没有这种词"
	frames(tt)
	if !tt.HasText("没有找到「没有这种词」") {
		t.Fatalf("搜不到应该有空状态：%v", tt.Texts())
	}
	click(t, tt, "清除")
	frames(tt)
	if a.Search != "" || !tt.HasText("会写入记忆的角色") || tt.HasText("取视频的超时") {
		t.Errorf("清除后应该回到原来的展开状态（记忆展开、链接解析收着）：%v", tt.Texts())
	}
}

// 3. 改要重启的 → 提示条、计数、小圆点；点重启接收 → 都消失。改马上生效的不出提示条。改回去也不算。
func TestPluginsRestart(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["link-reader"] = true
	frames(tt)
	clickKey(t, tt, "plugin-link-reader-expand") // 链接解析：马上生效
	frames(tt)
	if v, _ := s.Vals["link-reader.expand"].(bool); !v || tt.HasText("要重启接收才生效") {
		t.Fatalf("马上生效的改动不该出提示条：%v", tt.Texts())
	}
	s.Open["link-reader"] = false // 收起来，下面的卡才在窗口里
	frames(tt)
	click(t, tt, "启用技能")
	click(t, tt, "启用子任务")
	frames(tt)
	if !tt.HasText("有 2 个插件的改动要重启接收才生效") || geometryTree(tt).Find("plugin-skills").Find("pending-dot") == nil {
		t.Fatalf("改了两个要重启的插件：%v", tt.Texts())
	}
	click(t, tt, "启用子任务") // 改回去
	frames(tt)
	if !tt.HasText("有 1 个插件的改动要重启接收才生效") {
		t.Errorf("改回去的不该再算：%v", tt.Texts())
	}
	click(t, tt, "重启接收")
	frames(tt)
	if !tt.HasText("正在重启接收……") {
		t.Errorf("点了以后应该在重启：%v", tt.Texts())
	}
	tt.Advance(1100 * time.Millisecond)
	frames(tt)
	if tt.HasText("要重启接收") || tt.HasText("正在重启接收") || geometryTree(tt).Find("plugin-skills").Find("pending-dot") != nil {
		t.Errorf("重启完提示条和小圆点都该消失：%v", tt.Texts())
	}
}

// 4. 数字越界不保存、框变危险色；名单添加只能从已知会话里挑，× 能删。
func TestPluginsFields(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["link-reader"] = true
	frames(tt)
	clickKey(t, tt, "plugin-link-reader-maxLinks")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("25")
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 3 || !tt.HasText("要在 1～20 个之间") {
		t.Errorf("越界不该保存、要提示范围：值 %v，%v", v, tt.Texts())
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("12")
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 12 || tt.HasText("要在 1～20 个之间") {
		t.Errorf("改对了应该保存：值 %v", v)
	}
	// 名单（"在哪里用"里 QQ 群"只在这些"的会话）：添加只能挑这个场合里已知的，× 能删
	click(t, tt, "添加")
	frames(tt)
	if tt.HasText("添加 本地私聊 · 晴枝") || tt.HasText("添加 QQ 私聊 · 小雨") {
		t.Error("QQ 群的名单里不该能挑别的场合的会话")
	}
	click(t, tt, "添加 QQ 群 · 旧相机交流")
	frames(tt)
	click(t, tt, "删掉 QQ 群 · 周末读书会")
	frames(tt)
	key := "link-reader.scope.QQ 群.list"
	got := fmt.Sprint(s.Vals[key])
	if got != "[QQ 群 · 旧相机交流]" {
		t.Errorf("名单应该只剩加的那个：%s", got)
	}
	for _, v := range s.list(key) {
		ok := false
		for _, k := range knownSessions {
			ok = ok || k == v
		}
		if !ok {
			t.Errorf("名单里有不认识的会话 %q", v)
		}
	}
}

// 5. 0 / 1 / 100 个插件：空状态一行；100 个都画出来，量最长一帧。
func TestPluginsScale(t *testing.T) {
	for _, n := range []int{0, 1, 100} {
		a, tt := pluginTester(t)
		var list []plugin
		base := defaultPlugins()
		for i := 0; i < n; i++ {
			p := base[i%len(base)]
			p.ID, p.Name = fmt.Sprintf("%s-%d", p.ID, i), fmt.Sprintf("%s %d", p.Name, i)
			list = append(list, p)
		}
		a.Plugins = newPluginState(list)
		tt.Frame()
		var worst time.Duration
		for i := 0; i < 5; i++ {
			start := time.Now()
			tt.Frame()
			worst = max(worst, time.Since(start))
		}
		switch n {
		case 0:
			if !tt.HasText("还没有插件") {
				t.Errorf("0 个插件应该有空状态：%v", tt.Texts())
			}
		default:
			if got := strings.Count(strings.Join(tt.Texts(), "\n"), "插件："); got != n {
				t.Errorf("%d 个插件只画了 %d 张卡", n, got)
			}
		}
		t.Logf("%d 个插件：最长一帧 %v", n, worst)
	}
}

// 下拉（四个以上选项）选了能保存；分段选了能保存。
func TestPluginsChoice(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["subagent"] = true // 只开这一张：链接解析也展开时子任务会被挤到窗口外面，点不到
	frames(tt)
	if n := geometryTree(tt).Find("plugin-subagent-model"); n == nil || n.Label != "用的模型" {
		t.Fatalf("下拉的键不在触发按钮上：%+v", n)
	}
	clickKey(t, tt, "plugin-subagent-model")
	frames(tt)
	tt.Advance(motionFast)
	click(t, tt, "Sonnet 5.5")
	frames(tt)
	if v := s.Vals["subagent.model"]; v != "Sonnet 5.5" {
		t.Errorf("下拉选了没保存：%v", v)
	}
	s.Open["subagent"], s.Open["link-reader"] = false, true
	frames(tt)
	click(t, tt, "封面加文字")
	frames(tt)
	if v := s.Vals["link-reader.bilibiliFormat"]; v != "封面加文字" {
		t.Errorf("分段选了没保存：%v", v)
	}
}

// 没有开关的卡在开关的位置写为什么（主人 11-07）：核心插件"总是开着"，按会话开的写几个会话。
func TestPluginsNoSwitchReason(t *testing.T) {
	_, tt := pluginTester(t)
	for id, want := range map[string]string{"memory": "总是开着", "channel-responder": "总是开着", "repeater": "0 个会话", "poke": "3 个会话"} {
		n := geometryTree(tt).Find("plugin-" + id).Find("no-switch")
		if n == nil || n.Text != want {
			t.Errorf("%s 开关位置应该写 %q：%+v", id, want, n)
		}
	}
}

// 数字框：鼠标停满一会儿再滚轮能调（往上变大、按住 Shift 一档 10、夹在范围里）；刚滑过来就滚的照常滚页面、不改数字；按住单位左右拖也能调。
func TestPluginsNumberScrub(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["link-reader"] = true
	frames(tt)
	center := func(field string) (float32, float32) {
		n := geometryTree(tt).Find("plugin-link-reader-" + field + "-field").Find("input")
		return n.Box.X + n.Box.W/2, n.Box.Y + n.Box.H/2
	}
	x, y := center("maxLinks")
	tt.Move(x, y)
	frames(tt)
	tt.Wheel(x, y, 0, -40) // 刚停上去就滚：不改
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 3 {
		t.Fatalf("刚滑过来就滚不该改数字：%v", v)
	}
	tt.Advance(400 * time.Millisecond)
	x, y = center("maxLinks")
	tt.Wheel(x, y, 0, -40)
	tt.Wheel(x, y, 0, -40)
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 5 {
		t.Errorf("停满以后往上滚两档应该是 5：%v", v)
	}
	tt.HoldModifiers(ui.Shift)
	frames(tt)
	tt.Wheel(x, y, 0, -40)
	tt.HoldModifiers(0)
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 15 {
		t.Errorf("按住 Shift 一档应该加 10：%v", v)
	}
	for i := 0; i < 10; i++ {
		tt.Wheel(x, y, 0, -40)
	}
	frames(tt)
	if v := s.Vals["link-reader.maxLinks"]; v != 20 || *s.Draft["link-reader.maxLinks"] != "20" {
		t.Errorf("应该夹在上限 20，框里也写 20：%v", v)
	}
	// 拖单位：往左拖 5 格
	u := geometryTree(tt).Find("plugin-link-reader-videoTimeout-field").Find("unit")
	ux, uy := u.Box.X+u.Box.W/2, u.Box.Y+u.Box.H/2
	tt.Press(ux, uy)
	for i := 1; i <= 5; i++ {
		tt.Move(ux-float32(i)*scrubDrag, uy)
		tt.Frame()
	}
	tt.Release(ux-5*scrubDrag, uy)
	frames(tt)
	if v := s.Vals["link-reader.videoTimeout"]; v != 55 {
		t.Errorf("往左拖 5 格应该是 55：%v", v)
	}
}

// "在哪里用"排在"设置"上面（主人 11-07：先平台、再平台里的群和私聊、最后功能）；
// 关掉一个平台，它的场合都不显示；场合选"只在这些"才列名单；会话数跟着变；她自己用的工具范围不能改。
func TestPluginsScope(t *testing.T) {
	a, tt := pluginTester(t)
	s := a.pluginState()
	s.Open["link-reader"] = true
	frames(tt)
	card := func(id string) *geometryNode { return geometryTree(tt).Find("plugin-" + id) }
	sc, st := card("link-reader").Find("scope-title"), card("link-reader").Find("settings-title")
	if sc == nil || st == nil || sc.Box.Y >= st.Box.Y {
		t.Fatal("在哪里用应该在设置上面")
	}
	for _, occ := range []string{"本地私聊", "本地群聊", "QQ 私聊", "QQ 群"} {
		if !tt.HasText(occ) {
			t.Errorf("应该有 %s 这一行", occ)
		}
	}
	if !tt.HasText("在这些里开") || tt.HasText("这些里不开") {
		t.Error("只有选了只在这些的 QQ 群才列名单")
	}
	if n := card("link-reader").Find("settings-title").Find("effect"); n == nil || n.Text != "改了马上生效" {
		t.Error("生效方式应该写在设置那一行右边")
	}
	// 关掉 QQ 平台：两个 QQ 场合都不显示
	click(t, tt, "平台：QQ")
	frames(tt)
	if tt.HasText("QQ 私聊") || tt.HasText("QQ 群") {
		t.Errorf("关了 QQ 平台，QQ 的场合不该显示：%v", tt.Texts())
	}
	click(t, tt, "平台：QQ")
	frames(tt)
	// 复读：只在群里，开一个群，会话数变成 1
	s.Open["link-reader"], s.Open["repeater"] = false, true
	frames(tt)
	if tt.HasText("QQ 私聊") {
		t.Error("复读只在群里，不该有私聊的行")
	}
	s.Vals["repeater.scope.QQ 群.list"] = []string{"QQ 群 · 周末读书会"}
	frames(tt)
	if n := card("repeater").Find("no-switch"); n == nil || n.Text != "1 个会话" {
		t.Errorf("复读开了一个群，应该写 1 个会话：%+v", n)
	}
	// 她自己用的工具：范围只写一行，不能改
	s.Open["repeater"], s.Open["skills"] = false, true
	frames(tt)
	if f := card("skills").Find("scope-fixed"); f == nil || !strings.Contains(f.Text, "安全边界") {
		t.Error("技能的范围应该是一行不能改的说明")
	}
}
