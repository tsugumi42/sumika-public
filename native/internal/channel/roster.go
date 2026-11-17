package channel

import (
	"fmt"
	"strings"
)

// 事件上 actor_kind 的三个值（daemon/api/event.ts ActorKind）：这条是"从谁的号来"的
const (
	ActorOwner     = "owner"     // 主人
	ActorCharacter = "character" // 角色自己的号
	ActorHuman     = "human"     // 别的人（群友、私聊的朋友）
)

// 事件上 origin 的取值里入站会用到的几个（daemon/api/event.ts Origin；automation:* 和 character 是出站写的，不在这里）
const (
	OriginOwner    = "owner"    // 主人说的
	OriginPeer     = "peer"     // 别人说的
	OriginHuman    = "human"    // 真人登录她的号手动发的
	OriginExternal = "external" // 以前别的程序（MaiBot）用这个号说的
)

// Actor 是一条消息的发言人认出来的样子
type Actor struct {
	Kind string // ActorOwner / ActorCharacter / ActorHuman
	ID   string // 主人是 "owner"，角色是角色 id，别人是 "渠道:号"（Bun 的 qq:<号>，数据不动）
}

// Account 是一个配置了的号：绑给哪个角色。一个号绑一个角色（ADR-007）
type Account struct {
	Channel   string // 渠道名，等于适配器的 Name()
	ID        string // 她的号
	Character string // 绑的角色
}

// Roster 是认人用的名册：谁是主人、哪个号绑哪个角色。中间件按它认人，适配器不认（规格 第五节"适配器的边界"）
type Roster struct {
	Owners   map[string]string // 渠道 → 主人在这个渠道里的号
	Accounts []Account
	NoReply  map[string]string // 平台:原始号 → 不接话原因；只限制触发，原文与观察照留（ADR-116）
}

// validate 查名册：号和角色都要填；同一个号不能出现两次（尤其是绑给两个角色，规格 第十六节 B"多账号"：启动即报错）。
// 同一个角色绑几个号不拦：每个号一条连接，各记各的"看到了"，角色是同一个
func (r Roster) validate() error {
	seen := map[[2]string]string{}
	for i, a := range r.Accounts {
		if a.Channel == "" || a.ID == "" || a.Character == "" {
			return fmt.Errorf("第 %d 个账号没填全（渠道 %q、号 %q、角色 %q）", i+1, a.Channel, a.ID, a.Character)
		}
		k := [2]string{a.Channel, a.ID}
		if prev, dup := seen[k]; dup {
			if prev == a.Character {
				return fmt.Errorf("账号 %s:%s 配了两次", a.Channel, a.ID)
			}
			return fmt.Errorf("账号 %s:%s 同时绑给了角色 %q 和 %q：一个号只能绑一个角色（ADR-007）", a.Channel, a.ID, prev, a.Character)
		}
		seen[k] = a.Character
	}
	return nil
}

// account 查一个号配没配、绑给谁
func (r Roster) account(channel, id string) (Account, bool) {
	for _, a := range r.Accounts {
		if a.Channel == channel && a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// accountByID 按号找账号，渠道可以不知道：被踢的通知是整个号的事，适配器不填会话（types.Notice 的约定），渠道只能靠号在名册里找。
// 渠道给了就是精确找；没给时全名册里只有一个号叫这个才算找到（两个渠道恰好同号的，分不清，不猜）
func (r Roster) accountByID(channel, id string) (Account, bool) {
	if channel != "" {
		return r.account(channel, id)
	}
	var found []Account
	for _, a := range r.Accounts {
		if a.ID == id {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		return Account{}, false
	}
	return found[0], true
}

// accountsOf 是这个角色在这个渠道里绑的所有号（"叫到她"要认她的每一个号：@ 任何一个都算）
func (r Roster) accountsOf(channel, character string) []string {
	var out []string
	for _, a := range r.Accounts {
		if a.Channel == channel && a.Character == character {
			out = append(out, a.ID)
		}
	}
	return out
}

// Resolve 认人：主人 > 角色的号 > 别人。身份按号认，不按名字（群名片改成"主人"的群友，照样是群友）。
// 主人先判：主人的号同时配成账号时，这条消息仍算主人说的（Bun role() 的顺序）
func (r Roster) Resolve(channel, userID string) Actor {
	if owner := r.Owners[channel]; owner != "" && owner == userID {
		return Actor{Kind: ActorOwner, ID: "owner"}
	}
	if a, ok := r.account(channel, userID); ok {
		return Actor{Kind: ActorCharacter, ID: a.Character}
	}
	return Actor{Kind: ActorHuman, ID: channel + ":" + userID}
}

// CanTrigger 认存库后的身份，不认昵称；名单按平台隔离，主人和角色先还原成该平台的号。
func (r Roster) CanTrigger(channel, actor string) bool {
	id := ""
	if actor == "owner" {
		id = r.Owners[channel]
		if channel == "console" && id == "" {
			id = "owner" // 本地的主人是内核身份，没有平台账号
		}
	} else if raw, ok := strings.CutPrefix(actor, channel+":"); ok {
		id = raw
	} else {
		for _, a := range r.Accounts {
			if a.Channel == channel && a.Character == actor {
				if _, denied := r.NoReply[channel+":"+a.ID]; denied {
					return false
				}
				id = a.ID
			}
		}
	}
	if id == "" || id == "0" {
		return false
	}
	_, denied := r.NoReply[channel+":"+id]
	return !denied
}
