package channel

import (
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// S3（规格-运行时自检 B4）：一段按"结果未知"收场 → 计数 +1，计划状态照旧是 unknown（埋点不改行为）；确认的段不数
func TestSelfcheckReceiptUnknownCounted(t *testing.T) {
	t.Parallel()
	sc := selfcheck.NewCounters()
	h := newOH(t, types.Caps{}, func(c *OutletConfig) { c.Selfcheck = sc })
	h.run(nil)
	id := h.say("正常的一条")
	h.waitPlan(id, store.PlanDone)
	if n := sc.Get(selfcheck.OutletReceiptUnknown); n != 0 {
		t.Fatalf("确认了的段不该数无回执：%d", n)
	}
	h.unknownReceipts()
	id = h.say("没回执的一条")
	h.waitPlan(id, store.PlanUnknown)
	if n := sc.Get(selfcheck.OutletReceiptUnknown); n != 1 {
		t.Fatalf("结果未知应数 1 次：%d", n)
	}
}
