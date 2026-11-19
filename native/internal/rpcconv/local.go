package rpcconv

// 第 2 批新类型的转换（规格-Bun后台第2批 第二节）：本地私聊、审批、检查点、转发。
// 约定同 conv.go：空切片不发、收回来是 nil；uint64 线上是 int64；ApprovalChoice 线上是字符串。

import (
	"sumika-native/internal/api"
	"sumika-native/internal/rpcwire"
)

// ───── 草稿 ─────

func DraftOut(d api.LocalDraft) rpcwire.LocalDraft {
	return rpcwire.LocalDraft{TurnID: d.TurnID, Step: d.Step, Text: d.Text, CharacterID: d.CharacterID, Reset: d.Reset}
}

func DraftIn(d rpcwire.LocalDraft) api.LocalDraft {
	return api.LocalDraft{TurnID: d.TurnID, Step: d.Step, Text: d.Text, CharacterID: d.CharacterID, Reset: d.Reset}
}

// ───── 本地群聊 ─────

func StepsOut(l []api.LocalProcessStep) []rpcwire.LocalProcessStep { return mapSlice(l, stepOut) }
func StepsIn(l []rpcwire.LocalProcessStep) []api.LocalProcessStep  { return mapSlice(l, stepIn) }

// ───── 审批 ─────

func egressTargetOut(t api.EgressTarget) rpcwire.EgressTarget {
	return rpcwire.EgressTarget{Kind: t.Kind, Value: t.Value, Reason: t.Reason, Danger: t.Danger}
}

func egressTargetIn(t rpcwire.EgressTarget) api.EgressTarget {
	return api.EgressTarget{Kind: t.Kind, Value: t.Value, Reason: t.Reason, Danger: t.Danger}
}

func ApprovalOut(a api.LocalApproval) rpcwire.LocalApproval {
	out := rpcwire.LocalApproval{ID: a.ID, CharacterID: a.CharacterID, Key: KeyOut(a.Key), Title: a.Title, Tool: a.Tool, Subject: a.Subject,
		Why: a.Why, Purpose: a.Purpose, Preview: a.Preview, Workdir: a.Workdir, Notes: strs(a.Notes),
		Options: mapSlice(a.Options, func(c api.ApprovalChoice) string { return string(c) }), KindLabel: a.KindLabel, ExpiresAt: a.ExpiresAt}
	if a.Whisper != nil {
		out.Whisper = &rpcwire.ApprovalWhisper{Source: KeyOut(a.Whisper.Source), Batch: a.Whisper.Batch, Character: a.Whisper.Character}
	}
	if a.Egress != nil {
		out.Egress = &rpcwire.ApprovalEgress{ID: a.Egress.ID, Targets: mapSlice(a.Egress.Targets, egressTargetOut), Danger: a.Egress.Danger, Unchecked: a.Egress.Unchecked}
	}
	return out
}

func ApprovalIn(a rpcwire.LocalApproval) api.LocalApproval {
	out := api.LocalApproval{ID: a.ID, CharacterID: a.CharacterID, Key: KeyIn(a.Key), Title: a.Title, Tool: a.Tool, Subject: a.Subject,
		Why: a.Why, Purpose: a.Purpose, Preview: a.Preview, Workdir: a.Workdir, Notes: strs(a.Notes),
		Options: mapSlice(a.Options, func(s string) api.ApprovalChoice { return api.ApprovalChoice(s) }), KindLabel: a.KindLabel, ExpiresAt: a.ExpiresAt}
	if a.Whisper != nil {
		out.Whisper = &api.ApprovalWhisper{Source: KeyIn(a.Whisper.Source), Batch: a.Whisper.Batch, Character: a.Whisper.Character}
	}
	if a.Egress != nil {
		out.Egress = &api.ApprovalEgress{ID: a.Egress.ID, Targets: mapSlice(a.Egress.Targets, egressTargetIn), Danger: a.Egress.Danger, Unchecked: a.Egress.Unchecked}
	}
	return out
}

func ApprovalsOut(l []api.LocalApproval) []rpcwire.LocalApproval { return mapSlice(l, ApprovalOut) }
func ApprovalsIn(l []rpcwire.LocalApproval) []api.LocalApproval  { return mapSlice(l, ApprovalIn) }

func RuleOut(r api.ApprovalRule) rpcwire.ApprovalRule {
	return rpcwire.ApprovalRule{Scope: r.Scope, Key: r.Key, Label: r.Label, Tool: r.Tool, Effect: r.Effect, Pattern: r.Pattern,
		Project: r.Project, Session: r.Session, Conversation: r.Conversation, Dirs: strs(r.Dirs), At: r.At, LastUsed: r.LastUsed}
}

