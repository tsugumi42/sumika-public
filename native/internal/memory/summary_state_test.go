package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 整理的退避、停掉、重新开和"还有几条没整理"（W5，13.2.5）。库里的状态：失败次数和下次时间在 summary_state 同一行，重启不清零

func (r *rig) state() SummaryState {
	r.t.Helper()
	st, ok, err := r.m.SummaryState(context.Background(), r.conv)
	if err != nil || !ok {
		r.t.Fatalf("读状态：%v %v", ok, err)
	}
	return st
}

func (r *rig) fail() SummaryState {
	r.t.Helper()
	st, err := r.m.NoteFailure(context.Background(), r.conv)
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func TestBackoffDelay(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]time.Duration{0: 0, 1: 2 * time.Minute, 2: 4 * time.Minute, 3: 8 * time.Minute, 4: 16 * time.Minute,
		5: 32 * time.Minute, 6: time.Hour, 7: time.Hour, 100: time.Hour} {
		if got := BackoffDelay(n); got != want {
			t.Errorf("第 %d 次失败：等 %v，该等 %v", n, got, want)
		}
	}
}

// B3：失败次数和下次时间落库，第 n 次等 2ⁿ 分钟，满 6 次就不留下次时间（停了）；成功清零
func TestB3FailuresAreWrittenToTheCursorRow(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	for i, wait := range []time.Duration{2, 4, 8, 16, 32} {
		st := r.fail()
		if st.Failures != i+1 || st.RetryAt != r.clock.Add(wait*time.Minute).UnixMilli() || st.LastSeq != 0 {
			t.Fatalf("第 %d 次失败后 %+v，该等 %d 分钟、游标不动", i+1, st, wait)
		}
	}
	st := r.fail()
	if st.Failures != MaxFailures || st.RetryAt != 0 {
		t.Errorf("第 6 次该停（没有下次时间）：%+v", st)
	}
	if st = r.fail(); st.Failures != MaxFailures {
		t.Errorf("停了以后再记（force 失败）不再往上加：%+v", st)
	}
	// 成功清零
	r.reply("no", "", cand("主人明天考试", "owner", 1))
	res, err := r.m.SummarizeBatch(context.Background(), r.conv, true)
	if err != nil || res.Skipped != "" {
		t.Fatalf("force 整理：%+v %v", res, err)
	}
	if st = r.state(); st.Failures != 0 || st.RetryAt != 0 || st.LastSeq != 1 {
		t.Errorf("成功以后失败次数清零、游标挪过去：%+v", st)
	}
}

// B3：重启不清零。退避写在库里，关掉再打开还在；没有游标行的会话记不了失败
func TestB3BackoffSurvivesRestart(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.fail()
	r.fail()
	want := r.state()
	if err := r.m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, err := Open(context.Background(), r.file, nil, nil, Options{Writable: true, Character: "suzune"})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	got, ok, err := m2.SummaryState(context.Background(), r.conv)
	if err != nil || !ok || got.Failures != 2 || got.RetryAt != want.RetryAt {
		t.Errorf("重启以后 %+v %v，该还是 %+v", got, err, want)
	}
	// 邻居：没有游标行的会话，记失败什么都不写
	st, err := m2.NoteFailure(context.Background(), "console:nobody")
	if err != nil || st != (SummaryState{}) {
		t.Errorf("没立过游标的会话 %+v %v", st, err)
	}
	if _, ok, _ := m2.SummaryState(context.Background(), "console:nobody"); ok {
		t.Error("记失败不该顺手造出游标行")
	}
}

// B3 + B5：还在退避里不调模型；排队的整理轮到时才看，那时已过期就照常调
func TestB3B5BackoffIsCheckedWhenTheRunStarts(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.reply("no", "", cand("主人明天考试", "owner", 1))
	r.fail() // 等 2 分钟
	res := r.run()
	if res.Skipped != SkipBackoff || res.Cursor != 0 || res.Counts[CountSkippedBackoff] != 1 || r.gen.count() != 0 || r.cursor() != 0 {
		t.Fatalf("退避中：%+v 模型 %d 次", res, r.gen.count())
	}
	r.clock = r.clock.Add(119 * time.Second)
	if res := r.run(); res.Skipped != SkipBackoff || r.gen.count() != 0 {
		t.Errorf("差 1 秒还没到：%+v", res)
	}
	r.clock = r.clock.Add(2 * time.Second) // 到点
	res = r.run()
	if res.Skipped != "" || r.gen.count() != 1 || r.cursor() != 1 {
		t.Errorf("到点该照常整理：%+v 模型 %d 次", res, r.gen.count())
	}
	if st := r.state(); st.Failures != 0 || st.RetryAt != 0 {
		t.Errorf("成功以后退避清掉：%+v", st)
	}
}

