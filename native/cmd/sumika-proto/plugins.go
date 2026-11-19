package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 插件页（docs/规格-插件页.md）：设置里的一页，每个插件一张能展开的卡，展开后是它自己的配置项。
// 原型只改假状态（规格-交互原型 X05），形状照 Go 后台 main 4f321f42 的真实配置；不读写 data/。

// fieldKind 是配置项的类型，按它自动选控件（规格第三节的表）。
type fieldKind int

const (
	fieldSwitch fieldKind = iota // 开关
	fieldChoice                  // 选一个：两三个用分段，四个以上用下拉
	fieldInt                     // 有范围的整数：数字框 + 单位
	fieldText                    // 文字
	fieldPick                    // 从已知的里挑的名单（会话、角色）：小标签 + ×，"添加"只能挑已知的
	fieldMulti                   // 多选的固定项：一排能点的小标签
	fieldFixed                   // 只读的名单（写在别的文件里的，比如 mcp.json）
)

// pluginField 是一个配置项的描述：加插件只加数据，不写界面。
type pluginField struct {
	Key, Name, Help string
	Kind            fieldKind
	Options         []string // 选一个、多选、名单的候选
	Min, Max        int      // 整数的范围
	Unit            string   // 整数后面的单位
	Default         any      // 初值：bool、int、string、[]string
}

// plugin 是一个插件的描述。
type plugin struct {
	ID, Name, Desc, Icon string
	Core                 bool         // 不能关（照 issue #16：Core 不给关）
	PerSession           bool         // 按会话开（复读、戳回去）：卡上没有总开关
	Hot                  bool         // 改了马上生效；否则重启接收后生效
	Needs                []string     // 依赖的插件 ID
	Note                 string       // 展开区第一行之外的一句说明（比如"在 mcp.json 里改"）
	Scope                *pluginScope // 在哪里用（主人 11-07：先选平台、再选平台里的群和私聊、最后才是功能设置）
	Fields               []pluginField
	On                   bool   // 初始开没开
	Error                string // 出错的原因（假数据）
}

// pluginScope 是插件"在哪里用"：能选哪些平台、平台里分私聊群聊各怎么开；Fixed 非空时不能改，只写一行原因。
type pluginScope struct {
	Platforms []string          // 能选的平台（本地、QQ）
	Kinds     []string          // 能选的会话种类（私聊、群聊）
	On        []string          // 默认开着的平台
	Modes     map[string]string // 默认的开法，键是场合（本地私聊、本地群聊、QQ 私聊、QQ 群），没写的是"全部"
	Lists     map[string][]string
	Fixed     string
}

// 一个场合里怎么开：全部会话、只在挑出来的、除了挑出来的、都不开。
var scopeModes = []string{"全部", "只在这些", "除了这些", "不开"}

// occasion 是平台 + 会话种类合成的场合名，照词汇表的四种场合写（ADR-140）。
func occasion(platform, kind string) string {
	switch platform + kind {
	case "本地私聊":
		return "本地私聊"
	case "本地群聊":
		return "本地群聊"
	case "QQ私聊":
		return "QQ 私聊"
	default:
		return "QQ 群"
	}
}

// sessionsIn 是已知会话里属于这个场合的。
func sessionsIn(occ string) []string {
	var out []string
	for _, k := range knownSessions {
		if strings.HasPrefix(k, occ+" · ") {
			out = append(out, k)
		}
	}
	return out
}

// knownSessions 是"从已知里挑"的候选：本机已知的会话（名字都是编的，四种场合都有）。
var knownSessions = []string{"本地私聊 · 晴枝", "本地群聊 · 纸灯小组", "QQ 私聊 · 小雨", "QQ 群 · 周末读书会", "QQ 群 · 旧相机交流", "QQ 私聊 · 阿栗"}

