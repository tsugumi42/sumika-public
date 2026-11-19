package linkplugin

// 发送流程（规格-链接发送改版 A1～A7）的验收：假出口按剧本给回执、按剧本“认领”回声，等回声不真等（记下等了多久就回），
// 不连协议端。号、群、链接、图都是编的。

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/plugintrace"
)

// sentPart 是假出口收到的一段：形状、每张图的 md5、文字
type sentPart struct {
	kind   string
	images []string
	videos int
	text   string
	reply  bool
}

// echoReply 是剧本给某次提交的回答：回执，以及（可选）等回声时回的回声
type echoReply struct {
	out  PartOutcome
	echo *channel.EchoNote
}

// scriptOut 是假出口：收到的第 n 段（从 1 起，跨计划累计）按 script 回答；AwaitPart 记下等了多久，按剧本回声
type scriptOut struct {
	mu      sync.Mutex
	calls   []sentPart
	waits   []time.Duration
	script  func(n int, p sentPart) echoReply
	replies map[string][]echoReply
	hook    func(channel.EchoClaimed)
	afters  int
	plans   int
	// echoNeeds 大于 0：回声要等够这么久才“到”（模拟回声晚），AwaitPart 的 wait 不够就回没到
	echoNeeds time.Duration
	// onGiveUp 在 AwaitPart 等满了没等到（wait 大于 0）、返回之前调：测“放弃以后、换形式之前回声到了”（不在锁里）
	onGiveUp func(planID string, idx int)
}

func (s *scriptOut) Submit(_ context.Context, _ string, _ types.Conversation, parts []types.Part, res []*types.MediaResource) (channel.Held, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans++
	id := fmt.Sprintf("p%d", s.plans)
	if s.replies == nil {
		s.replies = map[string][]echoReply{}
	}
	for _, part := range parts {
		sp := shapeOfPart(part)
		s.calls = append(s.calls, sp)
		s.replies[id] = append(s.replies[id], s.script(len(s.calls), sp))
	}
	for _, r := range res {
		_ = r.Release()
	}
	done := make(chan struct{})
	close(done)
	return channel.Held{PlanID: id, Done: done}, nil
}
func (s *scriptOut) CancelPlan(string) {}
func (s *scriptOut) AwaitPart(_ context.Context, planID string, idx int, wait time.Duration) (channel.EchoNote, bool) {
	s.mu.Lock()
	if wait > 0 {
		s.waits = append(s.waits, wait)
	}
	rs := s.replies[planID]
	if idx < 0 || idx >= len(rs) || rs[idx].echo == nil || wait < s.echoNeeds {
		giveUp := s.onGiveUp
		s.mu.Unlock()
		if wait > 0 && giveUp != nil {
			giveUp(planID, idx)
		}
		return channel.EchoNote{}, false
	}
	n := *rs[idx].echo
	s.mu.Unlock()
	return n, true
}
func (s *scriptOut) OnClaim(fn func(channel.EchoClaimed)) { s.hook = fn }
func (s *scriptOut) outcomes(_ types.Conversation, planID string) ([]PartOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.replies[planID]
	if !ok {
		return nil, errors.New("没有这个计划")
	}
	out := make([]PartOutcome, len(rs))
	for i, r := range rs {
		out[i] = r.out
		out[i].Index = i
	}
	return out, nil
}
func (s *scriptOut) sent() []sentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentPart(nil), s.calls...)
}

func shapeOfPart(p types.Part) sentPart {
	sp := sentPart{kind: p.Kind}
	add := func(body []types.BodySegment) {
		for _, s := range body {
			switch s.Kind {
			case types.BodyText:
				sp.text += s.Text
			case types.BodyVideo:
				sp.videos++
			default:
				h := md5.New()
				_ = s.Media.Read(func(r io.Reader) error { _, e := io.Copy(h, r); return e })
				sp.images = append(sp.images, hex.EncodeToString(h.Sum(nil)))
			}
		}
	}
	add(p.Body)
	for _, n := range p.Nodes {
		add(n.Body)
		if n.ReplyTo > 0 {
			sp.reply = true
		}
	}
	return sp
}

