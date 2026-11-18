package llm

// 表一 A12"模型接口兼容"：录下来的真实回复回放（llmtest.Replay，录像在 testdata/cassettes，来源和改过什么写在每份的 metadata 里）。
// 期望值照录像手写（另用一个不经过这里解析的小脚本把每块的字段摘出来核对过），不拿现在的输出当答案。
// 请求体是本仓库的客户端发的：改了请求的写法，本地 SUMIKA_RECORD_REQUESTS=1 重写一次录像（回复不动）

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/tools"
)

func replayClient(t *testing.T, name string) *Client {
	t.Helper()
	srv := httptest.NewServer(llmtest.Replay(t, filepath.Join("testdata", "cassettes", name)))
	t.Cleanup(srv.Close)
	return New(Magpie(srv.URL + "/v1"))
}

// Venice 那份的对话：查汇率的工具、乘 17 加 9、再加 7
const (
	veniceSystem = "Use the provided tool for private data. After receiving the result, give only the requested number; do not call the tool again."
	veniceAsk    = "Look up conversion rate for code ZEBRA. Then multiply that rate by 17 and add 9. You must use lookup_rate before answering."
	veniceTools  = `[{"type":"function","function":{"name":"lookup_rate","description":"Retrieve the private conversion rate for a code. Only this tool knows the rate.","parameters":{"type":"object","properties":{"code":{"type":"string","enum":["ZEBRA","MOOSE"]}},"required":["code"]}}}]`
	veniceCallID = "chatcmpl-tool-ab656a8b683341f2"
	veniceArgs   = `{"code": "ZEBRA"}`
	veniceThink1 = "The user wants me to look up the conversion rate for code ZEBRA, then multiply by 17 and add 9. Let me start by looking up the rate."
	veniceThink2 = "We have the rate: 23. Multiply by 17: 23 * 17 = 391. Then add 9: 391 + 9 = 400. So the answer is 400."
	veniceThink3 = "We need to add 7 to the previous answer, which was 400. So 400+7=407. Reply with just the number."
)

func veniceDecls() []tools.Declaration {
	return []tools.Declaration{{
		Name:        "lookup_rate",
		Description: "Retrieve the private conversion rate for a code. Only this tool knows the rate.",
		Parameters:  `{"type":"object","properties":{"code":{"type":"string","enum":["ZEBRA","MOOSE"]}},"required":["code"]}`,
	}}
}

func firstAsk() chat.Request {
	return chat.Request{Model: "deepseek-v4-flash", Style: chat.StyleForeground, Tools: veniceDecls(), Thinking: "high",
		Messages: []chat.Message{msg(chat.RoleSystem, veniceSystem), msg(chat.RoleUser, veniceAsk)}}
}

func veniceArgsValue(t *testing.T) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(veniceArgs)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// veniceCall 是第一次回复：只有工具调用和思考，正文是 null
func veniceCall(t *testing.T) chat.Message {
	return chat.Message{Role: chat.RoleAssistant, Blocks: []chat.Block{
		chat.Thinking{Text: veniceThink1, Replay: openaichat.ReplayFrom("reasoning_content", nil)},
		chat.ToolCall{ID: veniceCallID, Name: "lookup_rate", Arguments: veniceArgsValue(t)},
	}}
}

func veniceResult() chat.Message {
	return chat.Message{Role: chat.RoleTool, Blocks: []chat.Block{chat.ToolResult{CallID: veniceCallID, Text: "23"}}}
}

func veniceAnswer() chat.Message {
	return chat.Message{Role: chat.RoleAssistant, Blocks: []chat.Block{
		chat.Text("400"),
		chat.Thinking{Text: veniceThink2, Replay: openaichat.ReplayFrom("reasoning_content", nil)},
	}}
}

