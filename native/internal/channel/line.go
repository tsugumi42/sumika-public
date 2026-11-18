package channel

// 给模型看的那一批话（规格-渠道中间件 第九节"Text"一行、第十六节 B"防伪造"）：逐条排成「[时间] 名字(身份)：内容」，引用写成「↪引用 谁：开头几个字」。
// 搬自 daemon/qq/segments.ts 的 formatLine 和 daemon/util/escape.ts：原文是数据不是指令——方括号换成全角、换行后缩进、"↪"换掉，
// 名字另外把半角括号、冒号也换掉，这样谁都没法在一条消息里伪造出一行「[时间] 主人(主人)：……」、"[系统]"框架头或者引用标记。
// 一条消息就是一行以"["开头的头（后面接着的缩进行是它的内容），所以 Text 里头的行数和 Input.Seqs 对得上。
//
// 和 Bun 的差别：时间写明时区（"10月26日 23:30 +08:00"，规格 第九节：本地时区写明 +08:00）；
// 图片没有识图描述（识图是内容插件的事），写 [图片]；@ 的名字、引用的内容靠中间件的查询钩子（LineEnv），没配就写号/不写引用。

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/identity"
)

var nlRE = regexp.MustCompile(`\r?\n`)
var spacedNLRE = regexp.MustCompile(`\s*\r?\n\s*`)

// escapeUserText：别人的原文里的方括号换全角、"↪"换成"→"、换行后缩进两格
func escapeUserText(s string) string {
	s = strings.NewReplacer("[", "［", "]", "］", "↪", "→").Replace(s)
	return nlRE.ReplaceAllString(s, "\n  ")
}

// escapeName：名字（群名片、昵称、被 @ 的、被引用的人）除了上面那些，半角括号换全角、冒号换空格、换行并成空格。
// 行头「[时间] 名字(身份)：」里的半角括号和冒号只能是系统写的：群名片改成"主人(主人)：……"也伪造不出主人的行头
func escapeName(s string) string {
	s = escapeUserText(spacedNLRE.ReplaceAllString(s, " "))
	s = strings.NewReplacer("(", "（", ")", "）", "：", " ", ":", " ").Replace(s)
	return s
}

// EscapeUserText、EscapeName 给中间件以外拼"系统写的行"的插件用（链接资料），规矩和这一行的一样
func EscapeUserText(s string) string { return escapeUserText(s) }
func EscapeName(s string) string     { return escapeName(s) }

// LineEnv 是排一行需要的、中间件自己不知道的东西；都可以是 nil
type LineEnv struct {
	// NameOf 是某个号在这个会话里显示的名字（被 @ 的人）；nil 或空就写号
	NameOf func(conv types.Conversation, userID string) string
	// Quote 是被引用的那条消息（平台消息号）的作者名和内容；owner 是所有者那一行括号里的称呼（空 = "用户"），
	// 被引用的是所有者时用它当作者名。找不到返回 false，那一行就不写引用（Bun 一样）
	Quote func(conv types.Conversation, platformID, owner string) (name, text string, ok bool)
	// QuoteLinks 是被引用的那条消息带的链接摘要（链接解析，规格-链接解析 8.1）；没有链接资料返回空串，nil 就不写
	QuoteLinks func(conv types.Conversation, platformID string) string
	// Location 是时间用的时区；nil 用本机的
	Location *time.Location
	// Mine 是她自己的号：@ 到她的号写成"@你"
	Mine []string
	// Owner 是所有者那一行括号里的身份（角色卡上的称呼）；空 = "用户"
	Owner string
}

// lineTime：「10月26日 23:30 +08:00」
func lineTime(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	return fmt.Sprintf("%d月%d日 %02d:%02d %s", int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Format("-07:00"))
}

