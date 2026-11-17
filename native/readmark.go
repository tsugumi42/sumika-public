package main

// 看了就标已读（主人 2026-11-02："现在未读也还不能自动刷新消除"；界面第一次写库）。
// 条件全部成立才标：这个数据源肯标（api.ReadMarker，archive 下后台回 ErrReadMarkUnsupported）、QQ 会话、
// 会话列表里它有未读、第一页已经取回、窗口在前台、聊天滚到了最新一条可见。滚到中间看旧消息不算读到最新，窗口在后台不标。
// 标到的是界面上看到的最新一条的时间（不是"现在"）；后台只往前记，写完概览订阅自己重查，列表上的数字随即归零。
// 谁来问：chatView 在列表建好之后每帧调 markIfRead，它只在"该标而没标过"时起一个后台活（busy 挡住重入、at 挡住重复），
// 不在构建里碰库。写失败只记日志、不弹窗、不崩，同一个时间点不重试（下一条新消息或换会话再试），免得每帧一次。

import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"sumika-native/internal/api"
)

// readMark 是 app 里标已读的状态。fg 在窗口事件里写、界面线程读，所以是原子量；其余只在界面线程碰
type readMark struct {
	logf func(format string, args ...any) // 写日志的去处（外壳接上的日志文件）；nil 退回 log.Printf
	fg   atomic.Bool                      // 窗口在前台：显示着、没最小化、有键盘焦点
	view atomic.Value                     // string：界面此刻正摆在眼前的会话（聊天页里选中的那个；停在记忆、运行、设置页是空串）。界面线程每帧写，通知线程读
	off  bool                             // 这个数据源说不标（archive、没接 ReadMarker 的）：到换数据源（attach）为止不再问
	at   int64                            // 这个会话已经标到哪了（毫秒）；换会话归零
	busy bool                             // 有一个标的活在路上；换会话归零
}

// watching 主人此刻是不是正看着这个会话：窗口在前台，并且聊天页摆着的就是它。通知据此决定弹不弹
func (r *readMark) watching(conv string) bool {
	if conv == "" || !r.fg.Load() {
		return false
	}
	v, _ := r.view.Load().(string)
	return v == conv
}

// publishViewing 把"正摆着哪个会话"交给别的线程读（view 每帧调一次）
func (a *app) publishViewing() {
	v := ""
	if a.rail <= 1 {
		v = a.sel
	}
	a.rm.view.Store(v)
}

func (r *readMark) log(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// unreadOf 会话列表里这个会话现在的未读数；列表里没有就是 0
func (a *app) unreadOf(id string) int {
	for _, list := range [][]conv{a.local, a.homes, a.convs} {
		for _, c := range list {
			if c.Key.Conversation == id {
				return c.Unread
			}
		}
	}
	return 0
}

// markIfRead 见文件头。要在 ui.List 建好之后调：AtEnd 读的是列表上一帧的样子
func (a *app) markIfRead() {
	rm := &a.rm
	if rm.off || rm.busy || a.sel == "" || !a.firstDone || len(a.msgs) == 0 || a.reader == nil {
		return
	}
	marker, ok := a.reader.(api.ReadMarker)
	if !ok {
		rm.off = true
		return
	}
	key := a.keyOf(a.sel)
	if key.Platform != "qq" || a.unreadOf(a.sel) == 0 {
		return
	}
	upTo := a.msgs[len(a.msgs)-1].at // 按 (时间, 序号) 排好的，最后一条的时间最大
	if upTo <= rm.at || !rm.fg.Load() || !a.list.AtEnd() {
		return
	}
	rm.busy = true
	gen, srcGen := a.gen, a.srcGen
	go func() {
		ctx, cancel := context.WithTimeout(a.base(), 10*time.Second)
		defer cancel()
		err := marker.MarkRead(ctx, key, upTo)
		a.post(func() { a.markDone(gen, srcGen, upTo, err) })
	}()
}

// markDone 标已读的结果回到界面线程。换了数据源、换了会话的，只处理"这个数据源不标"，别的丢掉
func (a *app) markDone(gen, srcGen uint64, upTo int64, err error) {
	rm := &a.rm
	if a.srcGen != srcGen {
		return
	}
	if errors.Is(err, api.ErrReadMarkUnsupported) {
		rm.off = true
		if a.gen == gen {
			rm.busy = false
		}
		return
	}
	if a.gen != gen {
		return
	}
	rm.busy = false
	rm.at = upTo // 失败也记上：这个时间点不再重试
	if err != nil {
		rm.log("标已读失败（%s，读到 %s）：%v", a.sel, time.UnixMilli(upTo).Format("15:04:05"), err)
	}
}

// trackForeground 跟踪窗口在不在前台（外壳建好窗口后调一次）。事件都在界面线程里来；
// 焦点自己记，显示和最小化每次现问。变了就叫窗口重画一帧，让 markIfRead 看到新状态
func (a *app) trackForeground() {
	w := a.win
	focused := w.IsFocused()
	update := func() {
		a.rm.fg.Store(focused && w.IsVisible() && !w.IsMinimized())
		w.Invalidate()
	}
	w.OnFocus(func() { focused = true; update() })
	w.OnBlur(func() { focused = false; update() })
	w.OnShow(update)
	w.OnHide(update)
	w.OnMinimize(update)
	w.OnRestore(update)
	update()
}
