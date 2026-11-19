package linkexpand

import (
	"encoding/json"
	"os"
	"testing"
)

// goldenOptions 把旧的 bilibiliFormat 翻成模板设置：card 写 @卡片（B 站视频的内置默认已改成封面文字排版），cover-text 是 @封面文字
// ForwardVideo 照生产默认（expand.send.forwardVideo 默认开）：视频留在合并转发里；关着的排法另有测试（TestForwardVideoOffSplitsVideos）
func goldenOptions(format string) Options {
	o := Options{ForwardVideo: true}
	if format == "card" {
		o.Templates = map[string]string{"bilibili-video": "@卡片", "bilibili-mall": "@卡片"}
	}
	if format == "cover-text" {
		o.Templates = map[string]string{"bilibili-video": "@封面文字", "bilibili-mall": "@封面文字"}
	}
	return o
}

// 回归：各站点内置默认模板的输出，与模板化之前硬编码 Build 录下的基线逐字相同
// （testdata/build_golden.json 由重构前的 Build 录成；GOLDEN_UPDATE=1 只在“有意改版式”时重录，要写进提交说明）。
func TestBuiltinTemplatesMatchLegacyOutput(t *testing.T) {
	want := map[string]json.RawMessage{}
	raw, err := os.ReadFile("testdata/build_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]Draft{}
	for _, c := range goldenCases() {
		r := c.build(t)
		got[c.name] = Build(&r, "-71", goldenOptions(c.format), c.previous)
	}
	if os.Getenv("GOLDEN_UPDATE") != "" {
		b, _ := json.MarshalIndent(got, "", " ")
		if err = os.WriteFile("testdata/build_golden.json", b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if len(want) != len(got) {
		t.Fatalf("基线 %d 个样本，现在 %d 个", len(want), len(got))
	}
	for name, d := range got {
		have, _ := json.MarshalIndent(d, "", " ")
		var a, b any
		_ = json.Unmarshal(have, &a)
		_ = json.Unmarshal(want[name], &b)
		ha, _ := json.Marshal(a)
		hb, _ := json.Marshal(b)
		if string(ha) != string(hb) {
			t.Errorf("%s 的输出和重构前不一致：\n现在 %s\n基线 %s", name, ha, hb)
		}
	}
}
