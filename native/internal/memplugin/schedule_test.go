package memplugin

// 后台整理的排程（W5，规格-记忆-v2 13.2.5、13.2.13）：计时、启动扫描、串行和同键合并、退避落库、停掉的顺序。
// 对着边界清单 ④ 关掉后旧任务还在回来、⑤ 多角色多会话、⑦ 上限、⑧ 数据晚到、⑩ 失败不是全有或全无、⑬ 并发和乱序、㉑ 隐私。
// 模型、事件来源、钟都是假的，库是临时目录里的副本

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/prompts"
	"sumika-native/internal/turn"
)

const (
	cv  = "console:suzu"
	grp = "qq:group:30001"
)

// ⑦ 39 条不跑（等安静 10 分钟），40 条立刻跑；到点时不再复查门槛
func TestW5ThirtyNineWaitFortyRunsAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	for i := 0; i < 39; i++ {
		w.ev.add(cv, fmt.Sprintf("第%d句闲话。", i+1))
		r.p.Notify("suzu", cv)
	}
	r.idle()
	if w.gen.count() != 0 {
		t.Fatalf("39 条不该跑：模型调了 %d 次", w.gen.count())
	}
	r.advance(10*time.Minute - time.Second)
	if w.gen.count() != 0 {
		t.Fatalf("差 1 秒才安静 10 分钟：模型调了 %d 次", w.gen.count())
	}
	// 第 40 条来了：立刻整理，不等计时
	w.ev.add(cv, "第40句闲话。")
	r.notify("suzu", cv, 1)
	if w.gen.count() != 1 {
		t.Fatalf("40 条该立刻跑：模型调了 %d 次", w.gen.count())
	}
	if seq, _ := w.cursor(cv); seq != 40 {
		t.Errorf("游标 %d，该到 40", seq)
	}
}

// 安静 10 分钟：到点把攒着的整理掉，不再复查门槛（Bun 的做法）；每条新通知都重置计时
func TestW5QuietTimerRunsWhatIsPendingAndEachNotificationResetsIt(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 5)
	r.notify("suzu", cv, 1)
	r.advance(9 * time.Minute)
	w.ev.add(cv, "又来一句。")
	r.notify("suzu", cv, 1) // 重置：从现在起再等 10 分钟
	r.advance(9 * time.Minute)
	if w.gen.count() != 0 {
		t.Fatalf("新通知该重置计时：模型调了 %d 次", w.gen.count())
	}
	r.advance(time.Minute)
	if w.gen.count() != 1 {
		t.Fatalf("安静满 10 分钟该整理：模型调了 %d 次", w.gen.count())
	}
	if seq, _ := w.cursor(cv); seq != 6 {
		t.Errorf("6 条都该整理掉：游标 %d", seq)
	}
	// 邻居：整理完再没有新通知，计时器到点只是空转，不调模型
	r.advance(time.Hour)
	if w.gen.count() != 1 {
		t.Errorf("没有新东西不该再调：%d 次", w.gen.count())
	}
}

// ⑧ 通知比入库先到：到点读库仍能看到这条
func TestW5NotificationBeforeTheRowIsStored(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	r.notify("suzu", cv, 1) // 通知先到，库里还没有
	w.ev.add(cv, "我下周三去看牙医。")
	r.advance(10 * time.Minute)
	if w.gen.count() != 1 || !strings.Contains(w.gen.promptOf(0), "我下周三去看牙医") {
		t.Fatalf("到点读库该看到这条：模型调了 %d 次", w.gen.count())
	}
}

// ⑦ 过滤和整理用同一个：40 条里补录的不数，数不够不跑；真的攒够了才跑（⑬ 补录行夹在中间）
func TestW5BackfilledRowsDontCountTowardsForty(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	for i := 0; i < 39; i++ {
		w.ev.add(cv, fmt.Sprintf("第%d句闲话。", i+1))
		if i%3 == 0 {
			w.ev.add(cv, "补录的旧话。", func(e *memory.Event) { e.Backfill = true })
		}
		r.p.Notify("suzu", cv)
	}
	r.idle()
	if w.gen.count() != 0 {
		t.Fatalf("39 条实时的加十几条补录的，不该跑：%d 次", w.gen.count())
	}
	w.ev.add(cv, "第40句闲话。")
	r.notify("suzu", cv, 1)
	if w.gen.count() != 1 {
		t.Fatalf("实时的攒够 40 条该跑：%d 次", w.gen.count())
	}
	if strings.Contains(w.gen.promptOf(0), "补录的旧话") {
		t.Error("补录的不该给模型")
	}
	if seq, _ := w.cursor(cv); seq != 40+13 {
		t.Errorf("游标该越过补录的行：%d", seq)
	}
}

// ⑦ 201 条：第一批 200 条，续跑排在后面，不丢不跳
func TestW5TwoHundredAndOneRunsAsTwoBatches(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 201)
	r.notify("suzu", cv, 1)
	if w.gen.count() != 2 {
		t.Fatalf("201 条该分两批：模型调了 %d 次", w.gen.count())
	}
	if p := w.gen.promptOf(0); !strings.Contains(p, "[200] ") || strings.Contains(p, "[201] ") {
		t.Errorf("第一批该是 1～200")
	}
	if p := w.gen.promptOf(1); !strings.Contains(p, "[201] ") || strings.Contains(p, "[200] ") {
		t.Errorf("第二批该只有 201")
	}
	if seq, _ := w.cursor(cv); seq != 201 {
		t.Errorf("游标 %d", seq)
	}
}

