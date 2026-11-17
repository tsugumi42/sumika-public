package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// report 是一次对拍的全部输出：一份 JSON（给机器读）加终端摘要，都只有计数和统计，没有样本文字。
type report struct {
	Generated string         `json:"generated"`
	Command   string         `json:"command"` // run 或 registry
	GoVersion string         `json:"goVersion"`
	Options   map[string]any `json:"options"`
	Counts    map[string]int `json:"counts,omitempty"` // 每个来源送了多少条
	Gates     map[string]any `json:"gates"`            // 写在命令里的门槛，随输出打印
	Rows      []Row          `json:"rows"`
	Residue   []int          `json:"residuePids,omitempty"`
	Started   int            `json:"serversStarted"`
	Summary   map[string]int `json:"summary"`
}

func gatesJSON() map[string]any {
	return map[string]any{
		"vecMaxDiff": vecMaxDiff, "vecDiffCeiling": vecDiffCeiling, "vecCosGate": vecCosGate, "registeredCos": registeredCos,
		"scoreMaxDiff": scoreMaxDiff, "logitMaxDiff": logitMaxDiff, "logitCeiling": logitCeiling,
		"simD": simD, "simDCeiling": simDCeiling, "nearBand": nearBand, "nearMinPairs": nearMinPairs,
		"speedP50Ratio": speedP50Ratio, "speedP95LimitMs": speedP95Limit, "speedBatchLimitMs": speedBatchLimit, "coldLoadLimitMs": coldLoadLimit,
		"rssLoadedLimitMiB": rssLoadedLimit, "rssSlackMiB": rssSlackMiB,
	}
}

func newReport(command string, opts map[string]any) *report {
	return &report{Generated: time.Now().Format("2006-01-02 15:04:05"), Command: command, GoVersion: runtime.Version(), Options: opts, Gates: gatesJSON()}
}

func (r *report) finish() {
	r.Summary = map[string]int{}
	for _, row := range r.Rows {
		r.Summary[row.Verdict]++
	}
}

// exitCode：有不过 → 1；没有不过、但有"没量够" → 3（不能当通过）；否则 0。"没跑"单独列在摘要里，不改退出码
// （比如没给 -python 时只跑 Go 这一边，摘要里一眼能看到）。
func (r *report) exitCode() int {
	switch {
	case r.Summary[vFail] > 0:
		return 1
	case r.Summary[vThin] > 0 || r.Summary[vWait] > 0:
		return 3
	}
	return 0
}

func (r *report) print(w io.Writer) {
	stages := []string{}
	byStage := map[string][]Row{}
	for _, row := range r.Rows {
		if _, ok := byStage[row.Stage]; !ok {
			stages = append(stages, row.Stage)
		}
		byStage[row.Stage] = append(byStage[row.Stage], row)
	}
	if len(r.Counts) > 0 {
		keys := make([]string, 0, len(r.Counts))
		for k := range r.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, r.Counts[k]))
		}
		fmt.Fprintf(w, "样本：%s\n", strings.Join(parts, "，"))
	}
	for _, st := range stages {
		fmt.Fprintf(w, "\n== %s ==\n", st)
		for _, row := range byStage[st] {
			fmt.Fprintf(w, "[%s] %s\n    %s", row.Verdict, row.What, row.Value)
			if row.Gate != "" {
				fmt.Fprintf(w, "\n    门槛：%s", row.Gate)
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintf(w, "\n汇总：")
	for _, v := range []string{vPass, vFail, vThin, vWait, vNotRun, vInfo} {
		if n := r.Summary[v]; n > 0 {
			fmt.Fprintf(w, " %s %d", v, n)
		}
	}
	fmt.Fprintf(w, "；起过 %d 份服务，收尾时残留进程 %d 个\n", r.Started, len(r.Residue))
}

func (r *report) writeJSON(path string) error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
