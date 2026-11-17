package memplugin

// 压缩前强制整理（W7，规格-记忆-v2 13.2.7）：各种结局怎么交回切点、一批接一批、到点交回部分游标、超时以后照常写库、
// 插件停掉丢结果、两个会话互不等，最后经过真的内核看切点真的被限住。名字、会话 id、句子都是编的；库在临时目录里

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 钩子的调用：等待时限由调用方给
func (r *rig) compact(char, conv string, wait time.Duration) (upTo int64, limited bool) {
	r.t.Helper()
	upTo, limited, err := r.lf.beforeCompact(context.Background(), turn.BeforeCompact{CharacterID: char, Conversation: conv, Trigger: "rule", WaitUntil: time.Now().Add(wait)})
	if err != nil {
		r.t.Fatalf("钩子不该返回错误：%v", err)
	}
	return upTo, limited
}

// exec 直接改库副本（测试里模拟"别的批挪过游标"、重置游标）
func (w *writable) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(w.file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// freeOnce 返回放行函数：测试中途失败也会放行（收尾时），不然卡住的协程让 stop 一直等
func freeOnce(t *testing.T, release chan struct{}) func() {
	var once sync.Once
	f := func() { once.Do(func() { close(release) }) }
	t.Cleanup(f)
	return f
}

// waitFor 等条件成立（后台协程做完）
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等了 20 秒：%s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// 各种结局交回什么：做了一批 / 已整理过 / 游标被挪过 / 等识图 / 没有可整理的 → 重读游标、限住；
// 没选模型 / 忘掉期间作废 / 出错 → 不限制（13.2.7"交回什么"）
func TestW7ResultsMapToACut(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		setup       func(t *testing.T, w *writable)
		wantUpTo    int64
		wantLimited bool
		wantFail    int // 钩子以后库里记的连续失败次数
	}
	cases := []tc{
		{"做了一批", func(t *testing.T, w *writable) { w.ev.addN(cv, 5) }, 5, true, 0},
		{"没有可整理的", func(t *testing.T, w *writable) { w.initCursor(t, cv, 3) }, 3, true, 0},
		{"已整理过只挪游标", func(t *testing.T, w *writable) {
			w.ev.addN(cv, 5)
			if res, err := w.m.SummarizeBatch(context.Background(), cv, true); err != nil || res.Skipped != "" {
				t.Fatalf("先整理一遍：%+v %v", res, err)
			}
			w.exec(t, "UPDATE summary_state SET last_seq = 0 WHERE conversation = ?", cv) // 游标退回去，这一批的幂等键还在
		}, 5, true, 0},
		{"游标被别的批挪过", func(t *testing.T, w *writable) {
			w.ev.addN(cv, 9)
			w.gen.block = func(context.Context) error {
				w.exec(t, "UPDATE summary_state SET last_seq = 7 WHERE conversation = ?", cv)
				return nil
			}
		}, 7, true, 0},
		{"第一条是没识完的图", func(t *testing.T, w *writable) {
			w.ev.add(cv, "[图片]", func(e *memory.Event) { e.Images = []memory.Image{{Hash: "h1"}} })
			w.ev.add(cv, "你猜这是什么？")
		}, 0, true, 0},
		{"没选后台模型", func(t *testing.T, w *writable) { w.ev.addN(cv, 5); w.setModel("") }, 0, false, 0},
		{"忘掉期间算的作废", func(t *testing.T, w *writable) {
			w.ev.addN(cv, 5)
			w.gen.block = func(ctx context.Context) error { _, err := w.m.BumpGeneration(ctx); return err }
		}, 0, false, 0},
		{"模型出错", func(t *testing.T, w *writable) {
			w.ev.addN(cv, 5)
			w.gen.reply = func(int, string) (string, error) { return "", fmt.Errorf("网关 503") }
		}, 0, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, nil)
			w := r.mems["suzu"]
			r.enable()
			w.initCursor(t, cv, 0)
			if c.name == "没有可整理的" {
				w.exec(t, "UPDATE summary_state SET last_seq = 3 WHERE conversation = ?", cv)
			}
			c.setup(t, w)
			upTo, limited := r.compact("suzu", cv, time.Minute)
			if upTo != c.wantUpTo || limited != c.wantLimited {
				t.Errorf("交回 (%d, %v)，该是 (%d, %v)", upTo, limited, c.wantUpTo, c.wantLimited)
			}
			if st := w.state(t, cv); st.Failures != c.wantFail {
				t.Errorf("连续失败 %d 次，该是 %d", st.Failures, c.wantFail)
			}
			if strings.Contains(r.logs.String(), "网关 503") {
				t.Errorf("日志里不该有模型的错误原文：%s", r.logs)
			}
		})
	}
}

