package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linktemplate"
)

type LinkContext struct {
	Enabled bool `yaml:"enabled"`
}
type LinkReaderConfig struct {
	Context LinkContext `yaml:"context"`
	Proxy   string      `yaml:"proxy"`
	Expand  LinkExpand  `yaml:"expand"`
	// X 是推特取数线路的设置（link_x.go）
	X LinkX `yaml:"x"`
	// FakeIPRanges 是代理 fake-ip 模式的假地址段（CIDR 列表，link_fakeip.go）。默认 netsafe.DefaultFakeIPRanges（IPv4 198.18.0.0/15、IPv6 2001:2::/48 和 fdfe:dcba:9876::/48，ADR-392）；写 [] 表示没有假地址段。收图下载（mediafetch）也读这一项
	FakeIPRanges []string `yaml:"fakeIPRanges"`
	// Rate 是取数自己的节奏：额度用满排队等，平台真拦了才失败（link_rate.go）
	Rate LinkRate `yaml:"rate"`
}
type LinkVideo struct {
	Enabled        bool `yaml:"enabled"`
	TimeoutSeconds int  `yaml:"timeoutSeconds"`
	MaxMB          int  `yaml:"maxMB"`
}

// LinkTranslate 是链接展开的翻译设置：开关只在已开展开的会话里生效（走 Expand 的名单），模型默认是 defaults.backgroundModel，可用 model 单独指定。
type LinkTranslate struct {
	Enabled bool `yaml:"enabled"`
	// Target 是目标语言名，原样写进提示词
	Target string `yaml:"target"`
	// TimeoutSeconds 是单次翻译调用的时限
	TimeoutSeconds int `yaml:"timeoutSeconds"`
	// Retries 是超时后再试几次（每次都按 TimeoutSeconds 计时），都超时就只发原文；0 不重试
	Retries int `yaml:"retries"`
	// Model 是翻译用的模型 ID；空串（默认）跟后台模型（defaults.backgroundModel，按角色覆盖的照旧）。思考档不跟它走，翻译固定关思考
	Model string `yaml:"model"`
}

// 翻译设置的默认值
const (
	DefaultTranslateTarget  = "简体中文"
	DefaultTranslateTimeout = 15 // 用户 2026-11-12：8 秒连十几个字的短句都常超时，改 15 秒
	DefaultTranslateRetries = 1  // 超时再试一次
)

