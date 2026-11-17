package turn

import (
	"testing"

	"sumika-native/internal/llm"
)

// 窗口来源不可靠（保守默认、资料库各家说法不一致）时硬线比例最多 0.75；服务商报的和手填的照配置
func TestHardLineIsMoreConservativeWhenWindowUncertain(t *testing.T) {
	t.Parallel()
	c := ContextLimits{}
	window := 200000
	normal, uncertain := c.hardFor(window, false), c.hardFor(window, true)
	if normal != 170000 || uncertain != 150000 {
		t.Fatalf("普通 %v 不可靠 %v", normal, uncertain)
	}
	// 配置里本来就比 0.75 低的不抬高
	if got := (ContextLimits{HardRatio: 0.6}).hardFor(window, true); got != 120000 {
		t.Fatalf("低于 0.75 的不抬高：%v", got)
	}
	for name, m := range map[string]llm.Model{
		"默认":     {Limits: llm.LimitSources{Window: llm.SourceDefault}},
		"资料库不一致": {Limits: llm.LimitSources{Window: llm.SourceModelsDev, DevMixed: true}},
	} {
		if !m.WindowUncertain() {
			t.Errorf("%s 该算不可靠", name)
		}
	}
	for name, m := range map[string]llm.Model{
		"服务商":  {Limits: llm.LimitSources{Window: llm.SourceProvider}},
		"手填":   {Limits: llm.LimitSources{Window: llm.SourceUser}},
		"资料库":  {Limits: llm.LimitSources{Window: llm.SourceModelsDev}},
		"没填来源": {},
	} {
		if m.WindowUncertain() {
			t.Errorf("%s 不该算不可靠", name)
		}
	}
	// limits()：没报输出上限按 32000，报了用报的
	if _, out := limits(llm.Model{}); out != llm.FallbackMaxOutput {
		t.Fatalf("兜底：%d", out)
	}
	if _, out := limits(llm.Model{MaxOutputTokens: 128000}); out != 128000 {
		t.Fatalf("报了：%d", out)
	}
}
