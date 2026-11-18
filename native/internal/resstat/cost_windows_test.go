package resstat

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/runmetrics"
)

// 采样成本的实测（规格-运行页 第四节第 5 条的预算：页面打开时本进程 CPU 低于单核 1%，一次采样低于 20 毫秒）。
// 不做断言，只把数字打出来，写进提交说明；机器上别的东西也在跑，所以看量级。要跑设 SUMIKA_RES_COST=1（约 70 秒）。
// 量的是采样本身（进程表、运行时、队列、显存计数器），不含界面画一帧的开销。
func TestSampleCostReport(t *testing.T) {
	if os.Getenv("SUMIKA_RES_COST") == "" {
		t.Skip("手动量采样成本：设 SUMIKA_RES_COST=1")
	}
	sys := newSystem()
	selfCPU := func() time.Duration {
		in, err := sys.info(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		return in.cpu
	}
	for _, every := range []time.Duration{time.Second, 2 * time.Second} {
		s := New(Options{Queues: &runmetrics.Registry{}})
		ctx := context.Background()
		var durs []time.Duration
		var cpu0 time.Duration
		start := time.Now()
		for i := 0; time.Since(start) < 30*time.Second; i++ {
			t0 := time.Now()
			rep, err := s.Sample(ctx, true)
			d := time.Since(t0)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				t.Logf("间隔 %v：第一次采样 %v（GPU 还在准备：%v，进程表 %d 个进程）", every, d, rep.GPU.Pending, len(rep.Processes))
			} else {
				durs = append(durs, d)
			}
			if i == 2 { // 前两次含打开性能计数器，不算稳态；从第三次起量 CPU
				cpu0, start = selfCPU(), time.Now()
			}
			time.Sleep(every)
		}
		wall := time.Since(start)
		used := selfCPU() - cpu0
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		t.Logf("间隔 %v：稳态 %d 次，单次用时 中位 %v、最大 %v；这 %v 里本进程 CPU %v，占单核 %.3f%%",
			every, len(durs), durs[len(durs)/2], durs[len(durs)-1], wall.Round(time.Second), used, float64(used)/float64(wall)*100)
		s.Forget()
	}
}
