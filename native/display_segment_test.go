package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

func displayTestMessage(key api.ConversationKey, id string, segments ...api.DisplaySegment) *msg {
	ref := api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: id}}
	return msgFromView(key, api.MessageView{Ref: ref, Kind: "message", ActorKind: "human", DisplayName: "编造的发送者", CreatedAt: t0, Segments: segments}, charOf)
}
func displayTestEachPlace(t *testing.T, fn func(*testing.T, api.ConversationKey)) {
	t.Helper()
	for _, conv := range []string{"console:a", "home:room", "qq:private:10003:10004", "qq:group:10005"} {
		t.Run(conv, func(t *testing.T) {
			key := demoKey(conv)
			if conv == "console:a" {
				key = api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}
			}
			fn(t, key)
		})
	}
}

func TestDisplaySegmentTextAndPlaceholders(t *testing.T) {
	cases := []struct {
		name    string
		segment api.DisplaySegment
		want    string
	}{
		{"text", api.DisplaySegment{Kind: "text", Text: "编造正文"}, "编造正文"},
		{"at", api.DisplaySegment{Kind: "at", Target: "all"}, "@全体成员"},
		{"face fallback", api.DisplaySegment{Kind: "face", Ref: "invalid"}, "[表情invalid]"},
		{"superface fallback", api.DisplaySegment{Kind: "superface", Ref: "invalid"}, "[超级表情invalid]"},
		{"voice", api.DisplaySegment{Kind: "voice"}, "[语音]"},
		{"card missing", api.DisplaySegment{Kind: "card"}, "[卡片]"},
		{"other", api.DisplaySegment{Kind: "other"}, "[其他消息]"},
		{"unknown", api.DisplaySegment{Kind: "invented-kind"}, "[invented-kind]"},
		{"emoji number only", api.DisplaySegment{Kind: "emoji", Ref: "invented", Summary: "[挥手]"}, "[挥手]"},
		{"image absent", api.DisplaySegment{Kind: "image"}, "[尚无本地媒体]"},
		{"emoji absent", api.DisplaySegment{Kind: "emoji"}, "[尚无本地媒体]"},
		{"image empty hash", api.DisplaySegment{Kind: "image", Media: &api.MediaRef{}, MediaState: "expired"}, "[图片没下载下来]"},
		{"emoji empty hash with number", api.DisplaySegment{Kind: "emoji", Ref: "invented", Summary: "[挥手]", Media: &api.MediaRef{}, MediaState: "expired"}, "[图片没下载下来]"},
		{"image too big", api.DisplaySegment{Kind: "image", MediaState: "too_big"}, "[图片太大，没有下载]"},
		{"image expired", api.DisplaySegment{Kind: "image", MediaState: "expired"}, "[图片已过期]"},
		{"image pending", api.DisplaySegment{Kind: "image", MediaState: "pending"}, "[图片正在下载]"},
	}
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := displayTestMessage(key, tc.name, tc.segment)
				a := chatApp([]*msg{m}, key.Scene == "group")
				tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
				if !tt.HasText(tc.want) {
					t.Fatalf("缺少 %q：%v", tc.want, tt.Texts())
				}
			})
		}
	})
}

func TestDisplaySegmentMediaOrderAndKeys(t *testing.T) {
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		source := api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "media"}}
		for _, kind := range []string{"image", "emoji"} {
			label := "图片"
			if kind == "emoji" {
				label = "表情包"
			}
			for _, width := range []int{821, 845} {
				t.Run(kind+"/"+strconv.Itoa(width), func(t *testing.T) {
					ref := &api.MediaRef{Source: source, Hash: "invented-" + kind}
					m := displayTestMessage(key, "media", api.DisplaySegment{Kind: "text", Text: "图片之前"}, api.DisplaySegment{Kind: kind, Media: ref, Width: 240, Height: 120}, api.DisplaySegment{Kind: "text", Text: "图片之后"})
					n := &imgNet{orig: map[string][2]int{ref.Hash: {240, 120}}}
					a, q := imgApp(t, n, m)
					a.group = key.Scene == "group"
					tt := testerOf(width, 700, func(c *ui.Context) { a.chatView(c) })
					before, ok1 := tt.Find("图片之前")
					picture, ok2 := tt.Find(label)
					after, ok3 := tt.Find("图片之后")
					if !ok1 || !ok2 || !ok3 || picture.W <= 0 || picture.H <= 0 || picture.X < 0 || picture.X+picture.W > float32(width) || picture.Y < before.Y+before.H || after.Y < picture.Y+picture.H {
						t.Fatalf("图文顺序或边界错误：%+v %+v %+v 文本=%v", before, picture, after, tt.Texts())
					}
					settleImgs(a, q, tt)
					if len(n.tiers(ref.Hash)) == 0 {
						t.Fatalf("没有按媒体键 %q 读取图片", ref.Hash)
					}
					if got, ok := tt.Find(label); !ok || got != picture {
						t.Fatalf("图片到达后布局改变：%+v → %+v", picture, got)
					}
				})
			}
		}
	})
}

