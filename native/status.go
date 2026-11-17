package main

// 生产的运行状态与设置视图（规格-界面接Go后台 3.2、4.6）：
//   - 后台状态（竖栏"运行"，托盘"查看后台状态"直达）：只看历史 / 接收中 / 受阻 / 正在退出，及原因和未恢复的提醒；
//   - 设置：照原型的分类（settings.go 的 settingsTabs），每项写中文名、一行说明、值（翻成中文、带单位）和来源（你的配置 / 默认值）；
//     配置键名、完整路径收在“显示配置键名和来源”里；外观独立编辑主题，数据位置写 sumika.root、重启生效。
// 排法照 settings.go 的 group / row（CrychicUI 的 layout.Section 和设置行）。

import (
	"fmt"
	"strings"

	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/ELY-Open-Source/CrychicUI/layout"
	"github.com/ELY-Open-Source/CrychicUI/navigation"
	"github.com/ELY-Open-Source/CrychicUI/typography"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/api"
	"sumika-native/internal/envcheck"
	"sumika-native/internal/loadui"
	"sumika-native/internal/winshell"
)

func (a *app) statusView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Padding(t.Space(6), t.Space(8)).Gap(t.Space(6)).MaxWidth(860).Children(func() {
		layout.PageHeader(c, "后台状态", layout.PageHeaderOptions{Subtitle: "关掉窗口后台照常运行；退出要用托盘菜单"})
		a.statusBody(c)
	})
}

// statusBody 是后台状态页的内容；运行页的"状态"标签直接放它（外壳已经有"运行"的标题），statusView 是单独成页的写法
func (a *app) statusBody(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(6)).Children(func() {
		if a.demoMode {
			group(c, "数据", "", func() { row(c, "演示数据", "全是编的，不碰任何库", func() {}) })
			return
		}
		h, rt := a.host, a.runtime
		group(c, "现在", "", func() {
			row(c, phaseText(h.Phase), h.Reason, func() {})
			if h.Account != "" {
				row(c, "开始接收用的小号", h.Account, func() {})
			} else if h.NoRecv != "" {
				row(c, "不能开始接收", h.NoRecv, func() {})
			}
			if rt.Connection != "" {
				row(c, "QQ 连接", rt.Connection, func() {})
			}
			if h.LocalByBun { // 本地私聊交给了 Bun 子进程（backend.local: bun）：它在不在、为什么不在
				row(c, "本地私聊", h.LocalLine, func() {})
			}
		})
		if len(h.StoreUnavailable) > 0 {
			group(c, "平台库没打开", "", func() {
				for _, line := range h.StoreUnavailable {
					row(c, line, "", func() {})
				}
			})
		}
		a.dataLocationInfo(c)
		if len(rt.Alerts.Items) > 0 {
			group(c, "没恢复的提醒", "", func() {
				for _, al := range rt.Alerts.Items {
					row(c, al.Title, al.Body, func() {})
				}
			})
		}
		if len(a.chars) > 0 {
			group(c, "角色", "", func() {
				for _, ch := range a.chars {
					hint := ch.Status
					if ch.Err != "" {
						hint = "读不到：" + ch.Err
					} else if ch.Model == "" {
						hint += " · 还没选模型"
					} else {
						hint += " · " + ch.Model
					}
					row(c, ch.Name, hint, func() {})
				}
			})
		}
		if a.status != "" {
			typography.Caption(c, a.status).TextColor(t.Danger)
		}
		a.envGroup(c, h.Env)
	})
}

// noSettingsPage 是不属于任何一页的键：表示“这个键现在没有页面”。
const noSettingsPage = -1

// settingPage 每个键归哪一页（返回 tabXxx，下标含义见 settings.go）。没有页面的键返回 noSettingsPage；
// 认不出前缀的键落在“数据”页。
func settingPage(key string) int {
	// 职业、快捷键两页因为没有可改的内容已撤掉（待办 P0-6）；这两类键还在注册表里（标“尚未实现”），但不再放进任何一页，
	// 否则它们会掉进下面“认不出前缀”的兜底、冒到“数据”页。
	for _, p := range []string{"occupation", "shortcuts."} {
		if strings.HasPrefix(key, p) {
			return noSettingsPage
		}
	}
	for _, g := range []struct {
		tab      int
		prefixes []string
	}{
		{tabLook, []string{"appearance."}},
		{tabModels, []string{"magpie.", "defaults.", "agent.", "characters.", "models."}},
		{tabMemory, []string{"memory.", "expression.", "vision.", "embed."}},
		{tabTools, []string{"tools."}},
		{tabAccounts, []string{"qq.", "platform.", "plugins.", "reply."}},
		{tabNotify, []string{"notifications."}},
		{tabData, []string{"data.", "backup."}},
	} {
		for _, p := range g.prefixes {
			if strings.HasPrefix(key, p) {
				return g.tab
			}
		}
	}
	return tabData
}

