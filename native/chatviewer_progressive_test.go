package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// loadingApp 造一条 120×80 的静态图消息：列表缩略图已在缓存里，看大图用的 480 档还没好（imgLoading）。
func loadingApp(t *testing.T) (*app, *ui.Tester, imgKey) {
	t.Helper()
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "image", Width: 120, Height: 80,
		Media: &api.MediaRef{Hash: "pic1", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m := mediaMsg("p", s)
	a := &app{sel: "c", msgs: []*msg{m}, gen: 1, srcGen: 1}
	a.list = a.newList()
	st := a.is()
	st.scale = 1
	st.m[keyOf(m.segs[0], 320, false, 1)] = &imgEntry{state: imgOK, bmp: redBitmap(pureRed)}
	st.m[keyOf(m.segs[0], 256, false, 1)] = &imgEntry{state: imgOK, bmp: redBitmap(pureRed)}
	big := keyOf(m.segs[0], 480, false, 1)
	st.m[big] = &imgEntry{state: imgLoading, job: &imgJob{}}
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c); a.chatImageWindow(c) }, 900, 600)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return a, tt, big
}

func noModal(t *testing.T, tt *ui.Tester, when string) {
	t.Helper()
	if tt.HasText("正在准备图片") {
		t.Fatalf("%s：出现了“正在准备图片”浮层", when)
	}
}

// 加载快（低于阈值就好了）：逐帧都没有浮层、没有加载指示，图一直在，尺寸不变。
func TestViewerFastLoadShowsNoOverlay(t *testing.T) {
	a, tt, big := loadingApp(t)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	var first ui.Rect
	for i := 0; i < 8; i++ {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
		noModal(t, tt, "加载中")
		if _, ok := tt.Find("正在加载原图"); ok {
			t.Fatalf("第 %d 帧（不到阈值）就出现了加载指示", i)
		}
		r, ok := tt.Find("图片·大图")
		if !ok {
			t.Fatalf("第 %d 帧大图区域是空白：%v", i, tt.Texts())
		}
		if i == 0 {
			first = r
		} else if r != first {
			t.Fatalf("第 %d 帧大图 %+v 和第一帧 %+v 不一样", i, r, first)
		}
	}
	a.is().m[big].bmp = redBitmap(pureRed) // 原图好了
	a.is().m[big].state = imgOK
	for i := 0; i < 4; i++ {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
		noModal(t, tt, "原图好了以后")
		if _, ok := tt.Find("正在加载原图"); ok {
			t.Fatal("没到阈值原图就好了，不该出现加载指示")
		}
		if r, _ := tt.Find("图片·大图"); r != first {
			t.Fatalf("换成原图以后尺寸变了 %+v vs %+v", r, first)
		}
	}
}

// 加载慢：阈值之前没有指示，阈值之后出现在右下角；原图好了指示消失，尺寸不变。
func TestViewerSlowLoadShowsCornerSpinner(t *testing.T) {
	a, tt, big := loadingApp(t)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	img0, ok := tt.Find("图片·大图")
	if !ok {
		t.Fatal("没有大图区域")
	}
	tt.Advance(viewLoadingDelay - 50*time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("正在加载原图"); ok {
		t.Fatal("阈值前出现了加载指示")
	}
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	noModal(t, tt, "阈值后")
	sp, ok := tt.Find("正在加载原图")
	if !ok {
		t.Fatalf("超过阈值后没有加载指示：%v", tt.Texts())
	}
	if sp.X < 900/2 || sp.Y < 600/2 {
		t.Fatalf("加载指示 %+v 应在右下角", sp)
	}
	if sp.X < img0.X+img0.W/2 && sp.Y < img0.Y+img0.H/2 {
		t.Fatalf("加载指示 %+v 不该盖在图 %+v 的中间", sp, img0)
	}
	a.is().m[big].bmp = redBitmap(pureRed)
	a.is().m[big].state = imgOK
	tt.Advance(20 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("正在加载原图"); ok {
		t.Fatal("原图好了加载指示还在")
	}
	if r, _ := tt.Find("图片·大图"); r != img0 {
		t.Fatalf("换成原图后尺寸 %+v 变了，原来 %+v", r, img0)
	}
}

// 失败：角落出现失败图标（不等阈值），图还在，没有浮层。
func TestViewerFailureShowsCornerIcon(t *testing.T) {
	a, tt, big := loadingApp(t)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.is().m[big].state = imgFailed
	tt.Advance(20 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	noModal(t, tt, "失败")
	f, ok := tt.Find("原图加载失败")
	if !ok {
		t.Fatalf("失败后角落没有图标：%v", tt.Texts())
	}
	if f.X < 900/2 || f.Y < 600/2 {
		t.Fatalf("失败图标 %+v 应在右下角", f)
	}
	if _, ok := tt.Find("图片·大图"); !ok {
		t.Fatal("失败时缩略图应还在")
	}
}
