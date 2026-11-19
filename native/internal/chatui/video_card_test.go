package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func videoSeg() segview.Segment {
	return segview.Segment{Kind: "video", Width: 640, Height: 360, DurationMS: 48000, Size: 5643691, CanDownload: true, HasCover: true}
}

func videoMsg(self bool, seg segview.Segment) *Msg {
	return &Msg{ID: "v", Self: self, Name: "甲", Sender: "甲", At: time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local),
		Blocks: []segview.Block{{Kind: segview.Video, VideoIndex: 1, Video: seg}}}
}

type videoMedia struct{ dl, cover MediaResult }

func (v videoMedia) Image(string, int, bool, int) MediaResult { return v.cover }
func (v videoMedia) Face(string) MediaResult                  { return MediaResult{} }
func (v videoMedia) Download(string, int) MediaResult         { return v.dl }

func videoRender(t *testing.T, dl MediaResult, group bool) (*ui.Tester, Host, *[3]string) {
	t.Helper()
	var got [3]string
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Group: group, Media: videoMedia{dl: dl, cover: mbReady},
		OnDownload:       func(id string, pos int) { got[0] = id },
		OnCancelDownload: func(id string, pos int) { got[1] = id },
		OnOpenMedia:      func(id string, pos int) { got[2] = id },
	}
	m := videoMsg(false, videoSeg())
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("page")).Padding(20).Children(func() { h.Message(c, m, nil, nil) })
	}, 700, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt, h, &got
}

// 视频卡：封面铺满气泡，中间圆按钮、左上角“时长 · 大小”牌子、右下时间牌子；没有“视频”标题行和通栏按钮。
func TestVideoCardLayoutAndStates(t *testing.T) {
	for _, c := range []struct {
		name  string
		dl    MediaResult
		label string
		chip  string
	}{
		{"没下载", MediaResult{State: MediaMissing, Progress: -1, Description: "点一下下载"}, "下载视频", "0:48 · 5.4 MB"},
		{"下载中", MediaResult{State: MediaDownloading, Progress: 0.5}, "取消下载视频", "2.7 MB / 5.4 MB"},
		{"下载好了", MediaResult{State: MediaDownloaded, Progress: 1}, "播放视频", "0:48 · 5.4 MB"},
		{"失败", MediaResult{State: MediaFailed, Description: "编造失败原因"}, "重试下载视频", "下载失败"},
	} {
		tt, _, _ := videoRender(t, c.dl, false)
		bub := mustFind(t, tt, "气泡")
		card := mustFind(t, tt, "视频")
		if !mbNear(card.X, bub.X) || !mbNear(card.Y, bub.Y) || !mbNear(card.W, bub.W) || !mbNear(card.H, bub.H) {
			t.Fatalf("%s：视频卡 %+v 应贴满气泡 %+v", c.name, card, bub)
		}
		btn := mustFind(t, tt, c.label)
		if btn.W != 56 || btn.H != 56 {
			t.Fatalf("%s：圆按钮 %+v 应是 56×56，不是通栏按钮", c.name, btn)
		}
		if !mbNear(btn.X+btn.W/2, card.X+card.W/2) || !mbNear(btn.Y+btn.H/2, card.Y+card.H/2) {
			t.Fatalf("%s：圆按钮 %+v 不在卡片 %+v 中间", c.name, btn, card)
		}
		meta := mustFind(t, tt, "视频信息")
		if meta.X > card.X+card.W/3 || meta.Y > card.Y+card.H/3 {
			t.Fatalf("%s：信息牌子 %+v 不在左上角", c.name, meta)
		}
		if !tt.HasText(c.chip) {
			t.Fatalf("%s：牌子文字应是 %q：%v", c.name, c.chip, tt.Texts())
		}
		chip := mustFind(t, tt, "时间牌子")
		if chip.X < card.X+card.W/2 || chip.Y < card.Y+card.H/2 {
			t.Fatalf("%s：时间牌子 %+v 不在右下", c.name, chip)
		}
		all := strings.Join(tt.Texts(), "|")
		for _, bad := range []string{"字节", "点一下下载", "ready"} {
			if strings.Contains(all, bad) {
				t.Fatalf("%s：界面上还有 %q：%s", c.name, bad, all)
			}
		}
	}
}

// 三个状态的按钮各走各的宿主动作。
func TestVideoCardActions(t *testing.T) {
	for i, c := range []struct {
		dl    MediaResult
		label string
	}{
		{MediaResult{State: MediaMissing, Progress: -1}, "下载视频"},
		{MediaResult{State: MediaDownloading, Progress: -1}, "取消下载视频"},
		{MediaResult{State: MediaDownloaded, Progress: 1}, "播放视频"},
	} {
		tt, _, got := videoRender(t, c.dl, false)
		if err := tt.Click(c.label); err != nil {
			t.Fatal(err)
		}
		for j := range got {
			if (got[j] == "v") != (i == j) {
				t.Fatalf("按钮 %q 触发的动作不对：%v", c.label, *got)
			}
		}
	}
}

// 群聊里名字在上面，封面接在名字下面铺满宽度。
func TestVideoCardWithGroupName(t *testing.T) {
	tt, h, _ := videoRender(t, MediaResult{State: MediaMissing, Progress: -1}, true)
	bub, card := mustFind(t, tt, "气泡"), mustFind(t, tt, "视频")
	if !mbNear(card.X, bub.X) || !mbNear(card.W, bub.W) || !mbNear(card.Y+card.H, bub.Y+bub.H) {
		t.Fatalf("封面 %+v 应贴着气泡 %+v 的左右下", card, bub)
	}
	if card.Y < bub.Y+h.T.NameLine {
		t.Fatalf("封面顶 %.1f 应在名字区下面", card.Y)
	}
}

// 文件条：左边圆形图标，右边文件名和人能读的大小；点图标按状态走动作。
func TestFileBar(t *testing.T) {
	var got string
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: videoMedia{dl: MediaResult{State: MediaMissing, Progress: -1, Description: "点一下下载"}},
		OnDownload: func(id string, pos int) { got = id }}
	m := &Msg{ID: "f", At: time.Now(), Blocks: []segview.Block{{Kind: segview.File, VideoIndex: 1, Video: segview.Segment{Name: "资料.zip", Size: 2345678, CanDownload: true}}}}
	tt := ui.NewTester(func(c *ui.Context) { ui.Column(c.Key("p")).Padding(20).Children(func() { h.Message(c, m, nil, nil) }) }, 700, 400)
	tt.Frame()
	tt.Frame()
	btn := mustFind(t, tt, "下载")
	name := mustFind(t, tt, "资料.zip")
	if btn.X+btn.W > name.X || btn.W != 44 {
		t.Fatalf("图标 %+v 应在文件名 %+v 左边", btn, name)
	}
	if !tt.HasText("2.2 MB · 点一下下载") {
		t.Fatalf("大小应是人能读的单位：%v", tt.Texts())
	}
	if err := tt.Click("下载"); err != nil || got != "f" {
		t.Fatalf("点图标没有触发下载：%v %q", err, got)
	}
}