// 本地私聊、本地群聊、QQ 私聊、QQ 群：同样的做法，各会话的游标各管各的，钩子只动叫它的那一个
func TestW7EachScene(t *testing.T) {
	t.Parallel()
	scenes := []struct{ name, conv string }{
		{"本地私聊", "console:suzu"}, {"本地群聊", "console:group:7"}, {"QQ 私聊", "qq:private:20001:50001"}, {"QQ 群", "qq:group:30001"},
	}
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	for _, s := range scenes {
		w.initCursor(t, s.conv, 0)
		w.ev.addN(s.conv, 3)
	}
	for _, s := range scenes {
		before := map[string]int64{}
		for _, o := range scenes {
			before[o.conv], _ = w.cursor(o.conv)
		}
		upTo, limited := r.compact("suzu", s.conv, time.Minute)
		seq, _ := w.cursor(s.conv)
		if !limited || upTo != seq || seq == 0 {
			t.Errorf("%s：交回 (%d, %v)，游标 %d", s.name, upTo, limited, seq)
		}
		for _, o := range scenes {
			if now, _ := w.cursor(o.conv); o.conv != s.conv && now != before[o.conv] {
				t.Errorf("%s 的钩子动了 %s 的游标：%d → %d", s.name, o.name, before[o.conv], now)
			}
		}
	}
}

// 这个角色没有记忆、没开写入、会话没有游标又没配启动扫描：什么都不限制，也不调模型
func TestW7NothingToDoMeansNoLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.ev.addN(cv, 5) // 没有游标，没配 Characters：不回填
	for _, c := range []struct{ name, char string }{{"没有这个角色的记忆", "nobody"}, {"没有游标", "suzu"}} {
		if upTo, limited := r.compact(c.char, cv, time.Minute); upTo != 0 || limited {
			t.Errorf("%s：交回 (%d, %v)", c.name, upTo, limited)
		}
	}
	if w.gen.count() != 0 {
		t.Errorf("不该调模型：%d 次", w.gen.count())
	}
	ro := openMemory(t, fixedNow, standardFacts(), nil) // 只读的记忆
	lf, _ := newLife(t, only(ro), nil)
	if upTo, limited, err := lf.beforeCompact(context.Background(), turn.BeforeCompact{CharacterID: character, Conversation: cv, WaitUntil: time.Now().Add(time.Minute)}); upTo != 0 || limited || err != nil {
		t.Errorf("没开写入：交回 (%d, %v, %v)", upTo, limited, err)
	}
}

// More 为真就接着跑：401 条是 200 + 200 + 1 三批，一次钩子调用全做完，force 不看退避
func TestW7MoreKeepsRunning(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 401)
	w.exec(t, "UPDATE summary_state SET failures = 3, retry_at = ? WHERE conversation = ?", time.Now().Add(time.Hour).UnixMilli(), cv) // 退避中
	upTo, limited := r.compact("suzu", cv, time.Minute)
	if upTo != 401 || !limited || w.gen.count() != 3 {
		t.Fatalf("交回 (%d, %v)，模型 %d 次，该是 (401, true) 和 3 次", upTo, limited, w.gen.count())
	}
	if seq, _ := w.cursor(cv); seq != 401 {
		t.Errorf("游标 %d", seq)
	}
}

