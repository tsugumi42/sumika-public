package main

// 设置各页（规格-原生界面原型 第二节第一版）：放什么照 docs/archive/notes/清单-设置项与缺的组件-2026-10-24.md。
// 模型页使用 MewTypeUI settings（modelsettings.go），其他演示页用 CrychicUI，排法照它的 form.SettingsForm：每组 layout.Section，
// 开关用 choice.ToggleRow，选择用"左边名字 + 右边 choice.Dropdown / choice.Segments / input.NumberField"的行（SettingsForm 只认
// 开关和下拉两种行，所以这一行照它的排法自己写，见 row）；要填的（带单位的数、文字、时间）用表单字段（input.UnitField、
// input.TextField、datetime.TimePicker，名字在框上面）。分类用 navigation.VerticalTabs；换页淡出淡入（ui.go 的 fade）。
// 演示的后台设置只在窗口内；外观与正式窗口共用 pageLook，正式窗口由宿主接上持久化。

import (
	"time"

	"github.com/ELY-Open-Source/CrychicUI/button"
	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/datetime"
	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/ELY-Open-Source/CrychicUI/input"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/ELY-Open-Source/CrychicUI/layout"
	"github.com/ELY-Open-Source/CrychicUI/navigation"
	"github.com/ELY-Open-Source/CrychicUI/overlay"
	"github.com/ELY-Open-Source/CrychicUI/typography"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// 设置页的分类，顺序就是左栏顺序。下标只在这里定：别处一律用 tabXxx 的名字，不写数字
// （撤页、加页时写死的数字会错位，错位以后点哪页都不报错，只是内容对不上页名）。
const (
	tabLook     = iota // 外观
	tabModels          // 模型
	tabMemory          // 记忆和学习
	tabTools           // 工具与审批
	tabAccounts        // 平台账号
	tabNotify          // 通知
	tabData            // 数据
	tabPlugins         // 插件（链接解析、生活信息这些）
)

var settingsTabs = []string{
	tabLook: "外观", tabModels: "模型", tabMemory: "记忆和学习", tabTools: "工具与审批",
	tabAccounts: "平台账号", tabNotify: "通知", tabData: "数据", tabPlugins: "插件",
}

var timeUnits = []string{"分钟", "小时"}

type settingsState struct {
	tab int

	// 记忆和学习（线框 SetMemory，默认值照 sumika.example.yml）
	memIdle, memBatch, memTopK, memChars            float64
	memIdleU                                        string
	learnGroups, learnPrivate, learnHome, waitAudit bool
	reviewHours, reviewBatches                      float64
	reviewU                                         string
	paceOpen                                        bool
	visionN, backfillN, directMax                   float64
	direct                                          bool

	// 工具与审批
	approval  string
	workdir   string
	autoDirs  []string
	approvalM float64
	approvalU string
	alwaysAsk []string

	// 平台账号
	owner            string
	groups, privates []string
	reply            bool
	assign           string

	// 通知
	notify             [6][4]bool
	quietFrom, quietTo time.Duration
	sound              string

	// 数据
	daily        bool
	dailyAt      time.Time
	keep         float64
	media        int
	alertGB      float64
	logDays      string
	exportFrom   time.Time
	exportTo     time.Time
	confirmClean bool
	cleaned      string
}

func newSettings(chars []character) *settingsState {
	s := &settingsState{
		memIdle: 10, memIdleU: "分钟", memBatch: 40, memTopK: 8, memChars: 1200,
		learnGroups: true, learnPrivate: true, waitAudit: true, reviewHours: 3, reviewU: "小时", reviewBatches: 10,
		visionN: 2, backfillN: 2, direct: true, directMax: 4,
		approval: "manual", workdir: `D:\work`, autoDirs: []string{`D:\work\notes`, `D:\work\files`}, approvalM: 10, approvalU: "分钟",
		alwaysAsk: []string{"删除文件、清空回收站", "git push、git reset --hard"},
		owner:     "10000（占位）", groups: []string{"测试群（1001）"}, assign: "先空着",
		notify:    [6][4]bool{{true, true, true, true}, {true, true, false, true}, {true, false, false, true}, {true, true, false, false}, {true, false, false, false}, {true, false, false, false}},
		quietFrom: time.Hour, quietTo: 9 * time.Hour, sound: "轻轻的\"叮\"",
		daily: true, dailyAt: time.Date(2026, 1, 1, 4, 30, 0, 0, time.Local), keep: 14, media: 1, alertGB: 10, logDays: "30 天",
	}
	s.exportTo = time.Now()
	s.exportFrom = s.exportTo.AddDate(0, 0, -7)
	if len(chars) > 0 {
		s.assign = chars[0].Name
	}
	return s
}

// ───────────── 页面骨架 ─────────────

func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	s := a.set
	if s.tab != tabModels {
		a.models.leave()
	}
	if s.tab != tabPlugins {
		a.linkSettings.leave()
		a.lifeSettings.leave()
	}
	ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(240).Padding(t.Space(4), t.Space(3)).Background(theme.T(c).Materials().Sidebar().Background()).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			layout.PageHeader(c, "设置", layout.PageHeaderOptions{})
			navigation.VerticalTabs(c, "设置分类", settingsTabs, &s.tab)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Box(c).Children(func() {
				c.Key(s.tab)
				ui.Column(c).Padding(t.Space(4), t.Space(8), t.Space(10), t.Space(8)).Gap(t.Space(6)).MaxWidth(860).Transition(fade).Children(func() {
					layout.PageHeader(c, settingsTabs[s.tab], layout.PageHeaderOptions{})
					switch s.tab {
					case tabLook:
						a.pageLook(c)
					case tabModels:
						a.pageModelSettings(c)
					case tabMemory:
						a.pageMemory(c)
					case tabTools:
						a.pageTools(c)
					case tabAccounts:
						a.pageAccounts(c)
					case tabNotify:
						a.pageNotify(c)
					case tabData:
						a.pageData(c)
					case tabPlugins:
						a.pageLinkSettings(c)
					}
				})
			})
		})
	})
}

