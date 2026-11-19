package config

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/linktemplate"
)

type LinkKey string

const (
	LinkContextEnabled    LinkKey = "contextEnabled"
	LinkEnabled           LinkKey = "enabled"
	LinkList              LinkKey = "list"
	LinkMaxLinks          LinkKey = "maxLinks"
	LinkBareBiliID        LinkKey = "bareBiliID"
	LinkRotateOnReject    LinkKey = "rotateOnReject"
	LinkPanoramaOriginals LinkKey = "panoramaOriginals"
	LinkStartEmoji        LinkKey = "startEmoji"
	LinkDoneEmoji         LinkKey = "doneEmoji"
	LinkFailEmoji         LinkKey = "failEmoji"
	LinkPrivateStatus     LinkKey = "privateStatus"
	LinkTemplate          LinkKey = "template"
	LinkSiteForward       LinkKey = "siteForward"
	LinkSiteForwardOver   LinkKey = "siteForwardOver"
	LinkSiteForwardBytes  LinkKey = "siteForwardBytes"
	LinkSiteDescLimit     LinkKey = "siteDescLimit"
	LinkSiteShowDesc      LinkKey = "siteShowDesc"    // Site 填站点键，Bool 是“显示简介”开关（R-219）
	LinkSiteStyle         LinkKey = "siteStyle"       // Site 填模板站点键，Text 填样式 ID（default / card / text / mixed）；一次写模板和合并转发两项
	LinkPlatformEnabled   LinkKey = "platformEnabled" // Site 填网站 ID，Bool 是开关
	LinkKindEnabled       LinkKey = "kindEnabled"     // Site 填网站 ID，Text 填页面类型，Bool 是开关
	LinkPlatformImage     LinkKey = "platformImage"   // Site 填网站 ID，Text 填发图方式（auto / rotate / forward）
	LinkTranslateEnabled  LinkKey = "translateEnabled"
	LinkTranslateTarget   LinkKey = "translateTarget"
	LinkTranslateTimeout  LinkKey = "translateTimeout"
	LinkTranslateRetries  LinkKey = "translateRetries"
	LinkTranslateModel    LinkKey = "translateModel"
	LinkPlatformTranslate LinkKey = "platformTranslate" // Site 填网站 ID，Bool 是这个网站的翻译开关
	LinkForwardCard       LinkKey = "forwardCard"       // Card 是合并转发的节点名和卡片文字，五项一次写
	LinkVideoEnabled      LinkKey = "videoEnabled"
	LinkVideoTimeout      LinkKey = "videoTimeout"
	LinkVideoMaxMB        LinkKey = "videoMaxMB"
	LinkImageMaxMB        LinkKey = "imageMaxMB"
	LinkTweetCardTheme    LinkKey = "tweetCardTheme"     // auto / light / dark（tweetCard.check 查）
	LinkTweetCardMaxH     LinkKey = "tweetCardMaxHeight" // 0 不封顶或 480～8000 像素（tweetCard.check 查）
	LinkDirectImageMax    LinkKey = "directImageMax"
	LinkStripHashtags     LinkKey = "stripTrailingHashtags"
	LinkArtistMode        LinkKey = "artistMode"
	LinkArtistAutoDetect  LinkKey = "artistAutoDetect"
	LinkEchoWait          LinkKey = "echoWaitMS"
	LinkHistoryCheck      LinkKey = "historyCheck"
	LinkForwardVideo      LinkKey = "forwardVideo"
	LinkForwardQuoteReply LinkKey = "forwardQuoteReply"
	LinkTweetTextCard     LinkKey = "tweetTextCard" // 纯文字推文画推特卡片（tweetCard.textOnly，ADR-399）
)

type LinkChange struct {
	Key           LinkKey
	Bool          bool
	Int           int
	Text, Mode    string
	Site          string // 站点键（模板、转发、简介设置用）
	Card          LinkForward
	Conversations []string
}

// linkValueKind 是单值链接键的值放在 LinkChange 的哪个字段
type linkValueKind int

const (
	linkBool linkValueKind = iota
	linkInt
	linkText
)

