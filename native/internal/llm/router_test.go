package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/llm/chat"
	"sumika-native/internal/modelref"
)

// fakeProvider 是一个假服务商：记下收到的每个请求体里的 model 和 Authorization，回一句固定的话
type fakeProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	models []string
	auths  []string
}

func newFakeProvider(t *testing.T, reply string) *fakeProvider {
	t.Helper()
	f := &fakeProvider{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.models = append(f.models, req.Model)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) client(name, key string) *Client {
	return New(Source{Name: name, BaseURL: f.srv.URL + "/v1", APIKey: key})
}

func (f *fakeProvider) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

func routeReq(model string) chat.Request {
	return chat.Request{Model: model, Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("你好")}}}, MaxOutputTokens: 100}
}

// 装配按服务商取 client：两个假服务商各收到自己的请求，model 字段不带服务商前缀；
// 同名模型在两家各走各的（规格 9.7、禁止的偷懒写法"引用解析用取名字相同的第一个"）
func TestRouterSendsToTheProviderInTheRef(t *testing.T) {
	a, b := newFakeProvider(t, "甲"), newFakeProvider(t, "乙")
	refs := modelref.New("a", "a", "b")
	r := NewRouter(refs, map[string]*Client{"a": a.client("甲家", "key-a"), "b": b.client("乙家", "key-b")})
	ctx := context.Background()

	for _, c := range []struct {
		model, want string
		to          *fakeProvider
		wantSent    string
	}{
		{"a/same", "甲", a, "same"},
		{"b/same", "乙", b, "same"},
		{"same", "甲", a, "same"},                   // 裸引用归默认服务商 a
		{"b/group/auto-x", "乙", b, "group/auto-x"}, // 只按第一个斜杠切
		{"group/auto-x", "甲", a, "group/auto-x"},   // 首段不是服务商：整串是模型 ID
	} {
		res, err := r.StreamChat(ctx, routeReq(c.model), nil)
		if err != nil || res.Text != c.want {
			t.Fatalf("%s：%q, %v，想要 %q", c.model, res.Text, err, c.want)
		}
		got := c.to.seen()
		if len(got) == 0 || got[len(got)-1] != c.wantSent {
			t.Errorf("%s：服务商收到的 model = %v，想要最后一个是 %q", c.model, got, c.wantSent)
		}
	}
	if n := len(a.seen()) + len(b.seen()); n != 5 {
		t.Errorf("一共收到 %d 个请求，想要 5 个（各走各的，没有多发）", n)
	}
	if got := a.auths; got[0] != "Bearer key-a" || b.auths[0] != "Bearer key-b" {
		t.Errorf("各家用自己的 key：a=%v b=%v", got, b.auths)
	}
}

func TestRouterRefusesWithoutSubstituting(t *testing.T) {
	a := newFakeProvider(t, "甲")
	refs := modelref.New("a", "a", "off", "nokey")
	r := NewRouter(refs, map[string]*Client{"a": a.client("甲家", "k")}, "off")
	for _, c := range []struct{ model, want string }{
		{"off/m", "已停用"},
		{"nokey/m", "还没有可用的连接"},
		{"", "没选模型"},
		{"a/", "后面没写模型"},
	} {
		_, err := r.StreamChat(context.Background(), routeReq(c.model), nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q 的错 = %v，想要含 %q", c.model, err, c.want)
			continue
		}
		var local *localError
		if !errors.As(err, &local) {
			t.Errorf("%q 的错应该是本地错误（不换模型重试）：%v", c.model, err)
		}
	}
	if n := len(a.seen()); n != 0 {
		t.Errorf("引用不通的请求不能落到别家：a 收到了 %v", a.seen())
	}
}

// 冷却键带服务商：a/m 冷却了，b/m 仍可用；裸 m 和 a/m 是同一个键（旧代码按裸 ID 记，同名会互相冷却）
func TestFailoverKeysIncludeProvider(t *testing.T) {
	now := time.Unix(100, 0)
	f := &Failover{Now: func() time.Time { return now }, Refs: modelref.New("a", "a", "b")}
	boom := io.ErrUnexpectedEOF

	p := f.Plan("a/m", []string{"b/m"})
	if p.Model != "a/m" || !p.Next(context.Background(), boom) || p.Model != "b/m" {
		t.Fatalf("第一次计划：%+v", p)
	}
	// a/m 在冷却，b/m 没有
	q := f.Plan("b/m", []string{"a/m"})
	if q.Model != "b/m" {
		t.Fatalf("b/m 不该被 a/m 的冷却连累：%+v", q)
	}
	if r := f.Plan("m", []string{"b/m"}); r.Model != "b/m" || len(r.skipped) != 1 || r.skipped[0] != "a/m" {
		t.Fatalf("裸 m 就是 a/m，冷却中该被跳过，计划 = %+v", r)
	}
	// 同一个模型写成裸的和写成规范形式的，去重成一个
	if s := f.Plan("a/x", []string{"x", "a/x"}); len(s.models) != 1 || s.Model != "a/x" {
		t.Fatalf("去重：%+v", s)
	}
	// 不开备选链时，计划里的模型也是规范形式（请求、trace 用它）
	if one := f.Plan("x", nil); one.Model != "a/x" {
		t.Fatalf("单模型计划：%q", one.Model)
	}
}

func TestModelRef(t *testing.T) {
	if got := (Model{Provider: "magpie", ID: "group/auto-x"}).Ref(); got != "magpie/group/auto-x" {
		t.Errorf("Ref = %q", got)
	}
	if got := (Model{ID: "x"}).Ref(); got != "x" {
		t.Errorf("没有服务商的模型引用就是 ID：%q", got)
	}
}
