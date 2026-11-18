package workcontext

import (
	"context"
	"encoding/json"
	"fmt"

	"sumika-native/internal/kernel"
)

type Candidate struct {
	State         kernel.WorkState
	Expected      uint64
	Stamp, Digest string
}

// Candidate 保留最新完整组；结构化目标/清单由宿主折叠，不交给自由摘要取舍。
func (s *Service) Candidate(ctx context.Context, inv kernel.Invocation) (Candidate, error) {
	head, err := s.DB.LoadWork(ctx, inv)
	if err != nil {
		return Candidate{}, err
	}
	_, entries, err := s.Load(ctx, inv)
	if err != nil {
		return Candidate{}, err
	}
	total := 0
	for _, e := range entries {
		total += e.Tokens
	}
	state := head.State
	through := state.ThroughOrdinal
	open := map[string]int{}
	for i, e := range entries {
		if total <= int(s.Limits.KeepRecentTokens) || i == len(entries)-1 {
			break
		}
		total -= e.Tokens
		if e.Calls > 0 {
			open[e.Group] = e.Calls
		}
		if e.Result && open[e.Group] > 0 {
			open[e.Group]--
			if open[e.Group] == 0 {
				delete(open, e.Group)
			}
		}
		if e.Boundary && len(open) == 0 {
			through = head.State.ThroughOrdinal + uint64(i+1)
		}
	}
	for i, e := range entries {
		if head.State.ThroughOrdinal+uint64(i+1) > through {
			break
		}
		apply(&state, e.Delta)
	}
	state.ThroughOrdinal = through
	digest, err := s.DB.WorkDigest(ctx, inv, through)
	return Candidate{state, head.State.Revision, head.Stamp, digest}, err
}
func (s *Service) Install(ctx context.Context, inv kernel.Invocation, c Candidate) error {
	if c.State.RootID != inv.RootID || c.State.Owner != inv.Scope {
		return fmt.Errorf("工作候选归属不符")
	}
	for _, e := range c.State.Evidence {
		if err := s.validateEvidence(ctx, inv.Scope, e); err != nil {
			return err
		}
	}
	b, err := json.Marshal(c.State)
	if err != nil {
		return err
	}
	if len(b) > int(s.Limits.SummaryMaxTokens)*4 {
		return fmt.Errorf("工作交接超过结构化摘要预算；保留原记录，未安装候选")
	}
	return s.DB.SaveWork(ctx, inv, c.Expected, c.Stamp, c.Digest, c.State)
}
