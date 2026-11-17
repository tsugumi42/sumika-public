package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 分块消息里的图片（规格-引用 第十节）：真图、宽度贴合内容、点开大图、取不到时的占位。

func partsMsg(id string, parts ...Part) *Msg {
	return &Msg{ID: id, Self: true, Name: "我", Sender: "owner", Parts: parts, At: time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)}
}

func picAtt(name string, pos, w, h int) PartAtt {
	return PartAtt{Name: name, Image: true, Show: true, Pos: pos, Width: w, Height: h}
}

type viewLog struct {
	id  string
	pos []int
}

func partsHost(res MediaResult, log *viewLog) Host {
	h := mbHost(res)
	if log != nil {
		h.OnViewImage = func(id string, pos int) { log.id, log.pos = id, append(log.pos, pos) }
	}
	return h
}

func pbRect(t *testing.T, tt *ui.Tester) ui.Rect { t.Helper(); return mustFind(t, tt, "气泡") }

// 一条只有短文件名的分块消息：气泡贴合文字，不是铺满整列。旧写法把分块消息一律当"列表、代码块"取满宽，成了长条。
func TestPartsBubbleHugsShortContent(t *testing.T) {
	h := partsHost(MediaResult{}, nil)
	max := h.Column * h.T.BubbleShare
	for name, parts := range map[string][]Part{
		"短文件名":   {{Atts: []PartAtt{{Name: "a.txt"}}}},
		"一句话加文件": {{Text: "好", Atts: []PartAtt{{Name: "a.txt"}}}},
		"引用加短回复": {{Quote: &PartQuote{Target: "t", Name: "甲", Snippet: "嗯"}, Text: "好"}},
	} {
		tt := mbRender(t, h, partsMsg("m", parts...))
		if w := pbRect(t, tt).W; w > max*0.6 {
			t.Fatalf("%s：气泡宽 %.0f，列宽上限 %.0f，该贴合内容", name, w, max)
		}
	}
}

// 长文字、长引用仍然折行：气泡宽不超过上限，也不比引用块窄。
func TestPartsBubbleLongTextStillWraps(t *testing.T) {
	h := partsHost(MediaResult{}, nil)
	max := h.Column * h.T.BubbleShare
	long := strings.Repeat("这是一段很长的回复文字", 20)
	tt := mbRender(t, h, partsMsg("m", Part{Quote: &PartQuote{Target: "t", Name: "甲", Snippet: long}, Text: long}))
	w := pbRect(t, tt).W
	if w > max+0.5 || w < max*0.9 {
		t.Fatalf("长文字该折行填满气泡：宽 %.0f，上限 %.0f", w, max)
	}
	quote := mustFind(t, tt, "引用 甲：点一下回到原消息")
	if quote.W > w {
		t.Fatalf("引用块 %.0f 比气泡 %.0f 宽", quote.W, w)
	}
}

// 只有一张图：气泡 = 图宽，图铺满、不留边；宽高比对得上；时间叠在图上。
func TestPartsImageOnlyFillsBubbleAtImageRatio(t *testing.T) {
	h := partsHost(mbReady, nil)
	tt := mbRender(t, h, partsMsg("m", Part{Atts: []PartAtt{picAtt("风景.png", 0, 800, 600)}}))
	b, pic := pbRect(t, tt), mustFind(t, tt, "查看图片")
	if !mbNear(b.W, pic.W) || !mbNear(b.H, pic.H) {
		t.Fatalf("图该铺满气泡：气泡 %+v，图 %+v", b, pic)
	}
	if !mbNear(pic.W/pic.H, 800.0/600.0) || pic.W > mediaMaxW+0.5 {
		t.Fatalf("宽高比或宽度不对：%+v", pic)
	}
	if _, ok := tt.Find("时间牌子"); !ok {
		t.Fatal("纯图消息的时间该叠在图上")
	}
}

// 待发气泡和存好后的气泡是同一个样子：只有消息 id 不同，尺寸和排版一模一样（接替时不跳）。
func TestPartsPendingAndStoredLookTheSame(t *testing.T) {
	parts := []Part{{Text: "看这个", Atts: []PartAtt{picAtt("a.png", 0, 640, 480), picAtt("b.png", 1, 480, 640), {Name: "笔记.txt"}}}}
	var rects [2]ui.Rect
	for i, id := range []string{"live-send-1", "evt-1"} {
		rects[i] = pbRect(t, mbRender(t, partsHost(mbReady, nil), partsMsg(id, parts...)))
	}
	if rects[0] != rects[1] {
		t.Fatalf("待发 %+v 和存好 %+v 不一样", rects[0], rects[1])
	}
}