// group 一组：照 form.SettingsForm 用 layout.Section；hint 是组标题下一行灰字
func group(c *ui.Context, title, hint string, fn func()) {
	layout.Section(c, title, func() {
		if hint != "" {
			kit.Muted(c, hint)
		}
		fn()
	})
}

// row 一行选择：照 form.SettingsForm 里选择行的排法——左边名字（500 字重）和灰字，右边控件
func row(c *ui.Context, label, hint string, ctl func()) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(t.Space(3)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(t.Space(0.5)).Children(func() {
			ui.Text(c, label).FontWeight(500)
			if hint != "" {
				kit.Muted(c, hint)
			}
		})
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(ctl)
	})
}

// count 一个数加单位（单位固定）：input.NumberField + 灰字单位；单位能换的用 input.UnitField
func count(c *ui.Context, label string, v *float64, lo, hi, step float64, u string) {
	input.NumberField(c, label, v, input.NumberFieldOptions{Min: lo, Max: hi, Step: step})
	kit.Muted(c, u)
}

// ───────────── 各页 ─────────────

func (a *app) pageMemory(c *ui.Context) {
	s := a.set
	group(c, "记性", "聊过的话什么时候整理成记忆，每次回话想起多少", func() {
		input.UnitField(c, "聊天安静多久就整理一次", &s.memIdle, &s.memIdleU, timeUnits, input.UnitFieldOptions{Min: 1, Max: 240})
		row(c, "或者新攒够多少条就整理", "", func() { count(c, "攒够多少条", &s.memBatch, 5, 500, 5, "条") })
		row(c, "每次回话最多想起几条", "", func() { count(c, "想起几条", &s.memTopK, 0, 50, 1, "条") })
		row(c, "想起的记忆加起来最多多少字", "多了占上下文，少了容易想不全", func() { count(c, "最多多少字", &s.memChars, 200, 10000, 100, "字") })
	})
	group(c, "学说法", "从真人聊天里学\"什么时候怎么说\"，所有角色共用", func() {
		row(c, "从哪里学", "", func() {
			choice.Check(c, "QQ 群聊", &s.learnGroups)
			choice.Check(c, "QQ 私聊", &s.learnPrivate)
			choice.Check(c, "本地（只学你的话）", &s.learnHome)
		})
		choice.ToggleRow(c, "学到的先等你审核", "关掉就直接用；在\"学到的说法\"里也能一条条撤", &s.waitAudit)
		input.UnitField(c, "每隔多久让 AI 先审一遍（过了的才进\"等你审核\"；0 就是不让 AI 审）", &s.reviewHours, &s.reviewU, timeUnits, input.UnitFieldOptions{Min: 0, Max: 48})
		row(c, "AI 一次最多审几批", "", func() { count(c, "一次几批", &s.reviewBatches, 1, 50, 1, "批") })
		choice.ToggleRow(c, "学的节奏", "一段至少 8 行才学，一批 10 条，攒 40 条或安静 10 分钟学一次", &s.paceOpen)
		layout.Collapse(c, s.paceOpen, func() {
			typography.Caption(c, "细项和上面一样是数字加单位，原型先不摆")
		})
	})
	group(c, "看图", "识图模型在\"模型\"里选", func() {
		row(c, "同时看几张", "", func() { count(c, "同时看几张", &s.visionN, 1, 8, 1, "张") })
		row(c, "补看没看过的图时同时几张", "单独排队，新来的图先看", func() { count(c, "补看几张", &s.backfillN, 1, 8, 1, "张") })
		choice.ToggleRow(c, "QQ 私聊发来的图直接给她看", "她用的模型能看图时不等描述；群里照旧看描述", &s.direct)
		layout.Collapse(c, s.direct, func() {
			row(c, "一条消息最多直接给几张", "多的看描述", func() { count(c, "最多直接给几张", &s.directMax, 1, 10, 1, "张") })
		})
	})
}

