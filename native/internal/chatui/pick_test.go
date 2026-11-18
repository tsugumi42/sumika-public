package chatui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func TestPickerLimitToggleRange(t *testing.T) {
	var p Picker
	order := make([]string, 30)
	for i := range order {
		order[i] = fmt.Sprintf("m%02d", i)
	}
	p.Start("g", order[0])
	for _, id := range order[1:MaxPick] {
		p.Click(id, false, order)
	}
	if len(p.IDs) != MaxPick || p.Notice != "" {
		t.Fatalf("选满 %d：%d %q", MaxPick, len(p.IDs), p.Notice)
	}
	p.Click(order[MaxPick], false, order)
	if len(p.IDs) != MaxPick || p.Notice != fmt.Sprintf("最多选 %d 条", MaxPick) || p.Has(order[MaxPick]) {
		t.Fatalf("第 %d 条应点不上并提示：%d %q", MaxPick+1, len(p.IDs), p.Notice)
	}
	p.Click(order[3], false, order)
	if p.Has(order[3]) || len(p.IDs) != MaxPick-1 {
		t.Fatal("点已选的应取消")
	}
	var q Picker
	q.Start("g", order[5])
	q.Click(order[9], true, order)
	if !slices.Equal(q.InOrder(order), order[5:10]) {
		t.Fatalf("Shift 范围：%v", q.InOrder(order))
	}
	q.Click(order[2], true, order)
	if !slices.Equal(q.InOrder(order), order[2:10]) {
		t.Fatalf("反向 Shift 范围：%v", q.InOrder(order))
	}
	var r Picker
	r.Start("g", order[0])
	r.Click(order[29], true, order)
	if len(r.IDs) != MaxPick || r.Notice == "" {
		t.Fatalf("范围超过上限：%d %q", len(r.IDs), r.Notice)
	}
	if !r.Active("g") || r.Active("h") {
		t.Fatal("多选只在同一个会话")
	}
	r.Stop()
	if r.Active("g") {
		t.Fatal("退出后不在多选")
	}
}

// 多选时点消息任意处切换这一条；进出多选行高不变（只多左边距，不跳）。
func TestPickOverlayToggles(t *testing.T) {
	var pk Picker
	pk.Start("g", "m0")
	host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Group: true, Pick: &pk, PickOrder: func() []string { return []string{"m0", "m1"} }}
	msgs := []*Msg{
		{ID: "m0", Sender: "a", Name: "小A", Text: "第一条", Blocks: segview.TextBlocks("第一条"), At: time.UnixMilli(1000)},
		{ID: "m1", Sender: "b", Name: "小B", Text: "第二条", Blocks: segview.TextBlocks("第二条"), At: time.UnixMilli(900000)},
	}
	var hs [2]ui.Handle
	picking := false
	var heights [2]float32
	tt := ui.NewTester(func(c *ui.Context) {
		h := host
		if !picking {
			h.Pick = nil
		}
		ui.Column(c).Size(720, 480).Padding(24).Children(func() {
			for i, m := range msgs {
				var prev *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				ui.Column(c.Key(i)).FillWidth().Bind(&hs[i]).Children(func() { h.Message(c, m, prev, nil) })
			}
		})
		heights[0], heights[1] = hs[0].Bounds(c).H, hs[1].Bounds(c).H
	}, 720, 480)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	before := heights
	picking = true
	tt.Frame()
	tt.Frame()
	if heights != before {
		t.Fatalf("进多选行高变了：%v → %v", before, heights)
	}
	// 勾选圈在行的最右边（让出的那一格里），不在中间、不在左边的头像底下
	if c, ok := tt.Find("勾选圈"); !ok || c.X < 720-24-host.T.pickGutter() {
		t.Fatalf("勾选圈应在最右边那一格：%+v %v", c, ok)
	}
	r, ok := tt.Find("第二条")
	if !ok {
		t.Fatal(tt.Texts())
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !slices.Equal(pk.IDs, []string{"m0", "m1"}) {
		t.Fatalf("点正文应选上这一条：%v", pk.IDs)
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !slices.Equal(pk.IDs, []string{"m0"}) {
		t.Fatalf("再点取消：%v", pk.IDs)
	}
}

// 操作条：已选 N 条、复制、引用、取消；多位时弹出对象列表；一位时引用直接给她。
func TestPickBar(t *testing.T) {
	r := newComposerRig(t)
	var pk Picker
	pk.Start("g", "m0")
	pk.Click("m1", false, nil)
	var copied, cancelled int
	var quoted [][]string
	var all bool
	targets := threeTargets
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(640, 300).Justify(ui.End).Padding(24).Children(func() {
			r.host.PickBar(c, &pk, targets, 1, func() { copied++ }, func(ids []string, a bool) { quoted = append(quoted, ids); all = a }, func() { cancelled++ })
		})
	}, 640, 300)
	tt.Frame()
	for _, want := range []string{"已选 2 条", "复制", "引用", "取消", "其中 1 条没有文字或已撤回，引用时跳过"} {
		if !tt.HasText(want) {
			t.Fatalf("操作条缺 %q：%v", want, tt.Texts())
		}
	}
	_ = tt.Click("复制")
	_ = tt.Click("取消")
	if err := tt.Click("引用"); err != nil {
		t.Fatal(err)
	}
	settle(t, tt, nil) // 菜单从按钮那条边长开（MenuMorph），没露出来的部分不收点击：等它长完
	if err := tt.Click("所有人"); err != nil {
		t.Fatalf("%v %v", err, tt.Texts())
	}
	tt.Frame()
	if copied != 1 || cancelled != 1 || len(quoted) != 1 || !all {
		t.Fatalf("复制 %d 取消 %d 引用 %v 所有人 %v", copied, cancelled, quoted, all)
	}
	targets = threeTargets[:1]
	tt.Frame()
	if err := tt.Click("引用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(quoted) != 2 || !slices.Equal(quoted[1], []string{"a"}) || tt.HasText("所有人") {
		t.Fatalf("单角色直接给她：%v", quoted)
	}
}
