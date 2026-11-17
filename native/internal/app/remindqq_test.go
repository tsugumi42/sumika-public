package app

// 提醒发到主人 QQ 私聊的端到端验收（规格-提醒 第八节、第十三节投递表）：
//   - 假 SnowLuma 逐帧断言：目标是主人的私聊、内容是她说的那句、只发一次；
//   - 关着（reminders.qq）、主人号没配好、qq.reply 没开、发送条件不齐、协议端回失败、结果不明、等不到回包：
//     都不往 QQ 乱发、不崩，退回本地或记不明，不重复狂发。
//
// 号、角色、话都是编的；数据目录只用 t.TempDir()；不连真协议端，不发任何真实消息。

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/remind"
)

type remindQQ struct {
	re   *remindEnv
	rm   *remoteEnv // 复用假协议端的取帧、断言
	peer *sendPeer
}

// newRemindQQ 起一套能真发的假环境：假 SnowLuma 已连上、发送条件齐全，reminders.qq 按 qqOn 写进配置，qq.reply 开着。
// 模型回 herWords。
func newRemindQQ(t *testing.T, qqOn bool) *remindQQ {
	t.Helper()
	re := newRemindEnvBase(t, llmtest.Script{Reply: llmtest.Text(herWords)})
	if qqOn {
		re.e.extraConfig = "reminders:\n  qq: true\n"
	}
	e, a, peer := sendAppFrom(t, re.e, "全部齐全", func(cfg embed.LocalConfig) LocalEmbedding { return &sendEmbedding{&guardLocal{cfg: cfg}} }, true)
	re.a = a
	a.opt.Attention = re.log.add
	a.cfg.QQ.Reply = true
	if a.reminders == nil || a.rhost == nil {
		t.Fatal("应当装上提醒的调度器和宿主")
	}
	conn := startSend(t, e, a)
	return &remindQQ{re: re, rm: &remoteEnv{t: t, e: e, a: a, conn: conn}, peer: peer}
}

// qqReminder 在 at 建一条提醒，目标默认只发 QQ
func (q *remindQQ) qqReminder(text string, at time.Time, targets ...string) remind.Reminder {
	if len(targets) == 0 {
		targets = []string{remind.TargetQQ}
	}
	return q.re.add(text, at, func(d *remind.Draft) { d.Targets = targets })
}

// frames 取走假协议端目前收到的全部发送帧
func (q *remindQQ) frames() []remoteFrame {
	q.rm.drain()
	out := q.rm.seen
	q.rm.seen = nil
	return out
}

// setOwner 同时改内存和配置文件里的主人号（只改一边是“主人号改了”那一格）
func (q *remindQQ) setOwner(memory, file string) {
	q.re.t.Helper()
	q.re.a.cfg.QQ.Owner = config.QQID(memory)
	rewriteConfig(q.re.t, q.re.e, "owner: "+testOwner, "owner: "+file)
}

func lastRing(t *testing.T, r remind.Reminder) remind.Entry {
	t.Helper()
	for i := len(r.History) - 1; i >= 0; i-- {
		if r.History[i].Kind == "ring" {
			return r.History[i]
		}
	}
	t.Fatalf("记录里没有响过的一笔：%+v", r.History)
	return remind.Entry{}
}

// 开着、主人号配好：到点往主人的私聊发一条，内容是她说的那句，只发一次；本地不再写（目标只有 qq）
func TestReminderQQSendsOnceToOwnerPrivate(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	at := q.re.e.now.Add(time.Hour)
	r := q.qqReminder("吃药", at)
	q.re.ringAt(at.Add(20 * time.Second))
	got := q.frames()
	if len(got) != 1 {
		t.Fatalf("该恰好发一条，实际 %d：%+v", len(got), got)
	}
	f := got[0]
	if f.Action != "send_private_msg" || f.Target != testOwner || f.Text != herWords {
		t.Fatalf("目标或内容不对：%+v", f)
	}
	if said := q.re.said(); len(said) != 0 {
		t.Fatalf("只发 QQ 时本地不该再写：%v", said)
	}
	cur := q.re.reminder(r.ID)
	if cur.Status != remind.StatusDone || !strings.Contains(lastRing(t, cur).Note, "QQ") {
		t.Fatalf("响完该是 done，记录说明发到 QQ：%+v", cur)
	}
	// 再检查几次、再过一天、重启：都不再发
	q.re.ringAt(at.Add(26 * time.Hour))
	q.re.restart()
	q.re.a.reminders.Tick(context.Background())
	q.re.a.reminders.Wait()
	q.rm.noFrames(100 * time.Millisecond)
	if n := q.peer.sends.Load(); n != 1 {
		t.Fatalf("协议端累计收到 %d 次发送，该是 1", n)
	}
}

