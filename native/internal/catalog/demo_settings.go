package catalog

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// settingsDemoChrome 是舞台里样板以外占的高：上下内距、标题和简介、节标题和各处的间距。
func settingsDemoChrome(k *winshell.Kit) float32 {
	g := k.G
	return 2*g.SpacePanel + 3*g.SpaceSection + 2*g.TitleLine + g.LabelLine + 3*g.SpaceSmall
}

// settingsDemoHeight 是样板的高：舞台高减去标题、简介和上下内距（量过，默认窗口 1900×1000 里正好一屏）。
func settingsDemoHeight(c *ui.Context, k *winshell.Kit) float32 {
	_, vh := c.Size()
	return max(vh-settingsDemoChrome(k), 4*k.G.SettingsGroupWidth/10)
}

// 内置插件的名字（样板用的示例，正式页里是后台报上来的 id）。
var settingsDemoPlugins = []string{
	"记忆", "复读", "戳回去", "技能", "联网", "子任务", "项目说明", "MCP", "渠道回复",
	"提醒", "定期任务", "目标", "检查点", "审批转发", "向主人提问", "备份", "通知",
}

// settingsDemo：设置页样板。左边分类栏，右边按事情分组的面；内容取正式界面里“记忆和学习”和“插件”两页的真实条目，值写成人话。
// 分类栏、分组、一项、小圆点、折叠列表都是 winshell 的通用函数，这里只摆内容。
func settingsDemo(c *ui.Context, e Env) {
	k := e.Kit
	page := Local(e, "page", func() string { return "memory" })

	cats := []winshell.Category{
		{ID: "memory", Label: "记忆和学习", Icon: chatui.Icon("brain", false)},
		{ID: "plugins", Label: "插件", Icon: chatui.Icon("layers", false)},
	}
	e.Section(c, "pane", "设置页", func() {
		k.SettingsPane(c, e.Key("settings"), winshell.SettingsPaneSpec{
			Group: "通用", Categories: cats, Selected: *page, OnSelect: func(id string) { *page = id },
			Height: settingsDemoHeight(c, k), Gen: e.Replay,
			Content: func() {
				if *page == "plugins" {
					pluginsPage(c, e)
					return
				}
				memoryPage(c, e)
			},
		})
	})
}

// 下面几个小函数把“值 + 默认值 + 控件”接成一项：值放在 Local 里，值和默认不同就出小圆点，改回默认小圆点就消失。

func stepItem(c *ui.Context, e Env, name, hint string, key string, def, v0 float64, o winshell.StepperSpec) {
	k := e.Kit
	v := Local(e, key, func() float64 { return v0 })
	text := func(x float64) string {
		if x == 0 && o.ZeroLabel != "" {
			return o.ZeroLabel
		}
		return fmt.Sprintf("%g %s", x, o.Unit)
	}
	k.SettingItem(c, e.Key(key), name, hint, winshell.SettingItemOptions{Changed: *v != def, Was: strings.TrimSpace(text(def))}, func() {
		k.Stepper(c, e.Key(key+"-step"), name, *v, o, func(x float64) { *v = x })
	})
}

func flagItem(c *ui.Context, e Env, name, hint, key string, def, v0 bool) {
	k := e.Kit
	v := Local(e, key, func() bool { return v0 })
	words := map[bool]string{true: "开", false: "关"}
	k.SettingItem(c, e.Key(key), name, hint, winshell.SettingItemOptions{Changed: *v != def, Was: words[def]}, func() {
		k.Switch(c, e.Key(key+"-sw"), name, v)
	})
}

func selectItem(c *ui.Context, e Env, name, hint, key string, items []string, def, v0 int) {
	k := e.Kit
	v := Local(e, key, func() int { return v0 })
	open := Local(e, key+"-open", func() bool { return false })
	k.SettingItem(c, e.Key(key), name, hint, winshell.SettingItemOptions{Changed: *v != def, Was: items[def]}, func() {
		k.SettingSelect(c, e.Key(key+"-dd"), name, open, *v, items, func(i int) { *v = i })
	})
}

// chipItem 是多选的筛选片：片少的放右边，片多的（below）放名字下面占满一行。
func chipItem(c *ui.Context, e Env, name, hint, key string, labels []string, def, v0 []bool, below bool) {
	k := e.Kit
	v := Local(e, key, func() []bool { return append([]bool(nil), v0...) })
	pick := func(on []bool) string {
		var names []string
		for i, b := range on {
			if b {
				names = append(names, labels[i])
			}
		}
		if len(names) == 0 {
			return "都不选"
		}
		return strings.Join(names, "、")
	}
	changed := false
	for i := range def {
		changed = changed || def[i] != (*v)[i]
	}
	k.SettingItem(c, e.Key(key), name, hint, winshell.SettingItemOptions{Changed: changed, Was: pick(def), Below: below}, func() {
		k.MultiChips(c, e.Key(key+"-chips"), labels, *v)
	})
}