// ⑬ 同键合并：1000 条通知加 10 次 force，模型调用不超过 2 次
func TestW5ThousandNotificationsAndTenForcesCallTheModelAtMostTwice(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		extra int // 模型想的时候又来了几条
		calls int
	}{{"想的时候没有新话", 0, 1}, {"想的时候又来了新话", 10, 2}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, nil)
			w := r.mems["suzu"]
			r.enable()
			w.initCursor(t, cv, 0)
			w.ev.addN(cv, 50)
			release, entered := make(chan struct{}), make(chan struct{}, 8)
			w.gen.block = func(ctx context.Context) error {
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			r.p.Notify("suzu", cv)
			<-entered // 第一批正在想
			for i := 0; i < 999; i++ {
				r.p.Notify("suzu", cv)
			}
			for i := 0; i < 10; i++ {
				r.p.Summarize("suzu", cv, true)
			}
			for i := 0; i < c.extra; i++ {
				w.ev.add(cv, "想的时候又来的话。")
			}
			close(release)
			r.idle()
			if w.gen.count() != c.calls {
				t.Errorf("模型调了 %d 次，该是 %d", w.gen.count(), c.calls)
			}
			if seq, _ := w.cursor(cv); seq != int64(50+c.extra) {
				t.Errorf("游标 %d", seq)
			}
			if w.gen.peakLive() != 1 {
				t.Errorf("同一个会话同时调了 %d 次", w.gen.peakLive())
			}
		})
	}
}

// 同时整理的会话数有上限（起步 2）；五个会话都攒够了，同一时刻最多两个在调模型，最后都整理完
func TestW5AtMostTwoConversationsAtATime(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	release, entered := make(chan struct{}), make(chan struct{}, 8)
	w.gen.block = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	convs := []string{"console:a", "console:b", "console:c", "console:d", "console:e"}
	for _, c := range convs {
		w.initCursor(t, c, 0)
		w.ev.addN(c, 40)
		r.p.Notify("suzu", c)
	}
	<-entered
	<-entered
	time.Sleep(100 * time.Millisecond) // 给第三个一点机会冒出来
	if got := w.gen.peakLive(); got != 2 {
		t.Fatalf("同时在调的该正好 2 个：%d", got)
	}
	close(release)
	r.idle()
	if w.gen.count() != 5 || w.gen.peakLive() != 2 {
		t.Errorf("五个都该整理完、同时最多 2 个：调了 %d 次，最多同时 %d", w.gen.count(), w.gen.peakLive())
	}
	for _, c := range convs {
		if seq, _ := w.cursor(c); seq == 0 {
			t.Errorf("%s 没整理", c)
		}
	}
}

// B3 / ⑩：失败了自动再试，不等新消息；第 n 次等 2ⁿ 分钟；日志只有原因码，没有错误体、会话 id、原文
func TestW5FailedBatchRetriesOnItsOwnWithBackoff(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, grp, 0)
	w.ev.addN(grp, 5)
	w.gen.reply = func(n int, _ string) (string, error) {
		if n <= 3 {
			return "", errors.New("上游 502：key=TESTKEY-secret-body")
		}
		return candidateReply("主人想不起来的一件事", 1), nil
	}
	r.notify("suzu", grp, 1)
	r.advance(10 * time.Minute) // 安静到点：第 1 次，失败
	if st := w.state(t, grp); w.gen.count() != 1 || st.Failures != 1 || st.RetryAt != r.clock.Now().Add(2*time.Minute).UnixMilli() {
		t.Fatalf("第 1 次失败后 %+v，调了 %d 次", st, w.gen.count())
	}
	r.advance(2*time.Minute - time.Second)
	if w.gen.count() != 1 {
		t.Fatalf("还差 1 秒，不该再试：%d 次", w.gen.count())
	}
	r.advance(time.Second) // 第 2 次，失败，等 4 分钟
	r.advance(4 * time.Minute)
	r.advance(8 * time.Minute) // 第 4 次，成功
	if w.gen.count() != 4 {
		t.Fatalf("该试 4 次：%d", w.gen.count())
	}
	if st := w.state(t, grp); st.Failures != 0 || st.RetryAt != 0 || st.LastSeq != 5 {
		t.Errorf("成功以后清零、游标挪过去：%+v", st)
	}
	if w.rows(t, "facts") != 1 {
		t.Errorf("成功那一批该写进一条候选")
	}
	// 这一轮没有任何新通知，全靠自己到点再试
	log := r.logs.String()
	for _, secret := range []string{"502", "TESTKEY", "secret-body", grp, "第1句闲话", "主人想不起来"} {
		if strings.Contains(log, secret) {
			t.Errorf("日志里不该有 %q：\n%s", secret, log)
		}
	}
	if !strings.Contains(log, "model_call") {
		t.Errorf("日志该写原因码：\n%s", log)
	}
}

// 排一遍失败到停、重启后还是停、新通知再给一次机会（B3 + 13.2.5 的"6 次封顶"）
func TestB3SixFailuresStopAndSurviveARestart(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 3)
	w.gen.reply = func(int, string) (string, error) { return "", errors.New("上游挂了") }
	r.notify("suzu", cv, 1)
	r.advance(10 * time.Minute)
	for n := 1; n < memory.MaxFailures; n++ {
		r.advance(memory.BackoffDelay(n))
	}
	if st := w.state(t, cv); w.gen.count() != 6 || st.Failures != 6 || st.RetryAt != 0 {
		t.Fatalf("该试满 6 次就停：%+v，调了 %d 次", st, w.gen.count())
	}
	r.advance(48 * time.Hour)
	if w.gen.count() != 6 {
		t.Fatalf("停了不该再自己试：%d 次", w.gen.count())
	}

	// 重启：停掉再起一个，状态在库里
	r.lf.stop()
	r.p = r.newPlugin(nil)
	r.enable()
	r.advance(48 * time.Hour)
	if st := w.state(t, cv); w.gen.count() != 6 || st.Failures != 6 {
		t.Fatalf("重启以后仍然是停的：%+v，调了 %d 次", st, w.gen.count())
	}
	// 新通知：再给一次机会；这一次失败就直接又停，不会再白试 6 次
	w.ev.add(cv, "新来的一句。")
	r.notify("suzu", cv, 1)
	r.advance(10 * time.Minute)
	if st := w.state(t, cv); w.gen.count() != 7 || st.Failures != 6 || st.RetryAt != 0 {
		t.Fatalf("新通知该多给一次机会：%+v，调了 %d 次", st, w.gen.count())
	}
	r.advance(48 * time.Hour)
	if w.gen.count() != 7 {
		t.Errorf("又停了：%d 次", w.gen.count())
	}
}

