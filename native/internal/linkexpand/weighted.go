package linkexpand

import (
	"regexp"
	"strings"

	"sumika-native/internal/assets/emoji"
)

// WeightedLength 是推特官方（twitter-text）的加权长度：码点落在配置 v3 的 weight 100 区间算 1，其余算 2；
// 一个表情序列（含肤色、零宽连接的组合）算 2；一条 http(s) 链接固定算 23，不管多长。
// 出处：github.com/twitter/twitter-text 的 config/v3.json（defaultWeight 200、transformedURLLength 23、scale 100、
// 区间 0–4351、8192–8205、8208–8223、8242–8247，即 U+0000–U+10FF、U+2000–U+200D、U+2010–U+201F、U+2032–U+2037）
// 和 js/src/parseTweet.js 的逐字符累加（链接、表情整体计数，其余按区间取权重）。
// 和官方的差别：不做 NFC 规范化；链接只认 http(s)://（不认没有协议头的域名）；表情用本项目 emoji.Split 的切法。
// 只用于 X、Telegram 的“帖子长不长”判断（规格-链接发送改版 B13）：中文一字算 2，英文一个字母算 1，和推特的“280”同一把尺。
func WeightedLength(s string) int {
	n := 0
	for _, p := range emoji.Split(s) {
		if p.Emoji {
			n += tweetDefaultWeight
			continue
		}
		n += weightedPlain(p.Text)
	}
	return n
}

// asciiURLRe 认链接：http(s):// 后面连着的可打印 ASCII；遇到空白或中日文就停（“见https://x.invalid/a，后面的字”不把后面的字吞进链接）。
var asciiURLRe = regexp.MustCompile(`https?://[!-~]+`)

const (
	tweetDefaultWeight = 2
	tweetURLWeight     = 23
)

// weightedPlain 算一段不含表情的文字：先把链接换成固定长度，其余逐码点取权重。
func weightedPlain(s string) int {
	n := 0
	for {
		loc := asciiURLRe.FindStringIndex(s)
		if loc == nil {
			break
		}
		url := strings.TrimRight(s[loc[0]:loc[1]], linkPunct) // 紧贴链接的句读不算链接（官方的链接识别同样不吃结尾标点）
		n += runesWeight(s[:loc[0]]) + tweetURLWeight
		s = s[loc[0]+len(url):]
	}
	return n + runesWeight(s)
}

func runesWeight(s string) int {
	n := 0
	for _, r := range s {
		if r <= 0x10FF || r >= 0x2000 && r <= 0x200D || r >= 0x2010 && r <= 0x201F || r >= 0x2032 && r <= 0x2037 {
			n++
		} else {
			n += tweetDefaultWeight
		}
	}
	return n
}
