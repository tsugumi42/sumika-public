package channel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/turn"
)

// 回合的路由和屏障的测试（规格-渠道中间件 第六节第 4 步、第九节、第十节 10.1"屏障"、第十六节 B"回合和队列""屏障""回合定了"）。
// 假内核、假写库，不碰库；回合定了以后写库、改写、守卫的测试在 settle_test.go。号、名字都是编的。

// rw 是一套：假内核、假写库、回合
type rw struct {
	t   *testing.T
	log *callLog
	k   *fakeKernel
	w   *fakeWriter
	r   *Responder

	mu   sync.Mutex
	logs []string
}

func (h *rw) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

func (h *rw) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func newRW(t *testing.T, tweak ...func(*ResponderConfig)) *rw {
	t.Helper()
	h := &rw{t: t, log: &callLog{}}
	h.k = newFakeKernel(t, h.log, nil)
	h.w = &fakeWriter{log: h.log}
	cfg := ResponderConfig{Kernel: h.k, Writer: h.w, Roster: testRoster(), Logf: h.logf, Line: LineEnv{Location: tz8}}
	for _, f := range tweak {
		f(&cfg)
	}
	var err error
	if h.r, err = NewResponder(cfg); err != nil {
		t.Fatal(err)
	}
	h.k.r = h.r
	t.Cleanup(h.r.Close)
	return h
}

func (h *rw) idle() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.r.Idle(ctx); err != nil {
		h.t.Fatalf("等了 10 秒路由、裁决还没做完：%v\n调用：%v", err, h.log.all())
	}
}

func (h *rw) waitFor(what string, cond func() bool) {
	h.t.Helper()
	eventually(h.t, what, cond, func() string { return fmt.Sprintf("调用：%v\n日志：%s", h.log.all(), h.logText()) })
}

// ---------- 交给内核的那一份 ----------

// 卡上写了称呼就用称呼；没写（OwnerCall 为 nil）写"所有者"
func TestRelationOwnerCall(t *testing.T) {
	t.Parallel()
	owner := batchFor(convOwnerPM, mk(11, whoOwner, "在吗", false))
	aud := audience.Audience{Channel: chQQ, Scene: "private", PersonIDs: []string{whoOwner}, PrivateOK: true, OwnerAsked: true}
	if got := DefaultRelation(owner, aud, nil); got != "对面是所有者。这里只有你们两个。" {
		t.Fatalf("没写称呼：%q", got)
	}
	if got := DefaultRelation(owner, aud, func(string) string { return "主人" }); got != "对面是主人。这里只有你们两个。" {
		t.Fatalf("卡上的称呼：%q", got)
	}
	friend := batchFor(convFriend, mk(11, whoFriend, "在吗", false))
	if got := DefaultRelation(friend, audience.Audience{Channel: chQQ, Scene: "private", PersonIDs: []string{whoFriend}}, func(string) string { return "主人" }); !strings.Contains(got, "主人允许你私聊的朋友，不是主人") || !strings.Contains(got, "主人的私事不要说") {
		t.Fatalf("朋友私聊用卡上的称呼：%q", got)
	}
}