type LinkExpand struct {
	Enabled       bool     `yaml:"enabled"`
	Mode          string   `yaml:"mode"`
	Conversations []string `yaml:"conversations"`
	MaxLinks      int      `yaml:"maxLinks"`
	BareBiliID    bool     `yaml:"bareBiliID"`
	// RotateOnReject：图被协议端拒收时转 180° 重发一次（用户 2026-11-08 要求，默认开）
	RotateOnReject bool `yaml:"rotateOnReject"`
	// PanoramaOriginals：X 拆图拼回成功后，是否把原图放进合并转发附在拼好的图后面（默认关，只发拼好的一张）
	PanoramaOriginals bool `yaml:"panoramaOriginals"`
	// ImageMaxMB：发送前单张图的体积上限（十进制 MB，1～20）。QQ 和 SnowLuma 都查不到权威的单图上限，20 是保守值，也是下载端的上限
	ImageMaxMB    int    `yaml:"imageMaxMB"`
	PrivateStatus string `yaml:"privateStatus"`
	// 三个阶段的表情回应（只在 QQ 群；私聊协议端不支持）：开始 / 完成 / 失败，空串关闭该阶段
	StartEmoji string    `yaml:"startEmoji"`
	DoneEmoji  string    `yaml:"doneEmoji"`
	FailEmoji  string    `yaml:"failEmoji"`
	Video      LinkVideo `yaml:"video"`
	// TweetCard 是推特卡片的设置（link_x.go）
	TweetCard LinkTweetCard `yaml:"tweetCard"`
	// Artist 是画师的推文发原图的设置（link_artist.go，规格-画师原图）
	Artist LinkArtist `yaml:"artist"`
	// Translate 是展开时对帖子正文的翻译（规格-链接翻译）
	Translate LinkTranslate `yaml:"translate"`
	// DirectImageMax 是直接发的图数上限（1～9，默认 3）：X、pixiv 作品、Telegram、B 站动态的图不超过它就和文字同一条消息直发，超过走合并转发
	DirectImageMax int `yaml:"directImageMax"`
	// Text 是文字排版的设置（去推文结尾的 hashtag 等，见 link_text.go）
	Text LinkText `yaml:"text"`
	// Templates 是各站点的消息模板（键 <站点> 或 <站点>-forward；值可写 @预设名）；没写的用内置默认。见 internal/linktemplate
	Templates map[string]string `yaml:"templates"`
	// Sites 是各站点的合并转发和简介设置
	Sites map[string]LinkSite `yaml:"sites"`
	// Platforms 是各网站和页面类型的开关（键是 linktemplate.Catalog 里的网站 ID）；没写的都开。
	Platforms map[string]LinkPlatform `yaml:"platforms"`
	// Forward 是合并转发的节点名和卡片文字（规格-链接解析模板第十四节）；全留空是 QQ 的默认样子
	Forward LinkForward `yaml:"forward"`
	// Send 是发送流程（等回声、核实、实验开关），见 link_send.go
	Send LinkSend `yaml:"send"`
	// Senders 是群里谁发的链接能触发展开：全部 / 只有名单里的人 / 名单里的人除外，可按群单独设（link_senders.go）
	Senders LinkSenders `yaml:"senders"`
}

// LinkForward 是合并转发外观的设置。NodeName 留空用她自己的 QQ 昵称（取不到才用内置名）；头像和号永远是她自己的，不能填别人的。
// 后四项是卡片外面的文字，各是一小段模板（占位符见 linktemplate.CardSet）：Source 标题、News 预览（最多两行）、Summary 底部摘要、Prompt 外显；
// 留空用 QQ 默认，模板里的字段取不到整项退回默认，超过显示长度自动用省略号截断。
type LinkForward struct {
	NodeName string   `yaml:"nodeName"`
	Source   string   `yaml:"source"`
	News     []string `yaml:"news"`
	Summary  string   `yaml:"summary"`
	Prompt   string   `yaml:"prompt"`
}

// Check 校验合并转发外观；错误文字直接给用户看，带键名。
func (f LinkForward) Check() []string {
	var bad []string
	if n := f.NodeName; n != "" {
		switch {
		case utf8.RuneCountInString(n) > linktemplate.NodeNameRunes:
			bad = append(bad, fmt.Sprintf("linkReader.expand.forward.nodeName：最多 %d 个字", linktemplate.NodeNameRunes))
		case strings.IndexFunc(n, func(r rune) bool {
			return r < 0x20 || r == 0x7f || (r >= 0x200b && r <= 0x200f) || r == 0xfffc || r == 0xfeff
		}) >= 0:
			bad = append(bad, "linkReader.expand.forward.nodeName：不能含换行或控制字符")
		}
	}
	one := func(key, src string) {
		if _, err := linktemplate.ParseCard(src); err != nil {
			bad = append(bad, "linkReader.expand.forward."+key+"："+err.Error())
		}
	}
	one("source", f.Source)
	one("summary", f.Summary)
	one("prompt", f.Prompt)
	if len(f.News) > linktemplate.CardNewsLines {
		bad = append(bad, fmt.Sprintf("linkReader.expand.forward.news：最多 %d 行", linktemplate.CardNewsLines))
	}
	for i, src := range f.News {
		one("news["+strconv.Itoa(i)+"]", src)
	}
	return bad
}