// 本地和 QQ 都选：QQ 一条、本地一条，各一次
func TestReminderQQAndLocalBothOnce(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	at := q.re.e.now.Add(time.Hour)
	q.qqReminder("开会", at, remind.TargetLocal, remind.TargetQQ)
	q.re.ringAt(at.Add(time.Second))
	got := q.frames()
	if len(got) != 1 || got[0].Target != testOwner || got[0].Text != herWords {
		t.Fatalf("QQ 该恰好一条发给主人：%+v", got)
	}
	if said := q.re.said(); len(said) != 1 || said[0] != herWords {
		t.Fatalf("本地该有一条她的话：%v", said)
	}
	q.re.ringAt(at.Add(3 * time.Hour))
	if len(q.frames()) != 0 || len(q.re.said()) != 1 {
		t.Fatal("再检查不该重发")
	}
}

// 晚了：句尾的说明也跟着发到 QQ
func TestReminderQQCarriesLateNote(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	at := q.re.e.now.Add(time.Hour)
	q.qqReminder("交材料", at)
	q.re.ringAt(at.Add(3 * time.Hour))
	got := q.frames()
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, herWords) || !strings.Contains(got[0].Text, "晚了") {
		t.Fatalf("QQ 上也该带晚了多久：%+v", got)
	}
}

// 模型出错：退回原文，原文照样发到 QQ
func TestReminderQQRawTextWhenModelFails(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	q.re.fail.Store(true)
	at := q.re.e.now.Add(time.Hour)
	q.qqReminder("交材料", at)
	q.re.ringAt(at.Add(time.Second))
	got := q.frames()
	if len(got) != 1 || got[0].Text != "提醒：交材料" || got[0].Target != testOwner {
		t.Fatalf("模型失败该发原文：%+v", got)
	}
}

// 不该发的几种：都不往 QQ 发任何帧，不崩，退回本地并说明；提醒照常收尾，不卡着一直重试
func TestReminderQQNotSentCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		qqOn  bool // 配置里 reminders.qq 先开着（关着时建不出发 QQ 的提醒）
		setup func(q *remindQQ)
		why   string // 记录里的原因；空表示这一格不经过发送（目标被去掉了）
	}{
		{"建好以后 reminders.qq 被关掉", true, func(q *remindQQ) { q.re.a.cfg.Reminders.QQ = false }, ""},
		{"主人号为空", true, func(q *remindQQ) { q.setOwner("", `""`) }, "主人号没配好"},
		{"主人号等于机器人号", true, func(q *remindQQ) { q.setOwner(testAccount, testAccount) }, "主人号没配好"},
		{"只改了文件里的主人号", true, func(q *remindQQ) { rewriteConfig(q.re.t, q.re.e, "owner: "+testOwner, "owner: 299") }, "主人号改了"},
		{"qq.reply 没开", true, func(q *remindQQ) { q.re.a.cfg.QQ.Reply = false }, "qq.reply 没开或发送条件不齐"},
		{"发送条件不齐（goSend 关）", true, func(q *remindQQ) { q.re.a.cfg.QQ.GoSend = false }, "qq.reply 没开或发送条件不齐"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			q := newRemindQQ(t, c.qqOn)
			at := q.re.e.now.Add(time.Hour)
			r := q.qqReminder("吃药", at)
			c.setup(q)
			q.re.ringAt(at.Add(time.Second))
			q.re.ringAt(at.Add(2 * time.Second)) // 再检查一次：不重试
			q.rm.noFrames(100 * time.Millisecond)
			if n := q.peer.sends.Load(); n != 0 {
				t.Fatalf("不该有任何发送，协议端收到 %d 次", n)
			}
			said := q.re.said()
			if len(said) != 1 || !strings.HasPrefix(said[0], herWords) {
				t.Fatalf("该退回本地一条她的话：%v", said)
			}
			cur := q.re.reminder(r.ID)
			if cur.Status != remind.StatusDone || cur.Pending != nil {
				t.Fatalf("该收尾，不卡着：%+v", cur)
			}
			if c.why != "" {
				if !strings.Contains(said[0], "QQ 没发出去") {
					t.Fatalf("本地那条该说明 QQ 没发出去：%q", said[0])
				}
				if note := lastRing(t, cur).Note; !strings.Contains(note, c.why) {
					t.Fatalf("记录里该写明原因 %q：%q", c.why, note)
				}
			}
		})
	}
}

