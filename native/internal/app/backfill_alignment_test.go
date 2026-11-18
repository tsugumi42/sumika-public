package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
	"sumika-native/internal/store"
)

// C01：实际 App.Run 核验假 WebSocket；未核验不恢复，重复 connected 不开第二轮。
func TestAlignmentConnectionLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	var history atomic.Int32
	e.peer.onAction = func(c *websocket.Conn, data []byte) {
		var req struct {
			Action string `json:"action"`
			Echo   any    `json:"echo"`
		}
		if json.Unmarshal(data, &req) != nil {
			return
		}
		if req.Action == "get_login_info" {
			<-gate
		}
		if req.Action != "get_group_msg_history" && req.Action != "get_friend_msg_history" {
			answerInfo(c, data)
			return
		}
		history.Add(1)
		scene, field, id := "group", "group_id", testGroup
		if req.Action == "get_friend_msg_history" {
			scene, field, id = "private", "user_id", testOwner
		}
		raw := fmt.Sprintf(`{"message_type":%q,%q:%s,"self_id":%s,"user_id":%s,"message_id":"1501","time":%d,"sender":{"user_id":%s,"nickname":"编的甲"},"message":[{"type":"text","data":{"text":"编造补录"}}]}`, scene, field, id, testAccount, testOwner, e.now.Add(-2*time.Hour).Unix(), testOwner)
		var message any
		_ = json.Unmarshal([]byte(raw), &message)
		reply, _ := json.Marshal(map[string]any{"status": "ok", "retcode": 0, "echo": req.Echo, "data": map[string]any{"messages": []any{message}}})
		_ = c.Write(context.Background(), websocket.MessageText, reply)
	}
	images := newImgServer(t)
	images.serve("/ready-sweep", http.StatusOK, pngBytes("编造连接补扫"))
	a := e.openWith(images)
	defer a.Close()
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AppendInbound(store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: backfillGroup().String(), ActorKind: "human", ActorID: "qq:" + testOwner, Kind: "message", CreatedAt: e.now.Add(-time.Hour).UnixMilli(), Segments: map[string]any{"segs": []types.Seg{{Kind: types.SegImage, URL: imgURL("/ready-sweep")}}}}, PlatformID: "1500", Observers: []string{testChar}})
	if err != nil {
		t.Fatal(err)
	}
	a.backfill.owner = ""
	clock := fake.NewClock(e.now)
	a.backfill.clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	conn := e.peer.accept(t)
	e.peer.send(t, conn, fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":%s,"time":%d}`, testAccount, e.now.Unix()))
	if history.Load() != 0 || images.hits("/ready-sweep") != 0 {
		t.Fatal("账号核验前请求历史")
	}
	view, err := a.alignment(context.Background())
	if err != nil || view.CurrentJob != nil || view.LastAttempt != nil {
		t.Fatal("核验前伪造对齐", view, err)
	}
	clock.WaitTimer(t, time.Second)
	select {
	case gate <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("首次核验没有向假端查询登录号")
	}
	waitFor(t, "首次账号核验完成", a.adapter.Ready)
	clock.Advance(time.Second)
	waitFor(t, "Ready 后无需等待历史防抖即可补扫", func() bool { return images.hits("/ready-sweep") == 1 })
	clock.WaitTimer(t, 30*time.Second)
	clock.Advance(29 * time.Second)
	if history.Load() != 0 {
		t.Fatal("防抖前拉历史")
	}
	clock.Advance(time.Second)
	clock.WaitTimer(t, time.Minute)
	if history.Load() != 1 {
		t.Fatalf("首次连接有效轮次 %d", history.Load())
	}
	s := &sink{a: a, account: testAccount, channel: "qq"}
	for range 3 {
		if err = s.Status(ctx, types.Status{Kind: types.StatusConnected, Account: testAccount}); err != nil {
			t.Fatal(err)
		}
	}
	if history.Load() != 1 || images.hits("/ready-sweep") != 1 {
		t.Fatal("重复连接另开任务")
	}
	conn.CloseNow()
	conn = e.peer.accept(t)
	e.peer.send(t, conn, fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":%s,"time":%d}`, testAccount, e.now.Unix()))
	clock.WaitTimer(t, time.Second)
	select {
	case gate <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("重连核验没有向假端查询登录号")
	}
	waitFor(t, "重连账号核验完成", a.adapter.Ready)
	clock.Advance(time.Second)
	clock.WaitTimer(t, 30*time.Second)
	clock.Advance(30 * time.Second)
	clock.WaitTimer(t, time.Minute)
	if history.Load() != 1 {
		t.Fatalf("短重连绕过节流复查了受阻旧段 %d", history.Load())
	}
	clock.Advance(backfillRetry)
	clock.WaitTimer(t, time.Minute)
	if history.Load() != 2 {
		t.Fatalf("重连未独立恢复或重复恢复 %d", history.Load())
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("退出有未收尾任务")
	}
	if e.llmCalls() != 0 {
		t.Fatal("自动补录触发模型", e.llmCalls())
	}
	e.peer.assertNoActions(t)
}

// C02：短停机或多日未知尾段都留缺口；实时消息不抹洞，不声称可以枚举未知私聊。
func TestAlignmentOfflineTailAndKnownCoverage(t *testing.T) {
	t.Parallel()
	for _, age := range []time.Duration{6 * time.Second, 30 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			c := backfillPrivate()
			e.watermark(e.clk.Now().Add(-age))
			e.p.stale = true
			e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
				return []types.Inbound{e.msg(c, "1601", age+time.Minute)}, nil
			}
			e.run()
			g := e.gaps()
			if len(g) == 0 || g[len(g)-1].State != "blocked" {
				t.Fatal("未知尾段消失", g)
			}
			before := g[0]
			e.save(e.msg(c, "1602", 0))
			after := e.gaps()[0]
			if before.ID != after.ID || before.FromMS != after.FromMS || after.State != "blocked" {
				t.Fatal("实时覆盖未知区间", after)
			}
		})
	}
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	v, err := a.alignment(context.Background())
	if err != nil || v.Discovery != "unsupported" || v.LastAttempt != nil {
		t.Fatal("未知新会话被宣称完整", v, err)
	}
}

