package kernel

import (
	"context"
	"io"
)

type ResultRef struct {
	ID, SHA256, MediaType string
	Owner                 Scope
	RunID, StepID, ToolID string
	Bytes                 int64
	Complete              bool
}

type ResultPage struct {
	Ref              ResultRef
	Text             string
	Start, End, Next int64
	EOF              bool
}

// ResultStore 由 K2 实现；句柄不是授权，读取须核对 Scope 和 UTF-8 字节边界。
type ResultStore interface {
	Put(context.Context, Invocation, string, io.Reader, bool) (ResultRef, error)
	Read(context.Context, Scope, string, int64, int64) (ResultPage, error)
}

type Evidence struct {
	Ref        ResultRef
	Start, End int64
	Claim      string
}

type WorkState struct {
	RootID                                   string
	Owner                                    Scope
	Revision, ThroughOrdinal                 uint64
	Goal                                     string
	Constraints, Completed, Pending, Unknown []string
	Evidence                                 []Evidence
	Next                                     string
}

type ReviewRequest struct {
	Invocation   Invocation
	CheckpointID string
	Trigger      string
	Work         WorkState
	Criteria     []string
	Evidence     []Evidence
}

type Finding struct {
	ID, Severity, Text string
	Evidence           []Evidence
}

type Verdict struct {
	CheckpointID, Status string
	Findings             []Finding
}
