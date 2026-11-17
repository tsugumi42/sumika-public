package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 在正文第一块后卡住 HTTP 流，Tester 必须能在模型结束前看见草稿。
type localGateWriter struct {
	http.ResponseWriter
	ctx  context.Context
	gate <-chan struct{}
	held bool
}

func (w *localGateWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if !w.held && strings.Contains(string(b), `"content":"前半句"`) {
		w.held = true
		w.ResponseWriter.(http.Flusher).Flush()
		select {
		case <-w.gate:
		case <-w.ctx.Done():
		}
	}
	return n, err
}
func (w *localGateWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

type localUIHarness struct {
	a        *app
	q        *uiQueue
	tt       *ui.Tester
	workdir  string
	maxFrame time.Duration
	requests func() []string
}

func newLocalUI(t *testing.T, script llmtest.Script, gate <-chan struct{}, history int) *localUIHarness {
	t.Helper()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	// freeApi 探测关掉：它用自己的 HTTP 客户端，不走下面的 DialContext，开着会真连外网（2026-11-12 代理反复跑时撞上）
	cfg := "embed:\n  enabled: false\nfreeApi:\n  probe:\n    enabled: false\ntools:\n  workdir: '" + filepath.ToSlash(workdir) + "'\nqq:\n  accounts:\n    - id: '100'\n      character: char00\n      token: invented-token\n      url: ws://127.0.0.1:1\ncharacters:\n"
	for i := range 10 {
		id := fmt.Sprintf("char%02d", i)
		dir := filepath.Join(data, "characters", id, "card")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, text := range map[string]string{"character.yml": "name: 编造角色" + id + "\n", "character.md": "## 身份\n测试用角色。\n## 行为\n回答主人的话。\n## 表达\n简短。\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg += "  " + id + ":\n    model: test/local\n"
	}
	if err := os.MkdirAll(filepath.Join(data, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "sumika.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := set.For("console")
	tx, err := st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// 一条语句插一批：逐行 Exec 时每行都要开语句子事务，一万行花 8 秒，是这个夹具的大头
	const batch = 500
	for from := 0; from < history; from += batch {
		n := min(batch, history-from)
		rows := strings.TrimSuffix(strings.Repeat("(?,'console','console:char00','owner','owner','message',?,'owner',?),", n), ",")
		args := make([]any, 0, 3*n)
		for i := from; i < from+n; i++ {
			args = append(args, fmt.Sprintf("history-%d", i), fmt.Sprintf("编的历史 %d", i), int64(1000+i))
		}
		if _, err := tx.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,origin,created_at) VALUES `+rows, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	set.Close()
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"test/local","context_length":100000}`)}
	// 后台自己的协程（例如免费接口探测）可能在测试结束后才吐最后一行日志，直接传 t.Logf 会 panic 整个进程；
	// 清理开始时先关掉出口，晚到的日志丢弃（后台没等干净的问题记在交回里，不在测试里掩盖行为，只防日志把进程带崩）
	var logMu sync.Mutex
	logClosed := false
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		if !logClosed {
			t.Logf(format, args...)
		}
	}
	var bodies []string
	var mu sync.Mutex
	handler := llmtest.Handler(script, &bodies, &mu)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate != nil && r.Method == http.MethodPost {
			handler(&localGateWriter{ResponseWriter: w, ctx: r.Context(), gate: gate}, r)
		} else {
			handler(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	b, err := backend.Open(context.Background(), backend.Options{Root: root, DataDir: data, Mode: backend.Receive, Account: "100", SharedAnalyzer: testAnalyzer(), LLM: llm.New(llm.Magpie(srv.URL)), Logf: logf, DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := b.Desktop()
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{ctx: ctx, started: time.Now(), host: hostState{Mode: backend.Receive}}
	q := &uiQueue{}
	a.postFn = q.post
	a.attach(&d.Reader)
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h := &localUIHarness{a: a, q: q, tt: tt, workdir: workdir, requests: func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}}
	t.Cleanup(func() {
		logMu.Lock()
		logClosed = true
		logMu.Unlock()
		cancel()
		if a.convCancel != nil {
			a.convCancel()
		}
		d.Close()
		b.Close()
	})
	h.until(t, "打开本地会话", func() bool { return a.firstDone && a.sel == "console:char00" })
	return h
}

func (h *localUIHarness) until(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute) // 只防卡死，见规范-测试耗时第二节第 8 条
	var framed time.Time
	for time.Now().Before(deadline) {
		posted := h.q.drain()
		// 没有新活、刚画过的就不再画：一帧要光栅化整个窗口，原来每毫秒画一帧，等后台时全花在重画不变的画面上
		if posted > 0 || time.Since(framed) > 50*time.Millisecond {
			frameStarted := time.Now()
			h.tt.Frame()
			framed = time.Now()
			h.maxFrame = max(h.maxFrame, time.Since(frameStarted))
		}
		if ready() {
			return
		}
		h.q.waitPost(10 * time.Millisecond)
	}
	t.Fatalf("没等到 %s；界面文字 %v", what, h.tt.Texts())
}
func (h *localUIHarness) click(t *testing.T, label string) {
	t.Helper()
	if err := h.tt.Click(label); err != nil {
		t.Fatalf("点 %s：%v；%v", label, err, h.tt.Texts())
	}
	h.tt.Frame()
}
func (h *localUIHarness) send(t *testing.T, text string) {
	t.Helper()
	h.click(t, "发一条本地消息")
	h.tt.Type(text)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
}

func TestLocalChatUIStreamsStopsAndSwitches(t *testing.T) {
	release := make(chan struct{})
	// 10000 条历史超过硬门槛：回复前先用活实例压缩（P2-1 起本地私聊从起点读），摘要合格再照常流式回复
	const summary = "## 聊了什么\n主人翻了一遍编的历史 0 到 9999，最后说了一句编的输入。\n## 答应过的事\n没有答应过什么。\n## 主人的状态和心情\n像在整理旧记录，语气平常。\n## 还没聊完的事\n等主人接着往下说。"
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("前半句", "，完整回复"), Replies: []llmtest.Reply{llmtest.Text(summary)}}, release, 10000)
	if len(h.a.chars) != 10 || len(h.a.msgs) != 200 {
		t.Fatalf("十倍数据没走正常分页：角色 %d，消息 %d", len(h.a.chars), len(h.a.msgs))
	}
	started := time.Now()
	var compactAt, replyAt time.Time
	seen := 0
	h.send(t, "编的输入")
	h.until(t, "流式前半句", func() bool {
		reqs := h.requests()
		for _, body := range reqs[seen:] {
			switch {
			case strings.Contains(body, "整理上下文的请求"):
				if compactAt.IsZero() {
					compactAt = time.Now()
				}
			case !compactAt.IsZero() && replyAt.IsZero() && strings.Contains(body, "编的输入"):
				replyAt = time.Now()
			}
		}
		seen = len(reqs)
		return h.tt.HasText("前半句") && h.a.localRun("console:char00").busy
	})
	reqs := h.requests()
	compact := -1
	for i, body := range reqs {
		if strings.Contains(body, "整理上下文的请求") {
			compact = i
			break
		}
	}
	if compact < 0 {
		t.Fatalf("10000 条历史这一轮没有先压缩：%d 次模型请求", len(reqs))
	}
	if compact+1 != len(reqs)-1 {
		t.Fatalf("压缩请求后面没有紧接着回复请求：压缩是第 %d 次，共 %d 次", compact+1, len(reqs))
	}
	if reply := reqs[compact+1]; !strings.Contains(reply, "前面的聊天已经整理成下面的摘要") || !strings.Contains(reply, "编的输入") {
		t.Fatalf("压缩后的回复请求没换上新快照：%s", reply[:min(len(reply), 300)])
	}
	if compactAt.IsZero() || replyAt.IsZero() {
		t.Fatalf("没量到压缩到回复的先后：压缩 %v，回复 %v", compactAt, replyAt)
	}
	stream, shown := h.tt.Find("前半句")
	stop, clickable := h.tt.Find("停止生成")
	if !shown || stream.W <= 0 || stream.H <= 0 || stream.Y < 0 || stream.Y+stream.H > 1030 {
		t.Fatalf("草稿没有可见的布局方框：%+v", stream)
	}
	if !clickable || stop.W < 24 || stop.H < 24 || stop.X < 0 || stop.Y < 0 || stop.X+stop.W > 2048 || stop.Y+stop.H > 1030 {
		t.Fatalf("停止按钮不可达或目标小于 24：%+v", stop)
	}
	t.Logf("草稿方框=%+v；停止目标=%+v；可访问文本 %d 项；Tester 构建帧最大 %.2f ms", stream, stop, len(h.tt.Texts()), float64(h.maxFrame)/float64(time.Millisecond))
	t.Logf("10 角色、10000 历史，驻留消息 %d；读历史到压缩请求 %d ms，压缩到回复请求 %d ms（读历史加压缩共 %d ms），输入到首段显示 %d ms",
		len(h.a.msgs), compactAt.Sub(started).Milliseconds(), replyAt.Sub(compactAt).Milliseconds(), replyAt.Sub(started).Milliseconds(), time.Since(started).Milliseconds())
	h.a.open("console:char01")
	h.tt.Frame()
	if h.tt.HasText("前半句") {
		t.Fatal("草稿串到了另一个角色")
	}
	h.a.open("console:char00")
	h.until(t, "切回生成中的会话", func() bool { return h.tt.HasText("前半句") })
	close(release)
	h.until(t, "完整回复落入历史", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("前半句，完整回复") })
	h.send(t, "第二条")
	h.until(t, "第二轮开始", func() bool { return h.a.localRun("console:char00").busy })
	h.click(t, "停止生成")
	h.until(t, "停止后收尾", func() bool { return !h.a.localRun("console:char00").busy })
	// 切换到 QQ 后没有可发送输入框，也没有桌面执行权限。
	h.a.sel = "qq:group:300"
	h.a.keys[h.a.sel] = api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: h.a.sel}
	h.tt.Frame()
	if _, ok := h.tt.Find("发一条本地消息"); ok {
		t.Fatal("QQ 会话里出现了发送输入框")
	}
	if !h.tt.HasText("QQ 发送需走平台发送规则") {
		t.Fatal("QQ 只读原因没显示")
	}
}

