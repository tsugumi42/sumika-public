package chatui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

var stateCodes = []string{"ready", "loading", "fetching", "failed", "expired", "unavailable", "not_receiving", "downloading", "timeout", "connection_lost", "too_large", "refused"}

func fwdMsg(f *segview.Forward) *Msg {
	return &Msg{ID: "f", Blocks: []segview.Block{{Kind: segview.ForwardBlock, Card: segview.Segment{Position: 2, Forward: f}}}}
}

func fwdRender(t *testing.T, f *segview.Forward) (*ui.Tester, Host, *string) {
	t.Helper()
	opened := new(string)
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, OnForward: func(id string, pos int) { *opened = id }}
	m := fwdMsg(f)
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 700, 500)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt, h, opened
}

func noStateCodes(t *testing.T, tt *ui.Tester, when string) {
	t.Helper()
	all := strings.ToLower(strings.Join(tt.Texts(), "|"))
	for _, code := range stateCodes {
		if strings.Contains(all, code) {
			t.Fatalf("%s：界面上出现了状态码 %q：%v", when, code, tt.Texts())
		}
	}
}

// 卡片铺满气泡内宽，文字左边缘贴着卡片内边距，没有给缩略图留的空白；整张卡能点。
func TestForwardCardFillsAndClicks(t *testing.T) {
	f := &segview.Forward{Title: "甲和乙的聊天记录", Lines: [3]string{"甲：早", "乙：[图片]", "甲：[合并转发]"}, Count: 7, State: "ready"}
	tt, h, opened := fwdRender(t, f)
	bub := mustFind(t, tt, "气泡")
	card := mustFind(t, tt, "查看转发消息")
	pv := h.T.BubblePadV
	if !mbNear(card.X, bub.X+pv) || !mbNear(card.W, bub.W-2*pv) {
		t.Fatalf("卡片 %+v 应铺满气泡 %+v 的内宽（边距 %.1f）", card, bub, pv)
	}
	title := mustFind(t, tt, "甲和乙的聊天记录")
	if title.X > card.X+h.T.S2+1 {
		t.Fatalf("标题左边 %.1f 离卡片左边 %.1f 超过内边距 %.1f，左边空了一块", title.X, card.X, h.T.S2)
	}
	for _, want := range []string{"甲：早", "乙：[图片]", "甲：[合并转发]", "查看 7 条转发消息"} {
		if !tt.HasText(want) {
			t.Fatalf("缺 %q：%v", want, tt.Texts())
		}
	}
	noStateCodes(t, tt, "就绪")
	if err := tt.Click("查看 7 条转发消息"); err != nil {
		t.Fatal(err)
	}
	if *opened != "f" {
		t.Fatal("点底部那一行没有打开详情")
	}
	*opened = ""
	tt.ClickAt(card.X+card.W/2, card.Y+4)
	if *opened != "f" {
		t.Fatal("点卡片上部没有打开详情")
	}
}

// 加载中、取不到的文案是人话；空转发、取不到内容也合理；任何状态下都没有状态码。
func TestForwardCardStates(t *testing.T) {
	for _, c := range []struct {
		name string
		f    *segview.Forward
		want []string
	}{
		{"加载中", &segview.Forward{Title: "聊天记录", Count: 7, State: "fetching"}, []string{"正在加载转发内容"}},
		{"没接收", &segview.Forward{Title: "聊天记录", Count: 7, State: "not_receiving"}, []string{"转发内容取不到", "没在接收 QQ"}},
		{"过期", &segview.Forward{Title: "聊天记录", Count: 7, State: "expired"}, []string{"转发内容取不到", "没有这条记录"}},
		{"超时", &segview.Forward{Title: "聊天记录", State: "timeout"}, []string{"转发内容取不到", "没有及时回话", "查看转发消息"}},
		{"空转发", &segview.Forward{Title: "聊天记录", State: "ready"}, []string{"没有可显示的消息", "查看转发消息"}},
		{"缺几条", &segview.Forward{Title: "聊天记录", Lines: [3]string{"甲：嗯"}, Count: 5, Missing: 2, State: "ready"}, []string{"甲：嗯", "查看 5 条转发消息（2 条没取下来）"}},
		{"没有数据", nil, []string{"聊天记录", "查看转发消息"}},
	} {
		tt, _, _ := fwdRender(t, c.f)
		for _, want := range c.want {
			found := false
			for _, tx := range tt.Texts() {
				if strings.Contains(tx, want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s：缺 %q：%v", c.name, want, tt.Texts())
			}
		}
		noStateCodes(t, tt, c.name)
	}
}

// 同一族的其他卡片（链接卡、视频、文件）不把内部状态码原样露出来。
func TestCardsDoNotLeakStateCodes(t *testing.T) {
	media := &blockMedia{state: MediaResult{State: MediaMissing, Progress: -1}}
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: media}
	m := &Msg{ID: "x", Blocks: []segview.Block{
		{Kind: segview.CardBlock, Card: segview.Segment{Card: &segview.Card{Title: "链接标题", Description: "描述", Source: "来源"}}},
		{Kind: segview.Video, VideoIndex: 1, Video: segview.Segment{FetchState: "ready", MediaState: "expired", CanDownload: true, DurationMS: 48000, Size: 5643691}},
		{Kind: segview.File, VideoIndex: 2, Video: segview.Segment{Name: "资料.zip", FetchState: "fetching", MediaState: "loading", CanDownload: true}},
	}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 700, 900)
	tt.Frame()
	tt.Frame()
	noStateCodes(t, tt, "链接卡、视频、文件")
}
