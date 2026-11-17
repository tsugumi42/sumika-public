package main

// 演示只替换接口，配置补丁仍走同一套字段和校验；没有文件和网络副作用。
import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

type demoModelSettings struct {
	mu     sync.Mutex
	raw    []byte
	models []api.ModelOption
	chars  []character
}

func newDemoModelSettings(chars []character) *demoModelSettings {
	c := config.Defaults()
	c.Magpie.BaseURL = "http://demo.invalid"
	for _, ch := range chars {
		c.Characters[ch.ID] = config.CharacterModels{Model: "demo/model-00"}
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		panic(err)
	}
	d := &demoModelSettings{raw: raw, chars: chars}
	for i := range 46 {
		d.models = append(d.models, api.ModelOption{ID: fmt.Sprintf("demo/model-%02d", i), Name: fmt.Sprintf("示例模型 %02d", i), Provider: fmt.Sprintf("示例服务商 %d", i/10+1), Subtitle: "能看图 · 128K 上下文 · 思考：低/中/高", Input: []string{"text", "image"}, ContextWindow: 128000, ContextReported: true, VisionReported: true, ReasoningReported: true, ReasoningLevels: []string{"low", "medium", "high"},
			MaxOutput: 32000, WindowSource: "provider", OutputSource: "default", ProviderWindow: 128000})
		// 演示几种来源：第 3 个起输出由资料库补，第 7 个整个窗口未知，第 11 个两边说法不一样
		m := &d.models[i]
		switch {
		case i%11 == 3:
			m.ContextWindow, m.ProviderWindow, m.DevWindow, m.MaxOutput, m.ProviderOutput, m.DevOutput, m.OutputSource = 272000, 272000, 1050000, 128000, 128000, 128000, "provider"
		case i%7 == 3:
			m.ContextWindow, m.ContextReported, m.WindowSource, m.ProviderWindow = 128000, false, "default", 0
		case i%3 == 1:
			m.MaxOutput, m.OutputSource, m.DevOutput = 65536, "modelsdev", 65536
		}
	}
	return d
}
func (d *demoModelSettings) view() (api.ModelSettingsView, error) {
	c, err := config.Parse(d.raw, "demo.yml")
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	models := make([]api.ModelOption, len(d.models))
	copy(models, d.models)
	for i, o := range c.ModelMeta.Overrides {
		_ = i
		for j := range models {
			if models[j].ID != i {
				continue
			}
			if o.ContextWindow > 0 {
				models[j].ContextWindow, models[j].UserWindow, models[j].WindowSource, models[j].ContextReported = o.ContextWindow, o.ContextWindow, "user", true
			}
			if o.MaxOutputTokens > 0 {
				models[j].MaxOutput, models[j].UserOutput, models[j].OutputSource = o.MaxOutputTokens, o.MaxOutputTokens, "user"
			}
		}
	}
	v := api.ModelSettingsView{ModelsDev: api.ModelsDevState{Enabled: true, Entries: 8940, FetchedAt: time.Now().Add(-3 * time.Hour)}, Configured: config.ModelsOf(c), Running: config.ModelsOf(c), RunningKnown: true, Version: config.ContentVersion(d.raw), Models: models, ModelsEditable: true, ModelsReadAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local), RunningReason: "下一轮使用新的设置"}
	v.RunningVersion = v.Version
	for _, ch := range d.chars {
		m := c.Characters[ch.ID]
		v.Characters = append(v.Characters, api.ModelCharacterSettings{ID: ch.ID, ConfiguredModel: m.Model, ConfiguredBackground: m.Background, RunningModel: m.Model, RunningBackground: v.Configured.Background(ch.ID)})
	}
	for _, m := range models {
		if len(v.Groups) == 0 || v.Groups[len(v.Groups)-1].Provider != m.Provider {
			v.Groups = append(v.Groups, api.ModelGroup{Provider: m.Provider})
		}
		g := &v.Groups[len(v.Groups)-1]
		g.Models = append(g.Models, m)
	}
	v.ReadOnly = []api.ModelReadOnly{{Key: "magpie.baseUrl", Value: c.Magpie.BaseURL}}
	return v, nil
}
func (d *demoModelSettings) ModelSettings(ctx context.Context) (api.ModelSettingsView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.ModelSettingsView{}, err
	}
	return d.view()
}
func (d *demoModelSettings) RefreshModels(ctx context.Context) (api.ModelSettingsView, error) {
	return d.ModelSettings(ctx)
}
func (d *demoModelSettings) SetModelSettings(ctx context.Context, ch api.ModelSettingsChange, version string) (api.ModelSettingsView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.ModelSettingsView{}, err
	}
	if version != config.ContentVersion(d.raw) {
		v, e := d.view()
		if e != nil {
			return v, e
		}
		return v, api.ErrModelSettingsVersion
	}
	raw, _, err := config.PatchModel(d.raw, "demo.yml", ch)
	if err != nil {
		return api.ModelSettingsView{}, err
	}
	d.raw = raw
	return d.view()
}
