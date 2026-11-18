package selfcheck_test

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/selfcheck"
)

// S14：没接 → unknown；没有 → ok；有 → bad，例子里有平台、轨、原因固定词和快照，备注写解除办法。
func TestStoreUnavailableCheck(t *testing.T) {
	c := findCheck(t, "store.unavailable", selfcheck.DefaultThresholds())
	eval := func(f func() []selfcheck.StoreUnavailable) selfcheck.Result {
		env := envAt(t0, selfcheck.NewCounters())
		env.StoreUnavailable = f
		return c.Eval(context.Background(), env)
	}
	if r := eval(nil); r.State != selfcheck.Unknown {
		t.Fatalf("没接平台库状态：%+v", r)
	}
	if r := eval(func() []selfcheck.StoreUnavailable { return nil }); r.State != selfcheck.Ok {
		t.Fatalf("没有不可用的平台：%+v", r)
	}
	r := eval(func() []selfcheck.StoreUnavailable {
		return []selfcheck.StoreUnavailable{{
			Platform: "qq", Track: "qq", Reason: "migrate_failed",
			Detail: "第 9 号迁移失败：编的", Snapshot: `C:\data\go\upgrade-snapshots\qq-failed.sqlite`,
		}}
	})
	if r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("有不可用的平台要 bad：%+v", r)
	}
	if r.Example["platform"] != "qq" || r.Example["track"] != "qq" || r.Example["reason"] != "migrate_failed" ||
		!strings.Contains(r.Example["snapshot"], "qq-failed.sqlite") {
		t.Fatalf("例子里缺平台、轨、原因或快照：%+v", r.Example)
	}
	if !strings.Contains(r.Next, "重启") || !strings.Contains(r.Next, "sumika-ctl db status") {
		t.Fatalf("下一步没写解除办法：%q", r.Next)
	}
}
