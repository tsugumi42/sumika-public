package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// entry 是清单里的一条：链接和它上面那行注释（# 平台/页面类型/变体 [来源]）。
// 注释是目录和编号的数据源；没有注释的链接按识别结果归类，变体写“（清单没写）”。
type entry struct {
	URL      string
	Platform string // 注释里写的平台（取数后以取数结果为准）
	Kind     string
	Variant  string
	From     string // 来源：库 / 旧清单 / 新找
	Code     string // 编号，如 B-07
}

func readList(path string) ([]entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseList(string(b)), nil
}

// parseList 读清单：# 开头是注释，紧跟在注释后面的链接拿这条注释当标签；连续几行注释只认最后一行。
func parseList(text string) []entry {
	var out []entry
	var pending string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			pending = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		e := entry{URL: line}
		e.Platform, e.Kind, e.Variant, e.From = parseLabel(pending)
		pending = ""
		out = append(out, e)
	}
	numberEntries(out)
	return out
}

// parseLabel 拆“平台/类型/变体 [来源]”。变体里还可能有斜杠，只切前两个。格式不对的整行当变体。
func parseLabel(s string) (platform, kind, variant, from string) {
	if s == "" {
		return "", "", "", ""
	}
	if i := strings.LastIndex(s, "["); i >= 0 && strings.HasSuffix(s, "]") {
		from = strings.TrimSpace(s[i+1 : len(s)-1])
		s = strings.TrimSpace(s[:i])
	}
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 3 || strings.ContainsAny(parts[0], " \t") || strings.ContainsAny(parts[1], " \t") {
		return "", "", s, from
	}
	return parts[0], parts[1], strings.TrimSpace(parts[2]), from
}

// platformCodes 是编号前缀；没列的平台取英文名前两个字母大写。
var platformCodes = map[string]string{
	"bilibili": "B", "x": "X", "pixiv": "P", "afdian": "AF", "bluesky": "BS", "booth": "BO", "douyin": "DY",
	"github": "GH", "mastodon": "MA", "misskey": "MK", "netease": "NE", "spotify": "SP", "steam": "ST",
	"telegram": "TG", "tiktok": "TT", "xiaoheihe": "XH", "xiaohongshu": "XHS", "youtube": "YT",
}

func codeFor(platform string) string {
	if c, ok := platformCodes[platform]; ok {
		return c
	}
	if platform == "" {
		return "U"
	}
	r := []rune(strings.ToUpper(platform))
	if len(r) > 2 {
		r = r[:2]
	}
	return string(r)
}

// numberEntries 按平台在清单里出现的顺序编号：B-01、B-02……同前缀的不同平台共用一个计数，编号不会撞。
func numberEntries(es []entry) {
	count := map[string]int{}
	for i := range es {
		c := codeFor(es[i].Platform)
		count[c]++
		es[i].Code = fmt.Sprintf("%s-%02d", c, count[c])
	}
}

// shown 是页面上显示的链接：保留路径和普通参数（?p=12 这类变体要看得出来），去掉片段和看着像签名、令牌的参数。
func shown(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			lk := strings.ToLower(k)
			for _, bad := range []string{"token", "sign", "auth", "xsec", "sec_", "session", "key", "share_", "spm", "vd_source"} {
				if strings.Contains(lk, bad) {
					q.Del(k)
					break
				}
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}