// LinkPlatform 是一个网站的开关：Enabled 关掉就整站不展开，Kinds 里写 false 的页面类型不展开；没写的都开。
// 只管“自动展开”，不影响取数给角色看。
type LinkPlatform struct {
	Enabled *bool           `yaml:"enabled"`
	Kinds   map[string]bool `yaml:"kinds"`
	// ImageMode 是这个网站发图的方式（ADR-333）：auto（默认）普通发优先；rotate 直接发并把静态图转 180° 优先；
	// forward 合并转发里转 180° 优先。按阶梯从选定的那一级开始，不成再往后；动图、视频不转，照常发。
	ImageMode string `yaml:"imageMode"`
	// Translate 是这个网站的翻译开关；没写跟随总开关 expand.translate.enabled
	Translate *bool `yaml:"translate"`
}

// 网站发图方式的取值
const (
	ImageModeAuto    = "auto"
	ImageModeRotate  = "rotate"
	ImageModeForward = "forward"
)

// ImageModeOf 说一个网站现在的发图方式（没写、写了不认识的都算 auto）。
func (c LinkExpand) ImageModeOf(platform string) string {
	switch m := c.Platforms[platform].ImageMode; m {
	case ImageModeRotate, ImageModeForward:
		return m
	}
	return ImageModeAuto
}

// PlatformEnabled 说一个网站的总开关开着没有（没写算开）。
func (c LinkExpand) PlatformEnabled(platform string) bool {
	v := c.Platforms[platform].Enabled
	return v == nil || *v
}

// TranslateOn 说一个网站的帖子正文现在要不要翻译：总开关开着，且这个网站没有单独关。
func (c LinkExpand) TranslateOn(platform string) bool {
	if !c.Translate.Enabled {
		return false
	}
	v := c.Platforms[platform].Translate
	return v == nil || *v
}

// KindEnabled 说一种页面现在该不该展开：网站开着、这种类型没被关；别名按清单对到同一类型，清单里没有的类型不由开关管。
func (c LinkExpand) KindEnabled(platform, kind string) bool {
	if !c.PlatformEnabled(platform) {
		return false
	}
	k, ok := linktemplate.LookupKind(platform, kind)
	if !ok {
		return true
	}
	v, set := c.Platforms[platform].Kinds[k.ID]
	return !set || v
}

// LinkSite 是一个站点的发送设置：Forward 为 auto / always / never（空按 auto），
// ForwardOver 是超过多少字折成合并转发（B 站等按字数的站点），ForwardOverBytes 是实际发出的文字超过多少 UTF-8 字节折成合并转发（x、telegram），
// DescLimit 是简介最多多少字；没写就用站点的内置默认，0 表示不限。
type LinkSite struct {
	Forward          string `yaml:"forward"`
	ForwardOver      *int   `yaml:"forwardOver"`
	ForwardOverBytes *int   `yaml:"forwardOverBytes"`
	DescLimit        *int   `yaml:"descLimit"`
	// ShowDesc 为真时内置默认模板带上简介那一行（默认关；只有内置默认去掉过简介的站点能开，R-219）
	ShowDesc bool `yaml:"showDesc"`
}

