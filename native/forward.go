package main

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/api"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
)

type forwardLayer struct {
	request     api.ForwardRequest
	view        api.ForwardView
	msgs        []*msg
	rows        []forwardRow
	list        ui.ListState
	busy, dirty bool
	load        loadstate.Loader // "正在从 QQ 取回…"这一行晚点才出现（loadstate）；只管我们自己发起的这一次读
}
type forwardViewState struct {
	open         bool
	box          overlay.BoxState
	stack        []*forwardLayer
	cancel       context.CancelFunc
	active       *forwardLayer
	gen          uint64
	closing      time.Time
	warning      string
	imgs         *imageStore
	cancelImages context.CancelFunc
	rendering    *forwardLayer
	rowAt        int
	downloads    downloadStore
	avatarTop    float32
	viewerUp     bool // 这一轮建面板时有看大图开着（按下那一刻的状态）；看大图是顶层 Overlay，不挂在面板上
}

func (f *forwardViewState) visible() bool { return f.open || !f.closing.IsZero() }

// 取消时就在界面线程结束忙状态；旧回调只凭请求代次判断，不能再改这一层。
func (f *forwardViewState) cancelRead() {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
		f.active.busy = false
		f.active.load.Cancel()
		f.active.dirty = true
		f.active = nil
	}
	f.gen++
}
func (a *app) stopForward() {
	a.forward.downloads.reset()
	a.forward.cancelRead()
	if a.forward.cancelImages != nil {
		a.forward.cancelImages()
		a.forward.cancelImages = nil
	}
	a.forward.imgs = nil
	a.forward.open = false
	a.forward.closing = time.Time{}
	a.forward.stack = nil
}
func (a *app) openForward(source api.EventRef, id string, via []string) {
	f := &a.forward
	if len(via) == 0 {
		a.stopForward()
	}
	for _, ancestor := range via {
		if ancestor == id {
			f.warning = "这条聊天记录已经在上层，不再重复打开"
			return
		}
	}
	if len(via) >= 10 {
		f.warning = "更里面的聊天记录不再展开（最多 10 层）"
		return
	}
	f.open = true
	f.closing = time.Time{}
	f.warning = ""
	layer := &forwardLayer{request: api.ForwardRequest{Source: source, ID: id, Via: append([]string(nil), via...)}, view: api.ForwardView{ID: id, State: "fetching"}}
	f.stack = append(f.stack, layer)
	if id == "" {
		layer.view.State = "empty_id"
		return
	}
	a.loadForward(layer, true)
}
func (a *app) loadForward(layer *forwardLayer, fetch bool) {
	if layer.busy {
		return
	}
	reader, ok := a.reader.(api.ForwardReader)
	if !ok {
		layer.view.State = "not_receiving"
		return
	}
	f := &a.forward
	f.cancelRead()
	ctx, cancel := context.WithCancel(a.base())
	f.cancel, f.active = cancel, layer
	gen := f.gen
	layer.busy = true
	layer.load.Begin()
	layer.dirty = false
	sourceGen := a.srcGen
	actions, canFetch := a.reader.(api.ForwardActions)
	go func() {
		defer cancel()
		result, err := reader.ReadForward(ctx, layer.request)
		if err == nil && fetch && result.Value.State != "ready" && canFetch {
			result, err = actions.FetchForward(ctx, layer.request, time.Minute)
		}
		if ctx.Err() != nil {
			return
		}
		var messages []*msg
		if err == nil {
			for i, n := range result.Value.Nodes {
				m := forwardNodeMsg(layer.request, n, i)
				messages = append(messages, m)
			}
		}
		rows := forwardRows(messages)
		a.post(func() {
			if !f.open || f.gen != gen || a.srcGen != sourceGen {
				return
			}
			layer.busy = false
			if err != nil {
				layer.load.Fail()
			} else {
				layer.load.Succeed()
			}
			f.cancel, f.active = nil, nil
			if err != nil {
				layer.view = result.Value
				if layer.view.State == "" {
					layer.view = api.ForwardView{ID: layer.request.ID, State: "unavailable"}
				}
			} else {
				layer.view, layer.msgs, layer.rows = result.Value, messages, rows
			}
			if layer.dirty {
				a.loadForward(layer, false)
			}
		})
	}()
}
func (a *app) refreshForward() {
	f := &a.forward
	if !f.open || len(f.stack) == 0 {
		return
	}
	for _, layer := range f.stack {
		layer.dirty = true
	}
	layer := f.stack[len(f.stack)-1]
	if !layer.busy {
		a.loadForward(layer, false)
	}
}
func forwardNodeMsg(req api.ForwardRequest, n api.ForwardNode, index int) *msg {
	v := api.MessageView{Ref: req.Source, ActorID: n.UID, ActorKind: "human", DisplayName: n.Name, CreatedAt: n.At, Segments: n.Segments}
	// 转发节点固定是外来普通发送者，不查询本地角色表。
	m := msgFromView(req.Source.Conversation, v, nil)
	m.id = fmt.Sprintf("forward:%s:%d", req.ID, index)
	m.source = req.Source
	m.forwardPath = append(append([]string(nil), req.Via...), req.ID)
	m.mine, m.md = false, false
	for _, d := range n.Segments {
		if d.Kind == "reply" {
			text := d.Summary
			if text == "" {
				text = "[回复了一条消息]"
			}
			m.quote = &quote{ok: true, preview: true, who: "引用的消息", text: oneLine(maskRawMentions(text))}
		}
	}
	return m
}
func (a *app) forwardCard(c *ui.Context, m *msg, s seg) {
	data := content.ForwardSummaryData{ID: s.Ref, Title: "聊天记录", Status: content.SummaryFailed, Error: "没取回来，点开看原因"}
	if b := s.Forward; b != nil {
		if b.Title != "" {
			data.Title = b.Title
		}
		for i, line := range b.Lines {
			if line != "" {
				data.Nodes = append(data.Nodes, content.SummaryNode{ID: fmt.Sprint(i), Text: line})
			}
		}
		if b.Count > 0 || b.State == "ready" {
			data.CountLabel = fmt.Sprintf("查看 %d 条聊天记录", b.Count)
		}
		if b.Missing > 0 {
			data.CountLabel += fmt.Sprintf("（还有 %d 条没取下来）", b.Missing)
		}
		switch b.State {
		case "ready":
			data.Status = content.SummaryReady
			data.Error = ""
		case "fetching":
			data.Status = content.SummaryLoading
			data.StatusText = "正在取回…"
			data.Error = ""
		default:
			data.Error = forwardStateText(api.ForwardView{State: b.State})
		}
	}
	open := func(string) { a.openForward(m.source, s.Ref, m.forwardPath) }
	ui.Column(c).FillWidth().MinWidth(0).Children(func() {
		content.ForwardSummary(c, data, content.ForwardSummaryOptions{Appearance: content.Appearance{Outgoing: m.mine}, OnOpen: open, OnRetry: open, CanRetry: true, RetryLabel: "查看或重试"})
		if data.Status == content.SummaryLoading {
			control.LinkButton(c, "查看取回进度", control.LinkButtonOptions{OnActivate: func() { open(s.Ref) }})
		}
	})
}