// 四种场合交给内核的 Input：会话、Seqs 和 Text 的行数、听众、档、场合段、对面是谁
func TestRespondInputFourPlaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		conv       types.Conversation
		msgs       []types.Msg
		wantAud    audience.Audience
		wantScenes []string
		wantRel    string
	}{
		{"QQ 群，主人叫她", convGroup, []types.Msg{mk(11, whoFriend, "先说一句", false), mk(12, whoOwner, "在吗", true)},
			audience.Audience{Channel: chQQ, Scene: "group", PersonIDs: []string{whoFriend, whoOwner}, OwnerAsked: true},
			[]string{"scene-group", "platform-qq"}, ""},
		{"QQ 群，群友叫她", convGroup, []types.Msg{mk(11, whoFriend, "在吗", true)},
			audience.Audience{Channel: chQQ, Scene: "group", PersonIDs: []string{whoFriend}},
			[]string{"scene-group", "platform-qq"}, ""},
		{"QQ 私聊，主人", convOwnerPM, []types.Msg{mk(11, whoOwner, "在吗", false)},
			audience.Audience{Channel: chQQ, Scene: "private", PersonIDs: []string{whoOwner}, PrivateOK: true, OwnerAsked: true},
			[]string{"scene-private", "platform-qq"}, "对面是所有者。这里只有你们两个。"},
		{"QQ 私聊，朋友", convFriend, []types.Msg{mk(11, whoFriend, "在吗", false)},
			audience.Audience{Channel: chQQ, Scene: "private", PersonIDs: []string{whoFriend}},
			[]string{"scene-private", "platform-qq"}, "对面是名qq " + friend + "：所有者允许你私聊的朋友，不是所有者。只说可以让别人知道的事，所有者的私事不要说。"},
		{"本地群聊，主人在场", convLocalG, []types.Msg{mk(11, whoOwner, "大家好", true)},
			audience.Audience{Channel: "console", Scene: "group", PersonIDs: []string{whoOwner}, PrivateOK: true, OwnerAsked: true},
			[]string{"scene-home-group"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newRW(t)
			b := batchFor(c.conv, c.msgs...)
			h.r.Release(b)
			h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
			in := h.k.inputs()[0]
			c.wantAud.Conversation = c.conv.String()
			if !audEqual(in.Audience, c.wantAud) {
				t.Errorf("听众：\n 得到 %+v\n 应为 %+v", in.Audience, c.wantAud)
			}
			if in.CharacterID != "ca" || in.Conversation != c.conv.String() {
				t.Errorf("角色和会话：%q %q", in.CharacterID, in.Conversation)
			}
			if in.Scope != ScopeOf(c.wantAud) {
				t.Errorf("档：%q 应为 %q", in.Scope, ScopeOf(c.wantAud))
			}
			if !slices.Equal(in.Scenes, c.wantScenes) {
				t.Errorf("场合段：%v 应为 %v", in.Scenes, c.wantScenes)
			}
			if in.Relation != c.wantRel {
				t.Errorf("对面是谁：%q 应为 %q", in.Relation, c.wantRel)
			}
			var wantSeqs []int64
			for _, m := range c.msgs {
				wantSeqs = append(wantSeqs, m.Seq)
			}
			if !slices.Equal(in.Seqs, wantSeqs) {
				t.Errorf("Seqs：%v 应为 %v", in.Seqs, wantSeqs)
			}
			if got := headerLines(in.Text); got != len(wantSeqs) {
				t.Errorf("Text 里有 %d 行头，Seqs 有 %d 个：\n%s", got, len(wantSeqs), in.Text)
			}
			if in.Addressed != b.Addressed {
				t.Errorf("Addressed：%v 应为 %v", in.Addressed, b.Addressed)
			}
		})
	}
}

// ---------- 回合进行中来了新的一批：路由 ----------

