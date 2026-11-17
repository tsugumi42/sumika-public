package tokenspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一套最小但完整的令牌：亮暗两个模式、两个中性色、一个尺寸、一个对比度计算。
func baseFiles() map[string]string {
	return map[string]string{
		"resolver.json": `{
  "version": "2025-10-01",
  "sets": {
    "primitive": {"sources": [{"$ref": "p.json"}]},
    "semantic": {"sources": [{"$ref": "s.json"}]},
    "component": {"sources": [{"$ref": "c.json"}]}
  },
  "modifiers": {
    "appearance": {"default": "light", "contexts": {"light": [], "dark": [{"$ref": "dark.json"}]}}
  },
  "resolutionOrder": [
    {"$ref": "#/sets/primitive"}, {"$ref": "#/modifiers/appearance"}, {"$ref": "#/sets/semantic"}, {"$ref": "#/sets/component"}
  ]
}`,
		"p.json": `{
  "neutral": {"$type": "number", "hue": {"$value": 250}, "chroma": {"$value": 0.008}},
  "away": {"$type": "number", "$value": -1},
  "ramp": {"$type": "color",
    "text": {"$value": {"colorSpace": "srgb", "components": [0, 0, 0], "hex": "#000000"}, "$extensions": {"sumika": {"neutral": {"l": 0.25, "k": 2.2}}}},
    "bg": {"$value": {"colorSpace": "srgb", "components": [0, 0, 0], "hex": "#000000"}, "$extensions": {"sumika": {"neutral": {"l": 0.955, "k": 0.9}}}}
  },
  "red": {"$type": "color", "$value": {"colorSpace": "oklch", "components": [0.57, 0.15, 30]}},
  "scale": {"$type": "dimension",
    "8": {"$value": {"value": 8, "unit": "px"}, "$extensions": {"sumika": {"axis": "space"}}},
    "4": {"$value": {"value": 4, "unit": "px"}, "$extensions": {"sumika": {"axis": "fixed"}}}
  }
}`,
		"dark.json": `{
  "away": {"$type": "number", "$value": 1},
  "ramp": {"$type": "color",
    "text": {"$value": {"colorSpace": "srgb", "components": [0, 0, 0], "hex": "#000000"}, "$extensions": {"sumika": {"neutral": {"l": 0.94, "k": 0.8}}}},
    "bg": {"$value": {"colorSpace": "srgb", "components": [0, 0, 0], "hex": "#000000"}, "$extensions": {"sumika": {"neutral": {"l": 0.22, "k": 1}}}}
  }
}`,
		"s.json": `{
  "color": {"$type": "color",
    "text": {"$value": "{ramp.text}"},
    "bg": {"$value": "{ramp.bg}"},
    "danger": {"$value": {"colorSpace": "srgb", "components": [0, 0, 0], "hex": "#000000"},
      "$extensions": {"sumika": {"reach": {"base": "{red}", "min": 4.5, "away": "{away}", "against": ["{color.bg}"]}}}}
  },
  "space": {"$type": "dimension", "s2": {"$value": "{scale.8}"}},
  "radius": {"$type": "dimension", "inner-min": {"$value": "{scale.4}"}}
}`,
		"c.json": `{}`,
	}
}

func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// build 先 -fix 把占位的 $value 换成算出来的值（和真流程一样），再读一遍、Build。
func build(t *testing.T, files map[string]string) error {
	t.Helper()
	dir := writeDir(t, files)
	s, err := Load(dir)
	if err != nil {
		return err
	}
	if _, err := s.Fix(); err != nil {
		return err
	}
	if s, err = Load(dir); err != nil {
		return err
	}
	m, err := s.Build()
	if err != nil {
		return err
	}
	_, err = m.Go()
	return err
}

func TestBaseBuilds(t *testing.T) {
	if err := build(t, baseFiles()); err != nil {
		t.Fatalf("最小令牌应该能生成：%v", err)
	}
}

