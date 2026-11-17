package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
)

// 主动联系的导演接线（规格-主动联系 第十五节第 1 步）：默认关；打开后到点只写日志，信号来自各平台库和窗口前台。临时数据目录，名字都是编的。

func proactiveRows(t *testing.T, a *App) []proactive.Row {
	t.Helper()
	rows, err := proactive.DBStore{DB: a.set.Trace()}.Recent(100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestProactiveOffByDefault(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	t.Cleanup(a.cleanup)
	if a.proactive != nil {
		t.Fatal("默认配置不该建导演")
	}
	a.runProactive() // 没建就什么也不做，不 panic
	if rows := proactiveRows(t, a); len(rows) != 0 {
		t.Fatalf("关着不该有日志：%+v", rows)
	}
	// 开了但没列角色：同样不建
	e2 := newEnv(t)
	e2.configure("proactive:\n  enabled: true\n")
	a2 := e2.open()
	t.Cleanup(a2.cleanup)
	if a2.proactive != nil {
		t.Fatal("没列角色不该建导演")
	}
}

func TestProactiveDueWakesThreadAndAnnotatesLog(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.configure(threadCfg)
	var fg atomic.Bool
	opt := e.options()
	opt.WindowForeground = fg.Load
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cleanup)
	if a.proactive == nil || a.pthreads == nil {
		t.Fatal("打开并列了角色，应当建导演和线的宿主")
	}
	ctx := context.Background()
	a.proactive.Tick(ctx, e.now) // 第一次只排下次
	if rows := proactiveRows(t, a); len(rows) != 0 {
		t.Fatalf("第一次只排期：%+v", rows)
	}
	e.setNow(e.now.Add(4 * time.Hour)) // 白天 16:00，没有任何主人发言
	a.proactive.Tick(ctx, e.now)
	waitFor(t, "线里这一轮跑完", func() bool {
		a.pthreads.mu.Lock()
		defer a.pthreads.mu.Unlock()
		return len(a.pthreads.runs) == 0
	})
	rows := proactiveRows(t, a)
	var wake *proactive.Row
	for i := range rows {
		if rows[i].Kind == proactive.KindWake {
			wake = &rows[i]
		}
	}
	// 默认假网关只回一句话、没有调工具：没表态，线按想了没开口收掉
	if wake == nil || wake.Character != testChar || wake.Reason != reasonModelNoStance || wake.Thread == "" {
		t.Fatalf("到点放行，叫了线里的一轮，结局补在同一行：%+v", rows)
	}
	if n := e.llmCalls(); n != 1 {
		t.Fatalf("线里一次请求：%d", n)
	}
}

func TestProactiveSignalsFromStoresAndWindow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.configure("proactive:\n  enabled: true\n  characters: [" + testChar + "]\n")
	var fg atomic.Bool
	opt := e.options()
	opt.WindowForeground = fg.Load
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cleanup)
	ctx := context.Background()

	sg, err := a.proactiveSignals(ctx, testChar, e.now)
	if err != nil || !sg.OwnerLastActive.IsZero() || !sg.LocalLastInput.IsZero() || sg.WindowForeground {
		t.Fatalf("没有主人发言：%+v %v", sg, err)
	}

	conv := "console:" + testChar
	st, err := a.set.ForConversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	add := func(kind string, at time.Time) {
		t.Helper()
		if _, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: kind, ActorID: kind, Kind: "message", Text: "测试", CreatedAt: at.UnixMilli()}, []string{testChar}); err != nil {
			t.Fatal(err)
		}
	}
	add("character", e.now.Add(-1*time.Minute)) // 她自己说的不算主人活跃
	add("owner", e.now.Add(-50*time.Hour))      // 窗口外的主人发言当不知道
	sg, _ = a.proactiveSignals(ctx, testChar, e.now)
	if !sg.OwnerLastActive.IsZero() {
		t.Fatalf("她自己的话和窗口外的发言都不算：%+v", sg)
	}
	add("owner", e.now.Add(-5*time.Minute))
	fg.Store(true)
	sg, err = a.proactiveSignals(ctx, testChar, e.now)
	if err != nil || !sg.OwnerLastActive.Equal(e.now.Add(-5*time.Minute).Truncate(time.Millisecond)) || !sg.LocalLastInput.Equal(sg.OwnerLastActive) || !sg.WindowForeground {
		t.Fatalf("本地主人 5 分钟前发过言、窗口在前台：%+v %v", sg, err)
	}

	// 信号接进导演：主人正在聊，到点被拦下，写一行 owner-chatting
	a.proactive.Tick(ctx, e.now)
	e.setNow(e.now.Add(4 * time.Hour))
	add("owner", e.now.Add(-2*time.Minute))
	a.proactive.Tick(ctx, e.now)
	rows := proactiveRows(t, a)
	if len(rows) != 1 || rows[0].Reason != proactive.ReasonOwnerChatting {
		t.Fatalf("主人刚发过言，应被拦下：%+v", rows)
	}
}
