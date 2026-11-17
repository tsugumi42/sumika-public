package turn_test

// 聊天记录按平台分库（规格-聊天记录按平台分库 第五节第 3 步、第八节第 5 条），用真开的两个平台库（本地、QQ，共用一个 trace）测内核：
// 本地主窗口和 QQ 会话同时跑，各自的事件只进自己平台的库、QQ 交回在 QQ 库里核；没登记的渠道不跑、哪个库都不写；
// 建 Hub 时收拾上一个进程没收尾的只扫一次，之后在跑的回合不会被当成没收尾

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

// channels 是一个库里 events 按渠道数的条数
func channels(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	rows, err := st.Events.Query(`SELECT channel, count(*) FROM events GROUP BY channel`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ch string
		var n int
		if err := rows.Scan(&ch, &n); err != nil {
			t.Fatal(err)
		}
		out[ch] = n
	}
	return out
}

func TestSplitLocalAndQQ(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{Reply: llmtest.Text("好的")})
	group := "qq:group:split-1"
	friend := audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"qq:1"}}
	seq := k.Append(group, "human", "qq:1", "群里有人说话")
	// 两边同时开跑：QQ 那一轮和主窗口那一句都在路上
	qq := k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: group, Scope: "public", Seqs: []int64{seq}, Text: "群里有人说话", Audience: friend, Addressed: true})
	local := k.Hub.Send(turntest.CharacterID, "主人在主窗口说话")
	rq := k.Wait(qq)
	k.Wait(local)
	// 交回在 QQ 库里核：选错库就查不到那一行、报错
	lines := k.Commit(group, rq)
	if len(lines) != 1 {
		t.Fatalf("交回：%+v", lines)
	}
	loc, q := channels(t, k.StoreFor("console:"+turntest.CharacterID)), channels(t, k.StoreFor(group))
	// 本地库：主人那句、她的回复（内核自己存）；QQ 库：群友那句、交回的她那句（中间件存）。哪边都没有另一边的
	if len(loc) != 1 || loc["console"] != 2 || len(q) != 1 || q["qq"] != 2 {
		t.Fatalf("各进各的库：本地 %v，QQ %v", loc, q)
	}
}

// 真的同时：本地那一轮卡在请求里的时候，QQ 那一轮跑完、交回，这时 QQ 的两句只在 QQ 库、本地库里只有主人那句；放开以后本地的回复进本地库
func TestSplitConcurrent(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	held := make(chan struct{})
	k := turntest.New(t, turntest.Options{Reply: llmtest.Text("好的"), Before: func(ctx context.Context, _ int, body string) {
		if strings.Contains(body, "主人卡着的那句") {
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}})
	local := k.Hub.Send(turntest.CharacterID, "主人卡着的那句")
	<-held
	group := "qq:group:split-3"
	seq := k.Append(group, "human", "qq:1", "群里一句")
	r := k.Wait(k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: group, Scope: "public", Seqs: []int64{seq}, Text: "群里一句",
		Audience: audience.Audience{Channel: "qq", Scene: "group"}, Addressed: true}))
	k.Commit(group, r)
	console := "console:" + turntest.CharacterID
	if loc, q := channels(t, k.StoreFor(console)), channels(t, k.StoreFor(group)); len(loc) != 1 || loc["console"] != 1 || len(q) != 1 || q["qq"] != 2 {
		close(release)
		t.Fatalf("本地卡着的时候：本地 %v，QQ %v", loc, q)
	}
	close(release)
	k.Wait(local)
	if loc := channels(t, k.StoreFor(console)); loc["console"] != 2 {
		t.Fatalf("放开以后本地的回复进本地库：%v", loc)
	}
}

// 压缩快照写进会话所在的库（本地主窗口的进本地库），下一轮建实例时从同一个库读到它、换上
func TestSplitCompactEpoch(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{
		Replies: []llmtest.Reply{llmtest.Text("主人这几天在准备考试，每天复习到很晚，答应了周末一起去看电影。"), llmtest.Text("好的")},
		Mutate:  func(c *turn.Config) { c.Context.KeepTokens = 50 },
	})
	console := "console:" + turntest.CharacterID
	for i := range 6 {
		k.Append(console, "owner", "owner", strings.Repeat(string([]rune("甲乙丙丁戊己")[i]), 300))
		k.Append(console, "character", turntest.CharacterID, strings.Repeat(string([]rune("子丑寅卯辰巳")[i]), 300))
	}
	c, err := k.Hub.Compact(turntest.CharacterID, console, "manual", 0)
	if err != nil || !c.OK {
		t.Fatalf("压缩：%+v %v", c, err)
	}
	var local, qq int
	k.StoreFor(console).Events.QueryRow(`SELECT count(*) FROM context_epochs`).Scan(&local)
	k.StoreFor("qq:group:x").Events.QueryRow(`SELECT count(*) FROM context_epochs`).Scan(&qq)
	if local != 1 || qq != 0 {
		t.Fatalf("快照只进本地库：本地 %d 行，QQ %d 行", local, qq)
	}
	k.Send("接着说")
	if b := k.Bodies()[1]; !strings.Contains(b, "准备考试") || strings.Contains(b, strings.Repeat("甲", 300)) {
		t.Fatalf("下一轮换上了这份快照（摘要在、切掉的原文不在）：%s", b)
	}
}

