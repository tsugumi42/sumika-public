package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

type modelCall struct {
	change  api.ModelSettingsChange
	version string
}
type modelFake struct {
	*demoModelSettings
	reads, refreshes int
	calls            []modelCall
	failure          error
	response         func(api.ModelSettingsView) api.ModelSettingsView
	gate             chan struct{}
}

func (f *modelFake) ModelSettings(ctx context.Context) (api.ModelSettingsView, error) {
	f.reads++
	return f.demoModelSettings.ModelSettings(ctx)
}
func (f *modelFake) RefreshModels(ctx context.Context) (api.ModelSettingsView, error) {
	f.refreshes++
	return f.demoModelSettings.ModelSettings(ctx)
}
func (f *modelFake) SetModelSettings(ctx context.Context, ch api.ModelSettingsChange, v string) (api.ModelSettingsView, error) {
	f.calls = append(f.calls, modelCall{ch, v})
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return api.ModelSettingsView{}, ctx.Err()
		}
	}
	if f.failure != nil {
		view, err := f.demoModelSettings.ModelSettings(ctx)
		if err != nil {
			return view, err
		}
		if f.response != nil {
			view = f.response(view)
		}
		return view, f.failure
	}
	return f.demoModelSettings.SetModelSettings(ctx, ch, v)
}

type modelHarness struct {
	a    *app
	fake *modelFake
	tt   *ui.Tester
	q    chan func()
}

func newModelHarness(t *testing.T) *modelHarness {
	t.Helper()
	f := &modelFake{demoModelSettings: newDemoModelSettings([]character{{ID: "test-a", Name: "编造角色"}})}
	h := &modelHarness{fake: f, q: make(chan func(), 32)}
	h.a = &app{realTab: tabModels, chars: []character{{ID: "test-a", Name: "编造角色"}}}
	h.a.models.writer = f
	h.a.postFn = func(fn func()) { h.q <- fn }
	h.tt = testerOf(2048, 1030, h.a.realSettingsView)
	h.wait(t)
	return h
}
func (h *modelHarness) wait(t *testing.T) {
	t.Helper()
	select {
	case fn := <-h.q:
		fn()
		h.tt.Frame()
	case <-time.After(uiWaitLimit):
		t.Fatal("后台没有返回")
	}
}
func (h *modelHarness) click(t *testing.T, label string) {
	t.Helper()
	if err := h.tt.Click(label); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
}
func (h *modelHarness) selectModel(t *testing.T) {
	t.Helper()
	h.click(t, "编造角色的说话模型")
	h.tt.Type("示例模型 17")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
}
func modelAssert(t *testing.T, id string, ok bool, h *modelHarness) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %v", id, h.tt.Texts())
	}
}