// picture 是一张编的 PNG（宽 w，左上角红点），转 180° 后字节不同
func picture(t *testing.T, w int) *types.MediaResource {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, 2))
	im.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return types.MemoryMedia(fmt.Sprintf("w%d", w), "image/png", w, 2, b.Bytes())
}

func digest(t *testing.T, m *types.MediaResource) string {
	t.Helper()
	h := md5.New()
	if err := m.Read(func(r io.Reader) error { _, e := io.Copy(h, r); return e }); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// flow 是一次展开的夹具：插件、假出口、插件日志、撤回记录
type flow struct {
	t       *testing.T
	r       *rig
	out     *scriptOut
	sink    *traceSink
	recalls []string
	recall  func(platformID string) error
	clock   time.Time
	// dups 是 RecallDup 收到的调用（撤回补发的那份）；dupErr 非空时让它失败；dupCalled 每调一次放一个信号，测试等它
	dupMu     sync.Mutex
	dups      []dupCall
	dupErr    error
	dupCalled chan struct{}
}

type dupCall struct{ orig, dup PartRef }

func (f *flow) dupSnapshot() []dupCall {
	f.dupMu.Lock()
	defer f.dupMu.Unlock()
	return append([]dupCall(nil), f.dups...)
}

// waitLate 等晚到回声触发的撤回协程退出（只在已经收到回调以后调；用插件自己的等待组，不睡）
func (f *flow) waitLate() { f.r.p.lateWG.Wait() }

const acct = "20001"

func newFlow(t *testing.T, script func(n int, p sentPart) echoReply, mut func(*config.LinkExpand)) *flow {
	t.Helper()
	f := &flow{t: t, out: &scriptOut{script: script}, clock: time.Unix(1_800_000_000, 0), dupCalled: make(chan struct{}, 16)}
	ex := &ExpandOptions{Outlet: f.out,
		Admit:    func(channel.Stored) bool { return true },
		Permit:   func(channel.Stored, []types.Part) error { return nil },
		Outcomes: f.out.outcomes,
		Block:    func(types.Conversation, types.Part, string) {},
		Account:  acct,
		Recall: func(_ context.Context, _ types.Conversation, _ string, _ int, platformID string) error {
			f.recalls = append(f.recalls, platformID)
			if f.recall != nil {
				return f.recall(platformID)
			}
			return nil
		},
		RecallDup: func(_ context.Context, _ types.Conversation, orig, dup PartRef) error {
			f.dupMu.Lock()
			f.dups = append(f.dups, dupCall{orig, dup})
			err := f.dupErr
			f.dupMu.Unlock()
			f.dupCalled <- struct{}{}
			return err
		},
	}
	ex.Prepare = func(context.Context, channel.Stored, Snapshot, *linkreader.Result, *linkexpand.Draft) (linkexpand.Prepared, error) {
		return linkexpand.Prepared{}, nil // 这组用例直接调 deliver，不走准备
	}
	f.r, f.sink = newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(mut), Expand: ex, Now: func() time.Time { return f.clock }})
	return f
}

// deliver 让插件发这几段（第一段带的资源一起交出），等它收尾
func (f *flow) deliver(mut func(*config.LinkExpand), parts ...types.Part) sendReport {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &expansionRun{source: storedMsg(31, nil), ctx: ctx, cancel: cancel}
	var res []*types.MediaResource
	for _, p := range parts {
		for _, s := range mediaSegs(p) {
			res = append(res, s.Media)
		}
	}
	snap := expandSettings(mut)()
	rep, err := f.r.p.deliver(run, retryInput{snapshot: snap, result: &linkreader.Result{Platform: "x", CleanURL: "https://x.com/a/status/3101"}}, parts, res)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *flow) rows(route string) []plugintrace.Event {
	var out []plugintrace.Event
	for _, e := range f.sink.snapshot() {
		if e.Route == route {
			out = append(out, e)
		}
	}
	return out
}

func ok(id string) echoReply {
	return echoReply{out: PartOutcome{State: OutcomeConfirmed, PlatformID: id}}
}

