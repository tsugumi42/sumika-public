package loadstate

import (
	"testing"
	"time"
)

// 全部用手动推进的时间，不碰墙钟

type clock struct{ t time.Time }

func newClock() *clock { return &clock{t: time.Unix(1_000_000, 0)} }

func (c *clock) add(d time.Duration) time.Time { c.t = c.t.Add(d); return c.t }

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestConstantsAreThreeHundredMs(t *testing.T) {
	if ShowDelay != 300*time.Millisecond || MinShow != 300*time.Millisecond {
		t.Fatalf("ShowDelay=%v MinShow=%v", ShowDelay, MinShow)
	}
}

// 快：阈值内数据到了，没有数据时也从头到尾不出骨架，只是空位
func TestFastFirstLoadNeverShowsSkeleton(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	for _, step := range []int{0, 100, 100, 99} {
		v := l.View(c.add(ms(step)))
		if v.Skeleton || v.Refreshing || !v.Waiting {
			t.Fatalf("到阈值前应只有空位：%+v", v)
		}
	}
	l.Succeed() // 299ms 时到
	v := l.View(c.add(ms(1)))
	if v.Skeleton || v.Waiting || v.Refreshing || v.Next != 0 {
		t.Fatalf("到了以后直接显示内容：%+v", v)
	}
}

// 慢：阈值那一刻出骨架；数据比最短时长早到，骨架留到 MinShow 才撤
func TestSlowFirstLoadShowsThenHolds(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.View(c.add(0))
	v := l.View(c.add(ms(299)))
	if v.Skeleton || v.Next != ms(1) {
		t.Fatalf("299ms：%+v", v)
	}
	if v = l.View(c.add(ms(1))); !v.Skeleton { // 300ms
		t.Fatalf("300ms 应出骨架：%+v", v)
	}
	l.Succeed() // 骨架刚出来，数据就到了
	if v = l.View(c.add(ms(50))); !v.Skeleton || v.Next != ms(250) {
		t.Fatalf("应继续留骨架：%+v", v)
	}
	if v = l.View(c.add(ms(249))); !v.Skeleton || v.Next != ms(1) {
		t.Fatalf("差 1ms 才够：%+v", v)
	}
	if v = l.View(c.add(ms(1))); v.Skeleton || v.Waiting || v.Next != 0 {
		t.Fatalf("留够了应换内容：%+v", v)
	}
	if !l.HasData() {
		t.Fatal("数据应已记下")
	}
}

// 数据在骨架留够以后才到：不再多留
func TestSkeletonLeavesWhenDataComesAfterMin(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.View(c.add(0))
	l.View(c.add(ms(300)))
	l.View(c.add(ms(400))) // 骨架已出现 400ms
	l.Succeed()
	if v := l.View(c.add(ms(1))); v.Skeleton {
		t.Fatalf("早就留够了：%+v", v)
	}
}

// 刷新：有旧数据时全程没有骨架；快的刷新连小标记都没有，慢的在阈值后出标记并留够
func TestRefreshKeepsDataAndMarkerIsDelayed(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.Succeed() // 第一轮，数据到了
	l.View(c.add(0))

	l.Begin()
	for _, step := range []int{0, 150, 149} {
		v := l.View(c.add(ms(step)))
		if v.Skeleton || v.Waiting || v.Refreshing {
			t.Fatalf("快刷新：%+v", v)
		}
	}
	l.Succeed()
	if v := l.View(c.add(ms(1))); v.Skeleton || v.Refreshing {
		t.Fatalf("快刷新结束：%+v", v)
	}

	l.Begin()
	l.View(c.add(0))
	if v := l.View(c.add(ms(300))); !v.Refreshing || v.Skeleton || v.Waiting {
		t.Fatalf("慢刷新阈值后应出标记、不出骨架：%+v", v)
	}
	l.Succeed()
	if v := l.View(c.add(ms(10))); !v.Refreshing {
		t.Fatalf("标记要留够：%+v", v)
	}
	if v := l.View(c.add(ms(290))); v.Refreshing {
		t.Fatalf("留够了应撤：%+v", v)
	}
}

