package ctlproto

import "strings"

// CmdLink 是链接解析的命令：link preview 取一条链接、用模板试渲染（不发消息、不下载媒体）；link template 看当前生效的模板。
// 只读：不改程序的任何状态，也不往 QQ 发东西（规格-链接解析模板第十节）。
const CmdLink = "link"

// 子命令（放在 Args[0]）。
const (
	LinkPreview  = "preview"  // preview <链接>：取数（走共享缓存）并按模板渲染
	LinkTemplate = "template" // template [站点键]：当前生效的模板和可用占位符
)

// LinkArgs 是 link 的选项。Template 写法同配置（可以是 @预设名），空用当前生效的；
// Site 指定站点键（带 -forward 后缀表示转发模板）；Sample 为真时不联网，用该站点的示例数据渲染。
type LinkArgs struct {
	Template string `json:"template,omitempty"`
	Site     string `json:"site,omitempty"`
	Sample   bool   `json:"sample,omitempty"`
}

// MaxLinkTemplate 是 --template 文字的字数上限（和配置里的模板上限一致）。
const MaxLinkTemplate = 2000

// Validate 只查形状，不查站点键认不认得（后端认站点表）。
func (a LinkArgs) Validate() string {
	if n := len([]rune(a.Template)); n > MaxLinkTemplate {
		return "--template 太长了"
	}
	if strings.ContainsRune(a.Template, 0) || strings.ContainsRune(a.Site, 0) || len(a.Site) > 64 {
		return "--template 或 --site 里有不合法的字符"
	}
	return ""
}

// LinkPreviewResult 是 link preview 的结果：一条条消息的文字描述，图片类写成 [封面] 之类的方块。
type LinkPreviewResult struct {
	Site     string   `json:"site"`            // 用的站点键
	Template string   `json:"template"`        // 实际用的模板原文（已展开 @预设）
	Forward  bool     `json:"forward"`         // 这次走的是不是转发形态
	Source   string   `json:"source"`          // sample：示例数据；fetched：取到的真实数据
	Messages []string `json:"messages"`        // 每条消息（或合并转发）一段文字
	Notes    []string `json:"notes,omitempty"` // 提示：比如模板写错退回了内置默认
}

// LinkTemplates 是 link template 的结果。
type LinkTemplates struct {
	Sites []LinkSiteInfo `json:"sites"`
}

// LinkSiteInfo 是一个站点的当前设置。
type LinkSiteInfo struct {
	Key          string      `json:"key"`
	Label        string      `json:"label"`
	Direct       string      `json:"direct"`            // 当前生效的直发模板原文
	Forward      string      `json:"forward,omitempty"` // 当前生效的转发模板原文；没有转发形态为空
	ForwardMode  string      `json:"forwardMode,omitempty"`
	ForwardOver  int         `json:"forwardOver,omitempty"`
	DescLimit    int         `json:"descLimit,omitempty"`
	Placeholders []LinkField `json:"placeholders,omitempty"` // 只在指定站点时列出
}

// LinkField 是一个占位符。
type LinkField struct {
	Names  []string `json:"names"`
	Kind   string   `json:"kind"` // text / slot / layout
	Sample string   `json:"sample,omitempty"`
	Doc    string   `json:"doc,omitempty"`
}