// B3：失败 3 次以后重启，次数和下次时间不变，没到点不调模型，到点才调
func TestB3RestartKeepsTheBackoffClock(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Quiet = time.Minute })
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 3)
	w.gen.reply = func(n int, _ string) (string, error) {
		if n <= 3 {
			return "", errors.New("上游挂了")
		}
		return candidateReply("主人说了三句闲话", 1), nil
	}
	r.notify("suzu", cv, 1)
	r.advance(time.Minute) // 第 1 次失败
	r.advance(2 * time.Minute)
	r.advance(4 * time.Minute) // 第 3 次失败，等 8 分钟
	before := w.state(t, cv)
	if before.Failures != 3 || w.gen.count() != 3 {
		t.Fatalf("该失败了 3 次：%+v", before)
	}
	r.lf.stop()
	r.p = r.newPlugin(func(o *Options) { o.Quiet = time.Minute })
	r.enable()
	if after := w.state(t, cv); after != before {
		t.Fatalf("重启以后状态变了：%+v → %+v", before, after)
	}
	w.ev.add(cv, "重启以后来的一句。")
	r.notify("suzu", cv, 1)
	r.advance(time.Minute) // 安静到点，但退避还有 7 分钟
	if w.gen.count() != 3 {
		t.Fatalf("还在退避里不该调：%d 次", w.gen.count())
	}
	r.advance(7*time.Minute - time.Second)
	if w.gen.count() != 3 {
		t.Fatalf("差 1 秒：%d 次", w.gen.count())
	}
	r.advance(time.Second) // 到点自己再试，不用等新消息
	if st := w.state(t, cv); w.gen.count() != 4 || st.Failures != 0 || st.LastSeq != 4 {
		t.Errorf("到点该整理成功：%+v，调了 %d 次", st, w.gen.count())
	}
}

// B4 / B5：排队的整理轮到时才看退避；force 不看
func TestB4B5QueuedRunSeesBackoffForceDoesNot(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 5)
	if _, err := w.m.NoteFailure(context.Background(), cv); err != nil { // 刚失败过一次：2 分钟内不调
		t.Fatal(err)
	}
	r.p.Summarize("suzu", cv, false)
	r.idle()
	if w.gen.count() != 0 || r.clock.pending() != 1 {
		t.Fatalf("退避中排的整理不该调模型、该留一个到点再来的计时：调了 %d 次，计时 %d 个", w.gen.count(), r.clock.pending())
	}
	r.advance(2 * time.Minute) // 轮到时已经过期：照常调（B5）
	if w.gen.count() != 1 || w.state(t, cv).Failures != 0 {
		t.Fatalf("到点该整理：调了 %d 次", w.gen.count())
	}
	// force：退避中也调（B4）
	w.ev.add(cv, "再来一句。")
	if _, err := w.m.NoteFailure(context.Background(), cv); err != nil {
		t.Fatal(err)
	}
	r.p.Summarize("suzu", cv, true)
	r.idle()
	if w.gen.count() != 2 {
		t.Fatalf("force 不看退避：调了 %d 次", w.gen.count())
	}
	// 邻居：force 只管它这一次，之后不带 force 排的整理照旧在退避里
	w.ev.add(cv, "第三句。")
	if _, err := w.m.NoteFailure(context.Background(), cv); err != nil {
		t.Fatal(err)
	}
	r.p.Summarize("suzu", cv, false)
	r.idle()
	if w.gen.count() != 2 {
		t.Errorf("上一次的 force 不该粘到这一次：调了 %d 次", w.gen.count())
	}
}

// B6：没选后台模型，不整理、游标不动、不算失败，也不排重试；选好以后下一次触发照常整理
func TestB6NoModelIsNotAFailureAndSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	w.setModel("")
	r.notify("suzu", cv, 1)
	if st := w.state(t, cv); w.gen.count() != 0 || st.Failures != 0 || st.LastSeq != 0 {
		t.Fatalf("没选模型：%+v，调了 %d 次", st, w.gen.count())
	}
	w.setModel("bg-model")
	r.advance(10 * time.Minute)
	if w.gen.count() != 1 {
		t.Errorf("选好以后下一次触发该整理：%d 次", w.gen.count())
	}
}

