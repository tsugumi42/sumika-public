package memory

import (
	"context"
	"slices"
	"strings"

	"sumika-native/internal/channel/types"
)

// 后台整理只碰允许的场合（规格-记忆写入上线 W5）。放行的名单是配置 memory.write.places，这里是名单的取值和"这个会话在不在名单里"的判断。
// 判断放在 SummarizeBatch 里（唯一的咽喉：排程、压缩前、整理按钮、影子整理工具都经过它），排程那一侧另有一层 memoryFor 提前挡，
// 为的是不立游标、不空转；两层的判断是同一个函数，不各写一份。

// 写入场合的取值（memory.write.places 的值）。owner-private 是平台私聊的一种：QQ 里主人自己的私聊
const (
	WritePlaceOwnerPrivate    = "owner-private"
	WritePlaceLocalPrivate    = "local-private"
	WritePlaceLocalGroup      = "local-group"
	WritePlacePlatformPrivate = "platform-private" // 所有人的 QQ 私聊，包括别人发来的
	WritePlacePlatformGroup   = "platform-group"
)

// AllWritePlaces 是五个取值（新切片）。测试和影子整理工具要"什么场合都整理"时显式传它
func AllWritePlaces() []string {
	return []string{WritePlaceOwnerPrivate, WritePlaceLocalPrivate, WritePlaceLocalGroup, WritePlacePlatformPrivate, WritePlacePlatformGroup}
}

// DefaultWritePlaces 是 Options.WritePlaces 没给（nil）时的名单：只整理主人自己的 QQ 私聊（我定的，规格-记忆写入上线 Q2）。
// 非 nil 的空切片是什么场合都不整理
func DefaultWritePlaces() []string { return []string{WritePlaceOwnerPrivate} }

// ValidWritePlace：配置里的取值认不认
func ValidWritePlace(s string) bool { return slices.Contains(AllWritePlaces(), s) }

// writePlaces 是这个实例放行的名单（nil 用默认）
func (m *Memory) writePlaces() []string {
	if m.opt.WritePlaces == nil {
		return DefaultWritePlaces()
	}
	return m.opt.WritePlaces
}

// convPlace 认会话 id 的形状并给出场合。渠道只认 console（本地）和 qq，别的渠道一律认不出（现在只记录，不整理）；
// 形状按真实的格式校验，校验不过也认不出，不让一个陌生的、不带 ":group:" 的会话 id 被当成平台私聊默认放行（PlaceOf 只看字符串里有没有 ":group:"）：
//   - qq 群 "qq:group:群号"、qq 私聊 "qq:private:她的号:对方的号"：号都是数字串，过 types.Conversation.Valid；
//   - 本地：主窗口 "console:<角色>"、话题 "console:<角色>:topic:<话题>"（turn/hub.go、store/display_conversations.go 的真实格式）；
//     本地群聊 "console:group:<名>" 和 "console:<角色>:group:<名>"（PlaceOf 把带 ":group:" 的 console 会话算本地群聊，目前没有哪里产生它们）。
//
// 最后必须和 PlaceOf 算出的场合一致：整理算范围、选听众用的是 PlaceOf，这里放行的不能和它说的不是一回事
func convPlace(conv string) (Place, bool) {
	channel, rest, ok := strings.Cut(conv, ":")
	if !ok || rest == "" {
		return "", false
	}
	var place Place
	parts := strings.Split(rest, ":")
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return "", false
		}
	}
	switch channel {
	case "console":
		switch {
		case len(parts) == 1:
			place = LocalPrivate
		case len(parts) == 3 && parts[1] == "topic":
			place = LocalPrivate
		case len(parts) == 2 && parts[0] == "group", len(parts) == 3 && parts[1] == "group":
			place = LocalGroup
		default:
			return "", false
		}
	case "qq":
		c := types.Conversation{Channel: channel}
		switch {
		case len(parts) == 2 && parts[0] == types.SceneGroup:
			c.Scene, c.ID = types.SceneGroup, parts[1]
			place = PlatformGroup
		case len(parts) == 3 && parts[0] == types.ScenePrivate:
			c.Scene, c.Self, c.ID = types.ScenePrivate, parts[1], parts[2]
			place = PlatformPrivate
		default:
			return "", false
		}
		if c.Valid() != nil || !digits(c.ID) || (c.Self != "" && !digits(c.Self)) || c.String() != conv {
			return "", false
		}
	default:
		return "", false
	}
	if PlaceOf(channel, conv, "") != place {
		return "", false
	}
	return place, true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// writePlaceOf：会话落在哪个取值；平台私聊先按"所有人的私聊"算（它在不在名单里要等知道是不是主人的）
