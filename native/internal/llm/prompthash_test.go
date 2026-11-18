package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptHashesChainAndPrefix(t *testing.T) {
	t.Parallel()
	base := `[{"role":"system","content":"规则"},{"role":"user","content":"你好"},{"role":"assistant","content":"嗯"}]`
	a := promptHashes(base, `[{"type":"function"}]`)
	var parsed struct {
		V     int     `json:"v"`
		Tools []any   `json:"tools"`
		Msgs  [][]any `json:"msgs"`
	}
	if err := json.Unmarshal([]byte(a), &parsed); err != nil || parsed.V != 1 || len(parsed.Msgs) != 3 || len(parsed.Tools) != 2 {
		t.Fatalf("指纹格式不对：%s %v", a, err)
	}
	if parsed.Msgs[0][0] != "system" || parsed.Msgs[1][0] != "user" || parsed.Msgs[0][1].(float64) != float64(len(`{"role":"system","content":"规则"}`)) {
		t.Fatalf("角色或字节数不对：%v", parsed.Msgs)
	}
	if strings.Contains(a, "你好") || strings.Contains(a, "规则") {
		t.Fatalf("指纹里不能有正文：%s", a)
	}
	// 只在末尾追加：前三条的链不变
	longer := promptHashes(strings.TrimSuffix(base, "]")+`,{"role":"user","content":"再说"}]`, "")
	if n, b := PromptPrefix(a, longer); n != 3 || b == 0 {
		t.Fatalf("追加以后公共前缀应是 3 条：%d %d", n, b)
	}
	// 中间那条差一个字节：从那条起都不同，链会把后面的都带歪
	changed := promptHashes(strings.Replace(base, "你好", "你好 ", 1), "")
	if n, _ := PromptPrefix(a, changed); n != 1 {
		t.Fatalf("第 1 条改了，公共前缀应只剩系统提示：%d", n)
	}
	if promptHashes(`{"不是数组":1}`, "") != "" {
		t.Fatal("拆不开应返回空")
	}
}