func RuleIn(r rpcwire.ApprovalRule) api.ApprovalRule {
	return api.ApprovalRule{Scope: r.Scope, Key: r.Key, Label: r.Label, Tool: r.Tool, Effect: r.Effect, Pattern: r.Pattern,
		Project: r.Project, Session: r.Session, Conversation: r.Conversation, Dirs: strs(r.Dirs), At: r.At, LastUsed: r.LastUsed}
}

func RulesOut(l []api.ApprovalRule) rpcwire.ApprovalRuleList {
	return rpcwire.ApprovalRuleList{Items: mapSlice(l, RuleOut)}
}
func RulesIn(l rpcwire.ApprovalRuleList) []api.ApprovalRule { return mapSlice(l.Items, RuleIn) }

func ApprovalModeOut(m api.ConversationApprovalMode) rpcwire.ConversationApprovalMode {
	return rpcwire.ConversationApprovalMode{Mode: m.Mode, Default: m.Default, Override: m.Override}
}

func ApprovalModeIn(m rpcwire.ConversationApprovalMode) api.ConversationApprovalMode {
	return api.ConversationApprovalMode{Mode: m.Mode, Default: m.Default, Override: m.Override}
}

// ───── 消息块、输入 ─────

func localPartOut(p api.LocalPart) rpcwire.LocalPart {
	out := rpcwire.LocalPart{Text: p.Text, Attachments: mapSlice(p.Attachments, attachmentOut), Mentions: strs(p.Mentions), All: p.All}
	if p.Quote != nil {
		out.Quote = &rpcwire.LocalQuote{Ref: RefOut(p.Quote.Ref), Text: p.Quote.Text, Start: p.Quote.Start, End: p.Quote.End}
	}
	return out
}

func localPartIn(p rpcwire.LocalPart) api.LocalPart {
	out := api.LocalPart{Text: p.Text, Attachments: mapSlice(p.Attachments, attachmentIn), Mentions: strs(p.Mentions), All: p.All}
	if p.Quote != nil {
		out.Quote = &api.LocalQuote{Ref: RefIn(p.Quote.Ref), Text: p.Quote.Text, Start: p.Quote.Start, End: p.Quote.End}
	}
	return out
}

func PartsOut(l []api.LocalPart) []rpcwire.LocalPart { return mapSlice(l, localPartOut) }
func PartsIn(l []rpcwire.LocalPart) []api.LocalPart  { return mapSlice(l, localPartIn) }

func SubmissionOut(s api.LocalInputSubmission) rpcwire.LocalInputSubmission {
	return rpcwire.LocalInputSubmission{Text: s.Text, Mode: s.Mode, ClientKey: s.ClientKey, ExpectedRunID: s.ExpectedRunID, Parts: PartsOut(s.Parts)}
}

func SubmissionIn(s rpcwire.LocalInputSubmission) api.LocalInputSubmission {
	return api.LocalInputSubmission{Text: s.Text, Mode: s.Mode, ClientKey: s.ClientKey, ExpectedRunID: s.ExpectedRunID, Parts: PartsIn(s.Parts)}
}

func AttachmentOut(a api.LocalAttachment) rpcwire.LocalAttachment { return attachmentOut(a) }
func AttachmentIn(a rpcwire.LocalAttachment) api.LocalAttachment  { return attachmentIn(a) }

func ReceiptOut(r api.InputReceipt) rpcwire.InputReceipt {
	return rpcwire.InputReceipt{InputID: r.InputID, RunID: r.RunID, State: r.State, Sequence: int64(r.Sequence), Revision: int64(r.Revision)}
}

func ReceiptIn(r rpcwire.InputReceipt) api.InputReceipt {
	return api.InputReceipt{InputID: r.InputID, RunID: r.RunID, State: r.State, Sequence: uint64(r.Sequence), Revision: uint64(r.Revision)}
}

func StopReceiptOut(r api.StopReceipt) rpcwire.StopReceipt {
	return rpcwire.StopReceipt{RunID: r.RunID, Revision: int64(r.Revision), Drained: r.Drained}
}

func StopReceiptIn(r rpcwire.StopReceipt) api.StopReceipt {
	return api.StopReceipt{RunID: r.RunID, Revision: uint64(r.Revision), Drained: r.Drained}
}

