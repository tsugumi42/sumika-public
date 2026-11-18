package winshell

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 选中块滑动（SlideIndicator）：五处各一条，量“点一下以后逐帧选中块在哪”。
// 共同的规矩：从旧位置出发、沿着方向走到新位置、中途被拉长（前缘先走）、终点和新项一样、减少动态效果时一帧到位。

type slideCase struct {
	name     string
	vertical bool
	view     func(k *Kit, c *ui.Context)
	state    func(k *Kit) *SlideState
	click    string // 点哪个字（换选中项）
	old      string // 原来选中的那一项的 id（SlideState.Item 的 id）
	back     string // 原来选中的那一项上的字（先点过去再点回来，让它留下按下的底色）
	w, h     int
}

// run 点一下 click，返回每帧（16 毫秒一帧）选中块的矩形、点之前和终点的矩形。
func (sc slideCase) run(t *testing.T, reduce bool, scale float64) (start ui.Rect, frames []ui.Rect, end ui.Rect) {
	t.Helper()
	old := SetMotionScale(scale)
	defer SetMotionScale(old)
	k := testKit()
	k.SetMaterial(MaterialSolid)
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Fill().Padding(10).Children(func() { sc.view(k, c) })
	}, sc.w, sc.h, reduce)
	for range 4 {
		step(tt, 50*time.Millisecond)
	}
	start = sc.state(k).Rect
	if err := tt.Click(sc.click); err != nil {
		t.Fatal(err)
	}
	frames = append(frames, sc.state(k).Rect)
	for range int(float64(time.Second)*scale/float64(16*time.Millisecond)) + 30 {
		step(tt, 16*time.Millisecond)
		frames = append(frames, sc.state(k).Rect)
	}
	end = frames[len(frames)-1]
	return
}

func slideCases() []slideCase {
	seg := 0
	cat := "a"
	chip := 0
	page := "a"
	return []slideCase{
		{name: "分段", view: func(k *Kit, c *ui.Context) {
			k.Segment(c, "seg", &seg, []string{"甲甲甲", "乙乙乙", "丙丙丙"})
		},
			state: func(k *Kit) *SlideState { return &k.SegHandles["seg"].Slide }, click: "丙丙丙", old: "0", back: "甲甲甲", w: 400, h: 200},
		{name: "分类行", vertical: true, view: func(k *Kit, c *ui.Context) {
			k.CategoryList(c, "t", []Category{{ID: "a", Label: "甲类"}, {ID: "b", Label: "乙类"}, {ID: "c", Label: "丙类"}, {ID: "d", Label: "丁类"}}, cat, func(id string) { cat = id })
		}, state: func(k *Kit) *SlideState { return k.slides("category/t") }, click: "丁类", old: "a", back: "甲类", w: 300, h: 400},
		{name: "筛选片", view: func(k *Kit, c *ui.Context) {
			k.Chips(c, "ch", []string{"全部", "晴枝", "雾栞", "砚", "小满"}, &chip)
		},
			state: func(k *Kit) *SlideState { return k.slides("chips/ch") }, click: "小满", old: "0", back: "全部", w: 500, h: 200},
		{name: "导航", vertical: true, view: func(k *Kit, c *ui.Context) {
			k.NavRail(c, "rail", []NavItem{{ID: "a", Label: "甲", Icon: IconHome}, {ID: "b", Label: "乙", Icon: IconChat}, {ID: "c", Label: "丙", Icon: IconSettings}}, page, func(id string) { page = id })
		}, state: func(k *Kit) *SlideState { return k.slides("nav/rail") }, click: "丙", old: "a", back: "甲", w: 300, h: 400},
	}
}

