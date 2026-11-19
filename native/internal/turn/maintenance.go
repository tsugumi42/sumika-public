package turn

import (
	"context"
	"strings"

	"sumika-native/internal/kernel"
	"sumika-native/internal/runstate"
)

// AcquireLocalMaintenance 与 Submit/StartInternal 共用准入锁；恢复不能让旧模型稍后继续写。
func (h *Hub) AcquireLocalMaintenance(ctx context.Context, scope kernel.Scope) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.Platform != "local" || scope.Channel != "console" || scope.Scene != "private" || scope.ActorKind != "owner" || strings.Contains(scope.Conversation, ":group:") {
		return nil, ErrInputAuthority
	}
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.sealed.Load() {
		return nil, runstate.ErrSealed
	}
	domain := convKey(scope.CharacterID, scope.Conversation)
	if lc.maintenance[domain] || len(lc.pending[domain]) > 0 {
		return nil, ErrInputBusy
	}
	for _, r := range lc.runs {
		if !r.snapshot.Drained && h.maintenanceDomainLocked(r.input.Identity) == domain {
			return nil, ErrInputBusy
		}
	}
	lc.maintenance[domain] = true
	return func() { lc.mu.Lock(); delete(lc.maintenance, domain); lc.mu.Unlock() }, nil
}

func (h *Hub) maintenanceDomainLocked(in kernel.Invocation) string {
	if r := h.lifecycle.runs[in.RunID]; r != nil && r.maintenanceDomain != "" {
		return r.maintenanceDomain
	}
	// 父身份只从宿主登记的运行读取；工具参数不能指定恢复 owner。
	for n := 0; in.Kind == kernel.KindChild && n < len(h.lifecycle.runs); n++ {
		parent := h.lifecycle.runs[in.ParentRunID]
		if parent == nil {
			return ""
		}
		if parent.maintenanceDomain != "" {
			return parent.maintenanceDomain
		}
		in = parent.input.Identity
	}
	return convKey(in.Scope.CharacterID, in.Scope.Conversation)
}