func (a *app) realSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(240).Padding(t.Space(4), t.Space(3)).Background(theme.T(c).Materials().Sidebar().Background()).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			layout.PageHeader(c, "设置", layout.PageHeaderOptions{})
			navigation.VerticalTabs(c, "设置分类", settingsTabs, &a.realTab)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(t.Space(4), t.Space(8), t.Space(10), t.Space(8)).Gap(t.Space(4)).MaxWidth(860).Children(func() {
				layout.PageHeader(c, settingsTabs[a.realTab], layout.PageHeaderOptions{})
				a.settingsTabContent(c)
			})
		})
	})
}

// settingsTabContent 是真实设置页右边当前分类的内容（旧组件库画的，页标题不在里面）。
// 旧界面的设置页和新界面外壳里的设置页（外观以外的分类）共用它。
func (a *app) settingsTabContent(c *ui.Context) {
	t := c.Theme()
	if a.realTab != tabModels {
		a.models.leave()
	}
	if a.realTab != tabMemory {
		a.embedUI.asked = false // 离开记忆页再回来重读一次缺模型状态：修好模型或重启接收后不用重开窗口
	}
	if a.realTab != tabPlugins {
		a.linkSettings.leave()
		a.lifeSettings.leave()
	}
	if a.realTab == tabLook {
		a.pageLook(c)
		return
	}
	if a.realTab == tabModels {
		a.pageModelSettings(c)
		return
	}
	if a.realTab == tabPlugins {
		a.pageLinkSettings(c)
		return
	}
	if a.realTab == tabData {
		a.pageDataLocation(c)
		a.secretsGroup(c)
	}
	r := a.real
	switch {
	case r.State == "" || r.State == api.Loading:
		if !a.realLoad.Loading() {
			a.realLoad.Begin() // 页面第一次看到它没到，从这一帧起算（loadstate）
		}
		rv := a.realLoad.View(c.Now())
		loadui.Wake(c, rv)
		loadui.Line(rv, "正在读配置…", func(s string) ui.Element { return kit.Muted(c, s) })
		return
	case r.State != api.Ready:
		msg := "读不到设置"
		if r.Err != nil {
			msg += "：" + r.Err.Error()
		}
		typography.Caption(c, msg).TextColor(t.Danger)
		return
	}
	a.settingsLocalToggle(c, "setting-detail", "显示配置键名和来源", "对照配置文件时用", &a.realDetail)
	n := 0
	for _, it := range r.Value.Items.Items {
		if settingPage(it.Value.Key) != a.realTab {
			continue
		}
		n++
		if it.Value.Key == remoteApprovalKey && a.remoteApprovalRow(c, it) {
			continue
		}
		if isNotifyKey(it.Value.Key) && a.notifyRow(c, it) {
			continue
		}
		a.settingRow(c, it)
		if it.Value.Key == embedBackendKey {
			a.embedMissingBlock(c) // 缺模型时入口紧跟在嵌入后端那一行下面
		}
	}
	if n == 0 {
		kit.Muted(c, "这一页还没有接上的项")
	}
}

// settingRow 一项设置：左边中文名和一行说明，右边值和来源。值已经翻成中文（api.Setting.Display）；
// 没接上的项右边写"还没启用"，配置里写了的值放在下面一行小字。键名和完整路径只在打开"显示配置键名和来源"时出现。
func (a *app) settingRow(c *ui.Context, it api.Result[api.Setting]) {
	t := c.Theme()
	s := it.Value
	name := s.Name
	if name == "" {
		name = s.Key // 表里漏了的键（测试会拦）也不让标题空着
	}
	value, note, source := "", "", ""
	switch it.State {
	case api.Ready:
		value = s.Display(s.Value)
		source = s.SourceText()
		if s.Effective {
			if running := s.Display(s.EffectiveValue); running != value {
				note = "运行中：" + running
			}
		}
	case api.Unsupported:
		value = "还没启用"
		if s.Source == "runtime" {
			value = "还没接上"
		}
		if s.Configured {
			note = "配置里写了：" + s.Display(s.Value)
		}
	default:
		value = "读不到"
		if it.Err != nil {
			note = it.Err.Error()
		}
	}
	if k := a.settingsKit(); k != nil {
		a.settingRowKit(c, k, it, name, value, note, source)
		return
	}
	ui.Row(c).FillWidth().Gap(t.Space(4)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(t.Space(0.5)).Children(func() {
			ui.Text(c, name).FontWeight(500)
			if s.Help != "" {
				kit.Muted(c, s.Help)
			}
			if a.realDetail {
				detail := s.Key
				if path := s.SourcePath(); path != "" {
					detail += " · " + path
				}
				typography.Caption(c, detail).Selectable()
			}
		})
		ui.Column(c).MaxWidth(t.Space(50)).Gap(t.Space(0.5)).AlignItems(ui.End).Children(func() {
			v := ui.Text(c, value).Selectable()
			if it.State != api.Ready {
				v.TextColor(t.TextMuted)
			}
			if note != "" {
				typography.Caption(c, note)
			}
			switch path := s.SourcePath(); {
			case source != "" && path != "":
				// 完整路径不常驻，悬停在"你的配置"上才出现
				anchor := ui.Box(c).Children(func() { typography.Caption(c, source) })
				overlay.Tooltip(c, anchor, "写在这个配置文件里\n"+path, overlay.TooltipOptions{})
			case source != "":
				typography.Caption(c, source)
			}
		})
	})
}

