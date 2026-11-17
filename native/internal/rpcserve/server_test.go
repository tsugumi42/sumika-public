package rpcserve_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/store"
)

const conv = "qq:group:90002"

func key() api.ConversationKey {
	return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: conv}
}

type fixture struct {
	reader api.Reader
	qq     *store.Store
	p      paths.Paths
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	svc := desktopread.New(set, []api.CharacterView{{ID: "a", Name: "编造甲"}}, nil)
	t.Cleanup(svc.Close)
	qq, _ := set.Platform("qq")
	f := fixture{reader: &desktopread.Reader{Service: svc}, qq: qq, p: p}
	for i := range 5 {
		f.add(t, int64(1000+i), "编造正文"+string(rune('甲'+i)))
	}
	return f
}

func (f fixture) add(t *testing.T, at int64, text string) {
	t.Helper()
	if _, err := f.qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, Kind: "message", ActorKind: "human", ActorID: "90001", Text: text, CreatedAt: at}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
}

// noVersion 清掉行版本：它是读服务每读一次就递增的计数，两次读本来就不同
func noVersion(r *api.Result[api.MessagePage]) {
	for i := range r.Value.Items {
		r.Value.Items[i].Version = 0
	}
}

func listen(t *testing.T, r api.Reader, runFile string) *rpcserve.Listener {
	t.Helper()
	l, err := rpcserve.Listen(context.Background(), rpcserve.New(r, "test"), runFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// 经 WebSocket 读到的和直接调 Reader 读到的一样（rpcconv 往返无损 + 分派没走样）
func TestOverWebSocketSameAsDirect(t *testing.T) {
	f := newFixture(t)
	l := listen(t, f.reader, "")
	c, err := rpcclient.Dial(ctx(t), l.URL, l.Token(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Server.Impl != "go" {
		t.Fatalf("server.impl = %q", c.Server.Impl)
	}
	remote := &rpcclient.Reader{C: c}
	req := api.ConversationRequest{Key: key(), Limit: 2}

	want, err := f.reader.ReadMessages(ctx(t), req, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := remote.ReadMessages(ctx(t), req, "")
	if err != nil {
		t.Fatal(err)
	}
	noVersion(&want)
	noVersion(&got)
	if !reflect.DeepEqual(want, got) || len(got.Value.Items) != 2 || !got.Value.HasMore {
		t.Fatalf("读消息不一样\n直接 %#v\n协议 %#v", want, got)
	}
	// 翻下一页：游标原样往返
	want2, _ := f.reader.ReadMessages(ctx(t), req, want.Value.NextCursor)
	got2, _ := remote.ReadMessages(ctx(t), req, got.Value.NextCursor)
	noVersion(&want2)
	noVersion(&got2)
	if !reflect.DeepEqual(want2, got2) {
		t.Fatalf("第二页不一样\n直接 %#v\n协议 %#v", want2, got2)
	}

	// 概览：取第一个快照比
	firstSnap := func(sub api.Subscription[api.Overview], err error) api.Overview {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		select {
		case s := <-sub.Updates():
			return s.Value
		case <-time.After(10 * time.Second):
			t.Fatal("等概览超时")
		}
		return api.Overview{}
	}
	wo := firstSnap(f.reader.WatchOverview(ctx(t), api.OverviewRequest{Limit: 50}))
	go_ := firstSnap(remote.WatchOverview(ctx(t), api.OverviewRequest{Limit: 50}))
	if !reflect.DeepEqual(wo, go_) {
		t.Fatalf("概览不一样\n直接 %#v\n协议 %#v", wo, go_)
	}
}

// 订阅会话后新来一条，客户端收到新快照；Close 以后通道关掉
func TestConversationSubscriptionPushesNewEvents(t *testing.T) {
	f := newFixture(t)
	l := listen(t, f.reader, "")
	c, err := rpcclient.Dial(ctx(t), l.URL, l.Token(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sub, err := (&rpcclient.Reader{C: c}).WatchConversation(ctx(t), api.ConversationRequest{Key: key(), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(n int) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case s, ok := <-sub.Updates():
				if !ok {
					t.Fatal("订阅通道提前关了")
				}
				if len(s.Value.Page.Items) == n {
					return
				}
			case <-deadline:
				t.Fatalf("等不到 %d 条的快照", n)
			}
		}
	}
	wait(5)
	f.add(t, 2000, "新来的一条")
	wait(6)
	sub.Close()
	if _, ok := <-sub.Updates(); ok {
		t.Fatal("Close 以后还能收到快照")
	}
}

func dialRaw(t *testing.T, l *rpcserve.Listener, h http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	return websocket.Dial(ctx(t), l.URL, &websocket.DialOptions{HTTPHeader: h})
}

func roundTrip(t *testing.T, c *websocket.Conn, req string) rpcwire.Message {
	t.Helper()
	if err := c.Write(ctx(t), websocket.MessageText, []byte(req)); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var m rpcwire.Message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// 规格第九节"形状 × 入口"表里 Go 那一列
func TestEntryRules(t *testing.T) {
	f := newFixture(t)
	l := listen(t, f.reader, "")
	auth := http.Header{"Authorization": {"Bearer " + l.Token()}}

	for name, h := range map[string]http.Header{
		"没带令牌":      {},
		"令牌错":       {"Authorization": {"Bearer " + strings.Repeat("0", 64)}},
		"令牌短一截":     {"Authorization": {"Bearer " + l.Token()[:10]}},
		"不是 Bearer": {"Authorization": {l.Token()}},
	} {
		if _, resp, err := dialRaw(t, l, h); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s：要 401，得 err=%v resp=%v", name, err, resp)
		}
	}
	h := auth.Clone()
	// 同源的 Origin：websocket 库自己会放行，只剩我们的"带 Origin 一律拒"在拦（跨源的库也会拒，测不出我们这道）
	h.Set("Origin", "http://"+strings.TrimSuffix(strings.TrimPrefix(l.URL, "ws://"), "/rpc"))
	if _, resp, err := dialRaw(t, l, h); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("带 Origin：要 403，得 err=%v resp=%v", err, resp)
	}
	if !strings.HasPrefix(l.URL, "ws://127.0.0.1:") {
		t.Errorf("监听地址不是回环：%s", l.URL)
	}

	c, _, err := dialRaw(t, l, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	cases := []struct {
		name, req string
		code      int
	}{
		{"没握手就读", `{"jsonrpc":"2.0","id":1,"method":"messages.read","params":{}}`, rpcwire.CodeNotInitialized},
		{"没握手调写方法名", `{"jsonrpc":"2.0","id":2,"method":"local.send","params":{}}`, rpcwire.CodeMethodNotFound},
		{"版本不对", `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocol_version":99}}`, rpcwire.CodeInvalidParams},
		{"批量", `[{"jsonrpc":"2.0","id":4,"method":"initialize"}]`, rpcwire.CodeInvalidRequest},
		{"不是 JSON", `{"jsonrpc":`, rpcwire.CodeParse},
		{"不是 2.0", `{"jsonrpc":"1.0","id":5,"method":"initialize"}`, rpcwire.CodeInvalidRequest},
		{"id 是对象", `{"jsonrpc":"2.0","id":{},"method":"initialize"}`, rpcwire.CodeInvalidRequest},
	}
	for _, tc := range cases {
		if m := roundTrip(t, c, tc.req); m.Error == nil || m.Error.Code != tc.code {
			t.Errorf("%s：要错误码 %d，得 %+v", tc.name, tc.code, m.Error)
		}
	}
	if m := roundTrip(t, c, `{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocol_version":1,"client":"t"}}`); m.Error != nil {
		t.Fatalf("握手失败：%v", m.Error)
	}
	after := []struct {
		name, req string
		code      int
	}{
		{"未知方法", `{"jsonrpc":"2.0","id":7,"method":"local.send","params":{}}`, rpcwire.CodeMethodNotFound},
		{"多一个字段", `{"jsonrpc":"2.0","id":8,"method":"runtime.read","params":{"x":1}}`, rpcwire.CodeInvalidParams},
		{"参数类型错", `{"jsonrpc":"2.0","id":9,"method":"messages.read","params":{"request":{"limit":"多"}}}`, rpcwire.CodeInvalidParams},
		{"params 是数组", `{"jsonrpc":"2.0","id":10,"method":"settings.read","params":[]}`, rpcwire.CodeInvalidParams},
		{"退订不存在的", `{"jsonrpc":"2.0","id":11,"method":"runtime.unsubscribe","params":{"subscription":"s99"}}`, rpcwire.CodeInvalidParams},
	}
	for _, tc := range after {
		if m := roundTrip(t, c, tc.req); m.Error == nil || m.Error.Code != tc.code {
			t.Errorf("%s：要错误码 %d，得 %+v", tc.name, tc.code, m.Error)
		}
	}
	// 别处的游标、不存在的会话：照 Result 报，不崩
	m := roundTrip(t, c, `{"jsonrpc":"2.0","id":12,"method":"messages.read","params":{"request":{"key":{"platform":"qq","channel":"qq","scene":"group","conversation":"qq:group:1"},"limit":10},"cursor":"不是游标"}}`)
	if m.Error == nil {
		var r rpcwire.Result[rpcwire.MessagePage]
		json.Unmarshal(m.Result, &r)
		if r.State == rpcwire.StateReady {
			t.Errorf("坏游标读成功了：%s", m.Result)
		}
	}
	// 超过 1 MiB 的请求：连接断开
	big := `{"jsonrpc":"2.0","id":13,"method":"settings.read","params":{"pad":"` + strings.Repeat("x", rpcwire.MaxRequestBytes) + `"}}`
	c.Write(ctx(t), websocket.MessageText, []byte(big))
	if _, _, err := c.Read(ctx(t)); err == nil {
		t.Error("超大请求以后连接还在")
	}
}

// run 文件：开着时有、内容对得上；关了删掉；别人改写过的不删
func TestRunFile(t *testing.T) {
	f := newFixture(t)
	path := f.p.RPCRunFile()
	l, err := rpcserve.Listen(context.Background(), rpcserve.New(f.reader, "test"), path)
	if err != nil {
		t.Fatal(err)
	}
	var rf rpcwire.RunFile
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &rf)
	if rf.URL != l.URL || rf.Token != l.Token() || rf.PID != os.Getpid() || rf.Impl != "go" {
		t.Fatalf("run 文件不对：%+v", rf)
	}
	c, err := rpcclient.DialRunFile(ctx(t), path, "test")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	l.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("关了以后 run 文件还在：%v", err)
	}

	l2, err := rpcserve.Listen(context.Background(), rpcserve.New(f.reader, "test"), path)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"token":"别人的"}`), 0o600)
	l2.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("别人的 run 文件被删了")
	}

	// 别的活着的后台占着（用父进程号，一定活着、又不是自己）：不开
	busy := filepath.Join(t.TempDir(), "rpc.json")
	os.WriteFile(busy, []byte(fmt.Sprintf(`{"url":"ws://127.0.0.1:1/rpc","token":"x","pid":%d,"impl":"bun"}`, os.Getppid())), 0o600)
	if _, err := rpcserve.Listen(context.Background(), rpcserve.New(f.reader, "test"), busy); err == nil || !strings.Contains(err.Error(), "已经有一个后台") {
		t.Fatalf("别的后台占着还开了：%v", err)
	}

	// 地址文件里的进程不在了：不连
	dead := filepath.Join(t.TempDir(), "rpc.json")
	os.WriteFile(dead, []byte(`{"url":"ws://127.0.0.1:1/rpc","token":"x","pid":999999999}`), 0o600)
	if _, err := rpcclient.DialRunFile(ctx(t), dead, "test"); err == nil || !strings.Contains(err.Error(), "不在了") {
		t.Fatalf("进程不在了还去连：%v", err)
	}
	// 客户端不把令牌发给非回环地址
	if _, err := rpcclient.Dial(ctx(t), "ws://192.168.1.2:1/rpc", "x", "test"); err == nil || !strings.Contains(err.Error(), "回环") {
		t.Fatalf("非回环地址没拦：%v", err)
	}
}

// 进程内管道：同一套编码和分派，不走网络
func TestPipeTransport(t *testing.T) {
	f := newFixture(t)
	st, ct := rpcserve.Pipe()
	go rpcserve.New(f.reader, "test").Serve(context.Background(), st)
	c, err := rpcclient.New(ctx(t), ct, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := (&rpcclient.Reader{C: c}).ReadMessages(ctx(t), api.ConversationRequest{Key: key(), Limit: 200}, "")
	if err != nil || len(got.Value.Items) != 5 {
		t.Fatalf("管道读消息：%v %d", err, len(got.Value.Items))
	}
}