// 到点前交回已经推到的游标；交回以后那一批照常做完、写库，剩下的由后台排程接着做
func TestW7DeadlineReturnsPartialCursorAndTheBatchStillLands(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	// 先把第一批做完；短等待预算只用于卡住的第二批，不要求真实落库在 200 毫秒内完成。
	w.ev.addN(cv, 200)
	w.gen.reply = func(n int, _ string) (string, error) {
		if n == 2 {
			return candidateReply("超时以后才写下的事", 201), nil
		}
		return `{"episode_record":"no","ops":[]}`, nil
	}
	if upTo, limited := r.compact("suzu", cv, time.Minute); upTo != 200 || !limited {
		t.Fatalf("准备已完成的第一批：(%d, %v)", upTo, limited)
	}
	w.ev.addN(cv, 201)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	free := freeOnce(t, release)
	w.gen.block = func(context.Context) error {
		if w.gen.count() == 2 { // 第二批卡住
			entered <- struct{}{}
			<-release
		}
		return nil
	}
	start := time.Now()
	upTo, limited := r.compact("suzu", cv, compactMargin+200*time.Millisecond)
	if took := time.Since(start); took < 150*time.Millisecond || took > 3*time.Second {
		t.Errorf("该在到点前留余量处交回，用了 %v", took)
	}
	if upTo != 200 || !limited {
		t.Fatalf("第一批做完、第二批卡着：交回 (%d, %v)，该是 (200, true)", upTo, limited)
	}
	<-entered
	if w.rows(t, "facts") != 0 {
		t.Fatal("第二批还没放行，不该有事实")
	}
	free()
	waitFor(t, "第二批和后台接着的第三批做完", func() bool { seq, _ := w.cursor(cv); return seq == 401 })
	r.idle()
	if w.rows(t, "facts") != 1 || w.gen.count() != 3 {
		t.Errorf("超时以后的结果照常写库：事实 %d 条，模型 %d 次", w.rows(t, "facts"), w.gen.count())
	}
	if !strings.Contains(r.logs.String(), "compact_hook_timeout") {
		t.Errorf("该记一行到点交回：%s", r.logs)
	}
}