// 协议端明确回失败：只试一轮，不狂发；本地带“QQ 没发出去”；记录写明；再检查、重启都不再发
func TestReminderQQSendFailedFallsBackLocalNoStorm(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	q.peer.result.Store(1)
	at := q.re.e.now.Add(time.Hour)
	r := q.qqReminder("吃药", at)
	q.re.ringAt(at.Add(time.Second))
	waitFor(t, "试过发送", func() bool { return q.peer.sends.Load() >= 1 })
	time.Sleep(200 * time.Millisecond)
	tried := q.peer.sends.Load()
	said := q.re.said()
	if len(said) != 1 || !strings.Contains(said[0], "QQ 没发出去") {
		t.Fatalf("发不出去该退回本地并说明：%v", said)
	}
	if note := lastRing(t, q.re.reminder(r.ID)).Note; !strings.Contains(note, "QQ 没发出去") {
		t.Fatalf("记录该写明失败：%q", note)
	}
	q.re.ringAt(at.Add(time.Hour))
	q.re.restart()
	q.re.a.reminders.Tick(context.Background())
	q.re.a.reminders.Wait()
	time.Sleep(200 * time.Millisecond)
	if now := q.peer.sends.Load(); now != tried {
		t.Fatalf("失败以后不该再发：之前 %d 次，现在 %d 次", tried, now)
	}
	if len(q.re.said()) != 1 {
		t.Fatalf("本地不该重复写：%v", q.re.said())
	}
}

// 协议端回的结果不明：记不明，不重发
func TestReminderQQUnknownResultNotResent(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	q.peer.result.Store(2)
	at := q.re.e.now.Add(time.Hour)
	r := q.qqReminder("吃药", at)
	q.re.ringAt(at.Add(time.Second))
	waitFor(t, "试过发送", func() bool { return q.peer.sends.Load() >= 1 })
	time.Sleep(200 * time.Millisecond)
	tried := q.peer.sends.Load()
	note := lastRing(t, q.re.reminder(r.ID)).Note
	if !strings.Contains(note, "不知道有没有发出去") {
		t.Fatalf("记录该写明不明：%q", note)
	}
	if said := q.re.said(); len(said) != 0 {
		t.Fatalf("不明不退回本地（可能已经发出去了）：%v", said)
	}
	q.re.ringAt(at.Add(time.Hour))
	time.Sleep(200 * time.Millisecond)
	if now := q.peer.sends.Load(); now != tried {
		t.Fatalf("结果不明不能重发：之前 %d 次，现在 %d 次", tried, now)
	}
}

// 等不到回包（超时）：记不明，不重发，不退回本地
func TestReminderQQTimeoutIsUnknownNotResent(t *testing.T) {
	t.Parallel()
	q := newRemindQQ(t, true)
	q.peer.result.Store(3) // 收下发送帧但永远不回包
	q.re.a.rhost.qqWait = 300 * time.Millisecond
	at := q.re.e.now.Add(time.Hour)
	r := q.qqReminder("吃药", at)
	q.re.ringAt(at.Add(time.Second))
	if n := q.peer.sends.Load(); n != 1 {
		t.Fatalf("该发过一次：%d", n)
	}
	note := lastRing(t, q.re.reminder(r.ID)).Note
	if !strings.Contains(note, "不知道有没有发出去") {
		t.Fatalf("超时该记不明：%q", note)
	}
	if said := q.re.said(); len(said) != 0 {
		t.Fatalf("不明不退回本地：%v", said)
	}
	q.re.ringAt(at.Add(time.Hour))
	if n := q.peer.sends.Load(); n != 1 {
		t.Fatalf("不明不能重发，累计 %d 次", n)
	}
}