func TestBadInputs(t *testing.T) {
	cases := []struct {
		name, want string
		edit       func(f map[string]string)
	}{
		{"引用不存在的令牌", "不存在的令牌", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"{ramp.text}"`, `"{ramp.nope}"`, 1)
		}},
		{"循环引用", "循环引用", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"text": {"$value": "{ramp.text}"}`, `"text": {"$value": "{color.x}"}, "x": {"$value": "{color.text}"}`, 1)
		}},
		{"生成的名字撞车", "撞车", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"s2": {"$value": "{scale.8}"}`, `"s2": {"$value": "{scale.8}"}, "s-2": {"$value": "{scale.8}"}`, 1)
		}},
		{"别名类型不符", "别名指向", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"s2": {"$value": "{scale.8}"}`, `"s2": {"$value": "{ramp.text}"}`, 1)
		}},
		{"组件层写字面量", "不能写字面量", func(f map[string]string) {
			f["c.json"] = `{"bubble": {"$type": "dimension", "pad": {"$value": {"value": 7, "unit": "px"}, "$extensions": {"sumika": {"axis": "fixed", "owner": "chatui"}}}}}`
		}},
		{"组件层缺 owner", "owner", func(f map[string]string) {
			f["c.json"] = `{"bubble": {"$type": "dimension", "pad": {"$value": "{space.s2}"}}}`
		}},
		{"组件引用别的组件", "不能引用别的组件", func(f map[string]string) {
			f["c.json"] = `{"$type": "dimension",
  "a": {"$value": "{space.s2}", "$extensions": {"sumika": {"owner": "chatui"}}},
  "b": {"$value": "{a}", "$extensions": {"sumika": {"owner": "winshell"}}}}`
		}},
		{"组件直接用原料", "只能引用语义层", func(f map[string]string) {
			f["c.json"] = `{"a": {"$type": "dimension", "$value": "{scale.8}", "$extensions": {"sumika": {"owner": "chatui"}}}}`
		}},
		{"default 不在 contexts 里", "default", func(f map[string]string) {
			f["resolver.json"] = strings.Replace(f["resolver.json"], `"default": "light"`, `"default": "dim"`, 1)
		}},
		{"contexts 少于两个", "至少要有两个", func(f map[string]string) {
			f["resolver.json"] = strings.Replace(f["resolver.json"], `"light": [], "dark": [{"$ref": "dark.json"}]`, `"light": []`, 1)
		}},
		{"resolutionOrder 引用不存在的 set", "不存在的 set", func(f map[string]string) {
			f["resolver.json"] = strings.Replace(f["resolver.json"], `"component": {"sources"`, `"comp": {"sources"`, 1)
		}},
		{"扩展里不认识的键", "不认识的键", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `{"sumika": {"reach"`, `{"sumika": {"fallback": 1, "reach"`, 1)
		}},
		{"计算参数里不认识的键", "reach 里不认识的键", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"min": 4.5`, `"min": 4.5, "tolerance": 1`, 1)
		}},
		{"对比度解不出来", "解不出来", func(f map[string]string) {
			f["s.json"] = strings.Replace(f["s.json"], `"min": 4.5`, `"min": 30`, 1)
		}},
		{"modifier 改语义层", "modifier 只能覆盖", func(f map[string]string) {
			f["dark.json"] = `{"color": {"$type": "color", "text": {"$value": {"colorSpace": "oklch", "components": [0.9, 0, 0]}}}}`
		}},
		{"原料引用语义层", "原料令牌", func(f map[string]string) {
			f["p.json"] = strings.Replace(f["p.json"], `"red": {"$type": "color", "$value": {"colorSpace": "oklch", "components": [0.57, 0.15, 30]}}`, `"red": {"$type": "color", "$value": "{color.text}"}`, 1)
		}},
		{"不认识的 $type", "不认识的 $type", func(f map[string]string) {
			f["p.json"] = strings.Replace(f["p.json"], `"$type": "number"`, `"$type": "percent"`, 1)
		}},
		{"尺寸缺 axis", "axis", func(f map[string]string) {
			f["p.json"] = strings.Replace(f["p.json"], `"$extensions": {"sumika": {"axis": "space"}}`, `"$description": "缺轴"`, 1)
		}},
		{"尺寸按模式变", "不分模式", func(f map[string]string) {
			f["dark.json"] = strings.Replace(f["dark.json"], `"ramp"`, `"scale": {"$type": "dimension", "8": {"$value": {"value": 9, "unit": "px"}, "$extensions": {"sumika": {"axis": "space"}}}}, "ramp"`, 1)
		}},
		{"JSON 里重复的键", "出现两次", func(f map[string]string) {
			f["c.json"] = `{"a": {}, "a": {}}`
		}},
		{"组名不合法", "只能用小写字母", func(f map[string]string) {
			f["c.json"] = `{"Bubble": {}}`
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := baseFiles()
			c.edit(f)
			err := build(t, f)
			if err == nil {
				t.Fatalf("应该报错（含 %q），却生成成功了", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("报错不对：要含 %q，实际 %v", c.want, err)
			}
		})
	}
}

// $value 被手改、和算出来的不一致时，不带 -fix 的生成要报错。
func TestValueDrift(t *testing.T) {
	dir := writeDir(t, baseFiles())
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Build(); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("占位的 $value 没被发现：%v", err)
	}
}

// -fix 写回的文件再读一遍，结果不变（写法稳定，不会每次都改出 diff）。
func TestFixIsStable(t *testing.T) {
	dir := writeDir(t, baseFiles())
	s, _ := Load(dir)
	if _, err := s.Fix(); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(dir)
	changed, err := s.Fix()
	if err != nil || len(changed) != 0 {
		t.Fatalf("第二次 -fix 不该再改文件：%v %v", changed, err)
	}
}