// defaultPlugins 是原型里的插件（规格第四节的表）。
func defaultPlugins() []plugin {
	return []plugin{
		{ID: "memory", Name: "记忆", Desc: "回答前想起相关的事，聊完记下新的", Icon: "brain", Core: true,
			Scope: &pluginScope{Platforms: []string{"本地", "QQ"}, Kinds: []string{"私聊", "群聊"}, On: []string{"本地"}, Modes: map[string]string{"本地群聊": "不开"}},
			Fields: []pluginField{
				{Key: "characters", Name: "会写入记忆的角色", Kind: fieldPick, Options: []string{"晴枝", "雾栞", "纸灯"}, Default: []string{"晴枝"}},
				{Key: "autoConfirm", Name: "自动确认", Help: "同一件事说过几次就当真，不用你点头", Kind: fieldSwitch, Default: false},
				{Key: "confirmOccasions", Name: "说过几次算数", Kind: fieldInt, Min: 2, Max: 10, Unit: "次", Default: 3},
				{Key: "confirmKinds", Name: "自动确认哪几种", Kind: fieldMulti, Options: []string{"事实", "承诺", "偏好", "边界"}, Default: []string{"事实", "偏好"}},
			}},
		{ID: "link-reader", Name: "链接解析", Desc: "读懂聊天里的链接，需要时展开成卡片", Icon: "attach", Hot: true, On: true,
			Scope: &pluginScope{Platforms: []string{"本地", "QQ"}, Kinds: []string{"私聊", "群聊"}, On: []string{"本地", "QQ"}, Modes: map[string]string{"QQ 群": "只在这些"}, Lists: map[string][]string{"QQ 群": {"QQ 群 · 周末读书会"}}},
			Fields: []pluginField{
				{Key: "expand", Name: "自动展开", Help: "收到链接时发一张卡片出来", Kind: fieldSwitch, Default: false},
				{Key: "maxLinks", Name: "一条消息最多读几个链接", Kind: fieldInt, Min: 1, Max: 20, Unit: "个", Default: 3},
				{Key: "bilibiliFormat", Name: "B 站的样子", Kind: fieldChoice, Options: []string{"卡片", "封面加文字"}, Default: "卡片"},
				{Key: "videoTimeout", Name: "取视频的超时", Kind: fieldInt, Min: 1, Max: 600, Unit: "秒", Default: 60},
				{Key: "videoMB", Name: "视频大小上限", Kind: fieldInt, Min: 1, Max: 1000, Unit: "MB", Default: 100},
			}},
		{ID: "repeater", Name: "复读", Desc: "群里两个人接连复读时跟一句", Icon: "refresh", PerSession: true,
			Scope: &pluginScope{Platforms: []string{"本地", "QQ"}, Kinds: []string{"群聊"}, On: []string{"QQ"}, Modes: map[string]string{"本地群聊": "不开", "QQ 群": "只在这些"}}}, // 群里默认关（规格-群聊插件）
		{ID: "poke", Name: "戳回去", Desc: "被戳了就戳回去", Icon: "emoji", PerSession: true,
			Scope: &pluginScope{Platforms: []string{"QQ"}, Kinds: []string{"私聊", "群聊"}, On: []string{"QQ"}, Modes: map[string]string{"QQ 群": "只在这些"}, Lists: map[string][]string{"QQ 群": {"QQ 群 · 周末读书会"}}}}, // 私聊默认开、群里默认关（ADR-189）
		{ID: "skills", Name: "技能", Desc: "按需读技能目录里的说明", Icon: "bulb", Scope: &pluginScope{Fixed: "只在你和她的本地对话里用（她自己用的工具只给主人在本地主窗口，安全边界，不能改）"}, Fields: []pluginField{
			{Key: "roots", Name: "技能目录", Kind: fieldPick, Options: []string{"项目里的 skills", "用户目录的 skills"}, Default: []string{"项目里的 skills"}},
			{Key: "readKB", Name: "一次最多读", Kind: fieldInt, Min: 1, Max: 32, Unit: "KB", Default: 8},
		}},
		{ID: "web", Name: "联网", Desc: "搜索网页、抓取网页正文", Icon: "globe", On: true, Error: "没有设置 Tavily 的密钥，搜索用不了", Scope: &pluginScope{Fixed: "只在你和她的本地对话里用（她自己用的工具只给主人在本地主窗口，安全边界，不能改）"}, Fields: []pluginField{
			{Key: "search", Name: "搜索服务", Kind: fieldChoice, Options: []string{"Tavily"}, Default: "Tavily"},
			{Key: "fetch", Name: "抓网页", Kind: fieldChoice, Options: []string{"Tavily", "直接取"}, Default: "直接取"},
			{Key: "dailySearch", Name: "每天最多搜索", Kind: fieldInt, Min: 0, Max: 1000, Unit: "次", Default: 50},
			{Key: "dailyFetch", Name: "每天最多抓取", Kind: fieldInt, Min: 0, Max: 1000, Unit: "次", Default: 100},
			{Key: "timeout", Name: "超时", Kind: fieldInt, Min: 1, Max: 60, Unit: "秒", Default: 30},
		}},
		{ID: "subagent", Name: "子任务", Desc: "把查资料、跑命令分给另一个她去做", Icon: "fork", Scope: &pluginScope{Fixed: "只在你和她的本地对话里用（她自己用的工具只给主人在本地主窗口，安全边界，不能改）"}, Fields: []pluginField{
			{Key: "model", Name: "用的模型", Kind: fieldChoice, Options: []string{"DeepSeek V4.1 Flash", "Agnes 2.5 Flash", "Sonnet 5.5", "本地模型"}, Default: "DeepSeek V4.1 Flash"},
			{Key: "maxActive", Name: "最多同时几个", Kind: fieldInt, Min: 1, Max: 8, Unit: "个", Default: 2},
		}},
		{ID: "projectcontext", Name: "项目说明", Desc: "在信任的目录里读 AGENTS.md 这类说明", Icon: "folder", Scope: &pluginScope{Fixed: "只在你和她的本地对话里用（她自己用的工具只给主人在本地主窗口，安全边界，不能改）"}, Fields: []pluginField{
			{Key: "roots", Name: "信任的目录", Kind: fieldPick, Options: []string{"C:\\example\\notes", "C:\\example\\lamp"}, Default: []string{}},
			{Key: "userContext", Name: "也读用户级的说明", Kind: fieldSwitch, Default: false},
		}},
		{ID: "mcp", Name: "MCP", Desc: "接外部工具服务器", Icon: "tool", Note: "服务器写在 mcp.json 里，在那里改", Scope: &pluginScope{Fixed: "只在你和她的本地对话里用（她自己用的工具只给主人在本地主窗口，安全边界，不能改）"}, Fields: []pluginField{
			{Key: "servers", Name: "服务器", Kind: fieldFixed, Default: []string{"filesystem", "fetch"}},
		}},
		{ID: "channel-responder", Name: "渠道回复", Desc: "把她的回复送回原来的会话", Icon: "send", Core: true, Scope: &pluginScope{Fixed: "所有会话（她的回复都从这里送回去）"}},
	}
}