// 忘掉 / 真删撞上整理（④）：结果作废，不是失败，不记失败次数；下一次触发再整理
func TestW5ForgetDuringTheCallDropsTheBatchWithoutCountingAFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	w.gen.reply = func(int, string) (string, error) { return candidateReply("主人想被忘掉的事", 1), nil }
	var once sync.Once
	w.gen.block = func(ctx context.Context) error {
		var err error
		once.Do(func() { _, err = w.m.BumpGeneration(ctx) })
		return err
	}
	r.notify("suzu", cv, 1)
	if st := w.state(t, cv); w.gen.count() != 1 || st.Failures != 0 || st.LastSeq != 0 || w.rows(t, "facts") != 0 || w.rows(t, "memory_jobs") != 0 {
		t.Fatalf("作废的一批什么都不该写、不算失败：%+v，调了 %d 次", st, w.gen.count())
	}
	r.advance(10 * time.Minute) // 下一次触发：世代号稳定了，照常
	if w.gen.count() != 2 || w.rows(t, "facts") != 1 {
		t.Errorf("下一次该整理并写下：调了 %d 次，事实 %d 条", w.gen.count(), w.rows(t, "facts"))
	}
}

// ④ 停掉时整理还在调模型：放行后不写库（事实、片段、memory_jobs、游标都不变），stop 要等到它退出才返回
func TestW5StopWhileTheModelIsThinking(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	lf := r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	w.gen.reply = func(int, string) (string, error) { return candidateReply("停掉以后不该写下的事", 1), nil }
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	w.gen.block = func(context.Context) error { // 不听取消，只等放行：模拟卡在不可中断的地方
		entered <- struct{}{}
		<-release
		return nil
	}
	r.p.Notify("suzu", cv)
	<-entered
	stopped := make(chan struct{})
	go func() { lf.stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("整理的协程还没退出，stop 不该返回")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-stopped
	if seq, _ := w.cursor(cv); seq != 0 || w.rows(t, "facts") != 0 || w.rows(t, "episodes") != 0 || w.rows(t, "memory_jobs") != 0 {
		t.Errorf("停掉以后不该写：游标 %d，事实 %d 片段 %d job %d", seq, w.rows(t, "facts"), w.rows(t, "episodes"), w.rows(t, "memory_jobs"))
	}
	if st := w.state(t, cv); st.Failures != 0 {
		t.Errorf("停掉不算整理失败：%+v", st)
	}
}

// ④ 到点前停掉，到点后不调模型；停掉以后再来通知、再要求整理：不排队、不起协程、不留计时
func TestW5StopBeforeTheTimerAndNothingAfterStop(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	lf := r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 5)
	r.notify("suzu", cv, 1)
	if r.clock.pending() != 1 {
		t.Fatalf("该有一个计时：%d", r.clock.pending())
	}
	lf.stop()
	if r.clock.pending() != 0 {
		t.Errorf("停掉要取消所有计时：还剩 %d", r.clock.pending())
	}
	r.clock.Advance(time.Hour)
	if w.gen.count() != 0 {
		t.Fatalf("停掉以后到点不该调模型：%d 次", w.gen.count())
	}
	before := w.ev.readCount()
	r.p.Notify("suzu", cv)
	r.p.Summarize("suzu", cv, true)
	lf.notify("suzu", cv) // 绕过 Plugin，直接冲着停掉的这一次来
	lf.request("suzu", cv, true)
	time.Sleep(20 * time.Millisecond)
	if r.clock.pending() != 0 || w.gen.count() != 0 || w.ev.readCount() != before {
		t.Errorf("停掉以后再来通知不该排任何东西：计时 %d，模型 %d，读事件 %d→%d", r.clock.pending(), w.gen.count(), before, w.ev.readCount())
	}
	lf.stop() // 可以重复调
}

// 和内核的接口：Setup 以后 Notify 能到排程，StopPlugin 之后再 Notify 什么也不做；同一个插件值可以再启用
func TestW5NotifyThroughTheHub(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Characters = func() []string { return nil } })
	w := r.mems["suzu"]
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	hub := turn.NewHub(context.Background(), turn.Config{Logf: func(string, ...any) {}}, nil, nil, nil)
	if err := hub.Use(r.p); err != nil {
		t.Fatal(err)
	}
	r.lf = r.p.current()
	if r.lf == nil {
		t.Fatal("启用以后该有当前这一次")
	}
	r.p.Notify("suzu", cv)
	r.idle()
	if w.gen.count() != 1 {
		t.Fatalf("40 条该整理：%d 次", w.gen.count())
	}
	hub.StopPlugin(ID)
	if r.p.current() != nil {
		t.Error("停掉以后不该还有当前这一次")
	}
	w.ev.addN(cv, 40)
	r.p.Notify("suzu", cv)
	r.p.Summarize("suzu", cv, true)
	time.Sleep(20 * time.Millisecond)
	if w.gen.count() != 1 || r.clock.pending() != 0 {
		t.Errorf("停掉以后 Notify 不该有用：模型 %d 次，计时 %d", w.gen.count(), r.clock.pending())
	}
	if err := hub.Use(r.p); err != nil {
		t.Fatalf("停掉以后该能再启用：%v", err)
	}
	r.lf = r.p.current()
	r.notify("suzu", cv, 1)
	if w.gen.count() != 2 {
		t.Errorf("再启用以后该能整理：%d 次", w.gen.count())
	}
	hub.StopPlugin(ID)
}