// 图加文字：图在上铺满气泡，文字在下；带引用时引用卡在最上面、图在引用下面，图仍然贴着气泡左右边（引用卡自己缩进）。
func TestPartsImageWithTextAndQuoteOrder(t *testing.T) {
	h := partsHost(mbReady, nil)
	tt := mbRender(t, h, partsMsg("m", Part{Text: "配文", Atts: []PartAtt{picAtt("a.png", 0, 400, 300)}}))
	b, pic, txt := pbRect(t, tt), mustFind(t, tt, "查看图片"), mustFind(t, tt, "配文")
	if !mbNear(b.W, pic.W) || txt.Y < pic.Y+pic.H || txt.X < b.X+h.T.BubblePadH-0.6 {
		t.Fatalf("图该铺满气泡宽、文字在图下面且缩进：气泡 %+v 图 %+v 字 %+v", b, pic, txt)
	}
	tt = mbRender(t, h, partsMsg("m", Part{Quote: &PartQuote{Target: "t", Name: "甲", Snippet: "被引的话"}, Text: "回复", Atts: []PartAtt{picAtt("a.png", 0, 400, 300)}}))
	q, pic, txt := mustFind(t, tt, "引用 甲：点一下回到原消息"), mustFind(t, tt, "查看图片"), mustFind(t, tt, "回复")
	if !(q.Y < pic.Y && pic.Y < txt.Y) {
		t.Fatalf("顺序该是引用、图、文字：%+v %+v %+v", q, pic, txt)
	}
	b = pbRect(t, tt)
	if !mbNear(pic.X, b.X) || !mbNear(pic.W, b.W) || !mbNear(pic.Y-(q.Y+q.H), h.T.BubblePadV) {
		t.Fatalf("带引用时图该贴着气泡左右边、和引用卡隔一个竖内边距：气泡 %+v 引用 %+v 图 %+v", b, q, pic)
	}
}

// 点图走 OnViewImage，报这条消息的 id 和图的序号；同一条消息的图各有各的序号（看大图可以左右切换）。
func TestPartsImageClickOpensViewer(t *testing.T) {
	var log viewLog
	h := partsHost(mbReady, &log)
	m := partsMsg("evt-9", Part{Atts: []PartAtt{picAtt("a.png", 3, 400, 300), picAtt("b.png", 4, 300, 400)}})
	tt := mbRender(t, h, m)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	if log.id != "evt-9" || len(log.pos) != 1 || log.pos[0] != 3 {
		t.Fatalf("点第一张该报 evt-9 的序号 3：%+v", log)
	}
}

// 取不到图（文件被删、读不到）：占位写清文件名，大小不变，不是空白大框也不塌成一条。
func TestPartsMissingImageKeepsSizeAndShowsName(t *testing.T) {
	ok := pbRect(t, mbRender(t, partsHost(mbReady, nil), partsMsg("m", Part{Atts: []PartAtt{picAtt("丢了的图.png", 0, 800, 600)}})))
	tt := mbRender(t, partsHost(MediaResult{State: MediaFailed}, nil), partsMsg("m", Part{Atts: []PartAtt{picAtt("丢了的图.png", 0, 800, 600)}}))
	if got := pbRect(t, tt); !mbNear(got.W, ok.W) || !mbNear(got.H, ok.H) {
		t.Fatalf("取不到时大小该和好的一样：%+v vs %+v", got, ok)
	}
	found := false
	for _, s := range tt.Texts() {
		found = found || strings.Contains(s, "丢了的图.png")
	}
	if !found {
		t.Fatalf("占位里该写文件名：%v", tt.Texts())
	}
}

// 没有宽高（老记录读不出来）：按 4:3 占位，不崩、不是 0 大小。
func TestPartsImageWithoutDimsUsesPlaceholderRatio(t *testing.T) {
	tt := mbRender(t, partsHost(mbReady, nil), partsMsg("m", Part{Atts: []PartAtt{picAtt("a.png", 0, 0, 0)}}))
	pic := mustFind(t, tt, "查看图片")
	if pic.W < 100 || !mbNear(pic.W/pic.H, 4.0/3.0) {
		t.Fatalf("没有宽高时按 4:3：%+v", pic)
	}
}

// 非图片附件、没给图的图片附件仍是文件小标签，不画真图。
func TestPartsFilesAndUnshownImagesStayChips(t *testing.T) {
	tt := mbRender(t, partsHost(mbReady, nil), partsMsg("m", Part{Text: "附件", Atts: []PartAtt{{Name: "笔记.txt"}, {Name: "旧图.png", Image: true}}}))
	if _, ok := tt.Find("查看图片"); ok {
		t.Fatal("没给图的附件不该画真图")
	}
	mustFind(t, tt, "附件：笔记.txt")
	mustFind(t, tt, "附件：旧图.png")
}

// 九张图：相册排法，每格都有图，宽度不超过上限。
func TestPartsAlbumOfNineStaysInsideBubble(t *testing.T) {
	var atts []PartAtt
	for i := range 9 {
		atts = append(atts, picAtt("图.png", i, 640, 480))
	}
	h := partsHost(mbReady, nil)
	tt := mbRender(t, h, partsMsg("m", Part{Atts: atts}))
	b := pbRect(t, tt)
	if b.W > mediaMaxW+0.5 || b.W > h.Column*h.T.BubbleShare+0.5 {
		t.Fatalf("相册宽 %.0f 超了上限", b.W)
	}
}
