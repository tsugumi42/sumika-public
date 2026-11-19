package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandleFiles(t *testing.T) {
	before, err := snapshotHandles()
	if err != nil {
		t.Fatal(err)
	}
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for range 3 {
		for range 10 {
			f, e := os.Create(filepath.Join(t.TempDir(), "leaked.txt"))
			if e != nil {
				t.Fatal(e)
			}
			files = append(files, f)
		}
		after, e := snapshotHandles()
		if e != nil {
			t.Fatal(e)
		}
		if settled(before, after) {
			t.Fatalf("漏关每会话一个文件未被发现：总数 %d→%d，线程 %d→%d，类型 %v→%v", before.Total, after.Total, before.Threads, after.Threads, before.Types, after.Types)
		}
		before = after
	}
	for _, f := range files {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	files = nil
	a := handleSnapshot{Total: 100, Threads: 10, Types: map[string]int{"File": 10, "Event": 10}}
	if !settled(a, a) {
		t.Fatal("稳定句柄误报")
	}
	b := handleSnapshot{Total: 106, Threads: 11, Types: map[string]int{"File": 10, "Event": 12, "Thread": 1, "IRTimer": 2, "WaitCompletionPacket": 1}}
	if !settled(a, b) {
		t.Fatal("新增线程的六个运行时句柄误报")
	}
	b.Types["Event"]++
	b.Total++
	if settled(a, b) {
		t.Fatal("超出线程预算的 Event 没有报错")
	}
}

func TestKernelHandles(t *testing.T) {
	if testing.Short() {
		t.Skip("压力和规模测试，放在每日整套")
	}
	var previous handleSnapshot
	for repeat := range 3 {
		k, err := newKernel(t.TempDir(), 2, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.close)
		// 等真实解析进程创建完再关闭，防止只测到尚未预热的空壳。等待用生产预热自己的一分钟时限
		// （Analyzer.Warmup），不另设更短的：整套并行时 pwsh 冷启动会超过十秒，那是负载，不是这个测试要查的泄漏。
		// 预热结束后再真问一句，拿不到结果就说明进程没起来，照样报红。
		start := time.Now()
		<-k.gate.Analyzer.Warmup()
		if a := k.gate.Analyzer.Analyze("Get-Date", time.Minute); a == nil {
			t.Fatalf("第 %d 次：解析器预热 %v 后仍问不出结果，进程没起来", repeat+1, time.Since(start))
		}
		t.Logf("第 %d 次：解析器就绪用了 %v", repeat+1, time.Since(start))
		k.close()
		after, err := snapshotHandles()
		if err != nil {
			t.Fatal(err)
		}
		if repeat > 0 {
			for _, name := range []string{"File", "Process"} {
				if after.Types[name] > previous.Types[name] {
					for _, path := range after.Files {
						t.Logf("关闭 File: %s", path)
					}
					t.Fatalf("第 %d 次关闭后 %s 仍增长：%d→%d", repeat+1, name, previous.Types[name], after.Types[name])
				}
			}
		}
		t.Logf("第 %d 次：File=%d Process=%d", repeat+1, after.Types["File"], after.Types["Process"])
		previous = after
	}
}

func TestHandleRun(t *testing.T) {
	if os.Getenv("SUMIKA_STRESS_HANDLES") != "1" {
		t.Skip("设置 SUMIKA_STRESS_HANDLES=1 实跑同档三次")
	}
	out := stressOutput(t)
	err := run(options{sessions: "10,50,100", characters: 4, turns: 10, tools: 3, steer: .05, stop: .05, compact: .05, out: out, diagnose: true})
	b, e := os.ReadFile(filepath.Join(out, "handles.md"))
	if e != nil {
		t.Fatal(e)
	}
	t.Log("\n" + string(b))
	if err != nil {
		t.Fatal(err)
	}
}