func writePlaceOf(p Place) string {
	switch p {
	case LocalPrivate:
		return WritePlaceLocalPrivate
	case LocalGroup:
		return WritePlaceLocalGroup
	case PlatformGroup:
		return WritePlacePlatformGroup
	}
	return WritePlacePlatformPrivate
}

// PlaceMayWrite：这个会话的场合在不在放行名单里，只看会话 id，不读库。
// 平台私聊是例外地"先放行"：名单里有 owner-private 而没有 platform-private 时，是不是主人的私聊要读事件才知道（PlaceAllows / SummarizeBatch 接着判）。
// 认不出的会话（陌生渠道、形状不对）一律 false
func (m *Memory) PlaceMayWrite(conv string) bool { return mayWrite(m.writePlaces(), conv) }

// mayWrite：会话的场合在不在 places 里（只看会话 id）。catchUp 要问"某份旧名单下放不放行"，所以名单是参数
func mayWrite(places []string, conv string) bool {
	place, ok := convPlace(conv)
	if !ok {
		return false
	}
	if place == PlatformPrivate {
		return slices.Contains(places, WritePlacePlatformPrivate) || slices.Contains(places, WritePlaceOwnerPrivate)
	}
	return slices.Contains(places, writePlaceOf(place))
}

// ownerPrivateOnly：平台私聊要不要再判"是不是主人的"（名单里有 owner-private、没有 platform-private）
func (m *Memory) ownerPrivateOnly() bool { return ownerPrivateOnlyIn(m.writePlaces()) }

func ownerPrivateOnlyIn(places []string) bool {
	return slices.Contains(places, WritePlaceOwnerPrivate) && !slices.Contains(places, WritePlacePlatformPrivate)
}

// PlaceAllows 比 PlaceMayWrite 多一步：名单里只有 owner-private 时，平台私聊要读一次"她在这个会话里最近看到的人话"，是主人说的才放行
// （和 privateSide 的兜底同一个判断；认不出对方是谁按"不是"算，收紧）。排程用它决定要不要立游标、要不要排批，省得别人的私聊白占名额。
// 读不出来返回 false 和错误，由调用方记日志
func (m *Memory) PlaceAllows(ctx context.Context, conv string) (bool, error) {
	return m.placeAllowed(ctx, m.writePlaces(), conv)
}

// placeAllowed 是 PlaceAllows 在任意一份名单下的版本
func (m *Memory) placeAllowed(ctx context.Context, places []string, conv string) (bool, error) {
	if !mayWrite(places, conv) {
		return false, nil
	}
	if place, _ := convPlace(conv); place != PlatformPrivate || !ownerPrivateOnlyIn(places) {
		return true, nil
	}
	es, err := m.events()
	if err != nil {
		return false, err
	}
	ev, ok, err := es.LastSaid(ctx, conv)
	if err != nil {
		return false, err
	}
	return ok && ev.Conversation == conv && ev.ActorKind == "owner", nil
}

// skipPlace：这一批因为场合没放行而不整理。游标不动、不调模型、不写库；计数 place_skipped，日志每个会话只记一次，
// 而且不写会话 id（里面有号码，B16），只写场合的取值
func (w *writer) skipPlace(conv string, counts counter, shape string) {
	counts.add(CountPlaceSkipped)
	if _, seen := w.placeSeen.LoadOrStore(conv, true); !seen {
		w.logf("记忆[%s]：有一个会话的场合没放行，不整理它（原因 place_skipped，场合 %s）", w.m.opt.Character, shape)
	}
}
