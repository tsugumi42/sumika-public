package linkreader

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 标准库没有聊天文本的 URL 边界；只找起点，URL 本身交给 net/url。
// 同时吃掉未知协议的整个候选，避免从 file://、凭据或外站路径里捡到裸域名。
var linkStart = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://|ｈｔｔｐｓ?：／／|` + wideHosts() + `|` + boothShopHost)

// boothShopHost 是裸写的 BOOTH 店铺子域（单层 [a-z0-9-] 标签，首尾不是连字符）；多级子域靠前一个字符是 "." 被丢掉。
const boothShopHost = `\b[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.booth\.pm[/／]`

var linkOrBare = regexp.MustCompile(linkStart.String() + `|(?i)\b(?:BV[A-Za-z0-9]{10}|av[0-9]+)\b`)

func wideHosts() string {
	hosts := []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com", "mobile.x.com", "fxtwitter.com", "vxtwitter.com", "fixupx.com", "t.co", "bilibili.com", "www.bilibili.com", "m.bilibili.com", "mobile.bilibili.com", "t.bilibili.com", "live.bilibili.com", "space.bilibili.com", "b23.tv", "bili2233.cn"}
	hosts = append(hosts, "mall.bilibili.com", "gf.bilibili.com", "show.bilibili.com", "player.bilibili.com", "bangumi.bilibili.com")
	hosts = append(hosts, "booth.pm", "www.booth.pm")
	hosts = append(hosts, "github.com", "www.github.com", "gist.github.com", "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be", "www.youtu.be", "youtube-nocookie.com", "www.youtube-nocookie.com", "douyin.com", "www.douyin.com", "v.douyin.com", "jx.douyin.com", "m.douyin.com", "iesdouyin.com", "www.iesdouyin.com", "jingxuan.douyin.com", "music.163.com", "y.music.163.com", "163cn.tv", "pixiv.net", "www.pixiv.net", "store.steampowered.com", "steamcommunity.com", "www.steamcommunity.com", "open.spotify.com", "spotify.link", "tiktok.com", "www.tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com")
	var b strings.Builder
	b.WriteString("(?:")
	for i, host := range hosts {
		if i > 0 {
			b.WriteByte('|')
		}
		for _, r := range host {
			b.WriteByte('[')
			if r == '.' {
				b.WriteString(`\.`)
			} else {
				b.WriteRune(r)
			}
			b.WriteRune(r + 0xfee0)
			b.WriteByte(']')
		}
	}
	b.WriteString(")[/／]")
	return b.String()
}

// Extract 按 Clean 去重，保留首次出现的顺序及其原样 Raw。
func Extract(text string) []Link { return ExtractWithBare(text, false) }

// ExtractWithBare 与 URL 共用边界和清洗，裸号不会从凭据或外站路径里另捡出来。
func ExtractWithBare(text string, bare bool) []Link {
	starts := linkStart
	if bare {
		starts = linkOrBare
	}
	var out []Link
	seen := make(map[string]struct{})
	for offset := 0; offset < len(text); {
		m := starts.FindStringIndex(text[offset:])
		if m == nil {
			break
		}
		start, end := offset+m[0], offset+m[1]
		bareID := bare && !strings.ContainsAny(text[start:end], "./：:")
		inQuery := false
		for !bareID && end < len(text) {
			r, n := utf8.DecodeRuneInString(text[end:])
			if boundary(r) || (!inQuery && unicode.Is(unicode.Han, r)) {
				break
			}
			if strings.ContainsRune("?#？＃", r) {
				inQuery = true
			}
			end += n
		}
		offset = end
		if start > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:start])
			if isURLPrefix(prev) {
				continue
			}
		}
		raw := text[start:end]
		if bareID {
			raw = "https://www.bilibili.com/video/" + raw
		}
		// 查询值和片段末尾的标点可能有意义，只裁剪地址末尾。
		if !inQuery {
			raw = strings.TrimRight(raw, ".,;!?")
		}
		if l, ok := Recognize(raw); ok {
			if _, exists := seen[l.Clean]; !exists {
				seen[l.Clean] = struct{}{}
				out = append(out, l)
			}
		}
	}
	return out
}

func boundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("()[]{}<>\"'`\\，。；！、（）【】《》「」『』", r)
}

func isURLPrefix(r rune) bool {
	if r >= '！' && r <= '～' {
		r -= 0xfee0
	}
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/:@%+", r)
}
