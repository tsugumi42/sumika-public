package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModelMetaDefaultsAndCheck(t *testing.T) {
	d := Defaults()
	if !d.ModelMeta.ModelsDev.Enabled || d.ModelMeta.ModelsDev.URL != "https://models.dev/api.json" || d.ModelMeta.ModelsDev.RefreshHours != 4 || len(d.ModelMeta.Overrides) != 0 {
		t.Fatalf("默认值：%+v", d.ModelMeta)
	}
	for name, m := range map[string]ModelMetaConfig{
		"网址不是 http": {ModelsDev: ModelsDevConfig{URL: "ftp://x/y"}},
		"网址没有主机":    {ModelsDev: ModelsDevConfig{URL: "https://"}},
		"刷新间隔为负":    {ModelsDev: ModelsDevConfig{RefreshHours: -1}},
		"覆盖窗口为负":    {Overrides: map[string]ModelOverride{"m": {ContextWindow: -1}}},
		"覆盖输出为负":    {Overrides: map[string]ModelOverride{"m": {MaxOutputTokens: -5}}},
		"模型 ID 带空白": {Overrides: map[string]ModelOverride{" m": {ContextWindow: 1}}},
	} {
		if len(m.check()) == 0 {
			t.Errorf("%s：应该报错", name)
		}
	}
	if bad := (ModelMetaConfig{Overrides: map[string]ModelOverride{"auto/deepseek-flash": {ContextWindow: 200000}, "x": {}}}).check(); len(bad) != 0 {
		t.Fatalf("正常的覆盖：%v", bad)
	}
}

func TestModelOverrideChangeTarget(t *testing.T) {
	n := func(v int) *int { return &v }
	path, value, _, err := ModelChange{Field: ModelContextOverride, Model: "auto/deepseek-v4.1-flash", Int: n(200000)}.Target()
	if err != nil || strings.Join(path, "|") != "modelMeta|overrides|auto/deepseek-v4.1-flash|contextWindow" || value != 200000 {
		t.Fatalf("目标：%v %v %v", path, value, err)
	}
	bad := map[string]ModelChange{
		"没有模型":      {Field: ModelContextOverride, Int: n(1)},
		"模型 ID 有冒号": {Field: ModelOutputOverride, Model: "a:b", Int: n(1)},
		"模型 ID 有换行": {Field: ModelOutputOverride, Model: "a\nb", Int: n(1)},
		"负数":        {Field: ModelOutputOverride, Model: "m", Int: n(-1)},
		"类型错":       {Field: ModelOutputOverride, Model: "m", String: new("1")},
		"别的字段带模型":   {Field: ContextWindow, Model: "m", Int: n(1)},
		"带角色":       {Field: ModelOutputOverride, Model: "m", Character: "c", Int: n(1)},
	}
	for name, ch := range bad {
		if _, _, _, err := ch.Target(); err == nil {
			t.Errorf("%s：应该拒绝", name)
		}
	}
}

// 在示例配置上加、改、去掉一个模型的覆盖：只动目标，不留 0；同一个模型的另一项不受影响
func TestModelOverrideSaveAndRemove(t *testing.T) {
	example, err := os.ReadFile("../../../sumika.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	example = bytes.ReplaceAll(example, []byte("\r\n"), []byte("\n"))
	for _, nl := range []string{"\n", "\r\n"} {
		raw := bytes.ReplaceAll(example, []byte("\n"), []byte(nl))
		dir := t.TempDir()
		file := filepath.Join(dir, "sumika.yml")
		history := filepath.Join(dir, "history")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		apply := func(ch ModelChange) Config {
			t.Helper()
			cur, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			c, err := SaveModel(context.Background(), file, history, ContentVersion(cur), ch, time.Unix(1234+time.Now().UnixNano()%1000, 0))
			if err != nil {
				t.Fatalf("%+v：%v", ch, err)
			}
			return c
		}
		n := func(v int) *int { return &v }
		c := apply(ModelChange{Field: ModelContextOverride, Model: "auto/deepseek-flash", Int: n(200000)})
		if c.ModelMeta.Overrides["auto/deepseek-flash"] != (ModelOverride{ContextWindow: 200000}) {
			t.Fatalf("加覆盖：%+v", c.ModelMeta.Overrides)
		}
		c = apply(ModelChange{Field: ModelOutputOverride, Model: "auto/deepseek-flash", Int: n(64000)})
		c = apply(ModelChange{Field: ModelOutputOverride, Model: "other-model", Int: n(1000)})
		if got := c.ModelMeta.Overrides["auto/deepseek-flash"]; got != (ModelOverride{ContextWindow: 200000, MaxOutputTokens: 64000}) {
			t.Fatalf("两项都在：%+v", got)
		}
		c = apply(ModelChange{Field: ModelContextOverride, Model: "auto/deepseek-flash", Int: n(0)})
		if got := c.ModelMeta.Overrides["auto/deepseek-flash"]; got != (ModelOverride{MaxOutputTokens: 64000}) {
			t.Fatalf("去掉窗口覆盖后输出覆盖还在：%+v", got)
		}
		c = apply(ModelChange{Field: ModelOutputOverride, Model: "auto/deepseek-flash", Int: n(0)})
		if got := c.ModelMeta.Overrides["auto/deepseek-flash"]; got != (ModelOverride{}) {
			t.Fatalf("全去掉：%+v", got)
		}
		if c.ModelMeta.Overrides["other-model"].MaxOutputTokens != 1000 {
			t.Fatalf("别的模型的覆盖不能受影响：%+v", c.ModelMeta.Overrides)
		}
		final, _ := os.ReadFile(file)
		if strings.Contains(string(final), ": 0\n") && strings.Contains(string(final), "contextWindow: 0") {
			t.Fatalf("不该留 0：%s", final)
		}
		// 值进了模型页的字段副本，克隆互不影响
		v := ModelsOf(c)
		cl := v.Clone()
		cl.Overrides["other-model"] = ModelOverride{}
		if v.Overrides["other-model"].MaxOutputTokens != 1000 {
			t.Fatal("Clone 要深拷贝覆盖表")
		}
	}
}
