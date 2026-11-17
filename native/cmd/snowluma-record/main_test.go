package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel/snowluma"
)

// 测试里的令牌、号码、名字一律编的（AGENTS「验收」）。

const testToken = "编的令牌-不进日志"

// peer 是假协议端：OneBot 正向 WS 的服务端。只发帧；记下客户端发来的帧，用来断言录帧工具没有发 action。
type peer struct {
	srv      *httptest.Server
	accepted chan *websocket.Conn
	actions  chan string
}

func newPeer(t *testing.T) *peer {
	t.Helper()
	p := &peer{accepted: make(chan *websocket.Conn, 1), actions: make(chan string, 8)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		p.accepted <- c
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			p.actions <- string(data)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *peer) cfg() snowluma.Config {
	return snowluma.Config{URL: "ws" + strings.TrimPrefix(p.srv.URL, "http"), Token: testToken}
}

func (p *peer) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case c := <-p.accepted:
		return c
	case <-time.After(5 * time.Second):
		t.Fatalf("5 秒内没收到录帧工具的连接")
		return nil
	}
}

func (p *peer) send(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("假协议端发帧：%v", err)
	}
}

// assertNoActions 断言客户端一帧都没发（只收不发：除了 WebSocket 握手和协议层的 ping/pong）。
func (p *peer) assertNoActions(t *testing.T) {
	t.Helper()
	select {
	case f := <-p.actions:
		t.Fatalf("假协议端收到了录帧工具发来的帧（只收不发）：%s", f)
	default:
	}
}

// 录几帧：文件里逐帧原样、每行能解析；-for 到点自己停；假协议端没收到任何 action；输出里没有令牌。
func TestRecordWritesFramesVerbatim(t *testing.T) {
	p := newPeer(t)
	path := filepath.Join(t.TempDir(), "snowluma-frames", "frames.jsonl")
	// 不靠总时限：等三帧都落盘再停（原来 700 毫秒到点，机器忙时只录到 1 帧；到点停由 TestRecordStopsWhenTimeIsUp 管）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var progress bytes.Buffer
	type result struct {
		n   int
		err error
	}
	res := make(chan result, 1)
	go func() {
		n, err := recordFor(ctx, p.cfg(), path, &progress)
		res <- result{n, err}
	}()

	c := p.accept(t)
	frames := []string{
		`{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_id":"300","sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":"编的一句"}}]}`,
		`{"post_type":"meta_event","time":100,"self_id":100,"meta_event_type":"heartbeat","interval":30000}`,
		`{"post_type":"notice","time":100,"self_id":100,"notice_type":"notify","sub_type":"poke","user_id":200,"target_id":100}`,
	}
	for _, f := range frames {
		p.send(t, c, f)
	}

	waitLines(t, path, len(frames))
	cancel()
	var r result
	select {
	case r = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("取消以后 5 秒内没停")
	}
	if r.err != nil {
		t.Fatalf("recordFor 返回 %v，ctx 取消应当返回 nil", r.err)
	}
	if r.n != len(frames) {
		t.Fatalf("录了 %d 帧，要 %d 帧", r.n, len(frames))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读录下来的文件：%v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(frames) {
		t.Fatalf("文件里 %d 行，要 %d 行：%q", len(lines), len(frames), string(data))
	}
	for i, l := range lines {
		var rec struct {
			At    string          `json:"at"`
			Frame json.RawMessage `json:"frame"`
		}
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("第 %d 行不是 JSON：%v（%q）", i+1, err, l)
		}
		if _, err := time.Parse(time.RFC3339Nano, rec.At); err != nil {
			t.Fatalf("第 %d 行的收到时间不是时间：%v（%q）", i+1, err, rec.At)
		}
		if string(rec.Frame) != frames[i] {
			t.Fatalf("第 %d 帧没有原样存：\n要 %s\n存 %s", i+1, frames[i], rec.Frame)
		}
	}

	p.assertNoActions(t)
	for _, out := range []string{progress.String(), startLine(p.cfg(), path), endLine(r.n, path)} {
		if strings.Contains(out, testToken) {
			t.Fatalf("输出里出现了令牌：%q", out)
		}
	}
	if !strings.Contains(progress.String(), "在录：") || !strings.Contains(startLine(p.cfg(), path), "token ***") {
		t.Fatalf("启动那一行不对：%q", progress.String())
	}
}