// ⑤ 两个角色各一个库：同一个会话 id、各自的事件来源，通知哪个整理哪个，互不串库
func TestW5CharactersDontShareACursorOrAModel(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil, "suzu", "kaede")
	a, b := r.mems["suzu"], r.mems["kaede"]
	r.enable()
	for _, w := range []*writable{a, b} {
		w.initCursor(t, grp, 0)
		w.ev.addN(grp, 40)
	}
	r.notify("suzu", grp, 1)
	if a.gen.count() != 1 || b.gen.count() != 0 {
		t.Fatalf("只通知了 suzu：suzu %d 次，kaede %d 次", a.gen.count(), b.gen.count())
	}
	if sa, _ := a.cursor(grp); sa != 40 {
		t.Errorf("suzu 的游标 %d", sa)
	}
	if sb, _ := b.cursor(grp); sb != 0 || b.rows(t, "memory_jobs") != 0 {
		t.Errorf("kaede 的库不该动：游标 %d", sb)
	}
	r.notify("kaede", grp, 1)
	if b.gen.count() != 1 || a.gen.count() != 1 {
		t.Errorf("再通知 kaede：suzu %d 次，kaede %d 次", a.gen.count(), b.gen.count())
	}
	// 没有记忆的角色：通知什么也不做，不留计时
	before := r.clock.pending()
	r.p.Notify("nobody", grp)
	r.idle()
	if r.clock.pending() != before {
		t.Errorf("没有记忆的角色不该留计时")
	}
}

// 写入没开的角色（真库，切换日之前）：通知不排任何东西
func TestW5ReadOnlyMemoryIsLeftAlone(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	ro, err := memory.Open(context.Background(), w.file, nil, nil, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	r.p = r.newPlugin(func(o *Options) { o.Memories = func(string) *memory.Memory { return ro } })
	r.enable()
	r.p.Notify("suzu", cv)
	r.p.Summarize("suzu", cv, true)
	r.idle()
	if r.clock.pending() != 0 || w.gen.count() != 0 {
		t.Errorf("只读的不该排整理：计时 %d，模型 %d", r.clock.pending(), w.gen.count())
	}
}

// 启动扫描：没有游标的会话从现在起整理、不回填（模型 0 次）；有游标而后面还有新的才排一批；启动以后才有的新会话从头整理
func TestW5StartupScanDoesNotBackfillButFollowsUp(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } })
	w := r.mems["suzu"]
	lastA := w.ev.addN("console:a", 300)   // 没有游标：不回填
	midB := w.ev.addN("console:b", 10)     // 游标立在这里
	lastB := w.ev.addN("console:b", 10)    // 之后又来了 10 条：要排一批
	lastC := w.ev.addN("console:c", 5)     // 游标就在最后：没有新的
	w.initCursor(t, "console:b", midB.Seq) // 上次运行时 Bun / Go 留下的游标
	w.initCursor(t, "console:c", lastC.Seq)
	r.enable()
	r.idle()
	if seq, ok := w.cursor("console:a"); !ok || seq != lastA.Seq {
		t.Fatalf("没有游标的会话该从最新处立游标（不回填）：%d %v，最新是 %d", seq, ok, lastA.Seq)
	}
	if w.gen.count() != 1 || !strings.Contains(w.gen.promptOf(0), "第10句闲话") {
		t.Fatalf("只该整理 B 后面的 10 条：模型 %d 次", w.gen.count())
	}
	if seq, _ := w.cursor("console:b"); seq != lastB.Seq {
		t.Errorf("B 的游标 %d，该到 %d", seq, lastB.Seq)
	}
	if seq, _ := w.cursor("console:c"); seq != lastC.Seq {
		t.Errorf("C 不该动：%d", seq)
	}
	if !strings.Contains(r.logs.String(), "startup_no_backfill") {
		t.Errorf("日志该写不回填的原因码：\n%s", r.logs.String())
	}

	// 启动以后才出现的会话：第一条消息也是新的，不能当历史跳过
	w.ev.add("console:d", "启动以后才有的第一句：我养了一只猫。")
	w.ev.addN("console:d", 2)
	r.notify("suzu", "console:d", 3)
	r.advance(10 * time.Minute)
	if w.gen.count() != 2 || !strings.Contains(w.gen.promptOf(1), "我养了一只猫") {
		t.Fatalf("新会话该从头整理：模型 %d 次", w.gen.count())
	}
	// 邻居：A 在启动时就有，来了新通知也只整理新的，不回头补那 300 条
	w.ev.add("console:a", "启动以后 A 里的新话。")
	r.notify("suzu", "console:a", 1)
	r.advance(10 * time.Minute)
	if w.gen.count() != 3 || strings.Contains(w.gen.promptOf(2), "第1句闲话") || !strings.Contains(w.gen.promptOf(2), "启动以后 A 里的新话") {
		t.Errorf("A 只该整理新的：模型 %d 次", w.gen.count())
	}
}

// ⑦ 启动最多排 20 批（含续跑）；用完的会话等新通知，新通知照常整理，不受启动名额管
func TestW5StartupBatchLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } })
	w := r.mems["suzu"]
	var convs []string
	for i := 0; i < 25; i++ {
		c := fmt.Sprintf("console:s%02d", i)
		convs = append(convs, c)
		w.initCursor(t, c, 0)
		w.ev.addN(c, 3)
	}
	r.enable()
	r.idle()
	if w.gen.count() != 20 {
		t.Fatalf("启动最多排 20 批：模型调了 %d 次", w.gen.count())
	}
	if !strings.Contains(r.logs.String(), "startup_over_limit") {
		t.Errorf("日志该写超出名额的原因码")
	}
	// 没排上的会话来了新通知，照常整理
	left := 0
	for _, c := range convs {
		if seq, _ := w.cursor(c); seq == 0 {
			left++
			w.ev.add(c, "启动之后的新话。")
			r.notify("suzu", c, 1)
			r.advance(10 * time.Minute)
		}
	}
	if left != 5 || w.gen.count() != 25 {
		t.Errorf("没排上的有 %d 个，模型共调了 %d 次", left, w.gen.count())
	}
}

