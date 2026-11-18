package resstat

import (
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"

	"sumika-native/internal/ctlproto"
)

func selfPID() int { return os.Getpid() }

func selfName() string {
	if p, err := os.Executable(); err == nil {
		return filepath.Base(p)
	}
	return ""
}

// 读主程序内部要的几个 runtime/metrics 名字（规格第四节第 3 条的表）。
// 用 runtime/metrics 而不是 runtime.ReadMemStats：后者每次读都要短暂停住整个程序（stop-the-world），页面每 2 秒开一次不值得
var runtimeMetricNames = []string{
	"/memory/classes/total:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/stacks:bytes",
	"/gc/heap/live:bytes",
	"/gc/gomemlimit:bytes",
	"/sched/goroutines:goroutines",
	"/gc/cycles/total:gc-cycles",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
	"/gc/pauses:seconds",
}

// readRuntime 读一次。某个指标这个 Go 版本没有（KindBad）就让对应字段保持零值或 nil，不影响别的
func readRuntime() ctlproto.RuntimeStats {
	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, n := range runtimeMetricNames {
		samples[i].Name = n
	}
	metrics.Read(samples)
	u := func(i int) int64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			v := samples[i].Value.Uint64()
			if v > math.MaxInt64 {
				return math.MaxInt64
			}
			return int64(v)
		}
		return 0
	}
	f := func(i int) (float64, bool) {
		if samples[i].Value.Kind() == metrics.KindFloat64 {
			return samples[i].Value.Float64(), true
		}
		return 0, false
	}
	rs := ctlproto.RuntimeStats{
		Total: u(0), HeapObj: u(1), Stacks: u(2), HeapLive: u(3),
		Goroutines: u(5), GCCycles: u(6),
	}
	if lim := u(4); lim > 0 && lim < math.MaxInt64 { // 没设内存上限时运行时给的是最大整数
		rs.MemLimit = &lim
	}
	if gc, ok1 := f(7); ok1 {
		if total, ok2 := f(8); ok2 && total > 0 {
			r := gc / total
			rs.GCCPU = &r
		}
	}
	if samples[9].Value.Kind() == metrics.KindFloat64Histogram {
		if ns, ok := histQuantileNs(samples[9].Value.Float64Histogram(), 0.99); ok {
			rs.PauseP99Ns = &ns
		}
	}
	return rs
}

// histQuantileNs 取直方图的分位数（秒换成纳秒）。取包含该分位的那个桶的上界；最后一个桶是无穷时用它的下界。一个样本都没有返回 false
func histQuantileNs(h *metrics.Float64Histogram, q float64) (int64, bool) {
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0, false
	}
	need := uint64(math.Ceil(q * float64(total)))
	var cum uint64
	for i, c := range h.Counts {
		cum += c
		if cum >= need {
			upper := h.Buckets[i+1]
			if math.IsInf(upper, 1) {
				upper = h.Buckets[i]
			}
			if math.IsInf(upper, -1) || upper < 0 {
				upper = 0
			}
			return int64(upper * 1e9), true
		}
	}
	return 0, false
}