// pluginState 是插件页的假状态。值按"插件 ID.配置项"存；Applied 是上一次"重启接收"时各插件的样子，
// 和现在不一样的、要重启才生效的插件就算"还没生效"（改回去也就不算了）。
type pluginState struct {
	List       []plugin
	On         map[string]bool
	Vals       map[string]any     // bool、int、string、[]string
	Draft      map[string]*string // 数字框绑的字（输入框的值在构建完才更新，要绑一直存在的字符串）；越界、没打完时不写进 Vals
	Open       map[string]bool    // 手动展开的卡
	Adding     map[string]bool    // 名单正在"添加"
	Applied    map[string]string
	Restarting bool
	RestartAt  time.Time
	HoverSince map[string]time.Time // 鼠标停到数字框上的时刻（滚轮要停满 scrubHover 才调）
	Wheel      map[string]float32   // 触控板攒的距离（不满一格不动）
	Drag       map[string]float32   // 拖单位攒的距离
}

// 数字框的拖动和滚轮：拖多远一格、触控板滚多远一格（鼠标滚轮是一档一格）、在框上停多久滚轮才调数字。
const (
	scrubDrag    float32 = 4
	scrubPrecise float32 = 24
	scrubHover           = 350 * time.Millisecond
)

func newPluginState(list []plugin) *pluginState {
	s := &pluginState{List: list, On: map[string]bool{}, Vals: map[string]any{}, Draft: map[string]*string{}, Open: map[string]bool{}, Adding: map[string]bool{}, Applied: map[string]string{},
		HoverSince: map[string]time.Time{}, Wheel: map[string]float32{}, Drag: map[string]float32{}}
	for _, p := range list {
		s.On[p.ID] = p.On || p.Core
		for _, f := range p.Fields {
			v := f.Default
			if l, ok := v.([]string); ok {
				v = append([]string(nil), l...)
			}
			s.Vals[p.ID+"."+f.Key] = v
		}
		if sc := p.Scope; sc != nil && sc.Fixed == "" {
			s.Vals[p.ID+".scope"] = append([]string(nil), sc.On...)
			for _, pl := range sc.Platforms {
				for _, k := range sc.Kinds {
					occ := occasion(pl, k)
					mode := sc.Modes[occ]
					if mode == "" {
						mode = "全部"
					}
					s.Vals[p.ID+".scope."+occ] = mode
					s.Vals[p.ID+".scope."+occ+".list"] = append([]string{}, sc.Lists[occ]...)
				}
			}
		}
	}
	for _, p := range list {
		s.Applied[p.ID] = s.sig(p)
	}
	return s
}

// sig 是一个插件现在的样子（开关 + 全部配置项），用来比"改了没生效"。
func (s *pluginState) sig(p plugin) string {
	var b strings.Builder
	fmt.Fprint(&b, s.On[p.ID])
	keys := make([]string, 0, len(s.Vals))
	for k := range s.Vals { // 配置项和"在哪里用"都在 Vals 里，按键排好再拼
		if strings.HasPrefix(k, p.ID+".") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%v", k, s.Vals[k])
	}
	return b.String()
}