// okSent 是带发送时刻的确认（撤回补发的那份要看它第一次调 Send 的时刻在 2 分钟内）
func okSent(id string, sentAt time.Time) echoReply {
	return echoReply{out: PartOutcome{State: OutcomeConfirmed, PlatformID: id, SentAtMS: sentAt.UnixMilli()}}
}
func rejected() echoReply {
	return echoReply{out: PartOutcome{State: OutcomeFailed, Category: CategoryRejected}}
}
func unsupported() echoReply {
	return echoReply{out: PartOutcome{State: OutcomeFailed, Category: CategoryUnsupported}}
}
func unconfirmed(sentAt time.Time) echoReply {
	return echoReply{out: PartOutcome{State: OutcomeUnknown, NoReceipt: true, SentAtMS: sentAt.UnixMilli()}}
}
func echoed(sentAt time.Time, id string, missing bool) echoReply {
	r := unconfirmed(sentAt)
	r.echo = &channel.EchoNote{PlatformID: id, Account: acct, LatencyMS: 300, MediaMissing: missing}
	return r
}

func textAndPics(text string, pics ...*types.MediaResource) types.Part {
	body := []types.BodySegment{{Kind: types.BodyText, Text: text}}
	for _, p := range pics {
		body = append(body, types.BodySegment{Kind: types.BodyImage, Media: p})
	}
	return types.Part{Kind: types.PartMessage, Body: body}
}

// 1. 明确失败：第二次提交就是整条转图，中间没有等回声、没有翻历史
func TestRejectedDoesNotWait(t *testing.T) {
	t.Parallel()
	pic := picture(t, 3)
	orig := digest(t, pic)
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if n == 1 {
			return rejected()
		}
		return ok("m2")
	}, nil)
	f.r.p.opt.Expand.After = func(context.Context, types.Conversation, string) (types.AfterPage, error) {
		f.out.mu.Lock()
		f.out.afters++
		f.out.mu.Unlock()
		return types.AfterPage{}, errors.New("不该翻历史")
	}
	f.deliver(nil, textAndPics("编的正文", pic))
	s := f.out.sent()
	if len(s) != 2 || s[1].kind != types.PartMessage || s[1].images[0] == orig || s[1].text != "编的正文" {
		t.Fatalf("第二帧应是同一条消息、图转过、文字原样：%+v", s)
	}
	if len(f.out.waits) != 0 || f.out.afters != 0 {
		t.Fatalf("明确失败不等回声、不翻历史：等了 %v，翻了 %d 次", f.out.waits, f.out.afters)
	}
}

// 2. 回包成功带消息号：当场算到了，不等、不补发
func TestReceiptWithIDIsConfirmedImmediately(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(int, sentPart) echoReply { return ok("m1") }, nil)
	rep := f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if len(f.out.sent()) != 1 || len(f.out.waits) != 0 || !rep.delivered || rep.unsure {
		t.Fatalf("带号的确认当场算到了：%+v 等了 %v %+v", f.out.sent(), f.out.waits, rep)
	}
}

// 3. 没确认送达、回声到了：不补发；插件日志有一条 route echo、ok、带 ms
func TestUnconfirmedEchoArrivesIsArrived(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(int, sentPart) echoReply { return echoed(f.clock, "m9", false) }, nil)
	rep := f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if len(f.out.sent()) != 1 || !rep.delivered || rep.unsure {
		t.Fatalf("回声到了就是到了，不补发：%+v %+v", f.out.sent(), rep)
	}
	rows := f.rows(RouteEcho)
	if len(rows) != 1 || rows[0].Result != plugintrace.OK || !rows[0].Timed || rows[0].Took != 300*time.Millisecond || rows[0].Note != noteEchoArrived || rows[0].Site != "x" {
		t.Fatalf("回声到达要记一行：%+v", rows)
	}
	if len(f.out.waits) != 1 || f.out.waits[0] != 2*time.Second {
		t.Fatalf("默认等 2 秒：%v", f.out.waits)
	}
}

