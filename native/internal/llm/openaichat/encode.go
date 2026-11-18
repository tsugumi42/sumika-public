// Package openaichat 是 OpenAI 兼容聊天接口的编解码器：把 chat.Request 拼成请求体字节，把流式回复解成 chat.Result。
// 编码照搬今天的 turn.messagesJSON、tools.RequestJSON、llm.UserContent、llm.chatBody：
// 手写固定字段顺序、jsjson.Quote、jsjson.Stringify 原样使用，服务商缓存按字节前缀认，输出不能变。
// 解码只管线上格式，不含计时和看门狗（那些留在 llm.Client）。
package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/tools"
)

// Codec 是写进 chat.Replay.Codec 的编解码器名字
const Codec = "openai-chat"

// Encode 拼出请求体。前台风格走手写 messages 数组，后台风格走 marshalNoEscape 的普通消息；
// 两种风格的字节不一样，这里都保留
func Encode(req chat.Request) ([]byte, error) {
	body, _, _, err := EncodeRequest(req)
	return body, err
}

// EncodeRequest 拼出请求体，同时返回嵌进去的 messages、tools 两段原文。
// 提示词指纹按这两段算（llm.PromptHashes 的口径），所以和实际发出去的字节一致。
func EncodeRequest(req chat.Request) (body []byte, messages, tools string, err error) {
	var b bytes.Buffer
	model, err := marshalNoEscape(req.Model)
	if err != nil {
		return nil, "", "", err
	}
	b.WriteString(`{"model":`)
	b.Write(model)
	b.WriteString(`,"messages":`)
	if req.Style == chat.StyleBackground {
		raw, err := marshalNoEscape(backgroundMessages(req.Messages))
		if err != nil {
			return nil, "", "", err
		}
		if string(raw) == "null" {
			raw = []byte("[]")
		}
		messages = string(raw)
		b.Write(raw)
	} else {
		messages = messagesJSON(req)
		b.WriteString(messages)
	}
	b.WriteString(`,"stream":true,"stream_options":{"include_usage":true}`)
	put := func(key string, v any) error {
		raw, err := marshalNoEscape(v)
		if err != nil {
			return err
		}
		b.WriteString(`,"` + key + `":`)
		b.Write(raw)
		return nil
	}
	if req.CacheKey != "" {
		if err := put("prompt_cache_key", req.CacheKey); err != nil {
			return nil, "", "", err
		}
	}
	if req.Thinking != "" {
		effort := req.Thinking
		if effort == "off" {
			effort = "none" // 关思考：OpenAI 兼容接口的写法，Magpie 原样转发
		}
		if err := put("reasoning_effort", effort); err != nil {
			return nil, "", "", err
		}
	}
	if req.MaxOutputTokens > 0 {
		if err := put("max_completion_tokens", req.MaxOutputTokens); err != nil {
			return nil, "", "", err
		}
	}
	if req.LongCache {
		b.WriteString(`,"prompt_cache_retention":"24h"`)
	}
	if req.NoStore {
		b.WriteString(`,"store":false`)
	}
	if req.Tools != nil {
		tools = toolsJSON(req.Tools)
		b.WriteString(`,"tools":` + tools)
	}
	b.WriteByte('}')
	return b.Bytes(), messages, tools, nil
}

// messagesJSON 是前台风格的 messages 数组（turn/loop.go 的 messagesJSON）：
// 没有结果的调用补 "No result provided"，带图的工具结果另起一条 user 消息，停掉的那条整条不发
func messagesJSON(req chat.Request) string {
	parts := make([]string, 0, len(req.Messages))
	q := jsjson.Quote
	tool := func(content, id string) string {
		return `{"role":"tool","content":` + q(content) + `,"tool_call_id":` + q(id) + `}`
	}
	// 没有结果的调用（被停下时还没开始的）：下一条 assistant、user 之前，或者到最后，补一条 "No result provided"
	var pending []chat.ToolCall
	answered := map[string]bool{}
	closePending := func() {
		for _, c := range pending {
			if !answered[c.ID] {
				parts = append(parts, tool("No result provided", c.ID))
			}
		}
		pending, answered = nil, map[string]bool{}
	}
	// 工具结果带图：tool 消息只能放字，图跟在这一串结果后面作为一条 user 消息发出；模型不能看图就不发
	var toolImages []chat.Image
	flushImages := func() {
		if len(toolImages) > 0 {
			parts = append(parts, `{"role":"user","content":`+userContent("Attached image(s) from tool result:", toolImages)+`}`)
		}
		toolImages = nil
	}
	systemRole := "system"
	if req.SystemRole == chat.SystemRoleDeveloper {
		systemRole = "developer"
	}
	for _, m := range req.Messages {
		if m.Omit {
			// 旧代码对停掉的 assistant 也先收尾未闭合的调用、发掉攒着的工具图，再整条不发
			if m.Role == chat.RoleAssistant {
				closePending()
				flushImages()
			}
			continue
		}
		switch m.Role {
		case chat.RoleSystem:
			flushImages()
			parts = append(parts, `{"role":`+q(systemRole)+`,"content":`+q(textOf(m.Blocks))+`}`)
		case chat.RoleUser:
			closePending()
			flushImages()
			parts = append(parts, `{"role":"user","content":`+userContent(textOf(m.Blocks), imagesOf(m.Blocks))+`}`)
		case chat.RoleAssistant:
			closePending()
			flushImages()
			calls := callsOf(m.Blocks)
			text := textOf(m.Blocks)
			if textclean.Trim(text) == "" {
				text = ""
			}
			// 没有正文也没有工具调用的整条不发
			if text == "" && len(calls) == 0 {
				continue
			}
			if len(calls) > 0 {
				pending = calls
			}
			s := `{"role":"assistant","content":`
			if text == "" {
				s += "null"
			} else {
				s += q(text)
			}
			var field string
			var details []jsjson.Value
			if thinking, ok := thinkingOf(m.Blocks); ok {
				field, details = ReplayParts(thinking.Replay)
				// 有结构化的思考时只带它；否则同一个模型的思考按原来的字段带回
				if len(details) == 0 && jsstr.Trim(thinking.Text) != "" && field != "" {
					s += `,` + q(field) + `:` + q(thinking.Text)
				}
			}
			if len(calls) > 0 {
				cs := make([]string, len(calls))
				for i, c := range calls {
					cs[i] = `{"id":` + q(c.ID) + `,"type":"function","function":{"name":` + q(c.Name) + `,"arguments":` + q(jsjson.Stringify(c.Arguments)) + `}}`
				}
				s += `,"tool_calls":[` + strings.Join(cs, ",") + `]`
			}
			if len(details) > 0 {
				s += `,"reasoning_details":` + jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: details})
			}
			parts = append(parts, s+"}")
		case chat.RoleTool:
			for _, block := range m.Blocks {
				result, ok := block.(chat.ToolResult)
				if !ok {
					continue
				}
				content := result.Text
				if textclean.Trim(content) == "" {
					content = "(no tool output)"
				}
				answered[result.CallID] = true
				parts = append(parts, tool(content, result.CallID))
				if req.SeeImages {
					toolImages = append(toolImages, result.Images...)
				}
			}
		}
	}
	closePending()
	flushImages()
	return "[" + strings.Join(parts, ",") + "]"
}

