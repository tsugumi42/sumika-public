package main

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/ctlproto"
)

func demoQuality() ctlproto.QualityReport {
	return ctlproto.QualityReport{
		Totals: ctlproto.QualityStats{Runs: 12, OK: 10, Failed: 1, Timeout: 1, FailRate: 2.0 / 12, P50Ms: 1200, P95Ms: 8000, Samples: 10},
		Rows: []ctlproto.QualityRow{
			{Kind: "model", Key: ctlproto.TaskForeground, Label: "前台回合", Wired: true, QualityStats: ctlproto.QualityStats{Runs: 12, OK: 10, Failed: 1, Timeout: 1, P50Ms: 1200, P95Ms: 8000, Samples: 10},
				Reasons: []ctlproto.QualityReason{{Class: "timeout", Count: 1, Share: .5}, {Class: "rate_limited", Count: 1, Share: .5}}},
			{Kind: "module", Key: ctlproto.ModuleBackfill, Label: "补录", Wired: false},
		},
		Slowest: []ctlproto.QualitySlow{{Kind: "model", Key: ctlproto.TaskForeground, Label: "前台回合", Model: "编造模型", P50Ms: 1200, P95Ms: 8000, Samples: 10, Thin: true}},
		Recent: []ctlproto.QualityFailure{
			{Kind: "model", Label: "前台回合", Model: "编造模型", Outcome: "timeout", Class: "timeout", Place: ctlproto.ScenePlaceLocalPrivate, Ms: 8000, AtMS: runNow.UnixMilli(), Summary: "编造的失败说明"},
			{Kind: "module", Label: "发送（QQ）", Outcome: "failed", Class: "retry_exhausted", Place: ctlproto.ScenePlaceQQGroup, Ms: -1, AtMS: runNow.UnixMilli()},
		},
		RecentOf: 2,
		Turns:    ctlproto.QualityTurns{Total: 12, Outcomes: []ctlproto.QualityOutcome{{Outcome: "done", Count: 10}, {Outcome: "error", Count: 2}}, P50Ms: 3000, P95Ms: 9000, Samples: 10},
		Tools:    []ctlproto.QualityTool{{Name: "read", Runs: 5, Done: 4, Failed: 1, MedianMs: 20, Samples: 4}},
	}
}

func TestRunQualityShowsEverything(t *testing.T) {
	f := &fakeStats{qual: demoQuality(), spans: ctlproto.SpansReport{Turns: 3, Stats: []ctlproto.SpanStat{{Kind: "model.wait", Name: "编造模型", Count: 3, TotalMs: 3000, P50Ms: 900, P95Ms: 1500, MaxMs: 1500}}}}
	h := newRunHarness(t, f, f, runTabQuality, 2048, 1030)
	h.need("失败率", "总表", "前台回合", "未接入", "最慢的环节", "每一步的耗时（3 轮）", "等第一个字节 · 编造模型", "工具", "最近的失败（共 2 条）", "完成 10", "出错 2")
	h.absent("编造的失败说明") // 选了才显示详情
	h.click("timeout")
	h.need("编造的失败说明")
}

// 重建表：每个场合一行，原因翻成中文；读不到不算这一页失败；没有记录不画这一块。
func TestRunQualityRebuild(t *testing.T) {
	rb := ctlproto.RebuildReport{
		Places: []ctlproto.RebuildRow{{Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 6, Untraced: 1, Counted: 4, NotCounted: 1, DroppedAvg: 10, DroppedMax: 25, Covered: 1,
			Reasons: []ctlproto.RebuildReasonCount{{Reason: "idle", Count: 3}, {Reason: "restart", Count: 2}}}},
		Rows: []ctlproto.RebuildRow{{Date: "2026-11-15", Place: ctlproto.ScenePlaceLocalPrivate, Rebuilds: 6, Counted: 4, DroppedAvg: 10, DroppedMax: 25}},
	}
	f := &fakeStats{qual: demoQuality(), rebuild: rb}
	h := newRunHarness(t, f, f, runTabQuality, 2048, 1030)
	h.need("重建（上下文从库里重新拼）", "本地私聊", "10.0 条", "25 条", "闲置超时 3，程序重启后第一次 2，没记原因的旧记录 1，没数出条数 1")
	f2 := &fakeStats{qual: demoQuality(), rebuildErr: errors.New("编造的错误")}
	h2 := newRunHarness(t, f2, f2, runTabQuality, 1100, 800)
	h2.need("失败率", "总表")
	h2.absent("重建（上下文从库里重新拼）", "没取到质量")
}