func DefaultLinks() LinkReaderConfig {
	return LinkReaderConfig{Context: LinkContext{Enabled: true}, Expand: LinkExpand{Mode: "whitelist", Conversations: []string{}, MaxLinks: 3, RotateOnReject: true, ImageMaxMB: 20, StartEmoji: "289", DoneEmoji: "124", FailEmoji: "9", PrivateStatus: "开始解析链接……", Video: LinkVideo{TimeoutSeconds: 60, MaxMB: 100}, TweetCard: LinkTweetCard{Theme: TweetThemeAuto, MaxHeight: DefaultTweetCardMaxHeight}, Artist: DefaultLinkArtist(), Translate: LinkTranslate{Enabled: true, Target: DefaultTranslateTarget, TimeoutSeconds: DefaultTranslateTimeout, Retries: DefaultTranslateRetries}, DirectImageMax: DefaultDirectImageMax, Text: DefaultLinkText(), Send: DefaultLinkSend(), Senders: defaultLinkSenders()}, X: defaultLinkX(), FakeIPRanges: DefaultFakeIPRanges(), Rate: defaultLinkRate()}
}
func (c LinkReaderConfig) Clone() LinkReaderConfig {
	c.Expand.Conversations = slices.Clone(c.Expand.Conversations)
	c.Rate = c.Rate.clone()
	c.Expand.Senders = c.Expand.Senders.clone()
	if c.FakeIPRanges != nil {
		c.FakeIPRanges = slices.Clone(c.FakeIPRanges)
	}
	c.Expand.Templates = maps.Clone(c.Expand.Templates)
	c.Expand.Sites = maps.Clone(c.Expand.Sites)
	c.Expand.Platforms = maps.Clone(c.Expand.Platforms)
	c.Expand.Forward.News = slices.Clone(c.Expand.Forward.News)
	for k, v := range c.Expand.Platforms {
		v.Kinds = maps.Clone(v.Kinds)
		c.Expand.Platforms[k] = v
	}
	return c
}

