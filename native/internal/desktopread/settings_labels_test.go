package desktopread

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"sumika-native/internal/config"
)

// 设置页每一项都有中文名和说明，表里也没有多余的：以后加了键忘了写名字会红，删了键忘了删表项也会红。
// 键表里的角色是动态的，所以造两个编的角色、一个编的账号去跑，再对照表。
func TestSettingLabelsCoverEveryKey(t *testing.T) {
	t.Parallel()
	c := config.Defaults()
	c.Characters = map[string]config.CharacterModels{"aoi": {Model: "m"}, "natsume": {Topic: "t"}}
	c.QQ.Accounts = []config.QQAccount{{ID: "90001", Character: "aoi"}}
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return c, nil }}
	result, err := reader.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, item := range result.Value.Items.Items {
		s := item.Value
		if s.Name == "" || s.Help == "" {
			t.Errorf("%s 没有中文名或说明：%q %q", s.Key, s.Name, s.Help)
		}
		used[labelKey(s.Key)] = true
		if n := utf8.RuneCountInString(s.Help); n > 26 {
			t.Errorf("%s 的说明 %d 个字，常驻只留一行：%q", s.Key, n, s.Help)
		}
		if strings.Contains(s.Name+s.Help, "ADR") || strings.ContainsAny(s.Help, "\n\r") {
			t.Errorf("%s 的名字或说明里有不该给主人看的东西：%q %q", s.Key, s.Name, s.Help)
		}
	}
	seen := map[string]bool{}
	for _, l := range settingLabels {
		if seen[l.key] {
			t.Errorf("表里重复：%s", l.key)
		}
		seen[l.key] = true
		if !used[l.key] {
			t.Errorf("表里有多余的项（没有哪个设置键用它）：%s", l.key)
		}
		if l.name == "" || l.help == "" {
			t.Errorf("表项 %s 缺名字或说明", l.key)
		}
	}
	// 角色那几项要写出是哪个角色，两个角色不会混成同一个名字
	names := map[string]string{}
	for _, item := range result.Value.Items.Items {
		if strings.HasPrefix(item.Value.Key, "characters.") {
			names[item.Value.Key] = item.Value.Name
		}
	}
	if names["characters.aoi.model"] != "角色 aoi：说话用的模型" || names["characters.natsume.model"] == names["characters.aoi.model"] {
		t.Fatalf("角色那几项的名字：%v", names)
	}
}

// 思考档位等取值要有中文叫法；叫法表里的取值都是配置里真能写的值
func TestSettingChoicesMatchConfigValues(t *testing.T) {
	t.Parallel()
	for _, level := range []config.Thinking{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if thinkingChoices[string(level)] == "" {
			t.Errorf("思考档位 %s 没有中文叫法", level)
		}
	}
	if len(thinkingChoices) != 7 {
		t.Errorf("思考档位叫法有多余的：%v", thinkingChoices)
	}
	l, _ := labelOf("tools.approvalMode")
	for _, v := range []string{"manual", "auto", "yolo"} {
		if l.choices[v] == "" {
			t.Errorf("审批方式 %s 没有中文叫法", v)
		}
	}
	// 读出来的默认值（英文原值）都在叫法表里，界面不会显示英文
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return config.Defaults(), nil }}
	result, _ := reader.ReadSettings(context.Background())
	for _, item := range result.Value.Items.Items {
		s := item.Value
		if len(s.Choices) == 0 {
			continue
		}
		var raws []string
		switch v := s.Value.(type) {
		case string:
			raws = []string{v}
		case []string:
			raws = v
		}
		for _, raw := range raws {
			if raw != "" && s.Choices[raw] == "" {
				t.Errorf("%s 的默认值 %q 没有中文叫法", s.Key, raw)
			}
		}
	}
}

