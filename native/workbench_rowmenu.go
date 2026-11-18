package main

// 会话列表行的右键菜单（审查 2026-11-11 第四节）。只接代码里已经有的动作：现在只有“标为已读”（api.ReadMarker，QQ 会话）。
// 置顶、删除或隐藏会话、静音：后台和界面都还没有对应的函数，这里不新写后台逻辑，交回里列出来。
// 以后加了删除类的动作，用 Danger 项，会自动排到菜单最下面，并且必须先走确认对话框。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// convRowMenu 是一行会话的右键菜单；这一行永远用不上的（不是 QQ 会话、数据源不会标已读）返回空，右键什么也不出。
func (a *app) convRowMenu(id string) []winshell.MenuItem {
	if _, ok := a.reader.(api.ReadMarker); !ok || a.keyOf(id).Platform != "qq" {
		return nil
	}
	item := winshell.MenuItem{Label: "标为已读"}
	switch {
	case a.rm.off: // 只看历史（archive）等不写库的数据源
		item.Disabled, item.Trailing = true, "这里不标已读"
	case a.unreadOf(id) <= 0:
		item.Disabled, item.Trailing = true, "没有未读"
	default:
		item.Action = func() { a.markReadNow(id) }
	}
	return []winshell.MenuItem{item}
}

// markReadNow 把一个会话标到它最新一条消息。和看了自动标（markIfRead）走同一个后台入口，后台只往前记、不超过会话里真有的最新一条；
// 不碰“当前会话标到哪了”的状态（那是自动标的）。写失败只记日志，未读照旧。
func (a *app) markReadNow(id string) {
	marker, ok := a.reader.(api.ReadMarker)
	cv := a.convByID(id)
	if !ok || cv == nil || cv.Last.CreatedAt == 0 {
		return
	}
	key, upTo, srcGen := a.keyOf(id), cv.Last.CreatedAt, a.srcGen
	go func() {
		ctx, cancel := context.WithTimeout(a.base(), 10*time.Second)
		defer cancel()
		err := marker.MarkRead(ctx, key, upTo)
		a.post(func() {
			if a.srcGen != srcGen {
				return
			}
			switch {
			case errors.Is(err, api.ErrReadMarkUnsupported):
				a.rm.off = true
			case err != nil:
				a.rm.log("手动标已读失败（%s）：%v", id, err)
			}
		})
	}()
}
