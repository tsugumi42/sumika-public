package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
)

// fakeGateway 是一个假的 OpenAI 兼容网关：/models 给两个模型；/chat/completions 记下请求体，按 SSE 吐几段思考、几段字、最后给用量
func fakeGateway(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m1","context_length":64000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"id":"m2"}]}`)
		case "/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, got); err != nil {
				t.Errorf("请求体不是 JSON：%s", b)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("x-magpie-provider", "假渠道")
			w.Header().Set("x-magpie-model", "m1-真身")
			for _, c := range []string{
				`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"想一"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"想。"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"好～"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":96},"completion_tokens_details":{"reasoning_tokens":3}}}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestModels(t *testing.T) {
	var got map[string]any
	srv := fakeGateway(t, &got)
	defer srv.Close()
	ms, err := New(Magpie(srv.URL + "/v1")).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Window() != 64000 || ms[1].Window() != 128000 || len(ms[0].SupportedReasoningLevels) != 2 {
		t.Fatalf("模型列表读得不对：%+v", ms)
	}
}

func TestStream(t *testing.T) {
	var got map[string]any
	srv := fakeGateway(t, &got)
	defer srv.Close()
	var events []Event
	res, err := New(Magpie(srv.URL+"/v1")).StreamChat(context.Background(), chat.Request{
		Model:    "m1",
		Style:    chat.StyleBackground,
		Messages: []chat.Message{msg(chat.RoleSystem, "你是她"), msg(chat.RoleUser, "在吗")},
		CacheKey: "chat:a:console",
		Thinking: "low",
	}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	// 发出去的请求：模型、流式、要用量、缓存键、思考档位
	if got["model"] != "m1" || got["stream"] != true || got["prompt_cache_key"] != "chat:a:console" || got["reasoning_effort"] != "low" {
		t.Fatalf("请求体不对：%v", got)
	}
	if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatalf("没要用量：%v", got["stream_options"])
	}
	if msgs, _ := got["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("消息条数不对：%v", got["messages"])
	}
	// 收到的：字、思考、用量（含缓存命中）、实际渠道
	if res.Text != "你好～" || res.Reasoning != "想一想。" {
		t.Fatalf("字或思考拼得不对：%q %q", res.Text, res.Reasoning)
	}
	// prompt_tokens 120 里 96 命中缓存：没命中的输入是 24（照 pi，总输入 = Input + CacheRead）
	if res.Usage != (Usage{Input: 24, Output: 9, CacheRead: 96, Reasoning: 3}) || res.Stop != "stop" {
		t.Fatalf("用量不对：%+v", res.Usage)
	}
	if res.Route != (Route{Provider: "假渠道", Model: "m1-真身"}) {
		t.Fatalf("响应头没读到：%+v", res.Route)
	}
	if len(events) != 5 || events[0].Reasoning != "想一" || events[2].Text != "你" || events[4].Usage == nil {
		t.Fatalf("事件顺序不对：%+v", events)
	}
	if res.FirstToken <= 0 || res.Total < res.FirstToken {
		t.Fatalf("计时不对：%v %v", res.FirstToken, res.Total)
	}
}

// 别家网关的思考字段：只给 reasoning（Magpie 的 opendesign 渠道）、两个都给且内容一样（不能出现两遍）
func TestReasoningFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"","reasoning_details":[{"type":"reasoning.text","text":""}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning":"先算","reasoning_details":[{"type":"reasoning.text","text":"先算"}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"一下","reasoning":"一下"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"2"},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := New(Magpie(srv.URL+"/v1")).StreamChat(context.Background(), chatReq("m", msg(chat.RoleUser, "1+1")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reasoning != "先算一下" || res.Text != "2" {
		t.Fatalf("思考应该是「先算一下」、字是「2」：%q %q", res.Reasoning, res.Text)
	}
}

// OpenRouter 转 Claude 的真实流（OpenCode 录像 openrouter-reasoning.json，表一 A12）：结构化思考分几块到，最后一块只带签名、
// 没有 text。签名要并进前面那条，下一次请求原样带回（pi 在这一块要求 text 是字符串，整块丢掉、签名跟着丢，7.1 A49）
func TestReasoningDetailSignatureOnly(t *testing.T) {
	res, err := streamOnce(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"想","reasoning_details":[{"type":"reasoning.text","text":"想","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning":"好了","reasoning_details":[{"type":"reasoning.text","text":"好了","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","signature":"签名","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"2"},"finish_reason":"stop"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.reasoningDetails) != 1 {
		t.Fatalf("连着的三块并成一条：%d 条", len(res.reasoningDetails))
	}
	if got := jsjson.Stringify(res.reasoningDetails[0]); got != `{"type":"reasoning.text","text":"想好了","format":"anthropic-claude-v1","index":0,"signature":"签名"}` {
		t.Fatalf("签名并进前面那条：%s", got)
	}
	// 只带签名、前面没有可并的：自己成一条，原样留着
	res, err = streamOnce(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.text","signature":"签名"}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"2"},"finish_reason":"stop"}]}`)
	if err != nil || len(res.reasoningDetails) != 1 || jsjson.Stringify(res.reasoningDetails[0]) != `{"type":"reasoning.text","signature":"签名"}` {
		t.Fatalf("单独的签名：%v %v", res.reasoningDetails, err)
	}
	// 既没有 text 也没有签名的，照旧不认
	res, _ = streamOnce(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.text"}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"2"},"finish_reason":"stop"}]}`)
	if len(res.reasoningDetails) != 0 {
		t.Fatalf("什么都没有的不认：%v", res.reasoningDetails)
	}
}

func TestThinkingOffSendsNone(t *testing.T) {
	var got map[string]any
	srv := fakeGateway(t, &got)
	defer srv.Close()
	if _, err := New(Magpie(srv.URL+"/v1")).StreamChat(context.Background(), chat.Request{Model: "m1", Style: chat.StyleBackground,
		Messages: []chat.Message{msg(chat.RoleUser, "x")}, Thinking: "off"}, nil); err != nil {
		t.Fatal(err)
	}
	if got["reasoning_effort"] != "none" {
		t.Fatalf("思考关着要发 reasoning_effort=none：%v", got)
	}
	if _, ok := got["prompt_cache_key"]; ok {
		t.Fatalf("没给缓存键不该发：%v", got)
	}
}

func TestCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"一"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-block // 不再吐字，等调用方取消
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	res, err := New(Magpie(srv.URL+"/v1")).StreamChat(ctx, chatReq("m1", msg(chat.RoleUser, "x")), func(e Event) {
		if e.Text == "一" {
			cancel()
		}
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("取消以后应该返回 context canceled，得到 %v", err)
	}
	if res.Text != "一" {
		t.Fatalf("取消前收到的字应该留着：%q", res.Text)
	}
}

// sse 起一个只吐给定几段的假网关
func sse(chunks ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func streamOnce(t *testing.T, chunks ...string) (Result, error) {
	t.Helper()
	srv := sse(chunks...)
	defer srv.Close()
	return New(Magpie(srv.URL+"/v1")).StreamChat(context.Background(), chatReq("m", msg(chat.RoleUser, "x")), nil)
}

// 各家放缓存命中的位置不一样（照 pi parseChunkUsage）
func TestUsageShapes(t *testing.T) {
	end := `{"choices":[{"index":0,"delta":{"content":"嗯"},"finish_reason":"stop"}]}`
	for name, c := range map[string]struct {
		chunk string
		want  Usage
	}{
		"DeepSeek 的 prompt_cache_hit_tokens": {`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":64}}`, Usage{Input: 36, Output: 5, CacheRead: 64}},
		"Kimi 的顶层 cached_tokens":             {`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"cached_tokens":40}}`, Usage{Input: 60, Output: 5, CacheRead: 40}},
		"details 里的 0 优先于别处":                 {`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":64}}`, Usage{Input: 100, Output: 5}},
		"缓存写另算":                              {`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":50}}}`, Usage{Input: 20, Output: 5, CacheRead: 30, CacheWrite: 50}},
		"Moonshot 放在 choice.usage":           {`{"choices":[{"index":0,"delta":{},"usage":{"prompt_tokens":10,"completion_tokens":2}}]}`, Usage{Input: 10, Output: 2}},
	} {
		res, err := streamOnce(t, end, c.chunk)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		if res.Usage != c.want {
			t.Fatalf("%s：用量 %+v，应该是 %+v", name, res.Usage, c.want)
		}
	}
}

// 结束原因照 pi mapStopReason：length 是截断不算错；没给、内容过滤、不认识的都算错，收到的字留着
func TestFinishReason(t *testing.T) {
	text := `{"choices":[{"index":0,"delta":{"content":"半句"}}]}`
	res, err := streamOnce(t, text, `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`)
	if err != nil || res.Stop != "length" {
		t.Fatalf("length 应该是截断、不报错：%v %q", err, res.Stop)
	}
	for name, chunks := range map[string][]string{
		"没给 finish_reason": {text},
		"内容过滤":             {text, `{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`},
		"不认识的":             {text, `{"choices":[{"index":0,"delta":{},"finish_reason":"weird"}]}`},
	} {
		res, err := streamOnce(t, chunks...)
		if err == nil {
			t.Fatalf("%s 应该报错", name)
		}
		if res.Text != "半句" {
			t.Fatalf("%s：出错前收到的字应该留着：%q", name, res.Text)
		}
	}
}

// Magpie 按 key 认客户端：magpie-local 会让用量页把 Sumika 记成 local
func TestMagpieKeyNamesSumika(t *testing.T) {
	if k := Magpie("http://127.0.0.1:3425/v1").APIKey; k != "magpie-sumika" {
		t.Fatalf("key = %q，Magpie 认不出是 sumika", k)
	}
}