// B4：force（压缩前、整理按钮、归档）不看退避，也不看"停了"
func TestB4ForceIgnoresBackoffAndStop(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.reply("no", "", cand("主人明天考试", "owner", 1))
	r.fail()
	for i := 0; i < MaxFailures; i++ {
		r.fail() // 停了
	}
	if res := r.run(); res.Skipped != SkipStopped || res.Counts[CountSkippedStopped] != 1 || r.gen.count() != 0 {
		t.Fatalf("停了：%+v", res)
	}
	res, err := r.m.SummarizeBatch(context.Background(), r.conv, true)
	if err != nil || res.Skipped != "" || r.gen.count() != 1 || r.cursor() != 1 {
		t.Errorf("force 该照常整理：%+v %v 模型 %d 次", res, err, r.gen.count())
	}
}

// 停了以后有新通知：再给一次机会（失败次数退到差一次就又停），这一次再失败就直接又停；没停的不动
func TestReopenSummaryGivesOneMoreTry(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	ctx := context.Background()
	r.fail()
	if ok, err := r.m.ReopenSummary(ctx, r.conv); ok || err != nil || r.state().Failures != 1 {
		t.Fatalf("没停的不该动：%v %v %+v", ok, err, r.state())
	}
	for i := 0; i < MaxFailures; i++ {
		r.fail()
	}
	ok, err := r.m.ReopenSummary(ctx, r.conv)
	if !ok || err != nil {
		t.Fatalf("停了的该重新开：%v %v", ok, err)
	}
	if st := r.state(); st.Failures != MaxFailures-1 || st.RetryAt != 0 {
		t.Errorf("重新开以后 %+v", st)
	}
	r.reply("no", "", cand("主人明天考试", "owner", 1))
	if res := r.run(); res.Skipped != "" || r.gen.count() != 1 {
		t.Errorf("重新开以后该能整理：%+v", res)
	}
	// 再来一轮：这一次失败了，只记一次就又停
	r.say(ownerA, "后天也有考试。")
	for i := 0; i < MaxFailures; i++ {
		r.fail()
	}
	r.m.ReopenSummary(ctx, r.conv)
	if st := r.fail(); st.Failures != MaxFailures || st.RetryAt != 0 {
		t.Errorf("再失败一次就又停：%+v", st)
	}
}