func forwardStateText(v api.ForwardView) string {
	switch v.State {
	case "ready":
		if v.Missing > 0 {
			return fmt.Sprintf("还有 %d 条没取下来", v.Missing)
		}
		return fmt.Sprintf("共 %d 条", len(v.Nodes))
	case "fetching":
		if v.Depth > 1 {
			return fmt.Sprintf("正在取回第 %d 层…", v.Depth)
		}
		return "正在从 QQ 取回聊天记录…"
	case "not_receiving":
		return "现在没在接收 QQ，没法取回这条聊天记录。开始接收后再点开。"
	case "refused":
		return "QQ 连接还没核验通过，暂时不取。"
	case "timeout":
		return "QQ 没有及时回话。"
	case "not_connected", "connection_lost":
		return "和 QQ 的连接还没准备好。"
	case "too_large":
		return "这条聊天记录太大，没有显示"
	}
	return "QQ 那边已经取不到这条聊天记录了（可能过期了）。"
}

// 转发栈与节点虚拟化归宿主，模态外壳和退场绘制使用组件库。
func (a *app) forwardWindow(c *ui.Context) {
	f := &a.forward
	if !f.visible() || len(f.stack) == 0 {
		return
	}
	f.downloads.beginFrame()
	defer f.downloads.endFrame()
	if !f.open {
		if !c.Now().Before(f.closing) {
			a.stopForward()
			return
		}
		// mygo 每帧重设 After；动效重建时也要保留最终清场的期限。
		c.After(f.closing.Sub(c.Now()))
	}
	layer := f.stack[len(f.stack)-1]
	// 转发媒体复用正文缓存实现，所有权属于查看窗口，退场后一起收掉。
	f.rendering = layer
	defer func() { f.rendering = nil }()
	if f.imgs != nil {
		f.imgs.beginFrame()
	}
	title := "聊天记录"
	if len(layer.msgs) > 0 {
		title = forwardTitle(layer.view)
	}
	closeView := func() {
		f.open = false
		f.cancelRead()
		motion := theme.T(c).Motion()
		duration := max(motion.Dialog().Duration(), motion.DialogScrim().Duration())
		if c.Preferences().ReduceMotion {
			duration = 0
		}
		f.closing = c.Now().Add(duration)
		c.After(duration + time.Millisecond)
	}
	back := func() {
		if len(f.stack) > 1 {
			f.cancelRead()
			last := len(f.stack) - 1
			f.stack[last] = nil
			f.stack = f.stack[:last]
			f.warning = ""
			parent := f.stack[len(f.stack)-1]
			if parent.dirty {
				a.loadForward(parent, false)
			}
		} else {
			closeView()
		}
	}
	var actions []overlay.BoxAction
	if len(f.stack) > 1 {
		actions = append(actions, overlay.BoxAction{ID: "back", Label: "返回", Left: true, KeepOpen: true})
	}
	if layer.view.State != "ready" && layer.view.State != "fetching" && layer.view.State != "not_receiving" && layer.view.State != "refused" {
		if _, ok := a.reader.(api.ForwardActions); ok {
			actions = append(actions, overlay.BoxAction{ID: "retry", Label: "重试", KeepOpen: true})
		}
	}
	actions = append(actions, overlay.BoxAction{ID: "close", Label: "关闭聊天记录", KeepOpen: true})
	f.box.Open = f.open
	f.viewerUp = false
	c.Key("forward-window")
	overlay.Box(c, &f.box, title, overlay.BoxOptions{
		Wide: true, FullBody: true, BodyHeight: theme.T(c).Metrics().Dialog().MaxBodyHeight(),
		Actions: actions,
		OnAction: func(id string) {
			switch id {
			case "back":
				back()
			case "retry":
				a.loadForward(layer, true)
			case "close":
				closeView()
			}
		},
		OnClose: func() {
			// 看大图的遮罩只关大图；嵌套转发的 Esc 和遮罩返回一层。
			if !f.viewerUp {
				back()
			}
			f.box.Open = f.open
		},
	}, func() {
		t := theme.T(c)
		body := ui.Column(c).Fill().Gap(t.Metrics().Dialog().ButtonGap())
		a.is().observeScale(c, body)
		body.Children(func() {
			ui.List(c.Key(layer), &layer.list, len(layer.rows)).Grow(1).MinHeight(0).ItemKey(func(i int) any {
				row := layer.rows[i]
				return [2]int{row.node, row.part}
			}).Rows(func(row ui.ListRow) {
				f.rowAt = row.Index
				a.forwardRow(c, layer, layer.rows[row.Index])
			})
			stateText := func(s string) ui.Element { return ui.Text(c, s).Wrap().TextColor(t.Colors().Dialog().TextForeground()) }
			if layer.busy && layer.view.State == "fetching" { // 我们自己的读还在途：本地有缓存时一瞬间就回来，别闪一下"正在取回"
				fv := layer.load.View(c.Now())
				loadui.Wake(c, fv)
				loadui.Line(fv, forwardStateText(layer.view), stateText)
			} else {
				stateText(forwardStateText(layer.view))
			}
			if f.warning != "" {
				ui.Text(c, f.warning).Wrap().TextColor(t.Colors().Dialog().TextForeground())
			}
		})
		if f.open && body.OverlayShortcut(ui.Alt, ui.KeyLeft) {
			back()
		}
	})
}
func forwardTitle(v api.ForwardView) string {
	names := []string{}
	seen := map[string]bool{}
	for _, n := range v.Nodes {
		if n.Name != "" && !seen[n.Name] {
			seen[n.Name] = true
			names = append(names, n.Name)
		}
	}
	if len(names) == 0 {
		return "聊天记录"
	}
	if len(names) > 3 {
		return names[0] + "和" + names[1] + "等的聊天记录"
	}
	title := names[0]
	for _, n := range names[1:] {
		title += "和" + n
	}
	return title + "的聊天记录"
}
