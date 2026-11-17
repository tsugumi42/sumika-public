package turn

// 线上字节对拍（规格-模型对话编解码 设计第 1 条）：假网关收到的整份请求体逐字节存下来，比键序、空白、转义。
// 重录：先在 testdata/wire/intended.md 登记一行，再 WIRE_UPDATE=1 跑。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/llm/llmtest/wire"
	"sumika-native/internal/tools"
)

// wireDir 是这个包的录下字节放的地方（go test 的工作目录就是包目录）
const wireDir = "testdata/wire"

// wireModel 是能看图、能思考的固定模型（线上字节对拍只认名字和上限，不连真服务）
var wireModel = json.RawMessage(`{"id":"test/vision","context_length":1048000,"max_output_tokens":32000,"reasoning":true,"modalities":{"input":["text","image"],"output":["text"]},"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]}`)

func wireWeatherDecl() tools.Declaration {
	return tools.Declaration{Name: "weather", Description: "查天气", Parameters: `{"type":"object","properties":{}}`}
}

// wireGate 返回带一次图片结果的天气工具插件
func wireTool(t *testing.T, res tools.Result) toolPlugin {
	t.Helper()
	return toolPlugin{"wire-weather", []Tool{{Decl: wireWeatherDecl(), Exec: func(context.Context, ToolCall) (tools.Result, error) {
		return res, nil
	}}}}
}

// wireBodies 取第 from 次以后的全部请求体，并把每次运行都不一样的临时工作目录换成 <WORKDIR>
func wireBodies(t *testing.T, h *harness, wd string, from int) [][]byte {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) <= from {
		t.Fatalf("只发了 %d 次请求，要从第 %d 次取", len(h.bodies), from+1)
	}
	out := make([][]byte, 0, len(h.bodies)-from)
	for _, body := range h.bodies[from:] {
		out = append(out, wireMask(wd, []byte(body)))
	}
	return out
}