// QQ 回合说完没交回：下一轮开始照库重写那一段，要读 QQ 库（群友那句还在、没交回的她那句不在）；读错库就连群友那句一起没了
func TestSplitRewriteFromQQ(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{Replies: []llmtest.Reply{llmtest.Text("第一句回话"), llmtest.Text("第二句回话")}})
	group := "qq:group:split-2"
	friend := audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"qq:1"}}
	k.Say(group, friend, "public", "human", "qq:1", "大家好")
	k.Say(group, friend, "public", "human", "qq:1", "再说一句")
	if b := k.Bodies()[1]; !strings.Contains(b, "大家好") || strings.Contains(b, "第一句回话") {
		t.Fatalf("第二轮的历史照 QQ 库重写：群友那句在、没交回的那句不在：%s", b)
	}
}

func TestSplitUnknownChannel(t *testing.T) {
	t.Parallel()
	k := turntest.New(t, turntest.Options{})
	o := k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: "tg:private:1", Text: "你好",
		Audience: audience.Audience{Channel: "tg", Scene: "private"}, Addressed: true})
	select {
	case r := <-o:
		if r.Err == nil || !strings.Contains(r.Err.Error(), `"tg"`) {
			t.Fatalf("没登记的渠道要报错：%+v", r)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("30 秒没回")
	}
	if k.Requests() != 0 {
		t.Fatal("没登记的渠道不该请求模型")
	}
	for _, conv := range []string{"console:" + turntest.CharacterID, "qq:group:x"} {
		if n := channels(t, k.StoreFor(conv)); len(n) != 0 {
			t.Fatalf("不落进别的库：%s 里有 %v", conv, n)
		}
	}
}

// 建 Hub 时只扫一次：两个平台也只有一行"上一个进程没收尾"；之后建实例、跑回合都不再扫，在跑的回合不会被当成没收尾
func TestSplitSweepOnce(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	tr := set.TraceStore()
	if err := tr.TurnStart("old", turntest.CharacterID, "qq:group:old", "qq", 1000); err != nil {
		t.Fatal(err)
	}
	var logs []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := turn.NewHub(ctx, turn.Config{Logf: func(f string, a ...any) { logs = append(logs, f) }, Now: func() time.Time { return time.UnixMilli(2000) }}, set, nil, nil)
	_ = hub
	swept := 0
	for _, l := range logs {
		if strings.Contains(l, "上一个进程没收尾") {
			swept++
		}
	}
	var outcome string
	set.Trace().QueryRow(`SELECT coalesce(outcome, '') FROM turns WHERE id = 'old'`).Scan(&outcome)
	if swept != 1 || outcome != "interrupted" {
		t.Fatalf("上一个进程没收尾的记成被打断、只扫一次：扫了 %d 次，结局 %q", swept, outcome)
	}

	// 跑回合不再扫：一个回合正在跑（请求卡住）时，记录里它还没有结局
	held, release := make(chan struct{}), make(chan struct{})
	k := turntest.New(t, turntest.Options{Before: func(ctx context.Context, _ int, body string) {
		if strings.Contains(body, "第一句") { // 两个会话同时请求时序号会重号，按请求体认
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}})
	o := k.Hub.Send(turntest.CharacterID, "第一句")
	<-held
	// 卡着的时候另一个会话来一轮（建实例、选库）：不该把卡着的那一轮当成没收尾
	group := "qq:group:other"
	seq := k.Append(group, "human", "qq:1", "群里一句")
	other := k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: group, Scope: "public", Seqs: []int64{seq}, Text: "群里一句",
		Audience: audience.Audience{Channel: "qq", Scene: "group"}, Addressed: true})
	k.Wait(other)
	var open int
	k.Set.Trace().QueryRow(`SELECT count(*) FROM turns WHERE conversation = ? AND ended_at IS NULL`, "console:"+turntest.CharacterID).Scan(&open)
	close(release)
	k.Wait(o)
	if open != 1 {
		t.Fatalf("卡着的那一轮还在跑，不该有结局：没结局的 %d 个", open)
	}
}
