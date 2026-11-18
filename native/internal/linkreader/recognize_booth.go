package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// BOOTH 店铺子域只允许单层 [a-z0-9-] 标签；多级、下划线和首尾连字符都不是店铺名。
var boothShop = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// 只认站点确实提供的语言段；未知语言不猜，免得把别的页面误认成商品。
var boothLang = map[string]bool{"ja": true, "en": true, "ko": true, "zh-tw": true}

// recognizeBooth 只识别商品页（/items/<id>）；店铺列表、搜索、分类都不在这里认。
// 返回规范化后的语言段与商品 ID；店铺子域和无语言段都归到 ja。
func recognizeBooth(u *url.URL, host string) (lang, id string, ok bool) {
	if host != "booth.pm" && !strings.HasSuffix(host, ".booth.pm") {
		return "", "", false
	}
	if shop := strings.TrimSuffix(host, ".booth.pm"); shop != host && shop != "www" && !boothShop.MatchString(shop) {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	lang = "ja"
	switch {
	case len(parts) == 2 && parts[0] == "items":
	case len(parts) == 3 && parts[1] == "items" && boothLang[strings.ToLower(parts[0])]:
		lang = strings.ToLower(parts[0])
		parts = parts[1:]
	default:
		return "", "", false
	}
	if !positiveID(parts[1]) || len(parts[1]) > 20 {
		return "", "", false
	}
	return lang, parts[1], true
}
