package app

import (
	"context"
	"encoding/json"
	"strings"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/identity"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/persona"
)

// ownerCall 是排消息头、整理记忆时叫所有者什么：读角色卡上的 ownerName，没写、读不出来是"用户"
func (a *App) ownerCall(character string) string {
	return persona.OwnerCallFor(a.paths.CharactersDir(), character)
}

// lineEnv 同时交给入库和回合重排；名字是 Bun 的启动快照，引用按当前会话从 Go 库查询。
func (a *App) lineEnv() channel.LineEnv {
	return channel.LineEnv{
		NameOf: func(conv types.Conversation, uid string) string {
			group := ""
			if conv.Scene == types.SceneGroup {
				group = conv.ID
			}
			return a.people.Name(group, uid)
		},
		Quote:      a.quote,
		QuoteLinks: a.quoteLinks,
	}
}

// quoteLinks 是被引用那条的链接摘要；给她看关着（没装链接插件）就不查
func (a *App) quoteLinks(conv types.Conversation, platformID string) string {
	if a.links == nil {
		return ""
	}
	st, err := a.set.For(conv.Channel)
	if err != nil {
		return ""
	}
	ref, ok, err := st.EventByPlatformID(conv.Channel, conv.String(), platformID)
	if err != nil || !ok {
		return ""
	}
	notes, err := st.QuotedLinkNotes(context.Background(), conv.String(), ref.Seq)
	if err != nil {
		a.logf("引用的链接资料查询失败：%v", err)
		return ""
	}
	return linkplugin.QuoteBrief(notes)
}

func (a *App) quote(conv types.Conversation, platformID, owner string) (name, text string, ok bool) {
	st, err := a.set.For(conv.Channel)
	if err != nil {
		a.logf("引用查询取库失败：%v", err)
		return "", "", false
	}
	ref, ok, err := st.EventByPlatformID(conv.Channel, conv.String(), platformID)
	if err != nil {
		a.logf("引用查询失败：%v", err)
		return "", "", false
	}
	if !ok {
		return "", "", false
	}
	row, ok, err := st.EventRow(conv.String(), ref.Seq)
	if err != nil {
		a.logf("引用事件查询失败：%v", err)
		return "", "", false
	}
	if !ok {
		return "", "", false
	}
	switch ref.ActorKind {
	case "owner":
		if owner == "" {
			owner = identity.OwnerDefault
		}
		name = owner
	case "character":
		name = ref.ActorID
	default:
		uid := strings.TrimPrefix(ref.ActorID, conv.Channel+":")
		group := ""
		if conv.Scene == types.SceneGroup {
			group = conv.ID
		}
		name = a.people.Name(group, uid)
		if name == "" {
			var seg struct {
				From struct {
					Nick string `json:"nick"`
					Card string `json:"card"`
				} `json:"from"`
			}
			if row.Segments.Valid && json.Unmarshal([]byte(row.Segments.String), &seg) == nil {
				name = seg.From.Card
				if name == "" {
					name = seg.From.Nick
				}
			}
		}
		if name == "" {
			name = uid
		}
	}
	return name, row.Text, true
}
