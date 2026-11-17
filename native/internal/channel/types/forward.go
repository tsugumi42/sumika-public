package types

import "context"

const ForwardDepth = 10
const ForwardNodes = 200

// ForwardReader 只查聊天记录资源，和发送转发是不同的能力；回来的时间是 OneBot 秒。
type ForwardReader interface {
	Forward(context.Context, string, string) (ForwardTree, error)
}

type ForwardTree struct {
	ID      string               `json:"id"`
	Group   bool                 `json:"group"`
	Nodes   []ForwardMessageNode `json:"nodes"`
	Missing int                  `json:"truncated,omitempty"`
}

type ForwardMessageNode struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Time int64  `json:"time"`
	Segs []Seg  `json:"segs"`
}