// ⑦ 启动那一串的续跑也占名额：一个会话有 450 条没整理要 3 批，名额只有 2 就只跑 2 批，剩下的等新通知
func TestW5StartupContinuationsUseTheSameBudget(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) {
		o.Characters = func() []string { return []string{"suzu"} }
		o.StartupBatches = 2
	})
	w := r.mems["suzu"]
	w.initCursor(t, cv, 0)
	last := w.ev.addN(cv, 450)
	r.enable()
	r.idle()
	if w.gen.count() != 2 {
		t.Fatalf("名额 2：模型调了 %d 次", w.gen.count())
	}
	if seq, _ := w.cursor(cv); seq != 400 {
		t.Errorf("游标 %d，该停在 400", seq)
	}
	r.notify("suzu", cv, 1) // 新通知：不受启动名额管，续完剩下的
	if seq, _ := w.cursor(cv); seq != last.Seq || w.gen.count() != 3 {
		t.Errorf("新通知该续完：游标 %d，模型 %d 次", seq, w.gen.count())
	}
}

// 没配启动扫描：没有游标的会话一律不动（免得把整段历史当新消息整理），日志只提一次
func TestW5NoScanConfiguredMeansNoBackfill(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.ev.addN(cv, 300)
	r.notify("suzu", cv, 5)
	r.advance(10 * time.Minute)
	if _, ok := w.cursor(cv); ok || w.gen.count() != 0 {
		t.Errorf("没有游标又没扫描：不该立游标、不该整理（模型 %d 次）", w.gen.count())
	}
	if n := strings.Count(r.logs.String(), "startup_no_backfill"); n != 1 {
		t.Errorf("日志该只提一次：%d\n%s", n, r.logs.String())
	}
}

// 启动扫描读不到会话：不立游标、不整理；事件来源好了以后下一条通知再扫，不把已有的历史当新消息。
// 扫描失败期间新出现的会话，重扫时分不出它是新的还是旧的，按旧的算（不回填，收紧的一边），它后面的消息照常整理
func TestW5ScanThatFailedIsRetriedOnTheNextNotification(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } })
	w := r.mems["suzu"]
	last := w.ev.addN("console:old", 100)
	w.ev.setErr(errors.New("库暂时读不了"))
	r.enable()
	r.idle()
	if _, ok := w.cursor("console:old"); ok {
		t.Fatal("读不到会话，不该立游标")
	}
	w.ev.setErr(nil)
	mid := w.ev.add("console:mid", "扫描失败期间来的新会话。")
	r.notify("suzu", "console:mid", 1) // 这一条触发重扫
	if seq, ok := w.cursor("console:old"); !ok || seq != last.Seq {
		t.Fatalf("重扫以后 old 该从最新处立游标：%d %v", seq, ok)
	}
	if seq, ok := w.cursor("console:mid"); !ok || seq != mid.Seq || w.gen.count() != 0 {
		t.Fatalf("扫描失败期间出现的会话按旧的算：%d %v，模型 %d 次", seq, ok, w.gen.count())
	}
	w.ev.add("console:mid", "重扫之后的新话。")
	r.notify("suzu", "console:mid", 1)
	r.advance(10 * time.Minute)
	if w.gen.count() != 1 || !strings.Contains(w.gen.promptOf(0), "重扫之后的新话") || strings.Contains(w.gen.promptOf(0), "第1句闲话") {
		t.Errorf("只该整理重扫之后的：模型 %d 次", w.gen.count())
	}
}

// 在没识完的图前面截断：前面的先整理，隔一会儿再看；图好了就带上描述，等够 10 分钟就按"未看清"整理，不卡住这个会话（⑧）
func TestW5WaitsForTheImageThenGivesUp(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		readyAt time.Duration // 图在第几分钟识好；0 = 一直没好
		want    string
	}{{"图识好了", 5 * time.Minute, "[图片：一只橘猫趴在键盘上]"}, {"一直没好", 0, "[图片（未看清）]"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(o *Options) { o.Quiet = 2 * time.Minute })
			w := r.mems["suzu"]
			r.enable()
			w.initCursor(t, cv, 0)
			w.ev.add(cv, "我给你看个东西。")
			w.ev.add(cv, "[图片]", func(e *memory.Event) { e.Images = []memory.Image{{Hash: "h1"}} })
			w.ev.add(cv, "你猜这是什么？") // 图后面还有话：只剩占位的一批不调模型，要有话才看得出调没调
			r.notify("suzu", cv, 3)
			r.advance(2 * time.Minute) // 到点：图前面的一句先整理
			if w.gen.count() != 1 || strings.Contains(w.gen.promptOf(0), "[图片") {
				t.Fatalf("图前面的先整理：模型 %d 次", w.gen.count())
			}
			if seq, _ := w.cursor(cv); seq != 1 {
				t.Fatalf("游标不能越过没识完的图：%d", seq)
			}
			for elapsed := 2 * time.Minute; elapsed < 12*time.Minute && w.gen.count() == 1; elapsed += time.Minute {
				if c.readyAt > 0 && elapsed >= c.readyAt {
					w.ev.mu.Lock()
					w.ev.evs[1].Images = []memory.Image{{Hash: "h1", Ready: true, Brief: "一只橘猫趴在键盘上"}}
					w.ev.mu.Unlock()
				}
				r.advance(time.Minute) // 没有任何新通知，全靠自己隔一会儿再看
			}
			if w.gen.count() != 2 || !strings.Contains(w.gen.promptOf(1), c.want) {
				t.Fatalf("图的那一批该带 %q：模型 %d 次", c.want, w.gen.count())
			}
			if seq, _ := w.cursor(cv); seq != 3 {
				t.Errorf("游标 %d", seq)
			}
		})
	}
}