func TestLocalChatUIApprovalExecutesOnlyAfterClick(t *testing.T) {
	for _, choice := range []string{"deny", "approve", "stop"} {
		t.Run(choice, func(t *testing.T) {
			allow := choice == "approve"
			name := "ui-approved.txt"
			args, _ := json.Marshal(map[string]string{"purpose": "写界面测试文件", "path": name, "content": "真实工具写的编造内容"})
			h := newLocalUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ui-write", "write", string(args)), llmtest.Text("审批处理完成")}}, nil, 0)
			target := filepath.Join(h.workdir, name)
			h.send(t, "写这个临时文件")
			h.until(t, "真实审批卡", func() bool { return h.tt.HasText("允许一次") })
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("点击前已执行：%v", err)
			}
			if choice == "stop" {
				h.click(t, "停止生成")
				h.until(t, "审批等待被停止", func() bool { return !h.a.localRun("console:char00").busy && !h.tt.HasText("允许一次") })
				if err := h.a.localRun("console:char00").err; err != "" {
					t.Fatalf("主动停止被显示成发送失败：%s", err)
				}
			} else {
				h.click(t, map[bool]string{true: "允许一次", false: "拒绝"}[allow])
				h.until(t, "审批后回复", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("审批处理完成") })
			}
			b, err := os.ReadFile(target)
			if allow {
				if err != nil || string(b) != "真实工具写的编造内容" {
					t.Fatalf("批准没执行：%q %v", b, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("拒绝仍执行：%q %v", b, err)
			}
			h.tt.Frame()
			h.tt.Frame()
			if h.tt.HasText("允许一次") {
				t.Fatal("已结束的审批卡还允许重复批准")
			}
		})
	}
}

