package config

import (
	"context"
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

func (ch LinkChange) targets() ([]configEdit, error) {
	path := []string{"linkReader", "expand"}
	var value any
	switch ch.Key {
	case LinkContextEnabled:
		return []configEdit{{path: []string{"linkReader", "context", "enabled"}, value: ch.Bool}}, nil
	case LinkList:
		ids := ch.Conversations
		if ids == nil {
			ids = []string{}
		}
		return []configEdit{{path: append(path, "mode"), value: ch.Mode}, {path: []string{"linkReader", "expand", "conversations"}, value: ids}}, nil
	case LinkEnabled:
		path = append(path, "enabled")
		value = ch.Bool
	case LinkBareBiliID:
		path = append(path, "bareBiliID")
		value = ch.Bool
	case LinkStartEmoji, LinkDoneEmoji, LinkFailEmoji:
		if ch.Text != "" {
			if n, err := strconv.ParseUint(ch.Text, 10, 32); err != nil || n == 0 {
				return nil, errors.New("表情编号须为正整数，留空关闭")
			}
		}
		path = append(path, string(ch.Key))
		value = ch.Text
	case LinkRotateOnReject:
		path = append(path, "rotateOnReject")
		value = ch.Bool
	case LinkPanoramaOriginals:
		path = append(path, "panoramaOriginals")
		value = ch.Bool
	case LinkTweetTextCard:
		path = append(path, "tweetCard", "textOnly")
		value = ch.Bool
	case LinkMaxLinks:
		path = append(path, "maxLinks")
		value = ch.Int
	case LinkImageMaxMB:
		path = append(path, "imageMaxMB")
		value = ch.Int
	case LinkPrivateStatus:
		path = append(path, "privateStatus")
		value = ch.Text
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
	case LinkTranslateEnabled:
		path = append(path, "translate", "enabled")
		value = ch.Bool
	case LinkTranslateTarget:
		if !translateTarget(ch.Text) {
			return nil, errors.New("目标语言名须为1～40字，不含换行、尖括号和花括号")
		}
		path = append(path, "translate", "target")
		value = ch.Text
	case LinkTranslateTimeout:
		if ch.Int < 1 || ch.Int > 60 {
			return nil, errors.New("翻译时限须在1～60秒")
		}
		path = append(path, "translate", "timeoutSeconds")
		value = ch.Int
	case LinkTranslateRetries:
		if ch.Int < 0 || ch.Int > 3 {
			return nil, errors.New("翻译超时重试次数须在0～3次")
		}
		path = append(path, "translate", "retries")
		value = ch.Int
	case LinkTranslateModel:
		if !translateModelOK(ch.Text) {
			return nil, errors.New("模型 ID 不能带首尾空白或换行，最多200字；留空跟后台模型")
		}
		path = append(path, "translate", "model")
		value = ch.Text
	case LinkPlatformTranslate:
		if _, ok := linktemplate.PlatformByID(ch.Site); !ok {
			return nil, errors.New("不认识的网站")
		}
		path = append(path, "platforms", ch.Site, "translate")
		value = ch.Bool
	case LinkVideoEnabled:
		path = append(path, "video", "enabled")
		value = ch.Bool
	case LinkVideoTimeout:
		path = append(path, "video", "timeoutSeconds")
		value = ch.Int
	case LinkVideoMaxMB:
		path = append(path, "video", "maxMB")
		value = ch.Int
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