// 路由表（规格 第六节第 4 步，四种场合都点到）：先有一批在跑，再来一批。
// 批里有被叫到的、OwnerAsked 为真、档不一样：StopIn，被停下的回合处理完（写一条"已打断"）再 Respond；其余走 SteerIn，这一批的 seq 交给内核
func TestRouteWhileRunning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		conv          types.Conversation
		first, second types.Msg
		steer         bool
	}{
		{"QQ 群：群友叫她，又来一句没叫她的群友", convGroup, mk(11, whoFriend, "在吗", true), mk(12, whoOther, "我也在", false), true},
		{"QQ 群：群友叫她，又来一句叫她的", convGroup, mk(11, whoFriend, "在吗", true), mk(12, whoOther, "喂", true), false},
		{"QQ 群：群友叫她，主人插了一句没叫她的（OwnerAsked 为真）", convGroup, mk(11, whoFriend, "在吗", true), mk(12, whoOwner, "我的事", false), false},
		{"QQ 群：群友叫她，主人也叫她", convGroup, mk(11, whoFriend, "在吗", true), mk(12, whoOwner, "喂", true), false},
		{"QQ 群：主人叫她（档是 owner），群友没叫她插了一句（档是 public）", convGroup, mk(11, whoOwner, "喂", true), mk(12, whoFriend, "我也说一句", false), false},
		{"QQ 私聊朋友：又发了一句", convFriend, mk(11, whoFriend, "在吗", false), mk(12, whoFriend, "人呢", false), true},
		{"QQ 私聊主人：又发了一句（OwnerAsked 等于 PrivateOK，为真）", convOwnerPM, mk(11, whoOwner, "在吗", false), mk(12, whoOwner, "人呢", false), false},
		{"本地群聊：群友说话，又来一句没叫她的群友", convLocalG, mk(11, whoFriend, "大家好", true), mk(12, whoOther, "你好", false), true},
		{"本地群聊：群友说话，主人插了一句", convLocalG, mk(11, whoFriend, "大家好", true), mk(12, whoOwner, "我来了", false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newRW(t)
			h.r.Release(batchFor(c.conv, c.first))
			h.waitFor("第一个 Respond", func() bool { return h.log.count("respond:") == 1 })
			h.r.Release(batchFor(c.conv, c.second))
			if c.steer {
				h.waitFor("插话", func() bool { return h.log.count("steer:") == 1 })
				h.idle()
				if got := h.k.steerSeqs; len(got) != 1 || !slices.Equal(got[0], []int64{12}) {
					t.Fatalf("插话交给内核的 seq：%v，应为 [12]", got)
				}
				if h.log.count("stop") != 0 || h.log.count("respond:") != 1 {
					t.Fatalf("插话不该停下、也不该再 Respond：%v", h.log.all())
				}
				return
			}
			h.waitFor("停下后再 Respond", func() bool { return h.log.count("respond:") == 2 })
			h.idle()
			all := h.log.all()
			if stop, resp2 := h.log.index("stop:t1"), h.log.index("respond:t2"); stop < 0 || stop > resp2 {
				t.Fatalf("该先停下再 Respond：%v", all)
			}
			if h.log.count("steer") != 0 {
				t.Fatalf("不该插话：%v", all)
			}
			// 被停下的回合：只记一条"已打断"，不写库、不交回
			if recs := h.w.records(); len(recs) != 1 || recs[0].Kind != "interrupted" {
				t.Fatalf("被停下的回合该记一条已打断：%v", recs)
			}
			if h.log.count("write") != 0 || h.log.count("commit") != 0 {
				t.Fatalf("被停下的回合不写库、不交回：%v", all)
			}
			if in := h.k.inputs()[1]; !slices.Equal(in.Seqs, []int64{12}) {
				t.Fatalf("第二次 Respond 的 Seqs：%v，应为 [12]", in.Seqs)
			}
		})
	}
}

// SteerIn 返回 false（这一轮其实不在跑）改走 Respond：等屏障放开再发，不另外停
func TestSteerRefusedFallsBackToRespond(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.k.steerOK = false
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("第一个 Respond", func() bool { return h.log.count("respond:") == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoOther, "我也在", false)))
	h.waitFor("插话被拒", func() bool { return h.log.count("steer-refused") == 1 })
	h.idle()
	if h.log.count("respond:") != 1 {
		t.Fatalf("回合还没处理完，不该先 Respond：%v", h.log.all())
	}
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone)
	h.waitFor("处理完再 Respond", func() bool { return h.log.count("respond:") == 2 })
	if h.log.count("stop") != 0 {
		t.Fatalf("改走 Respond 不用停：%v", h.log.all())
	}
	if in := h.k.inputs()[1]; !slices.Equal(in.Seqs, []int64{12}) {
		t.Fatalf("第二次 Respond 的 Seqs：%v", in.Seqs)
	}
}

