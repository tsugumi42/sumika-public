package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/settingsreg"
)

func registryValue(t *testing.T, v api.SettingsView, key string) settingsreg.Value {
	t.Helper()
	for _, item := range v.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("missing setting %s", key)
	return settingsreg.Value{}
}

func TestSettingsRegistryEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var r api.SettingsRegistry = &d.Reader
	ctx := context.Background()
	v, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := a.cfg.QQ.Reply
	for _, ch := range []api.SettingChange{
		{Key: "qq.reply", Value: json.RawMessage("true")},
		{Key: "group.frequency", Value: json.RawMessage("0")},
		{Key: "agent.thinking.local", Value: json.RawMessage(`"high"`)},
		{Key: "linkReader.context.enabled", Value: json.RawMessage("false")},
		{Key: "desktop.autoReceive", Value: json.RawMessage("true")},
	} {
		v, err = r.WriteSetting(ctx, ch, v.Version)
		if err != nil {
			t.Fatalf("%s: %v", ch.Key, err)
		}
		got := registryValue(t, v, ch.Key)
		raw, _ := json.Marshal(got.Value)
		if string(raw) != string(ch.Value) {
			t.Fatalf("%s: got %s want %s", ch.Key, raw, ch.Value)
		}
		if v.Effect == "" {
			t.Fatal("missing effect")
		}
	}
	if a.cfg.QQ.Reply != before {
		t.Fatal("restart setting mutated active config")
	}
	if a.currentModels().Values.Local != "high" {
		t.Fatal("model runtime not published")
	}
	if a.currentLinks().Values.Context.Enabled {
		t.Fatal("link runtime not published")
	}
	if on, err := desksettings.New(e.data).AutoReceive(); err != nil || !on {
		t.Fatalf("desktop not saved: %v %v", on, err)
	}
	// 旧入口继续能改，新入口读到相同值，所有 yml 写入共享文件版本。
	if _, err := d.Reader.SetModelSettings(ctx, config.ModelChange{Field: config.ThinkingLocal, String: new("low")}, v.Version.Config); err != nil {
		t.Fatal(err)
	}
	v, err = r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(registryValue(t, v, "agent.thinking.local").Value)
	if string(b) != `"low"` {
		t.Fatal("old writer diverged")
	}
	persisted, err := config.LoadIn(e.root, e.data)
	if err != nil || !persisted.QQ.Reply || persisted.Group.Frequency != 0 {
		t.Fatalf("reopen %v", err)
	}
}

