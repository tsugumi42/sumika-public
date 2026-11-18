package winshell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type tabRig struct {
	k      *Kit
	tt     *ui.Tester
	sel    int
	tabs   []ui.Handle
	side   ui.Handle // 页签那一排右边的按钮
	below  ui.Handle // 页签下面的一行字
	widths [][]float32
	outer  [][2]ui.Rect
}

func newTabRig(t *testing.T, reduce bool) *tabRig {
	r := &tabRig{k: testKit(), tabs: make([]ui.Handle, 4)}
	r.k.SetMaterial(MaterialSolid)
	r.k.Surface = func(c *ui.Context, e ui.Element, info SurfaceInfo) {
		if info.Kind == "icon-tab" {
			var i int
			fmt.Sscanf(info.ID[strings.LastIndex(info.ID, "-")+1:], "%d", &i)
			e.Bind(&r.tabs[i])
		}
	}
	tabs := []IconTab{{"聊天", IconChat}, {"记忆与学习", IconMemory}, {"运行", IconRuntime}, {"设置选项", IconSettings}}
	r.tt = motionTester(func(c *ui.Context) {
		c.SetTheme(&r.k.Theme)
		ui.Column(c.Key("root")).Fill().Padding(10).Gap(8).Children(func() {
			ui.Row(c.Key("line")).Gap(8).AlignItems(ui.Center).Children(func() {
				r.k.IconTabs(c, "tabs", tabs, &r.sel)
				ui.Box(c.Key("side")).Size(40, 30).Bind(&r.side)
			})
			ui.Text(c.Key("below"), "下面的字").Bind(&r.below)
		})
		w := make([]float32, len(r.tabs))
		for i := range r.tabs {
			w[i] = r.tabs[i].Bounds(c).W
		}
		r.widths = append(r.widths, w)
		r.outer = append(r.outer, [2]ui.Rect{r.side.Bounds(c), r.below.Bounds(c)})
	}, 600, 200, reduce)
	for range 4 {
		step(r.tt, 50*time.Millisecond)
	}
	return r
}

// 图标页签：点另一个，新的展开、旧的收回，两边宽度之和一帧一帧连着变（不突变）；页签这一排以外的东西一帧也不动；
// 终点：选中的是胶囊、其余是圆钮（宽 = 小控件高）。
func TestIconTabsExpandAndCollapse(t *testing.T) {
	r := newTabRig(t, false)
	h := r.k.G.HitSmall
	w0 := r.widths[len(r.widths)-1]
	if w0[0] <= h+10 || w0[1] != h || w0[2] != h || w0[3] != h {
		t.Fatalf("起点：选中的该是胶囊，其余是圆钮（%v）：%v", h, w0)
	}
	from := len(r.widths)
	if err := r.tt.Click("记忆与学习"); err != nil {
		t.Fatal(err)
	}
	for range 40 {
		step(r.tt, 16*time.Millisecond)
	}
	frames := r.widths[from-1:]
	end := frames[len(frames)-1]
	if end[0] != h || end[1] <= h+10 || end[2] != h {
		t.Fatalf("终点：第二个该是胶囊，其余圆钮：%v", end)
	}
	sum := func(w []float32) (s float32) {
		for _, x := range w {
			s += x
		}
		return
	}
	total := abs32(sum(end) - sum(w0))
	grew, shrank := false, false
	for i := 1; i < len(frames); i++ {
		a, b := frames[i-1], frames[i]
		if d := abs32(sum(b) - sum(a)); d > h*1.2 {
			t.Fatalf("第 %d 帧两边宽度之和突变 %.1f（%v → %v）", i, d, sum(a), sum(b))
		}
		if b[1] > a[1]+.01 && b[0] < a[0]-.01 {
			grew, shrank = true, true // 同一帧里一个在长一个在缩：两者同时进行
		}
		for j := range b {
			if b[j] > r.k.G.HitSmall+400 || b[j] < h-.01 {
				t.Fatalf("第 %d 帧页签 %d 宽 %.1f 超出范围", i, j, b[j])
			}
		}
	}
	if !grew || !shrank {
		t.Error("没有出现“一个在展开、一个在收回”同时进行的帧")
	}
	_ = total
	// 页签这一排以外的元素一帧也不动
	for i, o := range r.outer[from-1:] {
		if o != r.outer[from-1] || o[0].W == 0 {
			t.Fatalf("第 %d 帧页签以外的元素动了：%+v → %+v", i, r.outer[from-1], o)
		}
	}
	// 键盘左右键
	r.tt.Key(0, ui.KeyRight)
	step(r.tt, 16*time.Millisecond)
	if r.sel != 2 {
		t.Errorf("右方向键后选中 %d，应为 2", r.sel)
	}
}

// 减少动态效果：点完一帧就是终点的宽度。
func TestIconTabsReduceMotion(t *testing.T) {
	r := newTabRig(t, true)
	if err := r.tt.Click("运行"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	w := r.widths[len(r.widths)-1]
	h := r.k.G.HitSmall
	if w[0] != h || w[1] != h || w[2] <= h+10 || w[3] != h {
		t.Errorf("减少动态效果时点完一帧该是终点：%v", w)
	}
}
