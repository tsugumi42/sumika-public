package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// 标题栏上的右栏钮开合右栏（笔记 D5、G）。
func TestShellHeaderSideButton(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "资料与活动（Ctrl+\\）")
	if !hasKey(tt, "side") {
		t.Fatal("点右栏钮没开右栏")
	}
	click(t, tt, "资料与活动（Ctrl+\\）")
	if hasKey(tt, "side") {
		t.Fatal("再点没关右栏")
	}
}

// D：标题栏：头像、标题；状态只说后台真有的（在回复、在等批准、在取数据），没有就空着，不显示条数。
func TestShellHeaderStatus(t *testing.T) {
	a := shellApp()
	a.demoMode = false
	tt := shellTester(a, 2048, 1030)
	if !tt.HasText("白羽") {
		t.Fatal("标题栏没有会话名")
	}
	if _, ok := tt.Find("头像：header-avatar"); !ok {
		t.Fatal("标题栏没有头像")
	}
	status := func() string { r, _ := tt.Find("chat-status"); _ = r; return "" }
	_ = status
	for _, tc := range []struct {
		name  string
		setup func()
		want  string
	}{
		{"空闲", func() {}, ""},
		{"在回复", func() { a.localRuns = map[string]*localRun{a.sel: {busy: true}} }, "正在回复"},
		{"等批准", func() { a.localRuns = nil; a.approvals = []api.LocalApproval{{ID: "x", Key: a.keyOf(a.sel)}} }, "在等你批准"},
		{"取数据", func() { a.approvals = nil; a.loading = true }, "取数据中…"},
	} {
		tc.setup()
		tt.Frame()
		tt.Frame()
		if got := a.wbSubtitle(); got != tc.want {
			t.Errorf("%s：副标题 %q，应为 %q", tc.name, got, tc.want)
		}
		if tc.want != "" && !tt.HasText(tc.want) {
			t.Errorf("%s：标题栏没显示 %q：%v", tc.name, tc.want, tt.Texts())
		}
	}
	a.localRuns, a.approvals, a.loading, a.demoMode = nil, nil, false, true
	tt.Frame()
	tt.Frame()
	if !tt.HasText("演示数据（全是编的）") {
		t.Errorf("演示数据模式没在标题栏写明：%v", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if s == "1 条" || s == "0 条" {
			t.Fatalf("标题栏不该显示消息条数：%q", s)
		}
	}
}

// 玻璃：标题栏浮在消息列上面，消息列顶上让出标题栏；滚动时消息从标题栏下面透过去（列表顶 = 聊天区顶，不在标题栏下面开始）。
func TestShellMessagesScrollUnderHeader(t *testing.T) {
	a := shellApp()
	for i := range 80 {
		a.msgs = append(a.msgs, &msg{id: "x" + string(rune('A'+i%26)) + string(rune('a'+i/26)), who: "白羽", text: "一句话", at: 60000 * int64(i+2)})
	}
	tt := shellTester(a, 2048, 1030)
	list := boxOf(t, tt, "workbench-messages")
	head := boxOf(t, tt, "chat-header")
	if !closeTo(list.Box.Y, 0) || head.Box.H != 48 {
		t.Fatalf("消息列 y=%v：应从聊天窗顶开始（标题栏浮在上面），标题栏高 %v", list.Box.Y, head.Box.H)
	}
}

// SUMIKA_SHELL_SHOT=<目录> 时，三档面板材质（液态玻璃、磨砂、实色）× 浅色、深色各截一张：
// 消息列滚到中间，消息从标题栏和输入框底下透过去。只截聊天区，2048×1030、像素比 1.25。
func TestShellMaterialShots(t *testing.T) {
	dir := os.Getenv("SUMIKA_SHELL_SHOT")
	if dir == "" {
		t.Skip("SUMIKA_SHELL_SHOT 没设")
	}
	if got := loadBundledFont(".", bundledFamily, ui.RegisterFont); got.Err != nil || !got.Loaded {
		t.Fatalf("文渊圆体没注册上（要把 native/fonts/WenYuanRoundedSCVF.ttf 拷进 worktree）：%+v", got)
	}
	for _, material := range []string{"liquid", "blur", "solid"} {
		for _, dark := range []bool{false, true} {
			a := shellApp()
			a.workbench.shell.material = material
			a.workbench.shell.st.Panes.SideOpen = true
			for i := range 60 {
				text := "这是第几条编造的消息，长一点好让它在标题栏和输入框底下滚过去"
				a.msgs = append(a.msgs, &msg{id: fmt.Sprintf("s%d", i), who: "白羽", mine: i%3 == 0, text: text, blocks: segview.TextBlocks(text), at: 60000 * int64(i+2)})
			}
			a.list = a.newList()
			if dark {
				a.initLook()
				a.look.settings.Mode = "night"
			}
			tt := shellTester(a, 2048, 1030)
			a.list.ScrollTo(30, ui.Start)
			tt.Frame()
			tt.Frame()
			chat := boxOf(t, tt, "chat-local:r1")
			img := tt.Image()
			r := image.Rect(int(chat.Box.X*1.25), int(chat.Box.Y*1.25), int((chat.Box.X+chat.Box.W)*1.25), int((chat.Box.Y+chat.Box.H)*1.25)).Intersect(img.Bounds())
			mode := map[bool]string{false: "浅色", true: "深色"}[dark]
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("材质-%s-%s.png", material, mode)))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, img.SubImage(r))
			f.Close()
		}
	}
}