func memoryPage(c *ui.Context, e Env) {
	k := e.Kit
	rerank := []string{"没有设置", "深度求索·快", "本地小模型"}
	characters := []string{"铃音", "晴枝", "雾栞"}
	places := []string{"你自己的 QQ 私聊", "本地私聊", "本地群聊", "所有人的 QQ 私聊", "QQ 群"}
	kinds := []string{"事实", "偏好", "承诺", "界限"}
	vision := []string{"没有设置", "深度求索·快", "本地小模型"}

	k.SettingGroup(c, e.Key("recall"), "回话时想起什么", func() {
		stepItem(c, e, "每次回话最多想起几条", "每轮回复最多带入这么多条记忆", "topk", 8, 12, winshell.StepperSpec{Min: 1, Max: 50, Unit: "条"})
		stepItem(c, e, "想起的记忆最多多少字", "带入的记忆加起来不超过这个字数", "maxchars", 1200, 1200, winshell.StepperSpec{Min: 100, Max: 8000, Step: 100, Unit: "字"})
		selectItem(c, e, "重排用的模型", "没填就不重排，按原来的顺序", "rerank", rerank, 0, 0)
		stepItem(c, e, "重排时看前几条", "她主动搜记忆时，只重排前这么多条", "rerank-k", 20, 20, winshell.StepperSpec{Min: 1, Max: 100, Unit: "条"})
		stepItem(c, e, "重排最多等多久", "超时这一轮就按原来的顺序", "rerank-wait", 5, 8, winshell.StepperSpec{Min: 1, Max: 60, Unit: "秒"})
	})
	k.SettingGroup(c, e.Key("write"), "整理记忆", func() {
		stepItem(c, e, "聊天安静多久整理一次", "会话安静这么久，就总结成记忆", "idle", 30, 30, winshell.StepperSpec{Min: 1, Max: 720, Unit: "分钟"})
		stepItem(c, e, "攒够多少条就整理", "新消息攒到这个数也会整理", "batch", 40, 40, winshell.StepperSpec{Min: 5, Max: 500, Step: 5, Unit: "条"})
		chipItem(c, e, "能记事的角色", "只有这些角色会把你让她记的事写进记忆", "who-writes", characters, []bool{true, false, false}, []bool{true, false, false}, false)
		chipItem(c, e, "能自动整理记忆的角色", "后台会把这些角色的聊天整理成记忆", "who-sums", characters, []bool{true, false, false}, []bool{true, true, false}, false)
		chipItem(c, e, "后台整理哪些聊天", "只整理这些场合里的聊天", "places", places, []bool{true, true, false, false, false}, []bool{true, true, false, false, false}, true)
		selectItem(c, e, "接管前落下的聊天", "开自动整理前要自己选一个", "catchup", []string{"不补，从最新开始", "接着旧进度整理（会花钱）"}, 0, 0)
	})
	k.SettingGroup(c, e.Key("facts"), "待确认的事实", func() {
		flagItem(c, e, "待确认的事实自动生效", "关着只在日志里数会生效几条", "auto-confirm", false, false)
		stepItem(c, e, "自动生效要几次不同场合的证据", "至少 2 次", "occasions", 3, 3, winshell.StepperSpec{Min: 2, Max: 10, Unit: "次"})
		chipItem(c, e, "哪几种事实可以自动生效", "承诺和界限会塑造她的行为，要你亲口放行", "kinds", kinds, []bool{true, true, false, false}, []bool{true, true, false, false}, true)
		k.SettingItem(c, e.Key("quarantine"), "隔离这个时间点之后整理的记忆", "出事止血用；没填就不隔离", winshell.SettingItemOptions{}, func() { k.SettingValue(c, "没有设置") })
	})
	k.SettingGroup(c, e.Key("learn"), "学说法", func() {
		flagItem(c, e, "从 QQ 群聊里学说法", "学真人聊天里什么时候怎么说，所有角色共用", "learn-group", true, true)
		flagItem(c, e, "从 QQ 私聊里学说法", "和上面一样，学的是私聊里的说法", "learn-private", false, false)
		flagItem(c, e, "从本地聊天里学说法", "开了也只学你的话", "learn-home", false, true)
		flagItem(c, e, "学到的说法直接用", "关着要等你点通过才用", "auto-approve", false, false)
		stepItem(c, e, "多久让 AI 先审一遍", "审过的才进等你审核", "review-hours", 6, 6, winshell.StepperSpec{Min: 1, Max: 72, AllowZero: true, ZeroLabel: "不让 AI 审", Unit: "小时"})
		stepItem(c, e, "AI 一次最多审几批", "一批 10 条", "review-batches", 3, 3, winshell.StepperSpec{Min: 1, Max: 20, Unit: "批"})
	})
	k.SettingGroup(c, e.Key("vision"), "看图", func() {
		flagItem(c, e, "看图", "QQ 收到的图和表情包在后台看一遍", "vision", true, true)
		selectItem(c, e, "识图用的模型", "没选的话图都不看", "vision-model", vision, 1, 1)
		stepItem(c, e, "同时看几张图", "数字大了快，但更占模型额度", "vision-conc", 4, 4, winshell.StepperSpec{Min: 1, Max: 16, Unit: "张"})
		stepItem(c, e, "补看没看过的图时同时几张", "单独排队，新来的图先看", "vision-lanes", 2, 2, winshell.StepperSpec{Min: 1, Max: 8, Unit: "张"})
		flagItem(c, e, "QQ 私聊发来的图直接给她看", "模型能看图时不等描述；群里照旧", "vision-direct", true, true)
		stepItem(c, e, "一条消息最多直接给几张图", "多的改看文字描述", "vision-max", 4, 4, winshell.StepperSpec{Min: 1, Max: 10, Unit: "张"})
	})
	k.SettingGroup(c, e.Key("debug"), "调试", func() {
		flagItem(c, e, "显示配置键名和来源", "对照配置文件时用", "show-keys", false, false)
	})
}

