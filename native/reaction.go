package main

// 表情回应的显示（主人 2026-11-02："消息的表情回应的显示还没做"；"原来放时间的地方以后应该是放表情回应的"）：
// 被贴的那条消息的气泡下面一排小药丸，同一个表情合并成一个，写个数，悬停看是谁贴的。
// 数据：后台已经把回应连到被贴的那条上（api.MessageView.Reactions，store/display_reactions.go），界面不看通知行。
// 认人按号（ActorID），不按显示名：两个人同名各算一个，同一个人把同一个表情贴两次只算一次。
// 取消回应：后台现在没存（snowluma.reaction 把取消当别的通知丢掉），所以个数只增不减，等后台存了再减（待办）。
// 药丸绘制和时间尾部占位交给 content.Reactions，宿主只解析 QQ 表情和悬停名单。

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// reactionGroup 同一个表情的所有回应
type reactionGroup struct {
	Ref  string
	Who  []string // 显示名，按先贴的先排；同一个人只出现一次
	Seen map[string]bool
}

// Count 这个表情被几个人贴了
func (g reactionGroup) Count() int { return len(g.Who) }

// groupReactions 按表情合并，表情按第一次出现的先后排；同一个人（ActorID）同一个表情只算一次。
// 没有表情编号的不算（认不出是哪个）
func groupReactions(rs []api.ReactionView) []reactionGroup {
	var out []reactionGroup
	at := map[string]int{}
	for _, r := range rs {
		if r.Ref == "" {
			continue
		}
		i, ok := at[r.Ref]
		if !ok {
			i = len(out)
			at[r.Ref] = i
			out = append(out, reactionGroup{Ref: r.Ref, Seen: map[string]bool{}})
		}
		g := &out[i]
		who := r.ActorKind + "\x00" + r.ActorID // 认人按号；没有号的（协议端没说是谁）每条算一个人，不并
		if r.ActorID == "" {
			who = "\x00" + r.ID
		}
		if g.Seen[who] {
			continue
		}
		g.Seen[who] = true
		g.Who = append(g.Who, reactorName(r))
	}
	return out
}

// reactorName 悬停里写的名字：后台给的显示名；查不到时后台退回的是带 qq: 前缀的号，去掉前缀只写号
func reactorName(r api.ReactionView) string {
	n := strings.TrimSpace(r.DisplayName)
	if n == "" {
		n = r.ActorID
	}
	n = strings.TrimPrefix(n, "qq:")
	if n == "" {
		return "某人"
	}
	return n
}

// unicodeReaction 表情编号是 Unicode 码位的十进制（0x2000～0x10FFFF，除去代理区）时返回那个字符。
// 阈值和网页版一致（规格-原生界面第二版第三节："数字在 0x2000 到 0x10FFFF 之间当码位"）；小黄脸的编号最大 515，到不了
func unicodeReaction(ref string) (string, bool) {
	n, err := strconv.Atoi(ref)
	if err != nil || n < 0x2000 || n > 0x10FFFF || n >= 0xD800 && n <= 0xDFFF {
		return "", false
	}
	return string(rune(n)), true
}

// reactionTip 悬停的说明：谁贴的。人多了只列前几个
func reactionTip(who []string) string {
	const show = 12
	if len(who) <= show {
		return strings.Join(who, "、")
	}
	return strings.Join(who[:show], "、") + "，等 " + strconv.Itoa(len(who)) + " 人"
}

// 逐项组合才能把悬停名单绑定到正确的表情；尾部占位独立参与最后一行折行。
func (a *app) reactionFooter(c *ui.Context, m *msg, tail ui.Span, outside bool) {
	t := theme.T(c)
	ui.Row(c).Wrap().Gap(t.Metrics().Reaction().Gap()).MinWidth(0).Label("表情回应").Children(func() {
		for _, g := range m.reacts {
			item := content.Reaction{ID: g.Ref, Count: g.Count()}
			if emoji, ok := unicodeReaction(g.Ref); ok {
				item.Emoji = emoji
			} else {
				f := a.fs()
				res := resolveFace(segview.FaceRef{ID: g.Ref}, f.table, f.state)
				if res.Kind == faceAsImage {
					f.want(g.Ref)
					item.Image = f.peek(g.Ref)
				} else {
					item.Emoji = res.Text
				}
			}
			pill := content.Reactions(c.Key(g.Ref), []content.Reaction{item}, content.ReactionOptions{Appearance: content.Appearance{Outgoing: m.mine}, InBubble: !outside, Outside: outside}).Tooltip(reactionTip(g.Who))
			a.probeOf("react:"+m.id+":"+g.Ref, pill)
		}
		if tail.ObjectWidth > 0 {
			ui.Box(c).Width(max(0, tail.ObjectWidth-t.Metrics().Reaction().InBubbleShift()-t.Metrics().Reaction().Gap())).Height(tail.ObjectHeight).Shrink(0).Margin(-t.Metrics().Reaction().Gap(), 0, 0)
		}
	})
}
