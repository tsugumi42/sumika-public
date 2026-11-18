package memview

import (
	"context"
	"fmt"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

const (
	quoteRunes = 200 // 一条原话最多显示多少字
	maxQuoteID = int64(1) << 53
)

// 出处原话（规格-记忆页 3.3）：按（会话, seq）经这个角色的事件来源取，不复制进别处——不进记忆库、日志、缓存、剪贴板；
// 取到的值只在这次返回里，界面关详情或换角色就丢。事件来源（memsource）已经按会话选好平台库、带着"这个角色看见过吗"、撤回、识图简述。
//
// 取不到别的角色没看见的事件：QQ 的会话要这个角色的 observations 里有；本地会话（console:<角色>…）只认自己的。
// 被撤回的不给正文；找不到、没看见过、不是消息一律说"找不到"，不区分原因（不泄露别的角色的会话里有什么）。

func (s *Service) ReadMemoryQuote(ctx context.Context, character string, src api.MemorySource) (api.Result[api.MemoryQuote], error) {
	c, ok := s.byID[character]
	if !ok || memory.ValidCharacterID(character) != nil {
		return failed[api.MemoryQuote](ErrParam)
	}
	conv := src.Conversation
	if conv == "" || len(conv) > 200 || src.Seq <= 0 || src.Seq > maxQuoteID {
		return failed[api.MemoryQuote](ErrParam)
	}
	platform, perr := store.PlatformOfConversation(conv)
	if perr != nil {
		return ready(api.MemoryQuote{Status: "missing"}) // 认不出平台的会话：库里不可能有它
	}
	if c.Events == nil {
		return ready(api.MemoryQuote{Status: "unreadable", Platform: platform})
	}
	var evs []memory.Event
	var readErr error
	if err := s.run(ctx, func(ctx context.Context) error {
		evs, readErr = c.Events.Events(ctx, conv, src.Seq-1, 1)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil // 读不到不是查询失败：那一块照实写"读不到"，别的块照常
	}); err != nil {
		return failed[api.MemoryQuote](err)
	}
	if readErr != nil {
		return ready(api.MemoryQuote{Status: "unreadable", Platform: platform})
	}
	if len(evs) != 1 || evs[0].Seq != src.Seq || evs[0].Conversation != conv || evs[0].Kind != "message" || !seenBy(character, conv, evs[0]) {
		return ready(api.MemoryQuote{Status: "missing"})
	}
	ev := evs[0]
	if ev.Revoked {
		return ready(api.MemoryQuote{Status: "recalled"})
	}
	text := strings.TrimSpace(ev.Text)
	for _, im := range ev.Images {
		if im.Ready && strings.TrimSpace(im.Brief) != "" {
			text += fmt.Sprintf("（图：%s）", strings.TrimSpace(im.Brief))
		}
	}
	clean, cut := memory.CleanText(text, false, quoteRunes)
	if cut > 0 {
		clean += "…"
	}
	return ready(api.MemoryQuote{Status: "ok", Who: s.quoteWho(c, ev), At: ev.At, Text: clean})
}

// seenBy：这个角色看得到这一条吗。本地会话只认自己的（console:<角色>…），别的会话要 observations 里有（事件来源的 Seen）
func seenBy(character, conv string, ev memory.Event) bool {
	if strings.HasPrefix(conv, "console:") {
		parts := strings.Split(conv, ":")
		return len(parts) >= 2 && parts[1] == character
	}
	return ev.Seen
}

// quoteWho：说话人的类型只看 ActorKind 和 ActorID，名字只用来显示
func (s *Service) quoteWho(c Character, ev memory.Event) api.MemoryWho {
	switch {
	case ev.ActorKind == "owner":
		return api.MemoryWho{Kind: "owner", Name: "你"}
	case ev.ActorKind == "character" && ev.ActorID == c.ID:
		return api.MemoryWho{Kind: "self", Name: "她自己"}
	case personRE.MatchString(ev.ActorID):
		name := memory.CleanLine(ev.ActorName, 40)
		if name == "" {
			name = s.name("person", ev.ActorID)
		}
		return api.MemoryWho{Kind: "person", ID: config.MaskID(ev.ActorID), Name: name}
	}
	return api.MemoryWho{Kind: "other", Raw: memory.CleanLine(ev.ActorName, 40)}
}
