package memview

import (
	"regexp"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/memory"
)

// 身份的形状：qq:<号>。身份类型只由这个形状定，名字只用来显示（规格-记忆页 3.5）：
// subject 写成"主人""我""system" 一律是 other，不是你
var personRE = regexp.MustCompile(`^qq:[0-9]{3,20}$`)

func (s *Service) name(kind, id string) string {
	if s.opt.Names == nil {
		return ""
	}
	return memory.CleanLine(s.opt.Names(kind, id), 40)
}

// who 把一个原始的"谁"字符串变成 MemoryWho
func (s *Service) who(raw string) api.MemoryWho {
	switch {
	case raw == "":
		return api.MemoryWho{Kind: "none"}
	case raw == "owner":
		return api.MemoryWho{Kind: "owner", Name: "你"}
	case raw == "self":
		return api.MemoryWho{Kind: "self", Name: "她自己"}
	case personRE.MatchString(raw):
		return api.MemoryWho{Kind: "person", ID: config.MaskID(raw), Name: s.name("person", raw)}
	}
	return api.MemoryWho{Kind: "other", Raw: memory.CleanLine(raw, 40)}
}

// whoLabel：一个人在一句话里怎么写（范围标签用）：有名字写名字，再带打码 id；没名字只写打码 id
func whoLabel(w api.MemoryWho) string {
	switch w.Kind {
	case "owner":
		return "你"
	case "self":
		return "她自己"
	case "person":
		if w.Name != "" {
			return w.Name + "（" + w.ID + "）"
		}
		return w.ID
	case "other":
		return w.Raw
	}
	return "没写"
}

// convLabel：会话的显示名。认人那一处给了名字就用，没有就按会话 id 的形状写，号码打码
func (s *Service) convLabel(conv string) string {
	if conv == "" {
		return ""
	}
	if n := s.name("conv", conv); n != "" {
		return n + "（" + convShape(conv) + "）"
	}
	return convShape(conv)
}

func convShape(conv string) string {
	parts := strings.Split(conv, ":")
	switch {
	case len(parts) == 3 && parts[0] == "qq" && parts[1] == "group" && parts[2] != "":
		return "QQ 群 " + config.MaskID(parts[2])
	case len(parts) == 4 && parts[0] == "qq" && parts[1] == "private" && parts[3] != "":
		return "QQ 私聊 " + config.MaskID(parts[3])
	case len(parts) >= 2 && parts[0] == "console" && parts[1] != "":
		if len(parts) == 4 && parts[2] == "topic" {
			return "本地话题 " + memory.CleanLine(parts[1], 20)
		}
		if len(parts) >= 3 && parts[2] == "group" {
			return "本地群聊"
		}
		return "本地 " + memory.CleanLine(parts[1], 20)
	}
	return memory.CleanLine(conv, 40)
}

// scope 把一个范围值翻成人话（规格-记忆页 3.2）。认不出的（空串、conv: 后面空、不认识的前缀、超长）写"范围不认识"，原文前 40 字
func (s *Service) scope(raw string) api.MemoryScope {
	switch {
	case raw == "public":
		return api.MemoryScope{Kind: "public", Label: "公开来源"}
	case raw == "owner":
		return api.MemoryScope{Kind: "owner", Label: "主人场合的来源"}
	case strings.HasPrefix(raw, "conv:") && len(raw) > 5 && len(raw) <= 200 && strings.Contains(raw[5:], ":"):
		return api.MemoryScope{Kind: "conv", Label: "来自 " + s.convLabel(raw[5:])}
	case strings.HasPrefix(raw, "person:") && len(raw) > 7 && len(raw) <= 200:
		return api.MemoryScope{Kind: "person", Label: "和 " + whoLabel(s.who(raw[7:])) + " 的私聊来源"}
	}
	return api.MemoryScope{Kind: "unknown", Label: "范围不认识（" + memory.CleanLine(raw, 40) + "）"}
}

func (s *Service) scopes(raw []string) []api.MemoryScope {
	out := make([]api.MemoryScope, len(raw))
	for i, r := range raw {
		out[i] = s.scope(r)
	}
	return out
}