// 一帧也不来（连接活着但没动静）：-for 到点自己停，文件建好、是空的。
func TestRecordStopsWhenTimeIsUp(t *testing.T) {
	p := newPeer(t)
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	// 到点就是这个测试要测的，只能靠总时限；留足 1 秒，机器忙时握手也来得及（原来 250 毫秒，第二轮复核）
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	res := make(chan int, 1)
	go func() {
		n, err := recordFor(ctx, p.cfg(), path, io.Discard)
		if err != nil {
			t.Errorf("recordFor 返回 %v，到点停应当返回 nil", err)
		}
		res <- n
	}()

	p.accept(t) // 连接建立，但协议端一帧都不发

	select {
	case n := <-res:
		if n != 0 {
			t.Fatalf("一帧没来却录了 %d 帧", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("5 秒内没停：-for 到点应当自己停")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("ctx 结束原因是 %v，要是到点", ctx.Err())
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("录帧的文件没建：%v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("一帧没来，文件却有 %d 字节", st.Size())
	}
	p.assertNoActions(t)
}

// 令牌不进任何输出：地址里带了令牌也打码。
// 同一个 -out 再录一次：接着往后写，上次录的不冲掉
func TestRecordAppendsToExistingFile(t *testing.T) {
	p := newPeer(t)
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	old := "{\"at\":\"编的\",\"frame\":{}}\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	// 不靠总时限：等这一帧落盘再停，机器忙也不会一帧没录就到点（第二轮复核）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() {
		_, err := recordFor(ctx, p.cfg(), path, io.Discard)
		res <- err
	}()
	c := p.accept(t)
	p.send(t, c, `{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":5000}`)
	waitLines(t, path, 2)
	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("recordFor 返回 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内没停")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), old) || strings.Count(string(b), "\n") != 2 {
		t.Fatalf("文件 = %q，要上次那一行后面接着这次录的一行", b)
	}
	p.assertNoActions(t)
}

// 上次录到一半被杀，最后一行没写完：这次录的第一帧另起一行，不和那半行粘在一起
func TestRecordStartsNewLineAfterHalfLine(t *testing.T) {
	p := newPeer(t)
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	half := `{"at":"编的","frame":{"post_ty`
	if err := os.WriteFile(path, []byte(half), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() {
		_, err := recordFor(ctx, p.cfg(), path, io.Discard)
		res <- err
	}()
	c := p.accept(t)
	p.send(t, c, `{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":5000}`)
	waitLines(t, path, 2)
	cancel()
	if err := <-res; err != nil {
		t.Fatalf("recordFor 返回 %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 || lines[0] != half {
		t.Fatalf("文件 = %q，要那半行单独一行、后面是这次录的", b)
	}
	var rec struct {
		Frame json.RawMessage `json:"frame"`
	}
	if json.Unmarshal([]byte(lines[1]), &rec) != nil || len(rec.Frame) == 0 {
		t.Fatalf("这次录的那一行不是完整的 JSON：%q", lines[1])
	}
	p.assertNoActions(t)
}

func TestStartLineMasksToken(t *testing.T) {
	cfg := snowluma.Config{URL: "ws://127.0.0.1:3001/?access_token=" + testToken, Token: testToken}
	got := startLine(cfg, "x.jsonl")
	if strings.Contains(got, testToken) {
		t.Fatalf("启动那一行里出现了令牌：%q", got)
	}
	if !strings.Contains(got, "token ***") {
		t.Fatalf("令牌没有打码：%q", got)
	}
}

// 断线以后按退避重连接着录：同一个文件里前后两段都在，不重置、不丢。
func TestRecordReconnectsAndKeepsRecording(t *testing.T) {
	p := newPeer(t)
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		n   int
		err error
	}
	res := make(chan result, 1)
	go func() {
		n, err := recordFor(ctx, p.cfg(), path, io.Discard)
		res <- result{n, err}
	}()

	c1 := p.accept(t)
	p.send(t, c1, `{"post_type":"message","time":100,"self_id":100,"message_id":"300"}`)
	p.send(t, c1, `{"post_type":"message","time":100,"self_id":100,"message_id":"301"}`)
	waitLines(t, path, 2)

	c1.CloseNow()
	c2 := p.accept(t) // 退避 0.5～1 秒以后重新拨号
	p.send(t, c2, `{"post_type":"message","time":100,"self_id":100,"message_id":"302"}`)
	waitLines(t, path, 3)

	cancel()
	r := <-res
	if r.err != nil {
		t.Fatalf("recordFor 返回 %v，ctx 取消应当返回 nil", r.err)
	}
	if r.n != 3 {
		t.Fatalf("录了 %d 帧，要 3 帧", r.n)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读录下来的文件：%v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 3 {
		t.Fatalf("文件里有 %d 行，要 3 行：%q", got, string(data))
	}
	p.assertNoActions(t)
}

// waitLines 等文件里攒够 n 行（每帧写完就落盘），最多真等 5 秒。
func waitLines(t *testing.T, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Count(string(data), "\n") >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 里 5 秒内没攒够 %d 行：%q", path, n, string(data))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