// linkScalar 是只写一个值的链接键。path 相对 linkReader，设置登记里的键名就是 "linkReader." 加 path（LinkChangeFor 按它反查），
// 所以设置页和设置工具走同一条写入路径。取值范围统一由 Parse 的 check 查；check 只给几项更好懂的报错，先于写文件。
type linkScalar struct {
	path  string
	kind  linkValueKind
	check func(LinkChange) error
}

var linkScalars = map[LinkKey]linkScalar{
	LinkContextEnabled:    {path: "context.enabled", kind: linkBool},
	LinkEnabled:           {path: "expand.enabled", kind: linkBool},
	LinkBareBiliID:        {path: "expand.bareBiliID", kind: linkBool},
	LinkStartEmoji:        {path: "expand.startEmoji", kind: linkText, check: checkLinkEmoji},
	LinkDoneEmoji:         {path: "expand.doneEmoji", kind: linkText, check: checkLinkEmoji},
	LinkFailEmoji:         {path: "expand.failEmoji", kind: linkText, check: checkLinkEmoji},
	LinkRotateOnReject:    {path: "expand.rotateOnReject", kind: linkBool},
	LinkPanoramaOriginals: {path: "expand.panoramaOriginals", kind: linkBool},
	LinkTweetTextCard:     {path: "expand.tweetCard.textOnly", kind: linkBool},
	LinkMaxLinks:          {path: "expand.maxLinks", kind: linkInt},
	LinkImageMaxMB:        {path: "expand.imageMaxMB", kind: linkInt},
	LinkPrivateStatus:     {path: "expand.privateStatus", kind: linkText},
	LinkTranslateEnabled:  {path: "expand.translate.enabled", kind: linkBool},
	LinkTranslateTarget: {path: "expand.translate.target", kind: linkText, check: func(ch LinkChange) error {
		if !translateTarget(ch.Text) {
			return errors.New("目标语言名须为1～40字，不含换行、尖括号和花括号")
		}
		return nil
	}},
	LinkTranslateTimeout: {path: "expand.translate.timeoutSeconds", kind: linkInt, check: func(ch LinkChange) error {
		if ch.Int < 1 || ch.Int > 60 {
			return errors.New("翻译时限须在1～60秒")
		}
		return nil
	}},
	LinkTranslateRetries: {path: "expand.translate.retries", kind: linkInt, check: func(ch LinkChange) error {
		if ch.Int < 0 || ch.Int > 3 {
			return errors.New("翻译超时重试次数须在0～3次")
		}
		return nil
	}},
	LinkTranslateModel: {path: "expand.translate.model", kind: linkText, check: func(ch LinkChange) error {
		if !translateModelOK(ch.Text) {
			return errors.New("模型 ID 不能带首尾空白或换行，最多200字；留空跟后台模型")
		}
		return nil
	}},
	LinkVideoEnabled: {path: "expand.video.enabled", kind: linkBool},
	LinkVideoTimeout: {path: "expand.video.timeoutSeconds", kind: linkInt},
	LinkVideoMaxMB:   {path: "expand.video.maxMB", kind: linkInt},
	// 以下各项除推特卡片两项外，范围只由 Parse 的 check 查
	LinkTweetCardTheme: {path: "expand.tweetCard.theme", kind: linkText, check: func(ch LinkChange) error { return checkTweetCard("推特卡片配色", LinkTweetCard{Theme: ch.Text}) }},
	LinkTweetCardMaxH: {path: "expand.tweetCard.maxHeight", kind: linkInt, check: func(ch LinkChange) error {
		return checkTweetCard("推特卡片高度封顶", LinkTweetCard{MaxHeight: ch.Int})
	}},
	LinkDirectImageMax:    {path: "expand.directImageMax", kind: linkInt},
	LinkStripHashtags:     {path: "expand.text.stripTrailingHashtags", kind: linkBool},
	LinkArtistMode:        {path: "expand.artist.mode", kind: linkText},
	LinkArtistAutoDetect:  {path: "expand.artist.autoDetect", kind: linkBool},
	LinkEchoWait:          {path: "expand.send.echoWaitMS", kind: linkInt},
	LinkHistoryCheck:      {path: "expand.send.historyCheck", kind: linkBool},
	LinkForwardVideo:      {path: "expand.send.forwardVideo", kind: linkBool},
	LinkForwardQuoteReply: {path: "expand.send.forwardQuoteReply", kind: linkBool},
}

