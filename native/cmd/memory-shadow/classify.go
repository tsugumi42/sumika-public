package main

// 会话认场合，以及 -places 放行哪些（规格 W6 技术路线第 5 点）。
//
// 两道门：第一道是 W5 的 SummarizeBatch 场合门（Memory.PlaceMayWrite、SkipPlace），-places 的名单原样交给它（Options.WritePlaces）；
// 这里是工具侧的第二道，只会更窄、不会更宽：渠道只认 console、qq，会话 id 的形状认不出的不放行，
// "主人的私聊"和 W5 的 PlaceAllows 同一个判断（她在这个会话里最近看到的人话是主人说的），认不出对方是谁一律按"别人的私聊"算。
// 第一道门单独就拦得住（shadow_test.go 的 TestFirstGateAloneStopsOtherPlaces 关掉这一道再试），
// 这一道只是不让一个没被 W5 认出的会话进到统计和人数里；"放行的是 W5 门放行的子集"由 TestToolGateIsSubsetOfMemoryGate 钉住

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sumika-native/internal/memory"
)

// -places 的取值：就是 W5 的 memory.write.places 的那五个名字，不另写一份
const (
	placeOwnerPrivate    = memory.WritePlaceOwnerPrivate    // QQ 里主人自己的私聊
	placeLocalPrivate    = memory.WritePlaceLocalPrivate    // 本地私聊
	placeLocalGroup      = memory.WritePlaceLocalGroup      // 本地群聊
	placePlatformPrivate = memory.WritePlacePlatformPrivate // 所有人的 QQ 私聊，包括别人发来的
	placePlatformGroup   = memory.WritePlacePlatformGroup   // QQ 群
)

var allPlaces = memory.AllWritePlaces()

// defaultPlaces：不给 -places 时放行什么，就是 W5 的 memory.write.places 默认（Q2：只有主人自己的 QQ 私聊）；
// 不是"窗口里的每个会话"：真实聊天里有 QQ 群和别人的私聊，要显式加才跑
var defaultPlaces = memory.DefaultWritePlaces()

func parsePlaces(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return append([]string(nil), defaultPlaces...), nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if !memory.ValidWritePlace(p) {
			return nil, fmt.Errorf("-places 里的 %q 不认识（认 %s）", p, strings.Join(allPlaces, "、"))
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// convKind 是一个会话属于哪种场合
type convKind int

const (
	kindUnknown convKind = iota // 渠道不认识、形状不对：永远不放行
	kindLocalPrivate
	kindLocalGroup
	kindQQOwnerPrivate
	kindQQOtherPrivate
	kindQQGroup
)

var kindOrder = []convKind{kindLocalPrivate, kindLocalGroup, kindQQOwnerPrivate, kindQQOtherPrivate, kindQQGroup, kindUnknown}

func (k convKind) String() string {
	switch k {
	case kindLocalPrivate:
		return "本地私聊"
	case kindLocalGroup:
		return "本地群聊"
	case kindQQOwnerPrivate:
		return "QQ 主人的私聊"
	case kindQQOtherPrivate:
		return "QQ 别人的私聊"
	case kindQQGroup:
		return "QQ 群"
	}
	return "认不出的会话"
}

// allowed：-places 放不放行这一种。platform-private 放行所有 QQ 私聊（含主人的）；owner-private 只放主人的
func (k convKind) allowed(places []string) bool {
	has := func(p string) bool {
		for _, x := range places {
			if x == p {
				return true
			}
		}
		return false
	}
	switch k {
	case kindLocalPrivate:
		return has(placeLocalPrivate)
	case kindLocalGroup:
		return has(placeLocalGroup)
	case kindQQOwnerPrivate:
		return has(placeOwnerPrivate) || has(placePlatformPrivate)
	case kindQQOtherPrivate:
		return has(placePlatformPrivate)
	case kindQQGroup:
		return has(placePlatformGroup)
	}
	return false
}

// 翻一个会话的事件最多翻多少条（自检认对方是谁、片段里有没有主人的行用）
const ownerScanCap = 4000

// classifyConv 按会话 id 的形状和（QQ 私聊）会话里的行认场合。qq 私聊的形状是 qq:private:<她的号>:<对方的号>，群是 qq:group:<群号>，
// 号都是纯数字；本地的形状见 consoleKind。形状认不出的一律 kindUnknown。
// 形状规则是照 W5 的 convPlace 另写的一份（故意不共用：第二道门要独立），只会更窄；TestToolGateIsSubsetOfMemoryGate 对着一批怪形状钉住"不比它宽"
func classifyConv(ctx context.Context, es memory.EventSource, conv string) (convKind, error) {
	switch memory.ChannelOf(conv) {
	case "console":
		return consoleKind(conv), nil
	case "qq":
		parts := strings.Split(conv, ":")
		switch {
		case len(parts) == 3 && parts[1] == "group" && isDigits(parts[2]):
			return kindQQGroup, nil
		case len(parts) == 4 && parts[1] == "private" && isDigits(parts[2]) && isDigits(parts[3]):
			owner, err := ownerSaidLast(ctx, es, conv)
			if err != nil {
				return kindUnknown, err
			}
			if owner {
				return kindQQOwnerPrivate, nil
			}
			return kindQQOtherPrivate, nil
		}
	}
	return kindUnknown, nil
}

// consoleKind：本地会话 id 的形状（turn/hub.go、store/display_conversations.go 的真实格式）：主窗口 console:<角色>、话题 console:<角色>:topic:<话题>，
// 本地群聊 console:group:<名>、console:<角色>:group:<名>。每一段不许空、不许带首尾空白；别的形状认不出
func consoleKind(conv string) convKind {
	parts := strings.Split(strings.TrimPrefix(conv, "console:"), ":")
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return kindUnknown
		}
	}
	switch {
	case len(parts) == 1, len(parts) == 3 && parts[1] == "topic":
		return kindLocalPrivate
	case len(parts) == 2 && parts[0] == "group", len(parts) == 3 && parts[1] == "group":
		return kindLocalGroup
	}
	return kindUnknown
}

