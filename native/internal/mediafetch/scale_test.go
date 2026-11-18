package mediafetch

// 放大十倍验收（规格 M13）：一次涌入 500 张图，补扫在 12 万行 events 上。数字用 t.Log 打出来，抄进提交说明。
// 比较慢（几秒到十几秒），-short 跳过。

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

func dirBytes(root string) (files int, size int64) {
	filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			files++
			size += info.Size()
		}
		return nil
	})
	return
}

// 500 张图一次涌进来：Offer 不阻塞（p99 微秒级），全部下完，量总耗时、内存峰值、goroutine 数、磁盘占用
func TestScale500Images(t *testing.T) {
	if testing.Short() {
		t.Skip("放大测试，-short 跳过")
	}
	e := newEnv(t)
	const n = 500
	payload := bytes.Repeat([]byte("图"), 20_000) // 每张约 60 KB
	var hits atomic.Int64
	e.handle("/img", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(png(r.URL.Query().Get("i") + string(payload)))
	})
	type ev struct {
		seq  int64
		segs []types.Seg
	}
	evs := make([]ev, n)
	for i := range evs {
		segs := []types.Seg{{Kind: types.SegImage, URL: fmt.Sprintf("http://%s/img?i=%d&rkey=%s", testHost, i, secret), File: fmt.Sprintf("%d.jpg", i)}}
		evs[i] = ev{e.addEvent(segs, fmt.Sprint(1000+i)), segs}
	}
	e.start()
	var peakHeap atomic.Uint64
	var peakG atomic.Int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peakHeap.Load() {
				peakHeap.Store(m.HeapAlloc)
			}
			if g := int64(runtime.NumGoroutine()); g > peakG.Load() {
				peakG.Store(g)
			}
		}
	}()
	lat := make([]time.Duration, 0, n)
	begin := time.Now()
	for _, x := range evs {
		t0 := time.Now()
		e.f.Offer(x.seq, x.segs)
		lat = append(lat, time.Since(t0))
	}
	offered := time.Since(begin)
	e.idle()
	total := time.Since(begin)
	close(stop)
	<-sampled
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	files, size := dirBytes(filepath.Join(e.data, "go", "media", "qq"))
	done := 0
	for _, x := range evs {
		if e.segHash(x.seq, 0) != "" {
			done++
		}
	}
	t.Logf("500 张图：全部交完 %v，全部下完 %v；Offer 延迟 p50 %v p99 %v max %v；堆峰值 %.1f MB；goroutine 峰值 %d；落盘 %d 个文件 %.1f MB；回填 %d/%d；请求 %d 次",
		offered.Round(time.Millisecond), total.Round(time.Millisecond), lat[n/2], lat[n*99/100], lat[n-1],
		float64(peakHeap.Load())/(1<<20), peakG.Load(), files, float64(size)/(1<<20), done, n, hits.Load())
	if done != n || files != n {
		t.Fatalf("没下完：回填 %d，文件 %d，该是 %d", done, files, n)
	}
	if lat[n*99/100] > 50*time.Millisecond {
		t.Fatalf("Offer 的 p99 是 %v：它在会话队列里调，不该慢", lat[n*99/100])
	}
}

// 补扫在 12 万行 events 上多久：最坏的一种是近 7 天 12 万条，每条都带一张已经有 hash 的图（全表过一遍 json_each）；
// 再一种是同样 12 万条里 200 张还没下
func TestScaleSweepOn120kEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("放大测试，-short 跳过")
	}
	e := newEnv(t)
	const rows = 120_000
	now := e.clk.Now().UnixMilli()
	tx, err := e.st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, origin, kind, text, segments_json, created_at) VALUES (?, 'qq', ?, 'human', 'qq:10001', 'peer', 'message', 't', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	for i := 0; i < rows; i++ {
		var body string
		switch {
		case i%600 == 0 && i/600 < 200: // 200 张还没下
			body = fmt.Sprintf(`{"segs":[{"kind":"text","text":"看图"},{"kind":"image","url":"http://%s/p%d?rkey=%s","file":"%d.jpg"}],"from":{"id":"10001"},"line":"编的一行"}`, testHost, i, secret, i)
		case i%2 == 0: // 一半带已经下好的图
			body = fmt.Sprintf(`{"segs":[{"kind":"text","text":"看图%d"},{"kind":"image","hash":"%s","url":"http://%s/q%d?rkey=%s","file":"%d.jpg"}],"from":{"id":"10001"},"line":"编的一行"}`, i, hash, testHost, i, secret, i)
		default:
			body = fmt.Sprintf(`{"segs":[{"kind":"text","text":"编的话 %d"}],"from":{"id":"10001"},"line":"编的一行"}`, i)
		}
		at := now - int64(i%(6*24*3600))*1000 // 6 天以内
		if _, err := stmt.Exec(fmt.Sprintf("id%07d", i), fmt.Sprintf("qq:private:10000:%d", 10001+i%50), body, at); err != nil {
			t.Fatal(err)
		}
	}
	// 每条事件都有角色看到了（真实的库里除了陌生人都是这样）
	if _, err := tx.Exec(`INSERT INTO observations (character_id, event_seq) SELECT 'a', seq FROM events`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	since := now - 7*24*3600*1000
	// 量三次取最快：整套并行时机器忙，单次可能超一点；查询真变慢了三次都慢
	var took time.Duration
	found, pages := 0, 0
	for try := range 3 {
		begin := time.Now()
		found, pages = 0, 0
		var after int64
		for {
			page, err := e.st.MediaCandidates(since, after, sweepPage, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			pages++
			found += len(page)
			after = page[len(page)-1].Seq
			if len(page) < sweepPage {
				break
			}
		}
		if d := time.Since(begin); try == 0 || d < took {
			took = d
		}
	}
	t.Logf("12 万行 events 上补扫挑候选：%v，挑出 %d 条事件（%d 页）", took.Round(time.Millisecond), found, pages)
	if found != 200 {
		t.Fatalf("该挑出 200 条，挑出 %d", found)
	}
	if took > 3*time.Second { // 抓的是退回全表扫（十几秒以上）；3 秒给负载留余地，真实耗时看上面的日志
		t.Fatalf("补扫挑候选花了 %v，超过 3 秒：改成按 seq 起点扫（规格已知的坑 13）", took)
	}
}
