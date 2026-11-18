package tokenizer

import (
	"runtime"
	"runtime/debug"
	"slices"
	"testing"
	"time"
)

// 加载内存和耗时（规格 GM1 验收 8）。真 tokenizer.json 才有意义：SUMIKA_TEST_MODELS_ROOT。
// 一次只加载一个分词器，每个量 3 遍取耗时中位数；内存是最后一遍加载前后（各 GC 两次、
// 把空闲页还给系统以后）HeapAlloc 和进程工作集的增量。读文件不算在里面，哈希核对和拍平算在里面（Load 的一部分）。
// 机器上别的进程抢 CPU 时墙钟耗时会虚高，所以同时打 CPU 时间。

func settle() {
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
}

func TestTokenizerMemory(t *testing.T) {
	for _, name := range []string{"gte", "bge"} {
		t.Run(name, func(t *testing.T) {
			raw := realTokenizerJSON(t, name)
			spec := realSpec(name)
			var times, cpus []time.Duration
			var heapDelta, wsDelta int64
			for range 3 {
				settle()
				var m0 runtime.MemStats
				runtime.ReadMemStats(&m0)
				w0 := workingSetBytes()
				t0, c0 := time.Now(), processCPU()
				tk, err := Load(spec, raw)
				times = append(times, time.Since(t0))
				cpus = append(cpus, processCPU()-c0)
				if err != nil {
					t.Fatal(err)
				}
				settle()
				var m1 runtime.MemStats
				runtime.ReadMemStats(&m1)
				heapDelta, wsDelta = int64(m1.HeapAlloc)-int64(m0.HeapAlloc), workingSetBytes()-w0
				runtime.KeepAlive(tk)
			}
			slices.Sort(times)
			slices.Sort(cpus)
			t.Logf("TOKMEM %s: 加载耗时中位 %d ms（三遍 %v），CPU 时间中位 %d ms，HeapAlloc 增量 %.1f MB，工作集增量 %.1f MB",
				name, times[1].Milliseconds(), times, cpus[1].Milliseconds(), float64(heapDelta)/1e6, float64(wsDelta)/1e6)
			// 常驻增量的上限：上游原样的 1/5（规格 2.4，≤ 360 MB），只拦"没改好"
			if heapDelta > 360e6 || wsDelta > 360e6 {
				t.Errorf("常驻增量超过 360 MB：HeapAlloc %.1f MB，工作集 %.1f MB", float64(heapDelta)/1e6, float64(wsDelta)/1e6)
			}
		})
	}
}
