package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sumika-native/internal/channel/types"
)

// FileURL 只查询文件直链；不调用可能触发协议端落盘的 get_file。
// 转发缺原会话时传空串；私聊协议按 selfUid/fileId/fileHash 取址，不猜原群。
func (a *Adapter) FileURL(ctx context.Context, account, conversation string, seg types.Seg) (string, error) {
	if reason, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		return "", fmt.Errorf("文件查询账号不匹配：%s", reason)
	}
	if seg.Kind != types.SegFile || seg.FileID == "" || fileID(seg.FileID) != seg.FileID || fileID(seg.FileHash) != seg.FileHash {
		return "", errors.New("文件缺少合法取址凭据")
	}
	action := "get_private_file_url"
	params := map[string]any{"file_id": seg.FileID}
	if conversation != "" {
		conv, err := types.ParseConversation(conversation)
		if err != nil || conv.Channel != a.Name() || qqID(conv.ID) == "" || conv.Scene == types.ScenePrivate && conv.Self != account {
			return "", errors.New("文件查询会话不合法")
		}
		if conv.Scene == types.SceneGroup {
			action = "get_group_file_url"
			params["group_id"] = conv.ID
		} else {
			params["user_id"] = conv.ID
		}
	}
	if action == "get_private_file_url" {
		if seg.FileHash == "" {
			return "", errors.New("私聊或转发文件缺少 file_hash，无法确定下载地址")
		}
		params["file_hash"] = seg.FileHash
	}
	frame, err := a.query(ctx, action, params)
	if err != nil {
		if errors.Is(err, types.ErrQueryBudget) {
			return "", err
		}
		return "", fmt.Errorf("文件取址 %w：%s", ErrNoReply, errQuery(err))
	}
	var reply object
	if json.Unmarshal(frame, &reply) != nil || reply == nil {
		return "", errors.New("文件取址回包不是对象")
	}
	if str(reply, "status") == "failed" || string(reply["retcode"]) != "0" {
		return "", errors.New("协议端拒绝文件取址")
	}
	u := mediaURL(onlyStr(obj(reply, "data"), "url"))
	if u == "" {
		return "", errors.New("协议端没有返回 HTTP 文件地址")
	}
	return u, nil
}
