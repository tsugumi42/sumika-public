package main

// 滚到顶自动加载本地更早的消息（主人 2026-11-02）。原来顶上一个"加载更早 200 条"按钮；现在列表滚到接近顶部（可见的第一行在前 olderAhead 行以内）
// 就自动取下一页（olderAutoPage 条），按钮留作兜底（自动取失败以后停下，等主人点）。
// 谁来问：chatView 在列表建好之后每帧调 loadOlderNearTop，条件不成立或已经在取就什么也不做；成立时把取页交给界面线程的下一个活（post），
// 不在构建里改状态。取回来插在前面，列表有 Key，加在上面的行不会让正在看的那一行动（见 scrolltop_test.go 量的跳变）。
// 本地翻到头了（没有更早的）在标题栏写"本地没有更早的了"；从 SnowLuma 往前拉要等 QQ 历史接口（feat/qq-media-backfill），见待办。

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/loadui"
)

const (
	olderAhead    = 20  // 可见的第一行 ≤ 这个行号就开始取：提前一屏多，滚到顶之前就取回来
	olderAutoPage = 200 // 自动每次取多少条（和按钮一样）
)

// loadOlderNearTop 见文件头。要在 ui.List 建好之后调：Visible 读的是列表上一帧的样子
func (a *app) loadOlderNearTop() {
	// 前面刚加了行（取回一页、换会话）的这一帧，列表还没重新排版，Visible 给的是旧数组的下标：
	// 取回 200 条的那一帧它仍说"第 14 行在最上面"，信了就会马上接着取下一页，一路取到头。等排完版的下一帧再看
	top := ""
	if len(a.msgs) > 0 {
		top = a.msgs[0].id
	}
	stale := top != a.olderTop
	a.olderTop = top
	if stale || !a.firstDone || !a.hasMore || a.loading || a.olderOff || a.jump.id != "" || a.reader == nil || len(a.msgs) == 0 {
		return
	}
	first, last := a.list.Visible()
	if last < first || first > olderAhead { // 还没排过版时 Visible 回 (0, -1)，不当成"在顶上"
		return
	}
	gen := a.gen
	a.post(func() {
		if a.gen == gen {
			a.olderPage(olderAutoPage)
		}
	})
}

// subtitle 聊天标题栏下面的小字：条数，在取就写在取，翻到头了写明本地没有更早的了。
// "取数据中…"读得慢了才写（loadstate）；换会话后第一页还没到时连"0 条"也不写（先占着这一行），到了再写条数
func (a *app) subtitle(c *ui.Context) string {
	if a.loading && !a.subLoad.Loading() {
		a.subLoad.Begin()
	} else if !a.loading && a.subLoad.Loading() {
		a.subLoad.Succeed()
	}
	v := a.subLoad.View(c.Now())
	loadui.Wake(c, v)
	sub := fmt.Sprintf("%d 条", len(a.msgs))
	switch {
	case a.loading && !a.firstDone:
		sub = "　"
		if loadui.Visible(v) {
			sub = "取数据中…"
		}
	case a.loading:
		if loadui.Visible(v) {
			sub += " · 取数据中…"
		}
	case a.firstDone && !a.hasMore && len(a.msgs) > 0:
		sub += " · 本地没有更早的了"
	}
	return sub
}
