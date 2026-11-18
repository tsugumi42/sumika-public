package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
)

// B2、B13：完整装配的补录Submit→入库回调→流水线和图片工人；200条@她仍不进内核、闸门、拦截链。
func TestBackfillNoReplyAndMedia(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	image := pngBytes("编的补录图")
	is.serve("/history", 200, image)
	a := e.openWith(is)
	defer a.Close()
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	clk := fake.NewClock(e.now)
	a.backfill.clock = clk
	if err := st.SetQQCheckedThrough(context.Background(), testAccount, e.now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	peer := &historyPeer{ready: true}
	a.backfill.source = peer
	peer.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		if q.conv.Scene == types.ScenePrivate {
			return []types.Inbound{{Kind: types.InboundMessage, Account: testAccount, Conv: q.conv, PlatformID: "5900", From: types.User{ID: testOwner}, Time: e.now.Add(-2 * time.Hour)}}, nil
		}
		begin := 0
		switch q.anchor {
		case "6099":
			begin = 99
		case "6198":
			begin = 198
		}
		var rows []types.Inbound
		for i := begin; i < min(begin+100, 200); i++ {
			segs := []types.Seg{{Kind: types.SegAt, Target: testAccount}, {Kind: types.SegText, Text: "编的补录"}}
			if i == 0 {
				segs = append(segs, types.Seg{Kind: types.SegImage, URL: imgURL("/history"), File: "编的.jpg"})
			}
			rows = append(rows, types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: q.conv, PlatformID: fmt.Sprint(6000 + i), From: types.User{ID: testOwner}, Time: e.now.Add(-time.Duration(i) * time.Second), Segs: segs})
		}
		if begin == 198 {
			rows = append(rows, types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: q.conv, PlatformID: "5800", From: types.User{ID: testOwner}, Time: e.now.Add(-2 * time.Hour)})
		}
		return rows, nil
	}
	if err := a.backfill.round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.intake.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "补录图下载回填", func() bool { return e.segHash(a, "6000", 2) == sum256(image) })
	var tags, events, gateRows, stateRows, parts int
	for query, dst := range map[string]*int{`SELECT count(*) FROM events WHERE channel='qq'`: &events, `SELECT count(*) FROM events WHERE segments_json LIKE '%"backfill"%'`: &tags, `SELECT count(*) FROM outbound_parts`: &parts} {
		if err := st.Events.QueryRow(query).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Trace.QueryRow(`SELECT count(*) FROM gate_decisions`).Scan(&gateRows); err != nil {
		t.Fatal(err)
	}
	if err := st.Trace.QueryRow(`SELECT count(*) FROM gate_state`).Scan(&stateRows); err != nil {
		t.Fatal(err)
	}
	if events != 200 || tags != 200 || gateRows != 0 || stateRows != 0 || parts != 0 || e.llmCalls() != 0 {
		t.Fatalf("补录触发了回复或热闹：events=%d tags=%d gate=%d state=%d parts=%d llm=%d", events, tags, gateRows, stateRows, parts, e.llmCalls())
	}
	// 陌生人补录图同样排下载；观察权限保持原来的收紧规则。
	stranger := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: "400"}
	in := types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: stranger, PlatformID: "6300", From: types.User{ID: "400"}, Time: e.now, Segs: []types.Seg{{Kind: types.SegImage, URL: imgURL("/history"), File: "陌生人.jpg"}}}
	a.backfill.source = &historyPeer{ready: true, read: func(_ context.Context, q historyCall) ([]types.Inbound, error) { return []types.Inbound{in}, nil }}
	target := backfillTarget{conv: stranger}
	p := a.backfill.source.(*historyPeer)
	p.BeginQueryRound(120)
	if _, err := a.backfill.conversation(context.Background(), target, e.now.Add(-time.Hour).UnixMilli(), e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "陌生人补录图下载", func() bool { return e.segHash(a, "6300", 0) == sum256(image) })
	var observed int
	if err := st.Events.QueryRow(`SELECT count(*) FROM observations o JOIN channel_msgs m ON m.event_seq=o.event_seq WHERE m.platform_id='6300'`).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 0 {
		t.Fatal("陌生人补录被观察")
	}
}

// B16：archive不装，receive开关关闭不装；图片预算用尽只算暂时问不到，不记expired。
func TestBackfillModesAndRefreshBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	t.Cleanup(func() { a.Close() })
	if a.backfill == nil {
		t.Fatal("receive应装补录")
	}
	a.closeBackfill()
	a.cfg.QQ.Backfill.Enabled = false
	a.backfill = nil
	if err := a.openBackfill(); err != nil || a.backfill != nil {
		t.Fatalf("关闭补录仍构造：%v", err)
	}
	a.adapter.BeginQueryRound(0)
	_, err := (qqRefresher{ad: a.adapter, account: testAccount}).Message(context.Background(), backfillPrivate().String(), "7100")
	if !errors.Is(err, mediafetch.ErrRefreshUnavailable) {
		t.Fatalf("预算用完不能记图片过期：%v", err)
	}
	a.adapter.EndQueryRound()
	a.Close()
	opt := archiveFixture(t, "补录archive")
	archive, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if archive.backfill != nil {
		t.Fatal("archive不应装补录")
	}
}

// 新账号无导入水位时，从旧历史起点补；假墙钟晚30天也不附加72小时或接管时刻下限。
func TestBackfillNoTakeoverOrAgeFloor(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	c := backfillPrivate()
	old := e.msg(c, "7200", 46*time.Hour)
	e.save(old)
	if _, err := e.st.Events.Exec(`DELETE FROM qq_sync`); err != nil {
		t.Fatal(err)
	}
	oldest, err := e.st.QQCheckedThrough(context.Background(), testAccount, e.clk.Now().UnixMilli())
	if err != nil || oldest != old.Time.UnixMilli() {
		t.Fatalf("无水位应取最早历史：%d %v", oldest, err)
	}
	// 用导入水位，墙钟跳过30天，仍补到46小时前的同一条。
	e.watermark(old.Time)
	e.clk.Advance(30 * 24 * time.Hour)
	missing := e.msg(c, "7201", 31*24*time.Hour)
	boundary := old
	boundary.PlatformID = "7199"
	boundary.Time = old.Time.Add(-time.Minute)
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		return []types.Inbound{old, missing, boundary}, nil
	}
	e.run()
	if e.n() != 3 {
		t.Fatalf("接管时刻或年龄下限挡住旧空档：%d", e.n())
	}
	if len(e.gaps()) != 0 {
		t.Fatalf("不应有pre-takeover缺口：%+v", e.gaps())
	}
}
