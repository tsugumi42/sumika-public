package channel

// 听众（规格-渠道中间件 第四节）：每个批次算一次，内核、记忆、工具的关都原样用。字段照 audience.Audience，不改名；
// 本地主窗口（Channel 是 console、形式是 private）不归这里，调用的一方直接用 audience.Console。
//
// 按发言人算的几处：
//   - OwnerAsked：QQ 群和本地群聊里，取「触发这一轮的那几条」——批里被叫到的那几条；一条都没被叫到（闸门看热闹放行的）取批里最后一条。
//     这几条都是主人发的才为真，有一条是群友发的就为假（混批不算主人在问，默认收紧；和规格-记忆-v2 13.2.1 一致，ADR-186 第 2 条）。
//     私聊里等于 PrivateOK（对面是主人）：主人在私聊里问，本来就是主人在场，不另开口子；
//   - PersonIDs：私聊是对方；群里只取这一批的说话人，不看更早的（记忆的「这个人」只在和他私聊时给，ADR-139）；
//   - PrivateOK：本地（主窗口、本地群聊）为真，QQ 私聊对面是主人为真，QQ 群为假；

import (
	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/memory"
)

// OwnerWho 是主人在 Msg.Who、事件的 actor_id 里的写法（Roster.Resolve 认出主人时给的）
const OwnerWho = "owner"

// AudienceOf 算这一批的可信听众。记忆召回全量，身份仍用于工具授权和表达判断。
func AudienceOf(b types.Batch, r Roster) audience.Audience {
	a := audience.Audience{Channel: b.Conv.Channel, Conversation: b.Conv.String(), Scene: b.Conv.Scene}
	if b.Conv.Scene == types.ScenePrivate {
		if b.Conv.Channel == "console" {
			return audience.Console(a.Conversation)
		}
		partner := r.Resolve(b.Conv.Channel, b.Conv.ID)
		owner := partner.Kind == ActorOwner
		a.PersonIDs = []string{partner.ID}
		a.PrivateOK = owner
		a.OwnerAsked = owner
		return a
	}
	// 群：本地群聊主人在场（词汇表），平台的群主人不一定在
	a.PrivateOK = b.Conv.Channel == "console"
	a.PersonIDs = speakers(b.Msgs)
	a.OwnerAsked = ownerAsked(b.Msgs)
	return a
}

// speakers 是这一批里的说话人，不重复，按先后；她自己的话不算
func speakers(msgs []types.Msg) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Self || seen[m.Who] {
			continue
		}
		seen[m.Who] = true
		out = append(out, m.Who)
	}
	return out
}

// triggers 是触发这一轮的那几条：批里被叫到的；一条都没有（闸门看热闹放行的）取最后一条。她自己的话不算
func triggers(msgs []types.Msg) []types.Msg {
	var called, others []types.Msg
	for _, m := range msgs {
		if m.Self {
			continue
		}
		others = append(others, m)
		if m.Addressed {
			called = append(called, m)
		}
	}
	if len(called) > 0 {
		return called
	}
	if len(others) > 0 {
		return others[len(others)-1:]
	}
	return nil
}

// ownerAsked：触发这一轮的那几条都是主人发的才为真
func ownerAsked(msgs []types.Msg) bool {
	t := triggers(msgs)
	if len(t) == 0 {
		return false
	}
	for _, m := range t {
		if m.Who != OwnerWho {
			return false
		}
	}
	return true
}

// ScopeOf 是这一批的权限档（规格 第四节：记忆包算，中间件只调用）
func ScopeOf(a audience.Audience) string { return memory.ScopeKey(a) }
