package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
)

// 窗口和单次输出的来源：服务商报的优先，models.dev 缓存只补空，用户覆盖盖过一切，恢复后回到原来的来源；
// 保存覆盖不联网、不重启，下一轮用的模型列表（currentModels）里就是新值
func TestModelLimitSourcesAndOverrides(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cache := `{"v":1,"fetchedAt":1000,"entries":[` +
		`{"p":"acme","id":"编的模型","c":100000,"o":9000},` +
		`{"p":"acme","id":"new","c":1000000,"o":64000}]}`
	if err := os.MkdirAll(filepath.Join(e.data, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.data, "cache", "models-dev.json"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	var gets atomic.Int32
	e.llm = modelTestServer(t, e, nil, nil, &gets)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := func(v api.ModelSettingsView, id string) api.ModelOption {
		t.Helper()
		for _, m := range v.Models {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("没有模型 %s", id)
		return api.ModelOption{}
	}
	m := byID(v, testModel)
	if m.ContextWindow != 32768 || m.WindowSource != "provider" || m.MaxOutput != 9000 || m.OutputSource != "modelsdev" {
		t.Fatalf("服务商报了窗口、输出由资料库补：%+v", m)
	}
	n := byID(v, "fake/new")
	if n.ContextWindow != 512000 || n.WindowSource != "provider" || n.DevWindow != 1000000 || n.MaxOutput != 64000 || n.OutputSource != "modelsdev" {
		t.Fatalf("服务商的窗口不能被资料库盖掉：%+v", n)
	}
	if !v.ModelsDev.Enabled || v.ModelsDev.Entries != 2 || v.ModelsDev.FetchedAt.IsZero() || !v.ModelsDev.FromCache {
		t.Fatalf("资料库状态：%+v", v.ModelsDev)
	}
	// 本轮用的模型列表就是定好的值
	if got := a.currentModels().Models[0]; got.OutputLimit() != 9000 && a.currentModels().Models[1].OutputLimit() != 9000 {
		t.Fatalf("发布的模型列表没带上补的值：%+v", got.Limits)
	}
	// 覆盖输出和窗口
	v = modelSave(t, r, v, config.ModelChange{Field: config.ModelOutputOverride, Model: testModel, Int: new(4000)})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ModelContextOverride, Model: testModel, Int: new(16000)})
	m = byID(v, testModel)
	if m.ContextWindow != 16000 || m.WindowSource != "user" || m.UserWindow != 16000 || m.MaxOutput != 4000 || m.OutputSource != "user" || m.ProviderWindow != 32768 {
		t.Fatalf("覆盖后：%+v", m)
	}
	for _, rm := range a.currentModels().Models {
		if rm.ID == testModel && (rm.Window() != 16000 || rm.OutputLimit() != 4000) {
			t.Fatalf("下一轮用的列表要是新值：%+v", rm.Limits)
		}
	}
	if gets.Load() != 2 {
		t.Fatalf("保存覆盖不该联网重取模型列表：%d", gets.Load())
	}
	// 恢复：写 0，回到服务商和资料库的值，文件里不留 0
	v = modelSave(t, r, v, config.ModelChange{Field: config.ModelContextOverride, Model: testModel, Int: new(0)})
	v = modelSave(t, r, v, config.ModelChange{Field: config.ModelOutputOverride, Model: testModel, Int: new(0)})
	m = byID(v, testModel)
	if m.ContextWindow != 32768 || m.WindowSource != "provider" || m.MaxOutput != 9000 || m.OutputSource != "modelsdev" || m.UserWindow != 0 {
		t.Fatalf("恢复后：%+v", m)
	}
	// 刷新模型列表以后覆盖仍生效（刷新用保存的覆盖重算）
	v = modelSave(t, r, v, config.ModelChange{Field: config.ModelOutputOverride, Model: testModel, Int: new(5000)})
	v, err = r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m = byID(v, testModel); m.MaxOutput != 5000 || m.OutputSource != "user" {
		t.Fatalf("刷新后覆盖丢了：%+v", m)
	}
	// 拒绝：不存在的模型也能写覆盖（模型可能暂时不在列表里），但 ID 里的特殊字符、负数不行
	for _, ch := range []config.ModelChange{
		{Field: config.ModelOutputOverride, Model: "a:b", Int: new(1)},
		{Field: config.ModelOutputOverride, Model: testModel, Int: new(-1)},
	} {
		if _, err := r.SetModelSettings(context.Background(), ch, v.Version); err == nil {
			t.Fatalf("接受了 %+v", ch)
		}
	}
	_ = llm.SourceUser
}

// 本地私聊里发给模型的单次输出就是模型自己的上限（再被窗口余量夹），不再被固定的 4096 或 32768 压住；
// 这是 ADR-334 要改的行为：最终发出去的请求体里的 max_completion_tokens
func TestLocalRequestUsesModelOutputLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, models, want string
	}{
		{"模型报了输出上限", `{"id":"` + testModel + `","context_length":200000,"max_output_tokens":50000}`, `"max_completion_tokens":50000`},
		{"没报输出上限按 32000", `{"id":"` + testModel + `","context_length":200000}`, `"max_completion_tokens":32000`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			script := llmtest.Script{Models: []json.RawMessage{json.RawMessage(tc.models)}, Reply: llmtest.Text("好")}
			srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
			t.Cleanup(srv.Close)
			e.llm = srv
			e.configure("tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
			a := e.open()
			d := a.Desktop()
			t.Cleanup(func() { d.Close(); a.Close() })
			key := localKey("console:" + testChar)
			if err := d.Reader.SendLocal(context.Background(), key, "写个大文件", nil); err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			found := false
			for _, b := range e.bodies {
				if strings.Contains(b, tc.want) {
					found = true
				}
				if strings.Contains(b, `"max_completion_tokens":4096`) {
					t.Fatalf("单次输出被 4096 压住了：%s", b[:min(len(b), 300)])
				}
			}
			if !found {
				t.Fatalf("请求体里没有 %s（共 %d 个请求）", tc.want, len(e.bodies))
			}
		})
	}
}
