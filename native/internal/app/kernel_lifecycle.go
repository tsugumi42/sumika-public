package app

import (
	"context"
	"fmt"
	"time"

	"sumika-native/internal/turn"
)

// sealKernel 封入口并启动 Hub 的宽限期，不提前取消正在执行的工具。
func (a *App) sealKernel() {
	if a.responder != nil {
		a.responder.Stop()
	}
	if a.desktop != nil {
		a.desktop.sealChat()
	}
	if a.groups != nil {
		a.groups.Seal()
	}
	if a.hub != nil {
		a.hub.Seal()
	}
	if a.approvals != nil {
		a.approvals.CancelAll()
	}
}

func (a *App) drainKernel(ctx context.Context) error {
	if a.hub != nil {
		if err := a.hub.Drain(ctx); err != nil {
			a.drainFailed.Store(true)
			return fmt.Errorf("内核未排空，保留库和数据锁：%w", err)
		}
	}
	if a.approvals != nil {
		if n, err := a.approvals.Drain(ctx); err != nil || n != 0 {
			a.drainFailed.Store(true)
			return fmt.Errorf("审批收尾未完成：剩余 %d 张，错误：%w；保留库和数据锁", n, err)
		}
	}
	if a.desktop != nil {
		a.desktop.chatWork.Wait()
	}
	if a.groups != nil {
		a.groups.Wait()
	}
	if a.ecosystem != nil {
		if err := a.ecosystem.Close(ctx); err != nil {
			a.drainFailed.Store(true)
			return fmt.Errorf("生态未排空，保留库和数据锁：%w", err)
		}
	}
	if a.hubCancel != nil {
		a.hubCancel()
	}
	a.drainFailed.Store(false)
	return nil
}

func (a *App) lifecycleConfig() turn.LifecycleConfig {
	l := a.cfg.Agent.Lifecycle
	return turn.LifecycleConfig{MaxPendingPerConversation: l.MaxPendingPerConversation, MaxPendingGlobal: l.MaxPendingGlobal, DrainTimeout: time.Duration(l.DrainTimeoutMS) * time.Millisecond, StopGrace: time.Duration(l.StopGraceMS) * time.Millisecond, StopGraceSet: true, SnapshotInterval: time.Duration(l.SnapshotIntervalMS) * time.Millisecond}
}
