package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
)

type alignmentRefresh struct {
	segs []types.Seg
	err  error
}

func (r alignmentRefresh) Message(context.Context, string, string) ([]types.Seg, error) {
	return r.segs, r.err
}

// C04：状态投影消费实际下载器结局；暂时取不到、能力不足和预算不足不记过期。
func TestAlignmentExpiryEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                           string
		newStatus                      int
		refreshErr                     error
		downloaded, expired, unchecked int
	}{
		{name: "刷新成功", newStatus: http.StatusOK, downloaded: 1},
		{name: "刷新后仍过期", newStatus: http.StatusGone, expired: 1},
		{name: "刷新超时", refreshErr: context.DeadlineExceeded, unchecked: 1},
		{name: "刷新不支持", refreshErr: types.ErrUnsupported, unchecked: 1},
		{name: "共享预算不足", refreshErr: types.ErrQueryBudget, unchecked: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			server := newImgServer(t)
			server.serve("/alignment-old", http.StatusForbidden, nil)
			server.serve("/alignment-new", tc.newStatus, pngBytes("编造刷新图片"))
			a := e.openWith(server)
			defer a.Close()
			a.media.Close()
			st, _ := a.set.For("qq")
			f, err := mediafetch.New(mediafetch.Options{Store: st, Data: a.paths.Root, Platform: "qq", Downloader: server.downloader(), Refresher: alignmentRefresh{segs: []types.Seg{{Kind: types.SegImage, URL: imgURL("/alignment-new"), File: "编造.jpg"}}, err: tc.refreshErr}, Logf: e.logf, StatsEvery: -1})
			if err != nil {
				t.Fatal(err)
			}
			a.media = f
			a.backfill.media.media = f
			a.backfill.media.forwards = nil
			f.SetBackfillNotify(st.NotifyBackfillMedia)
			f.Start()
			b := a.backfill
			b.owner = ""
			b.groups = []string{testGroup}
			peer := &historyPeer{ready: true}
			b.source = peer
			c := backfillGroup()
			peer.read = func(context.Context, historyCall) ([]types.Inbound, error) {
				// 这项只测新消息的换址终局；先让手动补扫读完旧集合，避免它再捡起同一条新消息。
				waitFor(t, "手动补扫旧集合结束", func() bool { return !f.BackfillActivity().Sweeping })
				return []types.Inbound{{Kind: types.InboundMessage, Account: testAccount, Conv: c, PlatformID: "1951", From: types.User{ID: testOwner}, Time: e.now, Segs: []types.Seg{{Kind: types.SegImage, URL: imgURL("/alignment-old"), File: "编造.jpg"}}}, {Kind: types.InboundMessage, Account: testAccount, Conv: c, PlatformID: "1950", From: types.User{ID: testOwner}, Time: e.now.Add(-2 * time.Hour)}}, nil
			}
			if err = st.SetQQCheckedThrough(context.Background(), testAccount, e.now.Add(-time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); b.media.wg.Wait() }()
			if err = b.executeAccount(ctx, "manual", nil); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "媒体真实终局", func() bool {
				v, err := a.alignment(context.Background())
				if err != nil || v.LastAttempt == nil {
					return false
				}
				m := v.LastAttempt.Media
				return m.State != "waiting" && m.Downloaded+m.Expired+m.Unchecked+m.Failed+m.Skipped+m.AwaitingManual == 1
			})
			view, err := a.alignment(ctx)
			if err != nil || view.Media.Downloaded != tc.downloaded || view.Media.Expired != tc.expired || view.Media.Unchecked != tc.unchecked {
				t.Fatal("刷新终局不符合证据", view.Media, err)
			}
			var original string
			if err = st.Events.QueryRow(`SELECT json_extract(e.segments_json,'$.segs[0].url') FROM events e JOIN channel_msgs m ON m.event_seq=e.seq WHERE m.platform_id='1951'`).Scan(&original); err != nil || original != imgURL("/alignment-old") {
				t.Fatal("原事件地址被改写或移除", original, err)
			}
			var messages int
			if err = st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation=? AND kind='message'`, c.String()).Scan(&messages); err != nil || messages != 1 {
				t.Fatal("刷新改了原消息集合", messages, err)
			}
			tmp, err := os.ReadDir(filepath.Join(a.paths.Root, "go", "media", "qq", ".tmp"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(tmp) != 0 {
				t.Fatal("刷新终局留下临时文件")
			}
			if e.llmCalls() != 0 {
				t.Fatal("媒体恢复触发模型")
			}
			if server.hits("/alignment-old") != 1 {
				t.Fatal("初始地址请求数", server.hits("/alignment-old"))
			}
			if tc.refreshErr != nil && server.hits("/alignment-new") != 0 {
				t.Fatal("刷新失败仍下载新地址")
			}
		})
	}
}