// 经 Venice 转发的 DeepSeek v4 flash（真实录像）：每块都带显式的 null，工具调用的后几块 id、name 是 null 只靠 index 认，
// 有一块正文和思考挤在同一个 delta 里，用量里多 cache_read_input_tokens（缓存命中照 prompt_tokens_details 取）；
// 三次请求：调工具、拿到结果回答、接着问
func TestReplayDeepSeekVenice(t *testing.T) {
	c := replayClient(t, "deepseek-venice-tool-loop.json")
	ctx := context.Background()
	r1, err := c.StreamChat(ctx, firstAsk(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text != "" || r1.Reasoning != veniceThink1 || r1.reasoningField != "reasoning_content" || r1.Stop != "toolUse" ||
		len(r1.ToolCalls) != 1 || r1.ToolCalls[0] != (ToolCall{ID: veniceCallID, Name: "lookup_rate", Args: veniceArgs}) ||
		!r1.UsageSeen || r1.Usage != (Usage{Input: 101, Output: 83, CacheRead: 256}) || r1.Route != (Route{}) {
		t.Fatalf("调工具那一次：%+v", r1)
	}
	r2, err := c.StreamChat(ctx, chat.Request{Model: "deepseek-v4-flash", Style: chat.StyleForeground, Tools: veniceDecls(), Thinking: "high",
		Messages: []chat.Message{msg(chat.RoleSystem, veniceSystem), msg(chat.RoleUser, veniceAsk), veniceCall(t), veniceResult()}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Text != "400" || r2.Reasoning != veniceThink2 || r2.Stop != "stop" || len(r2.ToolCalls) != 0 || r2.Usage != (Usage{Input: 128, Output: 46}) {
		t.Fatalf("回答那一次：%+v", r2)
	}
	r3, err := c.StreamChat(ctx, chat.Request{Model: "deepseek-v4-flash", Style: chat.StyleForeground, Tools: veniceDecls(), Thinking: "high",
		Messages: []chat.Message{msg(chat.RoleSystem, veniceSystem), msg(chat.RoleUser, veniceAsk), veniceCall(t), veniceResult(), veniceAnswer(),
			msg(chat.RoleUser, "Add 7 to that. Reply with just the number.")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r3.Text != "407" || r3.Reasoning != veniceThink3 || r3.Stop != "stop" || r3.Usage != (Usage{Input: 148, Output: 33}) {
		t.Fatalf("接着问那一次：%+v", r3)
	}
}

// OpenRouter 转 Claude（真实录像）：思考在 reasoning 和 reasoning_details 两处都有，结构化思考分三块到、最后一块只带签名
// （签名并进前面那条、原样带回，7.1 A49）；块之间夹注释行；finish_reason 出现两次；用量里多 cost 一类字段
func TestReplayOpenRouterReasoning(t *testing.T) {
	res, err := replayClient(t, "openrouter-claude-reasoning.json").StreamChat(context.Background(), chat.Request{Model: "anthropic/claude-sonnet-4.6", Style: chat.StyleForeground,
		Messages: []chat.Message{msg(chat.RoleSystem, "Think through the arithmetic, then reply with only the final integer."), msg(chat.RoleUser, "What is 173 multiplied by 219?")}, Thinking: "medium"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	think := "173 × 219\n\n173 × 200 = 34,600\n173 × 19 = 173 × 20 - 173 = 3,460 - 173 = 3,287\n\n34,600 + 3,287 = 37,887"
	if res.Text != "37887" || res.Reasoning != think || res.reasoningField != "reasoning" || res.Stop != "stop" || res.Usage != (Usage{Input: 61, Output: 80, Reasoning: 73}) {
		t.Fatalf("字、思考、用量：%+v", res)
	}
	want, _ := json.Marshal(map[string]any{"type": "reasoning.text", "text": think, "format": "anthropic-claude-v1", "index": 0, "signature": recordedSignature(t)})
	if len(res.reasoningDetails) != 1 || !sameJSON(jsjson.Stringify(res.reasoningDetails[0]), string(want)) {
		t.Fatalf("结构化思考并成一条、带着签名：%v", res.reasoningDetails)
	}
}

// recordedSignature 是录像里那块只带签名的 reasoning_details 的签名（直接从录像的原文里取，不经过这里的解析）
func recordedSignature(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cassettes", "openrouter-claude-reasoning.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c llmtest.Cassette
	json.Unmarshal(b, &c)
	for _, line := range strings.Split(c.Interactions[0].Response.Body, "\n") {
		if i := strings.Index(line, `"signature":"`); i >= 0 {
			rest := line[i+len(`"signature":"`):]
			return rest[:strings.Index(rest, `"`)]
		}
	}
	t.Fatal("录像里没有签名")
	return ""
}

func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && jsonEqual(x, y)
}

func jsonEqual(x, y any) bool {
	bx, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(bx) == string(by)
}

// 不给 index 的网关（由 Venice 那份改的）：工具调用每一块都只带 id，照 id 拼成一个调用
func TestReplayNoIndex(t *testing.T) {
	res, err := replayClient(t, "no-index.json").StreamChat(context.Background(), firstAsk(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0] != (ToolCall{ID: veniceCallID, Name: "lookup_rate", Args: veniceArgs}) || res.Stop != "toolUse" {
		t.Fatalf("照 id 拼成一个调用：%+v", res.ToolCalls)
	}
}

// 流说完了却没给结束原因、内容过滤（由 Venice 那份改的）：报错，已经收到的字留着；没给结束原因的重来，内容过滤的不重来
func TestReplayFinishProblems(t *testing.T) {
	for _, c := range []struct {
		file, reason string
		retry        bool
	}{{"no-finish-reason.json", "", true}, {"content-filter.json", "content_filter", false}} {
		res, err := replayClient(t, c.file).StreamChat(context.Background(), firstAsk(), nil)
		var se *StopError
		if !errors.As(err, &se) || se.Reason != c.reason {
			t.Fatalf("%s：要报没说完，原因 %q：%v", c.file, c.reason, err)
		}
		if res.Text != "400" || res.Reasoning != veniceThink2 {
			t.Fatalf("%s：收到的字、思考留着：%+v", c.file, res)
		}
		if retry, _ := Retryable(err); retry != c.retry {
			t.Fatalf("%s：重不重来应该是 %v", c.file, c.retry)
		}
	}
}

// 本机 Magpie 路由到 DeepSeek v4.1 flash（主人 2026-10-27："可以，用 Magpie 真调两次 DeepSeek v4.1 flash（一次带思考的纯文字、
// 一次调工具）录进 A12 的录像"）。录制和回放共用这两次请求的写法
const magpieModel = "auto/deepseek-v4.1-flash"

func magpieTextAsk() chat.Request {
	return chat.Request{Model: magpieModel, Style: chat.StyleForeground, Thinking: "high", CacheKey: "a12:magpie:text",
		Messages: []chat.Message{msg(chat.RoleSystem, "先想清楚再回答，最后只回一个整数。"), msg(chat.RoleUser, "173 乘以 219 等于多少？")}}
}

func magpieToolAsk() chat.Request {
	return chat.Request{Model: magpieModel, Style: chat.StyleForeground, Thinking: "high", CacheKey: "a12:magpie:tool", Tools: veniceDecls(),
		Messages: []chat.Message{msg(chat.RoleSystem, "私有的数据只能用提供的工具查，不要自己编。"), msg(chat.RoleUser, "查一下代号 ZEBRA 的汇率。")}}
}

// TestRecordMagpie 真调本机 Magpie 两次，把回复原样录进 testdata/cassettes/magpie-deepseek-*.json。平时跳过：要花两次调用，
// 本地设 SUMIKA_RECORD_LIVE=1 才录（CI 里 llmtest.Record 不录）；SUMIKA_MAGPIE 换网关地址
func TestRecordMagpie(t *testing.T) {
	if os.Getenv("SUMIKA_RECORD_LIVE") != "1" {
		t.Skip("要真调模型：本地设 SUMIKA_RECORD_LIVE=1 才录")
	}
	upstream := os.Getenv("SUMIKA_MAGPIE")
	if upstream == "" {
		upstream = "http://127.0.0.1:3425"
	}
	for _, c := range []struct {
		file, what string
		req        chat.Request
	}{{"magpie-deepseek-text.json", "带思考的纯文字", magpieTextAsk()}, {"magpie-deepseek-tool.json", "调工具", magpieToolAsk()}} {
		meta := map[string]any{
			"source": "本机 Magpie（" + upstream + "）路由的 " + magpieModel + "，" + time.Now().Format("2006-01-02 15:04") + " 真调录下（" + c.what + "）",
			"note":   "回复体和 content-type、x-magpie-* 头是录下来的原样；请求头没记",
		}
		srv := httptest.NewServer(llmtest.Record(t, filepath.Join("testdata", "cassettes", c.file), upstream, meta))
		res, err := New(Magpie(srv.URL+"/v1")).StreamChat(context.Background(), c.req, nil)
		srv.Close()
		t.Logf("%s：%v；字 %q；思考 %d 字（%s）；工具 %+v；结束 %s；用量 %+v；渠道 %+v", c.what, err, res.Text, len([]rune(res.Reasoning)), res.reasoningField, res.ToolCalls, res.Stop, res.Usage, res.Route)
	}
}

// 本机 Magpie 的真实流（2026-10-27 录，路由到 opendesign 渠道）：响应头带 x-magpie-provider / model；思考在 reasoning 字段，
// 同时每块都有一条只带 index 的 reasoning_details（并成一条，字和思考一样）；调工具那次只有一块空思考，结构化思考留一条空的；
// 用量照 prompt_tokens_details 取缓存（0）。这两次回复都短，没有夹到心跳注释行
func TestReplayMagpie(t *testing.T) {
	ctx := context.Background()
	route := Route{Provider: "opendesign", Model: "opendesign/deepseek-v4.1-flash"}
	think := "We need answer only integer. Need compute 173*219. 219*170=37230? 219*17=3723, times10=37230. plus 219*3=657. total 37887. Need final only integer."
	res, err := replayClient(t, "magpie-deepseek-text.json").StreamChat(ctx, magpieTextAsk(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]any{"index": 0, "type": "reasoning.text", "text": think})
	if res.Text != "37887" || res.Reasoning != think || res.reasoningField != "reasoning" || res.Stop != "stop" || res.Route != route ||
		res.Usage != (Usage{Input: 51, Output: 56, Reasoning: 53}) || len(res.reasoningDetails) != 1 || !sameJSON(jsjson.Stringify(res.reasoningDetails[0]), string(want)) {
		t.Fatalf("带思考的纯文字：%+v", res)
	}
	res, err = replayClient(t, "magpie-deepseek-tool.json").StreamChat(ctx, magpieToolAsk(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" || res.Reasoning != "" || res.Stop != "toolUse" || res.Route != route || res.Usage != (Usage{Input: 333, Output: 41}) ||
		len(res.ToolCalls) != 1 || res.ToolCalls[0] != (ToolCall{ID: "call_00_ET_H7XdcrfOzU1Gdz0tsyN22798", Name: "lookup_rate", Args: `{"code": "ZEBRA"}`}) ||
		len(res.reasoningDetails) != 1 || jsjson.Stringify(res.reasoningDetails[0]) != `{"index":0,"type":"reasoning.text","text":""}` {
		t.Fatalf("调工具：%+v", res)
	}
}

// 不报用量的网关（由 Venice 那份改的）：照常回完，没收到用量（UsageSeen 假、用量都是 0），不报错
func TestReplayNoUsage(t *testing.T) {
	res, err := replayClient(t, "no-usage.json").StreamChat(context.Background(), firstAsk(), nil)
	if err != nil || res.Text != "400" || res.Stop != "stop" || res.UsageSeen || res.Usage != (Usage{}) {
		t.Fatalf("不报用量照常回完：%+v %v", res, err)
	}
}
