package backup

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// 备份期间入库的等待（规格-数据目录与备份 10.10.6）：备份跑着的时候，周期性地对每个登记了“写探针”的库探一次——
// 拿写锁再马上放（不写任何数据），量拿到写锁等了多久。单连接池里这个时间包含排在入库写入后面等的时间，就是“这时候来一次写入要等多久”。
// 探针由持有库连接的一方登记（app 里是各平台聊天库）；入库的热路径一行没动。

const (
	// DefaultProbeEvery：备份跑着时多久探一次
	DefaultProbeEvery = 250 * time.Millisecond
	// probeTimeout：一次探测最多等多久；超过就记“没探到”，不拖住备份
	probeTimeout = 10 * time.Second
	// SlowWriteWaitMs：写锁等待超过这么久（毫秒）在 backup list 里标出来
	SlowWriteWaitMs = 1000
)

// defaultProbeStopWait：备份做完后最多等探测的协程收尾多久（Service.probeStop，测试可换：探针卡死的那条测试不想真等 2 秒）
const defaultProbeStopWait = 2 * time.Second

// WriteProbe 是一个库的写锁探针：Probe 返回拿到写锁等了多久
type WriteProbe struct {
	Rel   string // 库文件相对数据目录的路径
	Probe func(ctx context.Context) (time.Duration, error)
}

// WriteWaitEntry 是清单里一个库在这份备份期间的写锁等待统计
type WriteWaitEntry struct {
	Path    string `json:"path"`
	Samples int    `json:"samples"`          // 探到几次
	Failed  int    `json:"failed,omitempty"` // 几次没探到（连接取不到、超时、只读连接拿不了写锁）
	MaxMs   int64  `json:"maxMs"`
	AvgMs   int64  `json:"avgMs"`
}

type probeStat struct {
	n, failed int
	total     time.Duration
	max       time.Duration
}

// startWriteSampler 起一个协程周期性地探，返回停止函数：停下并交回每个库的统计。没有探针就什么都不起，返回 nil
func (s *Service) startWriteSampler(ctx context.Context, probes []WriteProbe) func() []WriteWaitEntry {
	if len(probes) == 0 {
		return func() []WriteWaitEntry { return nil }
	}
	every := s.opt.ProbeEvery
	if every <= 0 {
		every = DefaultProbeEvery
	}
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	stats := make(map[string]*probeStat, len(probes))
	for _, p := range probes {
		stats[p.Rel] = &probeStat{}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // 探针出了意外不能带垮备份
		sample := func() {
			for _, p := range probes {
				if ctx.Err() != nil {
					return
				}
				pctx, pcancel := context.WithTimeout(ctx, probeTimeout)
				t0 := time.Now()
				d, err := p.Probe(pctx)
				pcancel()
				if ctx.Err() != nil {
					return // 备份做完了：这次探测可能是被我们取消的，不算
				}
				mu.Lock()
				st := stats[p.Rel]
				if err != nil {
					st.failed++
					// 没探到也要记最糟的：等了多久才放弃（比如等满超时），不然最糟的情况反而看不出来
					if el := time.Since(t0); el > st.max {
						st.max = el
					}
				} else {
					st.n++
					st.total += d
					if d > st.max {
						st.max = d
					}
				}
				mu.Unlock()
			}
		}
		sample()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sample()
			}
		}
	}()
	return func() []WriteWaitEntry {
		cancel()
		select {
		case <-done:
		case <-time.After(s.probeStop): // 卡在一次探测上：不再等它，用已经有的数
		}
		mu.Lock()
		defer mu.Unlock()
		var out []WriteWaitEntry
		for rel, st := range stats {
			if st.n == 0 && st.failed == 0 {
				continue
			}
			e := WriteWaitEntry{Path: rel, Samples: st.n, Failed: st.failed, MaxMs: st.max.Milliseconds()}
			if st.n > 0 {
				e.AvgMs = (st.total / time.Duration(st.n)).Milliseconds()
			}
			out = append(out, e)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out
	}
}

// WriteWaitSummary 把一份备份各库的写锁等待合成一句：总次数、最长、没探到几次
func WriteWaitSummary(ws []WriteWaitEntry) (samples, failed int, maxMs int64) {
	for _, w := range ws {
		samples += w.Samples
		failed += w.Failed
		if w.MaxMs > maxMs {
			maxMs = w.MaxMs
		}
	}
	return
}

func writeWaitLog(ws []WriteWaitEntry) string {
	n, failed, max := WriteWaitSummary(ws)
	if n == 0 && failed == 0 {
		return ""
	}
	s := fmt.Sprintf("备份期间对聊天库写锁的采样：探 %d 次，最长等 %d 毫秒", n, max)
	if failed > 0 {
		s += fmt.Sprintf("，另有 %d 次没探到", failed)
	}
	if max > SlowWriteWaitMs {
		s += "（超过 1 秒，入库在备份期间被拖慢了；没探到的次数也按等了多久算进最长）"
	}
	return s
}