// 没带写入的记忆：退避的入口都说不能写，不静默当没事
func TestSummaryStateNeedsWritable(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	ro, err := Open(context.Background(), r.file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.Writable() || !r.m.Writable() {
		t.Fatalf("Writable：只读 %v 可写 %v", ro.Writable(), r.m.Writable())
	}
	if _, err := ro.NoteFailure(context.Background(), r.conv); !errors.Is(err, ErrNotWritable) {
		t.Errorf("NoteFailure：%v", err)
	}
	if _, err := ro.ReopenSummary(context.Background(), r.conv); !errors.Is(err, ErrNotWritable) {
		t.Errorf("ReopenSummary：%v", err)
	}
}

// Pending 和 SummarizeBatch 用同一个过滤：被丢的不数，游标之前的不数，没有游标行 ok 为假
func TestPendingCountsWhatSummarizeWouldKeep(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	ctx := context.Background()
	if n, ok, err := r.m.Pending(ctx, "console:nobody"); n != 0 || ok || err != nil {
		t.Fatalf("没有游标行：%d %v %v", n, ok, err)
	}
	r.say(ownerA, "第一句。")
	r.say(ownerA, "补录的。", func(e *Event) { e.Backfill = true })
	r.say(ownerA, "自检的。", func(e *Event) { e.Selftest = true })
	r.say(pingA, "陌生人私聊。", func(e *Event) { e.Seen = false })
	r.say(pingA, "撤回了。", func(e *Event) { e.Revoked = true })
	r.say(herA, "没发出去。", func(e *Event) { e.Unsent = true })
	r.say(ownerA, "工具结果。", func(e *Event) { e.Kind = "tool" })
	r.say(pingA, "第二句。")
	r.say(herA, "她回的。")
	if n, ok, err := r.m.Pending(ctx, r.conv); n != 3 || !ok || err != nil {
		t.Errorf("该数 3 条（第一句、第二句、她回的）：%d %v %v", n, ok, err)
	}
	// 游标之前的不数
	r.reply("no", "")
	r.run()
	if n, _, _ := r.m.Pending(ctx, r.conv); n != 0 {
		t.Errorf("整理完以后 %d", n)
	}
	r.say(ownerA, "又一句。")
	if n, _, _ := r.m.Pending(ctx, r.conv); n != 1 {
		t.Errorf("新来一条 %d", n)
	}
	// 邻居：别的会话的不数
	r.ev.add("console:other", ownerA, "别处的话。")
	if n, _, _ := r.m.Pending(ctx, r.conv); n != 1 {
		t.Errorf("别的会话的话混进来了：%d", n)
	}
}

// Pending 最多数到一批（200 条）：计时只要知道攒够 40 条没有
func TestPendingStopsAtOneBatch(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	for i := 0; i < 250; i++ {
		r.say(ownerA, "一句话。")
	}
	if n, _, _ := r.m.Pending(context.Background(), r.conv); n != 200 {
		t.Errorf("数到 %d，该封顶在 200", n)
	}
}

// More：读满一批或凑够字数截断了，调用方接着排下一批；读完了、在没识完的图前面截断的不算
func TestBatchResultMore(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	for i := 0; i < 201; i++ {
		r.say(ownerA, "一句话。")
	}
	r.reply("no", "")
	res := r.run()
	if res.Counts[CountRead] != 200 || !res.More || res.Cursor != 200 {
		t.Fatalf("第一批该读满 200 条、还有：%+v", res)
	}
	if res = r.run(); res.More || res.Cursor != 201 {
		t.Errorf("第二批读完了：%+v", res)
	}

	// 字数凑够截断
	r2 := newRig(t, "console:suzune")
	long := strings.Repeat("字", 4000)
	r2.say(ownerA, long)
	r2.say(ownerA, long)
	r2.reply("no", "")
	if res := r2.run(); res.Counts[CountBatchFull] != 1 || !res.More {
		t.Errorf("凑够字数该还有：%+v", res)
	}

	// 邻居：在没识完的图前面截断不是"接着读"
	r3 := newRig(t, "console:suzune")
	r3.say(ownerA, "前面一句。")
	r3.say(ownerA, "[图片]", func(e *Event) {
		e.At = r3.clock.UnixMilli()
		e.Images = []Image{{Hash: "h1"}}
	})
	r3.reply("no", "")
	if res := r3.run(); res.Counts[CountWaitImages] != 1 || res.More {
		t.Errorf("等识图不是接着读：%+v", res)
	}
}

// 日志里只写原因码：错误本身可能带服务商的错误体、会话 id
func TestFailureCodeNeverCarriesTheErrorText(t *testing.T) {
	t.Parallel()
	secret := errors.New("上游 502 sk-test-secret qq:group:30001")
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("%w（原因 x）", ErrBadOutput), "bad_output"},
		{&reasoned{"调后台模型", "model_call", secret}, "model_call"},
		{&reasoned{"读事件", "events_error", secret}, "events_error"},
		{fmt.Errorf("包了一层：%w", ErrBusy), "busy"},
		{ErrNoGenerator, "no_generator"},
		{ErrNoEvents, "no_events"},
		{ErrNotWritable, "not_writable"},
		{secret, "other"},
	} {
		got := FailureCode(c.err)
		if got != c.want || strings.Contains(got, "secret") || strings.Contains(got, "30001") {
			t.Errorf("%v → %q，该是 %q", c.err, got, c.want)
		}
	}
}