// 4. 没确认送达、2 秒没回声：第二帧是整条转图；默认不调 After（它是会失败的桩）；日志记 echo_timeout
func TestUnconfirmedNoEchoGoesRotatedAfterWait(t *testing.T) {
	t.Parallel()
	pic := picture(t, 2)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return ok("m2")
	}, nil)
	f.r.p.opt.Expand.After = func(context.Context, types.Conversation, string) (types.AfterPage, error) {
		t.Error("historyCheck 关着不该翻历史")
		return types.AfterPage{}, errors.New("桩")
	}
	f.deliver(nil, textAndPics("编的正文", pic))
	s := f.out.sent()
	if len(s) != 2 || s[1].images[0] == orig || s[1].kind != types.PartMessage {
		t.Fatalf("没回声按没送到，整条转图重发：%+v", s)
	}
	rows := f.rows(RouteEcho)
	if len(rows) != 1 || rows[0].Result != plugintrace.Fail || rows[0].Kind != kindEchoTimeout || rows[0].Took != 2*time.Second {
		t.Fatalf("没等到要记 echo_timeout：%+v", rows)
	}
	// 改等待时长：照配置等
	f2 := newFlow(t, func(int, sentPart) echoReply { return unconfirmed(time.Now()) }, func(e *config.LinkExpand) { e.Send.EchoWaitMS = 3500 })
	f2.deliver(func(e *config.LinkExpand) { e.Send.EchoWaitMS = 3500 }, textAndPics("编的正文", picture(t, 2)))
	if len(f2.out.waits) == 0 || f2.out.waits[0] != 3500*time.Millisecond {
		t.Fatalf("echoWaitMS 要生效：%v", f2.out.waits)
	}
}

// 5. 开 historyCheck：没等到回声后只翻一次历史；查不了就结果不明，不换形式
func TestHistoryCheckOptIn(t *testing.T) {
	t.Parallel()
	on := func(e *config.LinkExpand) { e.Send.HistoryCheck = true }
	f := newFlow(t, func(int, sentPart) echoReply { return unconfirmed(time.Now()) }, on)
	calls := 0
	f.r.p.opt.Expand.After = func(context.Context, types.Conversation, string) (types.AfterPage, error) {
		calls++
		return types.AfterPage{}, errors.New("编的历史失败")
	}
	rep := f.deliver(on, textAndPics("编的正文", picture(t, 2)))
	if calls != 1 || len(f.out.sent()) != 1 || !rep.unsure {
		t.Fatalf("只查一次、查不了不补发：查了 %d 次，发了 %+v，%+v", calls, f.out.sent(), rep)
	}
}

// 6. 转图补发以后回声才到：记“回声迟到”，带实际延迟，不撤回
func TestLateEchoIsLogged(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return ok("m2")
	}, nil)
	f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if f.out.hook == nil {
		t.Fatal("插件启动时要登记认领回调")
	}
	f.out.hook(channel.EchoClaimed{PlanID: "p1", Idx: 0, Late: true, Note: channel.EchoNote{PlatformID: "m1", Account: acct, LatencyMS: 4200}})
	f.out.hook(channel.EchoClaimed{PlanID: "p2", Idx: 0, Late: false, Note: channel.EchoNote{PlatformID: "m2"}})
	f.out.hook(channel.EchoClaimed{PlanID: "别的计划", Idx: 0, Late: true})
	var late []plugintrace.Event
	for _, e := range f.rows(RouteEcho) {
		if e.Note == noteEchoLate {
			late = append(late, e)
		}
	}
	if len(late) != 1 || late[0].Took != 4200*time.Millisecond || late[0].Result != plugintrace.OK || late[0].URL != "https://x.com/a/status/3101" {
		t.Fatalf("迟到的回声记一行，不认识的计划不记：%+v", late)
	}
	if len(f.recalls) != 0 {
		t.Fatal("迟到的回声不撤回")
	}
}

// 8. 吞图：撤回调用的消息号是回声的号 → 转图后图文重发
func TestSwallowedImageRecallsAndResends(t *testing.T) {
	t.Parallel()
	pic := picture(t, 2)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return echoed(f.clock, "-90031", true)
		}
		return ok("m2")
	}, nil)
	f.deliver(nil, textAndPics("编的正文", pic))
	if len(f.recalls) != 1 || f.recalls[0] != "-90031" {
		t.Fatalf("撤回的是回声的号：%v", f.recalls)
	}
	s := f.out.sent()
	if len(s) != 2 || s[1].text != "编的正文" || len(s[1].images) != 1 || s[1].images[0] == orig {
		t.Fatalf("撤回后转图、图文一起重发：%+v", s)
	}
	if rows := f.rows(RouteRecall); len(rows) != 1 || rows[0].Result != plugintrace.OK || rows[0].Note != noteRecall {
		t.Fatalf("撤回记一行：%+v", rows)
	}
}

