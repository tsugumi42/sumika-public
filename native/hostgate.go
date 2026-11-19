package main

// 宿主上的群档位操作（控制接口用，规格-本机控制接口）：在 op 锁下跑，因为 h.cur 和 h.mode 只有 op 下才稳定
// （正在切换时 cur 是空的）。真正的校验和写库在 backend.App 的窄接口里（internal/app/gatectl.go），这里只负责拿到当前的后台。

import (
	"context"
	"errors"

	backend "sumika-native/internal/app"
)

var errBackendDown = errors.New("后台没在运行，现在读不了也改不了群档位")

// gateList 列各群的档位。archive 和 receive 都行；正在切换时等切换做完（op 锁）
func (h *host) gateList(ctx context.Context) ([]backend.GateRow, error) {
	h.op.Lock()
	defer h.op.Unlock()
	if h.cur == nil {
		return nil, errBackendDown
	}
	return h.cur.GateList(ctx)
}

// gateSet 把一个群调成 off 或 dry。只看历史模式（archive）由 App 回只读错误，这里不另判断
func (h *host) gateSet(conversation, mode string) (backend.GateChange, error) {
	h.op.Lock()
	defer h.op.Unlock()
	if h.cur == nil {
		return backend.GateChange{}, errBackendDown
	}
	return h.cur.LowerGate(conversation, mode)
}
