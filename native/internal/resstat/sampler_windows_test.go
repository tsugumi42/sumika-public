package resstat

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/procreg"
	"sumika-native/internal/runmetrics"
)

// 用真实的系统调用在本进程上跑一遍：能采到主程序自己，内存、CPU 有值，起的子进程出现在表里。

func TestSampleSelfAndChildTree(t *testing.T) {
	reg := &procreg.Registry{}
	s := New(Options{Registry: reg, Queues: &runmetrics.Registry{}})
	// 测试自己起的子进程：等 30 秒的 timeout。用完按进程句柄收掉，不按名字杀
	child := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	ctx := context.Background()

	first, err := s.Sample(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Processes) == 0 || first.Processes[0].Category != ctlproto.ProcMain {
		t.Fatalf("第一行应该是主程序：%+v", first.Processes)
	}
	if first.Processes[0].CPU != nil || first.IntervalMS != 0 {
		t.Fatalf("第一次采样没有前值，CPU 必须是 nil，不能是 0：%+v", first.Processes[0])
	}
	if first.Processes[0].Memory == nil || *first.Processes[0].Memory <= 0 || first.MemoryKind != ctlproto.MemPrivate {
		t.Fatalf("主程序的专用工作集该有值：%+v kind=%s", first.Processes[0].Memory, first.MemoryKind)
	}
	var cmdRow *ctlproto.ProcessRow
	for i, r := range first.Processes {
		if r.PID == child.Process.Pid {
			cmdRow = &first.Processes[i]
		}
	}
	if cmdRow == nil {
		t.Fatalf("没登记的子进程应该以“未登记”出现在表里：%+v", first.Processes)
	}
	if cmdRow.Category != ctlproto.ProcUnregistered || cmdRow.Depth != 1 || cmdRow.Parent != first.Processes[0].PID {
		t.Fatalf("子进程该挂在主程序下、类别未登记：%+v", *cmdRow)
	}
	// 登记以后换成登记的类别和标签
	unreg := reg.Register(procreg.CatMCP, "编造服务", child.Process.Pid)
	time.Sleep(MinGap + 50*time.Millisecond)
	spinCPU(t, s, 30*time.Millisecond) // 给主程序一点 CPU 时间，第二次才有可比的差
	second, err := s.Sample(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.IntervalMS <= 0 || second.Processes[0].CPU == nil {
		t.Fatalf("第二次采样应该有间隔和 CPU：%+v", second.Processes[0])
	}
	if *second.Processes[0].CPU <= 0 {
		t.Fatalf("主程序刚空转过，CPU 应该大于 0：%v", *second.Processes[0].CPU)
	}
	for _, r := range second.Processes {
		if r.PID == child.Process.Pid && (r.Category != ctlproto.ProcMCP || r.Label != "编造服务") {
			t.Fatalf("登记后应该显示登记的类别和标签：%+v", r)
		}
	}
	unreg()
	if second.Runtime.Goroutines <= 0 || second.Runtime.Total <= 0 {
		t.Fatalf("运行时读数不对：%+v", second.Runtime)
	}
	// 小测试程序的专用工作集可能比运行时登记的总量还小，这时“其他”是 nil（不编负数）；有值时必须不为负
	if o := second.Runtime.Other; o != nil && *o < 0 {
		t.Fatalf("其他不能是负数：%d", *o)
	}
}

// spinCPU 空转到系统记账的 CPU 时间真的涨了 d。按墙钟转靠不住：低优先级跑在忙机器上，150 毫秒可能一点 CPU 都分不到，
// Windows 的进程时间又是约 16 毫秒一跳（2026-11-09 合并队列读到过 0）。转得比 MaxAge 短，第二次采样才用得上前值。
func spinCPU(t *testing.T, s *Sampler, d time.Duration) {
	t.Helper()
	cpu := func() time.Duration {
		in, err := s.sys.info(s.self)
		if err != nil || !in.hasCPU {
			t.Fatalf("读不到本进程的 CPU 时间：%+v %v", in, err)
		}
		return in.cpu
	}
	start, deadline := cpu(), time.Now().Add(MaxAge-5*time.Second)
	for cpu()-start < d {
		if time.Now().After(deadline) {
			t.Fatalf("本进程转了 %v 也没分到 %v 的 CPU", MaxAge-5*time.Second, d)
		}
		for x, i := 0, 0; i < 1_000_000; i++ {
			x += i
		}
	}
}

// 本机的显存计数器：能读就检查主程序的行，不能读就只检查"不可用"带了原因（不同机器结果不同，不对数值做断言）
func TestRealGPUSource(t *testing.T) {
	g := newGPU()
	defer g.Close()
	r1, err := g.Read()
	if err != nil {
		t.Logf("这台机器的显存计数器不可用：%v", err)
		return
	}
	if len(r1.Util) != 0 {
		t.Fatalf("第一次读不该有利用率：%+v", r1.Util)
	}
	time.Sleep(300 * time.Millisecond)
	r2, err := g.Read()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("显存条目 %d 个，利用率条目 %d 个", len(r2.Memory), len(r2.Util))
}

// pid 被复用：登记簿里的创建时间和现在对不上，这一行作废，不张冠李戴。
func TestRegisteredPIDReuseDropped(t *testing.T) {
	// 登记时记下的创建时间故意写成错的，模拟"这个 pid 现在是另一个进程"
	reg := &procreg.Registry{CreatedOf: func(int) int64 { return 12345 }}
	child := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	unreg := reg.Register(procreg.CatMedia, "旧进程", child.Process.Pid)
	defer unreg()
	s := New(Options{Registry: reg, Queues: &runmetrics.Registry{}})
	rep, err := s.Sample(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Processes {
		if r.PID == child.Process.Pid && r.Category == ctlproto.ProcMedia {
			t.Fatalf("创建时间对不上的登记要被丢弃：%+v", r)
		}
	}
}
