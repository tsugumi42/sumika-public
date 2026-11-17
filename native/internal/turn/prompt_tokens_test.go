package turn

import (
	"testing"

	"sumika-native/internal/llm"
)

// 服务商回 prompt_tokens=1000、缓存读 300、缓存写 200 时 llm.Usage.Input 是 500；
// 下一轮硬门槛看的上一步真实输入必须是 1000，不是 800（旧写法漏了缓存写）
func TestPromptTokensCountsCacheWrite(t *testing.T) {
	cases := []struct {
		name string
		u    llm.Usage
		want int64
	}{
		{"没有缓存", llm.Usage{Input: 1000}, 1000},
		{"只有缓存读", llm.Usage{Input: 700, CacheRead: 300}, 1000},
		{"首次写缓存", llm.Usage{Input: 800, CacheWrite: 200}, 1000},
		{"读写都有", llm.Usage{Input: 500, CacheRead: 300, CacheWrite: 200}, 1000},
		{"输出不算输入", llm.Usage{Input: 500, Output: 99, CacheRead: 300, CacheWrite: 200}, 1000},
		{"全零", llm.Usage{}, 0},
	}
	for _, c := range cases {
		if got := promptTokens(c.u); got != c.want {
			t.Errorf("%s: promptTokens = %d，应为 %d", c.name, got, c.want)
		}
	}
}
