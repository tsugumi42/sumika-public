package ctlproto

const CmdCheckpoint = "checkpoint"

// 控制端只拿会话内的安全投影，不把完整 Invocation 或授权凭据给客户端。
type CheckpointSummary struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	ToolID    string `json:"toolId"`
	Tool      string `json:"tool"`
	State     string `json:"state"`
	CreatedMS int64  `json:"createdMs"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Child     bool   `json:"child"`
	Note      string `json:"note,omitempty"`
}
type CheckpointList struct {
	Items []CheckpointSummary `json:"items"`
}
type CheckpointFile struct {
	Path          string `json:"path"`
	TargetExists  bool   `json:"targetExists"`
	CurrentExists bool   `json:"currentExists"`
	TargetHash    string `json:"targetHash"`
	CurrentHash   string `json:"currentHash"`
	TargetBytes   int64  `json:"targetBytes"`
	CurrentBytes  int64  `json:"currentBytes"`
}
type CheckpointPlan struct {
	ID        string           `json:"id"`
	Mode      string           `json:"mode"`
	Version   string           `json:"version"`
	Workspace string           `json:"workspace"`
	Files     []CheckpointFile `json:"files"`
}
type CheckpointRestore struct {
	TargetID  string   `json:"targetId"`
	InverseID string   `json:"inverseId"`
	State     string   `json:"state"`
	Completed []string `json:"completed"`
	Remaining []string `json:"remaining"`
}

type CheckpointProgress struct {
	TargetID     string   `json:"targetId"`
	InverseID    string   `json:"inverseId"`
	State        string   `json:"state"`
	Done         int      `json:"done"`
	Total        int      `json:"total"`
	Completed    []string `json:"completed,omitempty"`
	Remaining    []string `json:"remaining,omitempty"`
	OutcomeSaved bool     `json:"outcomeSaved"`
	Error        string   `json:"error,omitempty"`
}