// isDigits：非空的纯数字串（QQ 号、群号）
func isDigits(s string) bool {
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

// ownerSaidLast：她在这个会话里最近看到的人话是不是主人说的（ActorKind 是 owner，身份按类型认，不看名字）。
// 和 W5 的 Memory.PlaceAllows 是同一个判断，不比它松：会话里主人早先说过话、但最近一句是别人说的，按"别人的私聊"算；
// 读不出、没有人话、事件不属于这个会话，都按"不是"算
func ownerSaidLast(ctx context.Context, es memory.EventSource, conv string) (bool, error) {
	ev, ok, err := es.LastSaid(ctx, conv)
	if err != nil {
		return false, err
	}
	return ok && ev.Conversation == conv && ev.ActorKind == "owner", nil
}

// speakersSince 列出这个会话里 afterSeq 之后说过话的不同的人（真人：主人算一个，群友 / 好友按 ActorID；她自己和别的角色不算）。最多翻 50000 条
func speakersSince(ctx context.Context, es memory.EventSource, conv string, afterSeq int64) ([]string, error) {
	people := map[string]bool{}
	after := afterSeq
	for scanned := 0; scanned < 50_000; {
		evs, err := es.Events(ctx, conv, after, 500)
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			after = e.Seq
			if e.Origin == "owner" || e.Origin == "peer" {
				people[e.ActorKind+"/"+e.ActorID] = true
			}
		}
		scanned += len(evs)
		if len(evs) < 500 {
			break
		}
	}
	out := make([]string, 0, len(people))
	for p := range people {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// sessionInfo 是一个会话在这次运行里的情况
type sessionInfo struct {
	Conv      string
	Kind      convKind
	Allowed   bool
	HasCursor bool
	Cursor    int64 // 运行前的游标（没有游标是 0）
	LastSeq   int64
	Behind    int64    // 游标落后最新的 seq 数（Conversations 的最新 seq 减游标；没有游标是 0）
	People    []string // 游标之后说过话的真人（主人和群友 / 好友的身份，不是名字）
	Batches   int      // 这次跑了几批（有模型调用或游标前进的）
	CapHit    bool     // 到 -max-batches 就停了，后面还有
	NotRun    bool     // 花费上限到了，这个会话一批也没跑
	// PlaceSkipped：工具侧放行了，W5 的场合门（SummarizeBatch 里）又拦下了：一批也没整理，游标没动
	PlaceSkipped bool
	Err          string
	LastAt       int64
}

// kindCounts 按场合汇总放行的会话数、落后的条数、涉及的人数
type kindCount struct {
	Kind     convKind
	Sessions int
	Allowed  int
	Behind   int64
	People   int // 不同的人数（同一个人在几个会话里都说过话只算一次）
}

func summarizeKinds(ss []sessionInfo) []kindCount {
	m := map[convKind]*kindCount{}
	people := map[convKind]map[string]bool{}
	for _, s := range ss {
		c := m[s.Kind]
		if c == nil {
			c = &kindCount{Kind: s.Kind}
			m[s.Kind] = c
			people[s.Kind] = map[string]bool{}
		}
		c.Sessions++
		if s.Allowed {
			c.Allowed++
			c.Behind += s.Behind
			for _, p := range s.People {
				people[s.Kind][p] = true
			}
		}
	}
	var out []kindCount
	for _, k := range kindOrder {
		if c := m[k]; c != nil {
			c.People = len(people[k])
			out = append(out, *c)
		}
	}
	return out
}
