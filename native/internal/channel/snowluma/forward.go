package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"sumika-native/internal/channel/types"
)

var ErrForwardEmpty = errors.New("payload_empty")

var _ types.ForwardReader = (*Adapter)(nil)

func (a *Adapter) Forward(ctx context.Context, account, id string) (types.ForwardTree, error) {
	if _, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		return types.ForwardTree{}, errors.New("refused")
	}
	if id == "" || fileID(id) == "" {
		return types.ForwardTree{}, errors.New("unavailable")
	}
	frame, err := a.query(ctx, "get_forward_msg", map[string]any{"id": id})
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errCallTimeout):
			return types.ForwardTree{}, errors.New("timeout")
		case errors.Is(err, errNotConnected):
			return types.ForwardTree{}, errors.New("not_connected")
		case errors.Is(err, errConnLost):
			return types.ForwardTree{}, errors.New("connection_lost")
		case errors.Is(err, errLoginMismatch), errors.Is(err, errUnhealthy), errors.Is(err, errSelfMismatch):
			return types.ForwardTree{}, errors.New("refused")
		}
		return types.ForwardTree{}, fmt.Errorf("%w", err)
	}
	return forwardFrame(frame, id)
}

// 节点不是现场消息，不能用占位号认身份；同一 segments 入口翻正文，名字只作显示。
func forwardFrame(frame []byte, id string) (types.ForwardTree, error) {
	var reply object
	if json.Unmarshal(frame, &reply) != nil || reply == nil {
		return types.ForwardTree{}, errors.New("unavailable")
	}
	if str(reply, "status") == "failed" || string(reply["retcode"]) != "0" {
		if strings.Contains(str(reply, "message"), "payload is empty") || strings.Contains(str(reply, "wording"), "payload is empty") {
			return types.ForwardTree{}, ErrForwardEmpty
		}
		return types.ForwardTree{}, errors.New("unavailable")
	}
	var nodes []json.RawMessage
	if json.Unmarshal(obj(reply, "data")["messages"], &nodes) != nil || len(nodes) == 0 {
		return types.ForwardTree{}, ErrForwardEmpty
	}
	tree := types.ForwardTree{ID: id, Missing: max(0, len(nodes)-types.ForwardNodes)}
	for _, raw := range nodes[:min(len(nodes), types.ForwardNodes)] {
		var node object
		_ = json.Unmarshal(raw, &node)
		groupID := forwardUID(node, "group_id")
		// 原群参与文件取址，只接受可作为协议参数的正整数，不能把不透明文本当群号。
		if _, ok := positiveID(groupID); !ok {
			groupID = ""
		}
		group := groupID != ""
		tree.Group = tree.Group || group
		sender := obj(node, "sender")
		name := onlyStr(sender, "nickname")
		if group && onlyStr(sender, "card") != "" {
			name = onlyStr(sender, "card")
		}
		segs, _ := segments(node["message"])
		if segs == nil {
			segs = []types.Seg{}
		} // 空节点也保存数组，后续读树和导入校验沿同一契约。
		for i := range segs {
			if segs[i].Kind == types.SegFile {
				segs[i].FileGroup = groupID
			}
			segs[i].Text = strings.Map(func(r rune) rune {
				if r == '\ufffc' || unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n' && r != '\t') {
					return -1
				}
				return r
			}, segs[i].Text)
		}
		uid := forwardUID(node, "user_id")
		if uid == "" {
			uid = forwardUID(sender, "user_id")
		}
		at := fileSize(node, "time")
		tree.Nodes = append(tree.Nodes, types.ForwardMessageNode{UID: uid, Name: cardNodeName(name), Time: at, Segs: segs})
	}
	return tree, nil
}

func cardNodeName(name string) string {
	rs := make([]rune, 0, 40)
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\ufffc' {
			continue
		}
		if len(rs) == 40 {
			break
		}
		rs = append(rs, r)
	}
	return strings.TrimSpace(string(rs))
}

func forwardUID(o object, key string) string {
	raw := o[key]
	if len(raw) == 0 || !(raw[0] == '"' || raw[0] >= '0' && raw[0] <= '9') {
		return ""
	}
	return fileID(qqID(str(o, key)))
}
