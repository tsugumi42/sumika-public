package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// 存储标签（规格-运行页 第十二节第 3 步验收①③）：空（没统计过）、加载、出错保留旧数据、不可用、没有数字的写"未统计"，三个宽度，重新统计和取消。

func neverScanned() ctlproto.StorageReport {
	rep := demoStorage(3, runNow)
	rep.NeverScanned, rep.ScannedAtMS, rep.ScanMs = true, 0, 0
	for i := range rep.Categories {
		if rep.Categories[i].Key == ctlproto.StoreMedia || rep.Categories[i].Key == ctlproto.StoreDev {
			c := &rep.Categories[i]
			c.Bytes, c.Files, c.Source, c.Approx, c.AsOfMS = 0, 0, ctlproto.SourceNone, false, 0
		}
	}
	return rep
}

func TestRunStorageNeverScanned(t *testing.T) {
	f := &fakeStats{store: neverScanned()}
	h := newRunHarness(t, f, f, runTabStorage, 1100, 900)
	h.need("还没有统计过", "开始统计", "媒体", "未统计", "聊天库")
	h.absent("重新统计")
	h.click("开始统计")
	h.until("点开始统计要请求重新统计", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.rescans >= 1 })
	f.mu.Lock()
	n := f.rescans
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("点开始统计要请求一次重新统计：%d", n)
	}
}

func TestRunStorageLoadingThenReady(t *testing.T) {
	f := &fakeStats{store: demoStorage(3, runNow), gate: make(chan struct{})}
	a, q := runApp(t, f, f)
	a.run.tab = runTabStorage
	tt := newTesterFor(a)
	tt.Frame()
	if !a.run.store.loading || a.run.store.has {
		t.Fatalf("请求挂着时该是加载中：%+v", a.run.store)
	}
	if tt.HasText("数据目录总占用") {
		t.Fatalf("加载中只显示骨架：%v", tt.Texts())
	}
	f.set(func(f *fakeStats) { close(f.gate); f.gate = nil })
	settleMem(q)
	tt.Frame()
	if !tt.HasText("数据目录总占用") || !tt.HasText("重新统计") {
		t.Fatalf("读到以后要显示数据：%v", tt.Texts())
	}
}

func TestRunStorageErrorKeepsOldDataAsStale(t *testing.T) {
	f := &fakeStats{store: demoStorage(3, runNow)}
	h := newRunHarness(t, f, f, runTabStorage, 1100, 900)
	h.need("媒体", "13.00 GB")
	h.absent("已过期")
	f.set(func(f *fakeStats) { f.storeErr = errors.New("读存储统计出错") })
	h.click("刷新")
	h.need("已过期", "媒体", "13.00 GB")
	f.set(func(f *fakeStats) { f.storeErr = nil })
	h.click("刷新")
	h.absent("已过期")
}

func TestRunStorageUnavailableAndError(t *testing.T) {
	f := &fakeStats{storeErr: &errRunUnavailable{"数据目录还没就绪"}}
	h := newRunHarness(t, f, f, runTabStorage, 1100, 900)
	h.need("存储暂时看不了", "数据目录还没就绪")
	h.absent("重试")

	g := &fakeStats{storeErr: errors.New("读存储统计出错")}
	h2 := newRunHarness(t, g, g, runTabStorage, 1100, 900)
	h2.need("没取到存储统计", "重试")

	h3 := newRunHarness(t, nil, nil, runTabStorage, 1100, 900)
	h3.need("后台还没起来")
}

func TestRunStorageAbsentAndNoCleanButtons(t *testing.T) {
	f := &fakeStats{store: demoStorage(3, runNow)}
	h := newRunHarness(t, f, f, runTabStorage, 1100, 900)
	h.need("位置不存在", "没有自动清理")
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "清理") && (s == "清理" || strings.HasPrefix(s, "清理缩略图") || strings.HasPrefix(s, "清理日志")) {
			t.Fatalf("第 3 步不出现清理按钮：%q", s)
		}
	}
	// 分行：聊天库按平台、记忆库按角色，点开才看见
	h.absent("角色01")
	rows, roots := storageRows(f.store, h.a.run.storeTbl.sort)
	if len(rows[ctlproto.StoreMemory].kids) != 3 {
		t.Fatalf("记忆库按角色分行：%v", rows[ctlproto.StoreMemory].kids)
	}
	if roots[len(roots)-1] != ctlproto.StoreLegacyMedia {
		t.Fatalf("没有数字的（位置不存在）沉底：%v", roots)
	}
}

func TestRunStorageThreeWidths(t *testing.T) {
	for _, w := range []int{380, 1100, 2048} {
		f := &fakeStats{store: demoStorage(10, runNow)}
		h := newRunHarness(t, f, f, runTabStorage, w, 900)
		h.need("数据目录总占用", "媒体", "重新统计")
		for _, label := range []string{"数据目录总占用", "重新统计"} {
			r, ok := h.tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(w) {
				t.Fatalf("宽 %d：%q 越界 %+v", w, label, r)
			}
		}
	}
}

func TestRunStorageRescanProgressAndCancel(t *testing.T) {
	old := runScanPoll
	runScanPoll = 150 * time.Millisecond // 比 settleMem 判“静下来”的窗口长，否则它每次都等满 5 秒
	t.Cleanup(func() { runScanPoll = old })
	rep := demoStorage(3, runNow)
	f := &fakeStats{store: rep}
	h := newRunHarness(t, f, f, runTabStorage, 1100, 900)
	base := tickersOf(h.a)
	// 扫描开始：读到的状态带 Running，页面写进度和取消
	run := rep
	run.Scan = ctlproto.StorageScan{Running: true, Entries: 1234, StartMS: runNow.UnixMilli()}
	f.set(func(f *fakeStats) { f.store = run })
	h.click("重新统计")
	h.need("正在统计，已处理 1,234 项", "取消统计")
	h.absent("重新统计")
	if h.a.run.pollStop == nil {
		t.Fatal("扫描进行中要轮询进度")
	}
	// 进度往前走：轮询自己读
	run.Scan.Entries = 5678
	f.set(func(f *fakeStats) { f.store = run })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !h.tt.HasText("正在统计，已处理 5,678 项") {
		h.q.drain()
		h.tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	h.need("正在统计，已处理 5,678 项")
	// 取消：服务那边停了，读到不在跑，轮询停
	f.set(func(f *fakeStats) { f.store = rep })
	if err := h.tt.Click("取消统计"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30 && h.a.run.pollStop != nil; i++ {
		h.q.drain()
		h.tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	cancels := f.cancels
	f.mu.Unlock()
	if cancels != 1 || h.a.run.pollStop != nil {
		t.Fatalf("取消要调一次 CancelScan 并停轮询：%d %v", cancels, h.a.run.pollStop != nil)
	}
	// 离开页面：订阅和轮询都没了
	h.a.rail = 0
	h.tt.Frame()
	waitTickers(t, h.a, base)
}
