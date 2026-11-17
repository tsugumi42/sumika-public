package config

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"sumika-native/internal/channel/types"
)

// 群里谁发的链接能触发展开（用户 2026-11-13）：全部、只有名单里的人、名单里的人除外。
// 只管群聊；私聊照旧由会话名单和私聊白名单决定。主人永远能触发（名单拦不住主人）。
// 只管“展开发出去”，不管“给她看的链接摘要”（linkReader.context）。
const (
	SendersAll       = "all"
	SendersWhitelist = "whitelist"
	SendersBlacklist = "blacklist"
	// maxSenderUsers 是一份名单最多几个号；maxSenderGroups 是最多给几个群单独设
	maxSenderUsers  = 2000
	maxSenderGroups = 500
)

// LinkSenderRule 是一份“谁能触发”的规则：模式加 QQ 号名单
type LinkSenderRule struct {
	Mode  string   `yaml:"mode"`
	Users []string `yaml:"users"`
}

// LinkSenders 是全局的规则，Groups 按群号单独覆盖（写了某个群就整份用它的，不和全局合并）
type LinkSenders struct {
	Mode   string                    `yaml:"mode"`
	Users  []string                  `yaml:"users"`
	Groups map[string]LinkSenderRule `yaml:"groups"`
}

func defaultLinkSenders() LinkSenders {
	return LinkSenders{Mode: SendersAll, Users: []string{}, Groups: map[string]LinkSenderRule{}}
}

func (s LinkSenders) clone() LinkSenders {
	s.Users = slices.Clone(s.Users)
	if s.Groups != nil {
		g := make(map[string]LinkSenderRule, len(s.Groups))
		for k, v := range s.Groups {
			v.Users = slices.Clone(v.Users)
			g[k] = v
		}
		s.Groups = g
	}
	return s
}

// qqNumber 是不是一个规范的 QQ 号（正整数、不带前导零、不带空白）
func qqNumber(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

func checkSenderRule(key string, r LinkSenderRule) []string {
	var bad []string
	if r.Mode != SendersAll && r.Mode != SendersWhitelist && r.Mode != SendersBlacklist {
		bad = append(bad, key+".mode：只能为 all / whitelist / blacklist")
	}
	if len(r.Users) > maxSenderUsers {
		bad = append(bad, fmt.Sprintf("%s.users：最多 %d 个号", key, maxSenderUsers))
	}
	for _, u := range r.Users {
		if !qqNumber(u) {
			bad = append(bad, fmt.Sprintf("%s.users：%q 不是 QQ 号（只写数字）", key, u))
			break
		}
	}
	return bad
}

func (s LinkSenders) check() []string {
	bad := checkSenderRule("linkReader.expand.senders", LinkSenderRule{Mode: s.Mode, Users: s.Users})
	if len(s.Groups) > maxSenderGroups {
		bad = append(bad, fmt.Sprintf("linkReader.expand.senders.groups：最多给 %d 个群单独设", maxSenderGroups))
	}
	for _, g := range slices.Sorted(maps.Keys(s.Groups)) {
		if !qqNumber(g) {
			bad = append(bad, fmt.Sprintf("linkReader.expand.senders.groups：%q 不是群号（只写数字）", g))
			continue
		}
		bad = append(bad, checkSenderRule("linkReader.expand.senders.groups."+g, s.Groups[g])...)
	}
	return bad
}

// rule 是这个群该用的规则：单独设过就用群自己的，否则用全局的
func (s LinkSenders) rule(group string) LinkSenderRule {
	if r, ok := s.Groups[group]; ok {
		return r
	}
	return LinkSenderRule{Mode: s.Mode, Users: s.Users}
}

// AllowsSender 说这条消息的发送者能不能触发展开。owner 是“发消息的是主人”（按身份认，不按名字）；
// qq 是发送者的 QQ 号（认不出就传空串）。不是群聊一律放行（私聊另有名单）；主人一律放行；
// 白名单只放名单里的号，认不出号的不放；黑名单只拦名单里的号；all 和写错的模式（加载时已被拒）都按 all。
func (c LinkExpand) AllowsSender(conv types.Conversation, owner bool, qq string) bool {
	if conv.Scene != types.SceneGroup || owner {
		return true
	}
	r := c.Senders.rule(conv.ID)
	// 认不出号（非规范的号）：白名单不放、黑名单不拦。现在身份里的号都来自协议端的数字，取不到非规范的号（复核 2026-11-13）
	listed := qqNumber(qq) && slices.Contains(r.Users, qq)
	switch r.Mode {
	case SendersWhitelist:
		return listed
	case SendersBlacklist:
		return !listed
	}
	return true
}

// validateSendersRaw 对 linkReader.expand.senders 的原始 YAML 查未知键（复核 2026-11-13）：这是权限配置，
// 白名单把 mode 拼成 mod、groups 拼成 group 时，宽松解码会悄悄按默认 all 全部放行，所以这一棵子树拼错键就拒绝加载。
func validateSendersRaw(raw map[string]any) error {
	lr, _ := asMap(raw["linkReader"])
	ex, _ := asMap(lr["expand"])
	v, present := ex["senders"]
	if !present || v == nil {
		return nil
	}
	s, ok := asMap(v)
	if !ok {
		return fmt.Errorf("linkReader.expand.senders：必须是配置对象")
	}
	for k := range s {
		if k != "mode" && k != "users" && k != "groups" {
			return fmt.Errorf("linkReader.expand.senders.%s：不认识这个键（只能写 mode、users、groups）", k)
		}
	}
	if g, present := s["groups"]; present && g != nil {
		groups, ok := asMap(g)
		if !ok {
			return fmt.Errorf("linkReader.expand.senders.groups：必须是“群号: 规则”的对象")
		}
		for id, r := range groups {
			rule, ok := asMap(r)
			if !ok {
				return fmt.Errorf("linkReader.expand.senders.groups.%s：必须是 {mode, users} 对象", id)
			}
			for k := range rule {
				if k != "mode" && k != "users" {
					return fmt.Errorf("linkReader.expand.senders.groups.%s.%s：不认识这个键（只能写 mode、users）", id, k)
				}
			}
		}
	}
	return nil
}

// asMap 把 YAML 解出来的对象统一成字符串键（群号不加引号时键可能是数字）
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[fmt.Sprint(k)] = x
		}
		return out, true
	}
	return nil, false
}
