package main

// 字体选择：列出这台电脑上装了的常用中文字体。mygo 的 ui.FontFamilies 只给名字和是否等宽，
// 不说字体有没有汉字，所以不把系统全部字体都放进来（Webdings 之类选了会变方块），
// 而是拿一份常见中文字体的候选名单，只留本机装了的（家族名不分大小写精确匹配）。
import (
	"strings"
	"sync"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
)

type fontChoice struct{ Label, Family string }

// cjkCandidates 的 families 里取第一个装了的名字。
var cjkCandidates = []struct {
	label    string
	families []string
}{
	{"微软雅黑", []string{"Microsoft YaHei UI", "Microsoft YaHei"}},
	{"等线", []string{"DengXian"}},
	{"思源黑体", []string{"Source Han Sans SC", "Noto Sans CJK SC", "Noto Sans SC"}},
	{"思源宋体", []string{"Source Han Serif SC", "Noto Serif CJK SC", "Noto Serif SC"}},
	{"HarmonyOS Sans", []string{"HarmonyOS Sans SC"}},
	{"MiSans", []string{"MiSans"}},
	{"更纱黑体", []string{"Sarasa UI SC"}},
	{"霞鹜文楷", []string{"LXGW WenKai", "LXGW WenKai Screen"}},
	{"资源圆体", []string{"Resource Han Rounded CN", "Resource Han Rounded SC"}},
	{"阿里巴巴普惠体", []string{"Alibaba PuHuiTi 3.0", "Alibaba PuHuiTi 2.0"}},
	{"苹方", []string{"PingFang SC"}},
	{"宋体", []string{"SimSun"}},
	{"黑体", []string{"SimHei"}},
	{"楷体", []string{"KaiTi"}},
}

// installedFamilies 取一次系统字体名单（mygo 首次要几十毫秒，之后缓存）；名单为空表示取不到，不据此判断"没装"。
var installedFamilies = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, f := range ui.FontFamilies() {
		out[strings.ToLower(f.Name)] = true
	}
	return out
})

// monoCandidates 是代码块、命令输出用的等宽字，取第一个装了的：Maple Mono（Bun 版和原型都用它，主人 2026-11-11 定继续用），
// NF 版带终端图标，代码和控制台共用；CN 版带中文。都没装时留空，chatui 退回系统的 monospace。
var monoCandidates = []string{"Maple Mono NF CN", "Maple Mono CN", "Maple Mono NF", "Maple Mono"}

// monoFamily 是这台电脑上要用的等宽字（取不到系统字体名单时直接用第一个候选，装没装交给字体回退）。
var monoFamily = sync.OnceValue(func() string {
	for _, f := range monoCandidates {
		if fontInstalled(f) {
			return f
		}
	}
	return ""
})

func fontInstalled(family string) bool {
	set := installedFamilies()
	return len(set) == 0 || set[strings.ToLower(family)]
}

// fontChoices 是下拉的全部选项：文渊圆体、系统字体、候选名单里本机有的；当前用的字体不在其中时也列出来，不悄悄丢。
func fontChoices(current string, installed func(string) bool) []fontChoice {
	out := []fontChoice{{"文渊圆体（默认）", bundledFamily}, {"系统字体", desksettings.SystemFont}}
	for _, c := range cjkCandidates {
		for _, name := range c.families {
			if installed(name) {
				out = append(out, fontChoice{c.label, name})
				break
			}
		}
	}
	for _, c := range out {
		if strings.EqualFold(c.Family, current) {
			return out
		}
	}
	return append(out, fontChoice{current + "（没找到）", current})
}

// fontNote 是字体那一行下面的提示；没有问题返回空。
func fontNote(current string, b bundledFont, installed func(string) bool) string {
	switch {
	case current == bundledFamily && b.Loaded && b.Err != nil:
		return "没找到文渊圆体的字体文件，现在用系统字体。放法见 native/README.md「界面字体」"
	case current != "" && current != bundledFamily && current != desksettings.SystemFont && !installed(current):
		return "这台电脑上没有字体“" + current + "”，现在用系统字体"
	}
	return ""
}

func (a *app) setFontFamily(family string) {
	a.changeLookBoth(func(d *theme.Definition) { d.Font = new(family) })
}

// fontRow 是设置页"字体"那一行；当前字体以主题为准，每次构建重新对一遍下标。
func (a *app) fontRow(c *ui.Context) {
	s := &a.look
	current := s.light.Typography().Font()
	if s.current != nil {
		current = s.current.Typography().Font()
	}
	choices := fontChoices(current, fontInstalled)
	labels := make([]string, len(choices))
	for i, ch := range choices {
		labels[i] = ch.Label
		if strings.EqualFold(ch.Family, current) {
			s.editor.fontLabel = ch.Label
		}
	}
	row(c, "字体", "", func() {
		choice.Dropdown(c, "字体", labels, &s.editor.fontLabel).OnChange(func() {
			for _, ch := range choices {
				if ch.Label == s.editor.fontLabel {
					a.setFontFamily(ch.Family)
					return
				}
			}
		})
	})
	ui.Box(c).Children(func() {
		if note := fontNote(current, s.bundled, fontInstalled); note != "" {
			kit.Muted(c, note).Wrap()
		}
	})
}
