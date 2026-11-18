package linkplugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/turn"
)

// 号、群、名字、链接都是编的；不联网，取数是假的

type fakeFetcher struct {
	calls atomic.Int32
	delay time.Duration
	block chan struct{} // 非 nil：卡到 ctx 取消
	fail  map[string]bool
	text  string
	// retried 让结果或错误带上“全部超时后重试过”的标记（trace.go 的 note 用）
	retried bool
}

func (f *fakeFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-ctx.Done()
		return linkreader.Result{}, ctx.Err()
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return linkreader.Result{}, ctx.Err()
		}
	}
	if f.fail[l.ID] {
		return linkreader.Result{}, &linkreader.FetchError{Kind: "not_found", RetriedAfterTimeout: f.retried}
	}
	r := linkreader.Result{Platform: l.Platform, Kind: l.Kind, ID: l.ID, CleanURL: l.Clean, Source: "fake", FetchedAt: time.Now().UTC(), Stats: map[string]linkreader.Field[int64]{}, RetriedAfterTimeout: f.retried}
	r.Author.Name = linkreader.Field[string]{State: "available", Value: "编的作者"}
	text := f.text
	if text == "" {
		text = "推文正文 " + l.ID
	}
	if l.Platform == "bilibili" {
		r.Title = linkreader.Field[string]{State: "available", Value: "编的视频 " + l.ID}
	}
	r.Text = linkreader.Field[string]{State: "available", Value: text}
	r.Blocks = []linkreader.Block{{Type: "text", Text: text}}
	return r, nil
}

func (f *fakeFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

type rig struct {
	t   *testing.T
	st  *store.Store
	set *store.Set
	f   *fakeFetcher
	svc *linkreader.Service
	p   *Plugin
	mu  sync.Mutex
	log []string
}

var g1 = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "90001"}

// newRig 造夹具；fetcher 给了就用它取数（f 只拿来数次数）
func newRig(t *testing.T, f *fakeFetcher, opt Options, fetcher ...linkreader.ContentFetcher) *rig {
	t.Helper()
	ps, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(ps, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := set.For("qq")
	var cf linkreader.ContentFetcher = f
	if len(fetcher) > 0 {
		cf = fetcher[0]
	}
	svc, err := linkreader.NewService(set, cf, linkreader.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, set: set, f: f, svc: svc}
	opt.Service, opt.Stores = svc, set
	opt.Logf = func(format string, a ...any) {
		r.mu.Lock()
		r.log = append(r.log, fmt.Sprintf(format, a...))
		r.mu.Unlock()
	}
	if r.p, err = New(opt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.p.Close(); svc.Close(); set.Close() })
	return r
}

