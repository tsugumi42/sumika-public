package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type handleSnapshot struct {
	Total     uint64
	Threads   int
	Types     map[string]int
	Files     []string
	ThreadIDs map[uint32]int
}

// Go 的每条 Windows M 保留一个 Thread、两个 Event、两个 Timer 和一个等待包。
// 只扣除新增线程有源码依据的对象；File 和其余类型一律不能继续增长。
func settled(a, b handleSnapshot) bool {
	threads := max(0, b.Threads-a.Threads)
	for name, count := range b.Types {
		budget := 0
		switch name {
		case "Thread", "WaitCompletionPacket":
			budget = threads
		case "Event", "IRTimer":
			budget = 2 * threads
		}
		if count > a.Types[name]+budget {
			return false
		}
	}
	return b.Total <= a.Total+uint64(6*threads)
}

func diagnose(o options, tiers []int) error {
	var text strings.Builder
	var files strings.Builder
	text.WriteString("# B7 句柄重复运行诊断\n\n每档第一次预热；第二、三次比较上一次关闭后的采样。File 等非线程资源不得增长；新增线程仅允许每条一个 Thread、两个 Event、两个 IRTimer、一个 WaitCompletionPacket，总增量最多六个。依据：Go runtime/os_windows.go 的 semacreate/minit。该判定识别运行时留存，不代表线程数已经停止增长；默认线程保护上限为 10000。完整起止类型、负载指标在 handles.json，File 路径在表后。\n\n| 会话 | 次数 | 线程起→峰→终 | 句柄起→峰→终 | 结束类型 | 扣除线程资源后稳定 |\n|---:|---:|---:|---:|---|---|\n")
	passed := true
	var results []result
	var observations []struct {
		Sessions, Repeat int
		Before, After    handleSnapshot
		Workload         result
	}
	for _, n := range tiers {
		var previous handleSnapshot
		for repeat := 1; repeat <= 3; repeat++ {
			before, err := snapshotHandles()
			if err != nil {
				return err
			}
			r, err := runTier(o, n, filepath.Join(o.out, fmt.Sprintf("sessions-%d-run-%d", n, repeat)))
			if err != nil {
				return err
			}
			results = append(results, r)
			if err = report(o, results); err != nil {
				return err
			}
			runtime.GC()
			after, err := snapshotHandles()
			if err != nil {
				return err
			}
			stable := repeat == 1 || settled(previous, after)
			passed = passed && stable && r.Pass
			names := make([]string, 0, len(after.Types))
			for name := range after.Types {
				names = append(names, name)
			}
			sort.Strings(names)
			var types []string
			for _, name := range names {
				types = append(types, fmt.Sprintf("%s=%d", name, after.Types[name]))
			}
			observations = append(observations, struct {
				Sessions, Repeat int
				Before, After    handleSnapshot
				Workload         result
			}{n, repeat, before, after, r})
			data, e := json.MarshalIndent(observations, "", "  ")
			if e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(o.out, "handles.json"), data, 0644); e != nil {
				return e
			}
			fmt.Fprintf(&text, "| %d | %d | %d→%d→%d | %d→%d→%d | %s | %v |\n", n, repeat, before.Threads, r.Peak.Threads, after.Threads, before.Total, r.Peak.Handles, after.Total, strings.Join(types, ", "), stable)
			fmt.Printf("sessions=%d repeat=%d threads=%d handles=%d types=%v stable=%v workload=%v failures=%d stuck=%d isolation=%d accounting=%d errors=%v\n", n, repeat, after.Threads, after.Total, after.Types, stable, r.Pass, r.Failures, r.Stuck, len(r.Isolation), len(r.Accounting), r.Errors)
			for _, issues := range [][]string{r.Errors, r.Isolation, r.Accounting} {
				for _, e := range issues {
					fmt.Fprintf(&files, "\n- %d/%d 负载问题：%s\n", n, repeat, e)
				}
			}
			for _, file := range after.Files {
				fmt.Fprintf(&files, "\n- %d/%d File: `%s`\n", n, repeat, file)
			}
			previous = after
			if err = os.WriteFile(filepath.Join(o.out, "handles.md"), []byte(text.String()+files.String()), 0644); err != nil {
				return err
			}
		}
	}
	if !passed {
		return fmt.Errorf("重复运行未过线，见 handles.md")
	}
	return nil
}
