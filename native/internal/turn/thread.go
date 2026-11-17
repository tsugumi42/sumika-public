package turn

import (
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
)

// 主动联系的话题线（规格-主动联系 第四、六节）：线是一个有库的本地会话（audience.IsThread），听众 PrivateOK 为假，
// 输入的发言人是宿主预先绑定的 system，不经 trustedInput 去推——那条路会把没有 Seqs 的 console 输入认成主人。

// ThreadRelation 是线里"对面是谁"那一句：线里没有人在和她说话（默认的"对面是主人"在这里不对）
const ThreadRelation = "这是你为了找所有者而开的话题线。这里没有人和你说话：只有导演的提示行，和他回复时宿主转述给你的话。"

// ReminderRelation 是提醒到点那一轮“对面是谁”那一句（规格-提醒 第二节）。这一轮用同一套受限身份（ThreadInput），会话是提醒专用会话
const ReminderRelation = "你之前记下的提醒到点了，你要跟所有者说一声。这里没有人和你说话，只有宿主的提示行；你写的话会原样发给他。"

// ThreadInput 拼线里一轮的输入：seqs 是宿主已经 AppendEvent 存好的醒来行或主人回复转述（origin proactive:wake / proactive:relay），
// text 是同一份文字。身份、听众、权限档全在这里写死，调用方只给角色、线的会话串和这几条事件。
// 场合提示词名字由宿主给（scene-proactive-thread）
func ThreadInput(characterID, conversation string, seqs []int64, text string, scenes []string) Input {
	aud := audience.Thread(conversation)
	scope := kernel.Scope{CharacterID: characterID, Conversation: conversation, ScopeID: audience.ThreadScope,
		Platform: "local", Channel: aud.Channel, Scene: aud.Scene, ActorKind: "system", ActorID: audience.ThreadActor}
	return Input{
		Identity:    kernel.Invocation{Scope: scope, Kind: kernel.KindForeground},
		CharacterID: characterID, Conversation: conversation, Seqs: append([]int64(nil), seqs...), Text: text,
		Audience: aud, Addressed: true, Scope: audience.ThreadScope, Relation: ThreadRelation, Scenes: append([]string(nil), scenes...),
	}
}

// MarkStale 标记这个会话下一轮建实例时从库里重建，不打断在跑的回合（规格-主动联系 第六节）。
// 线往目标会话写了一句话以后调：该会话已有的实例只会追"跑完的回合用过的事件"，看不到线写的这一句。
// 不用 RestartIn：它先 InterruptIn，在主人正聊着时被线打断会很怪。代价是该会话下一轮缓存冷一次
func (h *Hub) MarkStale(characterID, conversation string) {
	h.cancelIdleCompact(characterID, conversation)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.staleGen == nil {
		h.staleGen = map[string]uint64{}
	}
	h.staleGen[convKey(characterID, conversation)]++
}