func (s *Service) row(r memory.BrowseRow) api.MemoryRow {
	conversation := s.convLabel(r.Conversation)
	if r.LearnedBy != "" {
		conversation += " · 主人共用，由 " + memory.CleanLine(r.LearnedBy, 40) + " 得知"
	}
	return api.MemoryRow{
		Ref: r.Ref, Kind: r.Kind, Status: memory.CleanLine(r.Status, 30), ConfirmedBy: memory.CleanLine(r.ConfirmedBy, 30), Group: r.Group,
		Text: r.Text, TextCut: r.TextCut,
		Subject: s.who(r.Subject), AttributedTo: s.who(r.AttributedTo),
		Conversation: conversation, Scopes: s.scopes(r.Scopes),
		CreatedAt: r.CreatedAt, LearnedAt: r.LearnedAt, Temporal: memory.CleanLine(r.Temporal, 20), Expired: r.Expired,
		FromSeq: r.FromSeq, ToSeq: r.ToSeq,
	}
}

func (s *Service) places(scopes []string) []api.MemoryPlace {
	pv := memory.ScopePlaces(scopes)
	out := make([]api.MemoryPlace, len(pv))
	for i, p := range pv {
		out[i] = api.MemoryPlace{Place: string(p.Place), Visible: string(p.Visible), When: p.When}
	}
	return out
}

// ───────────── 修订的人话 ─────────────

func whoText(by string) string {
	switch by {
	case "summary":
		return "整理"
	case "remember-ai":
		return "角色提炼"
	case "remember":
		return "你让她记"
	case "owner":
		return "你在界面上"
	case "migration":
		return "导入"
	}
	return memory.CleanLine(by, 20)
}

func didText(op string) string {
	switch op {
	case "add":
		return "写下"
	case "supersede":
		return "被新的取代"
	case "revoke":
		return "撤销"
	case "restore":
		return "恢复"
	case "confirm":
		return "放行"
	case "scope", "scopes":
		return "改范围"
	case "migrate":
		return "导入"
	case "purged":
		return "真删"
	}
	return memory.CleanLine(op, 20)
}

func statusWord(st string) string {
	switch st {
	case "active":
		return "有效"
	case "candidate":
		return "待确认"
	case "revoked":
		return "已撤销"
	case "superseded":
		return "被取代"
	}
	return memory.CleanLine(st, 20)
}

func confirmedWord(cb string) string {
	switch cb {
	case "ai":
		return "她自己"
	case "owner":
		return "你"
	case "owner_said":
		return "你说的，她记下"
	case "":
		return "没记（导入的）"
	}
	return memory.CleanLine(cb, 20)
}

// change 把修订前后的小 JSON 写成一句：状态、放行者、范围的变化。读得出什么写什么，前后都没记就是空
func (s *Service) change(r memory.RevisionRow) string {
	var parts []string
	switch {
	case r.Before.Status != "" && r.After.Status != "" && r.Before.Status != r.After.Status:
		parts = append(parts, statusWord(r.Before.Status)+" → "+statusWord(r.After.Status))
	case r.Before.Status == "" && r.After.Status != "":
		parts = append(parts, "→ "+statusWord(r.After.Status))
	}
	if r.After.HasConfirmedBy && (!r.Before.HasConfirmedBy || r.Before.ConfirmedBy != r.After.ConfirmedBy) {
		parts = append(parts, "放行者："+confirmedWord(r.After.ConfirmedBy))
	}
	if r.After.HasScopes {
		labels := func(raw []string) string {
			ss := make([]string, len(raw))
			for i, x := range raw {
				ss[i] = s.scope(x).Label
			}
			return strings.Join(ss, "、")
		}
		if r.Before.HasScopes {
			parts = append(parts, "范围："+labels(r.Before.Scopes)+" → "+labels(r.After.Scopes))
		} else {
			parts = append(parts, "范围："+labels(r.After.Scopes))
		}
	}
	if r.After.SupersededBy != "" {
		parts = append(parts, "被 f:"+memory.CleanLine(r.After.SupersededBy, 40)+" 取代")
	}
	return strings.Join(parts, "，")
}

func (s *Service) revision(r memory.RevisionRow) api.MemoryRevision {
	return api.MemoryRevision{ID: r.ID, At: r.At, Who: whoText(r.By), Did: didText(r.Op), Change: s.change(r), Reason: memory.CleanLine(r.Reason, 200)}
}