func TestSettingsRegistryRejectsUnsafeChanges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	ctx := context.Background()
	v, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(e.data, "config", "sumika.yml")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []api.SettingChange{
		{Key: "trace.debug", Value: json.RawMessage("true")},
		// 后台没有任何功能读这四个目录键（注册表标尚未生效）：以前能写，现在和别的没人读的键一样只读
		{Key: "qq.qqDir", Value: json.RawMessage(`"X:\\fixture\\qq"`)},
		{Key: "qq.qqDataDir", Value: json.RawMessage(`"X:\\fixture\\qqdata"`)},
		{Key: "qq.protocolDir", Value: json.RawMessage(`"X:\\fixture\\protocol"`)},
		{Key: "qq.protocolLogDir", Value: json.RawMessage(`"X:\\fixture\\protocol\\logs"`)},
		{Key: "memory.write.characters", Value: json.RawMessage(`["fixture"]`)},
		{Key: "qq.reply", Value: json.RawMessage("null")},
		{Key: "qq.reply", Value: json.RawMessage(`"true"`)},
		{Key: "group.frequency", Value: json.RawMessage("2")},
		{Key: "characters.unknown.model", Value: json.RawMessage(`"fake/model"`)},
		{Key: "linkReader.expand.conversations", Value: json.RawMessage(`["qq:group:90009"]`)},
		{Key: "linkReader.expand.conversations", Value: json.RawMessage(`["console:owner:fixture"]`)},
		{Key: "linkReader.expand.conversations", Value: json.RawMessage(`["console:group:fixture"]`)},
		{Key: "linkReader.expand.conversations", Value: json.RawMessage(`["qq:private:90001:90009"]`)},
		{Key: "unregistered.path", Value: json.RawMessage("false")},
	} {
		if _, err := r.WriteSetting(ctx, ch, v.Version); err == nil {
			t.Fatalf("accepted %s", ch.Key)
		}
		after, err := os.ReadFile(file)
		if err != nil || string(before) != string(after) {
			t.Fatalf("rejection changed file %s: %v", ch.Key, err)
		}
	}
	// 外部修改必须冲突；返回的新快照可供用户重新确认。
	if err := os.WriteFile(file, append(before, []byte("\n# external\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := r.WriteSetting(ctx, api.SettingChange{Key: "qq.reply", Value: json.RawMessage("true")}, v.Version)
	if !errors.Is(err, config.ErrConfigVersion) || fresh.Version.Config == v.Version.Config {
		t.Fatalf("conflict: %v", err)
	}
	d.Close()
	if _, err := r.WriteSetting(ctx, api.SettingChange{Key: "qq.reply", Value: json.RawMessage("true")}, fresh.Version); !errors.Is(err, api.ErrModelSettingsClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestSettingsRegistryReportsSavedWhenViewFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ReadSettingsRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 全局模型设置仍可保存；新增坏卡使保存后的完整视图失败。
	dir := filepath.Join(a.paths.CharactersDir(), "broken-fixture", "card")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "character.yml"), []byte("name: ["), 0600); err != nil {
		t.Fatal(err)
	}
	v, err = r.WriteSetting(context.Background(), api.SettingChange{Key: "agent.thinking.local", Value: json.RawMessage(`"high"`)}, v.Version)
	var savedModel *api.SavedModelSettingsError
	var savedView *api.SavedSettingsError
	if !errors.As(err, &savedModel) || !errors.As(err, &savedView) || v.Effect == "" {
		t.Fatalf("saved state lost: %v", err)
	}
	c, loadErr := config.LoadIn(e.root, e.data)
	if loadErr != nil || c.Agent.Thinking.Local != "high" || a.currentModels().Values.Local != "high" {
		t.Fatalf("save or publish failed: %v", loadErr)
	}
}

// 缺模型文件：设置页读到的状态里有模型名、正在找的完整路径、登记表里的官方下载页；嵌入关着时为空。
func TestSettingsRegistryReportsMissingEmbedModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "sumika-models.exe")
	if err := os.WriteFile(exe, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "models")
	guardFixtureKeys(t, e, true, "model: gte-multilingual-base\n  modelsRoot: "+root+"\n  modelsExe: "+exe)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	v, err := d.Reader.ReadSettingsRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "_待分类", "gte-multilingual-base", "model_quantized.onnx")
	m := v.EmbedMissingModel
	if m == nil || m.Model != "gte-multilingual-base" || m.Path != want || m.DownloadURL != "https://huggingface.co/Alibaba-NLP/gte-multilingual-base" {
		t.Fatalf("缺模型状态：%+v，要 model=gte-multilingual-base path=%s page=https://huggingface.co/Alibaba-NLP/gte-multilingual-base", m, want)
	}

	// 嵌入没开：没有缺模型这回事。
	e2 := newEnv(t)
	a2 := e2.open()
	defer a2.Close()
	d2 := a2.Desktop()
	defer d2.Close()
	v2, err := d2.Reader.ReadSettingsRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v2.EmbedMissingModel != nil {
		t.Fatalf("嵌入关着不该报缺模型：%+v", v2.EmbedMissingModel)
	}
}

// “指定模型目录”走设置页现有的写配置接口：embed.modelsRoot 写进去，重新读配置就是这个值。
func TestSettingsRegistryWritesEmbedModelsRoot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	ctx := context.Background()
	v, err := d.Reader.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "models")
	raw, err := json.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := d.Reader.WriteSetting(ctx, api.SettingChange{Key: "embed.modelsRoot", Value: raw}, v.Version)
	if err != nil {
		t.Fatalf("写 embed.modelsRoot：%v", err)
	}
	c, err := config.LoadIn(e.root, e.data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Embed.ModelsRoot != dir {
		t.Fatalf("配置读出来是 %q，要 %q", c.Embed.ModelsRoot, dir)
	}
	b, err := json.Marshal(registryValue(t, view, "embed.modelsRoot").Value)
	if err != nil || string(b) != string(raw) {
		t.Fatalf("写回的快照是 %s，要 %s（%v）", b, raw, err)
	}
}