func RunOut(s api.LocalRunSnapshot) rpcwire.LocalRunSnapshot {
	return rpcwire.LocalRunSnapshot{Process: mapSlice(s.Process, stepOut), InputID: s.InputID, RootID: s.RootID, RunID: s.RunID, State: s.State,
		Outcome: s.Outcome, Text: s.Text, Revision: int64(s.Revision), Step: s.Step, SealedStep: s.SealedStep, Drained: s.Drained,
		ReasonClass: s.ReasonClass, ReasonDetail: s.ReasonDetail, ReasonModel: s.ReasonModel,
		Requeued: mapSlice(s.Requeued, func(r api.LocalRequeued) rpcwire.LocalRequeued {
			return rpcwire.LocalRequeued{InputID: r.InputID, RunID: r.RunID, Text: r.Text, Seq: int64(r.Seq)}
		})}
}

func RunIn(s rpcwire.LocalRunSnapshot) api.LocalRunSnapshot {
	return api.LocalRunSnapshot{Process: mapSlice(s.Process, stepIn), InputID: s.InputID, RootID: s.RootID, RunID: s.RunID, State: s.State,
		Outcome: s.Outcome, Text: s.Text, Revision: uint64(s.Revision), Step: s.Step, SealedStep: s.SealedStep, Drained: s.Drained,
		ReasonClass: s.ReasonClass, ReasonDetail: s.ReasonDetail, ReasonModel: s.ReasonModel,
		Requeued: mapSlice(s.Requeued, func(r rpcwire.LocalRequeued) api.LocalRequeued {
			return api.LocalRequeued{InputID: r.InputID, RunID: r.RunID, Text: r.Text, Seq: uint64(r.Seq)}
		})}
}

func HeldOut(l []api.HeldInput) rpcwire.HeldList {
	return rpcwire.HeldList{Items: mapSlice(l, func(h api.HeldInput) rpcwire.HeldInput {
		return rpcwire.HeldInput{Key: KeyOut(h.Key), InputID: h.InputID, Text: h.Text, AcceptedAt: h.AcceptedAt}
	})}
}

func HeldIn(l rpcwire.HeldList) []api.HeldInput {
	return mapSlice(l.Items, func(h rpcwire.HeldInput) api.HeldInput {
		return api.HeldInput{Key: KeyIn(h.Key), InputID: h.InputID, Text: h.Text, AcceptedAt: h.AcceptedAt}
	})
}

// ───── 提问卡 ─────

func questionOut(q api.LocalQuestion) rpcwire.LocalQuestion {
	return rpcwire.LocalQuestion{ID: q.ID, CharacterID: q.CharacterID, Key: KeyOut(q.Key), Text: q.Text, ExpiresAt: q.ExpiresAt,
		Options: mapSlice(q.Options, func(o api.LocalQuestionOption) rpcwire.LocalQuestionOption {
			return rpcwire.LocalQuestionOption{Label: o.Label, Detail: o.Detail}
		})}
}

func questionIn(q rpcwire.LocalQuestion) api.LocalQuestion {
	return api.LocalQuestion{ID: q.ID, CharacterID: q.CharacterID, Key: KeyIn(q.Key), Text: q.Text, ExpiresAt: q.ExpiresAt,
		Options: mapSlice(q.Options, func(o rpcwire.LocalQuestionOption) api.LocalQuestionOption {
			return api.LocalQuestionOption{Label: o.Label, Detail: o.Detail}
		})}
}

func QuestionsOut(l []api.LocalQuestion) []rpcwire.LocalQuestion { return mapSlice(l, questionOut) }
func QuestionsIn(l []rpcwire.LocalQuestion) []api.LocalQuestion  { return mapSlice(l, questionIn) }

// ───── 工作目录 ─────

func WorkspaceOut(w api.WorkspaceInfo) rpcwire.WorkspaceInfo {
	return rpcwire.WorkspaceInfo{CWD: w.CWD, Root: w.Root, Name: w.Name, Bound: w.Bound, Problem: w.Problem}
}

func WorkspaceIn(w rpcwire.WorkspaceInfo) api.WorkspaceInfo {
	return api.WorkspaceInfo{CWD: w.CWD, Root: w.Root, Name: w.Name, Bound: w.Bound, Problem: w.Problem}
}

// ───── 检查点 ─────

func CheckpointListOut(l []api.CheckpointSummary) rpcwire.CheckpointList {
	return rpcwire.CheckpointList{Items: mapSlice(l, func(c api.CheckpointSummary) rpcwire.CheckpointSummary {
		return rpcwire.CheckpointSummary{ID: c.ID, RunID: c.RunID, ToolID: c.ToolID, Tool: c.Tool, State: c.State, CreatedMS: c.CreatedMS, Files: c.Files, Bytes: c.Bytes, Child: c.Child, Note: c.Note}
	})}
}