// 9. 撤回失败：补发的一条只有转过的图、没有文字
func TestRecallFailedSendsImagesOnly(t *testing.T) {
	t.Parallel()
	pic := picture(t, 2)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return echoed(f.clock, "-90032", true)
		}
		return ok("m2")
	}, nil)
	f.recall = func(string) error { return errors.New("message not found or not retractable") }
	f.deliver(nil, textAndPics("编的正文", pic))
	s := f.out.sent()
	if len(s) != 2 || s[1].text != "" || len(s[1].images) != 1 || s[1].images[0] == orig {
		t.Fatalf("撤回失败只补发转好的图，不带文字：%+v", s)
	}
	if rows := f.rows(RouteRecall); len(rows) != 1 || rows[0].Result != plugintrace.Fail {
		t.Fatalf("撤回失败记 fail：%+v", rows)
	}
}

// 10（插件这一道）. 门槛不成立就不撤回、只补发转好的图：号不是自己的、超过 2 分钟、号为空或 0、展开被取消
func TestRecallRefusedOutsideGate(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		note   channel.EchoNote
		sentAt time.Time
	}{
		"别的号":     {channel.EchoNote{PlatformID: "-90033", Account: "29999", MediaMissing: true}, base},
		"超过两分钟":   {channel.EchoNote{PlatformID: "-90033", Account: acct, MediaMissing: true}, base.Add(-RecallWithin - time.Millisecond)},
		"没有发送时刻":  {channel.EchoNote{PlatformID: "-90033", Account: acct, MediaMissing: true}, time.Time{}},
		"号为空":     {channel.EchoNote{PlatformID: "", Account: acct, MediaMissing: true}, base},
		"号为 0":    {channel.EchoNote{PlatformID: "0", Account: acct, MediaMissing: true}, base},
		"号塞了控制字符": {channel.EchoNote{PlatformID: "-9\n0033", Account: acct, MediaMissing: true}, base},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFlow(t, func(n int, _ sentPart) echoReply {
				if n == 1 {
					r := unconfirmed(tc.sentAt)
					if tc.sentAt.IsZero() {
						r.out.SentAtMS = 0
					}
					note := tc.note
					r.echo = &note
					return r
				}
				return ok("m2")
			}, nil)
			f.clock = base
			f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
			if len(f.recalls) != 0 {
				t.Fatalf("门槛不成立不该调撤回：%v", f.recalls)
			}
			if s := f.out.sent(); len(s) != 2 || s[1].text != "" {
				t.Fatalf("只补发转好的图：%+v", s)
			}
		})
	}
	// 正好 2 分钟：还在门槛内
	f := newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return echoed(base.Add(-RecallWithin), "-90034", true)
		}
		return ok("m2")
	}, nil)
	f.clock = base
	f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if len(f.recalls) != 1 {
		t.Fatalf("正好 2 分钟还能撤：%v", f.recalls)
	}
	// 撤回接口没接（渠道不能撤回）：不撤，只补发图
	f2 := newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return echoed(base, "-90035", true)
		}
		return ok("m2")
	}, nil)
	f2.clock = base
	f2.r.p.opt.Expand.Recall = nil
	f2.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if s := f2.out.sent(); len(s) != 2 || s[1].text != "" {
		t.Fatalf("不能撤回就只补发图：%+v", s)
	}
}