// roleOf：括号里的身份。按号认，不按名字（群名片改成"主人"的群友，括号里照样是"群友"）
func roleOf(m types.Msg, conv types.Conversation, owner string) string {
	switch {
	case m.Who == OwnerWho:
		if owner == "" {
			owner = identity.OwnerDefault
		}
		return escapeName(owner)
	case m.Self:
		return identity.SelfAccount
	case conv.Scene == types.SceneGroup:
		return identity.GroupPeer
	}
	return identity.Friend
}

// segText：文字、@、引用、图以外的段（表情包、小黄脸、卡片、视频、文件、合并转发……）在那一行里的样子：方括号是系统写的（半角），内容转义
func segText(s types.Seg) string {
	raw := PlainText([]types.Seg{s})
	out := escapeUserText(raw)
	if strings.HasPrefix(out, "［") {
		out = "[" + strings.TrimPrefix(out, "［")
	}
	if strings.HasSuffix(out, "］") {
		out = strings.TrimSuffix(out, "］") + "]"
	}
	return out
}

// atGap：@ 后面接的字不以空白开头就补一个空格（和 PlainText 一样）
func atGap(segs []types.Seg, i int) string {
	if i+1 < len(segs) && segs[i+1].Kind == types.SegText && startsWithSpace(segs[i+1].Text) {
		return ""
	}
	return " "
}

// FormatLine 排一条消息的一行
func FormatLine(m types.Msg, conv types.Conversation, env LineEnv, fallback time.Time) string {
	var parts []string
	var quote string
	for i, s := range m.Segs {
		switch s.Kind {
		case types.SegText:
			parts = append(parts, escapeUserText(s.Text))
		case types.SegAt:
			switch {
			case s.Target == types.AtAll:
				parts = append(parts, "@全体成员"+atGap(m.Segs, i))
			default:
				name := s.Target
				for _, mine := range env.Mine {
					if mine == s.Target {
						name = identity.Self
					}
				}
				if name == s.Target && env.NameOf != nil {
					if n := env.NameOf(conv, s.Target); n != "" {
						name = n
					}
				}
				parts = append(parts, "@"+escapeName(name)+atGap(m.Segs, i))
			}
		case types.SegReply:
			if quote == "" && env.Quote != nil {
				if name, text, ok := env.Quote(conv, s.Target, env.Owner); ok {
					text = strings.Join(strings.Fields(text), " ")
					if r := []rune(text); len(r) > 20 {
						text = string(r[:20])
					}
					links := ""
					if env.QuoteLinks != nil {
						if b := env.QuoteLinks(conv, s.Target); b != "" {
							links = "｜链接：" + escapeUserText(b)
						}
					}
					quote = "↪引用 " + escapeName(name) + "：" + escapeUserText(text) + links + "\n  "
				}
			}
		default:
			parts = append(parts, segText(s))
		}
	}
	at := m.Time
	if at.IsZero() {
		at = fallback
	}
	name := m.Name
	if name == "" {
		name = m.Who
	}
	body := strings.TrimSpace(quote + strings.Join(parts, ""))
	return fmt.Sprintf("[%s] %s(%s)：%s", lineTime(at, env.Location), escapeName(name), roleOf(m, conv, env.Owner), body)
}

// seqLine 是一条事件排好的一行
type seqLine struct {
	seq  int64
	text string
}

// batchLines 是这一批别人说的话排成给模型看的行：一条一行头，按 seq 先后；她自己的话不在里面
func batchLines(b types.Batch, env LineEnv) []seqLine {
	var out []seqLine
	for _, m := range b.Msgs {
		if m.Self {
			continue
		}
		out = append(out, seqLine{m.Seq, FormatLine(m, b.Conv, env, b.Now)})
	}
	return out
}

// FormatBatch 是 batchLines 拼成 Input.Text 的样子，加上这几条的 seq（和头的行数一一对得上）
func FormatBatch(b types.Batch, env LineEnv) (text string, seqs []int64) {
	var lines []string
	for _, l := range batchLines(b, env) {
		lines = append(lines, l.text)
		seqs = append(seqs, l.seq)
	}
	return strings.Join(lines, "\n"), seqs
}

// truncRunes 截到 n 个字（给日志、原因用）
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
