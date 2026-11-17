package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 聊天标题栏小字里的"取数据中…"（loadstate 的规则）：阈值内不写，慢了才写。

func subtitleTester(a *app) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) { ui.Text(c, a.subtitle(c)).SingleLine() }, 600, 80)
	tt.Frame()
	return tt
}

func advanceFrames(tt *ui.Tester, steps int, step time.Duration, bad string, t *testing.T) {
	t.Helper()
	for i := 0; i < steps; i++ {
		if tt.HasText(bad) {
			t.Fatalf("第 %d 帧（%d 毫秒）就写了 %q：%v", i, i*int(step/time.Millisecond), bad, tt.Texts())
		}
		tt.Advance(step)
		tt.Frame()
	}
}

// 换会话后第一页还没到：阈值内整行占着但不写字（也不写"0 条"），慢了写"取数据中…"
func TestLoadHintSubtitleFirstPage(t *testing.T) {
	a := &app{loading: true}
	tt := subtitleTester(a)
	advanceFrames(tt, 5, 50*time.Millisecond, "取数据中", t)
	if tt.HasText("0 条") {
		t.Fatalf("第一页没到就写了 0 条：%v", tt.Texts())
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("取数据中…") {
		t.Fatalf("过了阈值应写取数据中：%v", tt.Texts())
	}
	a.loading, a.firstDone, a.msgs = false, true, []*msg{{}, {}}
	tt.Frame()
	if tt.HasText("取数据中") || !tt.HasText("2 条") {
		t.Fatalf("到了应写条数：%v", tt.Texts())
	}
}

// 往上翻一页：阈值内只写条数，慢了才在后面加"· 取数据中…"；快的整个过程字不变
func TestLoadHintSubtitleOlderPage(t *testing.T) {
	a := &app{firstDone: true, hasMore: true, msgs: []*msg{{}, {}, {}}}
	tt := subtitleTester(a)
	a.loading = true
	tt.Frame()
	advanceFrames(tt, 5, 50*time.Millisecond, "取数据中", t)
	a.loading = false // 250 毫秒时回来：从头到尾没写过
	tt.Frame()
	advanceFrames(tt, 8, 250*time.Millisecond, "取数据中", t)

	a.loading = true
	tt.Frame()
	tt.Advance(350 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("3 条 · 取数据中…") {
		t.Fatalf("慢的翻页过了阈值应加取数据中：%v", tt.Texts())
	}
	a.loading = false
	tt.Frame()
	if tt.HasText("取数据中") {
		t.Fatalf("读完提示还在：%v", tt.Texts())
	}
}
