package channel

import (
	"strings"
	"time"

	"sumika-native/internal/channel/types"
)

// 回合和给模型看的那一批话的测试共用的小东西：东八区、几个编的发言人、造一条消息和一批

var tz8 = time.FixedZone("CST", 8*3600)

const (
	whoOwner  = OwnerWho
	whoFriend = "qq:" + friend
	whoOther  = "qq:" + other
)

func mk(seq int64, who, text string, called bool) types.Msg {
	return types.Msg{Seq: seq, Time: baseTime, Who: who, Name: "名" + who, Origin: OriginPeer, Addressed: called,
		Segs: []types.Seg{{Kind: types.SegText, Text: text}}}
}

func batchFor(conv types.Conversation, ms ...types.Msg) types.Batch {
	b := types.Batch{Conv: conv, Character: "ca", Account: acctA, Now: baseTime, Trigger: types.TriggerMessage, Msgs: ms}
	for _, m := range ms {
		if m.Addressed {
			b.Addressed = true
		}
	}
	return b
}

// headerLines 是 Text 里以"["开头的行（每条消息一行头，内容里的方括号都转义了）
func headerLines(text string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "[") {
			n++
		}
	}
	return n
}
