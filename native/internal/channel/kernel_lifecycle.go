package channel

import (
	"context"
	"fmt"
)

func (o *Outlet) linkPlan(ctx context.Context, pr *planRun) error {
	tr := o.cfg.Stores.TraceStore()
	if err := tr.LinkKernelDelivery(ctx, pr.id, pr.identity, "plan"); err != nil {
		return err
	}
	for i := range pr.parts {
		if err := tr.LinkKernelDelivery(ctx, partID(pr.id, i), pr.identity, "part"); err != nil {
			return err
		}
	}
	return nil
}
func (o *Outlet) linkReceipt(ctx context.Context, planID, partID, platformID string) error {
	if platformID == "" {
		return nil
	}
	in, err := o.cfg.Stores.TraceStore().KernelDeliveryIdentity(ctx, planID)
	if err != nil {
		return err
	}
	if in.RunID == "" {
		return nil
	}
	// 平台号只能在所属计划段下解释，不和另一账号/会话的同号回执冲突。
	return o.cfg.Stores.TraceStore().LinkKernelDelivery(ctx, fmt.Sprintf("receipt/%s/%s", partID, platformID), in, "receipt")
}