// 事件来源出错：数不出来就记一笔原因码，不算整理失败（没调过模型）；好了以后下一条通知照常
func TestW5EventSourceErrorIsNotACountedFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	w.ev.setErr(errors.New("库暂时读不了：" + grp))
	r.notify("suzu", cv, 1)
	if st := w.state(t, cv); w.gen.count() != 0 || st.Failures != 0 {
		t.Fatalf("读不了事件：%+v，模型 %d 次", st, w.gen.count())
	}
	if log := r.logs.String(); !strings.Contains(log, "events_error") || strings.Contains(log, grp) {
		t.Errorf("日志该写原因码、不写错误原文：\n%s", log)
	}
	w.ev.setErr(nil)
	r.notify("suzu", cv, 1)
	if w.gen.count() != 1 {
		t.Errorf("好了以后该整理：%d 次", w.gen.count())
	}
}

// 整理出的事实只进库副本，真的走了写入路径：候选进了库，证据和游标在同一个事务里
func TestW5SummaryWritesThroughTheRealPath(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	e := w.ev.add(cv, "我下周三要去北京出差。")
	w.gen.reply = func(int, string) (string, error) { return candidateReply("主人下周三去北京出差", e.Seq), nil }
	r.notify("suzu", cv, 1)
	r.advance(10 * time.Minute)
	if w.rows(t, "facts") != 1 || w.rows(t, "memory_jobs") != 1 {
		t.Fatalf("事实 %d 条、job %d 条", w.rows(t, "facts"), w.rows(t, "memory_jobs"))
	}
	if seq, _ := w.cursor(cv); seq != e.Seq {
		t.Errorf("游标 %d", seq)
	}
	// 重复通知、重复到点：同一批不再整理（幂等键 + 游标）
	r.notify("suzu", cv, 3)
	r.advance(time.Hour)
	if w.gen.count() != 1 || w.rows(t, "facts") != 1 {
		t.Errorf("没有新东西不该再整理：模型 %d 次，事实 %d 条", w.gen.count(), w.rows(t, "facts"))
	}
}

// 旧的计时回调晚到（它已经在跑的时候被重置了）：代号对不上，当没来
func TestW5StaleTimerCallbackIsIgnored(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	lf := r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 5)
	r.notify("suzu", cv, 1)
	r.notify("suzu", cv, 1) // 重置了一次：第一个计时作废
	lf.s.mu.Lock()
	sl := lf.s.slots[skey{"suzu", cv}]
	lf.s.mu.Unlock()
	lf.s.timerFired(sl, false, 1)
	r.idle()
	if w.gen.count() != 0 {
		t.Fatalf("旧代号的回调不该触发整理：%d 次", w.gen.count())
	}
	lf.s.timerFired(sl, false, 2)
	r.idle()
	if w.gen.count() != 1 {
		t.Errorf("现在的代号该触发：%d 次", w.gen.count())
	}
}