// 插件停掉：钩子马上交回"不限制"，在途的整理不写库（游标、事实、memory_jobs 都不变），stop 等它退出
func TestW7StopDropsTheResult(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	lf := r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 5)
	w.gen.reply = func(int, string) (string, error) { return candidateReply("停掉以后不该写下的事", 1), nil }
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	free := freeOnce(t, release)
	w.gen.block = func(context.Context) error { entered <- struct{}{}; <-release; return nil }
	type ret struct {
		upTo    int64
		limited bool
	}
	got := make(chan ret, 1)
	go func() { u, l := r.compact("suzu", cv, time.Minute); got <- ret{u, l} }()
	<-entered
	stopped := make(chan struct{})
	go func() { lf.stop(); close(stopped) }()
	select {
	case g := <-got:
		if g.upTo != 0 || g.limited {
			t.Errorf("停掉以后钩子该交回 (0, false)：%+v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("插件停掉，钩子还在等")
	}
	free()
	<-stopped
	if seq, _ := w.cursor(cv); seq != 0 || w.rows(t, "facts") != 0 || w.rows(t, "memory_jobs") != 0 {
		t.Errorf("停掉以后不该写：游标 %d，事实 %d，job %d", seq, w.rows(t, "facts"), w.rows(t, "memory_jobs"))
	}
	if st := w.state(t, cv); st.Failures != 0 {
		t.Errorf("停掉不算失败：%+v", st)
	}
	// 停掉以后再被调：什么都不做
	if u, l, _ := lf.beforeCompact(context.Background(), turn.BeforeCompact{CharacterID: "suzu", Conversation: cv}); u != 0 || l {
		t.Errorf("停掉以后该不限制：(%d, %v)", u, l)
	}
}

// 两个会话互不等：A 的模型卡着，B 的钩子照样做完
func TestW7ConversationsDontWaitForEachOther(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	const a, b = "qq:group:30001", "qq:group:30002"
	w.initCursor(t, a, 0)
	w.initCursor(t, b, 0)
	w.ev.addN(a, 3)
	w.ev.addN(b, 3)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	free := freeOnce(t, release)
	var once sync.Once
	w.gen.block = func(context.Context) error {
		first := false
		once.Do(func() { first = true })
		if first {
			entered <- struct{}{}
			<-release
		}
		return nil
	}
	doneA := make(chan struct{})
	go func() { r.compact("suzu", a, time.Minute); close(doneA) }()
	<-entered
	doneB := make(chan [2]any, 1)
	go func() { u, l := r.compact("suzu", b, time.Minute); doneB <- [2]any{u, l} }()
	select {
	case g := <-doneB:
		if g[0] != int64(6) || g[1] != true {
			t.Errorf("B 交回 %v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B 在等 A")
	}
	if seq, _ := w.cursor(a); seq != 0 {
		t.Errorf("A 还卡着，游标不该动：%d", seq)
	}
	free()
	<-doneA
	if seq, _ := w.cursor(a); seq != 3 {
		t.Errorf("A 的游标 %d", seq)
	}
}

// 同一个会话：压缩前的整理等排程里正在跑的那一批，不同时调两次模型
func TestW7SameConversationRunsOneAtATime(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	w := r.mems["suzu"]
	r.enable()
	w.initCursor(t, cv, 0)
	w.ev.addN(cv, 40)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	free := freeOnce(t, release)
	var once sync.Once
	w.gen.block = func(context.Context) error {
		first := false
		once.Do(func() { first = true })
		if first {
			entered <- struct{}{}
			<-release
		}
		return nil
	}
	r.p.Notify("suzu", cv) // 40 条：排程立刻整理，卡在模型上
	<-entered
	done := make(chan [2]any, 1)
	go func() { u, l := r.compact("suzu", cv, time.Minute); done <- [2]any{u, l} }()
	time.Sleep(50 * time.Millisecond)
	if w.gen.peakLive() != 1 {
		t.Fatalf("同一个会话不该同时调两次模型：%d", w.gen.peakLive())
	}
	free()
	g := <-done
	r.idle()
	if g[0] != int64(40) || g[1] != true {
		t.Errorf("等排程那批做完再交回：%v", g)
	}
	if w.gen.peakLive() != 1 {
		t.Errorf("同时调模型的最多 %d", w.gen.peakLive())
	}
}

// 经过真的内核：识图没好，记忆只能整理到第 6 条；压缩的切点被限住，不越过第 7 条。不装插件时切在最后一轮
func TestW7KernelCutIsLimitedByMemory(t *testing.T) {
	t.Parallel()
	for _, withMemory := range []bool{false, true} {
		t.Run(fmt.Sprintf("记忆%v", withMemory), func(t *testing.T) {
			k := newKernelWith(t, func(c *turn.Config) { c.Context.KeepTokens = 50 })
			clock := newClock()
			logs := &logSink{}
			w := newWritable(t, clock, character, logs, nil)
			conv := "console:" + character
			var owners []int64
			for i := 0; i < 6; i++ {
				for _, who := range []string{"owner", "character"} {
					id := "owner"
					if who == "character" {
						id = character
					}
					text := strings.Repeat(string([]rune("甲乙丙丁戊己")[i]), 300)
					seq, err := k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: who, ActorID: id, Origin: who, Kind: "message",
						Text: text, CreatedAt: time.Now().UnixMilli()}, []string{character})
					if err != nil {
						t.Fatal(err)
					}
					if who == "owner" {
						owners = append(owners, seq)
					}
					// 记忆那边看到同样的事件（seq 一一对应）；第 7 条（第四轮主人那句）是还没识完的图
					w.ev.add(conv, text, func(e *memory.Event) {
						e.Seq, e.ActorKind, e.ActorID, e.Origin = seq, who, id, who
						if seq == 7 {
							e.Images = []memory.Image{{Hash: "h1"}}
						}
					})
				}
			}
			if owners[3] != 7 || owners[5] != 11 {
				t.Fatalf("会话里的 seq 排得不对：%v", owners)
			}
			w.initCursor(t, conv, 0)
			if withMemory {
				p := New(Options{Memories: func(c string) *memory.Memory {
					if c == character {
						return w.m
					}
					return nil
				}, Logf: logs.logf, Now: clock.Now, After: clock.After})
				if err := k.hub.Use(p); err != nil {
					t.Fatal(err)
				}
			}
			c, err := k.hub.Compact(character, conv, "manual", 0)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := k.st.Events.Query("SELECT from_seq FROM context_epochs")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var froms []int64
			for rows.Next() {
				var f int64
				rows.Scan(&f)
				froms = append(froms, f)
			}
			want := owners[5]
			if withMemory {
				want = owners[3] // 记忆只整理到第 6 条，切点不越过第 7 条
			}
			if len(froms) != 1 || froms[0] != want {
				t.Fatalf("切点：%v，该是 [%d]（压缩结果 %+v）", froms, want, c)
			}
			if seq, _ := w.cursor(conv); withMemory && seq != 6 || !withMemory && seq != 0 {
				t.Errorf("记忆游标 %d", seq)
			}
		})
	}
}