func TestSlideIndicatorAllFamilies(t *testing.T) {
	for i, sc := range slideCases() {
		t.Run(sc.name, func(t *testing.T) {
			start, frames, end := sc.run(t, false, 1)
			pos := func(r ui.Rect) float32 {
				if sc.vertical {
					return r.Y
				}
				return r.X
			}
			size := func(r ui.Rect) float32 {
				if sc.vertical {
					return r.H
				}
				return r.W
			}
			if start.W == 0 || end.W == 0 {
				t.Fatalf("没量到选中块：起点 %+v 终点 %+v", start, end)
			}
			travel := pos(end) - pos(start)
			if travel <= 1 {
				t.Fatalf("选中块没有移动：%v → %v", pos(start), pos(end))
			}
			first := frames[0]
			if abs32(pos(first)-pos(start)) > 1 {
				t.Errorf("第一帧应还在旧位置：%v，旧位置 %v", pos(first), pos(start))
			}
			// 位置：沿着方向走，只允许回弹的一点过冲（不超过行程的 12%）；宽高：被拉长过，到位恢复原宽
			maxSize, maxOver := float32(0), float32(0)
			prev := pos(first)
			for i, f := range frames {
				if pos(f) < prev-travel*.12 && i < len(frames)/2 {
					t.Fatalf("第 %d 帧往回走了：%v → %v", i, prev, pos(f))
				}
				prev = pos(f)
				maxSize = max(maxSize, size(f))
				maxOver = max(maxOver, pos(f)-pos(end))
			}
			if maxOver > travel*.12 {
				t.Errorf("过冲 %.1f 超过行程 %.1f 的 12%%", maxOver, travel)
			}
			if maxSize < size(end)+1 {
				t.Errorf("中途最宽（高）%.1f，应比起点终点 %.1f 都大（前缘先走，块被拉长）", maxSize, size(end))
			}
			if abs32(size(start)-size(end)) > 1.5 && sc.name != "筛选片" {
				t.Errorf("起点终点大小不同：%.1f %.1f", size(start), size(end))
			}
			// 终点：和不动画（减少动态效果）的终点一致
			_, redF, redEnd := slideCases()[i].run(t, true, 1)
			if abs32(end.X-redEnd.X) > .5 || abs32(end.Y-redEnd.Y) > .5 || abs32(end.W-redEnd.W) > .5 || abs32(end.H-redEnd.H) > .5 {
				t.Errorf("动画终点 %+v 和不动画的终点 %+v 不一样", end, redEnd)
			}
			if redF[0] != redEnd {
				t.Errorf("减少动态效果时点完第一帧就该到位：%+v，终点 %+v", redF[0], redEnd)
			}
		})
	}
}

// 慢放：同一个动画走到行程 90% 的时刻按倍数变长。
func TestSlideIndicatorSlowMotion(t *testing.T) {
	reach := func(scale float64) time.Duration {
		sc := slideCases()[0] // 每次一份新的选中状态
		start, frames, end := sc.run(t, false, scale)
		for i, f := range frames {
			if f.X-start.X >= .9*(end.X-start.X) {
				return time.Duration(i) * 16 * time.Millisecond
			}
		}
		t.Fatalf("倍数 %v 下一直没走到 90%%", scale)
		return 0
	}
	base1, slow4, slow10 := reach(1), reach(4), reach(10)
	t.Logf("走到 90%%：正常 %v，1/4 %v，1/10 %v", base1, slow4, slow10)
	for _, c := range []struct {
		name string
		got  time.Duration
		x    float64
	}{{"1/4", slow4, 4}, {"1/10", slow10, 10}} {
		r := float64(c.got) / float64(base1)
		if r < c.x*.8 || r > c.x*1.25 {
			t.Errorf("%s 慢放下时长是正常的 %.2f 倍，应接近 %v 倍", c.name, r, c.x)
		}
	}
	if MotionScale() != 1 {
		t.Errorf("测试结束没还原倍数：%v", MotionScale())
	}
}

