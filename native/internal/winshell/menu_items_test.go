package winshell

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/uitest"
)

type menuRig struct {
	k    *Kit
	tt   *ui.Tester
	open bool
}

// 一个点开的手画菜单（PillMenu 的面板）。
func newMenuRig(t *testing.T, items []MenuItem) *menuRig {
	t.Helper()
	r := &menuRig{k: testKit()}
	r.tt = ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&r.k.Theme)
		ui.Column(c.Key("root")).Width(500).Padding(20).Children(func() {
			r.k.PillMenu(c, "pill", "目录", "选目录", IconSearch, &r.open, items)
		})
	}, 700, 600)
	r.tt.Frame()
	r.open = true
	r.tt.Frame()
	r.tt.Frame()
	for range 10 { // 菜单淡入放完再量颜色
		r.tt.Advance(50 * time.Millisecond)
		r.tt.Frame()
	}
	return r
}

// rect 是菜单里这行字本身的位置（不是整项：Find 先找到的是整项）。
func (r *menuRig) rect(t *testing.T, text string) ui.Rect {
	t.Helper()
	for _, n := range flat(uitest.Tree(r.tt)) {
		if n.Kind == 1 && n.Text == text {
			return n.Box
		}
	}
	t.Fatalf("菜单里没有 %q：%q", text, r.tt.Texts())
	return ui.Rect{}
}

// 危险项在最下面、前面有一条分隔线、红字；快捷键和原因是右边的灰字；不可用的项变灰、点了不响应。
func TestMenuItemsDangerShortcutDisabled(t *testing.T) {
	var did []string
	act := func(name string) func() { return func() { did = append(did, name) } }
	r := newMenuRig(t, []MenuItem{
		{Label: "删除…", Danger: true, Action: act("删除")}, // 故意写在最前：该被排到最后
		{Label: "复制", Shortcut: "Ctrl+C", Action: act("复制")},
		{Label: "回复", Disabled: true, Trailing: "没有文字", Action: act("回复")},
		{Label: "分享", Separator: true, Action: act("分享")},
	})
	del, copyR, reply, share := r.rect(t, "删除…"), r.rect(t, "复制"), r.rect(t, "回复"), r.rect(t, "分享")
	if !(copyR.Y < reply.Y && reply.Y < share.Y && share.Y < del.Y) {
		t.Fatalf("危险项该在最下面：复制 %.0f 回复 %.0f 分享 %.0f 删除 %.0f", copyR.Y, reply.Y, share.Y, del.Y)
	}
	if !r.tt.HasText("Ctrl+C") || !r.tt.HasText("没有文字") {
		t.Fatalf("快捷键和原因该在右边：%q", r.tt.Texts())
	}
	if ctrl, name := r.rect(t, "Ctrl+C"), copyR; ctrl.X < name.X+name.W {
		t.Fatalf("快捷键该在名字右边：%+v %+v", ctrl, name)
	}
	root := uitest.Tree(r.tt)
	seps := 0
	for _, n := range flat(root) {
		if len(n.Key) > 0 && contains(n.Key, "-separator-") {
			seps++
			if n.Box.H > 1.5 {
				t.Fatalf("分隔线该是一条细线：%+v", n.Box)
			}
		}
	}
	if seps != 2 { // 分享上面一条（显式）、删除上面一条（危险项自动）
		t.Fatalf("分隔线应有 2 条，实际 %d", seps)
	}
	// 红字：删除的字色和别的项不同
	img := r.tt.Image()
	if ink := darkest(img, del); ink.R <= ink.G+30 {
		t.Fatalf("危险项该是红字：%+v", ink)
	}
	if ink := darkest(img, copyR); ink.R > ink.G+30 {
		t.Fatalf("普通项不该是红字：%+v", ink)
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "菜单-危险灰项快捷键.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img.SubImage(image.Rect(0, 30, 420, 300))); err != nil {
			t.Fatal(err)
		}
	}
	// 不可用的项点了没反应
	_ = r.tt.Click("回复")
	r.tt.Frame()
	if len(did) != 0 {
		t.Fatalf("不可用的项不该响应：%v", did)
	}
	if !r.open {
		t.Fatal("点不可用的项菜单不该收起")
	}
	if err := r.tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	if len(did) != 1 || did[0] != "复制" {
		t.Fatalf("可用的项该响应：%v", did)
	}
}

