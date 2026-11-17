package resstat

import (
	"math"
	"runtime"
	"runtime/metrics"
	"testing"
)

func TestReadRuntimeIsSane(t *testing.T) {
	runtime.GC()
	rs := readRuntime()
	if rs.Total <= 0 || rs.HeapObj <= 0 || rs.Goroutines <= 0 || rs.GCCycles <= 0 {
		t.Fatalf("运行时读数不对：%+v", rs)
	}
	if rs.Total < rs.HeapObj {
		t.Fatalf("总量不该小于堆对象：%+v", rs)
	}
	if rs.PauseP99Ns == nil || *rs.PauseP99Ns < 0 {
		t.Fatalf("GC 过一次以后暂停 p99 该有值：%+v", rs.PauseP99Ns)
	}
	if rs.GCCPU != nil && (*rs.GCCPU < 0 || *rs.GCCPU > 1) {
		t.Fatalf("GC 占比要在 0 到 1：%v", *rs.GCCPU)
	}
	if rs.MemLimit != nil && *rs.MemLimit == math.MaxInt64 {
		t.Fatal("没设内存上限时必须是 nil，不能是最大整数")
	}
}

// 用到的每个指标名在这个 Go 版本里都认识：改了版本有指标没了，这条先红，不是页面悄悄少一行
func TestRuntimeMetricNamesAreKnown(t *testing.T) {
	known := map[string]bool{}
	for _, d := range metrics.All() {
		known[d.Name] = true
	}
	for _, n := range runtimeMetricNames {
		if !known[n] {
			t.Fatalf("这个 Go 版本没有指标 %s", n)
		}
	}
}

func TestHistQuantile(t *testing.T) {
	h := &metrics.Float64Histogram{Counts: []uint64{0, 98, 1, 1}, Buckets: []float64{0, 0.001, 0.002, 0.004, math.Inf(1)}}
	// 100 个样本，p99 落在第三个桶（0.002 到 0.004）：取上界 4 毫秒
	if ns, ok := histQuantileNs(h, 0.99); !ok || ns != 4_000_000 {
		t.Fatalf("p99：%d %v", ns, ok)
	}
	// p50 在第二个桶：上界 2 毫秒
	if ns, ok := histQuantileNs(h, 0.5); !ok || ns != 2_000_000 {
		t.Fatalf("p50：%d %v", ns, ok)
	}
	// 落在最后一个无穷桶：用它的下界，不返回无穷
	h2 := &metrics.Float64Histogram{Counts: []uint64{0, 1}, Buckets: []float64{0, 0.5, math.Inf(1)}}
	if ns, ok := histQuantileNs(h2, 0.99); !ok || ns != 500_000_000 {
		t.Fatalf("无穷桶：%d %v", ns, ok)
	}
	if _, ok := histQuantileNs(&metrics.Float64Histogram{Counts: []uint64{0, 0}, Buckets: []float64{0, 1, 2}}, 0.99); ok {
		t.Fatal("一个样本都没有不能编数")
	}
}
