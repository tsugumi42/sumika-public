package web

import "strings"

// 人机校验页、拦截页的常见标志。只在内容很短时才看：真正的长文章里偶然出现这些词不算被拦。
var blockedMarkers = []string{
	"just a moment", "checking your browser", "cf-browser-verification", "cf-challenge", "attention required",
	"verify you are human", "are you a robot", "enable javascript and cookies", "access denied",
	"captcha", "请完成安全验证", "安全验证", "正在验证您是否是真人", "人机验证", "访问验证", "请开启 javascript", "请启用 javascript",
}

// 超过这么多字就不当校验页：校验页本身都很短。
const blockedMaxRunes = 1500

// LooksBlocked 判断抓回来的内容是不是人机校验或拦截页，而不是正文。
func LooksBlocked(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" || len([]rune(text)) > blockedMaxRunes {
		return false
	}
	lower := strings.ToLower(text)
	for _, m := range blockedMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
