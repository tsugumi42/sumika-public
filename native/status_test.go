package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/desktopread"
)

// realSettingsOf 用编的配置读出一份设置快照（不碰真实的 data/ 和配置文件）
func realSettingsOf(t *testing.T, edit func(c *config.Config)) api.Result[api.Settings] {
	t.Helper()
	c := config.Defaults()
	if edit != nil {
		edit(&c)
	}
	r, err := desktopread.SettingsReader{Load: func(context.Context) (config.Config, error) { return c, nil }}.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func settingsApp(r api.Result[api.Settings], tab int) *app {
	return &app{real: r, realTab: tab}
}

// 没接上的项写"还没启用"，配置里写了的值放在下面；列表用顿号、空的写"没填"
func TestRealSettingsUnsupportedAndEmpty(t *testing.T) {
	r := realSettingsOf(t, func(c *config.Config) {
		c.Source = "x.yml"
		c.Present = map[string]bool{"memory.topK": true}
		c.Memory.TopK = 12
		c.Memory.Write.Places = []string{"owner-private", "platform-group"}
	})
	tt := testerOf(1300, 2400, func(c *ui.Context) { settingsApp(r, tabMemory).realSettingsView(c) }) // 记忆和学习页
	for _, want := range []string{"每次回话最多想起几条", "还没启用", "配置里写了：12 条", "你自己的 QQ 私聊、QQ 群", "没填", "Sumika 自带的本地模型"} {
		if !tt.HasText(want) {
			t.Errorf("记忆页缺 %q：%q", want, tt.Texts())
		}
	}
	// embed.backend 以前落在"数据"页
	if tt := testerOf(1300, 1400, func(c *ui.Context) { settingsApp(r, tabData).realSettingsView(c) }); tt.HasText("记忆检索用的向量后端") {
		t.Error("向量后端应该在记忆页，不在数据页")
	}
}

// 最长的中文名、说明和值在设置页里不折行、不撑破、不和右边的值重叠（设计自查第 8、11 条）。
// 造了很长的角色 id、模型名、路径，把每一页的每一行量一遍：名字和说明各占一行高，右边缘留在窗口里，名字说明和值不相交。
func TestRealSettingsFitWidth(t *testing.T) {
	longID := "a-very-long-character-identifier-for-stress"
	longModel := "provider-name/some-very-long-model-name-with-many-suffixes-v4.1-flash-fast-20261102"
	r := realSettingsOf(t, func(c *config.Config) {
		c.Source = `D:\一个很深很深的目录\再深一层的目录\又一层\Sumika\data\config\sumika.yml`
		c.Present = map[string]bool{"tools.workdir": true, "tools.autoApproveDirs": true, "memory.topK": true, "vision.model": true}
		c.Characters = map[string]config.CharacterModels{longID: {Model: longModel, Topic: longModel, Background: longModel}}
		c.Defaults.BackgroundModel = longModel
		c.Vision.Model = longModel
		c.Memory.Rerank.Model = longModel
		c.Memory.Write.Places = []string{"owner-private", "local-private", "local-group", "platform-private", "platform-group"}
		c.Memory.Write.ConfirmKinds = []string{"fact", "preference", "promise", "boundary"}
		c.Tools.Workdir = `D:\Projects\一个很长很长的工作目录名字\子目录\再往下\更深`
		c.Tools.AutoApproveDirs = []string{`D:\work\notes\很长的笔记目录`, `D:\work\files\收件箱里的另一个目录`, `E:\data\解包数据`}
	})
	// 每个显示出来的值出现几次，只拿唯一的值去量相交
	count := map[string]int{}
	for _, it := range r.Value.Items.Items {
		if it.State == api.Ready {
			count[it.Value.Display(it.Value.Value)]++
		}
	}
	for _, w := range []int{2048, 1100, 900} {
		var rows, wrapped, overlap, outside int
		var widestName, widestHelp float32
		var base float32
		for tab := range settingsTabs {
			if tab == tabLook || tab == tabModels {
				continue // 外观和模型是编辑器，分别在对应交互测试里验收。
			}
			for _, detail := range []bool{false, true} {
				a := settingsApp(r, tab)
				a.realDetail = detail
				tt := testerOf(w, 4000, func(c *ui.Context) { a.realSettingsView(c) })
				for _, it := range r.Value.Items.Items {
					s := it.Value
					if settingPage(s.Key) != tab {
						continue
					}
					if slices.Contains(settingsTabs, s.Name) {
						continue // 和左边的分类同名，按文字找会找到分类按钮
					}
					rows++
					// 用户自己起的角色 id 可以很长：这几行允许名字折成两行，其余的名字和所有说明都得是一行
					lines := float32(1.4)
					if strings.HasPrefix(s.Key, "characters.") {
						lines = 2.4
					}
					name, ok := tt.Find(s.Name)
					if !ok {
						t.Errorf("%d 宽，%s 找不到：%q", w, s.Key, s.Name)
						continue
					}
					if base == 0 || name.H < base {
						base = name.H
					}
					if name.H > base*lines {
						wrapped++
						t.Errorf("%d 宽，名字折行了：%q 高 %.1f（一行 %.1f）", w, s.Name, name.H, base)
					}
					if name.X+name.W > float32(w) {
						outside++
						t.Errorf("%d 宽，名字撑出窗口：%q 右边缘 %.1f", w, s.Name, name.X+name.W)
					}
					if name.W > widestName {
						widestName = name.W
					}
					if help, ok := tt.Find(s.Help); ok {
						if help.H > base*1.4 {
							wrapped++
							t.Errorf("%d 宽，说明折行了：%q 高 %.1f（名字一行 %.1f）", w, s.Help, help.H, base)
						}
						if help.X+help.W > float32(w) {
							outside++
							t.Errorf("%d 宽，说明撑出窗口：%q 右边缘 %.1f", w, s.Help, help.X+help.W)
						}
						if help.W > widestHelp {
							widestHelp = help.W
						}
					}
					if it.State != api.Ready {
						continue
					}
					v := s.Display(s.Value)
					if count[v] != 1 {
						continue
					}
					val, ok := tt.Find(v)
					if !ok {
						t.Errorf("%d 宽，%s 的值找不到：%q", w, s.Key, v)
						continue
					}
					if val.X+val.W > float32(w) {
						outside++
						t.Errorf("%d 宽，值撑出窗口：%q 右边缘 %.1f", w, v, val.X+val.W)
					}
					// 同一行（纵向有重叠）的名字和值不能叠在一起
					sameRow := val.Y < name.Y+name.H && name.Y < val.Y+val.H
					if sameRow && name.X+name.W > val.X {
						overlap++
						t.Errorf("%d 宽，名字和值叠了：%q 右边缘 %.1f，值 %q 左边缘 %.1f", w, s.Name, name.X+name.W, v, val.X)
					}
				}
			}
		}
		t.Logf("%d 宽：量了 %d 行（含显示细节的一遍），折行 %d，撑出窗口 %d，相交 %d；名字栏最宽 %.1f，说明栏最宽 %.1f（量到的是栏宽），一行高 %.1f", w, rows, wrapped, outside, overlap, widestName, widestHelp, base)
	}
}
