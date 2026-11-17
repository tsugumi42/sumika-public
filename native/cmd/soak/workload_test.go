package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	k, err := newKernel(ctx, t.TempDir())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer k.close()
	defer cancel()
	c := &counters{}
	k.counts = c
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	k.hub.Cfg.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	// 一组走完所有动作，再访问其余三组并回到第一组；推进钟验证闲置回收，不重复同一路径。
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 24, 36, 48} {
		if n%12 == 0 {
			clock.Add(int64(30 * time.Second))
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("第 %d 句前已超时：%v", n+1, err)
		}
		if err := k.step(ctx, n, c); err != nil {
			t.Fatalf("第 %d 句：%v", n+1, err)
		}
	}
	got := c.snapshot()
	if got != (totals{turns: 16, tools: 4, steers: 1, stops: 1, compacts: 1, fresh: 5}) {
		t.Fatalf("负载没走齐：%+v，工具出错=%d", got, c.toolErrors.Load())
	}
	var instances, aborted, compacted, steered int
	if err = k.set.Trace().QueryRow(`SELECT count(DISTINCT character_id || ':' || conversation) FROM turns`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if err = k.set.Trace().QueryRow(`SELECT count(*) FROM turns WHERE outcome = 'stopped'`).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	st, err := k.set.For("console")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Events.QueryRow(`SELECT count(*) FROM context_epochs WHERE status = 'ok'`).Scan(&compacted); err != nil {
		t.Fatal(err)
	}
	if err = st.Events.QueryRow(`SELECT count(*) FROM events WHERE text = '插一句：先读短篇，再讨论结局。'`).Scan(&steered); err != nil {
		t.Fatal(err)
	}
	if instances != 4 || aborted != 1 || compacted != 1 || steered != 1 {
		t.Fatalf("库里的结果不对：实例=%d，停止=%d，压缩=%d，插话=%d", instances, aborted, compacted, steered)
	}
	t.Logf("实际负载：%+v；库中实例=%d，停止=%d，压缩=%d，插话=%d", got, instances, aborted, compacted, steered)
}
