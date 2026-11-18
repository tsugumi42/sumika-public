package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
)

// 悄悄话处理那一轮里 remember 写的事实带来源（规格-群聊引用和路由 8.3）：批号、来源会话、被引事件 id 存进 facts.whisper_json，
// 召回时〔来源〕前先写明“悄悄话”，记忆页详情读得到。来源只由宿主给：不在悄悄话会话里、缺批号或来源会话的都不写。
// 名字、会话、话都是编的，库在 t.TempDir()。

// whisperEvents 让悄悄话会话里主人那条交代像真库里那样带“自检”标记（悄悄话事件都带，不进整理），别的会话照常读
func whisperEvents(t *testing.T, conv string, selftest bool) *sqlEvents {
	t.Helper()
	ev := newSQLEvents(t)
	ev.bound = func(c string, after int64, limit int) ([]Event, error) {
		if c != conv || after != 0 {
			return nil, nil
		}
		return []Event{{Seq: 1, Conversation: conv, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我喜欢蓝色纸鹤，记一下", Seen: true, Selftest: selftest}}, nil
	}
	return ev
}

var whisperSayer = Audience{Channel: "console", Scene: "private", PrivateOK: true}

func rememberWhisper(t *testing.T, m *Memory, conv string, ws *WhisperSource) ToolResult {
	t.Helper()
	a := whisperSayer
	a.Conversation = conv
	res, err := m.Remember(context.Background(), a, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人喜欢蓝色纸鹤", SourceQuote: "蓝色纸鹤", Temporal: "stable", Whisper: ws})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRememberWhisperCarriesSource(t *testing.T) {
	t.Parallel()
	conv := audience.WhisperConversation("suzune", "qq:group:ga")
	file := migratedDB(t, nil)
	m, err := Open(context.Background(), file, nil, nil, Options{Writable: true, Character: "suzune", Logf: (&logSink{}).logf, Events: whisperEvents(t, conv, true), Now: func() time.Time { return toolsNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ws := &WhisperSource{Batch: "B1", Source: "qq:group:ga", Quotes: []string{"e1", "e2"}}
	if res := rememberWhisper(t, m, conv, ws); res != (ToolResult{Text: "记住了。"}) {
		t.Fatalf("悄悄话会话里主人的交代该能当原话：%+v", res)
	}
	raw := str(t, m, `SELECT coalesce(whisper_json, '') FROM facts`)
	got := parseWhisper(raw)
	if got == nil || got.Batch != "B1" || got.Source != "qq:group:ga" || strings.Join(got.Quotes, ",") != "e1,e2" {
		t.Fatalf("来源没存对：%q", raw)
	}
	if c := str(t, m, `SELECT conversation FROM facts`); c != conv {
		t.Fatalf("记录会话仍是悄悄话会话：%q", c)
	}
	// 召回（memory_search）时来源前先写明悄悄话
	a := whisperSayer
	a.Conversation = conv
	r, err := m.Search(context.Background(), a, "蓝色纸鹤")
	if err != nil || !strings.Contains(r.Text, "〔来源：悄悄话（批号=B1；来源会话=qq:group:ga；被引事件=e1、e2）；") {
		t.Fatalf("召回的来源要写明悄悄话：%v %q", err, r.Text)
	}
	// 记忆页详情
	b, err := OpenBrowser(file, BrowserOptions{})
	if err != nil {
		t.Skipf("开浏览器：%v", err)
	}
	defer b.Close()
	d, err := b.Detail(context.Background(), "f:"+str(t, m, `SELECT id FROM facts`))
	if err != nil || d.Whisper == nil || d.Whisper.Batch != "B1" || !strings.Contains(d.Whisper.Text(), "来源会话 qq:group:ga") {
		t.Fatalf("记忆页详情要带悄悄话来源：%v %+v", err, d.Whisper)
	}
}

// 来源不是模型能填的：不在悄悄话会话里，宿主给了也不写；缺批号或来源会话的半截来源不写；普通自检会话里的交代仍不当原话。
func TestRememberWhisperSourceOnlyWhereTrusted(t *testing.T) {
	t.Parallel()
	conv := audience.WhisperConversation("suzune", "qq:group:ga")
	ok := &WhisperSource{Batch: "B1", Source: "qq:group:ga"}

	// 普通本地私聊（不是悄悄话会话）：带了来源也不写
	ev := newSQLEvents(t)
	ev.add(ownerSays(tlMain, "我喜欢蓝色纸鹤，记一下", time.Minute))
	m, _ := openTools(t, ev)
	a := whisperSayer
	a.Conversation = tlMain
	if res, err := m.Remember(context.Background(), a, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人喜欢蓝色纸鹤", SourceQuote: "蓝色纸鹤", Temporal: "stable", Whisper: ok}); err != nil || res.IsError {
		t.Fatalf("普通私聊照常记：%v %+v", err, res)
	}
	if raw := str(t, m, `SELECT coalesce(whisper_json, '') FROM facts`); raw != "" {
		t.Fatalf("不在悄悄话会话里，来源不能写：%q", raw)
	}

	// 悄悄话那一轮里认不出来历（来源没给全）：不写，不能写下一条没有来历的记忆
	for name, ws := range map[string]*WhisperSource{"缺批号": {Source: "qq:group:ga"}, "缺来源会话": {Batch: "B1"}, "空白": {Batch: " ", Source: " "}, "没有": nil} {
		m, _ := openTools(t, whisperEvents(t, conv, true))
		if res := rememberWhisper(t, m, conv, ws); !res.IsError || !strings.Contains(res.Text, "认不出这条悄悄话的来历") {
			t.Fatalf("%s：该拒绝：%+v", name, res)
		}
		if n := count(t, m, `SELECT count(*) FROM facts`); n != 0 {
			t.Errorf("%s：不该写进任何东西：%d", name, n)
		}
	}

	// 自检标记只在悄悄话会话里放行：别的会话里带标记的话还是不当原话
	ev2 := newSQLEvents(t)
	ev2.bound = func(c string, after int64, limit int) ([]Event, error) {
		if c != tlMain || after != 0 {
			return nil, nil
		}
		return []Event{{Seq: 1, Conversation: tlMain, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我喜欢蓝色纸鹤，记一下", Seen: true, Selftest: true}}, nil
	}
	m2, _ := openTools(t, ev2)
	a.Conversation = tlMain
	res, err := m2.Remember(context.Background(), a, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人喜欢蓝色纸鹤", SourceQuote: "蓝色纸鹤", Temporal: "stable"})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "没有找到对应的所有者原话") {
		t.Fatalf("自检会话里的话不能当原话：%v %+v", err, res)
	}
}

func TestWhisperSourceCleansAndBounds(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("长", 500)
	var qs []string
	for i := 0; i < 40; i++ {
		qs = append(qs, "e"+strings.Repeat("x", i))
	}
	w := WhisperSource{Batch: "B\n1", Source: "来源‮" + long, Quotes: append(qs, "")}.clean()
	if strings.ContainsAny(w.Batch+w.Source, "\n‮") || len([]rune(w.Source)) > maxWhisperField+1 || len(w.Quotes) != maxWhisperQuotes {
		t.Fatalf("要压成一行、截短、限条数：%+v", w)
	}
	if parseWhisper("不是 JSON") != nil || parseWhisper(`{"batch":"B"}`) != nil || parseWhisper("") != nil {
		t.Fatal("坏的、缺字段的、空的读成没有")
	}
}

// 主人只引用、没写字：占位文字不是主人说的话，被引的是别人的话，不能拿它当“主人原话”写记忆或忘记忆
func TestRememberWhisperPlaceholderIsNotOwnerWords(t *testing.T) {
	t.Parallel()
	conv := audience.WhisperConversation("suzune", "qq:group:ga")
	ev := newSQLEvents(t)
	ev.bound = func(c string, after int64, limit int) ([]Event, error) {
		if c != conv || after != 0 {
			return nil, nil
		}
		return []Event{{Seq: 1, Conversation: conv, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: audience.WhisperNoSay, Seen: true, Selftest: true}}, nil
	}
	m, _ := openTools(t, ev)
	a := whisperSayer
	a.Conversation = conv
	ws := &WhisperSource{Batch: "B1", Source: "qq:group:ga", Quotes: []string{"e1"}}
	res, err := m.Remember(context.Background(), a, RememberArgs{InputSeqs: []int64{1}, Kind: "fact", Text: "主人喜欢蓝色纸鹤", SourceQuote: "没有写字", Temporal: "stable", Whisper: ws})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "没有找到对应的所有者原话") {
		t.Fatalf("只引用、没写字的悄悄话不能当主人原话：%v %+v", err, res)
	}
	if res, err := m.Forget(context.Background(), a, ForgetArgs{InputSeqs: []int64{1}, ID: "ABCDEF", Reason: "试试", SourceQuote: "没有写字"}); err != nil || !res.IsError || !strings.Contains(res.Text, "没有找到对应的所有者原话") {
		t.Fatalf("忘记忆同一道门槛：%v %+v", err, res)
	}
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 0 {
		t.Fatalf("不该写进任何东西：%d", n)
	}
}

// 悄悄话处理里记的事实不进共用池：就算模型说“不是专属”，也只留在她自己的库里；同样的话在普通私聊里记，则会共享（对照）
func TestRememberWhisperIsNeverShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conv := audience.WhisperConversation("aria", "qq:group:ga")
	events := newSQLEvents(t)
	events.bound = func(c string, after int64, limit int) ([]Event, error) {
		if after != 0 {
			return nil, nil
		}
		switch c {
		case conv:
			return []Event{{Seq: 1, Conversation: conv, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我喜欢蓝色纸鹤，记一下", Seen: true, Selftest: true}}, nil
		case tlMain:
			return []Event{{Seq: 1, Conversation: tlMain, At: evAt(time.Minute), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我喜欢红色纸鹤，记一下", Seen: true}}, nil
		}
		return nil, nil
	}
	pool, err := OpenOwnerPool(ctx, filepath.Join(t.TempDir(), "owner-memory.db"), nil, nil, Options{Writable: true, SourceEvents: func(string) (EventSource, error) { return events, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	open := func(char string) *Memory {
		m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: char, Writable: true, Shared: pool, Events: events, Now: func() time.Time { return toolsNow }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	a, b := open("aria"), open("sora")
	no := false
	w := whisperSayer
	w.Conversation = conv
	if res, err := a.Remember(ctx, w, RememberArgs{InputSeqs: []int64{1}, Kind: "preference", Text: "主人喜欢蓝色纸鹤", SourceQuote: "蓝色纸鹤", Temporal: "stable", OnlyHer: &no, Whisper: &WhisperSource{Batch: "B1", Source: "qq:group:ga"}}); err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	p := whisperSayer
	p.Conversation = tlMain
	if res, err := a.Remember(ctx, p, RememberArgs{InputSeqs: []int64{1}, Kind: "preference", Text: "主人喜欢红色纸鹤", SourceQuote: "红色纸鹤", Temporal: "stable", OnlyHer: &no}); err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	seen := func(text string) bool {
		for _, h := range b.Recall(ctx, Audience{Channel: "console", Conversation: tlMain, Scene: "private", PrivateOK: true}, Query{Text: text, Mode: ModeExplicit, TopK: 12}).Hits {
			if h.Text == text {
				return true
			}
		}
		return false
	}
	if !seen("主人喜欢红色纸鹤") {
		t.Fatal("对照：普通私聊里记的非专属事实应共享给另一位角色")
	}
	if seen("主人喜欢蓝色纸鹤") {
		t.Fatal("悄悄话处理里记的事实不该进共用池")
	}
}
