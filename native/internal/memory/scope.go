// Package memory 是 Go 版的记忆（规格-记忆-v2；第 2 步先做检索，见 13.1）。
// 只通过三个插口碰外面：读聊天原文（EventSource）、算向量（embed.Embedder）、调后台模型（以后的 Generator）；
// 不认识 QQ、界面和对话循环。听众（谁在看、在哪看）由调用方算好给，模型改不了（规格-记忆 3.3）。
package memory

import (
	"slices"
	"strings"

	"sumika-native/internal/audience"
)

// Place 是场合 = 渠道 × 形式（docs/词汇表.md，ADR-140、144）。照 daemon/util/place.ts；Go 后台以后有公共的场合包就换过去
type Place string

const (
	LocalPrivate    Place = "local-private"
	LocalGroup      Place = "local-group"
	PlatformPrivate Place = "platform-private"
	PlatformGroup   Place = "platform-group"
)

// ChannelOf 是会话 id 的第一段（console:… / qq:…）
func ChannelOf(conversation string) string {
	ch, _, _ := strings.Cut(conversation, ":")
	return ch
}

// PlaceOf：channel 是 console 就是本地，其余都算平台；形式先看 scene（"private" / "group"），没有就按会话 id 带不带 :group: 认
func PlaceOf(channel, conversation, scene string) Place {
	group := strings.Contains(conversation, ":group:")
	if scene != "" {
		group = scene == "group"
	}
	switch {
	case channel == "console" && group:
		return LocalGroup
	case channel == "console":
		return LocalPrivate
	case group:
		return PlatformGroup
	default:
		return PlatformPrivate
	}
}

// Audience 是谁在看、在哪看：用中立的 internal/audience 那一份（2026-10-26 从这里挪过去，渠道中间件填、内核原样交给记忆），
// 这里只起个别名，记忆包里的用法不变。字段的意思见那个包；OwnerAsked（ADR-174）怎么用见 AllowedScopes 和规格-记忆-v2 13.2.12 的 W1
type Audience = audience.Audience

// AllowedScopes 是召回集合：同一角色不按场合过滤（ADR-242）。角色隔离由各自的 Memory 和 EventSource 保证。
func AllowedScopes(Audience) []string { return nil }

// DisclosureScopes 是发送前守卫的表达集合，与召回分开。主人问自己的事照答，其他听众仍检查私事披露。
func DisclosureScopes(a Audience) []string {
	place := PlaceOf(a.Channel, a.Conversation, a.Scene)
	group := place == LocalGroup || place == PlatformGroup
	if !group && a.Channel == "console" && a.PrivateOK {
		return nil
	}
	out := []string{"public"}
	if a.Conversation != "" {
		out = append(out, "conv:"+a.Conversation)
	}
	if a.PrivateOK || (place == PlatformGroup && a.OwnerAsked) {
		out = append(out, "owner")
	}
	if !group {
		for _, p := range a.PersonIDs {
			out = append(out, "person:"+p)
		}
	}
	return out
}

// ScopeKey 保留表达身份档：主人问过后，群友不能复用上一档的工具结果和表达指令。
func ScopeKey(a Audience) string {
	allowed := DisclosureScopes(a)
	if allowed == nil {
		return "all"
	}
	sorted := slices.Clone(allowed)
	slices.Sort(sorted)
	return strings.Join(sorted, ",")
}

// CanSee：一条记忆的范围是一组（ADR-159 第 4 条），组里任何一个值在可见集合里就能看；allowed 是 nil 表示全部
func CanSee(scopes []string, allowed []string) bool {
	if allowed == nil {
		return true
	}
	for _, s := range scopes {
		if slices.Contains(allowed, s) {
			return true
		}
	}
	return false
}