func CheckpointListIn(l rpcwire.CheckpointList) []api.CheckpointSummary {
	return mapSlice(l.Items, func(c rpcwire.CheckpointSummary) api.CheckpointSummary {
		return api.CheckpointSummary{ID: c.ID, RunID: c.RunID, ToolID: c.ToolID, Tool: c.Tool, State: c.State, CreatedMS: c.CreatedMS, Files: c.Files, Bytes: c.Bytes, Child: c.Child, Note: c.Note}
	})
}

func CheckpointPlanOut(p api.CheckpointPlan) rpcwire.CheckpointPlan {
	return rpcwire.CheckpointPlan{ID: p.ID, Mode: p.Mode, Version: p.Version, Workspace: p.Workspace,
		Files: mapSlice(p.Files, func(f api.CheckpointFile) rpcwire.CheckpointFile {
			return rpcwire.CheckpointFile{Path: f.Path, TargetExists: f.TargetExists, CurrentExists: f.CurrentExists, TargetHash: f.TargetHash, CurrentHash: f.CurrentHash, TargetBytes: f.TargetBytes, CurrentBytes: f.CurrentBytes}
		})}
}

func CheckpointPlanIn(p rpcwire.CheckpointPlan) api.CheckpointPlan {
	return api.CheckpointPlan{ID: p.ID, Mode: p.Mode, Version: p.Version, Workspace: p.Workspace,
		Files: mapSlice(p.Files, func(f rpcwire.CheckpointFile) api.CheckpointFile {
			return api.CheckpointFile{Path: f.Path, TargetExists: f.TargetExists, CurrentExists: f.CurrentExists, TargetHash: f.TargetHash, CurrentHash: f.CurrentHash, TargetBytes: f.TargetBytes, CurrentBytes: f.CurrentBytes}
		})}
}

func CheckpointRestoreOut(r api.CheckpointRestore) rpcwire.CheckpointRestore {
	return rpcwire.CheckpointRestore{TargetID: r.TargetID, InverseID: r.InverseID, State: r.State, Completed: strs(r.Completed), Remaining: strs(r.Remaining)}
}

func CheckpointRestoreIn(r rpcwire.CheckpointRestore) api.CheckpointRestore {
	return api.CheckpointRestore{TargetID: r.TargetID, InverseID: r.InverseID, State: r.State, Completed: strs(r.Completed), Remaining: strs(r.Remaining)}
}

func CheckpointProgressOut(p api.CheckpointProgress) rpcwire.CheckpointProgress {
	return rpcwire.CheckpointProgress{TargetID: p.TargetID, InverseID: p.InverseID, State: p.State, Done: p.Done, Total: p.Total,
		Completed: strs(p.Completed), Remaining: strs(p.Remaining), OutcomeSaved: p.OutcomeSaved, Error: p.Error}
}

func CheckpointProgressIn(p rpcwire.CheckpointProgress) api.CheckpointProgress {
	return api.CheckpointProgress{TargetID: p.TargetID, InverseID: p.InverseID, State: p.State, Done: p.Done, Total: p.Total,
		Completed: strs(p.Completed), Remaining: strs(p.Remaining), OutcomeSaved: p.OutcomeSaved, Error: p.Error}
}

// ───── 转发 ─────

func ForwardRequestOut(r api.ForwardRequest) rpcwire.ForwardRequest {
	return rpcwire.ForwardRequest{Source: RefOut(r.Source), ID: r.ID, Via: strs(r.Via)}
}

func ForwardRequestIn(r rpcwire.ForwardRequest) api.ForwardRequest {
	return api.ForwardRequest{Source: RefIn(r.Source), ID: r.ID, Via: strs(r.Via)}
}

func ForwardOut(v api.ForwardView) rpcwire.ForwardView {
	return rpcwire.ForwardView{ID: v.ID, Group: v.Group, Missing: v.Missing, Fetched: v.Fetched, State: v.State, Depth: v.Depth,
		Nodes: mapSlice(v.Nodes, func(n api.ForwardNode) rpcwire.ForwardNode {
			return rpcwire.ForwardNode{UID: n.UID, Name: n.Name, At: n.At, Segments: mapSlice(n.Segments, segmentOut)}
		})}
}

func ForwardIn(v rpcwire.ForwardView) api.ForwardView {
	return api.ForwardView{ID: v.ID, Group: v.Group, Missing: v.Missing, Fetched: v.Fetched, State: v.State, Depth: v.Depth,
		Nodes: mapSlice(v.Nodes, func(n rpcwire.ForwardNode) api.ForwardNode {
			return api.ForwardNode{UID: n.UID, Name: n.Name, At: n.At, Segments: mapSlice(n.Segments, segmentIn)}
		})}
}