// wireMask 把工作目录的几种写法（JSON 转义过的、原样的、正斜杠的、小写的）都换成 <WORKDIR>。
// 只在原样字节串上换，不解析、不重排——其余字节仍然逐字节比
func wireMask(wd string, body []byte) []byte {
	if wd == "" {
		return body
	}
	esc, _ := json.Marshal(wd)
	forms := []string{string(esc[1 : len(esc)-1]), wd, strings.ReplaceAll(wd, `\`, "/")}
	for _, f := range forms[:3] {
		forms = append(forms, strings.ToLower(f))
	}
	s := string(body)
	for _, f := range forms {
		s = strings.ReplaceAll(s, f, "<WORKDIR>")
	}
	return []byte(s)
}

// wireBody 把一份请求经真的 llm.Client 发到固定的假网关，返回它收到的原样字节
func wireBody(t *testing.T, req chat.Request) []byte {
	t.Helper()
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text("好的")}, &bodies, &mu))
	defer srv.Close()
	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	if _, err := client.StreamChat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("应该发一次请求，发了 %d 次", len(bodies))
	}
	return []byte(bodies[0])
}

// wireBuilt 是 requestFor 要的固定一半：模型、上限、工具声明、缓存键都不随运行变
func wireBuilt() *Built {
	yes := true
	model := llm.Model{ID: "test/vision", ContextLength: 1048000, MaxOutputTokens: 32000, Reasoning: &yes}
	return &Built{
		Model: model, SystemRole: chat.SystemRoleDeveloper, Window: 1048000, MaxOut: 32000,
		CacheKey: "wire:cache", Effort: "low", Decls: []tools.Declaration{wireWeatherDecl()},
	}
}

func wireArgs(t *testing.T, raw string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 1. 纯文字一轮
func TestWirePlainText(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	h := newHarnessOpts(t, harnessOpts{wd: wd, replies: `[` + textReply + `]`})
	h.send("在吗")
	wire.Check(t, wireDir, "plain-text", wireBodies(t, h, wd, 0)...)
}

// 2. 调工具一轮：两次请求都存
func TestWireToolCall(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	h := newHarnessOpts(t, harnessOpts{wd: wd, replies: `[` + callReply("weather", `{}`) + `,` + textReply + `]`})
	h.use(wireTool(t, tools.Result{Text: "晴，二十度"}))
	h.send("今天天气怎么样")
	wire.Check(t, wireDir, "tool-call", wireBodies(t, h, wd, 0)...)
}

// 3. 工具结果带图：第二次请求里 tool 消息后面另起一条 user 消息带图片
func TestWireToolImage(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	const url = "data:image/png;base64,wiretoolimagepayload"
	h := newHarnessOpts(t, harnessOpts{wd: wd, model: "test/vision", models: []json.RawMessage{wireModel},
		replies: `[` + callReply("weather", `{}`) + `,` + textReply + `]`})
	h.use(wireTool(t, tools.Result{Text: "Read image file [image/png]", Images: []llm.Image{{DataURL: url}}}))
	h.send("看看这张图")
	bodies := wireBodies(t, h, wd, 0)
	if len(bodies) != 2 || !strings.Contains(string(bodies[0]), "test/vision") || !strings.Contains(string(bodies[1]), "Attached image(s) from tool result:") {
		t.Fatalf("第二次请求里应该有工具结果的图：%d 次请求", len(bodies))
	}
	wire.Check(t, wireDir, "tool-image", bodies...)
}

// 4a. 思考回放：reasoning_content
func TestWireThinkingContent(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	h := newHarnessOpts(t, harnessOpts{wd: wd, replies: `[{"reasoning":["先想想"],"content":["好的"],` + usage + `},` + textReply + `]`})
	h.send("第一句")
	h.send("第二句")
	bodies := wireBodies(t, h, wd, 0)
	if !strings.Contains(string(bodies[1]), `"reasoning_content":"先想想"`) {
		t.Fatalf("第二次请求没带回 reasoning_content：%.300s", bodies[1])
	}
	wire.Check(t, wireDir, "thinking-content", bodies...)
}

// 4b. 思考回放：reasoning_details
func TestWireThinkingDetails(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	detail := `{"type":"reasoning.text","text":"想过了","signature":"sig-wire","id":"detail-1","format":"openai-responses-v1","index":0}`
	h := newHarnessOpts(t, harnessOpts{wd: wd, replies: `[{"reasoningDetails":[[` + detail + `]],"content":["好的"],` + usage + `},` + textReply + `]`})
	h.send("第一句")
	h.send("第二句")
	bodies := wireBodies(t, h, wd, 0)
	if !strings.Contains(string(bodies[1]), `"reasoning_details":[{`) {
		t.Fatalf("第二次请求没带回 reasoning_details：%.300s", bodies[1])
	}
	wire.Check(t, wireDir, "thinking-details", bodies...)
}

// 5. assistant 空正文写 null
func TestWireAssistantNullContent(t *testing.T) {
	t.Parallel()
	b := wireBuilt()
	msgs := []Msg{
		{Role: "user", Text: "查一下天气"},
		{Role: "assistant", Calls: []Call{{ID: "call-null", Name: "weather", Args: wireArgs(t, `{}`)}}},
		{Role: "toolResult", CallID: "call-null", Text: "晴"},
		{Role: "user", Text: "继续"},
	}
	body := wireBody(t, b.requestFor(msgs))
	if !strings.Contains(string(body), `"content":null`) {
		t.Fatalf("assistant 空正文应写 null：%.300s", body)
	}
	wire.Check(t, wireDir, "assistant-null", body)
}

// 6. 没闭合的调用补 No result provided
func TestWireUnclosedCall(t *testing.T) {
	t.Parallel()
	b := wireBuilt()
	msgs := []Msg{
		{Role: "user", Text: "查两个地方"},
		{Role: "assistant", Calls: []Call{
			{ID: "call-a", Name: "weather", Args: wireArgs(t, `{}`)},
			{ID: "call-b", Name: "weather", Args: wireArgs(t, `{}`)},
		}},
		{Role: "toolResult", CallID: "call-a", Text: "晴"},
		{Role: "user", Text: "继续"},
	}
	body := wireBody(t, b.requestFor(msgs))
	if !strings.Contains(string(body), `"content":"No result provided","tool_call_id":"call-b"`) {
		t.Fatalf("没闭合的调用应补 No result provided：%.500s", body)
	}
	wire.Check(t, wireDir, "unclosed-call", body)
}

// 7. 停掉的那条不发
func TestWireStoppedAssistantSkipped(t *testing.T) {
	t.Parallel()
	b := wireBuilt()
	msgs := []Msg{
		{Role: "system", Text: "你是测试角色。"},
		{Role: "user", Text: "第一句"},
		{Role: "assistant", Text: "半句话", Stop: "aborted", Calls: []Call{{ID: "call-stop", Name: "weather", Args: wireArgs(t, `{}`)}}},
		{Role: "user", Text: "第二句"},
	}
	body := wireBody(t, b.requestFor(msgs))
	if strings.Contains(string(body), "半句话") || strings.Contains(string(body), "call-stop") {
		t.Fatalf("停掉的那条不该出现在请求里：%.500s", body)
	}
	wire.Check(t, wireDir, "stopped-skip", body)
}

// 8. 子任务
func TestWireSubtask(t *testing.T) {
	t.Parallel()
	wd := sameLengthTempDir(t)
	got := make(chan []byte, 1)
	h := newHarnessOpts(t, harnessOpts{wd: wd, replies: `[` + textReply + `]`, mutate: func(c *Config) {
		c.InternalAuthorize = func(context.Context, kernel.Invocation, string) error { return nil }
	},
		onBody: func(_ context.Context, body string) {
			if !strings.Contains(body, "记录线上字节的子任务") {
				return
			}
			select {
			case got <- []byte(body):
			default:
			}
		},
	})
	inv := kernel.Invocation{
		RootID: "wire-child-root", RunID: "wire-child-run", ParentRunID: "wire-parent-run", Kind: kernel.KindChild,
		Scope: kernel.Scope{CharacterID: h.c.CharacterID, Conversation: "internal:wire-child", AuthorityID: "wire-authority", AuthorityRevision: 1},
	}
	limits := h.hub.Cfg.BudgetConfig.Limits(time.Now())
	if err := AdmitBudgetRoot(t.Context(), h.hub.Cfg.Budget, inv, limits); err != nil {
		t.Fatal(err)
	}
	handle, err := h.hub.StartInternal(t.Context(), InternalRun{Identity: inv, Goal: "记录线上字节的子任务", ModelID: "test/think", Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	body := recv(t, got, "子任务第一次请求")
	if out := recv(t, handle.Done, "子任务完成"); out.Err != nil {
		t.Fatal(out.Err)
	}
	wire.Check(t, wireDir, "subtask", wireMask(wd, body))
}

// 9. 压缩
func TestWireCompact(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `]`, mutate: smallKeep})
	h.seedRounds(6)
	if c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0); err != nil || !c.OK {
		t.Fatalf("压缩应该成功：%+v %v", c, err)
	}
	wire.Check(t, wireDir, "compact", wireBodies(t, h, "", 0)...)
}