// 撤回时展开被取消：不撤、不补发
func TestRecallSkippedWhenExpansionCanceled(t *testing.T) {
	t.Parallel()
	var f *flow
	var cancel context.CancelFunc
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		cancel() // 第一段发完、还没处理吞图时展开被取消（源消息撤回、展开关了）
		return echoed(f.clock, "-90036", true)
	}, nil)
	ctx, c := context.WithCancel(context.Background())
	cancel = c
	run := &expansionRun{source: storedMsg(32, nil), ctx: ctx, cancel: c}
	pic := picture(t, 2)
	_, _ = f.r.p.deliver(run, retryInput{snapshot: expandSettings(nil)(), result: &linkreader.Result{Platform: "x", CleanURL: "https://x.com/a/status/3102"}}, []types.Part{textAndPics("编的正文", pic)}, []*types.MediaResource{pic})
	if len(f.recalls) != 0 || len(f.out.sent()) != 1 {
		t.Fatalf("取消后不撤回、不补发：撤 %v，发 %+v", f.recalls, f.out.sent())
	}
}

// 11. 三张图一条消息被拒：第二帧仍是一条消息、三张都转了、文字原样
func TestMultiImageDirectRotatesAllTogether(t *testing.T) {
	t.Parallel()
	pics := []*types.MediaResource{picture(t, 2), picture(t, 3), picture(t, 4)}
	var origs []string
	for _, p := range pics {
		origs = append(origs, digest(t, p))
	}
	f := newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return rejected()
		}
		return ok("m2")
	}, nil)
	f.deliver(nil, textAndPics("三张", pics...))
	s := f.out.sent()
	if len(s) != 2 || s[1].kind != types.PartMessage || len(s[1].images) != 3 || s[1].text != "三张" {
		t.Fatalf("整条转图重发，不拆开：%+v", s)
	}
	for i, h := range s[1].images {
		if h == origs[i] {
			t.Fatalf("第 %d 张没转：%+v", i+1, s)
		}
	}
	// 转图后仍被拒：文字版（写明试过哪两种），只补一次
	f2 := newFlow(t, func(n int, p sentPart) echoReply {
		if len(p.images) > 0 {
			return rejected()
		}
		return ok("m9")
	}, nil)
	f2.deliver(nil, textAndPics("三张", picture(t, 2), picture(t, 3), picture(t, 4)))
	s = f2.out.sent()
	if len(s) != 3 || len(s[2].images) != 0 || !strings.Contains(s[2].text, "QQ 拒收了这组图（直接发、转 180° 直接发都被拒）") {
		t.Fatalf("转图也被拒就退文字版：%+v", s)
	}
	// 合并转发里一张卡同理：整个转发里的图全转，仍是一个转发
	f3 := newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return rejected()
		}
		return ok("m2")
	}, nil)
	a, b := picture(t, 2), picture(t, 3)
	ha, hb := digest(t, a), digest(t, b)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyImage, Media: a}}}, {Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyText, Text: "字"}, {Kind: types.BodyImage, Media: b}}}}}
	f3.deliver(nil, fw)
	s = f3.out.sent()
	if len(s) != 2 || s[1].kind != types.PartForward || len(s[1].images) != 2 || s[1].images[0] == ha || s[1].images[1] == hb {
		t.Fatalf("转发被拒：整个转发转图重发一次：%+v", s)
	}
}

// 12a. 视频不转：视频被拒直接文字版（视频没有备份），不提交第二次
func TestVideoNeverRotated(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if p.videos > 0 {
			return rejected()
		}
		return ok("m9")
	}, nil)
	v := types.MemoryMedia("v", "video/mp4", 1, 1, []byte("编的视频"))
	f.deliver(nil, types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyText, Text: "编的视频帖"}, {Kind: types.BodyVideo, Media: v}}})
	s := f.out.sent()
	if len(s) != 2 || s[1].videos != 0 || !strings.Contains(s[1].text, "视频没发出去") {
		t.Fatalf("视频被拒直接文字版：%+v", s)
	}
}

