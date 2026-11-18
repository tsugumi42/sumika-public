package channel

import (
	"sumika-native/internal/channel/types"
)

func (o *Outlet) releaseResources(resources []*types.MediaResource) {
	for _, r := range resources {
		if err := r.Release(); err != nil {
			o.logf("出站资源释放失败：%v", err)
		}
	}
}

// 逻辑终态可能早于 Write 退出；只有实际调用全部退出后才归还资源。
func (o *Outlet) finishPlan(pr *planRun) {
	pr.finished.Do(func() {
		pr.cancel()
		o.mu.Lock()
		delete(o.plans, pr.id)
		o.mu.Unlock()
		if len(pr.resources) > 0 {
			go func() { pr.calls.Wait(); o.releaseResources(pr.resources); close(pr.done) }()
		} else {
			close(pr.done)
		}
	})
}

// CancelPlan 只取消未发部分，适配器中的动作仍按真实回执收尾。
func (o *Outlet) CancelPlan(id string) {
	o.mu.Lock()
	pr := o.plans[id]
	if pr != nil {
		pr.cancel()
	}
	o.mu.Unlock()
	if pr != nil {
		o.abortRest(pr, "计划已取消，没发的段中止")
	}
}
