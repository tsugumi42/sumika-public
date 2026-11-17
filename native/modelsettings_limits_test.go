package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

func TestLimitValueFormatting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		m    api.ModelOption
		want string
	}{
		{api.ModelOption{ContextWindow: 128000, MaxOutput: 32000, WindowSource: "provider", OutputSource: "default"}, "上下文 128K · 输出 32K（未知）"},
		{api.ModelOption{ContextWindow: 1050000, MaxOutput: 128000, WindowSource: "modelsdev", OutputSource: "user"}, "上下文 1.05M（资料库） · 输出 128K（手填）"},
		{api.ModelOption{ContextWindow: 8192, MaxOutput: 900, WindowSource: "provider", OutputSource: "provider"}, "上下文 8.2K · 输出 900"},
	} {
		if got := modelLimitValue(tc.m); got != tc.want {
			t.Errorf("得到 %q，想要 %q", got, tc.want)
		}
	}
	m := api.ModelOption{ProviderWindow: 272000, DevWindow: 1050000, ProviderOutput: 128000, DevOutput: 128000}
	if got := modelLimitDisagree(m); got != "上下文：服务商 272K，资料库 1.05M" {
		t.Errorf("两边不一样：%q", got)
	}
	if modelLimitDisagree(api.ModelOption{ProviderWindow: 1, DevWindow: 1}) != "" || modelLimitDisagree(api.ModelOption{ProviderWindow: 1}) != "" {
		t.Error("一样或只有一边说，不提示")
	}
	now := time.Now()
	for _, tc := range []struct {
		s    api.ModelsDevState
		want string
	}{
		{api.ModelsDevState{}, "已关闭"},
		{api.ModelsDevState{Enabled: true}, "还没有缓存"},
		{api.ModelsDevState{Enabled: true, Error: "x"}, "没取到，也没有缓存"},
		{api.ModelsDevState{Enabled: true, FetchedAt: now.Add(-3 * time.Hour)}, "3 小时前更新"},
		{api.ModelsDevState{Enabled: true, FetchedAt: now.Add(-3 * time.Hour), Error: "x"}, "没联网，用的是缓存 · 3 小时前更新"},
	} {
		if got := modelsDevStatus(tc.s, now); got != tc.want {
			t.Errorf("得到 %q，想要 %q", got, tc.want)
		}
	}
}

// 页面上每个模型一行，写窗口、输出和来源；点一行改窗口、再恢复
func TestModelLimitsSectionOverrideAndReset(t *testing.T) {
	h := newModelHarness(t)
	h.tt.SetSize(2048, 6000)
	h.tt.Frame()
	modelAssert(t, "section", h.tt.HasText("模型的上下文和输出") && h.tt.HasText("模型资料库") && h.tt.HasText("3 小时前更新"), h)
	modelAssert(t, "unknown-tag", h.tt.HasText("输出 32K（未知）"), h)
	modelAssert(t, "devtag", h.tt.HasText("输出 65.5K（资料库）"), h)
	modelAssert(t, "disagree", h.tt.HasText("上下文：服务商 272K，资料库 1.05M"), h)
	h.click(t, "demo/model-00")
	modelAssert(t, "menu", h.tt.HasText("改上下文窗口") && h.tt.HasText("改单次输出上限") && !h.tt.HasText("恢复上下文窗口"), h)
	h.click(t, "改上下文窗口")
	h.click(t, "编辑数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("200000")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	ch := h.fake.calls[0].change
	modelAssert(t, "save", ch.Field == config.ModelContextOverride && ch.Model == "demo/model-00" && ch.Int != nil && *ch.Int == 200000 && h.tt.HasText("上下文 200K（手填）"), h)
	h.click(t, "demo/model-00")
	modelAssert(t, "menu-reset", h.tt.HasText("恢复上下文窗口") && !h.tt.HasText("恢复单次输出上限"), h)
	h.click(t, "恢复上下文窗口")
	h.wait(t)
	ch = h.fake.calls[1].change
	modelAssert(t, "reset", ch.Field == config.ModelContextOverride && ch.Model == "demo/model-00" && ch.Int != nil && *ch.Int == 0 && !h.tt.HasText("200K（手填）"), h)
}

// 窗口填得太小要拒绝，不发保存
func TestModelLimitsRejectsTinyWindow(t *testing.T) {
	h := newModelHarness(t)
	h.tt.SetSize(2048, 6000)
	h.tt.Frame()
	h.click(t, "demo/model-00")
	h.click(t, "改上下文窗口")
	h.click(t, "编辑数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("999")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	modelAssert(t, "tiny", h.a.models.numberBox.Open && h.a.models.numberError != "" && len(h.fake.calls) == 0, h)
}