// 没有回合在手里的：直接 Respond，不 Steer、不 Stop
func TestIdleReleaseJustResponds(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Release(batchFor(convGroup, mk(11, whoOwner, "喂", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	if got := h.log.all(); len(got) != 1 {
		t.Fatalf("只该有一次 Respond：%v", got)
	}
}

// ---------- 屏障 ----------

// 上一回合的"回合定了"还在处理（写库卡着）时来的新批次：不 Steer、不 Stop、不 Respond，处理完（Commit 返回）才发，并且历史含上一回合的话
func TestBarrierHoldsNextBatchUntilSettledIsProcessed(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.w.hold = make(chan struct{})
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("第一个 Respond", func() bool { return h.log.count("respond:") == 1 })
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	h.waitFor("写库卡着", func() bool { return h.log.count("write-wait") == 1 })

	// 回合定了，处理中：新批次进来（被叫到的、也有主人在问的，哪种都一样）
	h.r.Release(batchFor(convGroup, mk(12, whoOwner, "喂", true)))
	h.waitFor("挂起", func() bool { return h.r.Holding("ca", convGroup.String()) })
	time.Sleep(50 * time.Millisecond) // 给错误实现一点机会先动手
	if got := h.log.all(); h.log.count("steer") != 0 || h.log.count("stop") != 0 || h.log.count("respond:") != 1 || h.log.count("commit") != 0 {
		t.Fatalf("屏障没放开前不该有 Steer、Stop、Respond、Commit：%v", got)
	}

	// 不同的会话不受影响（清单⑤）
	h.r.Release(batchFor(convOwnerPM, mk(21, whoOwner, "在吗", false)))
	h.waitFor("别的会话照常 Respond", func() bool { return h.log.count("respond:") == 2 })

	close(h.w.hold)
	h.waitFor("处理完以后才 Respond", func() bool { return h.log.count("respond:") == 3 })
	h.idle()
	if c, s, r := h.log.index("commit-ok:t1"), h.log.index("start:"), h.log.index("respond:t3"); c < 0 || s < c || r < s {
		t.Fatalf("该先 Commit、再开始发、再放新的一批：%v", h.log.all())
	}
	if in := h.k.inputs()[2]; in.Conversation != convGroup.String() || !slices.Equal(in.Seqs, []int64{12}) {
		t.Fatalf("挂起的那一批该原样发出去：%+v", in)
	}
}

// 挂起的好几批并成一批：消息按 seq 排，Addressed 取并集
func TestBarrierMergesHeldBatches(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.w.hold = make(chan struct{})
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	h.waitFor("写库卡着", func() bool { return h.log.count("write-wait") == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoOther, "第二句", false)))
	h.r.Release(batchFor(convGroup, mk(13, whoFriend, "第三句", true)))
	h.waitFor("路由做完、两批都挂起", func() bool {
		select {
		case <-h.r.arrive.idleChan():
			return h.r.Holding("ca", convGroup.String())
		default:
			return false
		}
	})
	close(h.w.hold)
	h.waitFor("并成一次 Respond", func() bool { return h.log.count("respond:") == 2 })
	in := h.k.inputs()[1]
	if !slices.Equal(in.Seqs, []int64{12, 13}) || !in.Addressed {
		t.Fatalf("并起来的 Seqs 应为 [12 13]、Addressed 为真：%+v", in)
	}
}

// 早期出错（结局 error）也要放开屏障：后面的批次能进来，并且记一条"出错"的计划
func TestErrorOutcomeReleasesBarrier(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.k.finishErr("ca", convGroup.String(), turn.OutcomeError, fmt.Errorf("找不到模型"))
	h.waitFor("记出错", func() bool { return len(h.w.records()) == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoFriend, "再来", true)))
	h.waitFor("后面的批次进得来", func() bool { return h.log.count("respond:") == 2 })
	if rec := h.w.records()[0]; rec.Kind != "error" || !strings.Contains(rec.Reason, "找不到模型") {
		t.Fatalf("该记一条出错的计划，带原因：%+v", rec)
	}
	if h.log.count("commit") != 0 || h.log.count("write") != 0 {
		t.Fatalf("出错的回合不写库、不交回：%v", h.log.all())
	}
}

// 裁决里 panic 了，屏障照样放开
func TestPanicInSettleStillReleasesBarrier(t *testing.T) {
	t.Parallel()
	h := newRW(t, func(c *ResponderConfig) { c.Guard = &fakeGuard{panics: true} })
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	h.waitFor("守卫炸了也记下拦下", func() bool { return len(h.w.records()) == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoFriend, "再来", true)))
	h.waitFor("后面的批次进得来", func() bool { return h.log.count("respond:") == 2 })
	if rec := h.w.records()[0]; rec.Kind != "blocked" || rec.Reason != "守卫没判成" {
		t.Fatalf("守卫炸了该拦下、reason 记守卫没判成：%+v", rec)
	}
}

