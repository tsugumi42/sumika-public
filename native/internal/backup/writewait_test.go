package backup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/sqlitesnap"
)

// 备份期间入库的等待（规格-数据目录与备份 10.10.6）

// chatProbe 给 fixture 的聊天库登记一个写锁探针
func (f *fx) chatProbe(probe func(ctx context.Context) (time.Duration, error)) {
	f.svc.Register(func() Contribution {
		return Contribution{WriteProbes: []WriteProbe{{Rel: "go/chat/qq.db", Probe: probe}}}
	})
}

func (f *fx) writeWaitOf(res Result, rel string) (WriteWaitEntry, bool) {
	f.t.Helper()
	m, err := ReadManifest(res.Folder)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, w := range m.WriteWait {
		if w.Path == rel {
			return w, true
		}
	}
	return WriteWaitEntry{}, false
}

// 一边持续往聊天库写，一边备份：探针探到了、没失败、没有把入库拖到超时；清单里有这一项；顺手量写入方自己看到的最长单次写入（合成数，临时目录上）
func TestBackupWriteWaitProbe(t *testing.T) {
	f := newFx(t, func(o *Options) { o.ProbeEvery = 2 * time.Millisecond })
	f.chatProbe(func(ctx context.Context) (time.Duration, error) { return sqlitesnap.WriteLockWait(ctx, f.chat) })
	mustExec(t, f.chat, `CREATE TABLE IF NOT EXISTS load (n INTEGER, pad TEXT)`)
	for i := 0; i < 400; i++ { // 把库做大一点，快照要花一会儿
		mustExec(t, f.chat, `INSERT INTO load VALUES (?, ?)`, i, "填充填充填充填充填充填充填充填充填充填充填充填充填充填充填充")
	}
	var n, maxWriteNs atomic.Int64
	var inBackup atomic.Bool
	var maxDuring atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			t0 := time.Now()
			if _, err := f.chat.Exec(`INSERT INTO load VALUES (1, '写入方')`); err != nil {
				return
			}
			d := time.Since(t0).Nanoseconds()
			n.Add(1)
			if d > maxWriteNs.Load() {
				maxWriteNs.Store(d)
			}
			if inBackup.Load() && d > maxDuring.Load() {
				maxDuring.Store(d)
			}
		}
	}()
	for n.Load() < 200 {
		time.Sleep(time.Millisecond)
	}
	inBackup.Store(true)
	res := f.mustRun(ReasonManual)
	inBackup.Store(false)
	close(stop)
	<-done
	w, ok := f.writeWaitOf(res, "go/chat/qq.db")
	if !ok || w.Samples < 1 || w.Failed != 0 || w.MaxMs < 0 || w.MaxMs >= 5000 {
		t.Fatalf("清单里该有聊天库的写锁等待采样，探到了、没失败、没被拖到超时：%v %+v", ok, w)
	}
	t.Logf("合成数（临时目录，不代表真机）：备份期间探了 %d 次，写锁最长等 %d 毫秒、平均 %d 毫秒；写入方自己看到备份期间最长一次写入 %.1f 毫秒（整个测试里最长 %.1f 毫秒）",
		w.Samples, w.MaxMs, w.AvgMs, float64(maxDuring.Load())/1e6, float64(maxWriteNs.Load())/1e6)
	// backup list 读得到
	entries, _ := f.svc.List()
	if len(entries) != 1 || len(entries[0].Manifest.WriteWait) != 1 {
		t.Fatalf("总览读的清单头里也该带：%+v", entries)
	}
}

// 别的连接占着写锁时，备份期间的探测量到这段等待
func TestBackupWriteWaitSeesBlockedWriter(t *testing.T) {
	f := newFx(t, func(o *Options) { o.ProbeEvery = 5 * time.Millisecond })
	// 探针用自己的连接池（和快照用的 f.chat 分开）：被别的连接占着的写锁只拖慢探针，不会让同一个池里的快照也等着
	probeDB := openRW(t, f.p.ChatDB("qq"))
	probeFirst := make(chan struct{})    // 第一次探测开始
	probeReturned := make(chan struct{}) // 第一次探测拿到写锁返回
	var once, once1 atomic.Bool
	f.chatProbe(func(ctx context.Context) (time.Duration, error) {
		if once.CompareAndSwap(false, true) {
			close(probeFirst)
		}
		d, err := sqlitesnap.WriteLockWait(ctx, probeDB)
		if once1.CompareAndSwap(false, true) {
			close(probeReturned)
		}
		return d, err
	})
	holder := openRW(t, f.p.ChatDB("qq"))
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	// 复制文件时卡住：备份还没做完，探测的协程一直开着，我们才有时间放手
	inCopy := make(chan struct{})
	releaseCopy := make(chan struct{})
	var once2 atomic.Bool
	realCopy := f.svc.copyFile
	f.svc.copyFile = func(ctx context.Context, src, dst string) (int64, string, error) {
		if once2.CompareAndSwap(false, true) {
			close(inCopy)
			<-releaseCopy
		}
		return realCopy(ctx, src, dst)
	}
	ch, err := f.svc.Go(Request{Reason: ReasonManual})
	if err != nil {
		t.Fatal(err)
	}
	<-probeFirst
	<-inCopy
	hold := 150 * time.Millisecond
	time.Sleep(hold) // 这里的睡眠是在“造”被量的那段等待，不是在等什么事件
	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select { // 等探测拿到写锁返回：之后统计里才有这一次
	case <-probeReturned:
	case <-time.After(10 * time.Second):
		t.Fatal("放手以后探测一直没返回")
	}
	close(releaseCopy)
	out := <-ch
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	w, ok := f.writeWaitOf(out.Result, "go/chat/qq.db")
	if !ok || w.MaxMs < hold.Milliseconds()-40 {
		t.Fatalf("别的连接占了 %v 的写锁，清单里的最长等待该差不多这么久：%v %+v", hold, ok, w)
	}
}

