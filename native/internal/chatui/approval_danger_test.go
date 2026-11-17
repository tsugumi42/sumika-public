package chatui

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 审批卡“更多选择”里的“拒绝并说明原因…”是危险项：放在最下面、前面一条分隔线、红字。
// 菜单要画在窗口里（系统菜单不认 Danger）；选了它还是请宿主弹出写原因的框。
func TestApprovalDenyReasonIsDanger(t *testing.T) {
	var denied []string
	h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0], DenyReason: true,
		OnDeny: func(_, reason string) { denied = append(denied, reason) }}
	tt := approvalTester(&h, scopeStep(AllowConversation, AllowAlways))
	if err := tt.Click("更多选择"); err != nil {
		t.Fatal(err)
	}
	for range 8 { // 菜单淡入放完再量颜色
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	menu := tt.Menu()
	if len(menu) != 4 || menu[2] != "-" || menu[3] != "拒绝并说明原因…" { // “-” 是分隔线
		t.Fatalf("拒绝并说明原因该在最下面：%v", menu)
	}
	deny, ok := tt.Find("拒绝并说明原因…")
	if !ok {
		t.Fatalf("菜单没出来：%q", tt.Texts())
	}
	other, _ := tt.Find("记住，以后都允许「派 reader 子任务」")
	if ink := darkestIn(tt.Image(), deny); ink.R <= ink.G+30 {
		t.Fatalf("危险项该是红字：%+v", ink)
	}
	if ink := darkestIn(tt.Image(), other); ink.R > ink.G+30 {
		t.Fatalf("别的项不该是红字：%+v", ink)
	}
	if err := tt.Click("拒绝并说明原因…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Equal(denied, []string{"?"}) {
		t.Fatalf("选了它该请宿主弹出写原因的框：%v", denied)
	}
}

// darkestIn 是矩形里最暗的像素，当作字色。
func darkestIn(img *image.RGBA, r ui.Rect) (best struct{ R, G, B uint8 }) {
	lum := 1 << 30
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			px := img.RGBAAt(x, y)
			if l := int(px.R) + int(px.G) + int(px.B); l < lum {
				lum, best.R, best.G, best.B = l, px.R, px.G, px.B
			}
		}
	}
	return best
}
