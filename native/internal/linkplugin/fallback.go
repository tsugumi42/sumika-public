package linkplugin

import (
	"strings"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
)

// 链接展开的媒体回执兜底（ADR-310、333）。段是否真的发出去由宿主读计划得出（Outcomes）；
// 换形式的阶梯在 degrade.go，这里是回执的词表和几个小工具。

// 段的终态。字符串与 store.Part.State 的值相同：confirmed、failed、aborted、unknown
const (
	OutcomeConfirmed = "confirmed"
	OutcomeFailed    = "failed"
	OutcomeAborted   = "aborted"
	OutcomeUnknown   = "unknown"
)

// 失败类别（适配器的回执原因归成三类，不含正文、URL、令牌）：
// 能力缺失（协议端明说发不了这类段）；“这一张被拒”（内容审核、单张太大……）；本地原因两者都不是。
const (
	CategoryUnsupported = "协议端不支持"
	CategoryRejected    = "协议端拒绝"
	CategoryOther       = "发送出了问题"
)

// PartOutcome 是计划里一段的终态。Index 是提交时的段序；Category 只在 failed 时有，是固定类别，不含正文、URL、令牌
type PartOutcome struct {
	Index    int
	State    string
	Category string
	// NoReceipt 说这个 unknown 是发出去了但没拿到确认（没确认送达、没回包、超时、写出后断线）：值得等回声（规格-链接发送改版 A1）；
	// PlatformID 是已确认的段的平台消息号（撤回、核实时排除用），SentAtMS 是这一段第一次调 Send 的时刻（毫秒，撤回的 2 分钟从它算）
	NoReceipt  bool
	PlatformID string
	SentAtMS   int64

	echo   *channel.EchoNote // 这一段被回声认领时的样子（插件自己填，不是宿主给的）
	noEcho bool              // 没确认送达、到点也没等到回声，按没送到算
	guard  *lateGuard        // 大图按没送到换形式时开的晚到回声看守（late.go）；不是大图、不是等不到回声的为 nil
}

// hasMedia 说这一段（正文或任一转发节点）带不带媒体段
func hasMedia(p types.Part) bool {
	for _, s := range p.Body {
		if s.Kind != types.BodyText {
			return true
		}
	}
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			if s.Kind != types.BodyText {
				return true
			}
		}
	}
	return false
}

func allConfirmed(outcomes []PartOutcome) bool {
	for _, o := range outcomes {
		if o.State != OutcomeConfirmed {
			return false
		}
	}
	return len(outcomes) > 0
}

// hasFailure 说有没有明确失败或被中止的段（unknown 可能已经发出去了，不算）。
func hasFailure(outcomes []PartOutcome) bool {
	for _, o := range outcomes {
		if o.State == OutcomeFailed || o.State == OutcomeAborted {
			return true
		}
	}
	return false
}

// mediaLabels 数一段里的媒体：图、动图、视频各叫什么、一共几个。
func mediaLabels(p types.Part) (labels []string, count int) {
	seen := map[string]bool{}
	add := func(kind string) {
		label := map[string]string{types.BodyImage: "图", types.BodyGIF: "动图", types.BodyVideo: "视频"}[kind]
		if label == "" {
			return
		}
		count++
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	for _, s := range p.Body {
		add(s.Kind)
	}
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			add(s.Kind)
		}
	}
	return labels, count
}

// failureNote 给补发的文字版写原因。例：
// “图没发出去：QQ 拒收了这张图（直接发、合并转发、转 180° 的合并转发都被拒）”“图没发出去：这个群暂时发不了图”。
func failureNote(it textItem, scene string) string {
	labels, count := mediaLabels(it.part)
	if len(labels) == 0 {
		return ""
	}
	place := "这个会话"
	if scene == types.SceneGroup {
		place = "这个群"
	}
	label := strings.Join(labels, "、")
	switch it.category {
	case CategoryUnsupported:
		return label + "没发出去：" + place + "暂时发不了" + label
	case CategoryRejected:
		noun := "这组" + label
		if count == 1 {
			noun = map[string]string{"图": "这张图", "动图": "这张动图", "视频": "这个视频"}[label]
		}
		note := label + "没发出去：QQ 拒收了" + noun
		if len(it.tried) > 0 {
			names := make([]string, len(it.tried))
			for i, f := range it.tried {
				names[i] = f.String()
			}
			note += "（" + strings.Join(names, "、") + "都被拒）"
		}
		return note
	}
	return label + "没发出去：发送时出了问题"
}