// checkTemplates 校验各站点的模板和发送设置；错误文字直接给用户看，指出是哪个键、第几个字。
func (c LinkExpand) checkTemplates() []string {
	var bad []string
	for _, key := range slices.Sorted(maps.Keys(c.Templates)) {
		site, _, ok := linktemplate.SiteByKey(key)
		if !ok {
			bad = append(bad, "linkReader.expand.templates."+key+"：不认识的站点或形态")
			continue
		}
		if src := c.Templates[key]; strings.TrimSpace(src) != "" {
			if _, err := site.Parse(src); err != nil {
				bad = append(bad, "linkReader.expand.templates."+key+"："+err.Error())
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.Sites)) {
		site, forward, ok := linktemplate.SiteByKey(key)
		v := c.Sites[key]
		switch {
		case !ok || forward:
			bad = append(bad, "linkReader.expand.sites."+key+"：不认识的站点")
		case v.Forward != "" && v.Forward != linktemplate.ForwardAuto && v.Forward != linktemplate.ForwardAlways && v.Forward != linktemplate.ForwardNever:
			bad = append(bad, "linkReader.expand.sites."+key+".forward：只能为 auto / always / never")
		case v.Forward == linktemplate.ForwardAlways && !site.HasForward():
			bad = append(bad, "linkReader.expand.sites."+key+".forward：这个站点没有合并转发形态，只能写 auto 或 never")
		case v.ForwardOver != nil && (*v.ForwardOver < 0 || *v.ForwardOver > 100000):
			bad = append(bad, "linkReader.expand.sites."+key+".forwardOver：须在 0～100000")
		case v.ForwardOverBytes != nil && (*v.ForwardOverBytes < 1000 || *v.ForwardOverBytes > 17500):
			bad = append(bad, "linkReader.expand.sites."+key+".forwardOverBytes：须在 1000～17500 字节（0 不按长度折已取消：过小的值会让节点封顶 80 个、正文被截）")
		case v.ForwardOverBytes != nil && site.ForwardOverBytes == 0:
			bad = append(bad, "linkReader.expand.sites."+key+".forwardOverBytes：这个站点按字数判断（用 forwardOver），只有 x 和 telegram 按字节")
		case v.ShowDesc && !site.HasDescOption():
			bad = append(bad, "linkReader.expand.sites."+key+".showDesc：这个站点的默认模板本来就没有简介那一行（可以开的站点见 sumika.example.yml）")
		case v.DescLimit != nil && (*v.DescLimit < 0 || *v.DescLimit > 100000):
			bad = append(bad, "linkReader.expand.sites."+key+".descLimit：须在 0～100000")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(c.Platforms)) {
		p, ok := linktemplate.PlatformByID(id)
		if !ok {
			bad = append(bad, "linkReader.expand.platforms."+id+"：不认识的网站")
			continue
		}
		if m := c.Platforms[id].ImageMode; m != "" && m != ImageModeAuto && m != ImageModeRotate && m != ImageModeForward {
			bad = append(bad, "linkReader.expand.platforms."+id+".imageMode：只能为 auto / rotate / forward")
		}
		for _, kind := range slices.Sorted(maps.Keys(c.Platforms[id].Kinds)) {
			if !slices.ContainsFunc(p.Kinds, func(k linktemplate.CatalogKind) bool { return k.ID == kind }) {
				bad = append(bad, "linkReader.expand.platforms."+id+".kinds."+kind+"：这个网站没有这种页面类型")
			}
		}
	}
	return bad
}

func (c LinkExpand) Allows(conv types.Conversation) bool {
	if !c.Enabled || !ValidLinkConversation(conv.String()) {
		return false
	}
	included := slices.Contains(c.Conversations, conv.String())
	return (c.Mode == "whitelist" && included) || (c.Mode == "blacklist" && !included)
}
func ValidLinkConversation(key string) bool {
	c, e := types.ParseConversation(key)
	if e != nil || c.Channel != "qq" {
		return false
	}
	valid := func(s string) bool {
		n, e := strconv.ParseUint(s, 10, 64)
		return e == nil && n > 0 && strconv.FormatUint(n, 10) == s
	}
	return valid(c.ID) && (c.Scene == types.SceneGroup || valid(c.Self))
}

// translateTarget 校验目标语言名：它会原样写进提示词，不许带换行和会破坏提示词结构的符号。
func translateTarget(s string) bool {
	if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > 40 {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '<' || r == '>' || r == '{' || r == '}' })
}

// translateModelOK 校验翻译模型 ID：空串是跟后台模型；不带首尾空白和控制字符，最多 200 字
func translateModelOK(m string) bool {
	return m == strings.TrimSpace(m) && strings.IndexFunc(m, unicode.IsControl) < 0 && utf8.RuneCountInString(m) <= 200
}
func textSetting(s string) bool {
	// 不带换行等控制字符：这些文字会原样进审批卡，换行能在卡上伪造“来自”“你说的”之类的行
	return strings.TrimSpace(s) != "" && strings.IndexFunc(s, unicode.IsControl) < 0 && utf8.RuneCountInString(s) <= 200
}
func (c LinkReaderConfig) check() []string {
	e := c.Expand
	var bad []string
	if e.Mode != "whitelist" && e.Mode != "blacklist" {
		bad = append(bad, "linkReader.expand.mode：只能为 whitelist / blacklist")
	}
	if e.MaxLinks < 1 || e.MaxLinks > 20 {
		bad = append(bad, "linkReader.expand.maxLinks：必须在1～20")
	}
	if e.ImageMaxMB < 1 || e.ImageMaxMB > 20 {
		bad = append(bad, "linkReader.expand.imageMaxMB：必须在1～20 MB")
	}
	if e.Video.TimeoutSeconds < 1 || e.Video.TimeoutSeconds > 600 || e.Video.MaxMB < 1 || e.Video.MaxMB > 1000 {
		bad = append(bad, "linkReader.expand.video：时限1～600秒、大小1～1000 MB")
	}
	if t := e.Translate; t.TimeoutSeconds < 1 || t.TimeoutSeconds > 60 {
		bad = append(bad, "linkReader.expand.translate.timeoutSeconds：必须在1～60秒")
	} else if t.Retries < 0 || t.Retries > 3 {
		bad = append(bad, "linkReader.expand.translate.retries：必须在0～3次")
	} else if !translateTarget(t.Target) {
		bad = append(bad, "linkReader.expand.translate.target：目标语言名必须为1～40字，不含换行、NUL、尖括号和花括号")
	}
	if !translateModelOK(e.Translate.Model) {
		bad = append(bad, "linkReader.expand.translate.model：模型 ID 不带首尾空白、换行等控制字符，最多200字；留空跟后台模型")
	}
	bad = append(bad, c.X.check()...)
	bad = append(bad, c.checkFakeIPRanges()...)
	bad = append(bad, c.Rate.check()...)
	bad = append(bad, e.Senders.check()...)
	bad = append(bad, e.TweetCard.check()...)
	bad = append(bad, e.Artist.check()...)
	bad = append(bad, e.checkTemplates()...)
	bad = append(bad, e.checkDirectImageMax()...)
	bad = append(bad, e.Forward.Check()...)
	bad = append(bad, e.Send.Check()...)
	if !textSetting(e.PrivateStatus) {
		bad = append(bad, "linkReader.expand.privateStatus：状态文字必须为1～200字，不含换行等控制字符")
	}
	for key, v := range map[string]string{"startEmoji": e.StartEmoji, "doneEmoji": e.DoneEmoji, "failEmoji": e.FailEmoji} {
		if v == "" {
			continue
		}
		if n, err := strconv.ParseUint(v, 10, 32); err != nil || n == 0 {
			bad = append(bad, "linkReader.expand."+key+"：须为平台正整数编号，留空关闭")
		}
	}
	seen := map[string]bool{}
	for _, id := range e.Conversations {
		if !ValidLinkConversation(id) || seen[id] {
			bad = append(bad, "linkReader.expand.conversations：存在非法或重复会话")
		}
		seen[id] = true
	}
	return bad
}
func (c *LinkReaderConfig) UnmarshalYAML(b []byte) error {
	type plain LinkReaderConfig
	p := plain(*c)
	if e := yaml.Unmarshal(b, &p); e != nil {
		return e
	}
	var raw map[string]any
	if e := yaml.Unmarshal(b, &raw); e != nil {
		return e
	}
	checks := []struct {
		path string
		kind string
	}{{"context.enabled", "bool"}, {"expand.enabled", "bool"}, {"expand.bareBiliID", "bool"}, {"expand.rotateOnReject", "bool"}, {"expand.panoramaOriginals", "bool"}, {"expand.video.enabled", "bool"}, {"expand.translate.enabled", "bool"}, {"expand.translate.timeoutSeconds", "int"}, {"expand.translate.retries", "int"}, {"expand.translate.model", "string"}, {"expand.maxLinks", "int"}, {"expand.imageMaxMB", "int"}, {"expand.video.timeoutSeconds", "int"}, {"expand.video.maxMB", "int"}, {"expand.conversations", "list"}, {"x.fxTimeoutSeconds", "int"}, {"x.syndication", "bool"}, {"fakeIPRanges", "list"}}
	for _, k := range linkSendKinds {
		checks = append(checks, struct {
			path string
			kind string
		}{k[0], k[1]})
	}
	for _, ch := range checks {
		var value any = raw
		present := true
		for _, key := range strings.Split(ch.path, ".") {
			mapping, ok := value.(map[string]any)
			if !ok {
				return errors.New("linkReader 配置结构无效")
			}
			value, present = mapping[key]
			if !present {
				break
			}
		}
		if !present {
			continue
		}
		valid := false
		switch ch.kind {
		case "bool":
			_, valid = value.(bool)
		case "int":
			switch value.(type) {
			case int, int64, uint64:
				uintValue := fmt.Sprint(value)
				_, err := strconv.ParseInt(uintValue, 10, 32)
				valid = err == nil
			}
		case "string":
			_, valid = value.(string)
		case "list":
			if a, ok := value.([]any); ok {
				valid = true
				for _, v := range a {
					if _, ok := v.(string); !ok {
						valid = false
					}
				}
			}
		}
		if !valid {
			return fmt.Errorf("linkReader.%s 类型错误", ch.path)
		}
	}
	*c = LinkReaderConfig(p)
	return nil
}
