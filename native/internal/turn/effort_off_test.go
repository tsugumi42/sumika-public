package turn

import (
	"testing"

	"sumika-native/internal/llm"
)

// 思考档位选“关”：能思考的模型返回 off（llm 发 none 真关），不支持思考的模型不发；其他档位不变
func TestEffortOffKeptForReasoningModels(t *testing.T) {
	yes, no := true, false
	r := llm.Model{Reasoning: &yes}
	if got := Effort(r, "off"); got != "off" {
		t.Fatalf("能思考的模型选 off 要保留 off：%q", got)
	}
	if got := Effort(r, "high"); got != "high" {
		t.Fatalf("high 不变：%q", got)
	}
	if got := Effort(llm.Model{Reasoning: &no}, "off"); got != "" {
		t.Fatalf("不支持思考的模型不发：%q", got)
	}
}
