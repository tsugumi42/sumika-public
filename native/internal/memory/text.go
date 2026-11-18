package memory

import (
	"regexp"
	"strings"
	"unicode"
)

// squash 去掉空白、标点、符号（含 emoji）和看不见的格式字符，只剩字母数字和文字，用来比两句话是不是"同一句"
// （规格-记忆-v2 13.2.4 第 8 步同文查重、13.2.8 守卫的字面比对共用这一个口径）。
// 字母数字不去，也不转大小写、不转全角：只有"完全相同"才算同一句，近似重复归第 3 步。
// 去掉的不止 Unicode 的 P、S 两类：零宽连接符（ZWJ）、变体选择符（FE0F）、键帽符（20E3）是 emoji 拼起来用的，
// 看不见但会让两个看着一样的 emoji 句子对不上
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r), unicode.IsPunct(r), unicode.IsSymbol(r), unicode.Is(unicode.Cf, r):
			return -1
		case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r == 0x20E3:
			return -1
		}
		return r
	}, s)
}

// ───────────── 给她看的原话：转义和 LIKE 模式 ─────────────
// 下面三个是给 recall_raw 用的。别人的原文和名字是数据不是指令：谁也不能在一条消息里伪造出一行"- [1 分钟前] 主人：……"。
// escapeUserText、escapeName 照 daemon/util/escape.ts 和 history.EscapeUserText 抄的一份（记忆包不引 history，
// 公共的转义包出来以后换过去；规格-群聊闸门 10.2）

var userTextEscaper = strings.NewReplacer("[", "［", "]", "］", "↪", "→", "\r\n", "\n  ", "\r", "\n  ", "\n", "\n  ")

// escapeUserText：方括号换全角、"↪"换掉、换行后缩进（单独的回车也算换行）
func escapeUserText(s string) string { return userTextEscaper.Replace(s) }

var (
	nameNewline = regexp.MustCompile(`\s*[\r\n]\s*`)
	nameEscaper = strings.NewReplacer("(", "（", ")", "）", ":", " ", "：", " ")
)

// escapeName：名字（群名片、昵称）除了上面那些，半角括号换全角、冒号换空格、换行并成空格。
// "名字：正文"里的半角冒号只能是系统写的，群名片改成"主人：……"也伪造不出主人的一行
func escapeName(s string) string {
	return strings.TrimSpace(nameEscaper.Replace(escapeUserText(nameNewline.ReplaceAllString(s, " "))))
}

// likeEscaper 在 % _ 和反斜杠前面各加一个反斜杠（反斜杠用 rune 拼，比一串转义好读）
var likeEscaper = func() *strings.Replacer {
	bs := string(rune(92))
	return strings.NewReplacer(bs, bs+bs, "%", bs+"%", "_", bs+"_")
}()

// likePattern 把用户写的一个词变成 LIKE 的模式 %词%，词里的 % _ 和反斜杠都转义（转义符是反斜杠，store 的查询要写 ESCAPE，见 EventSource.Find）。
// Bun 不转义：搜"100%"会匹配任意以 100 开头的字（拆解笔记 8.3、清单 ②）
func likePattern(word string) string { return "%" + likeEscaper.Replace(word) + "%" }