// delayedLocalSend 只控制真实 API 的先后：停止已返回，发送 worker 才进入后台。
// 用它覆盖界面已经显示生成中、后台尚无活动回合的空档。
type delayedLocalSend struct {
	api.Reader
	api.LocalChat
	entered, release, stopped chan struct{}
}

func (d *delayedLocalSend) SendLocal(ctx context.Context, key api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	close(d.entered)
	<-d.release
	return d.LocalChat.SendLocal(ctx, key, text, draft)
}

func (d *delayedLocalSend) StopLocal(ctx context.Context, key api.ConversationKey) error {
	err := d.LocalChat.StopLocal(ctx, key)
	close(d.stopped)
	return err
}

func TestLocalChatUIStopBeforeSendWorker(t *testing.T) {
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("不该生成")}, nil, 0)
	original := h.a.reader
	delayed := &delayedLocalSend{Reader: original, LocalChat: original.(api.LocalChat), entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(delayed.release) }) }
	t.Cleanup(unblock)
	h.a.reader = delayed
	h.send(t, "停止发生在发送 worker 前")
	h.until(t, "发送 worker 进入受控空档", func() bool {
		select {
		case <-delayed.entered:
			return true
		default:
			return false
		}
	})
	h.click(t, "停止生成")
	h.until(t, "后台停止已返回", func() bool {
		select {
		case <-delayed.stopped:
			return true
		default:
			return false
		}
	})
	unblock()
	h.until(t, "尚未开始的发送被取消", func() bool { return !h.a.localRun("console:char00").busy })
	page, err := original.ReadMessages(context.Background(), api.ConversationRequest{Key: h.a.keyOf("console:char00"), Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value.Items) != 0 {
		t.Fatalf("点停止后尚未进入后台的输入仍被存下或生成了回复：%+v", page.Value.Items)
	}
}