// pending 是改了、要重启接收才生效的插件 ID（马上生效的不算）。
func (s *pluginState) pending() []string {
	var ids []string
	for _, p := range s.List {
		if !p.Hot && s.sig(p) != s.Applied[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// matches 是搜索词命中的配置项（插件名、说明命中时返回 nil, true：整张卡都显示）。
func (p plugin) matches(q string) (fields map[string]bool, hit bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Desc+" "+p.ID), q) {
		return nil, true
	}
	for _, f := range p.Fields {
		if strings.Contains(strings.ToLower(f.Name+" "+f.Help), q) {
			if fields == nil {
				fields = map[string]bool{}
			}
			fields[f.Key] = true
		}
	}
	return fields, fields != nil
}

// plugins 是插件页的内容（设置页主区里，滚动区之内）。搜索词用左栏的"搜索设置"。
func (a *app) plugins(c *ui.Context) {
	s := a.pluginState()
	if s.Restarting && !c.Now().Before(s.RestartAt) { // 假装重启完：现在的样子就是生效的样子
		s.Restarting = false
		for _, p := range s.List {
			s.Applied[p.ID] = s.sig(p)
		}
	}
	// 重启提示条：有改动要重启才出现
	if ids := s.pending(); len(ids) > 0 || s.Restarting {
		// 和插件卡同一种浮起的底 + 细边（主人 11-07：原来浅强调色底配强调色按钮太花）；四边都是 spaceSmall，
		// 左边的图标和字放在内距 controlInset 的格子里，离边和按钮里的字离按钮边一样远
		radius := radiusControl + spaceSmall
		bar := ui.Row(c.Key("plugin-restart")).FillWidth().Padding(spaceSmall).Gap(spaceSmall).AlignItems(ui.Center).
			Radius(radius).Background(a.P.Composer).Border(1, a.P.Line)
		bar.Children(func() {
			text := fmt.Sprintf("有 %d 个插件的改动要重启接收才生效", len(ids))
			if s.Restarting {
				text = "正在重启接收……"
			}
			ui.Row(c.Key("restart-text")).Padding(controlInset).Gap(spaceSmall).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
				ui.Icon(c, chatui.Icon("refresh", false)).Size(iconSmall, iconSmall).TextColor(a.T.AccentOf(chatui.DefaultChar).Text).Shrink(0)
				ui.Text(c, text).FontSize(labelSize).FixedLineHeight(metaLine).TextColor(a.P.Ink).SingleLine().MinWidth(0)
			})
			a.button(c, "plugin-restart-now", "重启接收", true, s.Restarting, func() {
				s.Restarting, s.RestartAt = true, c.Now().Add(time.Second)
				c.After(time.Second + time.Millisecond)
			})
		})
		a.surface(c, bar, "plugin-restart", "panel", radius, spaceSmall)
	}
	// 排序：核心插件在最上面（主人 11-07），然后是出错的，其余按原来的顺序；不加组标题（主人：不该把能不能关当成固定的分类）
	list := append([]plugin(nil), s.List...)
	rank := func(p plugin) int {
		switch {
		case p.Core:
			return 0
		case p.Error != "":
			return 1
		}
		return 2
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i]) < rank(list[j]) })
	shown := 0
	for _, p := range list {
		fields, hit := p.matches(a.Search)
		if !hit {
			continue
		}
		shown++
		a.pluginCard(c, s, p, fields)
	}
	if shown == 0 {
		ui.Row(c.Key("plugin-empty")).FillWidth().Gap(spaceSmall).AlignItems(ui.Center).Justify(ui.Center).Padding(spaceSection, 0).Children(func() {
			text := "还没有插件"
			if strings.TrimSpace(a.Search) != "" {
				text = fmt.Sprintf("没有找到「%s」", strings.TrimSpace(a.Search))
			}
			ui.Text(c, text).FontSize(labelSize).FixedLineHeight(metaLine).TextColor(a.P.Muted).SingleLine()
			if strings.TrimSpace(a.Search) != "" {
				a.button(c, "plugin-search-clear", "清除", false, false, func() { a.Search = "" })
			}
		})
	}
}

func (a *app) pluginState() *pluginState {
	if a.Plugins == nil {
		a.Plugins = newPluginState(defaultPlugins())
	}
	return a.Plugins
}

