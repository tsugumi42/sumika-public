package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// videoWorkbench 在新视图里放一条带视频的消息，下载状态由假后台推送。
func videoWorkbench(t *testing.T, state api.VideoDownloadProgress) (*app, *uiQueue, *transferTestReader, *ui.Tester) {
	t.Helper()
	v := view("vid", "human", "编造发送者", "", t0)
	v.Ref.Conversation = api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:90002"}
	ref := &api.VideoDownloadRef{Source: v.Ref, Index: 0}
	v.Segments = []api.DisplaySegment{{Kind: "video", Name: "视频", Download: ref, Width: 480, Height: 270, DurationMS: 48000, Size: 5643691}}
	r := &transferTestReader{message: v, progress: state}
	a, q := imgApp(t, &imgNet{})
	a.reader = r
	a.sel = "c"
	a.msgs = []*msg{msgFromView(v.Ref.Conversation, v, charOf)}
	a.list = a.newList()
	t.Cleanup(a.downloads.reset)
	tt := ui.NewTester(func(c *ui.Context) {
		a.downloads.beginFrame()
		defer a.downloads.endFrame()
		a.applyLook(c)
		a.workbenchChat(c)
	}, 900, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	return a, q, r, tt
}

func (r *transferTestReader) subCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

func cardTexts(tt *ui.Tester) string {
	var out []string
	for _, s := range tt.Texts() {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "|")
}

// 鼠标悬停在视频卡上连续很多帧：下载状态文字不变，后台只订阅一次（不是每隔几帧重建一次、状态回到默认）。
func TestVideoHoverDoesNotResetDownloadState(t *testing.T) {
	for _, st := range []api.VideoDownloadProgress{
		{State: api.VideoAwaitingManual},
		{State: api.VideoManualDownloading, Received: 4096, Total: 8192},
		{State: api.VideoDownloadFailed, Error: "编造失败原因"},
	} {
		a, q, r, tt := videoWorkbench(t, st)
		for i := 0; i < 6; i++ { // 让订阅的第一份状态到界面
			q.drain()
			tt.Frame()
		}
		card, ok := tt.Find("视频")
		if !ok {
			t.Fatalf("没找到视频卡：%v", tt.Texts())
		}
		before := cardTexts(tt)
		subs := r.subCount()
		if subs != 1 {
			t.Fatalf("状态 %v：订阅 %d 次，应为 1", st.State, subs)
		}
		for i := 0; i < 60; i++ {
			tt.Move(card.X+card.W/2+float32(i%3), card.Y+card.H/2)
			q.drain()
			tt.Frame()
			if got := cardTexts(tt); got != before {
				t.Fatalf("状态 %v：悬停第 %d 帧文字变了\n之前 %s\n现在 %s", st.State, i, before, got)
			}
			if n := r.subCount(); n != 1 {
				t.Fatalf("状态 %v：悬停第 %d 帧订阅变成 %d 次", st.State, i, n)
			}
		}
		_ = a
	}
}
