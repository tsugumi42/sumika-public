package api

import (
	"context"
	"time"
)

// ForwardReader 保持纯读；取回动作另走 ForwardActions，archive 不提供可联网的动作。
type ForwardReader interface {
	ReadForward(context.Context, ForwardRequest) (Result[ForwardView], error)
}
type ForwardActions interface {
	FetchForward(context.Context, ForwardRequest, time.Duration) (Result[ForwardView], error)
}

type ForwardRequest struct {
	Source EventRef
	ID     string
	Via    []string
}
type ForwardBrief struct {
	Title          string
	Lines          [3]string
	Count, Missing int
	State          string
}
type ForwardView struct {
	ID      string
	Group   bool
	Nodes   []ForwardNode
	Missing int
	Fetched int64
	State   string
	Depth   int
}
type ForwardNode struct {
	UID, Name string
	At        int64
	Segments  []DisplaySegment
}