// checkTweetCard 用推特卡片自己的检查（只填了要写的那一项，另一项是合法的零值），报错把键名换成设置页上的叫法
func checkTweetCard(name string, t LinkTweetCard) error {
	if bad := t.check(); len(bad) > 0 {
		_, msg, _ := strings.Cut(bad[0], "：")
		return errors.New(name + "：" + msg)
	}
	return nil
}

func checkLinkEmoji(ch LinkChange) error {
	if ch.Text != "" {
		if n, err := strconv.ParseUint(ch.Text, 10, 32); err != nil || n == 0 {
			return errors.New("表情编号须为正整数，留空关闭")
		}
	}
	return nil
}

// linkScalarKeys 是设置登记键名到 LinkKey 的反查表
var linkScalarKeys = func() map[string]LinkKey {
	m := make(map[string]LinkKey, len(linkScalars))
	for k, f := range linkScalars {
		m["linkReader."+f.path] = k
	}
	return m
}()

// LinkChangeFor 把设置登记里的单值链接键和它的 JSON 值换成 LinkChange；ok 为 false 是这张表里没有这个键。
func LinkChangeFor(key string, raw json.RawMessage) (ch LinkChange, ok bool, err error) {
	k, ok := linkScalarKeys[key]
	if !ok {
		return ch, false, nil
	}
	ch.Key = k
	var target any = &ch.Text
	switch linkScalars[k].kind {
	case linkBool:
		target = &ch.Bool
	case linkInt:
		target = &ch.Int
	}
	if json.Unmarshal(raw, target) != nil {
		return ch, true, errors.New("设置值类型不正确")
	}
	return ch, true, nil
}

