package app

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
)

// 实时、补录都经过真实入库回调；声明 60 MiB 能抓住旧的 50 MiB 限额。
func TestVideoStoredPaths(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	content := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	is.serve("/video", 200, content)
	a := e.openWith(is)
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, tc := range []struct {
		conv types.Conversation
		kind string
	}{
		{types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}, types.SegVideo},
		{backfillGroup(), types.SegVideo},
		{types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}, types.SegFile},
		{backfillGroup(), types.SegFile},
	} {
		conv := tc.conv
		pid := []string{"8400", "8401", "8402", "8403"}[i]
		in := types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: conv, PlatformID: pid, From: types.User{ID: testOwner}, Time: e.now,
			Segs: []types.Seg{{Kind: tc.kind, URL: imgURL("/video"), File: "编的视频.mp4", Size: 60 << 20}}}
		ticket, err := a.intake.Submit(ctx, in, channel.Options{Backfill: i%2 == 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ticket.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "实时或补录视频入库", func() bool { return e.segHash(a, pid, 0) == sum256(content) })
	}
	if is.hits("/video") != 4 || !e.mediaOK(a, sum256(content), content) {
		t.Fatal("视频没有下载进同一媒体库")
	}
	desk := a.Desktop()
	defer desk.Close()
	if _, ok := any(desk.Reader).(api.VideoDownloadActions); !ok {
		t.Fatal("桌面未提供可选视频动作")
	}
}