// ⑤ 同一个角色的两个会话同时整理，都想替换同一条事实：整理写入时不取代任何有效事实（规格-记忆写入上线 W2），
// 两条都写成待确认、各自记下想取代谁（wants_supersede），旧的原样有效，一条 supersede 修订都没有
func TestW5TwoConversationsReplaceTheSameFactOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	r := newRigSeeded(t, nil, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope) VALUES ('FOLD', 'fact', '主人住在北京', 'owner', 'private', '[]', 'active', 1, 'owner')`); err != nil {
			t.Fatal(err)
		}
	})
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, "console:a", 0)
	w.initCursor(t, "console:b", 0)
	w.ev.addN("console:a", 40) // seq 1～40
	w.ev.addN("console:b", 40) // seq 41～80
	var arrived sync.WaitGroup
	arrived.Add(2)
	w.gen.block = func(context.Context) error { arrived.Done(); arrived.Wait(); return nil } // 两个会话都进了模型，再一起往下走
	w.gen.reply = func(_ int, prompt string) (string, error) {
		text, src := "主人搬到了上海", 1
		if strings.Contains(prompt, "[41] ") {
			text, src = "主人搬到了杭州", 41
		}
		return fmt.Sprintf(`{"episode_record":"no","ops":[{"op":"upsert_fact","kind":"fact","text":%q,"subject":"owner","visibility":"private","supersedes":"FOLD","src":[%d]}]}`, text, src), nil
	}
	r.p.Notify("suzu", "console:a")
	r.p.Notify("suzu", "console:b")
	r.idle()
	if w.gen.count() != 2 {
		t.Fatalf("两个会话各整理一次：%d", w.gen.count())
	}
	if st := w.text(t, `SELECT status FROM facts WHERE id = 'FOLD'`); st != "active" {
		t.Errorf("旧的不该被整理写入时换掉：%s", st)
	}
	if n := w.num(t, `SELECT count(*) FROM fact_revisions WHERE fact_id = 'FOLD' AND op = 'supersede'`); n != 0 {
		t.Errorf("写入时不该有 supersede 修订：%d 条", n)
	}
	if n := w.num(t, `SELECT count(*) FROM facts WHERE status = 'active'`); n != 1 {
		t.Errorf("有效的只剩旧的那一条：%d", n)
	}
	if n := w.num(t, `SELECT count(*) FROM fact_revisions WHERE op = 'add' AND after_json LIKE '%"wants_supersede":"FOLD"%'`); n != 2 {
		t.Errorf("两条待确认各记下想取代 FOLD：%d", n)
	}
	if n := w.num(t, `SELECT count(*) FROM facts WHERE status = 'candidate'`); n != 2 {
		t.Errorf("两个会话各写了一条待确认：%d", n)
	}
	if w.rows(t, "memory_jobs") != 2 {
		t.Errorf("两批都该记下：%d", w.rows(t, "memory_jobs"))
	}
}

// 启动扫描顺带查整理提示词（W3a）：用户改过的 summarize-v2.md 没有候选块的占位符，启动时就提醒一次（证据功能关着）；出厂的和补全的不提醒
func TestStartupScanWarnsWhenThePromptLostItsCandidateBlock(t *testing.T) {
	t.Parallel()
	chars := func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } }
	const warn = "（原因 prompt_stale）"
	userDir := func(t *testing.T, body string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "summarize-v2.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for _, c := range []struct {
		name   string
		store  func(t *testing.T) prompts.Store
		wantWn int
	}{
		{"出厂的：不提醒", func(t *testing.T) prompts.Store { return prompts.Store{Root: repoRoot} }, 0},
		{"用户改过、占位符还在：不提醒", func(t *testing.T) prompts.Store {
			return prompts.Store{Root: repoRoot, UserDir: userDir(t, "{{known}}\n{{candidates}}\n{{speakers}}\n{{transcript}}")}
		}, 0},
		{"用户改过、占位符没了：提醒一次", func(t *testing.T) prompts.Store {
			return prompts.Store{Root: repoRoot, UserDir: userDir(t, "{{known}}\n{{speakers}}\n{{transcript}}")}
		}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := c.store(t)
			r := newRigExtra(t, rigExtras{prompts: &store}, chars, nil)
			r.enable()
			r.idle()
			if n := strings.Count(r.logs.String(), warn); n != c.wantWn {
				t.Errorf("启动时提醒了 %d 次，应该 %d 次：\n%s", n, c.wantWn, r.logs.String())
			}
		})
	}
}

// 启动扫描顺带补提升（W3b）：记账模式下已经攒够证据的待确认事实，打开自动提升的那次启动提成有效；没打开自动提升什么都不动。日志只有计数
func TestStartupScanConfirmsWhatTheEvidenceAlreadyCovers(t *testing.T) {
	t.Parallel()
	chars := func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } }
	seed := func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope) VALUES ('CAND', 'fact', '主人喜欢喝茶', 'owner', 'private', '[]', 'candidate', 1, 'owner')`); err != nil {
			t.Fatal(err)
		}
	}
	for _, auto := range []bool{true, false} {
		name := "打开了自动提升"
		if !auto {
			name = "没打开：什么都不动"
		}
		t.Run(name, func(t *testing.T) {
			r := newRigExtra(t, rigExtras{memOpts: func(o *memory.Options) { o.AutoConfirm = auto }}, chars, seed)
			w := r.mems["suzu"]
			// 三个不同的天、本人说的三次证据（迁移之后才有这张表，直接往副本里放）
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(w.file))
			if err != nil {
				t.Fatal(err)
			}
			for d := 1; d <= 3; d++ {
				if _, err := db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES ('CAND', ?, 'qq:private:7001', ?, 'owner', 1, 1)`, fmt.Sprintf("2026-10-%02d", d), d); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			r.enable()
			r.idle()
			status := w.text(t, `SELECT status FROM facts WHERE id = 'CAND'`)
			logs := r.logs.String()
			const line = "启动时补提升待确认：看了 1 条，提升 1 条，0 条想取代的旧事实受保护"
			switch {
			case auto && (status != "active" || !strings.Contains(logs, line) || w.text(t, `SELECT confirmed_by FROM facts WHERE id = 'CAND'`) != "ai"):
				t.Errorf("打开了：该提升并记一行日志：状态 %s\n%s", status, logs)
			case !auto && (status != "candidate" || strings.Contains(logs, "补提升") || w.num(t, `SELECT count(*) FROM fact_revisions WHERE op = 'confirm'`) != 0):
				t.Errorf("没打开：一个字节不动、没有日志：状态 %s\n%s", status, logs)
			}
			if strings.Contains(logs, "主人喜欢喝茶") {
				t.Errorf("日志里不该有事实原文：\n%s", logs)
			}
		})
	}
}

// 启动扫描遇到不整理的角色：只开了 remember（整理没开）要说"后台整理没开，只有 remember 在写"，不能说"写入没开"（memwrite 刚记了写入已开）；
// 记忆本身只读才说"写入没开（write_disabled）"，和"没有记忆库"分开说（静默失败盘点 二-18）。每个角色只记一次
func TestStartupScanWarnsTheRightReasonForNonSummarizingCharacters(t *testing.T) {
	t.Parallel()
	chars := func(o *Options) { o.Characters = func() []string { return []string{"suzu"} } }
	const (
		onlyRemember = "后台整理没开，只有 remember 在写"
		readOnly     = "记忆库是只读的、写入没开（write_disabled），后台整理不管它"
	)
	for _, c := range []struct {
		name        string
		opts        func(*memory.Options)
		wantRemOnly int
		wantRO      int
	}{
		{"只开 remember：说整理没开", func(o *memory.Options) { o.SummarizeEnabled = false }, 1, 0},
		{"记忆只读：说写入没开", func(o *memory.Options) { o.Writable, o.SummarizeEnabled = false, false }, 0, 1},
		{"整理开着：都不说", func(o *memory.Options) {}, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRigExtra(t, rigExtras{memOpts: c.opts}, chars, nil)
			r.enable()
			r.idle()
			logs := r.logs.String()
			if n := strings.Count(logs, onlyRemember); n != c.wantRemOnly {
				t.Errorf("%q 出现 %d 次，应该 %d 次：\n%s", onlyRemember, n, c.wantRemOnly, logs)
			}
			if n := strings.Count(logs, readOnly); n != c.wantRO {
				t.Errorf("%q 出现 %d 次，应该 %d 次：\n%s", readOnly, n, c.wantRO, logs)
			}
		})
	}
}
