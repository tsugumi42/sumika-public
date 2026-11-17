package types

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
)

// FillCardDisplay 翻译完整卡片的显示字段；实时适配器和旧中立段读取共用，避免从 200 字预览重建 JSON。
func FillCardDisplay(s *Seg) {
	var root cardObject
	if json.Unmarshal([]byte(s.CardJSON), &root) != nil {
		return
	}
	meta := cardObj(root, "meta")
	s.CardTitle = cardText(cardString(root, "title"), 120)
	s.CardDescription = cardText(cardString(root, "desc"), 300)
	s.CardSource = cardText(cardString(root, "app"), 40)
	for _, group := range []string{"detail_1", "news", "music", "video", "miniapp"} {
		node := cardObj(meta, group)
		if s.CardTitle == "" {
			s.CardTitle = cardText(cardString(node, "title"), 120)
		}
		if s.CardDescription == "" {
			s.CardDescription = cardText(cardString(node, "desc"), 300)
		}
		if tag := cardText(cardString(node, "tag"), 40); tag != "" {
			s.CardSource = tag
		}
		if name := cardText(cardString(node, "appname"), 40); name != "" {
			s.CardSource = name
		}
		if s.CardURL == "" {
			for _, key := range []string{"qqdocurl", "jumpUrl", "url", "sourceUrl", "shareUrl", "targetUrl", "musicUrl", "legacyUrl", "pcJumpUrl"} {
				if u := cardURL(cardString(node, key)); u != "" {
					s.CardURL = u
					break
				}
			}
		}
		if s.URL == "" {
			for _, key := range []string{"preview", "image", "cover"} {
				if u := cardURL(cardString(node, key)); u != "" {
					s.URL = u
					break
				}
			}
		}
	}
	if s.CardTitle == "" {
		s.CardTitle = cardText(cardString(root, "prompt"), 120)
	}
}

func cardText(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\ufffc' {
			continue
		}
		if n == limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func cardURL(raw string) string {
	if len(raw) > MediaRefMax || strings.ContainsAny(raw, `\`) || strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) {
		return ""
	}
	return raw
}

type cardObject map[string]json.RawMessage

func cardObj(o cardObject, key string) cardObject {
	var out cardObject
	_ = json.Unmarshal(o[key], &out)
	return out
}

func cardString(o cardObject, key string) string {
	var out string
	_ = json.Unmarshal(o[key], &out)
	return out
}