// 同一个菜单里图标要么都有要么都没有：有一项带图标，没图标的项留同宽的空位，文字左边对齐。
func TestMenuItemsIconsAlign(t *testing.T) {
	r := newMenuRig(t, []MenuItem{{Label: "选择文件夹…", Icon: IconSearch}, {Label: "最近用过的目录"}, {Label: "恢复默认", Danger: true}})
	a, b, c := r.rect(t, "选择文件夹…"), r.rect(t, "最近用过的目录"), r.rect(t, "恢复默认")
	if abs32(a.X-b.X) > 0.5 || abs32(a.X-c.X) > 0.5 {
		t.Fatalf("有无图标的项文字该左对齐：%.1f %.1f %.1f", a.X, b.X, c.X)
	}
	r2 := newMenuRig(t, []MenuItem{{Label: "甲"}, {Label: "乙"}})
	if p, q := r2.rect(t, "甲"), r.rect(t, "选择文件夹…"); p.X >= q.X {
		t.Fatalf("没有任何图标的菜单不该留空位：%.1f 对 %.1f", p.X, q.X)
	}
}

func contains(s, part string) bool { return strings.Contains(s, part) }

// flat 把元素树摊平。
func flat(n *uitest.Node) []*uitest.Node {
	out := []*uitest.Node{n}
	for _, ch := range n.Children {
		out = append(out, flat(ch)...)
	}
	return out
}

// darkest 是矩形里最暗的那个像素，当作字色。
func darkest(img *image.RGBA, r ui.Rect) color.RGBA {
	best, lum := color.RGBA{255, 255, 255, 255}, 1<<30
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			px := img.RGBAAt(x, y)
			if l := int(px.R) + int(px.G) + int(px.B); l < lum {
				best, lum = px, l
			}
		}
	}
	return best
}

// 会话行右键：出菜单，里面是宿主给的项；危险项在最下面；不可用的灰；没有菜单的行右键什么也不出。
func TestSidebarRowContextMenu(t *testing.T) {
	k := testKit()
	s := &State{}
	var marked []string
	convs := []Conversation{{ID: "a", Name: "甲会话"}, {ID: "b", Name: "乙会话"}}
	query := ""
	sp := &SidebarSpec{
		Search: &query,
		Count:  len(convs), At: func(i int) Conversation { return convs[i] }, Selected: "a", OnSelect: func(string) {},
		RowMenu: func(id string) []MenuItem {
			if id == "b" {
				return nil
			}
			return []MenuItem{{Label: "标为已读", Action: func() { marked = append(marked, id) }}, {Label: "删除…", Danger: true, Disabled: true, Trailing: "还没有这个功能"}}
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Fill().Children(func() { k.Sidebar(c, s, sp, false) })
	}, 600, 700)
	tt.Frame()
	tt.Frame()
	if err := tt.RightClick("乙会话"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if tt.HasText("标为已读") {
		t.Fatal("没有菜单的行右键不该出菜单")
	}
	if err := tt.RightClick("甲会话"); err != nil {
		t.Fatal(err)
	}
	settle(tt) // 菜单长开（MenuMorph）以后再点

	if !tt.HasText("标为已读") || !tt.HasText("还没有这个功能") {
		t.Fatalf("右键该出菜单：%q", tt.Texts())
	}
	if err := tt.Click("标为已读"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(marked) != 1 || marked[0] != "a" {
		t.Fatalf("该对这一行的会话动手：%v", marked)
	}
}