func TestDisplaySegmentSuperfaceCopy(t *testing.T) {
	key := demoKey("qq:private:10005")
	m := displayTestMessage(key, "face", api.DisplaySegment{Kind: "text", Text: "前"}, api.DisplaySegment{Kind: "superface", Ref: "invalid"}, api.DisplaySegment{Kind: "text", Text: "后"})
	a, _ := faceApp(t, &fakeNet{}, m)
	a.fs().table = testTable
	var rc ui.Rect
	drawCheck = func(r ui.Rect, _ []ui.Span, _ []ui.TextObject) { rc = r }
	defer func() { drawCheck = nil }()
	tt := testerOf(500, 300, bubbleView(a, m))
	mid := rc.Y + rc.H/2
	tt.Press(rc.X-2, mid)
	tt.Move(rc.X+rc.W/2, mid)
	tt.Move(rc.X+rc.W+3, mid)
	tt.Release(rc.X+rc.W+3, mid)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	if got := clipText(tt); got != "前[超级表情invalid]后" {
		t.Fatalf("复制超级表情：%q", got)
	}
}

func TestDisplaySegmentVideoFileAndCard(t *testing.T) {
	longName := strings.Repeat("编造文件名称", 18) + ".txt"
	cases := []struct {
		name    string
		segment api.DisplaySegment
		label   string
	}{
		{"video", api.DisplaySegment{Kind: "video", Media: &api.MediaRef{Hash: "invented-video"}, DurationMS: 12500}, "播放视频"},
		{"file", api.DisplaySegment{Kind: "file", Name: longName, Size: 2048}, "下载文件：" + longName},
		{"card", api.DisplaySegment{Kind: "card", Card: &api.CardView{Title: "编造卡片标题", Description: "编造卡片简介"}}, "编造卡片标题"},
	}
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := displayTestMessage(key, tc.name, tc.segment)
				a := chatApp([]*msg{m}, key.Scene == "group")
				tt := testerOf(845, 700, func(c *ui.Context) { a.chatView(c) })
				r, ok := tt.Find(tc.label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 845 {
					t.Fatalf("组件 %q 的边界不对：%+v，文本=%v", tc.label, r, tt.Texts())
				}
				if tc.name == "video" && !tt.HasText("0:13") {
					t.Fatalf("视频时长缺失：%v", tt.Texts())
				}
			})
		}
	})
}

func TestDisplaySegmentForwardNode(t *testing.T) {
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		m := displayTestMessage(key, "forward", api.DisplaySegment{Kind: "forward", Ref: "invented-forward", Forward: &api.ForwardBrief{Title: "编造聊天记录", Count: 1, State: "ready"}})
		r := &forwardUIReader{views: map[string]api.ForwardView{
			"invented-forward": {ID: "invented-forward", State: "ready", Nodes: []api.ForwardNode{{UID: "100", Name: "编造节点作者", At: t0, Segments: []api.DisplaySegment{{Kind: "reply", Summary: "编造节点引用"}, {Kind: "text", Text: "编造节点正文"}, {Kind: "image", MediaState: "expired"}}}}},
		}}
		posts := make(chan func(), 10)
		a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
		t.Cleanup(a.stopForward)
		tt := testerOf(900, 800, func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() { a.forwardCard(c, m, m.segs[0]) })
			a.forwardWindow(c)
		})
		click(t, tt, "编造聊天记录")
		select {
		case fn := <-posts:
			fn()
		case <-time.After(uiWaitLimit):
			t.Fatal("转发节点没有返回")
		}
		tt.Frame()
		tt.Advance(time.Second)
		for _, text := range []string{"编造节点引用", "编造节点正文", "[图片已过期]"} {
			if !tt.HasText(text) {
				t.Fatalf("转发节点缺少 %q：%v", text, tt.Texts())
			}
		}
	})
}