func (a *app) pageTools(c *ui.Context) {
	s := a.set
	t := c.Theme()
	group(c, "审批方式", "", func() {
		ui.Row(c).FillWidth().Gap(t.Space(3)).AlignItems(ui.Stretch).Children(func() {
			choice.OptionCard(c, "手动批准", "写文件、跑命令前都问你", "manual", &s.approval).Grow(1).Basis(0)
			choice.OptionCard(c, "自动放行", "只读的、在允许目录里的直接做，其他还是问", "auto", &s.approval).Grow(1).Basis(0)
			choice.OptionCard(c, "免审批", "什么都不问，出了事找不回来；只建议临时打开", "yolo", &s.approval).Grow(1).Basis(0)
		})
	})
	group(c, "范围", "", func() {
		input.TextField(c, "工作目录", &s.workdir, input.TextFieldOptions{Help: "读写文件、跑命令的默认目录"})
		input.UnitField(c, "审批多久没人管就取消", &s.approvalM, &s.approvalU, timeUnits, input.UnitFieldOptions{Min: 1, Max: 1440})
		row(c, "QQ 里能用的工具", "只有聊天和记忆，不能碰你的电脑（固定的，改不了）", func() {})
	})
	group(c, "不用问就能改的目录", "自动放行时用", func() {
		input.ListField(c, "目录", &s.autoDirs, input.ListFieldOptions{Add: "加一个目录", Item: "目录"})
	})
	group(c, "无论如何都要问的", "免审批也照样拦下", func() {
		input.ListField(c, "要问的操作", &s.alwaysAsk, input.ListFieldOptions{Add: "加一条", Item: "操作"})
	})
}

var checks = []string{"收文字", "发文字", "收图片", "发图片", "收小黄脸", "发小黄脸", "贴表情回应", "引用回复", "戳一戳", "拉历史消息"}

func (a *app) pageAccounts(c *ui.Context) {
	s := a.set
	t := c.Theme()
	names := []string{"先空着"}
	for _, ch := range a.chars {
		names = append(names, ch.Name)
	}
	group(c, "QQ（通过 SnowLuma 连接）", "号码都是占位的", func() {
		row(c, "小号 1449****23", "ws://127.0.0.1:3001 · 连上 3 天 · 2 个群 · 3 个私聊", func() {
			choice.Dropdown(c, "分给谁", names, &s.assign)
			display.StatusBadge(c, "在线", true)
		})
		row(c, "收发自检", "上次 10/12 · 升级 SnowLuma 后再测一次", func() { button.Outline(c, "再测一次") })
		ui.Row(c).FillWidth().Wrap().Gap(t.Space(1.5)).Children(func() {
			for i, ck := range checks {
				v := display.BadgeSuccess
				if i == 9 {
					v = display.BadgeWarning
				}
				display.Badge(c, ck, display.BadgeOptions{Variant: v})
			}
		})
	})
	group(c, "谁能找她", "群与私聊的接话名单", func() {
		input.TextField(c, "主人的 QQ 号", &s.owner, input.TextFieldOptions{})
		choice.ToggleRow(c, "QQ 私聊自动回复", "默认关", &s.reply)
		input.ListField(c, "群白名单", &s.groups, input.ListFieldOptions{Add: "加一个群", Item: "群"})
		input.ListField(c, "私聊白名单", &s.privates, input.ListFieldOptions{Add: "加一个人", Item: "号码"})
	})
}

var notifyRows = []string{"她们要你批准", "她们有问题问你", "主动来找你聊天", "账号掉线", "定期任务的结果", "记忆等你确认"}
var notifyCols = []string{"窗口里", "系统通知", "QQ 私聊", "桌宠气泡"}