// 探针一直失败（比如 archive 模式只读连接拿不了写锁）：如实记“没探到”，备份照样做完
func TestWriteProbeFailureIsCounted(t *testing.T) {
	f := newFx(t, func(o *Options) { o.ProbeEvery = 2 * time.Millisecond })
	f.chatProbe(func(context.Context) (time.Duration, error) { return 0, errors.New("拿不了写锁") })
	res := f.mustRun(ReasonManual)
	w, ok := f.writeWaitOf(res, "go/chat/qq.db")
	if !ok || w.Samples != 0 || w.Failed < 1 {
		t.Fatalf("该如实记没探到：%v %+v", ok, w)
	}
}

// 没有探针的备份：清单里没有这一项（缺着，不是写一个 0）
func TestNoProbesNoWriteWait(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	if m, _ := ReadManifest(res.Folder); len(m.WriteWait) != 0 {
		t.Fatalf("没有探针不该有等待数：%+v", m.WriteWait)
	}
}

// 探针卡死（不看取消）也不能把备份拖住
func TestStuckProbeDoesNotBlockBackup(t *testing.T) {
	f := newFx(t, func(o *Options) { o.ProbeEvery = 2 * time.Millisecond })
	f.svc.probeStop = 100 * time.Millisecond
	block := make(chan struct{})
	defer close(block)
	f.chatProbe(func(context.Context) (time.Duration, error) { <-block; return 0, nil })
	done := make(chan Result, 1)
	go func() { done <- f.mustRun(ReasonManual) }()
	select {
	case res := <-done:
		if res.Status != StatusOK {
			t.Fatalf("%+v", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("探针卡死把备份拖住了")
	}
}

func TestWriteWaitSummary(t *testing.T) {
	n, failed, max := WriteWaitSummary([]WriteWaitEntry{{Path: "a", Samples: 3, MaxMs: 40, Failed: 1}, {Path: "b", Samples: 5, MaxMs: 900}})
	if n != 8 || failed != 1 || max != 900 {
		t.Fatalf("%d %d %d", n, failed, max)
	}
	if line := writeWaitLog([]WriteWaitEntry{{Samples: 2, MaxMs: 2500}}); line == "" {
		t.Fatal("有数时该有一句日志")
	}
	if writeWaitLog(nil) != "" {
		t.Fatal("没数不写")
	}
}

// 探针失败（比如等满超时）也要把等了多久记进最长：不然最糟的情况反而看不出来
func TestWriteProbeFailureCountsElapsedWait(t *testing.T) {
	f := newFx(t, func(o *Options) { o.ProbeEvery = 2 * time.Millisecond })
	f.chatProbe(func(context.Context) (time.Duration, error) {
		time.Sleep(40 * time.Millisecond) // 在“等”，等了这么久才放弃
		return 0, errors.New("等满超时")
	})
	var slowOnce atomic.Bool
	realCopy := f.svc.copyFile
	f.svc.copyFile = func(ctx context.Context, src, dst string) (int64, string, error) {
		if slowOnce.CompareAndSwap(false, true) {
			time.Sleep(200 * time.Millisecond) // 让备份比探测多活一会儿（造时间，不是等事件）
		}
		return realCopy(ctx, src, dst)
	}
	res := f.mustRun(ReasonManual)
	w, ok := f.writeWaitOf(res, "go/chat/qq.db")
	if !ok || w.Failed < 1 || w.MaxMs < 30 {
		t.Fatalf("没探到的那几次等了多久也该进最长：%v %+v", ok, w)
	}
}
