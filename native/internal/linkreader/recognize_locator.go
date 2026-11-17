package linkreader

import (
	"net/url"
	"strings"
)

// 把“定位”参数（分 P、时间点）从原链接带进改写后的规范链接。
// Recognize 把活动页、嵌入播放器、b23.tv 的编号短码改写成正式页面时，原来的查询串整串不要，
// 但 p、t 这些决定“看哪一段”的参数丢了就等于改了内容（保留表见 tracking.go 的 locatorKeys）。
//
// keys 的写法：“p” 原样带；“p:page” 表示原链接里叫 page、正式链接里叫 p。值只认数字，
// 其余一律不带，免得把别的东西混进干净链接。
func biliLocator(u *url.URL, keys ...string) string {
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	var out []string
	for _, k := range keys {
		to, from, renamed := strings.Cut(k, ":")
		if !renamed {
			from = to
		}
		v := q[from]
		if len(v) != 1 || !digits(v[0]) || len(v[0]) > 12 {
			continue
		}
		if to == "p" && v[0] == "1" {
			continue // 和不写一样，见 isBiliFirstPart
		}
		out = append(out, to+"="+v[0])
	}
	return strings.Join(out, "&")
}

// withLocator 给 recognizeBiliExtra 的返回值追加定位查询串；没认出来或没有定位参数时原样返回。
func withLocator(l Link, ok, handled bool) func(loc string) (Link, bool, bool) {
	return func(loc string) (Link, bool, bool) {
		if ok && loc != "" {
			l.Clean += "?" + loc
		}
		return l, ok, handled
	}
}
