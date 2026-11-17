package turn

// 从上下文起点重建（规格-私聊从上下文起点重建 第一节）：只改本地私聊和主人的 QQ 私聊，其余场合照旧走锚定窗口。
// 判断要窄：本地主窗口的私聊（会话名正好是 console:<角色>）和主人本人的 QQ 私聊。

import (
	"strings"

	"sumika-native/internal/kernel"
)

// rebuildFromStart：这个回合的会话重建时从上下文起点读到现在。
// 本地只认 console:<角色> 这一种形状（话题、自检、悄悄话、子任务另有会话名），QQ 只认主人私聊
// （PrivateOK 由渠道按主人号算出）。cfg.NoStartRebuild 只给评测和测试关掉这一条，生产不设。
func rebuildFromStart(cfg Config, in Input) bool {
	if cfg.NoStartRebuild || in.Identity.Kind == kernel.KindChild {
		return false
	}
	if in.CharacterID != "" && in.Conversation == "console:"+in.CharacterID && ownerConsoleAudience(in.Audience) {
		return true
	}
	return strings.HasPrefix(in.Conversation, "qq:private:") && in.Audience.Channel == "qq" && in.Audience.Scene == "private" && in.Audience.PrivateOK
}

// idleCompactLocal：空闲压缩只给本地私聊（ADR-388 第 5 条：主人 QQ 私聊的压缩挪后，群聊都不做）。
// 两种场合都从上下文起点重建，但 QQ 那边改的是“谁能看到什么”，单独做复核，不跟这里一起放开
func idleCompactLocal(cfg Config, in Input) bool {
	return rebuildFromStart(cfg, in) && in.Audience.Channel == "console"
}
