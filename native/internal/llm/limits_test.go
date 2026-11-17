package llm

import "testing"

func TestClampOutput(t *testing.T) {
	m := Model{ContextLength: 100000, MaxOutputTokens: 64000}
	if got := m.ClampOutput(180000, 64000); got != 5904 {
		t.Fatalf("got %d", got)
	}
	if got := m.ClampOutput(100, 0); got != 64000 {
		t.Fatalf("小输入不该压：%d", got)
	}
	if got := (Model{}).ClampOutput(100, 0); got != FallbackMaxOutput {
		t.Fatalf("没报的用兜底：%d", got)
	}
	if got := m.ClampOutput(400000, 64000); got != 1 {
		t.Fatalf("溢出时至少 1：%d", got)
	}
}