// C05：补录期间新媒体归属该轮；重复消息不加媒体数，无关用户视频任务不拖住该轮。
func TestAlignmentMediaCompletionAccounting(t *testing.T) {
	t.Parallel()
	t.Run("旧图与重复连接及关闭", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		images := newImgServer(t)
		body := pngBytes("本轮旧图")
		images.serve("/alignment", 200, body)
		a := e.openWith(images)
		defer a.Close()
		st, _ := a.set.For("qq")
		b := a.backfill
		b.clock = fake.NewClock(e.now)
		b.owner = ""
		b.groups = []string{testGroup}
		peer := &historyPeer{ready: true}
		b.source = peer
		c := backfillGroup()
		peer.read = func(context.Context, historyCall) ([]types.Inbound, error) {
			return []types.Inbound{
				{Kind: types.InboundMessage, Account: testAccount, Conv: c, PlatformID: "1701", From: types.User{ID: testOwner}, Time: e.now.Add(-30 * 24 * time.Hour), Segs: []types.Seg{{Kind: types.SegImage, URL: imgURL("/alignment")}}},
				{Kind: types.InboundMessage, Account: testAccount, Conv: c, PlatformID: "1700", From: types.User{ID: testOwner}, Time: e.now.Add(-31 * 24 * time.Hour)},
			}, nil
		}
		if err := st.SetQQCheckedThrough(context.Background(), testAccount, e.now.Add(-30*24*time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); b.media.wg.Wait() }()
		if err := b.executeAccount(ctx, "manual", nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "本轮补录旧图下载", func() bool {
			v, err := a.alignment(context.Background())
			return err == nil && v.LastAttempt != nil && v.LastAttempt.Media.Downloaded == 1 && v.LastAttempt.Media.State == "completed"
		})
		if images.hits("/alignment") != 1 || e.segHash(a, "1701", 0) != sum256(body) {
			t.Fatal("轮次媒体重复或未下载")
		}
		// 第二轮的旧消息已在窗口之外；不再提交，既不计补回也不计去重。
		if err := b.executeAccount(ctx, "manual", nil); err != nil {
			t.Fatal(err)
		}
		v, err := a.alignment(context.Background())
		if err != nil || v.LastAttempt.Inserted != 0 || v.LastAttempt.Duplicates != 0 {
			t.Fatal("范围外历史被重复提交", v, err)
		}
		if images.hits("/alignment") != 1 {
			t.Fatal("重复连接重新下载")
		}
		b.media.wg.Wait()
		// 关闭的媒体阶段明确 skipped；不会为了 run 打开下载器。
		b.media.media = nil
		b.media.forwards = nil
		if err = b.executeAccount(ctx, "manual", nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "关闭媒体阶段", func() bool {
			v, err := a.alignment(context.Background())
			return err == nil && v.LastAttempt.Media.State == "skipped"
		})
		if e.llmCalls() != 0 {
			t.Fatal("补录触发模型")
		}
	})
	alignmentMediaExtraCases(t)
}

