package turn

// 规格-模型配置 M2：一轮对话的请求按模型引用里的服务商发给各家；同名模型在两家各走各的，
// trace 里的模型带服务商 ID，冷却也带服务商。服务商 a 是 harness 的假网关，b 是这里另起的假服务商。

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/llm"
	"sumika-native/internal/modelref"
)

// sameModel 是 harness 的角色模型（testdata/cases/live-second.json 里的 test/think，ID 里自带斜杠），b 的目录里也有同名的一个
const sameModel = "test/think"

// fakeB 是服务商 b：记下每个 POST 的请求体里的 model，回一句固定的话
type fakeB struct {
	mu     sync.Mutex
	models []string
}

func (f *fakeB) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

func (f *fakeB) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.models = append(f.models, requestModel(string(body)))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"乙家的回复\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],"+usage+"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}

// routedHarness 搭两家服务商：a 是假网关（ID 为 primary 的模型），b 是 fakeB（同名模型）。
// character 的模型写成 characterModel；chain 是备选链。aFails 为真时 a 一律回 503
func routedHarness(t *testing.T, characterModel string, chain []string, aFails bool) (*harness, *fakeB, *[]string) {
	t.Helper()
	b := &fakeB{}
	srvB := memServer(b.handler())
	t.Cleanup(srvB.Close)
	var aModels []string
	var aMu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `]`,
		intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
			aMu.Lock()
			aModels = append(aModels, requestModel(body))
			aMu.Unlock()
			if aFails {
				http.Error(w, "a 挂了", http.StatusServiceUnavailable)
				return true
			}
			return false
		},
		routed: func(a *llm.Client) (*llm.Router, []llm.Model) {
			bClient := llm.New(llm.Source{Name: "乙家", BaseURL: srvB.URL + "/v1", APIKey: "key-b"})
			router := llm.NewRouter(modelref.New("a", "a", "b"), map[string]*llm.Client{"a": a, "b": bClient})
			// b 的目录里有一个和 a 的角色模型同名的模型（同名模型在两家各走各的）
			return router, []llm.Model{{Provider: "b", ID: sameModel, ContextLength: 100000, MaxOutputTokens: 4000}}
		},
		mutate: func(c *Config) {
			c.Retry.Max = 1
			if characterModel != "" {
				for id := range c.Models {
					c.Models[id] = characterModel
				}
			}
			c.DefaultFallbackModels = chain
		},
	})
	return h, b, &aModels
}

func traceModels(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	for _, row := range runContexts(t, h) {
		out = append(out, strings.SplitN(row, "|", 2)[0])
	}
	return out
}

// 角色模型写成 b/xxx：请求只到 b，发出的 model 不带 b/；a 一个请求都没收到；trace 记 b/xxx
func TestTurnRoutesByProviderInRef(t *testing.T) {
	t.Parallel()
	h, b, aSeen := routedHarness(t, "b/"+sameModel, nil, false)
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "乙家的回复" {
		t.Fatalf("该由 b 回：%v %+v", o.Err, o.Result)
	}
	if got := b.seen(); len(got) != 1 || got[0] != sameModel {
		t.Fatalf("b 收到的 model 该是不带前缀的 %q：%v", sameModel, got)
	}
	if len(*aSeen) != 0 {
		t.Fatalf("a 不该收到请求：%v", *aSeen)
	}
	if got := traceModels(t, h); len(got) != 1 || got[0] != "b/"+sameModel {
		t.Fatalf("trace 里的模型该带服务商 ID：%v", got)
	}
}

// 裸 ID（不带服务商）归 defaultProvider a；trace 记 a/xxx；发给 a 的是裸 ID
func TestTurnBareRefGoesToDefaultProvider(t *testing.T) {
	t.Parallel()
	h, b, aSeen := routedHarness(t, "", nil, false)
	primary := h.c.Characters[h.c.CharacterID].Model
	if primary != sameModel {
		t.Fatalf("harness 的角色模型变了：%s", primary)
	}
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" {
		t.Fatalf("该由 a 回：%v %+v", o.Err, o.Result)
	}
	if len(*aSeen) != 1 || (*aSeen)[0] != primary || len(b.seen()) != 0 {
		t.Fatalf("a 收到 %v（想要 [%s]），b 收到 %v", *aSeen, primary, b.seen())
	}
	if got := traceModels(t, h); len(got) != 1 || got[0] != "a/"+primary {
		t.Fatalf("trace：%v", got)
	}
}

// 备选链跨服务商：a 的主模型 503 → 换到 b；a/主模型 进冷却，下一轮直接用 b，a 不再被请求；
// 冷却键带服务商：b 的同名模型不受影响（旧代码按裸 ID 记，同名模型会互相冷却）
func TestTurnFailoverAcrossProvidersCoolsByProvider(t *testing.T) {
	t.Parallel()
	h, b, aSeen := routedHarness(t, "", []string{"b/" + sameModel}, true)
	primary := h.c.Characters[h.c.CharacterID].Model
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "乙家的回复" {
		t.Fatalf("a 挂了该换到 b：%v %+v", o.Err, o.Result)
	}
	aFirst := len(*aSeen)
	if aFirst != 2 || (*aSeen)[0] != primary || len(b.seen()) != 1 || b.seen()[0] != sameModel {
		t.Fatalf("a 收到 %v，b 收到 %v", *aSeen, b.seen())
	}
	rows := strings.Join(runContexts(t, h), "\n")
	if !strings.Contains(rows, "a/"+primary+"|") || !strings.Contains(rows, "b/"+sameModel+"|") || !strings.Contains(rows, `"originalModel":"a/`+primary+`"`) {
		t.Fatalf("trace 的模型和切换记录都该带服务商：\n%s", rows)
	}
	// 第二轮：a/主模型 在冷却，直接用 b；a 不再收到请求
	o = takeOutcome(t, h.hub.Send(h.c.CharacterID, "再来一句"))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "乙家的回复" {
		t.Fatalf("第二轮：%v %+v", o.Err, o.Result)
	}
	if len(*aSeen) != aFirst || len(b.seen()) != 2 {
		t.Fatalf("冷却中的 a/%s 不该再被请求：a %v，b %v", primary, *aSeen, b.seen())
	}
}