// backgroundMessages 是后台风格的 messages：只有角色和文字，由 marshalNoEscape 写出（今天 memoryGenerator、顾问的路径）
func backgroundMessages(msgs []chat.Message) []plainMessage {
	out := make([]plainMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Omit {
			continue
		}
		out = append(out, plainMessage{Role: string(m.Role), Content: textOf(m.Blocks)})
	}
	return out
}

type plainMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// userContent 是一条 user 消息的 content（llm.UserContent 原样搬来）
func userContent(text string, images []chat.Image) string {
	if len(images) == 0 {
		return jsjson.Quote(text)
	}
	parts := make([]string, 0, len(images)+1)
	parts = append(parts, `{"type":"text","text":`+jsjson.Quote(text)+`}`)
	for _, image := range images {
		parts = append(parts, `{"type":"image_url","image_url":{"url":`+jsjson.Quote(image.DataURL)+`}}`)
	}
	return `[` + strings.Join(parts, ",") + `]`
}

// toolsJSON 是请求里的 tools 字段（tools.RequestJSON 原样搬来）：这个地址不支持 strict，不发 strict
func toolsJSON(ds []tools.Declaration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = `{"type":"function","function":{"name":` + lit(d.Name) + `,"description":` + lit(d.Description) + `,"parameters":` + d.Parameters + `}}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// lit 照 tools.lit：store.JSString 写出 JS 的字符串
func lit(s string) string {
	j, _ := store.JSString(s)
	return j
}

// marshalNoEscape 是 chatBody 用的同一个写法（关 HTML 转义、去掉 Encoder 的换行）
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// textOf 把一条消息里的正文块接起来；后台风格只有正文，前台风格一条消息也只有一块。
// 多条正文是类型允许、今天数据里没有的写法，接起来不丢字
func textOf(blocks []chat.Block) string {
	var b strings.Builder
	for _, block := range blocks {
		if text, ok := block.(chat.Text); ok {
			b.WriteString(string(text))
		}
	}
	return b.String()
}

func imagesOf(blocks []chat.Block) []chat.Image {
	var out []chat.Image
	for _, block := range blocks {
		if image, ok := block.(chat.Image); ok {
			out = append(out, image)
		}
	}
	return out
}

func callsOf(blocks []chat.Block) []chat.ToolCall {
	var out []chat.ToolCall
	for _, block := range blocks {
		if call, ok := block.(chat.ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
}

func thinkingOf(blocks []chat.Block) (chat.Thinking, bool) {
	for _, block := range blocks {
		if thinking, ok := block.(chat.Thinking); ok {
			return thinking, true
		}
	}
	return chat.Thinking{}, false
}

// ReplayFrom 把解码器认出的思考出处（线上字段名、结构化数据）打包成不透明回放。
// 格式是 {"field":"reasoning_content","details":[...]}；只有本编解码器解析
func ReplayFrom(field string, details []jsjson.Value) chat.Replay {
	if field == "" && len(details) == 0 {
		return chat.Replay{}
	}
	parts := make([]string, 0, 2)
	if field != "" {
		parts = append(parts, `"field":`+jsjson.Quote(field))
	}
	if len(details) > 0 {
		parts = append(parts, `"details":`+jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: details}))
	}
	return chat.Replay{Codec: Codec, Data: []byte("{" + strings.Join(parts, ",") + "}")}
}

// ReplayParts 是 ReplayFrom 的反向：取出思考字段和结构化思考。
// 不是本编解码器写的（Codec 非空且不是 Codec）或数据读不懂，就当没有回放
func ReplayParts(r chat.Replay) (field string, details []jsjson.Value) {
	if len(r.Data) == 0 || r.Codec != "" && r.Codec != Codec {
		return "", nil
	}
	v, err := jsjson.Parse(string(r.Data))
	if err != nil || v.Kind != jsjson.Object {
		return "", nil
	}
	if f, ok := v.Obj["field"]; ok && f.Kind == jsjson.String {
		field = f.Str
	}
	if d, ok := v.Obj["details"]; ok && d.Kind == jsjson.Array {
		details = d.Arr
	}
	return field, details
}
