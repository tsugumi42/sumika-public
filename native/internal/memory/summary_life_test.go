package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 整理的生命周期、并发和日志（边界 ④⑤⑬㉑）：模型调用期间发生的事，旧的结果迟到、被取消、库被关掉，都不能写出半截或写出过期的

// ④ 模型调用期间有人忘掉 / 真删：世代号变了，这一批作废，什么都不写；下一次再整理时（世代号稳定）照常写
func TestGenerationChangedWhileTheModelRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.reply("yes", "主人明天考试。", cand("主人明天考试", "owner", 1))
	var forgot atomic.Bool
	r.gen.block = func(ctx context.Context) error {
		if forgot.CompareAndSwap(false, true) {
			_, err := r.m.BumpGeneration(ctx) // 管理页在模型还在想的时候点了"忘掉"
			return err
		}
		return nil
	}
	res, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
	if err != nil || res.Skipped != SkipGeneration || res.Counts[CountDroppedGeneration] != 1 {
		t.Fatalf("结果 %+v 错误 %v", res, err)
	}
	if len(r.factTexts()) != 0 || len(r.episodes()) != 0 || r.jobs() != 0 || r.cursor() != 0 {
		t.Errorf("作废的一批什么都不该写：事实 %v 片段 %v job %d 游标 %d", r.factTexts(), r.episodes(), r.jobs(), r.cursor())
	}
	// 邻居：世代号没变，慢一点的模型迟到返回照常写库
	r.gen.block = func(ctx context.Context) error { time.Sleep(30 * time.Millisecond); return nil }
	if res := r.run(); res.Skipped != "" || len(r.factTexts()) != 1 || r.cursor() != 1 {
		t.Errorf("世代号没变该照常写：%+v", res)
	}
}

// ④ 取消了的 ctx（插件停掉）：模型不管取消、照样返回，结果也不能写库；模型认取消、返回错误，同样不写
func TestCancelledContextWritesNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		block func(cancel context.CancelFunc) func(context.Context) error
	}{
		{"模型不理会取消，照样返回结果", func(cancel context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { cancel(); return nil }
		}},
		{"模型认取消，返回错误", func(cancel context.CancelFunc) func(context.Context) error {
			return func(ctx context.Context) error { cancel(); return ctx.Err() }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "console:suzune")
			r.say(ownerA, "我明天考试。")
			r.reply("yes", "主人明天考试。", cand("主人明天考试", "owner", 1))
			// 多试几次：写锁那里 ctx 取消和锁空闲同时就绪时不能靠运气（Go 的 select 随机挑）
			for i := 0; i < 20; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				r.gen.block = c.block(cancel)
				_, err := r.m.SummarizeBatch(ctx, r.conv, false)
				cancel()
				if err == nil {
					t.Fatalf("第 %d 次：ctx 取消了，该返回错误", i+1)
				}
			}
			if len(r.factTexts()) != 0 || len(r.episodes()) != 0 || r.jobs() != 0 || r.cursor() != 0 {
				t.Errorf("取消以后不该写：事实 %v job %d 游标 %d", r.factTexts(), r.jobs(), r.cursor())
			}
		})
	}
}

// ④ 模型还在想的时候库被关掉：返回 ErrClosed，不 panic，什么都没写（关库的人要等整理的协程退出，这里验的是万一没等）
func TestCloseWhileTheModelRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.reply("yes", "主人明天考试。", cand("主人明天考试", "owner", 1))
	r.gen.block = func(context.Context) error { return r.m.Close() }
	_, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("库关了该返回 ErrClosed：%v", err)
	}
}

