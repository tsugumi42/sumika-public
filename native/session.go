package main

// 打开的会话（规格-界面接Go后台 4.3、4.4）：先登记订阅再拿初始快照，之后后台每次提交都推一份新快照，
// 不再每 3 秒取最新 20 条。快照里的最新一页和变化的行按事件 id 并进来：新的标"实时到"、按 (created_at, seq) 插到位置上，
// 已有的只在后台行版本更新时替换，旧页不能把新状态（撤回、发送结果）改回去。往上翻用后台给的游标，不拿 seq 拼。
// 读库、建 msg、预排都在后台线程；界面线程只并结果。每次换会话代次加一，晚到的结果对不上就丢（A→B→A 不串）。

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tsugumi42/MewTypeUI/layout"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// pageResult 后台线程建好的一批消息
type pageResult struct {
	msgs  []*msg
	title string
	group bool
	gone  map[string]bool
	err   error
	next  api.Cursor
	more  bool
	prev  *msg // 这一页最老一条再往前的那条消息（a.topPrev）
}

func (a *app) keyOf(id string) api.ConversationKey {
	if k, ok := a.keys[id]; ok {
		return k
	}
	return demoKey(id)
}

func (a *app) titleFor(id string) string {
	for _, list := range [][]conv{a.local, a.homes, a.convs} {
		for _, c := range list {
			if c.Key.Conversation == id {
				return c.Title
			}
		}
	}
	return id
}

// open 换到一个会话并订阅它
func (a *app) open(id string) {
	a.pn.layout.Active = layout.Chat
	if a.sel == id || a.reader == nil {
		return
	}
	a.switchTo(id)
	k := a.keyOf(id)
	a.title, a.group = a.titleFor(id), k.Scene == "group"
	ctx, cancel := context.WithCancel(a.base())
	a.convCancel = cancel
	go a.watchConversation(ctx, a.reader, k, a.gen)
	a.refreshHeld()
}

// switchTo 换会话的状态重置（不取数据，测试里直接调）：取消上一个会话的订阅，代次加一
func (a *app) switchTo(id string) {
	a.downloads.reset()
	if a.convCancel != nil {
		a.convCancel()
		a.convCancel = nil
	}
	a.sel, a.msgs, a.list, a.status, a.loading = id, nil, a.newList(), "", true
	a.recalled, a.speakers = map[string]bool{}, map[string]bool{}
	a.cursor, a.hasMore, a.olderOff, a.topPrev = "", false, false, nil
	a.jump, a.flashID = jumpState{}, ""
	a.selBar, a.quoteHL = chatui.SelectionBar{}, chatui.Highlight{}
	a.chatMD = nil
	a.gen++
	a.firstDone = false
	a.arr.reset()
	// 新会话重新算标到哪了（off 是数据源的事，不动）
	a.rm.at, a.rm.busy = 0, false
	a.fs().clearMissing()  // 确认没有的小黄脸图，换了会话再取一次
	a.avs().clearMissing() // 没有头像的，换了会话也再取一次
}

// build 在后台线程把一批 DTO 建成消息并预排；通知（撤回等）不显示成气泡，撤回状态后台已经算在行上
func (a *app) build(k api.ConversationKey, views []api.MessageView, charOf func(string) (character, bool)) pageResult {
	var r pageResult
	for _, v := range views {
		if v.Recalled {
			if r.gone == nil {
				r.gone = map[string]bool{}
			}
			r.gone[v.Ref.Event.EventID] = true
		}
		if !showsAsRow(v) {
			continue
		}
		r.msgs = append(r.msgs, msgFromView(k, v, charOf))
	}
	return r
}

// showsAsRow：普通消息和宿主写的系统行（命令回复、目标达成等，kind 是 notice、发言人是 system）显示成一行，
// 群事件显示成通知条（v.Service），撤回这类通知不显示。预览用的 store.DisplayScope.ShownOnly 不算通知条：群事件不抢预览、不把会话顶到前面
func showsAsRow(v api.MessageView) bool {
	return v.Kind == "message" || (v.Kind == "notice" && v.ActorKind == "system") || v.Service != ""
}