func TestRunQualityEmptyAndError(t *testing.T) {
	f := &fakeStats{}
	h := newRunHarness(t, f, f, runTabQuality, 1100, 800)
	h.need("这段时间没有记录")
	f2 := &fakeStats{qualErr: errors.New("编造的读取错误")}
	h2 := newRunHarness(t, f2, f2, runTabQuality, 1100, 800)
	h2.need("没取到质量", "编造的读取错误", "重试")
	f2.mu.Lock()
	f2.qualErr, f2.qual = nil, demoQuality()
	f2.mu.Unlock()
	h2.click("重试")
	h2.need("失败率")
}

// 耗时表读不到不算这一页失败。
func TestRunQualitySpansFailureNotFatal(t *testing.T) {
	f := &fakeStats{qual: demoQuality(), spansErr: errors.New("编造的错误")}
	h := newRunHarness(t, f, f, runTabQuality, 1100, 800)
	h.need("失败率", "总表")
	h.absent("每一步的耗时", "没取到质量")
}

// 换时间范围重新读；离开标签就不再订阅。
func TestRunQualityRangeAndUnsubscribe(t *testing.T) {
	f := &fakeStats{qual: demoQuality()}
	h := newRunHarness(t, f, f, runTabQuality, 1100, 800)
	before := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.qualityCalls }()
	h.click("最近 7 天")
	h.click("最近 30 天")
	if h.a.run.rangeKey != "30d" {
		t.Fatalf("范围没换：%q", h.a.run.rangeKey)
	}
	h.until("换范围重新读", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.qualityCalls > before })
	h.a.run.tab = runTabStatus
	h.frame()
	if h.a.run.cancel != nil {
		t.Fatal("离开质量标签后不该再订阅")
	}
}

func TestRunQualityLook(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("只在看样子时跑")
	}
	f := &fakeStats{qual: demoQuality(), spans: ctlproto.SpansReport{Turns: 3, Stats: []ctlproto.SpanStat{{Kind: "model.wait", Name: "编造模型", Count: 3, TotalMs: 3000, P50Ms: 900, P95Ms: 1500, MaxMs: 1500}}}}
	h := newRunHarness(t, f, f, runTabQuality, 2048, 1030)
	h.click("timeout")
	out, err := os.Create(filepath.Join(dir, "质量页.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// 用户钩子是模块表里的一行（规格-用户钩子 第六节）：成功也记，失败按原因分
func hooksQuality() ctlproto.QualityReport {
	return ctlproto.QualityReport{
		Totals: ctlproto.QualityStats{Runs: 9, OK: 5, Failed: 4, FailRate: 4.0 / 9, P50Ms: 900, P95Ms: 12000, Samples: 9},
		Rows: []ctlproto.QualityRow{
			{Kind: "module", Key: ctlproto.ModuleHooks, Label: "用户钩子", Wired: true, QualityStats: ctlproto.QualityStats{Runs: 9, OK: 5, Failed: 4, FailRate: 4.0 / 9, P50Ms: 900, P95Ms: 12000, Samples: 9},
				Reasons: []ctlproto.QualityReason{{Class: "check_failed", Count: 3, Share: .75}, {Class: "exit_nonzero", Count: 1, Share: .25}}},
		},
		Recent: []ctlproto.QualityFailure{
			{Kind: "module", Label: "用户钩子", Outcome: "failed", Class: "check_failed", Place: ctlproto.ScenePlaceLocalPrivate, Ms: 11800, AtMS: runNow.UnixMilli(), Summary: "Stop · go vet ./... · 退出码 1 · # hookdemo"},
		},
		RecentOf: 1,
	}
}

func TestRunQualityShowsHooksRow(t *testing.T) {
	f := &fakeStats{qual: hooksQuality()}
	h := newRunHarness(t, f, f, runTabQuality, 2048, 1030)
	h.need("用户钩子", "check_failed")
	h.absent("未接入")
	h.click("check_failed")
	h.need("go vet ./...")
}

func TestRunQualityHooksLook(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("只在看样子时跑")
	}
	f := &fakeStats{qual: hooksQuality()}
	h := newRunHarness(t, f, f, runTabQuality, 2048, 1030)
	h.click("check_failed")
	out, err := os.Create(filepath.Join(dir, "质量页-用户钩子.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}