// pluginCard 是一张插件卡：折叠时一行（图标、名字、一句话、开关、箭头），展开后是生效方式和配置项。
// 开关不在展开按钮里面：点开关只切开关，点行的其余部分展开（规格第三节）。
func (a *app) pluginCard(c *ui.Context, s *pluginState, p plugin, hits map[string]bool) {
	pad := spaceTight
	// 卡的圆角和展开区里的控件同心：控件离卡边 = 卡内距 + 展开区内距（spaceSmall），圆角 = 控件圆角 + 这段距离；
	// 顶上那行按钮离卡边只有卡内距，它的圆角 = 卡圆角 − 卡内距（按钮高不够两倍圆角时长高，同心优先，同过程卡头部）
	radius := radiusControl + pad + spaceSmall
	head := radius - pad
	open := s.Open[p.ID] || hits != nil
	on := s.On[p.ID]
	card := ui.Column(c.Key("plugin-"+p.ID)).Label("插件："+p.Name).FillWidth().Padding(pad).Radius(radius).Background(a.P.Composer).Border(1, a.P.Line) // 卡是浮起的一层，里面的数字框、分段才是凹下去的，分得开
	a.surface(c, card, "plugin-"+p.ID, "panel", radius, pad)
	card.Children(func() {
		// 右边留 spaceSmall：开关的右缘和展开区里控件的右缘（卡内距 + 展开区内距）对齐
		ui.Row(c.Key("head")).FillWidth().AlignItems(ui.Center).Children(func() {
			b := ui.ButtonBase(c.Key("toggle")).Label(p.Name).Grow(1).MinWidth(0).MinHeight(max(hitRegular, 2*head)).Padding(spaceSmall).Gap(spaceMedium).
				AlignItems(ui.Center).Justify(ui.Start).Radius(head).OnClick(func() { s.Open[p.ID] = !s.Open[p.ID] })
			bg := ui.Transparent
			if b.Pressed() {
				bg = a.P.Press
			} else if b.Hovered() {
				bg = a.P.Hover
			}
			b.Background(b.AnimateColor("fill", bg, controlMotion))
			b.Children(func() {
				ui.Icon(c, chatui.Icon(p.Icon, false)).Size(iconSize, iconSize).TextColor(a.T.AccentOf(chatui.DefaultChar).Text).Shrink(0)
				ui.Column(c.Key("text")).Grow(1).MinWidth(0).Children(func() {
					ui.Row(c.Key("name-row")).Gap(spaceTight).AlignItems(ui.Center).Children(func() {
						ui.Text(c.Key("name"), p.Name).FontSize(labelSize).FixedLineHeight(metaLine).FontWeight(mediumWeight).TextColor(a.P.Ink).SingleLine().MinWidth(0)
						if p.Error != "" && on {
							ui.Icon(c.Key("error-badge"), chatui.Icon("warning", true)).Size(iconSmall, iconSmall).TextColor(a.P.Danger).Label("出错了")
						}
						if !p.Hot && s.sig(p) != s.Applied[p.ID] {
							dot := metaSize / 2
							ui.Box(c.Key("pending-dot")).Size(dot, dot).Radius(dot / 2).Background(a.P.Accent).Label("重启接收后生效").Tooltip("重启接收后生效")
						}
					})
					sub, ink := p.Desc, a.P.Muted
					if p.Error != "" && on {
						sub, ink = p.Error, a.P.Danger
					}
					ui.Text(c.Key("desc"), sub).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(ink).SingleLine().FillWidth()
				})
			})
			a.surface(c, b, "plugin-"+p.ID+"-toggle", "button", head, spaceSmall)
			// 开关放在箭头左边、四边都是 spaceSmall 的格子里（主人 11-07 截图：开关贴边；后来要箭头靠最右）。
			// 格子宽固定、内容靠右，各卡的开关对成一列；没有开关的在这里写为什么：核心插件"总是开着"，按会话开的写开着几个会话
			ui.Row(c.Key("switch-slot")).Padding(spaceSmall).Width(slotWidth() + 2*spaceSmall).Shrink(0).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				switch {
				case p.Core:
					ui.Text(c.Key("no-switch"), "总是开着").FontSize(metaSize).FixedLineHeight(hitSmall).TextColor(a.P.Meta).SingleLine()
				case p.PerSession:
					ui.Text(c.Key("no-switch"), fmt.Sprintf("%d 个会话", a.perSessionCount(s, p))).FontSize(metaSize).FixedLineHeight(hitSmall).TextColor(a.P.Meta).FontFeatures(chatui.Tabular).SingleLine()
				default:
					v := s.On[p.ID]
					a.switchControl(c, "plugin-"+p.ID+"-on", "启用"+p.Name, &v) // 读屏名和展开按钮（插件名）分开
					s.On[p.ID] = v
				}
			})
			// 箭头在最右边（主人 11-07：开关往左、箭头靠最右）：单独一个按钮，点它和点名字一样展开；
			// 放在四边 spaceSmall 的格子里，离卡边 = 卡内距 + spaceSmall，和展开区里控件的右缘对齐，圆角和卡同心
			ui.Box(c.Key("chevron-slot")).Padding(spaceSmall).Shrink(0).Children(func() {
				e := ui.ButtonBase(c.Key("expand")).Label("展开"+p.Name).Size(hitSmall, hitSmall).Center().Radius(radiusControl).
					OnClick(func() { s.Open[p.ID] = !s.Open[p.ID] })
				bg := ui.Transparent
				if e.Pressed() {
					bg = a.P.Press
				} else if e.Hovered() || b.Hovered() {
					bg = a.P.Hover
				}
				e.Background(e.AnimateColor("fill", bg, controlMotion))
				deg := float32(0)
				if open {
					deg = 90
				}
				e.Children(func() {
					ui.Icon(c.Key("chevron"), chatui.Icon("right", false)).Size(iconSmall, iconSmall).TextColor(a.P.Meta).
						Rotate(e.AnimateTo("chevron", deg, controlMotion))
				})
				a.surface(c, e, "plugin-"+p.ID+"-expand", "button", radiusControl, 0)
			})
		})
		if !open {
			return
		}
		body := ui.Column(c.Key("body")).FillWidth().Padding(spaceSmall, spaceSmall, spaceSmall, spaceSmall).Gap(spaceMedium).
			Transition(ui.ElementTransition{Animation: &controlMotion, Enter: &ui.Motion{Y: -spaceSmall, Opacity: 0}})
		if !on && !p.Core && !p.PerSession {
			body.Opacity(.72) // 关着：配置照样能改（下次开时用），整体淡一档提示"现在没在用"
		}
		body.Children(func() {
			ui.Box(c.Key("rule")).FillWidth().Height(1).Background(a.P.Line)
			effect := "重启接收后生效" // 写在"设置"标题那一行的右边：说的是设置改了以后什么时候生效
			if p.Hot {
				effect = "改了马上生效"
			}
			if p.Error != "" && on {
				ui.Text(c.Key("error"), p.Error).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Danger).FillWidth()
			}
			if p.Note != "" {
				ui.Text(c.Key("note"), p.Note).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Muted).FillWidth()
			}
			for _, need := range p.Needs {
				name, ok := need, s.On[need]
				for _, q := range s.List {
					if q.ID == need {
						name = q.Name
					}
				}
				state, ink := "已开", a.P.Muted
				if !ok {
					state, ink = "没开", a.P.Danger
				}
				ui.Text(c.Key("need-"+need), fmt.Sprintf("需要：%s（%s）", name, state)).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(ink)
			}
			if hits == nil { // 搜索命中具体配置项时只显示那几项
				a.pluginScope(c, s, p)
			}
			switch {
			case len(p.Fields) == 0:
				ui.Text(c.Key("effect"), "没有可调的设置").FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Meta)
			case hits == nil:
				a.pluginSection(c, "settings-title", "设置", effect)
			}
			for _, f := range p.Fields {
				if hits != nil && !hits[f.Key] {
					continue
				}
				a.pluginField(c, s, p, f)
			}
		})
	})
}

// slotWidth 是卡右边开关格子的宽：放得下开关，也放得下"总是开着""12 个会话"这样的四五个字。
func slotWidth() float32 { return 5 * metaSize }

