package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

func quotedImageView(segs ...api.DisplaySegment) api.MessageView {
	v := view("q1", "human", "编造发送者", "回复的话", t0)
	ref := api.EventRef{Conversation: v.Ref.Conversation, Event: api.EventKey{Platform: "qq", EventID: "orig"}}
	v.ReplyTo = &ref
	v.Reply = api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{Ref: ref, DisplayName: "编造被引人", Segments: segs}}
	return v
}

func thumbMediaRef(hash string) *api.MediaRef {
	return &api.MediaRef{Hash: hash, Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}
}

// 引用投影里的图片、贴纸、视频封面都会成为缩略图；文字显示原文第一行，只有图时写短词。
func TestQuoteThumbFromProjection(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		segs []api.DisplaySegment
		kind chatui.ThumbKind
		text string
	}{
		{"只有图", []api.DisplaySegment{{Kind: "image", Media: thumbMediaRef("a")}}, chatui.ThumbImage, "图片"},
		{"图加文字", []api.DisplaySegment{{Kind: "text", Text: "\n第一行\n第二行"}, {Kind: "image", Media: thumbMediaRef("a")}}, chatui.ThumbImage, "第一行"},
		{"贴纸", []api.DisplaySegment{{Kind: "emoji", Sticker: true, Media: thumbMediaRef("s")}}, chatui.ThumbSticker, "表情"},
		{"视频封面", []api.DisplaySegment{{Kind: "video", Cover: thumbMediaRef("c")}}, chatui.ThumbVideo, "视频"},
		{"视频没有封面", []api.DisplaySegment{{Kind: "video"}}, "", "[视频]"},
		{"纯文字", []api.DisplaySegment{{Kind: "text", Text: "只有字"}}, "", "只有字"},
	} {
		q := quoteOf(quotedImageView(c.segs...))
		if q == nil || !q.ok {
			t.Fatalf("%s：没有引用", c.name)
		}
		if q.thumbKind != c.kind || (c.kind != "") != (q.thumb != nil) {
			t.Fatalf("%s：缩略图种类 %q，期望 %q", c.name, q.thumbKind, c.kind)
		}
		if q.text != c.text {
			t.Fatalf("%s：文字 %q，期望 %q", c.name, q.text, c.text)
		}
		if q.thumb != nil && q.thumb.Animated {
			t.Fatalf("%s：缩略图不该是动图", c.name)
		}
	}
}

func quoteWorkbench(t *testing.T, thumbState int) (*app, *ui.Tester) {
	t.Helper()
	v := quotedImageView(api.DisplaySegment{Kind: "image", Width: 200, Height: 100, Media: thumbMediaRef("qthumb")})
	m := msgFromView(v.Ref.Conversation, v, charOf)
	a := &app{sel: "c", msgs: []*msg{m}, gen: 1, srcGen: 1}
	a.list = a.newList()
	st := a.is()
	st.scale = 1
	if m.quote == nil || m.quote.thumb == nil {
		t.Fatal("消息的引用没带缩略图")
	}
	size := chatui.Derive(chatui.DefaultSettings()).QuoteThumbSize()
	e := &imgEntry{state: thumbState, bmp: redBitmap(pureRed), job: &imgJob{}}
	st.m[keyOf(*m.quote.thumb, size, false, 1)] = e
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 600)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return a, tt
}

// 新界面里被引消息的缩略图走消息的图片加载链路画出来；取不到时退回文字。
func TestWorkbenchQuoteThumbnail(t *testing.T) {
	_, tt := quoteWorkbench(t, imgOK)
	r, ok := tt.Find("引用的图片")
	if !ok {
		t.Fatalf("引用块里没有缩略图：%v", tt.Texts())
	}
	if px := tt.Image(); px != nil {
		if got := px.RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2)); got != pureRed {
			t.Fatalf("缩略图中心像素 %v，应该是红色", got)
		}
	}
	if !tt.HasText("图片") {
		t.Fatalf("只有图时应写“图片”：%v", tt.Texts())
	}
	_, tt = quoteWorkbench(t, imgFailed)
	if _, ok := tt.Find("引用的图片"); ok {
		t.Fatal("取不到时不该画缩略图")
	}
	if !tt.HasText("图片") {
		t.Fatalf("取不到时文字也要在：%v", tt.Texts())
	}
}