func TestModelPageOpenAndSearch(t *testing.T) {
	h := newModelHarness(t)
	h.tt.Frame()
	h.tt.Frame()
	modelAssert(t, "open-once", h.fake.reads == 1 && h.tt.HasText("46 个"), h)
	modelAssert(t, "status-settled", !h.tt.FramePending(), h)
	h.click(t, "编造角色的说话模型")
	modelAssert(t, "open-chooser", h.tt.HasText("搜索模型") && h.tt.HasText("示例服务商 1") && h.tt.HasText("128K 上下文"), h)
	h.tt.Type("示例模型 17")
	h.tt.Frame()
	modelAssert(t, "search", h.tt.HasText("示例模型 17") && !h.tt.HasText("示例模型 01"), h)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	call := h.fake.calls[0]
	modelAssert(t, "save-target", call.change.Field == config.CharacterModel && call.change.Character == "test-a" && call.change.String != nil && *call.change.String == "demo/model-17" && call.version != "" && h.tt.HasText("示例模型 17") && !h.tt.HasText("正在保存"), h)
	firstVersion := call.version
	h.selectModel(t)
	h.wait(t)
	modelAssert(t, "save-version", h.fake.calls[1].version != firstVersion, h)
}
func TestModelPageSaveStates(t *testing.T) {
	for _, kind := range []string{"failed", "conflict", "saved"} {
		t.Run(kind, func(t *testing.T) {
			h := newModelHarness(t)
			h.fake.gate = make(chan struct{})
			want := "磁盘写不进去"
			h.fake.failure = errors.New(want)
			switch kind {
			case "conflict":
				h.fake.failure = api.ErrModelSettingsVersion
				want = "配置文件在外面改过，已重新读取"
			case "saved":
				h.fake.failure = &api.SavedModelSettingsError{Reasons: []string{"编造角色：总结启动失败"}}
				want = "已保存，部分没生效"
			}
			if kind != "failed" {
				h.fake.response = func(v api.ModelSettingsView) api.ModelSettingsView {
					v.Version = "external-version"
					x := v.Configured.Characters["test-a"]
					x.Model = "demo/model-23"
					v.Configured.Characters["test-a"] = x
					v.Characters[0].ConfiguredModel = x.Model
					return v
				}
			}
			h.selectModel(t)
			h.advance(350 * time.Millisecond) // 保存的提示过了阈值才写（loadstate）
			modelAssert(t, "saving-"+kind, h.tt.HasText("正在保存"), h)
			close(h.fake.gate)
			h.wait(t)
			if kind == "failed" {
				modelAssert(t, "rollback", h.tt.HasText(want) && h.tt.HasText("示例模型 00") && !h.tt.HasText("示例模型 17"), h)
			} else {
				modelAssert(t, kind, h.tt.HasText(want) && h.tt.HasText("示例模型 23") && h.a.models.view.Version == "external-version" && h.fake.reads == 1, h)
			}
		})
	}
}
func TestModelPageArchive(t *testing.T) {
	h := newModelHarness(t)
	h.a.models.view.ModelsEditable = false
	h.a.models.view.ModelsDisabledReason = "开始接收以后才能选模型"
	h.tt.Frame()
	h.click(t, "编造角色的说话模型")
	modelAssert(t, "archive-model", !h.a.models.chooser.Open && h.tt.HasText("开始接收以后才能选模型"), h)
	h.click(t, "本地思考")
	h.click(t, "高")
	h.wait(t)
	modelAssert(t, "archive-thinking", len(h.fake.calls) == 1 && h.fake.calls[0].change.Field == config.ThinkingLocal && *h.fake.calls[0].change.String == "high", h)
}
func TestModelPageNumbers(t *testing.T) {
	for _, tc := range []struct {
		label, text string
		field       config.ModelField
		valid       bool
	}{
		{"上下文长度上限", "0", config.ContextWindow, true},
		{"上下文长度上限", "999", config.ContextWindow, false},
		{"留给回复和工具往返", "0", config.ContextReserve, true},
		{"压缩后保留的原文", "999", config.ContextKeepTokens, false},
		{"压缩后保留的原文", "20001", config.ContextKeepTokens, true},
		{"开始压缩的比例", "0.765", config.ContextSoftRatio, true},
		{"开始压缩的比例", "0.99", config.ContextSoftRatio, false},
		{"强制压缩的比例", "0.91", config.ContextHardRatio, true},
		{"强制压缩的比例", "0.1", config.ContextHardRatio, false},
	} {
		t.Run(fmt.Sprint(tc.field, "/", tc.text), func(t *testing.T) {
			h := newModelHarness(t)
			h.tt.SetSize(2048, 2400)
			h.tt.Frame()
			h.click(t, tc.label)
			h.click(t, "编辑数值")
			h.tt.Key(ui.Cmd, ui.KeyA)
			h.tt.Type(tc.text)
			h.tt.Key(0, ui.KeyEnter)
			h.tt.Frame()
			if !tc.valid {
				modelAssert(t, "number-invalid", h.a.models.numberBox.Open && h.a.models.numberError != "" && len(h.fake.calls) == 0, h)
				return
			}
			h.wait(t)
			call := h.fake.calls[0].change
			value := ""
			if call.Int != nil {
				value = fmt.Sprint(*call.Int)
			} else if call.Float != nil {
				value = fmt.Sprint(*call.Float)
			}
			modelAssert(t, "number-save", call.Field == tc.field && value == tc.text && h.tt.HasText(tc.text), h)
		})
	}
}
func TestModelPageInheritanceAndToggle(t *testing.T) {
	h := newModelHarness(t)
	h.click(t, "编造角色的后台模型")
	// 在弹出框里搜着选，不按文字坐标点：页面那一行右边也写着"跟全局"，按坐标会点到页面上
	h.tt.Type("跟全局")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	modelAssert(t, "inherit", h.fake.calls[0].change.Field == config.CharacterBackground && *h.fake.calls[0].change.String == "", h)
	h.tt.SetSize(2048, 2400)
	h.tt.Frame()
	h.click(t, "本地闲聊自动压缩")
	h.wait(t)
	modelAssert(t, "toggle", h.fake.calls[1].change.Field == config.ContextAutoCompact && h.fake.calls[1].change.Bool != nil && !*h.fake.calls[1].change.Bool, h)
}
func TestModelPageRefreshAndMissing(t *testing.T) {
	h := newModelHarness(t)
	h.fake.models = h.fake.models[1:]
	h.click(t, "模型列表")
	h.wait(t)
	modelAssert(t, "refresh-missing", h.fake.refreshes == 1 && h.tt.HasText("列表里没有了") && h.tt.HasText("demo/model-00"), h)
	h.a.models.view.ModelsError = "编造的网关不可用"
	h.tt.Frame()
	modelAssert(t, "catalog-error", h.tt.HasText("编造的网关不可用") && h.tt.HasText("用的是 12:00 的缓存"), h)
	// 上下文长度上限是 0（跟着模型）时显示"跟模型"，不露裸 0
	modelAssert(t, "window-follows-model", h.a.models.view.Configured.Context.Window == 0 && h.tt.HasText("跟模型"), h)
}
func TestModelPageRuntimeAndStale(t *testing.T) {
	h := newModelHarness(t)
	v := h.a.models.view
	v.Active = []api.ModelUse{{Character: "test-a", Model: "demo/old"}}
	v.RunningReason = "这一轮还没结束，下一轮换上"
	h.a.models.accept(v)
	h.tt.Frame()
	modelAssert(t, "active-reason", h.tt.HasText("这一轮还没结束"), h)
	h.fake.gate = make(chan struct{})
	h.selectModel(t)
	h.a.srcGen++
	h.a.models = modelPageState{entered: true, loadError: "新的数据源"}
	close(h.fake.gate)
	h.wait(t)
	modelAssert(t, "stale-result", !h.a.models.loaded && h.tt.HasText("新的数据源"), h)
}
func TestModelPageReopenAndDemo(t *testing.T) {
	h := newModelHarness(t)
	h.a.realTab = tabLook
	h.tt.Frame()
	h.a.realTab = tabModels
	h.tt.Frame()
	h.wait(t)
	modelAssert(t, "reopen", h.fake.reads == 2, h)
	h.a.demoMode = true
	h.a.set = newSettings(h.a.chars)
	h.a.set.tab = tabModels
	h.a.models = modelPageState{}
	h.tt = testerOf(2048, 1030, h.a.settingsView)
	h.wait(t)
	h.selectModel(t)
	h.wait(t)
	modelAssert(t, "demo", strings.Contains(h.a.models.view.Configured.Characters["test-a"].Model, "model-17") && h.tt.HasText("示例模型 17"), h)
}