func TestDisplaySegmentQuoteAcrossPlaces(t *testing.T) {
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		ref := api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "reply"}}
		original := api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "original"}}
		v := api.MessageView{Ref: ref, ActorKind: "human", DisplayName: "编造发送者", CreatedAt: t0, Segments: []api.DisplaySegment{{Kind: "reply", Summary: "不应重复显示的引用段"}, {Kind: "text", Text: "编造回复正文"}}, ReplyTo: &original, Reply: api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{DisplayName: "编造原作者", Text: "编造引用正文"}}}
		m := msgFromView(key, v, charOf)
		a := chatApp([]*msg{m}, key.Scene == "group")
		tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
		quote, ok1 := tt.Find("编造引用正文")
		body, ok2 := tt.Find("编造回复正文")
		if !ok1 || !ok2 || quote.Y >= body.Y || tt.HasText("不应重复显示的引用段") {
			t.Fatalf("引用和正文布局错误：%+v %+v %v", quote, body, tt.Texts())
		}
	})
}

func TestDisplaySegmentQQQuotePreview(t *testing.T) {
	for _, conv := range []string{"qq:private:10003:10004", "qq:group:10005"} {
		t.Run(conv, func(t *testing.T) {
			key := demoKey(conv)
			ref := api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "reply"}}
			original := api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "original"}}
			segments := []api.DisplaySegment{
				{Kind: "text", Text: "前"}, {Kind: "voice"},
				{Kind: "superface", Ref: "333"}, {Kind: "emoji", Ref: "invented", Summary: "[挥手]"},
				{Kind: "file", Name: "编造.txt", Size: 2048}, {Kind: "text", Text: "后"},
			}
			v := api.MessageView{Ref: ref, ActorKind: "human", DisplayName: "编造发送者", CreatedAt: t0,
				Segments: []api.DisplaySegment{{Kind: "reply"}, {Kind: "text", Text: "编造回复"}}, ReplyTo: &original,
				Reply: api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{DisplayName: "编造原作者", Text: "错误的回退文字", Segments: segments}}}
			m := msgFromView(key, v, charOf)
			a := chatApp([]*msg{m}, key.Scene == "group")
			tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
			want := "前[语音][超级表情333][图片][文件：编造.txt · 2.0 KB]后"
			if m.quote == nil || m.quote.text != want || !tt.HasText(want) {
				t.Fatalf("引用预览不对：%+v，界面=%v", m.quote, tt.Texts())
			}
		})
	}
}

func TestDisplaySegmentPlaceholderCopy(t *testing.T) {
	cases := []struct {
		name    string
		segment api.DisplaySegment
		want    string
	}{
		{"voice", api.DisplaySegment{Kind: "voice"}, "[语音]"},
		{"image", api.DisplaySegment{Kind: "image"}, "[尚无本地媒体]"},
		{"emoji", api.DisplaySegment{Kind: "emoji", MediaState: "expired"}, "[图片已过期]"},
	}
	displayTestEachPlace(t, func(t *testing.T, key api.ConversationKey) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := displayTestMessage(key, "copy", tc.segment)
				a := chatApp([]*msg{m}, key.Scene == "group")
				tt := testerOf(500, 300, bubbleView(a, m))
				r, ok := tt.Find(tc.want)
				if !ok || r.W <= 0 || r.H <= 0 {
					t.Fatalf("找不到占位 %q：%v", tc.want, tt.Texts())
				}
				y := r.Y + r.H/2
				tt.Press(r.X-2, y)
				tt.Move(r.X+r.W/2, y)
				tt.Move(r.X+r.W+3, y)
				tt.Release(r.X+r.W+3, y)
				tt.Frame()
				tt.Key(ui.Cmd, ui.KeyC)
				if got := clipText(tt); got != tc.want {
					t.Fatalf("复制占位得到 %q，应为 %q", got, tc.want)
				}
			})
		}
	})
}