// perSessionCount 是按会话开的插件现在在几个已知会话里开着（照"在哪里用"算）。
func (a *app) perSessionCount(s *pluginState, p plugin) int {
	sc := p.Scope
	if sc == nil || sc.Fixed != "" {
		return 0
	}
	on := map[string]bool{}
	for _, pl := range s.list(p.ID + ".scope") {
		on[pl] = true
	}
	n := 0
	for _, pl := range sc.Platforms {
		if !on[pl] {
			continue
		}
		for _, k := range sc.Kinds {
			occ := occasion(pl, k)
			picked := map[string]bool{}
			for _, v := range s.list(p.ID + ".scope." + occ + ".list") {
				picked[v] = true
			}
			mode, _ := s.Vals[p.ID+".scope."+occ].(string)
			for _, sess := range sessionsIn(occ) {
				switch {
				case mode == "全部", mode == "只在这些" && picked[sess], mode == "除了这些" && !picked[sess]:
					n++
				}
			}
		}
	}
	return n
}

func (s *pluginState) list(key string) []string { l, _ := s.Vals[key].([]string); return l }

// pluginSection 是展开区里的小标题（"在哪里用""设置"）：正文色加粗，比配置项名字重一档；right 是写在同一行右边的灰字（生效方式）。
func (a *app) pluginSection(c *ui.Context, key, text, right string) {
	ui.Row(c.Key(key)).FillWidth().Gap(spaceSmall).AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("title"), text).FontSize(labelSize).FixedLineHeight(metaLine).FontWeight(strongWeight).TextColor(a.P.Ink).Grow(1)
		if right != "" {
			ui.Text(c.Key("effect"), right).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Meta).SingleLine()
		}
	})
}

// pluginScope 画"在哪里用"：先选平台，再给选了的平台里每种会话（四种场合）选开法，
// 选"只在这些""除了这些"时下面列这个场合里已知的会话来挑。不能改的只写一行原因。
func (a *app) pluginScope(c *ui.Context, s *pluginState, p plugin) {
	sc := p.Scope
	if sc == nil {
		return
	}
	a.pluginSection(c, "scope-title", "在哪里用", "")
	if sc.Fixed != "" {
		ui.Text(c.Key("scope-fixed"), sc.Fixed).FontSize(labelSize).FixedLineHeight(metaLine).TextColor(a.P.Muted).FillWidth()
		return
	}
	if len(sc.Platforms) > 1 {
		a.pluginField(c, s, p, pluginField{Key: "scope", Name: "平台", Kind: fieldMulti, Options: sc.Platforms})
	}
	on := map[string]bool{}
	for _, pl := range s.list(p.ID + ".scope") {
		on[pl] = true
	}
	for _, pl := range sc.Platforms {
		if !on[pl] && len(sc.Platforms) > 1 {
			continue
		}
		for _, k := range sc.Kinds {
			occ := occasion(pl, k)
			a.pluginField(c, s, p, pluginField{Key: "scope." + occ, Name: occ, Kind: fieldChoice, Options: scopeModes})
			mode, _ := s.Vals[p.ID+".scope."+occ].(string)
			if mode == "只在这些" || mode == "除了这些" {
				name := "在这些里开"
				if mode == "除了这些" {
					name = "这些里不开"
				}
				a.pluginField(c, s, p, pluginField{Key: "scope." + occ + ".list", Name: name, Kind: fieldPick, Options: sessionsIn(occ)})
			}
		}
	}
}