// 慢放也乘在包级的动效量上，还原后逐字节回到原值。
func TestMotionScaleRestoresPackageVars(t *testing.T) {
	was := ControlFade
	d0, e0 := DropdownGrow.Duration, SelectFade.Duration
	SetMotionScale(10)
	if DropdownGrow.Duration != d0*10 || SelectFade.Duration != e0*10 || ControlFade.Duration != was.Duration*10 {
		t.Errorf("慢放 10 倍后 DropdownGrow %v SelectFade %v ControlFade %v", DropdownGrow.Duration, SelectFade.Duration, ControlFade.Duration)
	}
	SetMotionScale(1)
	if DropdownGrow.Duration != d0 || SelectFade.Duration != e0 || ControlFade.Duration != was.Duration {
		t.Errorf("还原后 DropdownGrow %v SelectFade %v ControlFade %v", DropdownGrow.Duration, SelectFade.Duration, ControlFade.Duration)
	}
	if SetMotionScale(-3) != 1 || MotionScale() != 1 {
		t.Error("非法倍数应当当 1")
	}
}

// 会话行：点另一行，选中块（列表画的那块）滑过去；滑完交还给行自己画；滚动时不会被当成换选项。
func TestSlideIndicatorSidebarRows(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	s := &State{}
	sel := "c0"
	seg := 0
	sp := &SidebarSpec{Segments: []string{"私聊", "群聊"}, Segment: &seg, Count: 30, Search: new(string),
		At: func(i int) Conversation {
			return Conversation{ID: fmt.Sprint("c", i), Name: fmt.Sprint("会话", i), Preview: "你好"}
		},
		OnSelect: func(id string) { sel = id }}
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		sp.Selected = sel
		ui.Column(c.Key("root")).Width(300).Height(600).Children(func() { k.Sidebar(c, s, sp, false) })
	}, 320, 600, false)
	for range 4 {
		step(tt, 50*time.Millisecond)
	}
	st := k.slides("conversations")
	if err := tt.Click("会话3"); err != nil {
		t.Fatal(err)
	}
	var ys []float32
	moving := 0
	for range 60 {
		step(tt, 16*time.Millisecond)
		ys = append(ys, st.Rect.Y)
		if st.Moving {
			moving++
		}
	}
	if moving < 3 {
		t.Fatalf("会话行的选中块没有滑动：Moving 只有 %d 帧", moving)
	}
	if ys[len(ys)-1] <= ys[0]+10 {
		t.Errorf("选中块没有向下走到第 4 行：%v → %v", ys[0], ys[len(ys)-1])
	}
	if st.Moving {
		t.Error("一秒后还在滑")
	}
	// 滚动不重播
	before := st.Rect
	for range 6 {
		tt.Wheel(150, 300, 0, -1)
		step(tt, 16*time.Millisecond)
		if st.Moving {
			t.Fatal("只滚动，选中块却在滑")
		}
	}
	_ = before
}

// 选中只有一个东西：分段换选项的动画中途，目标格自己不画底（逐帧在块还没盖到它的时候量它的底色，应和轨道一样），
// 终点盖住它的是 Self 色的那一块。换选项用程序改值（不点，免得指针悬停的底色混进来）。
func TestSegmentSingleSelectionLayer(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	sel := 0
	var track, cell ui.Rect
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Fill().Padding(10).Children(func() {
			k.Segment(c, "seg", &sel, []string{"甲甲甲甲", "乙乙乙乙", "丙丙丙丙", "丁丁丁丁"})
		})
		if hs := k.SegHandles["seg"]; hs != nil {
			track, cell = hs.Track.Bounds(c), hs.Cells[3].Bounds(c)
		}
	}, 500, 120, false)
	for range 4 {
		step(tt, 50*time.Millisecond)
	}
	hs := k.SegHandles["seg"]
	px := func(x, y float32) [3]uint8 {
		p := tt.Image().RGBAAt(int(x), int(y))
		return [3]uint8{p.R, p.G, p.B}
	}
	probeX, probeY := cell.X+4, cell.Y+cell.H/2 // 目标格靠左，没有字
	trackColor := px(probeX, probeY)
	sel = 3
	tt.Frame()
	seen := 0
	for i := 0; i < 60; i++ {
		step(tt, 16*time.Millisecond)
		// 块的右边缘（窗口坐标）还没到探测点：目标格自己不该有任何底
		if hs.Slide.Moving && track.X+hs.Slide.Rect.X+hs.Slide.Rect.W < probeX-3 {
			if got := px(probeX, probeY); got != trackColor {
				t.Fatalf("第 %d 帧块还没盖到目标格，目标格自己却有底色 %v（轨道色 %v）", i, got, trackColor)
			}
			seen++
		}
	}
	if seen < 3 {
		t.Fatalf("只有 %d 帧满足“块没盖到、在滑动”，测不出东西", seen)
	}
	settle(tt)
	got, want := px(probeX, probeY), k.selectFill(selMode) // 分段是模式切换：选中是浮起的一块（B1）
	if abs(int(got[0])-int(want.R)) > 3 || abs(int(got[1])-int(want.G)) > 3 || abs(int(got[2])-int(want.B)) > 3 {
		t.Errorf("终点目标格的颜色 %v，应是浮起块的底 %v", got, want)
	}
}