func (ch LinkChange) targets() ([]configEdit, error) {
	if f, ok := linkScalars[ch.Key]; ok {
		if f.check != nil {
			if err := f.check(ch); err != nil {
				return nil, err
			}
		}
		var value any = ch.Text
		switch f.kind {
		case linkBool:
			value = ch.Bool
		case linkInt:
			value = ch.Int
		}
		return []configEdit{{path: append([]string{"linkReader"}, strings.Split(f.path, ".")...), value: value}}, nil
	}
	path := []string{"linkReader", "expand"}
	var value any
	switch ch.Key {
	case LinkList:
		ids := ch.Conversations
		if ids == nil {
			ids = []string{}
		}
		return []configEdit{{path: append(path, "mode"), value: ch.Mode}, {path: []string{"linkReader", "expand", "conversations"}, value: ids}}, nil
	case LinkTemplate:
		site, _, ok := linktemplate.SiteByKey(ch.Site)
		if !ok {
			return nil, errors.New("不认识的站点")
		}
		if strings.TrimSpace(ch.Text) != "" {
			if _, err := site.Parse(ch.Text); err != nil {
				return nil, err
			}
		}
		path = append(path, "templates", ch.Site)
		value = ch.Text
	case LinkSiteForward, LinkSiteForwardOver, LinkSiteForwardBytes, LinkSiteDescLimit:
		site, forward, ok := linktemplate.SiteByKey(ch.Site)
		if !ok || forward {
			return nil, errors.New("不认识的站点")
		}
		switch ch.Key {
		case LinkSiteForward:
			if ch.Text != linktemplate.ForwardAuto && ch.Text != linktemplate.ForwardAlways && ch.Text != linktemplate.ForwardNever {
				return nil, errors.New("合并转发只能是 auto / always / never")
			}
			if ch.Text == linktemplate.ForwardAlways && !site.HasForward() {
				return nil, errors.New("这个站点没有合并转发形态，只能选自动或从不")
			}
			path = append(path, "sites", ch.Site, "forward")
			value = ch.Text
		case LinkSiteForwardBytes:
			if site.ForwardOverBytes == 0 {
				return nil, errors.New("这个站点按字数判断，用 forwardOver")
			}
			if ch.Int < 1000 || ch.Int > 17500 {
				return nil, errors.New("字节门槛须在 1000～17500")
			}
			path = append(path, "sites", ch.Site, "forwardOverBytes")
			value = ch.Int
		default:
			if ch.Int < 0 || ch.Int > 100000 {
				return nil, errors.New("数值须在 0～100000")
			}
			name := "forwardOver"
			if ch.Key == LinkSiteDescLimit {
				name = "descLimit"
			}
			path = append(path, "sites", ch.Site, name)
			value = ch.Int
		}
	case LinkSiteShowDesc:
		site, forward, ok := linktemplate.SiteByKey(ch.Site)
		if !ok || forward {
			return nil, errors.New("不认识的站点")
		}
		if ch.Bool && !site.HasDescOption() {
			return nil, errors.New("这个站点的默认模板本来就没有简介那一行")
		}
		path = append(path, "sites", ch.Site, "showDesc")
		value = ch.Bool
	case LinkSiteStyle:
		site, forward, ok := linktemplate.SiteByKey(ch.Site)
		if !ok || forward {
			return nil, errors.New("不认识的站点")
		}
		st, ok := site.StyleByID(ch.Text)
		if !ok {
			return nil, errors.New("这个站点没有这种样式")
		}
		edits := []configEdit{{path: append(slices.Clone(path), "templates", ch.Site), value: st.Direct}}
		if st.Forward != "" {
			edits = append(edits, configEdit{path: append(slices.Clone(path), "sites", ch.Site, "forward"), value: st.Forward})
		}
		return edits, nil
	case LinkPlatformEnabled, LinkKindEnabled:
		p, ok := linktemplate.PlatformByID(ch.Site)
		if !ok {
			return nil, errors.New("不认识的网站")
		}
		path = append(path, "platforms", ch.Site)
		if ch.Key == LinkPlatformEnabled {
			path = append(path, "enabled")
		} else {
			if !slices.ContainsFunc(p.Kinds, func(k linktemplate.CatalogKind) bool { return k.ID == ch.Text }) {
				return nil, errors.New("这个网站没有这种页面类型")
			}
			path = append(path, "kinds", ch.Text)
		}
		value = ch.Bool
	case LinkPlatformImage:
		if _, ok := linktemplate.PlatformByID(ch.Site); !ok {
			return nil, errors.New("不认识的网站")
		}
		if ch.Text != ImageModeAuto && ch.Text != ImageModeRotate && ch.Text != ImageModeForward {
			return nil, errors.New("发图方式只能为 auto / rotate / forward")
		}
		path = append(path, "platforms", ch.Site, "imageMode")
		value = ch.Text
	case LinkForwardCard:
		if bad := ch.Card.Check(); len(bad) > 0 {
			return nil, errors.New(strings.TrimPrefix(bad[0], "linkReader.expand.forward."))
		}
		news := slices.Clone(ch.Card.News)
		if news == nil {
			news = []string{}
		}
		base := append(slices.Clone(path), "forward")
		edit := func(name string, v any) configEdit {
			return configEdit{path: append(slices.Clone(base), name), value: v}
		}
		return []configEdit{edit("nodeName", ch.Card.NodeName), edit("source", ch.Card.Source), edit("news", news), edit("summary", ch.Card.Summary), edit("prompt", ch.Card.Prompt)}, nil
	case LinkPlatformTranslate:
		if _, ok := linktemplate.PlatformByID(ch.Site); !ok {
			return nil, errors.New("不认识的网站")
		}
		path = append(path, "platforms", ch.Site, "translate")
		value = ch.Bool
	default:
		return nil, errors.New("链接设置不允许写这个键")
	}
	return []configEdit{{path: path, value: value}}, nil
}
func SaveLink(ctx context.Context, file, history, base string, ch LinkChange, now time.Time) (Config, error) {
	return saveConfig(ctx, file, history, base, now, func(raw []byte) ([]byte, Config, error) { return PatchLink(raw, file, ch) })
}
func PatchLink(raw []byte, file string, ch LinkChange) ([]byte, Config, error) {
	edits, e := ch.targets()
	if e != nil {
		return nil, Config{}, e
	}
	return patchSettings(raw, file, edits)
}
