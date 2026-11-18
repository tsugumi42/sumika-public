package channel

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/channel/types"
)

// AwaitPart 等这个来源发的某一段被回声认领（见 Outlet.AwaitPart）
func (o *AutomationOutlet) AwaitPart(ctx context.Context, planID string, idx int, wait time.Duration) (EchoNote, bool) {
	if o == nil || o.out == nil {
		return EchoNote{}, false
	}
	return o.out.AwaitPart(ctx, o.origin, planID, idx, wait)
}

// OnClaim 登记这个来源的认领回调：它发的段被回声认领时调（迟到的回声靠它通知）。回调在认领的锁里调，要快、不能再发东西
func (o *AutomationOutlet) OnClaim(fn func(EchoClaimed)) {
	if o != nil && o.out != nil {
		o.out.onClaim(o.origin, fn)
	}
}

// AutomationOutlet 由宿主注册后交给指定内置插件；调用方不能传另一个 origin 提权。
type AutomationOutlet struct {
	out    *Outlet
	origin string
	permit *originPermit
}
type originPermit struct{ identity byte }

func (o *Outlet) BindAutomation(id string) (*AutomationOutlet, error) {
	origin := types.OriginAutomationPrefix + id
	if e := checkOrigin(origin); e != nil {
		return nil, e
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.origins[origin]; ok {
		return nil, errors.New("自动化出口已经注册")
	}
	permit := &originPermit{}
	o.origins[origin] = permit
	return &AutomationOutlet{out: o, origin: origin, permit: permit}, nil
}
func (o *AutomationOutlet) Submit(ctx context.Context, character string, conv types.Conversation, parts []types.Part, resources []*types.MediaResource) (Held, error) {
	held, e := o.out.sendHeld(ctx, character, o.origin, conv, parts, resources, o.permit)
	if e != nil {
		return Held{}, e
	}
	if e = held.Start(); e != nil {
		return Held{}, e
	}
	return held, nil
}
func (o *AutomationOutlet) CancelPlan(id string) {
	o.out.mu.Lock()
	pr := o.out.plans[id]
	matches := pr != nil && pr.origin == o.origin
	o.out.mu.Unlock()
	if matches {
		o.out.CancelPlan(id)
	}
}