// 会话列表滑完交还给行：交接前后两帧，选中位置的颜色一样（同色、同圆角、不淡变）。
func TestSidebarHandoffKeepsColor(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	s := &State{}
	sel := "c0"
	seg := 0
	sp := &SidebarSpec{Segments: []string{"私聊", "群聊"}, Segment: &seg, Count: 8, Search: new(string),
		At: func(i int) Conversation {
			return Conversation{ID: fmt.Sprint("c", i), Name: fmt.Sprint("会话", i), Preview: "你好"}
		},
		OnSelect: func(id string) { sel = id }}
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		sp.Selected = sel
		ui.Column(c.Key("root")).Width(300).Height(600).Children(func() { k.Sidebar(c, s, sp, false) })
	}, 320, 600, false)
	for range 4 {
		step(tt, 50*time.Millisecond)
	}
	st := k.slides("conversations")
	r, ok := tt.Find("会话2")
	if !ok {
		t.Fatal("找不到会话2")
	}
	sel = "c2"
	tt.Frame()
	px := func() [3]uint8 {
		p := tt.Image().RGBAAt(int(r.X)-40, int(r.Y+r.H/2)) // 行里头像左边的内距，在选中块里
		return [3]uint8{p.R, p.G, p.B}
	}
	var prev [3]uint8
	wasMoving, handoffs := false, 0
	for i := 0; i < 90; i++ {
		step(tt, 16*time.Millisecond)
		cur := px()
		if wasMoving && !st.Moving { // 这一帧起行自己画底
			handoffs++
			for j := range cur {
				if abs(int(cur[j])-int(prev[j])) > 2 {
					t.Fatalf("交接前后颜色变了：%v → %v", prev, cur)
				}
			}
		}
		wasMoving, prev = st.Moving, cur
	}
	if handoffs != 1 {
		t.Fatalf("交接了 %d 次，应为 1 次", handoffs)
	}
}