// 不是我们交出去的回合（主窗口的）、没见过的回合定了：忽略，不写库、不卡住
func TestSettledOfSomeoneElsesTurnIsIgnored(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Settled(turn.Settled{TurnID: "x", CharacterID: "ca", Conversation: "console:ca", Outcome: turn.OutcomeDone, Said: []turn.Said{{Step: 0, Text: "主窗口说的"}}})
	h.r.Settled(turn.Settled{TurnID: "y", CharacterID: "ca", Conversation: convGroup.String(), Outcome: turn.OutcomeDone, Said: []turn.Said{{Step: 0, Text: "没人要的"}}})
	h.idle()
	if got := h.log.all(); len(got) != 0 {
		t.Fatalf("不认识的回合不该有任何动作：%v", got)
	}
}

// ---------- 插话没送到 ----------

// 插话被内核丢掉（回合收尾时没送到，或被后来的停止丢掉）：这批话她没看到，放回去，下一次 Respond 一起带上
func TestDroppedSteerIsRespondedLater(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoOther, "我也在", false)))
	h.waitFor("插话", func() bool { return h.log.count("steer:") == 1 })
	id := "s2"
	h.r.OnSteer(turn.SteerEvent{Phase: "dropped", ID: id, Requeue: true})
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone)
	h.waitFor("没送到的下一次带上", func() bool { return h.log.count("respond:") == 2 })
	if in := h.k.inputs()[1]; !slices.Equal(in.Seqs, []int64{12}) {
		t.Fatalf("丢掉的插话该在第二次 Respond 的 Seqs 里：%v", in.Seqs)
	}
}

// 送到了的插话不再发一遍；不认识的插话（主窗口的）忽略
func TestDeliveredSteerIsNotRepeated(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.r.Release(batchFor(convGroup, mk(12, whoOther, "我也在", false)))
	h.waitFor("插话", func() bool { return h.log.count("steer:") == 1 })
	h.r.OnSteer(turn.SteerEvent{Phase: "accepted", ID: "s2"})
	h.r.OnSteer(turn.SteerEvent{Phase: "delivered", ID: "s2"})
	h.r.OnSteer(turn.SteerEvent{Phase: "dropped", ID: "from-console"})
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone)
	h.idle()
	if h.log.count("respond:") != 1 {
		t.Fatalf("送到了的插话不该再 Respond：%v", h.log.all())
	}
}

// ---------- 关闭 ----------

// 关闭以后到的"回合定了"什么也不写；关闭以后放行的批次不 Respond
func TestCloseStopsEverything(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.r.Close()
	h.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	h.r.Release(batchFor(convOwnerPM, mk(21, whoOwner, "在吗", false)))
	time.Sleep(50 * time.Millisecond)
	if h.log.count("write") != 0 || h.log.count("commit") != 0 || h.log.count("record") != 0 || h.log.count("respond:") != 1 {
		t.Fatalf("关闭以后不该有任何写库和新的回合：%v", h.log.all())
	}
}

// 回合处理超过 StuckAfter 还没完：记一行日志（界面能知道在等上一句裁决）
func TestStuckRoundIsLogged(t *testing.T) {
	t.Parallel()
	h := newRW(t, func(c *ResponderConfig) { c.StuckAfter = 30 * time.Millisecond })
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("Respond", func() bool { return h.log.count("respond:") == 1 })
	h.waitFor("卡住的日志", func() bool { return strings.Contains(h.logText(), "内核还没说定了") })
}

// 能挂到真的内核上：hub.Use(responder.Plugin()) 不报错、能停掉。真内核回合的端到端测试要 internal/turn 的夹具（假网关、角色），
// 那些在 turn 包的测试文件里，渠道包引不到，所以这里只验接得上
func TestPluginMountsOnRealHub(t *testing.T) {
	t.Parallel()
	h := newRW(t)
	hub := turn.NewHub(context.Background(), turn.Config{}, nil, nil, nil)
	if err := hub.Use(h.r.Plugin()); err != nil {
		t.Fatal(err)
	}
	hub.StopPlugin(h.r.Plugin().ID())
}
