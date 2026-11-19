package linktemplate

import "strings"

// 发出去的样式：卡片（一张渲染图加链接）、图文（图加文字，一条消息）、混合（卡片加分段文字，或简短提示加合并转发）。
// 样式不是新的模板语言，只是“模板预设加合并转发模式”的一个固定搭配：选样式就是写入 @预设名 和 forward 两个设置，
// 高级用户仍可在编辑框里直接写通配符模板（那时样式显示“自定义”）。

const (
	StyleDefault = "default" // 按内置默认：模板和合并转发都不写进配置
	StyleCard    = "card"
	StyleText    = "text"
	StyleMixed   = "mixed"
)

// Style 是一个站点可选的一种样式。
type Style struct {
	ID     string
	Label  string
	Hint   string // 一句话说明发出去长什么样
	Direct string // 写进 templates.<站点> 的值（@预设名）；空表示不写，用内置默认
	// Forward 写进 sites.<站点>.forward；空表示站点没有合并转发形态，不写
	Forward string
}

// Styles 返回这个站点能选的样式，顺序固定：内置默认、卡片、图文、混合。
func (s Site) Styles() []Style {
	fwd := func(mode string) string {
		if s.HasForward() {
			return mode
		}
		return ""
	}
	auto := fwd(ForwardAuto)
	// 直发的图超过上限（expand.directImageMax）时：站点设成 never（图文样式）只发前几张，文字末尾标出发了几张、共几张；没有转发形态的站点默认进合并转发（规格-链接发送改版 B3、B10）
	textHint, mixedHint := "封面或图片加文字；图多时进合并转发", "先发卡片，文字另起一条；图多时进合并转发"
	if s.HasForward() {
		textHint = "封面或图片加文字；图多于上限时只发前几张"
	}
	out := []Style{
		{ID: StyleDefault, Label: "默认", Hint: "不改模板，按这个站点原来的排法", Direct: "", Forward: auto},
		{ID: StyleCard, Label: "卡片", Hint: "一张渲染好的卡片图加链接", Direct: "@卡片", Forward: fwd(ForwardNever)},
		{ID: StyleText, Label: "图文", Hint: textHint, Direct: "@图文", Forward: fwd(ForwardNever)},
	}
	if s.HasForward() {
		out = append(out, Style{ID: StyleMixed, Label: "混合", Hint: "内容全放进合并转发，不另发消息", Direct: "", Forward: ForwardAlways})
	} else {
		out = append(out, Style{ID: StyleMixed, Label: "混合", Hint: mixedHint, Direct: "@混合"})
	}
	return out
}

// StyleByID 按样式 ID 找这个站点的样式。
func (s Site) StyleByID(id string) (Style, bool) {
	for _, st := range s.Styles() {
		if st.ID == id {
			return st, true
		}
	}
	return Style{}, false
}

// effective 是配置值生效后的直发模板原文：没写用内置默认，写了 @预设名 就展开；写错了返回空串。
func (s Site) effective(cfg string) string {
	if strings.TrimSpace(cfg) == "" {
		return s.Source(false)
	}
	out, err := s.Resolve(cfg)
	if err != nil {
		return ""
	}
	return out
}

// CurrentStyle 说现在配置对应哪种样式；都对不上返回 ""（界面写“自定义”）。
// template 是 templates.<站点> 的值，forward 是 sites.<站点>.forward 的值（空按 auto）。
func (s Site) CurrentStyle(template, forward string) string {
	if forward == "" {
		forward = ForwardAuto
	}
	if !s.HasForward() {
		forward = ""
	}
	if strings.TrimSpace(template) == "" && forward == s.Styles()[0].Forward {
		return StyleDefault
	}
	got := s.effective(template)
	for _, st := range s.Styles()[1:] {
		if got != "" && got == s.effective(st.Direct) && forward == st.Forward {
			return st.ID
		}
	}
	return ""
}

// withoutCover 是混合样式的文字部分：去掉图文排法里的封面（或图片）槽位，卡片上已经有图了。
// 槽位夹在两行中间时连同一个换行一起去掉，不留空行（以前会员购的“封面文字”这样多出过一行空行）。
func withoutCover(text string) string {
	for _, slot := range []string{"$封面", "$图片"} {
		if strings.Contains(text, "\n"+slot+"\n") {
			return strings.Replace(text, "\n"+slot+"\n", "\n", 1)
		}
		if strings.Contains(text, slot) {
			return strings.Replace(text, slot, "", 1)
		}
	}
	return text
}

// withStyles 给每个站点补上样式需要的三个内置预设：卡片、图文、混合（没有就加，已有的同名预设不动）。
//
//	卡片：$卡片 加链接（站点有视频槽位就再带上 $视频）
//	图文：就是这个站点“封面加文字”的排法；内置默认本身是卡片的站点（B 站视频、会员购）用它们已有的“封面文字”
//	混合：没有合并转发形态的站点才需要，卡片后面 $分段 再跟图文的文字部分
func withStyles(in []Site) []Site {
	for i := range in {
		s := &in[i]
		if s.Key == "default" {
			continue
		}
		have := map[string]bool{}
		for _, p := range s.Presets {
			have[p.Name] = true
		}
		add := func(name, src string) {
			if !have[name] {
				s.Presets = append(s.Presets, Preset{Name: name, Source: src})
				have[name] = true
			}
		}
		card := "$卡片$链接"
		if s.Set.has("video") {
			card += "$视频"
		}
		add("卡片", card)
		if s.Key == "x" {
			// 推特卡片（规格-链接发送改版 C1）：一张照 X 网页样子渲染的图加干净链接；$卡片 在 x 站点的推特版式由 C 线实现，这里先登记
			add("推特卡片", "$卡片$链接")
		}
		text, _, err := s.resolveRaw(s.Direct, true) // 保留简介位记号，图文、混合预设也跟着 showDesc
		if err != nil {
			panic("linktemplate: " + s.Key + " 的内置默认写错了：" + err.Error())
		}
		if strings.Contains(text, "$卡片") {
			if text, _, err = s.resolveRaw("@封面文字", true); err != nil {
				panic("linktemplate: " + s.Key + " 默认是卡片，却没有“封面文字”预设")
			}
		}
		add("图文", text)
		if !s.HasForward() {
			add("混合", "$卡片$分段"+withoutCover(text))
		}
	}
	return in
}