// 连敲：读取在途时又 Begin，计时不重来，否则一直敲就永远不出占位
func TestBeginWhileLoadingKeepsClock(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.View(c.add(0))
	for i := 0; i < 5; i++ {
		l.Begin()
		l.View(c.add(ms(60)))
	}
	if v := l.View(c.add(0)); !v.Skeleton { // 共 300ms
		t.Fatalf("连发不该推迟占位：%+v", v)
	}
}

// 骨架还在留的时候又开始新一轮：接着留，不重新数阈值，也不闪掉再出
func TestNewRoundWhileHeldContinues(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.View(c.add(0))
	l.View(c.add(ms(300)))
	l.Succeed()
	l.View(c.add(ms(10))) // 在留
	l.Begin()
	if v := l.View(c.add(ms(10))); !v.Skeleton && !v.Refreshing {
		t.Fatalf("新一轮开始时占位应还在：%+v", v)
	}
}

// 失败：有旧数据标过期、数据不清；没有旧数据才算出错
func TestFailKeepsOldDataAsStale(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.Succeed()
	l.Begin()
	l.Fail()
	v := l.View(c.add(0))
	if !v.Stale || v.Failed || !l.HasData() {
		t.Fatalf("有旧数据：%+v has=%v", v, l.HasData())
	}
	l.Begin()
	l.Succeed()
	if v = l.View(c.add(0)); v.Stale {
		t.Fatalf("读到了应清掉过期：%+v", v)
	}

	var l2 Loader
	l2.Begin()
	l2.Fail()
	if v = l2.View(c.add(0)); !v.Failed || v.Stale || v.Skeleton {
		t.Fatalf("没有旧数据：%+v", v)
	}
}

// Reset 后（换成另一件东西）：阈值内是空位，不是旧内容也不是骨架；标记已出现的话升级成骨架
func TestResetThenLoad(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.Succeed()
	l.Reset()
	l.Begin()
	if v := l.View(c.add(0)); !v.Waiting {
		t.Fatalf("应是空位：%+v", v)
	}
	if v := l.View(c.add(ms(300))); !v.Skeleton {
		t.Fatalf("到阈值应出骨架：%+v", v)
	}

	var m Loader
	m.Begin()
	m.Succeed()
	m.Begin()
	m.View(c.add(0))
	if v := m.View(c.add(ms(300))); !v.Refreshing {
		t.Fatalf("先是标记：%+v", v)
	}
	m.Reset()
	if v := m.View(c.add(ms(1))); !v.Skeleton {
		t.Fatalf("数据作废后升级成骨架：%+v", v)
	}
}

// Cancel（离开页面）：占位立刻撤，数据不动
func TestCancelDropsPlaceholder(t *testing.T) {
	c := newClock()
	var l Loader
	l.Begin()
	l.View(c.add(0))
	l.View(c.add(ms(400)))
	l.Cancel()
	if v := l.View(c.add(0)); v.Skeleton || v.Waiting || v.Refreshing || l.Loading() {
		t.Fatalf("%+v loading=%v", v, l.Loading())
	}
}

// 自定义时长
func TestCustomDelays(t *testing.T) {
	c := newClock()
	l := Loader{Delay: ms(50), Min: ms(80)}
	l.Begin()
	l.View(c.add(0))
	if v := l.View(c.add(ms(50))); !v.Skeleton {
		t.Fatalf("%+v", v)
	}
	l.Succeed()
	if v := l.View(c.add(ms(79))); !v.Skeleton {
		t.Fatalf("%+v", v)
	}
	if v := l.View(c.add(ms(1))); v.Skeleton {
		t.Fatalf("%+v", v)
	}
}
