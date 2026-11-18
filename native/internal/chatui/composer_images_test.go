package chatui

import (
	"image/color"
	"testing"
)

// 输入框里的图片缩略图：方块铺满、关闭钮小而不压主体、点缩略图看大图、点关闭钮只移除。

func rigWithImages(t *testing.T, names ...string) (*composerRig, []uint64) {
	t.Helper()
	r := newComposerRig(t)
	src := thumb(t, color.RGBA{200, 80, 60, 255}, color.RGBA{60, 80, 200, 255})
	var ids []uint64
	for _, n := range names {
		id, ok := r.p.Attach(nil, Att{Kind: AttImage, Name: n, Path: "D:/假目录/" + n, Thumb: src, Width: 800, Height: 600})
		if !ok {
			t.Fatal("挂不上")
		}
		ids = append(ids, id)
	}
	r.tester.Frame()
	r.tester.Frame()
	return r, ids
}

func TestComposerImageTileIsSquareAndRemoveButtonIsSmall(t *testing.T) {
	r, _ := rigWithImages(t, "甲.png")
	tile := mustFind(t, r.tester, "查看图片：甲.png")
	want := attTile(r.host.T)
	if !mbNear(tile.W, want) || !mbNear(tile.H, want) {
		t.Fatalf("缩略图该是 %.0f 的方块：%+v", want, tile)
	}
	rm := mustFind(t, r.tester, "移除附件 甲.png")
	// 看得见的圆 20，可点范围 24（WCAG 2.2 最小目标尺寸），都远小于缩略图
	if rm.W < 24 || rm.H < 24 || rm.W > tile.W/3 {
		t.Fatalf("关闭钮可点范围该不小于 24、也不压主体：%+v（缩略图 %+v）", rm, tile)
	}
	if disc := mustFind(t, r.tester, "关闭钮"); !mbNear(disc.W, round1(attRemoveBase*r.host.T.TS)) {
		t.Fatalf("看得见的圆该是 %.0f：%+v", round1(attRemoveBase*r.host.T.TS), disc)
	}
	if rm.X < tile.X || rm.Y < tile.Y || rm.X+rm.W > tile.X+tile.W || rm.Y+rm.H > tile.Y+tile.H {
		t.Fatalf("关闭钮该在缩略图里面的角上：%+v 在 %+v", rm, tile)
	}
	if rm.Y-tile.Y > tile.H/4 || tile.X+tile.W-(rm.X+rm.W) > tile.W/4 {
		t.Fatalf("关闭钮该贴右上角：%+v 在 %+v", rm, tile)
	}
}

func TestComposerImageClickOpensViewerAndRemoveDoesNot(t *testing.T) {
	r, ids := rigWithImages(t, "甲.png", "乙.png")
	var opened []uint64
	r.host.OnOpenImage = func(id uint64) { opened = append(opened, id) }
	r.tester.Frame()
	if err := r.tester.Click("查看图片：乙.png"); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != ids[1] {
		t.Fatalf("点缩略图该打开被点的那张：%v，期望 %d", opened, ids[1])
	}
	if err := r.tester.Click("移除附件 甲.png"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(opened) != 1 || len(r.p.Main.Atts) != 1 || r.p.Main.Atts[0].Name != "乙.png" {
		t.Fatalf("点关闭钮只移除、不打开：打开 %v，剩 %+v", opened, r.p.Main.Atts)
	}
}

func TestComposerImageAttsListsOnlyReadyImagesInOrder(t *testing.T) {
	p := NewComposer()
	src := thumb(t, color.RGBA{1, 2, 3, 255}, color.RGBA{9, 8, 7, 255})
	a, _ := p.Attach(nil, Att{Kind: AttImage, Name: "a", Path: "p/a", Thumb: src})
	p.Attach(nil, Att{Kind: AttFile, Name: "f", Path: "p/f"})
	p.Attach(nil, Att{Kind: AttImage, Name: "saving", Pending: true})
	p.Attach(nil, Att{Kind: AttImage, Name: "nothumb", Path: "p/n"})
	b, _ := p.Attach(nil, Att{Kind: AttImage, Name: "b", Path: "p/b", Thumb: src})
	got := p.ImageAtts()
	if len(got) != 2 || got[0].ID != a || got[1].ID != b {
		t.Fatalf("只列读好的图、按顺序：%+v", got)
	}
}

// 没有缩略图（读不到、解不开）和还在保存的：画占位、不能点开，关闭钮照样能用，不是空白大框。
func TestComposerImageWithoutThumbShowsPlaceholderAndCannotOpen(t *testing.T) {
	r := newComposerRig(t)
	r.p.Attach(nil, Att{Kind: AttImage, Name: "坏图.png", Path: "D:/假目录/坏图.png"})
	r.tester.Frame()
	r.tester.Frame()
	tile := mustFind(t, r.tester, "查看图片：坏图.png")
	if tile.W < 40 {
		t.Fatalf("占位也该有方块的大小：%+v", tile)
	}
	opened := 0
	r.host.OnOpenImage = func(uint64) { opened++ }
	r.tester.Frame()
	_ = r.tester.Click("查看图片：坏图.png")
	if opened != 0 {
		t.Fatal("没有缩略图的不能点开")
	}
	if err := r.tester.Click("移除附件 坏图.png"); err != nil || len(r.p.Main.Atts) != 0 {
		t.Fatalf("关闭钮该能移除：%v %+v", err, r.p.Main.Atts)
	}
}
