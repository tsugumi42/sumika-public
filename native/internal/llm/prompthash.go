package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// promptHashes 是这次请求真正发出去的 messages、tools 的指纹（model_runs.prompt_hashes_json，提示词缓存排查用）：
// 不存正文，只存每条消息的角色、字节数和哈希链，tools 存字节数和哈希。
//
//	{"v":1,"tools":[字节数,"哈希"],"msgs":[["角色",字节数,"链"],...]}
//
// 链：第 i 条是 sha256(第 i-1 条的链 + 这条消息的原样 JSON 字节) 的前 16 个十六进制字（第 0 条前面是空串）。
// 两次请求的链从头比，最后一个相同的位置就是公共前缀到哪一条为止，第一个不同的就是断点（系统提示是第 0 条）。
// 字节按编码器实际嵌进请求体的那份算，所以和服务商收到的一致。拆不开（不是 JSON 数组）返回空串，调用方记 NULL
func promptHashes(raw, toolsJSON string) string {
	var msgs []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		return ""
	}
	type out struct {
		V     int     `json:"v"`
		Tools []any   `json:"tools,omitempty"`
		Msgs  [][]any `json:"msgs"`
	}
	o := out{V: 1, Msgs: make([][]any, 0, len(msgs))}
	if toolsJSON != "" {
		h := sha256.Sum256([]byte(toolsJSON))
		o.Tools = []any{len(toolsJSON), hex.EncodeToString(h[:8])}
	}
	chain := ""
	for _, m := range msgs {
		var head struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(m, &head)
		h := sha256.New()
		h.Write([]byte(chain))
		h.Write(m)
		chain = hex.EncodeToString(h.Sum(nil)[:8])
		o.Msgs = append(o.Msgs, []any{head.Role, len(m), chain})
	}
	b, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	return string(b)
}

// PromptPrefix 是两份 PromptHashes 的公共前缀：前多少条消息逐字节相同，以及这几条一共多少字节。
// 有一份拆不开就是 0、0
func PromptPrefix(a, b string) (msgs, bytes int) {
	type in struct {
		Msgs [][]any `json:"msgs"`
	}
	var x, y in
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return 0, 0
	}
	for i := 0; i < len(x.Msgs) && i < len(y.Msgs); i++ {
		if len(x.Msgs[i]) != 3 || len(y.Msgs[i]) != 3 || x.Msgs[i][2] != y.Msgs[i][2] {
			break
		}
		msgs++
		if n, ok := x.Msgs[i][1].(float64); ok {
			bytes += int(n)
		}
	}
	return msgs, bytes
}
