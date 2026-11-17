package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"sumika-native/internal/channel/types"
)

var _ types.Recaller = (*Adapter)(nil)

// Recall 撤回自己号发的一条（delete_msg，参数是平台消息号；SnowLuma 要求消息在它本地的消息存储里查得到）。
// 门槛（是不是本次刚发、2 分钟内）由装配处判断，这里只管号、会话、消息号合法，以及协议端的回包。
func (a *Adapter) Recall(ctx context.Context, account string, conv types.Conversation, platformID string) error {
	if reason, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		if ok {
			reason = "这条连接没配号"
		}
		return errors.New(reason)
	}
	if err := conv.Valid(); err != nil || conv.Channel != a.Name() {
		return errors.New("要撤回的会话不合法")
	}
	id, ok := decimalID(platformID)
	if !ok {
		return errors.New("平台消息号不是合法十进制整数")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("平台消息号不是合法十进制整数")
	}
	frame, _, err := a.call(ctx, nil, "delete_msg", map[string]any{"message_id": n})
	if err != nil {
		return fmt.Errorf("delete_msg 没有回包：%w", err)
	}
	var reply object
	if json.Unmarshal(frame, &reply) != nil || reply == nil {
		return errors.New("delete_msg 的回包不是对象")
	}
	if str(reply, "status") == "failed" || string(reply["retcode"]) != "0" {
		return fmt.Errorf("delete_msg 被协议端拒绝（retcode %s）", clipRetcode(reply["retcode"]))
	}
	return nil
}
