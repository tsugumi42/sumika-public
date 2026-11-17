package workcontext

import (
	"context"
	"encoding/json"
	"fmt"

	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

// Load 固定一次高水位；分页期间新追加的内容留给下一次请求。
func (s *Service) Load(ctx context.Context, inv kernel.Invocation) (kernel.WorkState, []Entry, error) {
	head, err := s.DB.LoadWork(ctx, inv)
	if err != nil {
		return kernel.WorkState{}, nil, err
	}
	state := head.State
	for _, e := range state.Evidence {
		if err := s.validateEvidence(ctx, inv.Scope, e); err != nil {
			return state, nil, err
		}
	}
	var out []Entry
	after := state.ThroughOrdinal
	for after < head.Ordinal {
		rows, err := s.DB.WorkEntries(ctx, inv, after, head.Ordinal, int(s.Limits.RecoveryPageSize))
		if err != nil {
			return state, nil, err
		}
		if len(rows) == 0 {
			return state, nil, fmt.Errorf("工作记录存在缺口，未恢复")
		}
		for _, row := range rows {
			var e Entry
			if err := json.Unmarshal(row.Payload, &e); err != nil {
				return state, nil, err
			}
			for _, ref := range e.Delta.Evidence {
				if err := s.validateEvidence(ctx, inv.Scope, ref); err != nil {
					return state, nil, err
				}
			}
			apply(&state, e.Delta)
			e.RunID = row.RunID
			out = append(out, e)
			after = row.Ordinal
		}
	}
	return state, out, nil
}

// RootEntries 读一个旧根折叠线之后的条目，供跨根恢复用：只读，不校验证据文件（缺了只影响 result_read 的回答），
// 一条解不开就整根报错，由调用方跳过这一根。
func (s *Service) RootEntries(ctx context.Context, owner kernel.Scope, root store.WorkRoot) ([]Entry, error) {
	inv := kernel.Invocation{RootID: root.RootID, Scope: owner}
	var out []Entry
	after := root.ThroughOrdinal
	for after < root.Ordinal {
		rows, err := s.DB.WorkEntries(ctx, inv, after, root.Ordinal, int(s.Limits.RecoveryPageSize))
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("工作记录存在缺口，未恢复")
		}
		for _, row := range rows {
			var e Entry
			if err := json.Unmarshal(row.Payload, &e); err != nil {
				return nil, err
			}
			e.RunID = row.RunID
			out = append(out, e)
			after = row.Ordinal
		}
	}
	return out, nil
}