// prevOf 把后台给的"最老一条再往前的消息"建成只用得上发送人和时间的 msg（没有就是 nil）
func prevOf(k api.ConversationKey, v *api.MessageView, charOf func(string) (character, bool)) *msg {
	if v == nil || !showsAsRow(*v) {
		return nil
	}
	return msgFromView(k, *v, charOf)
}

// charsSnapshot 给后台线程用的只读角色表（界面线程里取）
func (a *app) charsSnapshot() func(string) (character, bool) {
	chars := append([]character(nil), a.chars...)
	return func(id string) (character, bool) {
		for _, c := range chars {
			if c.ID == id {
				return c, true
			}
		}
		return character{}, false
	}
}

func (a *app) watchConversation(ctx context.Context, r api.Reader, k api.ConversationKey, gen uint64) {
	charOf := make(chan func(string) (character, bool), 1)
	a.post(func() { charOf <- a.charsSnapshot() })
	var lookup func(string) (character, bool)
	select {
	case lookup = <-charOf:
	case <-ctx.Done():
		return
	}
	sub, err := r.WatchConversation(ctx, api.ConversationRequest{Key: k, Limit: 200})
	if err != nil {
		a.post(func() {
			if a.gen == gen {
				a.loading, a.status = false, "打开会话失败："+err.Error()
			}
		})
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		if snap.Err != nil && !snap.Stale {
			err := snap.Err
			a.post(func() {
				if a.gen == gen {
					a.loading, a.status = false, "取消息失败："+err.Error()
				}
			})
			continue
		}
		page := a.build(k, snap.Value.Page.Items, lookup)
		changed := a.build(k, snap.Value.Changed, lookup)
		page.next, page.more = snap.Value.Page.NextCursor, snap.Value.Page.HasMore
		page.prev = prevOf(k, snap.Value.Page.Before, lookup)
		stale := snap.Err
		a.post(func() { a.applySnapshot(gen, page, changed, stale) })
	}
}

// applySnapshot 第一份快照整个换上（记下往上翻的游标）；之后的按 id 并进来，返回新加了几条
func (a *app) applySnapshot(gen uint64, page, changed pageResult, stale error) int {
	if a.gen != gen {
		return 0
	}
	a.refreshForward()
	a.loading = false
	if stale != nil {
		a.status = "刷新失败，显示的是上一次的：" + stale.Error()
	} else {
		a.status = ""
	}
	if !a.firstDone {
		a.takePage(page.msgs, page.gone, false)
		a.cursor, a.hasMore, a.topPrev = page.next, page.more, page.prev
		a.firstDone = true
		a.reconcileLive()
		if a.shown == 0 {
			a.shown = time.Since(a.started)
			if f := os.Getenv("SUMIKA_FIRSTPAGE"); f != "" { // 外部计时用：第一页出来时的绝对时间（毫秒）
				_ = os.WriteFile(f, []byte(fmt.Sprint(time.Now().UnixMilli())), 0o644)
			}
		}
		return len(page.msgs)
	}
	a.noteMeta(page.msgs, page.gone)
	a.noteMeta(changed.msgs, changed.gone)
	var n, m int
	a.msgs, n = mergeLive(a.msgs, page.msgs)
	a.msgs, m = mergeLive(a.msgs, changed.msgs)
	a.reconcileLive()
	return n + m
}

// takePage 把一页并进当前会话：older 为真插到前面（跳过已有的），否则整个换掉
func (a *app) takePage(got []*msg, gone map[string]bool, older bool) {
	a.noteMeta(got, gone)
	if !older {
		a.msgs = got
		return
	}
	known := make(map[string]bool, len(a.msgs))
	for _, m := range a.msgs {
		known[m.id] = true
	}
	fresh := got[:0:0]
	for _, m := range got {
		if !known[m.id] {
			fresh = append(fresh, m)
		}
	}
	a.msgs = append(fresh, a.msgs...)
}

// noteMeta 把一页里的撤回和说过话的角色并进会话的集合
func (a *app) noteMeta(got []*msg, gone map[string]bool) {
	for id := range gone {
		a.recalled[id] = true
	}
	for _, m := range got {
		if m.recalled {
			a.recalled[m.id] = true
		}
		if m.isChar {
			a.speakers[m.actor] = true
		}
	}
}

