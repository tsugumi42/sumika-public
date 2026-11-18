package config

import (
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"sumika-native/internal/kernel"
)

func TestKernelBudgetConfigurationBoundary(t *testing.T) {
	for _, raw := range []string{"maxRequests: 0", "maxTools: -1", "maxTokens: 1.5", "maxOutputTokens: '10'", "maxDurationMs: null", "requestOutputTokens: false", "requestOutputTokens: -1", "maxCostMicros: -1", "local: 5", "local:\n  maxRequests: -1", "local:\n  requestOutputTokens: 1.5", "local:\n  requestOutputTokens: -5", "local:\n  maxCostMicros: 5", "local:\n  maxDurationMs: null"} {
		var b Budget
		if err := yaml.Unmarshal([]byte(raw), &b); err == nil {
			t.Errorf("invalid budget accepted: %s", raw)
		}
	}
	// 金额门设不了：模型调用端没有可证明的价格上界，设了账本会把每个请求都拒掉，所以保存（加载）时就拒绝，并说明原因
	for _, raw := range []string{"maxCostMicros: 0", "maxCostMicros: 5000"} {
		var c Budget
		err := yaml.Unmarshal([]byte(raw), &c)
		if err == nil || !strings.Contains(err.Error(), "价格上界") {
			t.Errorf("%s 应该被拒绝并写明原因：%v", raw, err)
		}
	}
	var b Budget
	if err := yaml.Unmarshal([]byte("maxCostMicros: null\nmaxRequests: 30"), &b); err != nil || b.MaxCostMicros != nil || b.MaxRequests != 30 || b.Local != DefaultBudgetLocal() {
		t.Fatalf("null 金额门、其余默认：%+v %v", b, err)
	}
	for _, raw := range []string{"maxToolsPerRun: 0", "maxToolsGlobal: 33", "maxModelsGlobal: 1.5", "maxModelsGlobal: null"} {
		var s Scheduler
		if err := yaml.Unmarshal([]byte(raw), &s); err == nil {
			t.Errorf("invalid scheduler accepted: %s", raw)
		}
	}
}

// 本地干活和别的场合的额度分开：只有主人在本地主窗口私聊用 local 那组，QQ、本地群聊、后台任务保持顶层那组（不放宽）。
// local 默认全关（0 = 不设）：账本里是 kernel.NoLimit 和一百年后的时限
func TestBudgetLimitsForScope(t *testing.T) {
	b := DefaultBudget()
	at := time.Unix(1000, 0)
	local := kernel.Scope{Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner", Conversation: "console:x"}
	got := b.LimitsFor(local, at)
	if got.MaxRequests != kernel.NoLimit || got.MaxTools != kernel.NoLimit || got.MaxTokens != kernel.NoLimit || got.MaxOutputTokens != kernel.NoLimit ||
		got.RequestOutputTokens != 0 || !got.Deadline.Equal(at.Add(kernel.NoDeadlineAfter)) {
		t.Fatalf("本地私聊默认应该全关：%+v", got)
	}
	qq, grp, other, topic := local, local, local, local
	qq.Platform, qq.Channel = "qq", "qq"
	grp.Scene = "group"
	other.ActorKind = "human"
	topic.Conversation = "console:x:group:g"
	want := b.Limits(at)
	for name, sc := range map[string]kernel.Scope{"QQ": qq, "本地群聊": grp, "非主人": other, "群会话号": topic, "空身份（后台）": {}} {
		if g := b.LimitsFor(sc, at); g != want {
			t.Errorf("%s 不该放宽：%+v，想要 %+v", name, g, want)
		}
	}
	// 无人值守的数保持原样，单次输出不再是 4096，累计输出不被单次吃光（等于累计令牌）
	if want.MaxRequests != 24 || want.MaxTools != 60 || want.MaxTokens != 524288 || want.MaxOutputTokens != want.MaxTokens || want.RequestOutputTokens != 0 || want.Deadline != at.Add(15*time.Minute) {
		t.Fatalf("无人值守的数：%+v", want)
	}
	// 配置里写了的项才生效，没写的仍然是关
	var cfg Budget
	if err := yaml.Unmarshal([]byte("maxRequests: 10\nlocal:\n  maxRequests: 50\n  maxDurationMs: 60000\n"), &cfg); err != nil || cfg.MaxRequests != 10 || cfg.Local.MaxRequests != 50 || cfg.Local.MaxTools != 0 {
		t.Fatalf("local 块只改写的那几项：%+v %v", cfg, err)
	}
	l := cfg.LimitsFor(local, at)
	if l.MaxRequests != 50 || l.MaxTools != kernel.NoLimit || !l.Deadline.Equal(at.Add(time.Minute)) {
		t.Fatalf("写了的限额生效、没写的不设：%+v", l)
	}
}

// 回归：本地私聊默认全是 0，曾经用"Local 整块是零值"判断有没有加载配置，默认值会悄悄掉回顶层的 24 次硬停。
// 从配置文件加载（哪怕没写 local 块）和 DefaultBudget 都必须算"已加载"；手拼的 Budget 才用顶层那组
func TestBudgetLocalDefaultIsNotMistakenForUnloaded(t *testing.T) {
	at := time.Unix(1000, 0)
	local := kernel.Scope{Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner", Conversation: "console:x"}
	var loaded Budget
	if err := yaml.Unmarshal([]byte("maxRequests: 30"), &loaded); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]Budget{"默认": DefaultBudget(), "配置里没写 local": loaded} {
		if got := b.LimitsFor(local, at); got.MaxRequests != kernel.NoLimit {
			t.Errorf("%s：本地私聊被当成没加载、掉回顶层硬停：%+v", name, got)
		}
	}
	hand := Budget{MaxRequests: 5, MaxTools: 5, MaxTokens: 100, MaxOutputTokens: 100, MaxDurationMs: 1000}
	if got := hand.LimitsFor(local, at); got.MaxRequests != 5 {
		t.Errorf("手拼的 Budget 应该所有场合用顶层那组：%+v", got)
	}
}

// 自动发起的轮不拿本地的额度（目标续轮、定期任务无人值守）
func TestBudgetLimitsForInputAutoUsesTopLevel(t *testing.T) {
	b := DefaultBudget()
	at := time.Unix(1000, 0)
	local := kernel.Scope{Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner", Conversation: "console:x"}
	if got := b.LimitsForInput(local, true, at); got != b.Limits(at) {
		t.Fatalf("自动轮应该用顶层额度：%+v", got)
	}
	if got := b.LimitsForInput(local, false, at); got != b.LimitsFor(local, at) || got.MaxRequests != kernel.NoLimit {
		t.Fatalf("主人亲手发的用 local：%+v", got)
	}
}

// 校验出错要写清哪个键和现在的值；顶层那组（无人值守的兜底）不允许 0
func TestBudgetRangeErrorNamesKey(t *testing.T) {
	var cfg Budget
	err := yaml.Unmarshal([]byte("local:\n  maxTokens: -3\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "agent.budget.local.maxTokens") || !strings.Contains(err.Error(), "-3") {
		t.Fatalf("应该点名键和值：%v", err)
	}
	err = yaml.Unmarshal([]byte("maxRequests: 0"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "agent.budget.maxRequests") {
		t.Fatalf("无人值守那组不允许 0：%v", err)
	}
	// local 里的时限没有 60 分钟的上限了：不设就是不设，要设多久都行
	if err := yaml.Unmarshal([]byte("local:\n  maxDurationMs: 7200000\n"), &cfg); err != nil {
		t.Fatalf("本地时限不该再有上限：%v", err)
	}
}
