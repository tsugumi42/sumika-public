package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadui"
)

// 运行页用量、存储标签的加载占位（loadstate 的规则）：快 / 慢 / 刷新各一条，时间用 Tester.Advance 推进。
// 读服务用 fakeStats 的 gate 控制什么时候回。

func (h *runHarness) advance(d time.Duration) {
	h.t.Helper()
	h.tt.Advance(d)
	settleMem(h.q)
	h.tt.Frame()
}

func (h *runHarness) release(f *fakeStats) {
	f.set(func(f *fakeStats) {
		if f.gate != nil {
			close(f.gate)
			f.gate = nil
		}
	})
}

type runTabCase struct {
	name   string
	tab    int
	marker string // 数据出来后一定有的字
	mk     func() *fakeStats
}

func runTabCases(t *testing.T) []runTabCase {
	return []runTabCase{
		{"用量", runTabUsage, "总输入", func() *fakeStats {
			return &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000)), gate: make(chan struct{})}
		}},
		{"存储", runTabStorage, "数据目录总占用", func() *fakeStats {
			return &fakeStats{store: demoStorage(3, runNow), gate: make(chan struct{})}
		}},
		{"质量", runTabQuality, "失败率", func() *fakeStats {
			return &fakeStats{qual: demoQuality(), gate: make(chan struct{})}
		}},
	}
}

// 快：阈值内数据到了，全程不画骨架，到了直接出数据
func TestRunLoadFastNeverShowsSkeleton(t *testing.T) {
	for _, tc := range runTabCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.mk()
			h := newRunHarness(t, f, f, tc.tab, 1100, 800)
			base := loadui.Drawn()
			for i := 0; i < 5; i++ {
				h.advance(50 * time.Millisecond)
				if loadui.Drawn() != base {
					t.Fatalf("第 %d 帧（%d 毫秒）画了骨架", i, (i+1)*50)
				}
				h.absent(tc.marker)
			}
			h.release(f)
			h.frame()
			h.need(tc.marker)
			for i := 0; i < 8; i++ {
				h.advance(250 * time.Millisecond)
			}
			if loadui.Drawn() != base {
				t.Fatal("数据到了以后又画了骨架")
			}
		})
	}
}

// 慢：阈值后出骨架；数据早到也留够 300 毫秒才换
func TestRunLoadSlowShowsSkeletonAndHolds(t *testing.T) {
	for _, tc := range runTabCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.mk()
			h := newRunHarness(t, f, f, tc.tab, 1100, 800)
			base := loadui.Drawn()
			h.advance(290 * time.Millisecond)
			if loadui.Drawn() != base {
				t.Fatal("290 毫秒就出骨架了")
			}
			h.advance(20 * time.Millisecond)
			if loadui.Drawn() == base {
				t.Fatal("过了阈值没出骨架")
			}
			h.release(f)
			h.frame()
			h.absent(tc.marker)
			h.advance(200 * time.Millisecond)
			h.absent(tc.marker)
			h.advance(100 * time.Millisecond)
			h.need(tc.marker)
			mid := loadui.Drawn()
			h.advance(500 * time.Millisecond)
			if loadui.Drawn() != mid {
				t.Fatal("数据出来以后骨架还在画")
			}
		})
	}
}

// 刷新：旧数据一直在、不出骨架；"刷新中…"阈值后才写，换成"更新于"
func TestRunLoadRefreshKeepsDataMarkerDelayed(t *testing.T) {
	for _, tc := range runTabCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.mk()
			h := newRunHarness(t, f, f, tc.tab, 1100, 800)
			h.release(f)
			h.advance(time.Second)
			h.need(tc.marker, "更新于")
			base := loadui.Drawn()
			f.set(func(f *fakeStats) { f.gate = make(chan struct{}) })
			h.click("刷新")
			for i := 0; i < 5; i++ {
				h.need(tc.marker)
				if strings.Contains(strings.Join(h.tt.Texts(), "|"), "刷新中…") {
					t.Fatalf("第 %d 帧（%d 毫秒）刷新标记就出来了", i, i*50)
				}
				h.advance(50 * time.Millisecond)
			}
			h.advance(100 * time.Millisecond)
			h.need(tc.marker, "刷新中…")
			if loadui.Drawn() != base {
				t.Fatal("刷新不该画骨架")
			}
			h.release(f)
			h.advance(400 * time.Millisecond)
			h.need(tc.marker, "更新于")
			h.absent("刷新中…")
		})
	}
}

// 快的刷新：门不关，点刷新，每一帧都有数据、从头到尾没有标记和骨架
func TestRunLoadFastRefreshShowsNothing(t *testing.T) {
	for _, tc := range runTabCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.mk()
			h := newRunHarness(t, f, f, tc.tab, 1100, 800)
			h.release(f)
			h.advance(time.Second)
			base := loadui.Drawn()
			h.click("刷新")
			h.need(tc.marker)
			h.absent("刷新中…")
			h.advance(time.Second)
			if loadui.Drawn() != base {
				t.Fatal("快的刷新画了骨架")
			}
		})
	}
}

// 骨架的工具栏和统计格和真实的对齐：骨架里第二条（统计格的名称）的顶 = 真实"总输入"的顶
func TestRunLoadUsageSkeletonLinesUpWithRealStats(t *testing.T) {
	f := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000)), gate: make(chan struct{})}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.advance(400 * time.Millisecond)
	skel := h.tt.Image()
	h.release(f)
	h.advance(400 * time.Millisecond)
	r, ok := h.tt.Find("总输入")
	if !ok {
		t.Fatalf("没有总输入：%v", h.tt.Texts())
	}
	x := int(r.X)
	runs := barRunsAt(skel, x+900, x+5, x+25, x+45)
	if len(runs) < 3 {
		t.Fatalf("骨架里找到 %d 段：%v", len(runs), runs)
	}
	// 上面还有导航、标签条这些别的东西，取和"总输入"最近的那段比
	near := runs[0]
	for _, run := range runs {
		if abs32f(float32(run.top)-r.Y) < abs32f(float32(near.top)-r.Y) {
			near = run
		}
	}
	if d := float32(near.top) - r.Y; d < -1.5 || d > 1.5 {
		t.Errorf("骨架统计格名称的顶 %d，真实总输入的顶 %v（差 %v）；全部段 %v，r=%v", near.top, r.Y, d, runs, r)
	}
}
