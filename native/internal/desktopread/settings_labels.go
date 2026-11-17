package desktopread

import (
	"strings"

	"sumika-native/internal/settingsreg"
)

// settingLabel 是一个设置键给主人看的样子：中文名、一行说明、单位、取值的叫法。
// 说明按设计语言第十一节写：常驻最多一行，说"这个会怎样"，不讲原理，也不写字段名和 ADR 号。
// 示例配置（sumika.example.yml）里每个键的中文注释是这里的出处。
type settingLabel struct {
	key     string // "characters.*.model" 这种带 * 的是每个角色一条
	name    string
	help    string
	unit    string
	choices map[string]string
}

var thinkingChoices = settingsreg.ThinkingChoices

// settingLabels 只留“程序当下的状态”那几项（不是配置键，或和注册表里的同名规划项含义不同）；
// 配置键的名字和说明在 settingsreg/records.go 的记录里（ui 字段），labelOf 先查这张表再查注册表。
var settingLabels = []settingLabel{
	// 模型

	// 记忆和学习

	// 工具与审批

	// 平台账号

	// 数据

	// 程序当下的状态（Running 给出才有值，其余写"尚未接入"）
	{key: "appearance.theme", name: "深浅主题", help: "窗口现在用的是浅色还是深色"},
	{key: "appearance.reducedMotion", name: "减弱动效", help: "是否减少界面里的动画"},
	{key: "appearance.fontSize", name: "字号", help: "窗口里文字的大小"},
	{key: "appearance.layout", name: "界面布局", help: "几栏、栏宽这些排法"},
	{key: "occupation", name: "职业", help: "角色在本地干活时的身份"},
	{key: "models.capabilities", name: "模型能力", help: "模型能不能看图、想多深，要连上网关才知道"},
	{key: "tools.execution", name: "执行类工具", help: "能不能在这台电脑上跑命令、改文件"},
	{key: "tools.uiApproval", name: "在窗口里批准", help: "审批请求能不能直接在窗口里点"},
	{key: "platform.selectedAccount", name: "当前选中的小号", help: "平台账号页现在看的是哪个号"},
	{key: "platform.connection", name: "平台连接", help: "和 QQ 协议端连上没有"},
	{key: "notifications.alerts", name: "没恢复的提醒", help: "掉线、受阻这类还没解决的提醒"},
	{key: "notifications.autostart", name: "开机自动启动", help: "登录系统后自动打开 Sumika"},
	{key: "data.group", name: "数据分组", help: "数据页里按什么分组"},
	{key: "data.run", name: "后台运行情况", help: "后台现在是什么状态"},
	{key: "data.localPath", name: "本地数据位置", help: "聊天记录和记忆存在哪"},
	{key: "data.qqPath", name: "QQ 数据位置", help: "本机 QQ 的数据放在哪"},
	{key: "data.mode", name: "后台模式", help: "只看历史，还是正在接收"},
	{key: "data.size", name: "数据占用", help: "数据目录现在多大"},
	{key: "shortcuts.registered", name: "已注册的快捷键", help: "现在生效的全局快捷键"},
}

// labelKey 把一个设置键对到表里的条目名：characters.<id>.model 对到 characters.*.model，其余原样。
func labelKey(key string) string {
	if rest, ok := strings.CutPrefix(key, "characters."); ok {
		if i := strings.LastIndex(rest, "."); i > 0 {
			return "characters.*" + rest[i:]
		}
	}
	return key
}

func labelOf(key string) (settingLabel, bool) {
	k := labelKey(key)
	for _, l := range settingLabels {
		if l.key == k {
			return l, true
		}
	}
	if l, ok := settingsreg.LabelOf(key); ok {
		return settingLabel{key: k, name: l.Name, help: l.Help, unit: l.Unit, choices: l.Choices}, true
	}
	return settingLabel{}, false
}

// characterTitle 角色那三项在名字前面写上是哪个角色（id 来自配置，已经过脱敏）
func characterTitle(key, name string) string {
	rest, ok := strings.CutPrefix(key, "characters.")
	if !ok {
		return name
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return name
	}
	return "角色 " + rest[:i] + "：" + name
}