// ⑬ 两个调用同时整理同一批（读到的游标一样）：同一批只写一次（幂等键），另一个说"已整理过"
func TestConcurrentSameBatchIsWrittenOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	r.reply("yes", "主人明天考试。", cand("主人明天考试", "owner", 1))
	var arrived sync.WaitGroup
	arrived.Add(2)
	r.gen.block = func(context.Context) error { arrived.Done(); arrived.Wait(); return nil } // 两个都读完游标、进了模型，再一起往下走
	results := make([]BatchResult, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := r.m.SummarizeBatch(context.Background(), r.conv, false)
			if err != nil {
				t.Errorf("第 %d 个：%v", i, err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	done, skipped := 0, 0
	for _, res := range results {
		switch res.Skipped {
		case "":
			done++
		case SkipDone:
			skipped++
		}
	}
	if done != 1 || skipped != 1 || len(r.factTexts()) != 1 || len(r.episodes()) != 1 || r.jobs() != 1 || r.cursor() != 1 {
		t.Errorf("该一个写、一个说已整理过：%+v，事实 %v 片段 %v job %d", results, r.factTexts(), r.episodes(), r.jobs())
	}
}

// ⑬ 旧的批迟到：这一批在模型里的时候，另一个调用已经整理了更长的一批，游标往前走了。旧的这一批作废，不能把游标拉回去，也不重复写
func TestStaleBatchCannotPullTheCursorBack(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	r.say(ownerA, "我明天考试。")
	var calls atomic.Int32
	entered, gate := make(chan struct{}), make(chan struct{})
	r.gen.block = func(context.Context) error {
		if calls.Add(1) == 1 { // 第一个调用卡在模型里
			close(entered)
			<-gate
		}
		return nil
	}
	r.gen.set(
		`{"episode_record":"no","ops":[`+cand("旧批写的事", "owner", 1)+`]}`,
		`{"episode_record":"no","ops":[`+cand("新批写的事", "owner", 1, 2)+`]}`)
	var old BatchResult
	var oldErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		old, oldErr = r.m.SummarizeBatch(context.Background(), r.conv, false)
	}()
	<-entered
	r.say(ownerA, "后天去旅行。") // 旧批读完以后又来了一条
	if res := r.run(); res.Cursor != 2 {
		t.Fatalf("新批 %+v", res)
	}
	close(gate)
	<-done
	if oldErr != nil || old.Skipped != SkipCursorMoved || old.Counts[CountDroppedCursor] != 1 {
		t.Fatalf("旧批该作废：%+v %v", old, oldErr)
	}
	if r.cursor() != 2 || r.jobs() != 1 || strings.Join(r.factTexts(), "|") != "新批写的事" {
		t.Errorf("游标 %d，job %d，事实 %v：只该有新批写的", r.cursor(), r.jobs(), r.factTexts())
	}
}

// ⑤ 两个会话互不串：一个会话的游标、事件、job 不碰另一个
func TestTwoConversationsDoNotMix(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune")
	other := "console:other"
	if _, err := r.m.InitCursor(context.Background(), other, 0); err != nil {
		t.Fatal(err)
	}
	r.say(ownerA, "这是第一个会话的话。")
	r.ev.add(other, ownerA, "这是第二个会话的话。")
	r.run()
	if p := r.gen.last().prompt; strings.Contains(p, "第二个会话的话") {
		t.Errorf("别的会话的事件混进来了：%s", p)
	}
	if res, err := r.m.SummarizeBatch(context.Background(), other, false); err != nil || res.Cursor != 2 {
		t.Fatalf("第二个会话 %+v %v", res, err)
	}
	if p := r.gen.last().prompt; strings.Contains(p, "第一个会话的话") || !strings.Contains(p, "第二个会话的话") {
		t.Errorf("第二个会话的提示词：%s", p)
	}
	if r.jobs() != 2 || r.cursor() != 1 {
		t.Errorf("job %d 游标 %d", r.jobs(), r.cursor())
	}
}

// ㉑ 日志和错误里只有计数和原因码：没有聊天原文、模型输出、事实正文、会话 id、号码、模型调用的错误原文
func TestLogsAndErrorsNeverHoldTheText(t *testing.T) {
	t.Parallel()
	const secret = "橘子汽水口令" // 编的暗号；不用数字串，免得隐私扫描把它当号码
	conv := "qq:group:5001001"
	r := newRig(t, conv)
	r.say(pingA, "我的银行卡号是"+secret+"，别告诉别人。")
	r.say(ownerA, "阿萍的住址是向阳路十二号。")
	var everything []string
	note := func(res BatchResult, err error) {
		everything = append(everything, r.sink.String())
		if err != nil {
			everything = append(everything, err.Error())
		}
		everything = append(everything, res.Skipped, countsText(counter(res.Counts)))
	}

	r.reply("yes", "阿萍说了卡号"+secret, cand("阿萍的银行卡号是"+secret, "qq:60001", 1), `{"op":"evidence","text":"`+secret+`"}`)
	note(r.m.SummarizeBatch(context.Background(), conv, false)) // 成功的一批
	r.say(pingA, "再来一句。")
	r.gen.set("好的，卡号是" + secret + "，已经整理好了。") // 格式级失败
	note(r.m.SummarizeBatch(context.Background(), conv, false))
	r.gen.err = errors.New("网关 502 key=sk-secret-" + secret)
	note(r.m.SummarizeBatch(context.Background(), conv, false)) // 模型调用出错

	all := strings.Join(everything, "\n")
	for _, leak := range []string{secret, "银行卡", "向阳路", "5001001", "qq:60001", "60001", "sk-secret", "502", "阿萍"} {
		if strings.Contains(all, leak) {
			t.Errorf("日志、错误、结果里不该有 %q：\n%s", leak, all)
		}
	}
	if !strings.Contains(all, "added_candidate=1") {
		t.Errorf("日志里该有计数：\n%s", all)
	}
}