// pluginField 画一个配置项：左边名字（下面可选一行灰色说明），右边按类型选的控件；名单类的控件在下面另起一行。
func (a *app) pluginField(c *ui.Context, s *pluginState, p plugin, f pluginField) {
	key := p.ID + "." + f.Key
	id := "plugin-" + p.ID + "-" + f.Key
	label := func() {
		ui.Column(c.Key("label")).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, f.Name).FontSize(labelSize).FixedLineHeight(metaLine).TextColor(a.P.Ink)
			if f.Help != "" {
				ui.Text(c.Key("help"), f.Help).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Meta).SingleLine().FillWidth()
			}
		})
	}
	switch f.Kind {
	case fieldSwitch:
		ui.Row(c.Key(id + "-field")).FillWidth().Gap(spacePanel).AlignItems(ui.Center).Children(func() {
			label()
			v, _ := s.Vals[key].(bool)
			a.switchControl(c, id, f.Name, &v)
			s.Vals[key] = v
		})
	case fieldChoice:
		ui.Row(c.Key(id + "-field")).FillWidth().Gap(spacePanel).AlignItems(ui.Center).Children(func() {
			label()
			cur, _ := s.Vals[key].(string)
			if len(f.Options) <= 3 {
				n := 0
				for i, o := range f.Options {
					if o == cur {
						n = i
					}
				}
				ui.Column(c.Key(id + "-box")).Width(controlWide).Shrink(1).MinWidth(0).Children(func() { a.segment(c, id, &n, f.Options) })
				s.Vals[key] = f.Options[n]
				return
			}
			a.choice(c, id, f.Name, &cur, f.Options, func(x string) string { return x }).MinWidth(valueWidth * 3) // 和数字框一样宽，"全部""只在这些"左边对齐
			s.Vals[key] = cur
		})
	case fieldInt:
		ui.Column(c.Key(id + "-field")).FillWidth().Gap(spaceTight).Children(func() {
			cur, _ := s.Vals[key].(int)
			draft := s.Draft[key]
			if draft == nil {
				draft = new(string)
				*draft = strconv.Itoa(cur)
				s.Draft[key] = draft
			}
			n, err := strconv.Atoi(strings.TrimSpace(*draft))
			bad := err != nil || n < f.Min || n > f.Max
			if !bad {
				s.Vals[key] = n // 改对了就保存；越界、没打完的留在框里不保存
			}
			ui.Row(c.Key("row")).FillWidth().Gap(spacePanel).AlignItems(ui.Center).Children(func() {
				label()
				// 单位写在框里靠右（主人 11-07 截图：单位挂在框外、各框没对齐）：框宽固定，右缘和分段、开关对成一条线
				box := ui.Row(c.Key("input")).Width(valueWidth * 3).Height(rowHeight).Shrink(0).AlignItems(ui.Center).Radius(radiusControl).Background(a.P.Bubble)
				if bad {
					box.Border(1, a.P.Danger)
				}
				nudge := func(steps int, mods ui.Modifiers) { // 拖动、滚轮都走这里：一次 steps 格，按住 Shift 一格是 10，夹在范围里
					if mods&ui.Shift != 0 {
						steps *= 10
					}
					base, _ := s.Vals[key].(int) // 读状态里存着的值：两帧之间来好几下滚轮时，每下都在上一下的结果上加（越界没存的从存着的值起）
					v := min(max(base+steps, f.Min), f.Max)
					s.Vals[key], *draft = v, strconv.Itoa(v)
				}
				// 滚轮：鼠标在框上停满 scrubHover 才调数字。滚整页时框从鼠标底下滑过去，停不满，滚轮照常滚页面，不会误改
				if box.Hovered() {
					if _, ok := s.HoverSince[key]; !ok {
						s.HoverSince[key] = c.Now()
					}
				} else {
					delete(s.HoverSince, key)
				}
				held := c.Modifiers() // 这一帧按着的键：有的平台滚轮事件不带修饰键，和事件里的合起来看 Shift
				box.HandleInput(func(ev ui.InputEvent) bool {
					since, ok := s.HoverSince[key]
					if ev.Kind != ui.InputScroll || !ok || ev.Time.Sub(since) < scrubHover {
						return false
					}
					d := ev.DY
					if d == 0 {
						d = ev.DX // Windows 上按住 Shift 滚是横着滚
					}
					switch {
					case !ev.Precise: // 鼠标滚轮：一档一格，不按距离算（一档报的距离随系统设置，几十 DIP 都有）
						if d < 0 {
							nudge(1, ev.Mods|held) // 往上滚变大
						} else if d > 0 {
							nudge(-1, ev.Mods|held)
						}
					default: // 触控板按像素来，攒够 scrubPrecise 才动一格
						s.Wheel[key] += d
						for s.Wheel[key] <= -scrubPrecise {
							s.Wheel[key] += scrubPrecise
							nudge(1, ev.Mods|held)
						}
						for s.Wheel[key] >= scrubPrecise {
							s.Wheel[key] -= scrubPrecise
							nudge(-1, ev.Mods|held)
						}
					}
					return true
				})
				box.Children(func() {
					in := ui.TextInputBase(c.Key(id), draft).Label(f.Name).Grow(1).MinWidth(0).Height(rowHeight).Padding(fieldInset).Radius(radiusControl).
						FontSize(labelSize).FixedLineHeight(metaLine).FontFeatures(chatui.Tabular)
					a.surface(c, in, id, "input", radiusControl, fieldInset)
					if f.Unit != "" {
						// 单位是拖动把手：按住左右拖，每 scrubDrag 变一格；指针换成左右箭头。不在数字上拖，点数字是要放光标、选字的
						h := ui.ButtonBase(c.Key("unit")).Label("拖动调整" + f.Name).Padding(fieldInset).Shrink(0).Cursor(ui.CursorResizeEW) // 没有底色（悬停只把字调深），不设圆角
						if dx, _, ok := h.Dragged(); ok {
							s.Drag[key] += dx
							for s.Drag[key] >= scrubDrag {
								s.Drag[key] -= scrubDrag
								nudge(1, c.Modifiers())
							}
							for s.Drag[key] <= -scrubDrag {
								s.Drag[key] += scrubDrag
								nudge(-1, c.Modifiers())
							}
						} else if !h.Dragging() {
							delete(s.Drag, key)
						}
						ink := a.P.Muted
						if h.Hovered() || h.Dragging() {
							ink = a.P.Ink
						}
						h.Children(func() { ui.Text(c, f.Unit).FontSize(labelSize).FixedLineHeight(metaLine).TextColor(ink).SingleLine() })
						a.surface(c, h, id+"-unit", "button", 0, fieldInset)
					}
				})
				a.surface(c, box, id+"-box", "input", radiusControl, 0)
			})
			if bad {
				ui.Text(c.Key("range"), fmt.Sprintf("要在 %d～%d %s之间，改对了才保存", f.Min, f.Max, f.Unit)).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Danger)
			}
		})
	case fieldText:
		ui.Row(c.Key(id + "-field")).FillWidth().Gap(spacePanel).AlignItems(ui.Center).Children(func() {
			label()
			buf := s.Draft[key] // 绑一直存在的字符串（同数字框）
			if buf == nil {
				buf = new(string)
				*buf, _ = s.Vals[key].(string)
				s.Draft[key] = buf
			}
			a.textField(c, id, buf, f.Name).Width(controlWide).Shrink(1).MinWidth(0)
			s.Vals[key] = *buf
		})
	case fieldPick, fieldFixed:
		ui.Column(c.Key(id + "-field")).FillWidth().Gap(spaceSmall).Children(func() {
			ui.Row(c.Key("row")).FillWidth().Gap(spacePanel).AlignItems(ui.Center).Children(func() { label() })
			cur := s.list(key)
			ui.Row(c.Key("chips")).FillWidth().Wrap().Gap(spaceSmall).Children(func() {
				if len(cur) == 0 {
					ui.Text(c.Key("none"), "还没有").FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Meta).Padding(controlInset, 0)
				}
				for i, v := range cur {
					a.pluginChip(c, fmt.Sprint("chip-", i), v, f.Kind == fieldPick, false, func() {
						s.Vals[key] = append(append([]string(nil), cur[:i]...), cur[i+1:]...)
					})
				}
				if f.Kind == fieldPick && len(a.pickLeft(f.Options, cur)) > 0 {
					text := "添加"
					if s.Adding[key] {
						text = "完成"
					}
					a.button(c, id+"-add", text, false, false, func() { s.Adding[key] = !s.Adding[key] })
				}
			})
			if f.Kind == fieldPick && s.Adding[key] {
				left := a.pickLeft(f.Options, cur)
				ui.Row(c.Key("candidates")).FillWidth().Wrap().Gap(spaceSmall).Children(func() {
					for i, v := range left {
						a.pluginChip(c, fmt.Sprint("cand-", i), v, false, true, func() {
							s.Vals[key] = append(append([]string(nil), cur...), v)
							if len(left) == 1 {
								s.Adding[key] = false
							}
						})
					}
				})
			}
		})
	case fieldMulti:
		ui.Column(c.Key(id + "-field")).FillWidth().Gap(spaceSmall).Children(func() {
			ui.Row(c.Key("row")).FillWidth().Children(func() { label() })
			cur := s.list(key)
			has := map[string]bool{}
			for _, v := range cur {
				has[v] = true
			}
			ui.Row(c.Key("options")).FillWidth().Wrap().Gap(spaceSmall).Children(func() {
				for i, o := range f.Options {
					o := o
					// 读屏名带上配置项：单写"QQ"会和导航栏的 QQ 重名
					b := ui.ButtonBase(c.Key(fmt.Sprint("opt-", i))).Label(f.Name + "：" + o).Height(hitSmall).Padding(controlInset).Radius(radiusControl).Selected(has[o]).OnClick(func() {
						var next []string
						for _, x := range f.Options {
							if (x == o) != has[x] { // 点的那个翻转，其余照旧，顺序照选项表
								next = append(next, x)
							}
						}
						if next == nil {
							next = []string{}
						}
						s.Vals[key] = next
					})
					bg, ink := ui.Transparent, a.P.Muted
					if has[o] {
						bg, ink = a.P.Self, a.P.Ink
					} else if b.Hovered() {
						bg = a.P.Hover
					}
					b.Background(b.AnimateColor("fill", bg, controlMotion)).Border(1, a.P.Line).TextColor(ink)
					b.Children(func() { ui.Text(c, o).FontSize(labelSize).FixedLineHeight(metaLine).SingleLine() })
					a.surface(c, b, id+"-"+o, "button", radiusControl, controlInset)
				}
			})
		})
	}
}

