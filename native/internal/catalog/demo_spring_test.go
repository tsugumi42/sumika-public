package catalog

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 弹簧反馈条目：点“保存设置”弹出“设置已保存”，舞台里别的元素一帧都不动；约 2 秒后自己收起，不再要帧。
func TestSpringEntryToast(t *testing.T) {
	font := look()
	e, ok := Find("spring")
	if !ok {
		t.Fatal("没有登记 spring 条目")
	}
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	names := []string{"保存修改", "稍后再说", "开机自启动", "复制这段话", "保存设置", "允许运行"}
	pos := func() []ui.Rect {
		out := make([]ui.Rect, len(names))
		for i, n := range names {
			a, ok := tt.Access(n)
			if !ok {
				t.Fatalf("找不到 %q", n)
			}
			out[i] = a.Bounds
		}
		return out
	}
	before := pos()
	a, _ := tt.Access("保存设置")
	tt.Press(a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2)
	tt.Release(a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2)
	shown := false
	for i := 0; i < 400; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		if _, ok := tt.AccessByRole(ui.RoleStatus, "设置已保存"); ok {
			shown = true
		}
		// 按钮自己会因为悬停放大 2.5%，别的不该动：只比没被指着的几个
		got := pos()
		for _, j := range []int{0, 1, 2, 3, 5} {
			if got[j] != before[j] {
				t.Fatalf("第 %d 帧：%q 被挤动了：%v → %v", i, names[j], before[j], got[j])
			}
		}
	}
	if !shown {
		t.Error("点了按钮，提示条没出现")
	}
	if tt.FramePending() {
		t.Error("提示条收起后不该再要帧")
	}
}
