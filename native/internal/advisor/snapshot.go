// Package advisor 只审阅宿主冻结的工作材料，不接收角色 Live 或工具执行器。
package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/kernel"
)

type evidencePreview struct {
	ID, SHA256, Claim, Text string
	Start, End              int64
}
type snapshot struct {
	Goal                                               string
	Criteria, Constraints, Completed, Pending, Unknown []string
	Next                                               string
	Evidence                                           []evidencePreview
	Partial                                            bool
}

func validate(r kernel.ReviewRequest) error {
	i, s := r.Invocation, r.Invocation.Scope
	if i.RootID == "" || i.RunID == "" || i.Generation == 0 || r.CheckpointID == "" || s.CharacterID == "" || s.Conversation == "" || s.ScopeID == "" || s.AuthorityID == "" || s.AuthorityRevision == 0 || s.ActorKind == "" || s.ActorID == "" || s.ActorID == "0" || s.AccountMissing() || s.ActorIsAccount() {
		return fmt.Errorf("缺少可信审阅身份")
	}
	if (s.Scene != "private" && s.Scene != "group") || (s.Channel != "console" && s.Channel != "qq") || s.Platform == "" {
		return fmt.Errorf("未知审阅场合")
	}
	if r.Work.RootID != i.RootID || r.Work.Owner != s || strings.TrimSpace(r.Work.Goal) == "" {
		return fmt.Errorf("工作与审阅域不匹配")
	}
	switch i.Kind {
	case "foreground", "child", "advisor", "verify", "compact", "memory", "guard":
	default:
		return fmt.Errorf("未知运行用途")
	}
	switch r.Trigger {
	case "goal_changed", "plan_changed", "failure", "candidate":
	default:
		return fmt.Errorf("未知检查点")
	}
	return nil
}

// cursor 绑定工作、验收、权限和取消代次；换一次运行或传输不能重复审同一检查点。
func digest(r kernel.ReviewRequest) string {
	r.Invocation = kernel.Invocation{Scope: r.Invocation.Scope, RootID: r.Invocation.RootID, Generation: r.Invocation.Generation}
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func reviewID(r kernel.ReviewRequest, kind string) string { return kind + ":" + digest(r) }

func freeze(r kernel.ReviewRequest) kernel.ReviewRequest {
	r.Criteria = slices.Clone(r.Criteria)
	r.Evidence = slices.Clone(r.Evidence)
	r.Work.Constraints = slices.Clone(r.Work.Constraints)
	r.Work.Completed = slices.Clone(r.Work.Completed)
	r.Work.Pending = slices.Clone(r.Work.Pending)
	r.Work.Unknown = slices.Clone(r.Work.Unknown)
	r.Work.Evidence = slices.Clone(r.Work.Evidence)
	return r
}

func (s *Service) build(ctx context.Context, r kernel.ReviewRequest) (snapshot, error) {
	if err := validate(r); err != nil {
		return snapshot{}, err
	}
	if err := s.deps.Current(ctx, r); err != nil {
		return snapshot{}, err
	}
	x := snapshot{Goal: r.Work.Goal, Criteria: r.Criteria, Constraints: r.Work.Constraints, Completed: r.Work.Completed, Pending: r.Work.Pending, Unknown: r.Work.Unknown, Next: r.Work.Next}
	// UTF-8 字节数是保守 token 上界，整组加入，不在 JSON/证据的中间裁剪。
	fits := func() bool { b, _ := json.Marshal(x); return len(b)+len(reviewPrompt) <= s.cfg.Advisor.MaxInputTokens }
	if !fits() {
		return snapshot{}, fmt.Errorf("目标与验收超过审阅输入上限")
	}
	for _, e := range r.Evidence {
		text, err := s.readEvidence(ctx, r.Invocation.Scope, e)
		if err != nil {
			return snapshot{}, err
		}
		if e.End-e.Start > 4096 {
			x.Partial = true
		}
		p := evidencePreview{ID: e.Ref.ID, SHA256: e.Ref.SHA256, Claim: e.Claim, Text: text, Start: e.Start, End: e.End}
		x.Evidence = append(x.Evidence, p)
		if !fits() {
			x.Evidence = x.Evidence[:len(x.Evidence)-1]
			x.Partial = true
		}
	}
	return x, nil
}

func (s *Service) readEvidence(ctx context.Context, owner kernel.Scope, e kernel.Evidence) (string, error) {
	ref := e.Ref
	if ref.Owner != owner || ref.ID == "" || !ref.Complete || ref.Bytes <= 0 || e.Start < 0 || e.End <= e.Start || e.End > ref.Bytes {
		return "", fmt.Errorf("证据不完整或不属于当前域")
	}
	h := sha256.New()
	var preview strings.Builder
	previewEnd := min(e.End, e.Start+4096)
	startOK, endOK := false, e.End == ref.Bytes
	for offset := int64(0); offset < ref.Bytes; {
		// ResultStore 的 limit 包含包装；短原文和最后一页也要留出这部分预算。
		page, err := s.deps.Results.Read(ctx, owner, ref.ID, offset, min(s.deps.ReadPageBytes, int64(32768), ref.Bytes-offset+512))
		if err != nil {
			return "", err
		}
		if page.Ref != ref || page.Start != offset || page.End != offset+int64(len(page.Text)) || page.End <= offset || page.End > ref.Bytes || page.EOF != (page.End == ref.Bytes) {
			return "", fmt.Errorf("证据元数据或分页损坏")
		}
		h.Write([]byte(page.Text))
		if e.Start >= page.Start && e.Start < page.End {
			startOK = utf8.RuneStart(page.Text[e.Start-page.Start])
		}
		if e.End >= page.Start && e.End < page.End {
			endOK = utf8.RuneStart(page.Text[e.End-page.Start])
		}
		from, to := max(e.Start, page.Start), min(previewEnd, page.End)
		if from < to {
			preview.WriteString(page.Text[from-page.Start : to-page.Start])
		}
		offset = page.End
	}
	if hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return "", fmt.Errorf("证据 hash 不符")
	}
	if !startOK || !endOK {
		return "", fmt.Errorf("证据字节范围无效")
	}
	text := preview.String()
	if previewEnd < e.End {
		for !utf8.ValidString(text) && len(text) > 0 {
			text = text[:len(text)-1]
		}
		text += "\n[证据预览截断]"
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("证据不是 UTF-8 正文")
	}
	return text, nil
}