// mergeLive 把订阅推来的消息并进 have：已有的按后台行版本替换（版本旧的不动，"实时到"标记保留），
// 新的标 live，按 (created_at, seq) 插到位置上（补录的老消息 seq 大，按 seq 排会排错）
func mergeLive(have, got []*msg) ([]*msg, int) {
	at := make(map[string]int, len(have))
	for i, m := range have {
		at[m.id] = i
	}
	n := 0
	for _, m := range got {
		if i, ok := at[m.id]; ok {
			if old := have[i]; m.version > old.version {
				m.live = old.live
				m.markdown = old.markdown
				m.entered, m.noEnter, m.listKey, m.procRow = old.entered, old.noEnter, old.listKey, old.procRow
				have[i] = m
			}
			continue
		}
		m.live = true
		i := len(have)
		for i > 0 && after(have[i-1], m) {
			i--
		}
		have = append(have, nil)
		copy(have[i+1:], have[i:])
		have[i] = m
		for id, j := range at {
			if j >= i {
				at[id] = j + 1
			}
		}
		at[m.id] = i
		n++
	}
	return have, n
}

// after a 排在 b 后面：先比发送时间，同一毫秒比序号
func after(a, b *msg) bool {
	return a.at > b.at || (a.at == b.at && a.seq > b.seq)
}

// olderPage 往上再取一页（后台给的游标）
func (a *app) olderPage(limit int) {
	if a.loading || !a.hasMore || a.reader == nil {
		return
	}
	a.loading = true
	key, gen, cursor, r, lookup := a.keyOf(a.sel), a.gen, a.cursor, a.reader, a.charsSnapshot()
	ctx := a.base()
	go func() {
		res := a.readOlder(ctx, r, key, cursor, limit, lookup)
		a.post(func() { a.applyOlder(gen, res) })
	}()
}

func (a *app) readOlder(ctx context.Context, r api.Reader, k api.ConversationKey, cursor api.Cursor, limit int, lookup func(string) (character, bool)) pageResult {
	p, err := r.ReadMessages(ctx, api.ConversationRequest{Key: k, Limit: limit}, cursor)
	if err != nil {
		return pageResult{err: err}
	}
	res := a.build(k, p.Value.Items, lookup)
	res.next, res.more = p.Value.NextCursor, p.Value.HasMore
	res.prev = prevOf(k, p.Value.Before, lookup)
	return res
}

func (a *app) applyOlder(gen uint64, r pageResult) {
	if a.gen != gen {
		return
	}
	a.loading = false
	if r.err != nil {
		a.status = "取更早的消息失败：" + r.err.Error()
		a.olderOff = true // 自动翻到这里停：同一个位置不停地重试只会刷屏；点“加载更早”再试
		a.jump = jumpState{}
		return
	}
	a.takePage(r.msgs, r.gone, true)
	a.reconcileLive()
	a.cursor, a.hasMore, a.topPrev = r.next, r.more, r.prev
	if id := a.jump.id; id != "" { // 点引用时原消息还没加载：这一页里有了就跳，没有接着往上找
		if a.indexOf(id) >= 0 {
			a.jumpTo(id)
		} else {
			a.continueJump()
		}
	}
}

// stress 演示数据的压测：一直往上取，直到有 n 条或者没有更早的（生产没有这个按钮）
func (a *app) stress(n int) {
	if a.loading || len(a.msgs) == 0 || a.reader == nil {
		return
	}
	a.loading = true
	key, gen, r, lookup := a.keyOf(a.sel), a.gen, a.reader, a.charsSnapshot()
	ctx := a.base()
	go func() {
		defer a.post(func() {
			if a.gen == gen {
				a.loading = false
			}
		})
		for {
			var cursor api.Cursor
			var more bool
			var have int
			done := make(chan struct{})
			a.post(func() { cursor, more, have = a.cursor, a.hasMore, len(a.msgs); close(done) })
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
			if !more || have >= n {
				return
			}
			res := a.readOlder(ctx, r, key, cursor, 1000, lookup)
			applied := make(chan bool, 1)
			a.post(func() {
				if a.gen != gen || res.err != nil {
					applied <- false
					return
				}
				a.takePage(res.msgs, res.gone, true)
				a.cursor, a.hasMore, a.topPrev = res.next, res.more, res.prev
				applied <- true
			})
			select {
			case ok := <-applied:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