// pickLeft 是名单里还没选的候选。
func (a *app) pickLeft(options, cur []string) []string {
	has := map[string]bool{}
	for _, v := range cur {
		has[v] = true
	}
	var left []string
	for _, o := range options {
		if !has[o] {
			left = append(left, o)
		}
	}
	return left
}

// pluginChip 是名单里的一个小标签：已选的带 ×（removable），候选的前面是 +（candidate），整个标签都能点。
func (a *app) pluginChip(c *ui.Context, key, text string, removable, candidate bool, fn func()) {
	label := text
	if removable {
		label = "删掉 " + text
	} else if candidate {
		label = "添加 " + text
	}
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(hitSmall).Padding(controlInset).Gap(spaceTight).AlignItems(ui.Center).Radius(radiusControl).Disabled(!removable && !candidate)
	if removable || candidate {
		b.OnClick(fn)
	}
	bg := a.P.Bubble
	if candidate {
		bg = ui.Transparent
	}
	if (removable || candidate) && b.Hovered() {
		bg = a.P.Hover
	}
	b.Background(b.AnimateColor("fill", bg, controlMotion)).Border(1, a.P.Line).TextColor(a.P.Ink)
	b.Children(func() {
		if candidate {
			ui.Icon(c, chatui.Icon("add", false)).Size(iconSmall, iconSmall).TextColor(a.P.Muted)
		}
		ui.Text(c, text).FontSize(labelSize).FixedLineHeight(metaLine).SingleLine()
		if removable {
			ui.Icon(c, chatui.Icon("close", false)).Size(iconSmall, iconSmall).TextColor(a.P.Muted)
		}
	})
	a.surface(c, b, key+"-"+text, "button", radiusControl, controlInset)
}
