package main

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// stats rebuild 的中文输出（测试里造的数据）：按场合合计、按天；原因翻成中文；旧记录、没数出来的单列；没数的不拿 0 当平均
func TestRenderRebuild(t *testing.T) {
	rep := ctlproto.RebuildReport{
		Range: ctlproto.UsageRange{Kind: "days", Label: "最近 7 天"},
		Places: []ctlproto.RebuildRow{
			{Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 6, Untraced: 1, Counted: 4, NotCounted: 1, DroppedTotal: 40, DroppedAvg: 10, DroppedMax: 25, KeptAvg: 31.5, Covered: 1,
				Reasons: []ctlproto.RebuildReasonCount{{Reason: "idle", Count: 3}, {Reason: "restart", Count: 2}}},
			{Place: ctlproto.ScenePlaceQQGroup, Rebuilds: 2, Counted: 2, KeptAvg: 30, DroppedAvg: 0, Reasons: []ctlproto.RebuildReasonCount{{Reason: "新来的标识", Count: 2}}},
		},
		Rows: []ctlproto.RebuildRow{
			{Date: "2026-11-15", Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 4, Counted: 4, DroppedAvg: 10, DroppedMax: 25, KeptAvg: 31.5, Reasons: []ctlproto.RebuildReasonCount{{Reason: "idle", Count: 4}}},
			{Date: "2026-11-15", Place: ctlproto.ScenePlaceQQGroup, Rebuilds: 2, Counted: 2, KeptAvg: 30},
			{Date: "2026-11-14", Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 2, Untraced: 1, NotCounted: 1},
		},
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	renderRebuild(&out, &errOut, ctlproto.Reply{OK: true, Data: data})
	got := out.String()
	for _, want := range []string{
		"重建留痕（最近 7 天",
		"本地私聊  重建 6 次；平均带上 31.5 条、丢 10.0 条，最多丢 25 条；1 次丢掉的有摘要覆盖；1 次没数出丢了几条（不算进平均）；1 次是没记原因的旧记录",
		"原因：闲置超时 3 次，程序重启后第一次 2 次",
		"QQ 群  重建 2 次；平均带上 30.0 条、丢 0.0 条，最多丢 0 条；没有摘要覆盖",
		"原因：新来的标识 2 次", // 不认识的标识原样显示，不当成别的原因
		"2026-11-15", "2026-11-14",
		"本地私聊  重建 2 次；1 次没数出丢了几条（不算进平均）；1 次是没记原因的旧记录", // 一条都没数出来：不写平均
	} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：\n%s", want, got)
		}
	}
	if strings.Count(got, "2026-11-15") != 1 {
		t.Errorf("同一天只写一次日期：\n%s", got)
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}

	out.Reset()
	empty, _ := json.Marshal(ctlproto.RebuildReport{Range: ctlproto.UsageRange{Label: "最近 7 天"}})
	renderRebuild(&out, &errOut, ctlproto.Reply{OK: true, Data: empty})
	if !strings.Contains(out.String(), "没有重建记录") {
		t.Fatalf("没有记录要说没有：%q", out.String())
	}
}

// 命令行认得 stats rebuild 和它的选项，请求原样交给服务端校验
func TestParseStatsRebuild(t *testing.T) {
	o, err := parseArgs([]string{"stats", "rebuild", "--days", "3"})
	if err != nil || o.req.Cmd != ctlproto.CmdStats || strings.Join(o.req.Args, "|") != "rebuild" || o.req.Stats == nil || o.req.Stats.Days != 3 {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"stats", "rebuild", "--all"})
	if err != nil || o.req.Stats == nil || !o.req.Stats.All {
		t.Fatalf("%v %+v", err, o.req)
	}
}