// 12b. 视频进转发被拒：视频拿出来，转发（不带视频）重发、视频单独一条紧跟；这个会话 6 小时内照旧排法；记 forward_video_unsupported
func TestVideoInForwardFallsBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vdir := filepath.Join(dir, "inst", "v1")
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "out.mp4"), []byte("编的视频字节"), 0o600); err != nil {
		t.Fatal(err)
	}
	vf, err := os.Open(filepath.Join(vdir, "out.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	video, err := types.FileMedia("v", "video/mp4", vf, func() error { return os.RemoveAll(vdir) })
	if err != nil {
		t.Fatal(err)
	}
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if p.kind == types.PartForward && p.videos > 0 {
			return unsupported()
		}
		return ok(fmt.Sprintf("m%d", n))
	}, nil)
	pic := picture(t, 2)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyText, Text: "正文"}, {Kind: types.BodyImage, Media: pic}}},
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyVideo, Media: video}}},
	}}
	rep := f.deliver(nil, fw)
	s := f.out.sent()
	if len(s) != 3 || s[1].kind != types.PartForward || s[1].videos != 0 || len(s[1].images) != 1 || s[2].kind != types.PartMessage || s[2].videos != 1 {
		t.Fatalf("退回：转发不带视频重发一次、视频单独一条：%+v", s)
	}
	if !rep.delivered || rep.fallback {
		t.Fatalf("退回后都送到了，不是文字版：%+v", rep)
	}
	var row *plugintrace.Event
	for _, e := range f.sink.snapshot() {
		if e.Kind == kindFwdVideo {
			row = &e
		}
	}
	if row == nil || row.Note != noteVideoBack || row.Route != RouteSend {
		t.Fatalf("要记 forward_video_unsupported：%+v", f.sink.snapshot())
	}
	if f.r.p.forwardVideoOn(true, g1) {
		t.Fatal("这个会话 6 小时内照旧排法")
	}
	f.clock = f.clock.Add(quirkTTL)
	if !f.r.p.forwardVideoOn(true, g1) {
		t.Fatal("6 小时后再试")
	}
	if f.r.p.forwardVideoOn(false, g1) {
		t.Fatal("开关关着就不放")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "inst")); len(entries) != 0 {
		t.Fatalf("留下的硬链接目录要清掉：%v", entries)
	}
}

// 12c. 转发里的引用（reply）不支持：去掉引用按旧排法重发一次；6 小时内这个会话发之前就去掉；关着开关也去掉
func TestForwardReplyFallsBack(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if p.reply {
			return unsupported()
		}
		return ok(fmt.Sprintf("m%d", n))
	}, nil)
	mk := func() types.Part {
		return types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
			{Name: "被引", Body: []types.BodySegment{{Kind: types.BodyImage, Media: picture(t, 2)}}},
			{Name: "引用", ReplyTo: 1, Body: []types.BodySegment{{Kind: types.BodyText, Text: "评论"}}},
		}}
	}
	rep := f.deliver(nil, mk())
	s := f.out.sent()
	if len(s) != 2 || !s[0].reply || s[1].reply || s[1].kind != types.PartForward || len(s[1].images) != 1 {
		t.Fatalf("退回旧排法：去掉引用、同一个转发重发一次：%+v", s)
	}
	if !rep.delivered || rep.fallback {
		t.Fatalf("退回后送到了：%+v", rep)
	}
	found := false
	for _, e := range f.sink.snapshot() {
		found = found || (e.Kind == kindFwdReply && e.Note == noteReplyBack)
	}
	if !found {
		t.Fatal("要记 forward_reply_unsupported")
	}
	f.deliver(nil, mk())
	if s := f.out.sent(); len(s) != 3 || s[2].reply {
		t.Fatalf("6 小时内直接按旧排法：%+v", s)
	}
	off := func(e *config.LinkExpand) { e.Send.ForwardQuoteReply = false }
	f2 := newFlow(t, func(n int, p sentPart) echoReply { return ok("m1") }, off)
	f2.deliver(off, mk())
	if s := f2.out.sent(); len(s) != 1 || s[0].reply {
		t.Fatalf("开关关着发之前就去掉：%+v", s)
	}
}

// 12d. 纯文字推文引用纯文字推文：转发里没有媒体，引用被拒也要去掉引用重发，不能按“纯文字没有可换的形式”整条失败
func TestForwardReplyFallsBackWithoutMedia(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if p.reply {
			return echoReply{out: PartOutcome{State: OutcomeFailed, Category: CategoryRejected}}
		}
		return ok(fmt.Sprintf("m%d", n))
	}, nil)
	rep := f.deliver(nil, types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "被引", Body: []types.BodySegment{{Kind: types.BodyText, Text: "原推"}}},
		{Name: "引用", ReplyTo: 1, Body: []types.BodySegment{{Kind: types.BodyText, Text: "评论"}}},
	}})
	s := f.out.sent()
	if len(s) != 2 || !s[0].reply || s[1].reply || s[1].kind != types.PartForward {
		t.Fatalf("去掉引用、同一个转发重发一次：%+v", s)
	}
	if !rep.delivered || rep.fallback {
		t.Fatalf("退回后送到了：%+v", rep)
	}
	found := false
	for _, e := range f.sink.snapshot() {
		found = found || (e.Kind == kindFwdReply && e.Note == noteReplyBack)
	}
	if !found {
		t.Fatal("要记 forward_reply_unsupported")
	}
}

