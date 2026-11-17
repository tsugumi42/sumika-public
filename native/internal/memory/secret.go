package memory

import "regexp"

// 写前内容过滤（规格-记忆写入上线 W5，起步）：整理要写的事实和片段正文里带密钥形状的就丢，不写，计数 secret_dropped，日志不写正文。
// 理由：主窗口和私聊里贴过的口令、令牌不该被整理成长期记忆，再在别的场合被想起来。只管整理，不管 remember（那是主人亲口让她记的）。
// 宁严勿松：误丢一条普通的话比把口令写进长期记忆便宜得多；写出来的东西 W6 的报告 G 节会再扫一遍，误丢多不多从那里看。

var secretRE = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),                           // OpenAI 一类的 sk- 密钥（也认带连字符、下划线的新写法）
	regexp.MustCompile(`[0-9A-Fa-f]{32,}`),                                 // 32 位以上连续十六进制：哈希、令牌
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                 // AWS 访问密钥 ID
	regexp.MustCompile(`(?i)(密码|password|token|密钥)[\s:：=]*[^\s:：=]\S{5,}`), // "密码 / password / token / 密钥"后面紧跟 6 位以上连续非空白
}

// secretShaped：这段正文里有没有密钥的形状
func secretShaped(text string) bool {
	for _, re := range secretRE {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}
