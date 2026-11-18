package chatui

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
	"sumika-native/internal/segview"
)

var bubbleTestAt = time.Date(2026, 11, 9, 2, 4, 0, 0, time.Local)

func cardMessages() []*Msg {
	at := bubbleTestAt
	return []*Msg{
		{ID: "file", Sender: "丙", Name: "丙", At: at, Blocks: []segview.Block{{Kind: segview.File, VideoIndex: 1, Video: segview.Segment{Name: "EF Studio 预告片.mp4", Size: 1 << 27, CanDownload: true}}}},
		{ID: "forward", Sender: "丁", Name: "丁", At: at, Blocks: []segview.Block{{Kind: segview.ForwardBlock, Card: segview.Segment{Position: 2, Forward: &segview.Forward{Title: "聊天记录", Lines: [3]string{"甲：一句", "乙：两句", "丙：三句"}, Count: 6}}}}},
		{ID: "card", Sender: "戊", Name: "戊", At: at, Blocks: []segview.Block{{Kind: segview.CardBlock, Card: segview.Segment{Card: &segview.Card{Title: "QQ经典农场", Description: "偷我菜的全部记到小条条上", Source: "com.tencent.miniapp_01"}}}}},
	}
}

// 文件、转发、卡片的宽度跟内容走，不铺满消息列（主人 2026-11-09）；很长的内容也有上限。
func TestCardWidthFollowsContent(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 660, NoEnter: true, Group: true}
	maxW := float32(cardMaxW) * tk.TS
	long := strings.Repeat("很长的文件名", 40) + ".mp4"
	msgs := append(cardMessages(), &Msg{ID: "long", Sender: "己", Name: "己", At: bubbleTestAt, Blocks: []segview.Block{{Kind: segview.File, VideoIndex: 1, Video: segview.Segment{Name: long, Size: 1 << 20, CanDownload: true}}}})
	for _, m := range msgs {
		t.Run(m.ID, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("root")).Width(720).Children(func() { h.Message(c, m, nil, nil) })
			}, 760, 400)
			tt.Frame()
			tt.Frame()
			b, ok := tt.Find("气泡")
			if !ok {
				t.Fatal("没有气泡")
			}
			if b.W > maxW+2*tk.BubblePadV+1 {
				t.Fatalf("气泡宽 %.1f，超过上限 %.1f", b.W, maxW)
			}
			if b.W < float32(cardMinW)*tk.TS-1 {
				t.Fatalf("气泡宽 %.1f，比下限还窄", b.W)
			}
		})
	}
}

func imageDiff(a, b *image.RGBA, r ui.Rect, scale float32) int {
	n := 0
	for y := int(r.Y * scale); y < int((r.Y+r.H)*scale); y++ {
		for x := int(r.X * scale); x < int((r.X+r.W)*scale); x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				n++
			}
		}
	}
	return n
}

// 有缩略图的卡片：上面是图，下面是标题、简介、来源，不把链接写出来；点了打开链接。
func TestCardShowsThumbnailAndOpens(t *testing.T) {
	opened := ""
	h := mbHost(mbReady)
	h.OnLink = func(u string) { opened = u }
	m := &Msg{ID: "c", At: bubbleTestAt, Blocks: []segview.Block{{Kind: segview.CardBlock, Card: segview.Segment{Position: 0, HasHash: true, HasMedia: true,
		Card: &segview.Card{Title: "QQ经典农场", Description: "偷我菜的", Source: "小程序", URL: "https://example.com/x"}}}}}
	tt := mbRender(t, h, m)
	thumb, title := mustFind(t, tt, "卡片图片"), mustFind(t, tt, "QQ经典农场")
	if thumb.H <= 0 || title.Y < thumb.Y+thumb.H {
		t.Fatalf("标题该在图下面：图 %+v 标题 %+v", thumb, title)
	}
	if tt.HasText("https://example.com/x") {
		t.Fatal("不该把链接写出来")
	}
	if err := tt.Click("卡片：QQ经典农场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if opened != "https://example.com/x" {
		t.Fatalf("点了没打开：%q", opened)
	}
}