func alignmentMediaExtraCases(t *testing.T) {
	const bigSize int64 = (100 << 20) + 1
	for _, name := range []string{"延期重试等到实际终局", "大视频等手动且不等无关任务", "大文件等手动且不等无关任务", "第二档文件等手动且不等无关任务"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			images := newImgServer(t)
			clock := fake.NewClock(e.now)
			opt := e.options()
			opt.Now = clock.Now
			dl := images.downloader()
			opt.MediaDownloader = dl
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			a.media.Close()
			st, _ := a.set.For("qq")
			f, err := mediafetch.New(mediafetch.Options{Store: st, Data: a.paths.Root, Platform: "qq", Downloader: dl, Clock: clock, StatsEvery: -1, Logf: e.logf})
			if err != nil {
				t.Fatal(err)
			}
			a.media = f
			b := a.backfill
			b.media.media = f
			b.media.forwards = nil
			b.clock = clock
			b.owner = ""
			b.groups = []string{testGroup}
			f.SetBackfillNotify(st.NotifyBackfillMedia)
			f.Start()
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); b.media.wg.Wait() }()
			c := backfillGroup()
			peer := &historyPeer{ready: true}
			b.source = peer
			add := func(segs []types.Seg) int64 {
				t.Helper()
				r, err := st.AppendInbound(store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: c.String(), ActorKind: "human", ActorID: "qq:" + testOwner, Kind: "message", CreatedAt: e.now.UnixMilli(), Segments: map[string]any{"segs": segs}}, PlatformID: "2110", Observers: []string{testChar}})
				if err != nil {
					t.Fatal(err)
				}
				return r.Seq
			}
			msg := func(id string, age time.Duration, segs []types.Seg) types.Inbound {
				return types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: c, PlatformID: id, From: types.User{ID: testOwner}, Time: e.now.Add(-age), Segs: segs}
			}
			if err = st.SetQQCheckedThrough(ctx, testAccount, e.now.Add(-2*time.Minute).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if name == "延期重试等到实际终局" {
				var attempts atomic.Int32
				images.handle("/retry", func(w http.ResponseWriter, _ *http.Request) {
					if attempts.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = w.Write(pngBytes("编造重试后成功"))
				})
				images.serve("/round-new", http.StatusOK, pngBytes("编造本轮新图"))
				add([]types.Seg{{Kind: types.SegImage, URL: imgURL("/retry")}})
				peer.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
					if q.anchor == "" {
						return []types.Inbound{msg("2102", 30*time.Second, nil)}, nil
					}
					clock.WaitTimer(t, 30*time.Second)
					return []types.Inbound{msg("2102", 30*time.Second, nil), msg("2101", 90*time.Second, []types.Seg{{Kind: types.SegImage, URL: imgURL("/round-new")}}), msg("2100", 5*time.Minute, nil)}, nil
				}
				if err = b.executeAccount(ctx, "manual", nil); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "消息终局与新图下载", func() bool {
					v, err := a.alignment(ctx)
					return err == nil && v.LastAttempt != nil && v.LastAttempt.State == "completed" && v.Media.Downloaded == 1
				})
				waiting, err := a.alignment(ctx)
				if err != nil || waiting.Media.State != "waiting" || waiting.Media.Queued != 1 || waiting.Media.Running != 0 {
					t.Fatal("延期重试必须保留等待状态", waiting.Media, err)
				}
				clock.Advance(29 * time.Second)
				if images.hits("/retry") != 1 {
					t.Fatal("未到30秒提前重试")
				}
				clock.Advance(time.Second)
				waitFor(t, "延期重试真正完成", func() bool {
					v, err := a.alignment(ctx)
					return err == nil && v.LastAttempt != nil && v.Media.State == "completed" && v.Media.Downloaded == 2 && v.Media.Queued == 0 && v.Media.Running == 0
				})
				v, err := a.alignment(ctx)
				if err != nil || v.LastAttempt.Inserted != 2 || v.LastAttempt.Duplicates != 1 || peer.count() != 2 || images.hits("/retry") != 2 {
					t.Fatal("两页计数或重复媒体不实", v, err)
				}
			} else {
				images.handle("/unrelated", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", fmt.Sprint(bigSize))
					_, _ = w.Write(append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 1024)...))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				})
				unrelated := []types.Seg{{Kind: types.SegVideo, URL: imgURL("/unrelated"), Size: bigSize}}
				seq := add(unrelated)
				progress, err := f.RequestVideo(seq, 0, unrelated)
				if err != nil || progress.State != api.VideoManualDownloading {
					t.Fatal(progress, err)
				}
				defer func() {
					if err := f.CancelVideo(context.Background(), seq, 0); err != nil {
						t.Error(err)
					}
				}()
				waitFor(t, "无关手动视频已在下载", func() bool { return images.hits("/unrelated") == 1 })
				kind, file, size := types.SegVideo, "", bigSize
				if name == "大文件等手动且不等无关任务" {
					kind, file = types.SegFile, "编造视频.mp4"
				}
				if name == "第二档文件等手动且不等无关任务" {
					kind, file, size = types.SegFile, "编造程序.exe", 1
				}
				peer.read = func(context.Context, historyCall) ([]types.Inbound, error) {
					return []types.Inbound{msg("2121", time.Minute, []types.Seg{{Kind: kind, File: file, URL: imgURL("/auto-big"), Size: size}}), msg("2120", 5*time.Minute, nil)}, nil
				}
				if err = b.executeAccount(ctx, "manual", nil); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "大视频等手动不等无关下载", func() bool {
					v, err := a.alignment(ctx)
					return err == nil && v.LastAttempt != nil && v.LastAttempt.State == "completed" && v.Media.State == "completed" && v.Media.AwaitingManual == 1 && v.Media.Queued == 0 && v.Media.Running == 0
				})
				if images.hits("/auto-big") != 0 {
					t.Fatal("已知超限视频仍自动下载")
				}
			}
			if e.llmCalls() != 0 {
				t.Fatal("补录触发模型")
			}
		})
	}
}