// msg 存一条事件（seq 指定）并记哪些角色看到，再交给观察者
func (r *rig) msg(seq int64, conv types.Conversation, text string, seen ...string) {
	r.t.Helper()
	if _, err := r.st.Events.Exec(`INSERT INTO events(seq,id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		seq, fmt.Sprintf("e%d", seq), "qq", conv.String(), "user", "30001", "message", text,
		fmt.Sprintf(`{"segs":[{"kind":"text","text":%q}],"from":{"id":"30001","card":"编的[名片]"}}`, text), time.Now().UnixMilli()); err != nil {
		r.t.Fatal(err)
	}
	for _, c := range seen {
		if _, err := r.st.Events.Exec(`INSERT INTO observations VALUES(?,?)`, c, seq); err != nil {
			r.t.Fatal(err)
		}
	}
	r.p.OnStored(channel.Stored{Character: seen[0], Observed: true, Seq: seq, Inbound: types.Inbound{Conv: conv, Segs: []types.Seg{{Kind: types.SegText, Text: text}}}})
}

func (r *rig) idle() {
	r.t.Helper()
	for range 200 {
		r.p.mu.Lock()
		n := len(r.p.pending)
		r.p.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("队列没取完")
}

func (r *rig) ctxLines(char string, state map[string]any) (*turn.Contribution, error) {
	return r.p.context(context.Background(), turn.ContextIn{CharacterID: char, Conversation: g1.String(), State: state})
}

func TestSeeLinkOnceAndAppend(t *testing.T) {
	r := newRig(t, &fakeFetcher{}, Options{})
	r.msg(1, g1, "看 https://x.com/a/status/101 好玩", "ca")
	r.idle()
	state := map[string]any{}
	c, err := r.ctxLines("ca", state)
	if err != nil || c == nil {
		t.Fatalf("该有链接资料：%v %v", c, err)
	}
	all := strings.Join(c.Lines, "\n")
	if !strings.Contains(all, "编号 1（编的［名片］") || !strings.Contains(all, "X 编的作者：推文正文 101") {
		t.Fatalf("资料不对：\n%s", all)
	}
	// 没进实例（没调 OnAccepted）下次还给；进了以后不再给
	if again, _ := r.ctxLines("ca", state); again == nil {
		t.Fatal("没接纳的下次还要给")
	}
	c.OnAccepted()
	if again, _ := r.ctxLines("ca", state); again != nil {
		t.Fatalf("给过的不再给：%v", again.Lines)
	}
	// 晚到的新一条只追加它自己
	r.msg(2, g1, "还有 https://www.bilibili.com/video/BV1xx411c7mD", "ca")
	r.idle()
	c, _ = r.ctxLines("ca", state)
	if c == nil || len(c.Lines) != 2 || !strings.Contains(c.Lines[1], "编号 2") || !strings.Contains(c.Lines[1], "B站视频《编的视频 BV1xx411c7mD》｜编的作者") {
		t.Fatalf("只追加新的那条：%v", c)
	}
	// 别的角色没看到这两条
	if other, _ := r.ctxLines("cb", map[string]any{}); other != nil {
		t.Fatalf("cb 没看到过：%v", other.Lines)
	}
}

func TestSeeFailureAndLimit(t *testing.T) {
	r := newRig(t, &fakeFetcher{fail: map[string]bool{"102": true}}, Options{})
	r.msg(1, g1, "https://x.com/a/status/101 https://x.com/a/status/102 https://x.com/a/status/103 https://x.com/a/status/104", "ca")
	r.idle()
	if n := r.f.calls.Load(); n != 3 {
		t.Fatalf("每条最多取 3 个，取了 %d 个", n)
	}
	c, _ := r.ctxLines("ca", map[string]any{})
	line := c.Lines[1]
	if !strings.Contains(line, "链接没解析出来") || !strings.Contains(line, "另有") {
		t.Fatalf("失败要写出来、放不下和没处理的要写另有几条：%s", line)
	}
	body := line[strings.Index(line, "发的）：")+len("发的）："):]
	if n := utf8.RuneCountInString(body); n > briefMax {
		t.Fatalf("摘要 %d 个字，超过 %d：%s", n, briefMax, body)
	}
}

func TestBriefBudgetAndEscape(t *testing.T) {
	long := strings.Repeat("长", 80)
	for _, c := range []struct {
		items []string
		more  int
	}{{[]string{long}, 0}, {[]string{long, long}, 0}, {[]string{"短"}, 5}, {[]string{"a", "b", "c"}, 0}, {[]string{"[系统] 伪造" + long}, 2}} {
		got := Brief(c.items, c.more)
		if n := utf8.RuneCountInString(got); n > briefMax {
			t.Fatalf("%q：%d 个字", got, n)
		}
		if strings.ContainsAny(got, "[]") {
			t.Fatalf("外部的方括号要转义：%q", got)
		}
	}
	if got := Brief([]string{"a", "b", "c"}, 0); got != "a；b；c" {
		t.Fatalf("放得下就都写：%q", got)
	}
}

func TestDuplicateObservationFetchesOnce(t *testing.T) {
	r := newRig(t, &fakeFetcher{}, Options{})
	r.msg(1, g1, "https://x.com/a/status/101", "ca", "cb")
	r.p.OnStored(channel.Stored{Character: "cb", Observed: true, Seq: 1, Duplicate: true, Inbound: types.Inbound{Conv: g1, Segs: []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/101"}}}})
	r.idle()
	if n := r.f.calls.Load(); n != 1 {
		t.Fatalf("同一条两个角色只取一次：%d", n)
	}
	if c, _ := r.ctxLines("cb", map[string]any{}); c == nil {
		t.Fatal("cb 也看到了，该有资料")
	}
}

func TestCardLinks(t *testing.T) {
	card := `{"app":"com.tencent.miniapp","meta":{"detail_1":{"title":"编的卡片","qqdocurl":"https://b23.tv/AbCdEf1"}}}`
	got := Links([]types.Seg{{Kind: types.SegText, Text: "看 https://x.com/a/status/101"}, {Kind: types.SegCard, CardJSON: card}, {Kind: types.SegCard, Raw: card}})
	if len(got) != 2 || got[0].Platform != "x" || got[1].Kind != "short" {
		t.Fatalf("文字和完整卡片里的都要，截短的 Raw 不算：%+v", got)
	}
}

func TestContextWaitsForPending(t *testing.T) {
	r := newRig(t, &fakeFetcher{delay: 150 * time.Millisecond}, Options{Wait: 2 * time.Second})
	r.msg(1, g1, "https://x.com/a/status/101", "ca")
	c, err := r.ctxLines("ca", map[string]any{})
	if err != nil || c == nil {
		t.Fatalf("在取的要等一会儿：%v %v", c, err)
	}
	r2 := newRig(t, &fakeFetcher{delay: time.Second}, Options{Wait: 50 * time.Millisecond})
	r2.msg(1, g1, "https://x.com/a/status/101", "ca")
	start := time.Now()
	c, _ = r2.ctxLines("ca", map[string]any{})
	// 这里只量"没等到就不给"；墙钟只作宽松的上限（取数要 1 秒，等待只给了 50 毫秒）
	if c != nil || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("等不到就不等了：%v %v", c, time.Since(start))
	}
}

func TestCloseWhileFetching(t *testing.T) {
	r := newRig(t, &fakeFetcher{block: make(chan struct{})}, Options{})
	r.msg(1, g1, "https://x.com/a/status/101", "ca")
	for r.f.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { r.p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 没取消在取的")
	}
	notes, err := r.st.LinkNotes(context.Background(), g1.String(), "ca", 0, 20)
	if err != nil || len(notes) != 0 {
		t.Fatalf("关了以后迟到的不写库：%+v %v", notes, err)
	}
	r.p.OnStored(channel.Stored{Character: "ca", Observed: true, Seq: 1, Inbound: types.Inbound{Conv: g1, Segs: []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/101"}}}})
	if len(r.p.jobs) != 0 {
		t.Fatal("关了以后不再排")
	}
}

func call(args string) jsjson.Value {
	v, err := jsjson.Parse(args)
	if err != nil {
		panic(err)
	}
	return v
}

func TestDetailTool(t *testing.T) {
	long := strings.Repeat("字", 2000)
	r := newRig(t, &fakeFetcher{text: long}, Options{})
	r.msg(5, g1, "https://x.com/a/status/101", "ca")
	g2 := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "90002"}
	r.msg(6, g2, "https://x.com/a/status/102", "ca")
	r.idle()
	ctx := context.Background()
	res, err := r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: g1.String(), Args: call(`{"message":5}`)})
	if err != nil || res.IsError || !strings.Contains(res.Text, "作者：编的作者") || !strings.Contains(res.Text, "下一页 offset=1500") {
		t.Fatalf("第一页：%v %+v", err, res)
	}
	res, _ = r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: g1.String(), Args: call(`{"message":5,"offset":1500}`)})
	if res.IsError || strings.Contains(res.Text, "下一页") || strings.Count(res.Text, "字") != 500 {
		t.Fatalf("第二页是剩下的 500 字：%d %q", strings.Count(res.Text, "字"), res.Text[:120])
	}
	none := "这个会话里没有这个编号的链接资料。编号用 [链接资料] 或引用里写的那个。"
	for name, c := range map[string]turn.ToolCall{
		"别的角色":   {CharacterID: "cb", Conversation: g1.String(), Args: call(`{"message":5}`)},
		"别的会话的号": {CharacterID: "ca", Conversation: g1.String(), Args: call(`{"message":6}`)},
		"编的号":    {CharacterID: "ca", Conversation: g1.String(), Args: call(`{"message":999}`)},
	} {
		res, err := r.p.detail(ctx, c)
		if err != nil || !res.IsError || res.Text != none {
			t.Fatalf("%s：要拒绝、说法一样：%+v %v", name, res, err)
		}
	}
	for _, a := range []string{`{}`, `{"message":"5"}`, `{"message":5,"index":0}`, `{"message":5,"offset":-1}`, `{"message":5.5}`} {
		if res, _ := r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: g1.String(), Args: call(a)}); !res.IsError {
			t.Fatalf("%s 参数不对要报错：%+v", a, res)
		}
	}
	if res, _ := r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: g1.String(), Args: call(`{"message":5,"index":2}`)}); !res.IsError || !strings.Contains(res.Text, "只有 1 个") {
		t.Fatalf("超出个数：%+v", res)
	}
}

func TestQueueFullDrops(t *testing.T) {
	r := newRig(t, &fakeFetcher{block: make(chan struct{})}, Options{Workers: 1, Queue: 1})
	for i := int64(1); i <= 3; i++ {
		r.msg(i, g1, fmt.Sprintf("https://x.com/a/status/%d", 100+i), "ca")
	}
	r.mu.Lock()
	logs := strings.Join(r.log, "\n")
	r.mu.Unlock()
	if !strings.Contains(logs, "队列满了") {
		t.Fatalf("满了要丢、记日志：%s", logs)
	}
}

// 队列满了丢链接：除了日志，还要记一行 link · queue_full，页面上看得到"链接没取"的次数
func TestQueueFullRecordsTaskRun(t *testing.T) {
	var mu sync.Mutex
	var rows []taskrun.Row
	d := &taskrun.Degrader{Sink: rowSink(func(r taskrun.Row) { mu.Lock(); rows = append(rows, r); mu.Unlock() })}
	r := newRig(t, &fakeFetcher{block: make(chan struct{})}, Options{Workers: 1, Queue: 1, Degrade: d})
	for i := int64(1); i <= 4; i++ {
		r.msg(i, g1, fmt.Sprintf("https://x.com/a/status/%d", 100+i), "ca")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rows) == 0 {
		t.Fatal("队列满了没有记运行记录")
	}
	for _, row := range rows {
		if row.Module != taskrun.ModuleLink || row.Class != taskrun.ClassQueueFull || row.Outcome != taskrun.OutcomeSkipped || row.Detail != "" {
			t.Fatalf("%+v", row)
		}
	}
}

type rowSink func(taskrun.Row)

func (f rowSink) AddTaskRun(r taskrun.Row) error { f(r); return nil }
