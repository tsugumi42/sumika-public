package runstats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 重建留痕的汇总：按天、按场合；旧记录（没有 rebuild 键）单列；没数出来的不进平均和最大；会话串不出现在报告里
func TestRebuildReport(t *testing.T) {
	db, file := tempTrace(t)
	add := func(stage, conv string, at int64, rebuild, ctx string) {
		t.Helper()
		var c any
		if ctx != "" {
			c = ctx
		}
		var rb any
		if rebuild != "" {
			rb = rebuild
		}
		if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, output_tokens, cache_read_tokens,
 sections_json, injected_json, rebuild, context_json) VALUES (?, 'c', ?, ?, 'm', ?, 1, 1, 1, 0, '[]', '[]', ?, ?)`, fmt.Sprintf("rb%09d", nextID.Add(1)), conv, stage, at, rb, c); err != nil {
			t.Fatal(err)
		}
	}
	trace := func(reason string, kept, dropped, covered int, count string) string {
		b, _ := json.Marshal(map[string]any{"rebuild": map[string]any{"reason": reason, "kept": kept, "dropped": dropped, "covered": covered, "count": count, "snapshot": covered > 0}, "reset": nil})
		return string(b)
	}
	d7 := ms(2026, 11, 7, 10, 0, 0, 0)
	d8 := ms(2026, 11, 8, 10, 0, 0, 0)
	const local, group, qqp = "console:c", "qq:group:12345", "qq:private:67890:11111"
	// 11-07 本地私聊：闲置丢 12 条、闲置丢 4 条（有摘要覆盖）、一次没数出来
	add("reply", local, d7, "fresh", trace("idle", 30, 12, 0, "exact"))
	add("reply", local, d7+1, "compact", trace("idle", 30, 4, 4, "exact"))
	add("reply", local, d7+2, "fresh", trace("restart", 0, 0, 0, "failed"))
	// 11-08：本地私聊一次旧记录、QQ 群两次、QQ 私聊一次
	add("reply", local, d8, "fresh", "")
	add("reply", group, d8, "fresh", trace("evicted", 30, 20, 0, "exact"))
	add("reply", group, d8+1, "fresh", trace("model", 30, 2, 0, "exact"))
	add("reply", qqp, d8, "fresh", trace("first", 0, 0, 0, "exact"))
	// 不算的：非回复阶段、第一步之后（rebuild 列为空）、范围之外、context_json 不是 JSON 的旧行当旧记录
	add("compact", local, d8, "fresh", trace("idle", 1, 1, 0, "exact"))
	add("reply", local, d8+5, "", trace("idle", 1, 1, 0, "exact"))
	add("reply", local, ms(2026, 11, 20, 10, 0, 0, 0), "fresh", trace("idle", 1, 1, 0, "exact"))
	add("reply", local, d8+9, "fresh", "不是 JSON")

	svc := openService(t, file)
	rng, err := svc.RangeOf(ctlproto.StatsArgs{Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Rebuild(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Places) != 3 || rep.Places[0].Place != ctlproto.ScenePlaceLocalPrivate || rep.Places[1].Place != ctlproto.ScenePlaceQQPrivate || rep.Places[2].Place != ctlproto.ScenePlaceQQGroup {
		t.Fatalf("场合按固定顺序：%+v", rep.Places)
	}
	lp := rep.Places[0]
	// 本地私聊 7 天合计：3 条有留痕 + 2 条旧记录（没有留痕、不是 JSON）
	if lp.Rebuilds != 5 || lp.Untraced != 2 || lp.Counted != 2 || lp.NotCounted != 1 || lp.DroppedTotal != 16 || lp.DroppedAvg != 8 || lp.DroppedMax != 12 || lp.Covered != 1 || lp.KeptAvg != 30 {
		t.Fatalf("本地私聊合计：%+v", lp)
	}
	if len(lp.Reasons) != 2 || lp.Reasons[0].Reason != "idle" || lp.Reasons[0].Count != 2 || lp.Reasons[1].Reason != "restart" {
		t.Fatalf("旧记录不进原因，也不并进 unknown：%+v", lp.Reasons)
	}
	if g := rep.Places[2]; g.Rebuilds != 2 || g.Counted != 2 || g.DroppedAvg != 11 || g.DroppedMax != 20 || g.Covered != 0 {
		t.Fatalf("QQ 群合计：%+v", g)
	}
	if len(rep.Rows) != 4 || rep.Rows[0].Date != "2026-11-08" || rep.Rows[0].Place != ctlproto.ScenePlaceLocalPrivate || rep.Rows[3].Date != "2026-11-07" {
		t.Fatalf("按天（新的在前）再按场合：%+v", rep.Rows)
	}
	if rep.Rows[3].Rebuilds != 3 || rep.Rows[3].DroppedAvg != 8 {
		t.Fatalf("11-07 本地私聊：%+v", rep.Rows[3])
	}
	// 报告里没有会话串、号码
	raw, _ := json.Marshal(rep)
	for _, bad := range []string{"12345", "67890", "console:c", "qq:"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("报告里不该有 %q：%s", bad, raw)
		}
	}
	// 空范围：没有行，不报错
	empty, err := svc.Rebuild(context.Background(), ctlproto.UsageRange{FromMS: 1, ToMS: 2})
	if err != nil || len(empty.Places) != 0 || len(empty.Rows) != 0 {
		t.Fatalf("没有记录：%+v %v", empty, err)
	}
	if _, err := svc.Rebuild(context.Background(), ctlproto.UsageRange{FromMS: 5, ToMS: 5}); err == nil {
		t.Fatal("时间范围不对要报错")
	}
}