// 插件撤回门槛逐条：回声不是本次认领的（nil）、展开已取消、撤回接口没接，各自单独不成立就不撤
func TestRecallGateConditions(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(int, sentPart) echoReply { return ok("m1") }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &delivery{p: f.r.p, run: &expansionRun{ctx: ctx}}
	good := PartOutcome{State: OutcomeConfirmed, PlatformID: "-90040", SentAtMS: f.clock.UnixMilli(), echo: &channel.EchoNote{PlatformID: "-90040", Account: acct}}
	if why := d.recallGate(good); why != "" {
		t.Fatalf("都成立该能撤：%s", why)
	}
	noEcho := good
	noEcho.echo = nil
	if d.recallGate(noEcho) == "" {
		t.Fatal("不是这一次认领的回声不撤")
	}
	other := good
	other.PlatformID = "-90041" // 回执里的号和回声的号不一样
	if d.recallGate(other) == "" {
		t.Fatal("号对不上不撤")
	}
	cancel()
	if d.recallGate(good) == "" {
		t.Fatal("展开取消了不撤")
	}
}

// 第一段就是合并转发（规格-链接发送改版 A5）：转发段上挂着的引用去掉再发（QQ 的转发带不了回复段），不补一条文字去挂引用
func TestForwardFirstPartDropsQuote(t *testing.T) {
	t.Parallel()
	var quotes []string
	f := newFlow(t, func(int, sentPart) echoReply { return ok("m1") }, nil)
	f.r.p.opt.Expand.Permit = func(_ channel.Stored, parts []types.Part) error {
		for _, p := range parts {
			quotes = append(quotes, p.Quote)
		}
		return nil
	}
	fw := types.Part{Kind: types.PartForward, Quote: "123", Nodes: []types.ForwardNode{{Name: "编的作者", Body: types.TextBody("正文")}}}
	rep := f.deliver(nil, fw)
	if s := f.out.sent(); len(s) != 1 || s[0].kind != types.PartForward || !rep.delivered {
		t.Fatalf("只发一个转发：%+v %+v", s, rep)
	}
	if len(quotes) != 1 || quotes[0] != "" {
		t.Fatalf("转发段上不该带引用：%q", quotes)
	}
}

// “还有 N 条没处理”和“发过 N 次”走同一个 Draft.AppendNote（合 main 时把 A 线的 addExcessNote 并进来）：
// 第一段是转发就接在第一个节点里（规格-链接发送改版 B4 末条），不落到没人渲染的转发正文上；第一段是消息就接在它后面
func TestExcessNoteFindsAPlace(t *testing.T) {
	t.Parallel()
	d := linkexpand.Draft{Parts: []linkexpand.Part{{Kind: types.PartForward, Nodes: []linkexpand.Node{{Name: "甲"}, {Name: "乙"}}}}}
	d.AppendNote("还有 1 条没处理")
	if len(d.Parts) != 1 || len(d.Parts[0].Body) != 0 || len(d.Parts[0].Nodes[0].Body) != 1 {
		t.Fatalf("只有转发时接在第一个节点里、不另加一段：%+v", d.Parts)
	}
	d = linkexpand.Draft{Parts: []linkexpand.Part{{Kind: types.PartMessage}, {Kind: types.PartForward, Nodes: []linkexpand.Node{{Name: "甲"}}}}}
	d.AppendNote("还有 1 条没处理")
	if len(d.Parts[0].Body) != 1 || len(d.Parts[1].Nodes[0].Body) != 0 {
		t.Fatalf("第一段是消息就接在它后面：%+v", d.Parts)
	}
}
