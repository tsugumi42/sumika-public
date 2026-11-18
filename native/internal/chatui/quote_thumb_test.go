package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// thumbMedia 同时实现 MediaResolver 和 QuoteMedia。
type thumbMedia struct {
	blockMedia
	thumb MediaResult
	asked []string
}

func (m *thumbMedia) QuoteThumb(id string, px int) MediaResult {
	m.asked = append(m.asked, id)
	return m.thumb
}

func quoteMsg(kind ThumbKind) *Msg {
	text := "原消息的第一行"
	return &Msg{ID: "q", Name: "甲", Sender: "甲", At: time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local), Text: "回复的话", Blocks: segview.TextBlocks("回复的话"),
		Quote: &Quote{Name: "乙", Text: text, Target: "orig", Thumb: kind}}
}

func quoteRender(t *testing.T, media MediaResolver, m *Msg) *ui.Tester {
	t.Helper()
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: media}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("p")).Padding(20).Children(func() { h.Message(c, m, nil, nil) })
	}, 700, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt
}

// 被引消息有图：引用块里竖线右边有方形缩略图，高度等于两行字，右边是名字和文字；块高和没有缩略图时一样。
func TestQuoteShowsThumbnail(t *testing.T) {
	for _, kind := range []ThumbKind{ThumbImage, ThumbSticker, ThumbVideo} {
		media := &thumbMedia{thumb: mbReady}
		withThumb := quoteRender(t, media, quoteMsg(kind))
		quote := mustFind(t, withThumb, "引用")
		thumb := mustFind(t, withThumb, "引用的图片")
		size := Derive(DefaultSettings()).QuoteThumbSize()
		if !mbNear(thumb.W, size) || !mbNear(thumb.H, size) {
			t.Fatalf("%s：缩略图 %+v 应是 %.1f 的方形", kind, thumb, size)
		}
		if thumb.X < quote.X || thumb.X+thumb.W > quote.X+quote.W || thumb.Y < quote.Y || thumb.Y+thumb.H > quote.Y+quote.H {
			t.Fatalf("%s：缩略图 %+v 不在引用块 %+v 里", kind, thumb, quote)
		}
		if px := withThumb.Image(); px != nil {
			if got := px.RGBAAt(int(thumb.X+thumb.W/2), int(thumb.Y+thumb.H/2)); kind != ThumbVideo && got != mbRed {
				t.Fatalf("%s：缩略图中心像素 %v 不是图的颜色", kind, got)
			}
		}
		if len(media.asked) == 0 || media.asked[0] != "q" {
			t.Fatalf("%s：没有按引用所在消息 id 取图：%v", kind, media.asked)
		}
		if !withThumb.HasText("原消息的第一行") {
			t.Fatalf("%s：右边的文字不见了", kind)
		}
		// 块高不变
		plain := quoteRender(t, &thumbMedia{thumb: mbReady}, quoteMsg(""))
		if pq := mustFind(t, plain, "引用"); !mbNear(pq.H, quote.H) {
			t.Fatalf("%s：有缩略图引用块高 %.1f，没有时 %.1f", kind, quote.H, pq.H)
		}
		if kind == ThumbVideo {
			if _, ok := withThumb.Find("引用的图片"); !ok {
				t.Fatal("视频缩略图丢了")
			}
		}
	}
}

// 取不到、没实现取图接口、已丢失：退回只有文字，没有缩略图元素。加载中占位，块宽不跳。
func TestQuoteThumbnailFallsBackToText(t *testing.T) {
	cases := map[string]MediaResolver{
		"失败":    &thumbMedia{thumb: MediaResult{State: MediaFailed}},
		"缺失":    &thumbMedia{thumb: MediaResult{State: MediaMissing}},
		"没实现接口": &blockMedia{},
	}
	for name, media := range cases {
		tt := quoteRender(t, media, quoteMsg(ThumbImage))
		if _, ok := tt.Find("引用的图片"); ok {
			t.Fatalf("%s：不该有缩略图", name)
		}
		if !tt.HasText("原消息的第一行") {
			t.Fatalf("%s：文字也没了", name)
		}
	}
	loading := quoteRender(t, &thumbMedia{thumb: MediaResult{State: MediaLoading}}, quoteMsg(ThumbImage))
	ready := quoteRender(t, &thumbMedia{thumb: mbReady}, quoteMsg(ThumbImage))
	a, b := mustFind(t, loading, "引用的图片"), mustFind(t, ready, "引用的图片")
	qa, qb := mustFind(t, loading, "引用"), mustFind(t, ready, "引用")
	if a != b || !mbNear(qa.W, qb.W) || !mbNear(qa.H, qb.H) {
		t.Fatalf("加载前后缩略图或引用块变了：%+v/%+v vs %+v/%+v", a, qa, b, qb)
	}
	// 被引消息已丢失（Gone）：不取图
	gone := quoteMsg(ThumbImage)
	gone.Quote.Gone = true
	m := &thumbMedia{thumb: mbReady}
	tt := quoteRender(t, m, gone)
	if _, ok := tt.Find("引用的图片"); ok || len(m.asked) != 0 {
		t.Fatal("原消息不在本地时不该画缩略图也不该取图")
	}
}

// 输入区上方的引用卡同样显示缩略图。
func TestComposerQuoteCardThumbnail(t *testing.T) {
	r := newComposerRigSized(t, 640, 600)
	r.host.QuoteThumb = func(id string, px int) MediaResult { return mbReady }
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "乙", Text: "选中的话", Thumb: ThumbImage}, false)
	r.tester.Frame()
	r.tester.Frame()
	thumb, ok := r.tester.Find("引用的图片")
	if !ok {
		t.Fatalf("引用卡里没有缩略图：%v", r.tester.Texts())
	}
	card := mustFind(t, r.tester, "引用 1：乙")
	if thumb.X < card.X || thumb.X+thumb.W > card.X+card.W || thumb.Y < card.Y || thumb.Y+thumb.H > card.Y+card.H {
		t.Fatalf("缩略图 %+v 不在卡 %+v 里", thumb, card)
	}
	// 没有缩略图的引用卡不画
	r2 := newComposerRigSized(t, 640, 600)
	r2.host.QuoteThumb = func(id string, px int) MediaResult { return mbReady }
	r2.p.AddQuote(Quoted{MessageID: "m2", Name: "丙", Text: "纯文字"}, false)
	r2.tester.Frame()
	r2.tester.Frame()
	if _, ok := r2.tester.Find("引用的图片"); ok {
		t.Fatal("纯文字引用不该有缩略图")
	}
}