// 旧选项不准播独立的动画：换选项以后，旧选项那里一旦不被选中块盖着，它的颜色就已经是静止的终值（没选中、没悬停），
// 之后一帧也不再变——没有从悬停、按下色淡出来的中间值，也没有底色淡变。分段、筛选片、导航、设置分类各断言一次。
func TestOldItemHasNoOwnFade(t *testing.T) {
	for i, sc := range slideCases() {
		t.Run(sc.name, func(t *testing.T) {
			sc = slideCases()[i]
			k := testKit()
			k.SetMaterial(MaterialSolid)
			var hostB, oldB ui.Rect
			tt := motionTester(func(c *ui.Context) {
				c.SetTheme(&k.Theme)
				ui.Column(c.Key("root")).Fill().Padding(10).Children(func() { sc.view(k, c) })
				st := sc.state(k)
				hostB, oldB = st.Host().Bounds(c), st.Item(sc.old).Bounds(c)
			}, sc.w, sc.h, false)
			for range 4 {
				step(tt, 50*time.Millisecond)
			}
			st := sc.state(k)
			// 先点过去再点回来：原来那项被点下的那一刻，它的悬停、按下通道里留着按下色
			for _, label := range []string{sc.click, sc.back} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				for range 90 {
					step(tt, 16*time.Millisecond)
				}
			}
			px, py := oldB.X+4, oldB.Y+oldB.H/2
			if err := tt.Click(sc.click); err != nil {
				t.Fatal(err)
			}
			var cols [][3]uint8
			var exposed []bool
			for range 90 {
				step(tt, 16*time.Millisecond)
				p := tt.Image().RGBAAt(int(px), int(py))
				cols = append(cols, [3]uint8{p.R, p.G, p.B})
				b := ui.Rect{X: hostB.X + st.Rect.X, Y: hostB.Y + st.Rect.Y, W: st.Rect.W, H: st.Rect.H}
				exposed = append(exposed, px < b.X-2 || px > b.X+b.W+2 || py < b.Y-2 || py > b.Y+b.H+2)
			}
			final := cols[len(cols)-1]
			firstExposed := -1
			for j, e := range exposed {
				if e {
					firstExposed = j
					break
				}
			}
			if firstExposed < 0 {
				t.Fatal("选中块一直盖着旧选项，测不出东西")
			}
			for j := firstExposed; j < len(cols); j++ {
				if !exposed[j] {
					continue // 回弹时块又盖了它一下
				}
				for ch := range 3 {
					if abs(int(cols[j][ch])-int(final[ch])) > 1 {
						t.Fatalf("第 %d 帧旧选项露出来时颜色 %v 不是静止终值 %v（有独立的淡变）", j, cols[j], final)
					}
				}
			}
		})
	}
}

// 会话列表同一条规矩。
func TestOldSidebarRowHasNoOwnFade(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	s := &State{}
	sel := "c0"
	seg := 0
	sp := &SidebarSpec{Segments: []string{"私聊", "群聊"}, Segment: &seg, Count: 8, Search: new(string),
		At: func(i int) Conversation {
			return Conversation{ID: fmt.Sprint("c", i), Name: fmt.Sprint("会话", i), Preview: "你好"}
		},
		OnSelect: func(id string) { sel = id }}
	var hostB, oldB ui.Rect
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		sp.Selected = sel
		ui.Column(c.Key("root")).Width(300).Height(600).Children(func() { k.Sidebar(c, s, sp, false) })
		st := k.slides("conversations")
		hostB, oldB = st.Host().Bounds(c), st.Item("c0").Bounds(c)
	}, 320, 600, false)
	for range 4 {
		step(tt, 50*time.Millisecond)
	}
	st := k.slides("conversations")
	px, py := oldB.X+3, oldB.Y+oldB.H/2
	if err := tt.Click("会话4"); err != nil {
		t.Fatal(err)
	}
	var cols [][3]uint8
	var exposed []bool
	for range 90 {
		step(tt, 16*time.Millisecond)
		p := tt.Image().RGBAAt(int(px), int(py))
		cols = append(cols, [3]uint8{p.R, p.G, p.B})
		b := ui.Rect{X: hostB.X + st.Rect.X, Y: hostB.Y + st.Rect.Y, W: st.Rect.W, H: st.Rect.H}
		exposed = append(exposed, py < b.Y-2 || py > b.Y+b.H+2)
	}
	final := cols[len(cols)-1]
	n := 0
	for j := range cols {
		if !exposed[j] {
			continue
		}
		n++
		for ch := range 3 {
			if abs(int(cols[j][ch])-int(final[ch])) > 1 {
				t.Fatalf("第 %d 帧旧会话行露出来时颜色 %v 不是静止终值 %v", j, cols[j], final)
			}
		}
	}
	if n == 0 {
		t.Fatal("块一直盖着旧行，测不出东西")
	}
}