func (a *app) pageNotify(c *ui.Context) {
	s := a.set
	kit.Muted(c, "你在不在电脑前是自动判断的；这里决定每种事在不同情况下怎么提醒你")
	// 勾选矩阵：CrychicUI 的 choice.ChoiceMatrix 是每行只能选一个，这里每格一个勾，自己排（清单 C6）
	group(c, "什么事走哪条路", "掉线时 QQ 私聊发不出去，那一格不能选", func() {
		ui.Row(c).FillWidth().Children(func() {
			kit.Muted(c, "什么事").Grow(1)
			for _, col := range notifyCols {
				kit.Muted(c, col).Width(110)
			}
		})
		for i, r := range notifyRows {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, r).FontWeight(500).Grow(1)
				for j, col := range notifyCols {
					ui.Row(c).Width(110).Children(func() {
						// choice.Check 一定把名字显示在框旁边，表格里四列会挤成一团；这里用 mygo 的基础勾选框，名字只给读屏
						ck := ui.Checkbox(c, &s.notify[i][j], "").Label(r + "，" + col)
						if i == 3 && j == 2 {
							ck.Disabled(true)
						}
					})
				}
			})
		}
	})
	group(c, "其他", "", func() {
		datetime.TimeRangeSlider(c, "免打扰时段（这段时间只攒着，掉线除外）", &s.quietFrom, &s.quietTo, datetime.TimeRangeSliderOptions{})
		row(c, "提示音", "", func() {
			choice.Dropdown(c, "提示音", []string{"不出声", "轻轻的\"叮\"", "木琴", "水滴"}, &s.sound)
			button.Outline(c, "试听").Disabled(s.sound == "不出声")
		})
		row(c, "同一个角色连着来", "5 分钟内合成一条", func() {})
	})
}

func (a *app) pageData(c *ui.Context) {
	s := a.set
	t := c.Theme()
	group(c, "放在哪、占了多少", `D:\AppData\Sumika（模拟）`, func() {
		parts := []struct {
			name string
			gb   float32
			col  ui.Color
		}{{"聊天记录 1.9 GB", 1.9, t.Accent}, {"图片和表情包 1.1 GB", 1.1, t.Success}, {"记忆和向量 0.7 GB", 0.7, t.Warning}, {"日志 0.4 GB", 0.4, t.TextMuted}}
		ui.Row(c).FillWidth().Height(10).Radius(999).Clip().Background(t.SurfaceHover).Children(func() {
			for _, p := range parts {
				ui.Box(c).Grow(p.gb).Background(p.col)
			}
		})
		var items []display.LegendItem
		for _, p := range parts {
			items = append(items, display.LegendItem{Label: p.name, Color: p.col})
		}
		display.Legend(c, items)
		row(c, "数据放在", "", func() { button.Outline(c, "在文件夹里打开").Disabled(true) })
	})
	group(c, "备份", "", func() {
		choice.ToggleRow(c, "每天自动备份", "挑没在聊天的时候", &s.daily)
		layout.Collapse(c, s.daily, func() {
			datetime.TimePicker(c, "几点", &s.dailyAt, datetime.TimePickerOptions{Hours: "时", Minutes: "分", Period: "上午或下午"})
		})
		row(c, "留最近几份", "", func() { count(c, "留几份", &s.keep, 1, 60, 1, "份") })
		row(c, "每天那份带哪些媒体", "", func() {
			choice.Segments(c, "带哪些媒体", []string{"不带", "表情包和头像", "全部"}, &s.media)
		})
		row(c, "备份位置超过多少就提醒", "不自动删手动的", func() { count(c, "提醒门槛", &s.alertGB, 1, 500, 1, "GB") })
	})
	group(c, "导出和清理", "", func() {
		datetime.PresetRange(c, "导出一段时间的聊天", &s.exportFrom, &s.exportTo, datetime.PresetRangeOptions{Custom: "自己选", From: "从", To: "到",
			Presets: []datetime.Preset{{Label: "最近 7 天", Days: 7}, {Label: "最近 30 天", Days: 30}, {Label: "最近 90 天", Days: 90}}})
		row(c, "日志留多久", "", func() {
			choice.Dropdown(c, "日志留多久", []string{"7 天", "30 天", "90 天", "一直留着"}, &s.logDays)
		})
		row(c, "清理旧图片", "90 天前、没人再用的图，约 600 MB", func() {
			if s.cleaned != "" {
				typography.Caption(c, s.cleaned).TextColor(t.Success)
			}
			if button.Outline(c, "清理").Clicked() {
				s.confirmClean = true
			}
		})
	})
	_, ok := overlay.DangerDialog(c, "清理 90 天前的旧图片？", "这些图没有别的地方用到，删了只能从备份里找回。原型里不会真的删。", &s.confirmClean, overlay.DangerDialogOptions{Cancel: "取消", Confirm: "清理"})
	if ok.Clicked() {
		s.cleaned = "清掉了 612 MB（模拟）"
	}
}
