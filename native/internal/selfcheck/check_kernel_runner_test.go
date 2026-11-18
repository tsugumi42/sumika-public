package selfcheck

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 故障经 Runner 写进当天文件、sumika-ctl status 的那一行（Summary）列出 kernel.faulted；故障解除、再评估一次，那一行回到正常。
// 评估是周期性的（启动后先等 2 分钟，之后每 5 分钟），所以这一行最多晚一个周期；即时查看用 sumika-ctl kernel faults。
func TestKernelFaultShowsInSummary(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	var faults []KernelFault
	r := New(Config{Dir: dir, Counters: NewCounters(), Checks: []Check{kernelFaulted{}}, Now: clk.Now, StartDelay: -1,
		Faults: func() []KernelFault { return faults }})

	r.Evaluate(context.Background())
	if got := Summary(dir, clk.Now()); got != "正常" {
		t.Fatalf("没有故障该正常：%q", got)
	}

	faults = []KernelFault{{CharacterID: "c1", Conversation: "console:c1", Kind: "run_state", At: clk.Now(), Detail: "database is locked"}}
	clk.Add(time.Minute)
	r.Evaluate(context.Background())
	if got := Summary(dir, clk.Now()); !strings.Contains(got, "1 项异常（kernel.faulted）") {
		t.Fatalf("故障时状态行该列出 kernel.faulted：%q", got)
	}
	row := readFile(t, dir, "2026-11-13").Checks[0]
	if row.ID != "kernel.faulted" || row.State != Bad || row.Example["conversation"] != "console:c1" || row.Example["errorClass"] != StoreBusy {
		t.Fatalf("当天文件里的那一行：%+v", row)
	}

	faults = nil
	clk.Add(time.Minute)
	r.Evaluate(context.Background())
	if got := Summary(dir, clk.Now()); got != "正常" {
		t.Fatalf("故障解除后再评估一次该回到正常：%q", got)
	}
}
