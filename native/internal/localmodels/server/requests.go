package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"sumika-native/internal/localmodels/engine"
)

// 请求体的解析（规格第三节第一行）：比 Python 版严。Pydantic 会把 "128" 当成整数 128、把 128.0 当成 128；
// 这里类型不对一律 422，不做宽松转换（客户端是我们自己的）。422 的正文只写字段名和要求，不回显请求里的任何值，
// 连未知字段的名字也不回显（键名是调用方随便写的串，和输入文字一样不该出现在响应和日志里）。
//
// 不用 DisallowUnknownFields 直接解到结构体：它的错误文字带字段名，也分不清"缺字段"和"值是 null"。
// 先解成键到原始 JSON 的表，再逐个字段按类型解。

// embeddingsBody 是 /v1/embeddings 的请求。
type embeddingsBody struct {
	Model          string
	Input          []string
	EncodingFormat string // "float"（缺省）或 "base64"
	Dimensions     *int
	InputType      engine.InputType // 缺省 document
	Task           string
}

// rerankBody 是 /v1/rerank 的请求。
type rerankBody struct {
	Model     string
	Query     string
	Documents []string
	TopN      *int
}

var (
	embeddingsFields = []string{"model", "input", "encoding_format", "dimensions", "input_type", "task", "user"}
	rerankFields     = []string{"model", "query", "documents", "top_n"}
)

// bodyFields 把请求体拆成"键 → 原始 JSON"。不是 JSON、不是对象、有未知字段、不是合法 UTF-8 都是 422。
func bodyFields(body []byte, allowed []string) (map[string]json.RawMessage, *apiError) {
	if !utf8.Valid(body) { // 对着 Python 实测是 400（"解析请求体出错"），不是 422；Go 的解码器会悄悄把坏字节换成 U+FFFD，所以先拦
		return nil, newAPIError(400, "There was an error parsing the body")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, unprocessable("Request body must be a JSON object")
	}
	if hasLoneSurrogate(body) { // Python 对 \ud800 这样的转义回 422；Go 的解码器会悄悄换成 U+FFFD
		return nil, unprocessable("Request body contains a lone surrogate escape (not valid Unicode)")
	}
	known := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		known[k] = true
	}
	for k := range top {
		if !known[k] {
			return nil, unprocessable(fmt.Sprintf("Unknown field in the request body (extra fields are not allowed); allowed: %s", joinList(allowed)))
		}
	}
	return top, nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// stringField 是必填的字符串字段：缺、null、不是字符串都是 422。
func stringField(top map[string]json.RawMessage, name string) (string, *apiError) {
	raw, ok := top[name]
	if !ok {
		return "", unprocessable(fmt.Sprintf("Field '%s' is required", name))
	}
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", unprocessable(fmt.Sprintf("Field '%s' must be a string", name))
	}
	return s, nil
}

// optionalString 是可以缺、也可以是 null 的字符串字段（task、user）。
func optionalString(top map[string]json.RawMessage, name string) (string, *apiError) {
	raw, ok := top[name]
	if !ok || isNull(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", unprocessable(fmt.Sprintf("Field '%s' must be a string or null", name))
	}
	return s, nil
}

// optionalInt 是可以缺、也可以是 null 的整数字段（dimensions、top_n）。1.5、"128"、true 都不行。
func optionalInt(top map[string]json.RawMessage, name string) (*int, *apiError) {
	raw, ok := top[name]
	if !ok || isNull(raw) {
		return nil, nil
	}
	var n int
	if json.Unmarshal(raw, &n) != nil {
		return nil, unprocessable(fmt.Sprintf("Field '%s' must be an integer or null", name))
	}
	return &n, nil
}

// stringList 是必填的字符串数组字段；allowSingle 为真时也收一个字符串（embeddings 的 input）。
func stringList(top map[string]json.RawMessage, name string, allowSingle bool) ([]string, *apiError) {
	raw, ok := top[name]
	if !ok {
		return nil, unprocessable(fmt.Sprintf("Field '%s' is required", name))
	}
	trimmed := bytes.TrimSpace(raw)
	if allowSingle && len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) != nil {
			return nil, unprocessable(fmt.Sprintf("Field '%s' must be a string or an array of strings", name))
		}
		return []string{s}, nil
	}
	what := "an array of strings"
	if allowSingle {
		what = "a string or an array of strings"
	}
	var items []json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(trimmed, &items) != nil {
		return nil, unprocessable(fmt.Sprintf("Field '%s' must be %s", name, what))
	}
	list := make([]string, len(items))
	for i, item := range items {
		// 逐个解：直接解成 []string 的话，数组里的 null 会悄悄变成空串（encoding/json 把 null 当成什么都不做）
		if isNull(item) || json.Unmarshal(item, &list[i]) != nil {
			return nil, unprocessable(fmt.Sprintf("Field '%s' must be %s", name, what))
		}
	}
	return list, nil
}

func parseEmbeddings(body []byte) (embeddingsBody, *apiError) {
	var out embeddingsBody
	top, apiErr := bodyFields(body, embeddingsFields)
	if apiErr != nil {
		return out, apiErr
	}
	if out.Model, apiErr = stringField(top, "model"); apiErr != nil {
		return out, apiErr
	}
	if out.Input, apiErr = stringList(top, "input", true); apiErr != nil {
		return out, apiErr
	}
	out.EncodingFormat = "float"
	if raw, ok := top["encoding_format"]; ok {
		var s string
		if json.Unmarshal(raw, &s) != nil || (s != "float" && s != "base64") { // null 也进这里：Literal 不收 null
			return out, unprocessable("Field 'encoding_format' must be \"float\" or \"base64\"")
		}
		out.EncodingFormat = s
	}
	if out.Dimensions, apiErr = optionalInt(top, "dimensions"); apiErr != nil {
		return out, apiErr
	}
	out.InputType = engine.InputDocument
	if raw, ok := top["input_type"]; ok {
		var s string
		if json.Unmarshal(raw, &s) != nil || (s != "query" && s != "document") {
			return out, unprocessable("Field 'input_type' must be \"query\" or \"document\"")
		}
		out.InputType = engine.InputType(s)
	}
	if out.Task, apiErr = optionalString(top, "task"); apiErr != nil {
		return out, apiErr
	}
	if _, apiErr = optionalString(top, "user"); apiErr != nil { // 收下不用，但类型要对
		return out, apiErr
	}
	return out, nil
}

func parseRerank(body []byte) (rerankBody, *apiError) {
	var out rerankBody
	top, apiErr := bodyFields(body, rerankFields)
	if apiErr != nil {
		return out, apiErr
	}
	if out.Model, apiErr = stringField(top, "model"); apiErr != nil {
		return out, apiErr
	}
	if out.Query, apiErr = stringField(top, "query"); apiErr != nil {
		return out, apiErr
	}
	if out.Documents, apiErr = stringList(top, "documents", false); apiErr != nil {
		return out, apiErr
	}
	if out.TopN, apiErr = optionalInt(top, "top_n"); apiErr != nil {
		return out, apiErr
	}
	return out, nil
}