// 图片上的时间平时不显示，鼠标放到这条消息上才出现，移开又消失。
func TestMediaTimeOnlyOnHover(t *testing.T) {
	h := mbHost(mbReady)
	m := imgMsg("m", false, "", imgSeg(0, 400, 300))
	m.At = bubbleTestAt
	tt := mbRender(t, h, m)
	chip := mustFind(t, tt, "时间牌子")
	bub := mustFind(t, tt, "气泡")
	settle := func() *image.RGBA {
		tt.Advance(time.Second)
		tt.Frame()
		tt.Frame()
		img := tt.Image()
		out := image.NewRGBA(img.Bounds())
		copy(out.Pix, img.Pix)
		return out
	}
	tt.Move(bub.X+bub.W+200, bub.Y+bub.H+200)
	away := settle()
	tt.Move(bub.X+bub.W/2, bub.Y+bub.H/2)
	over := settle()
	if imageDiff(away, over, chip, 1) == 0 {
		t.Fatal("鼠标放上去时间牌子没有变化")
	}
	tt.Move(bub.X+bub.W+200, bub.Y+bub.H+200)
	back := settle()
	if d := imageDiff(away, back, chip, 1); d != 0 {
		t.Fatalf("鼠标移开后时间牌子没有消失：差 %d 像素", d)
	}
}

// 铺满气泡的视频封面，四个角都由气泡圆角裁掉，不留直角。
func TestFlushVideoCornersRounded(t *testing.T) {
	h := mbHost(mbReady)
	seg := imgSeg(0, 400, 300)
	seg.HasCover, seg.Kind = true, "video"
	m := &Msg{ID: "v", Sender: "甲", Name: "甲", At: bubbleTestAt, Blocks: []segview.Block{{Kind: segview.Video, VideoIndex: 0, Video: seg}}}
	tt := mbRender(t, h, m)
	bub, video := mustFind(t, tt, "气泡"), mustFind(t, tt, "视频")
	img := tt.Image()
	page := color.RGBA{240, 240, 240, 255}         // mbRender 的页面背景
	bx, by := int(video.X), int(video.Y+video.H)-1 // 左下角那个像素
	if got := img.RGBAAt(bx, by); got != page {
		t.Fatalf("视频左下角应透出页面背景 %v，实际 %v（气泡 %+v 视频 %+v）", page, got, bub, video)
	}
	if got := img.RGBAAt(int(video.X+video.W)-1, by); got != page {
		t.Fatalf("视频右下角应透出页面背景 %v，实际 %v", page, got)
	}
}

// 状态写进时间那一格以后，撤回、没发出去的气泡和普通气泡一样高，不多出空行。
func TestStatusNoteAddsNoLine(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 660, NoEnter: true}
	height := func(m *Msg) float32 {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("root")).Width(720).Children(func() { h.Message(c, m, nil, nil) })
		}, 760, 400)
		tt.Frame()
		tt.Frame()
		b, ok := tt.Find("气泡")
		if !ok {
			t.Fatal("没有气泡")
		}
		return b.H
	}
	plain := func(state State, notes ...string) *Msg {
		return &Msg{ID: "m", Text: "一句话", At: bubbleTestAt, State: state, Notes: notes}
	}
	base := height(plain(Sent))
	for name, m := range map[string]*Msg{
		"撤回":   plain(Recalled, "已撤回"),
		"没发出去": plain(Failed, "没发出去"),
		"被打断":  plain(Interrupted, "被打断"),
	} {
		if got := height(m); got != base {
			t.Errorf("%s：气泡高 %.1f，普通气泡 %.1f", name, got, base)
		}
	}
}

// LOOK_SHOT 时出一张各种气泡的图，给人看。
func TestBubbleLookShot(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 660, NoEnter: true, Group: true}
	long := strings.Repeat("日本利用压舞萌DX按键将拍击转化为电能。这些按键捕捉来自你拍击的动能。", 6)
	msgs := append([]*Msg{
		{ID: "a", Sender: "甲", Name: "甲", Text: "短的一句", Blocks: segview.TextBlocks("短的一句"), At: bubbleTestAt},
		{ID: "b", Sender: "乙", Name: "乙", Text: long, Blocks: segview.TextBlocks(long), At: bubbleTestAt},
	}, cardMessages()...)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(720).Padding(10).Background(tk.C.ChatBg).Children(func() {
			for i, m := range msgs {
				var prev *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				h.Message(c, m, prev, nil)
			}
		})
	}, 760, 1000)
	tt.Frame()
	tt.Frame()
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "气泡量测.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCardSourceText(t *testing.T) {
	for in, want := range map[string]string{"com.tencent.miniapp_01": "QQ 小程序", "com.tencent.structmsg": "QQ 卡片", "哔哩哔哩": "哔哩哔哩", "": ""} {
		if got := cardSourceText(in); got != want {
			t.Errorf("%q：%q，应为 %q", in, got, want)
		}
	}
}