func pluginsPage(c *ui.Context, e Env) {
	k := e.Kit
	freeOpen := Local(e, "free-open", func() bool { return false })
	linkOpen := Local(e, "link-open", func() bool { return false })
	allOpen := Local(e, "plugins-all", func() bool { return false })
	k.SettingGroup(c, e.Key("builtin"), "内置插件", func() {
		k.SettingItem(c, e.Key("free"), "免费接口（天气、节假日、汇率）", "", winshell.SettingItemOptions{}, func() {
			k.SettingLink(c, e.Key("free-link"), "设置", *freeOpen, func() { *freeOpen = !*freeOpen })
		})
		k.SettingItem(c, e.Key("link"), "链接解析", "", winshell.SettingItemOptions{}, func() {
			k.SettingLink(c, e.Key("link-link"), "设置", *linkOpen, func() { *linkOpen = !*linkOpen })
		})
		k.SettingCollapsed(c, e.Key("all"), fmt.Sprintf("另有 %d 个内置插件（只读）", len(settingsDemoPlugins)), settingsDemoPlugins, allOpen, 3)
	})
	// 点“设置”在下面多出的一组：展开收起（高度弹簧加淡变）；设置页内容列的间距是 SpaceLarge
	k.Disclose(c, e.Key("free-disc"), *freeOpen, k.G.SpaceLarge, func() {
		k.SettingGroup(c, e.Key("free-group"), "免费接口", func() {
			flagItem(c, e, "定期检测接口", "检测只带固定参数，不带你的地点；最近有成功调用的接口会跳过。", "probe", true, true)
			stepItem(c, e, "检测间隔", "", "probe-hours", 6, 12, winshell.StepperSpec{Min: 1, Max: 48, Unit: "小时"})
		})
	})
	k.Disclose(c, e.Key("link-disc"), *linkOpen, k.G.SpaceLarge, func() {
		k.SettingGroup(c, e.Key("link-group"), "链接解析", func() {
			flagItem(c, e, "给她看", "", "link-context", true, true)
			flagItem(c, e, "自动展开", "自动展开使用自己的名单，角色接话档位独立。", "link-expand", true, true)
			mode := Local(e, "link-mode", func() int { return 0 })
			k.SettingItem(c, e.Key("link-mode-item"), "名单方式", "", winshell.SettingItemOptions{Changed: *mode != 0, Was: "白名单"}, func() {
				k.Segment(c, e.Key("link-mode-seg"), mode, []string{"白名单", "黑名单"})
			})
			stepItem(c, e, "每条消息最多几个链接", "", "link-max", 3, 3, winshell.StepperSpec{Min: 1, Max: 10, Unit: "个"})
			flagItem(c, e, "翻译外文正文", "", "link-translate", false, false)
			stepItem(c, e, "翻译时限", "", "link-timeout", 20, 20, winshell.StepperSpec{Min: 1, Max: 60, Unit: "秒"})
		})
	})
}