// 每一项带叫法的设置，叫法表的取值要和配置里真认的取值逐个对上（两头都查）：
// config 加了新取值而这里没跟上，界面会悄悄露出英文原值，这个测试就红。
func TestSettingChoicesMatchConfigAcceptedValues(t *testing.T) {
	keysOf := func(key string) []string {
		l, ok := labelOf(key)
		if !ok || len(l.choices) == 0 {
			t.Fatalf("%s 没有取值叫法", key)
		}
		return slices.Sorted(maps.Keys(l.choices))
	}
	sorted := func(v []string) []string { return slices.Sorted(slices.Values(v)) }
	if got := keysOf("memory.write.places"); !slices.Equal(got, sorted(config.WritePlaceNames)) {
		t.Errorf("memory.write.places 叫法 %v 和配置认的 %v 对不上", got, config.WritePlaceNames)
	}
	if got := keysOf("memory.write.confirmKinds"); !slices.Equal(got, sorted(config.WriteKindNames)) {
		t.Errorf("memory.write.confirmKinds 叫法 %v 和配置认的 %v 对不上", got, config.WriteKindNames)
	}
	// catchUp、embed.backend 的取值集合在 config 里没有导出，用真的加载去验：叫法表里的每个取值都能加载；
	// 写别的值被拒。legacy 例外：配置要能读进来（否则旧配置启动就报“配置坏了”），由嵌入启动处报“已移除”
	loads := func(yml string) error {
		dir := t.TempDir()
		file := filepath.Join(dir, "sumika.yml")
		if err := os.WriteFile(file, []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SUMIKA_CONFIG", file)
		_, err := config.Load(dir)
		return err
	}
	// 反方向：配置认的取值叫法表里都得有（catchUp 的两个值在 config 里只有字面量，embed.backend 有导出常量）
	if got := keysOf("memory.write.catchUp"); !slices.Equal(got, []string{"cursor", "latest"}) {
		t.Errorf("memory.write.catchUp 叫法 %v 少了或多了配置认的 latest、cursor", got)
	}
	if got := keysOf("embed.backend"); !slices.Equal(got, []string{config.BackendGoModels}) {
		t.Errorf("embed.backend 叫法 %v 和配置认的 %s 对不上（legacy 已移除，不该再出现在界面上）", got, config.BackendGoModels)
	}
	if err := loads("embed:\n  backend: legacy\n"); err != nil {
		t.Errorf("legacy 配置要能读进来、交给嵌入启动处报原因，不能在这里拒：%v", err)
	}
	for _, v := range keysOf("memory.write.catchUp") {
		if err := loads("memory:\n  write:\n    catchUp: " + v + "\n"); err != nil {
			t.Errorf("memory.write.catchUp 叫法表里的 %q 配置不认：%v", v, err)
		}
	}
	if loads("memory:\n  write:\n    catchUp: bogus\n") == nil {
		t.Errorf("catchUp 现在连乱写的值都认，叫法表需要重新对账")
	}
	for _, v := range keysOf("embed.backend") {
		if err := loads("embed:\n  backend: " + v + "\n"); err != nil {
			t.Errorf("embed.backend 叫法表里的 %q 配置不认：%v", v, err)
		}
	}
	if loads("embed:\n  backend: bogus\n") == nil {
		t.Errorf("embed.backend 现在连乱写的值都认，叫法表需要重新对账")
	}
	// backup.dailyMedia 配置里不检查取值，出处是示例配置那行注释："none / stickers（…）/ all"
	example, err := os.ReadFile(filepath.Join("..", "..", "..", "sumika.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`dailyMedia:[^#\n]*#[^:\n]*：([^\n]*)`).FindSubmatch(example)
	if m == nil {
		t.Fatal("示例配置里找不到 dailyMedia 的取值说明")
	}
	want := regexp.MustCompile(`[a-z]+`).FindAllString(string(m[1]), -1)
	if got := keysOf("backup.dailyMedia"); !slices.Equal(got, sorted(want)) {
		t.Errorf("backup.dailyMedia 叫法 %v 和示例配置写的 %v 对不上", got, want)
	}
}
