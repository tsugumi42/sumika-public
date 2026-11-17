package desktopread

import (
	"encoding/json"
	"fmt"
	"strings"

	"sumika-native/internal/store"
)

// noticeActor 是通知里没有名字时的说法：不把号码摆到界面上。
const noticeActor = "一位群友"

// serviceText 把群事件翻成消息列里的通知条文字：进群、退群、踢人、禁言、解禁、改名片。
// 读取时翻，不写库：原始帧一直存在 segments_json 里，以前收到的不用补数据，以后加新的事件类型也不用迁移。
// 认不出的返回空串，这种通知聊天区不显示（库里还留着）。
func serviceText(r store.DisplayRow, name string) string {
	if r.Kind != "notice" || r.Segments == "" {
		return ""
	}
	var body struct {
		Notice string `json:"notice"`
		Raw    string `json:"raw"`
	}
	if json.Unmarshal([]byte(r.Segments), &body) != nil || body.Notice != "other" || body.Raw == "" {
		return ""
	}
	var f struct {
		Type     string          `json:"notice_type"`
		Sub      string          `json:"sub_type"`
		UserID   json.RawMessage `json:"user_id"`
		Duration int64           `json:"duration"`
		CardNew  *string         `json:"card_new"`
		CardOld  string          `json:"card_old"`
	}
	if json.Unmarshal([]byte(body.Raw), &f) != nil {
		return "" // 原始帧被截断过（超过存储上限）也走这里
	}
	if name = strings.TrimSpace(name); name == "" {
		name = noticeActor
	}
	switch f.Type {
	case "group_increase":
		return name + " 加入了群聊"
	case "group_decrease":
		switch f.Sub {
		case "leave":
			return name + " 退出了群聊"
		case "kick", "kick_me":
			return name + " 被移出了群聊"
		}
		return name + " 离开了群聊"
	case "group_ban":
		whole := wholeGroup(f.UserID)
		switch f.Sub {
		case "ban":
			if whole {
				return "开启了全员禁言"
			}
			return name + " 被禁言" + banDuration(f.Duration)
		case "lift_ban":
			if whole {
				return "关闭了全员禁言"
			}
			return name + " 被解除禁言"
		}
		return ""
	case "group_card":
		if f.CardNew == nil {
			return ""
		}
		old := strings.TrimSpace(f.CardOld)
		if old == "" {
			old = name
		}
		if card := strings.TrimSpace(*f.CardNew); card != "" {
			return old + " 把群名片改成了 " + card
		}
		return old + " 清空了群名片"
	}
	return ""
}

// wholeGroup：全员禁言的 user_id 是 0（有的协议端写成字符串 "0"）。
func wholeGroup(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return s == "" || s == "0"
}

func banDuration(seconds int64) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds < 60:
		return fmt.Sprintf(" %d 秒", seconds)
	case seconds < 3600:
		return fmt.Sprintf(" %d 分钟", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf(" %d 小时", seconds/3600)
	}
	return fmt.Sprintf(" %d 天", seconds/86400)
}
