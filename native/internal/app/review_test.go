package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 模型连接已建立但回复挂住；取消后故意延后传输返回，关库必须等回合写完结局。
func TestShutdownWaitsForInflightTurn(t *testing.T) {
	e := newEnv(t)
	requested := make(chan struct{})
	// Hold 停在第 0 段正文：假网关吐完回复头就挂着，等客户端中止连接。回复不会先读完，
	// "取消时请求还在途"才是确定的（2026-10-28 负载下红的那一次，回复在取消被处理之前就读完了）
	base := llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: llmtest.Text("编的回复"), Hold: func(int) int { return 0 }}, &e.bodies, &e.mu)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			close(requested)
		}
		base(w, r)
	}))
	defer srv.Close()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	// stuck：取消以后 SDK 的那次读已经读到错、正卡在推迟返回上。有它才说明"挂住的收尾"真的发生了
	stuck := make(chan struct{})
	// hooked：回复头到了、body 已经换成挂住的那一层（SDK 开始读它）。取消要等它：抢在回复头之前取消，
	// 传输层直接报错，没有 body 给 SDK，也就没有"读到取消的错"可等
	hooked := make(chan struct{})
	var hookOnce sync.Once
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			return transport.RoundTrip(r)
		}
		// 真正把请求交给假网关，但把返回给 SDK 的 body 卡在取消后的收尾上。
		resp, err := transport.RoundTrip(r)
		if err != nil {
			return resp, err
		}
		resp.Body = &delayedBody{ReadCloser: resp.Body, release: release, stuck: stuck}
		hookOnce.Do(func() { close(hooked) })
		return resp, nil
	})
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	http.DefaultTransport = transport
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer cancel()
	// 提前红了也要收好这次请求和关机：请求没取消，假网关的处理函数一直挂着（srv.Close 跟着等），
	// 关机的那个协程也还在关库，临时目录都删不掉
	fail := func(msg string) {
		t.Helper()
		t.Logf("诊断：请求到假网关 %v，回复头到 SDK %v，读到取消的错 %v", closed(requested), closed(hooked), closed(stuck))
		unblock()
		a.hubCancel()
		cancel()
		select {
		case <-done:
		case <-time.After(shutdownLimit + 5*time.Second):
		}
		t.Fatal(msg)
	}
	c := e.peer.accept(t)
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "711", testOwner, "编的挂起请求"))
	select {
	case <-requested:
	case <-time.After(10 * time.Second):
		fail("模型请求未到假网关")
	}
	select {
	case <-hooked:
	case <-time.After(10 * time.Second):
		fail("模型请求的回复头没有到 SDK")
	}
	cancel()
	select {
	case <-stuck:
	case <-time.After(10 * time.Second):
		fail("取消后 SDK 的读没有卡在推迟返回上")
	}
	// 给关机协程机会暴露提前关库；模型收尾期间必须仍能写 trace。
	timer := time.AfterFunc(200*time.Millisecond, unblock)
	defer func() { unblock(); timer.Stop(); a.kernel.idle(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("关机超时")
	}
	db, err := sql.Open("sqlite", filepath.Join(e.data, "go", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ended int
	if err := db.QueryRow(`SELECT count(*) FROM turns WHERE ended_at IS NOT NULL`).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if ended != 1 {
		t.Fatalf("关库前在途回合未收尾：已结束 %d 个", ended)
	}
	b := e.open()
	b.cleanup()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// closed 说通道关没关（失败时定位卡在哪一步，不等）
func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// delayedBody 把取消后的收尾拖住：读到错（连接断了）以后先给 stuck 一个信号，再等 release 才把错交给 SDK
type delayedBody struct {
	io.ReadCloser
	release chan struct{}
	stuck   chan struct{}
	once    sync.Once
}

func (b *delayedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(func() { close(b.stuck) })
		<-b.release
	}
	return n, err
}

// 显式 DataDir 的配置不能被另一套数据目录的环境变量带走。
func TestDataDirSelectsOwnConfig(t *testing.T) {
	first := newEnv(t)
	second := newEnv(t)
	second.writeConfigFreq(testToken, "0.7")
	t.Setenv("SUMIKA_DATA_DIR", "")
	// 项目根也隔离，旧实现只能读到默认值，不会访问真实 data/。
	root := t.TempDir()
	for _, e := range []*testEnv{first, second} {
		opt := e.options()
		opt.Root = root
		a, err := Open(context.Background(), opt)
		if err != nil {
			t.Errorf("指定数据目录没有读到自己的配置：%v", err)
			continue
		}
		want := 0.3
		if e == second {
			want = 0.7
		}
		if a.cfg.Group.Frequency != want || a.accountURL() != e.peer.url() {
			t.Errorf("读错数据目录的配置：frequency=%v url=%s", a.cfg.Group.Frequency, a.accountURL())
		}
		a.cleanup()
	}
}

func TestNormalLogsMaskGroup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.start()
	c := e.peer.accept(t)
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "712", testOwner, "编的普通请求"))
	waitFor(t, "试运行计划已记日志", func() bool { return e.logContains("试运行：计划") })
	for _, line := range e.logSnapshot() {
		if strings.Contains(line, "qq:group:"+testGroup) {
			t.Errorf("试运行日志泄露完整群号：%s", line)
		}
	}
}

func TestGatewayErrorLogsOmitBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"id":"`+testModel+`","context_length":32768}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"编的聊天原文","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	e.run(a)
	c := e.peer.accept(t)
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "713", testOwner, "编的错误请求"))
	waitFor(t, "网关错误已记录", func() bool { return e.logContains("回合出错：") })
	for _, line := range e.logSnapshot() {
		if strings.Contains(line, "编的聊天原文") {
			t.Errorf("模型错误日志泄露正文：%s", line)
		}
	}
	if !e.logContains("状态码 400") {
		t.Errorf("网关错误日志没有保留状态码：%v", e.logSnapshot())
	}
}

func TestLogMasksNumbersAndToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	a.logf("群 qq:group:300 私聊 qq:private:100:200 编号 12345 令牌 %s", testToken)
	want := "群 qq:group:***00 私聊 qq:private:***00:***00 编号 ***45 令牌 ***"
	logs := e.logSnapshot()
	if got := logs[len(logs)-1]; got != want {
		t.Fatalf("统一出口打码不对：%q，要 %q", got, want)
	}
}

func TestFrequencyZeroThenNonzero(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeConfigFreq(testToken, "0")
	a := e.open()
	a.cleanup()
	e.writeConfigFreq(testToken, "0.3")
	b := e.open()
	defer b.cleanup()
	rows, err := b.set.TraceStore().GateRules("qq:group:" + testGroup)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("0 改回 0.3 后自动零频率规则还在：%+v", rows)
	}
	_, _, ok, err := b.set.TraceStore().LoadGateState("app:auto-star-zero")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("自动零频率标记没有删除")
	}
}

func TestManualStarRulePreserved(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	if err := a.set.TraceStore().SetGateRule(store.GateRule{Conversation: "*", Frequency: 0}, false); err != nil {
		t.Fatal(err)
	}
	a.cleanup()
	b := e.open()
	defer b.cleanup()
	rows, err := b.set.TraceStore().GateRules("qq:group:" + testGroup)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Frequency != 0 {
		t.Fatalf("手设的全局规则被动了：%+v", rows)
	}
}

func TestUnusedBadCardDoesNotBlockOpen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	dir := filepath.Join(e.data, "characters", "unused", "card")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "character.yml"), []byte("name: null\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("不挂记忆的无关坏卡挡住启动：%v", err)
	}
	a.cleanup()
}

func TestNamesAndQuotesReachModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// 名册读 Go 自己的 QQ 库（go/chat/qq.db），不再读 Bun 的 shared/events.db：先让迁移建好库再写名册
	pp, err := paths.Init(e.data)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(pp, "qq")
	if err != nil {
		t.Fatal(err)
	}
	set.Close()
	db, err := sql.Open("sqlite", pp.ChatDB("qq"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO qq_people (uid,nickname,cards_json,aliases_json) VALUES ('400','编的昵称','{"300":"编的群名片","301":"编的别群名片"}','[]')`,
		`INSERT INTO qq_people (uid,nickname,cards_json,aliases_json) VALUES ('500','编的乙昵称','{}','[]')`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	a := e.open()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(30 * time.Second):
				t.Error("关机超时")
			}
		})
	}
	t.Cleanup(stop)
	c := e.peer.accept(t)
	first := fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":100,"user_id":400,"group_id":300,"message_id":"701","sender":{"nickname":"编的甲"},"message":[{"type":"text","data":{"text":"编的被引用句"}}]}`, e.now.Unix())
	e.peer.send(t, c, first)
	waitFor(t, "被引用消息入库", func() bool {
		st, _ := a.set.For("qq")
		_, ok, err := st.EventByPlatformID("qq", "qq:group:300", "701")
		return err == nil && ok
	})
	// 引用必须跨进程重开仍能查到；新实例的历史和当轮重排都要使用同一份名字查询。
	// 这里关闭后再开，不靠当前回合或入站对象的缓存。
	stop()
	b := e.start()
	c = e.peer.accept(t)
	frame := fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":100,"user_id":200,"group_id":300,"message_id":"702","sender":{"nickname":"编的主人"},"message":[{"type":"reply","data":{"id":"701"}},{"type":"at","data":{"qq":"100"}},{"type":"at","data":{"qq":"400"}},{"type":"at","data":{"qq":"500"}},{"type":"at","data":{"qq":"600"}},{"type":"text","data":{"text":"这个可以"}}]}`, e.now.Add(10*time.Second).Unix())
	e.peer.send(t, c, frame)
	waitFor(t, "假网关收到带引用的当轮请求", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return strings.Contains(strings.Join(e.bodies, "\n"), "这个可以")
	})
	e.mu.Lock()
	body := strings.Join(e.bodies, "\n")
	e.mu.Unlock()
	// 网关请求是 JSON，先解转义成消息正文再看引用和 @。
	var request struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	lines := ""
	for _, raw := range strings.Split(body, "\n") {
		if json.Unmarshal([]byte(raw), &request) == nil {
			for _, m := range request.Messages {
				lines += fmt.Sprint(m.Content)
			}
		}
	}
	for _, want := range []string{"@编的群名片", "@编的乙昵称", "@600", "↪引用 编的群名片：编的被引用句"} {
		if !strings.Contains(lines, want) {
			t.Errorf("假网关请求缺少 %q", want)
		}
	}
	st, _ := b.set.For("qq")
	var line string
	if err := st.Events.QueryRow(`SELECT json_extract(e.segments_json,'$.line') FROM channel_msgs m JOIN events e ON e.seq=m.event_seq WHERE m.platform_id='702'`).Scan(&line); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@编的群名片", "@编的乙昵称", "@600", "↪引用 编的群名片：编的被引用句"} {
		if !strings.Contains(line, want) {
			t.Errorf("入库行缺少 %q：%s", want, line)
		}
	}
}

// 第二步（归属标记）写失败，自动规则不能只写一半；删除失败也要把规则留住。
func TestAutoZeroMarkerFailureIsAtomic(t *testing.T) {
	t.Parallel()
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint("remove=", remove), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			defer a.cleanup()
			st := a.set.TraceStore()
			op := "INSERT"
			if remove {
				if err := a.syncZeroFrequency(true); err != nil {
					t.Fatal(err)
				}
				op = "DELETE"
			}
			if _, err := st.Trace.Exec(`CREATE TRIGGER fail_marker BEFORE ` + op + ` ON gate_state BEGIN SELECT RAISE(ABORT, '编的标记失败'); END`); err != nil {
				t.Fatal(err)
			}
			if err := a.syncZeroFrequency(!remove); err == nil {
				t.Fatal("没有返回标记写入错误")
			}
			rules, err := st.GateRules("*")
			if err != nil {
				t.Fatal(err)
			}
			_, _, owned, err := st.LoadGateState(autoStarZero)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if remove {
				want = 1
			}
			if len(rules) != want || owned != remove {
				t.Fatalf("失败留下了半份自动规则：rules=%+v owned=%v，要规则数 %d owned=%v", rules, owned, want, remove)
			}
		})
	}
}