// ───────────── 环境 ─────────────

// envGroup 是运行页“状态”里的“环境”一组（规格-环境自检 设计第 5 条）：只列不是正常的项，
// 下一步和影响折叠在各自那一行下面；全是正常时就一行。快照由宿主生成、随宿主状态带过来，这里只读。
func (a *app) envGroup(c *ui.Context, snap envcheck.Snapshot) {
	group(c, "环境", "命令行：sumika-ctl doctor", func() {
		if len(snap.Items) == 0 {
			row(c, "环境检查还没跑完", "", func() {})
			return
		}
		problems := 0
		for _, it := range snap.Items {
			if !envListed(it.State) {
				continue
			}
			problems++
			a.envItem(c, it)
		}
		if problems == 0 {
			row(c, fmt.Sprintf("环境检查都正常（%d 项）", len(snap.Items)), "", func() {})
		}
	})
}

// envListed 决定清单项在界面上出不出现：正常和不适用都不列（规格：不适用也不列）。
func envListed(s envcheck.State) bool {
	return s == envcheck.Bad || s == envcheck.Warn || s == envcheck.Unknown
}

// envStateName 是状态标记的字。
func envStateName(s envcheck.State) string {
	switch s {
	case envcheck.Bad:
		return "有问题"
	case envcheck.Warn:
		return "要注意"
	case envcheck.Unknown:
		return "不知道"
	}
	return ""
}

// envStateInk 用现有语义色：有问题用危险色，要注意用提醒色，不知道用弱一档。
func envStateInk(c *ui.Context, s envcheck.State) ui.Color {
	t := c.Theme()
	switch s {
	case envcheck.Bad:
		return t.Danger
	case envcheck.Warn:
		return t.Warning
	}
	return t.TextMuted
}

// envItem 是一项：一行状态标记、名字和详情（详情一行，放不下截断，悬停看全）；点开看下一步、影响和没找到的子项。
func (a *app) envItem(c *ui.Context, it envcheck.Item) {
	t := c.Theme()
	if a.envOpen == nil {
		a.envOpen = map[string]bool{}
	}
	open := a.envOpen[it.ID]
	subs := envSubProblems(it.Sub)
	more := it.Next != "" || it.Affects != "" || len(subs) > 0
	e := ui.ButtonBase(c.Key("env-"+it.ID)).Label(it.Name).FillWidth().MinHeight(t.Space(6)).Gap(t.Space(2)).AlignItems(ui.Center).Padding(t.Space(0.5), t.Space(2)).Radius(t.Space(1))
	if more {
		if e.Pressed() {
			e.Background(t.SurfacePressed)
		} else if e.Hovered() {
			e.Background(t.SurfaceHover)
		}
		e.OnClick(func() { a.envOpen[it.ID] = !open })
	}
	e.Children(func() {
		ui.Text(c.Key("state"), envStateName(it.State)).FontSize(t.Rem(0.85)).FixedLineHeight(t.Rem(1)).TextColor(envStateInk(c, it.State)).SingleLine().Shrink(0)
		ui.Text(c.Key("name"), it.Name).FontWeight(500).SingleLine().Shrink(0)
		ui.Text(c.Key("detail"), it.Detail).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0).Tooltip(it.Detail)
		if more {
			deg := float32(0)
			if open {
				deg = 90
			}
			ui.Icon(c.Key("chevron"), winshell.IconRight).Size(t.Rem(1), t.Rem(1)).Shrink(0).TextColor(t.TextMuted).Rotate(deg)
		}
	})
	layout.Collapse(c, open, func() {
		ui.Column(c.Key("more")).Gap(t.Space(1)).Padding(t.Space(1), 0, t.Space(1), t.Space(4)).Children(func() {
			if it.Next != "" {
				ui.Text(c, "下一步："+it.Next).Wrap()
			}
			if it.Affects != "" {
				ui.Text(c, "影响："+it.Affects).TextColor(t.TextMuted).Wrap()
			}
			for _, sub := range subs {
				ui.Row(c.Key("sub-" + sub.Name)).FillWidth().MinHeight(t.Space(5)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Text(c.Key("state"), envStateName(sub.State)).FontSize(t.Rem(0.85)).FixedLineHeight(t.Rem(1)).TextColor(envStateInk(c, sub.State)).Shrink(0)
					ui.Text(c.Key("name"), sub.Name).FontWeight(500).SingleLine().Shrink(0)
					ui.Text(c.Key("detail"), sub.Detail).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0).Tooltip(sub.Detail)
				})
			}
		})
	})
}

// envSubProblems 只留不是正常的子项：外部程序展开后不列找到的那些。
func envSubProblems(subs []envcheck.SubItem) []envcheck.SubItem {
	var out []envcheck.SubItem
	for _, sub := range subs {
		if envListed(sub.State) {
			out = append(out, sub)
		}
	}
	return out
}
