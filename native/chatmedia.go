package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

// chatMedia 是一条消息这一版的媒体宿主。旧消息、被换掉的消息和过期数据源一律当缺失。
type chatMedia struct {
	a          *app
	still      bool // 系统设了减少动态效果：动的小黄脸只显示第一帧
	message    *msg
	index      int
	gen        uint64
	sourceGen  uint64
	version    uint64
	layer      *forwardLayer
	forwardGen uint64
	images     *imageStore
	downloads  *downloadStore
	building   bool
	play       func(*imgEntry) // 只在创建它的这一帧构建里调用
}

func (a *app) bindChatMedia(c *ui.Context, m *msg, i int) *chatMedia {
	layer := a.forward.rendering
	r := &chatMedia{
		a: a, message: m, index: i, gen: a.gen, sourceGen: a.srcGen, version: m.version,
		layer: layer, images: a.is(), building: c != nil,
	}
	if c != nil {
		r.still = c.Preferences().ReduceMotion
		r.play = func(e *imgEntry) {
			a.rowAt = i
			a.play(c, e)
		}
	}
	if layer != nil {
		r.forwardGen = a.forward.gen
		r.images = a.forward.imgs
	}
	r.downloads = a.downloadStore()
	return r
}

func (r *chatMedia) valid(id string) bool {
	if r == nil || r.a == nil || r.message == nil || r.message.id != id || r.message.version != r.version {
		return false
	}
	if r.layer != nil {
		f := r.a.forward
		if !f.open || r.forwardGen != f.gen || len(f.stack) == 0 || f.stack[len(f.stack)-1] != r.layer {
			return false
		}
		return true
	}
	if r.gen != r.a.gen || r.sourceGen != r.a.srcGen || r.index < 0 || r.index >= len(r.a.msgs) || r.a.msgs[r.index] != r.message {
		return false
	}
	return true
}

func (r *chatMedia) segment(id string, position int) (seg, bool) {
	if !r.valid(id) || position < 0 || position >= len(r.message.segs) {
		return seg{}, false
	}
	s := r.message.segs[position]
	if s.position != position {
		return seg{}, false
	}
	return s, true
}

func (r *chatMedia) Image(id string, position int, cover bool, wantPx int) chatui.MediaResult {
	s, ok := r.segment(id, position)
	if !ok || r.images == nil {
		return chatui.MediaResult{State: chatui.MediaMissing}
	}
	if cover {
		if s.Cover == nil {
			return chatui.MediaResult{State: chatui.MediaMissing, Description: s.Summary}
		}
		s.Media = s.Cover
		s.Animated = false
	} else if s.Media == nil {
		return chatui.MediaResult{State: chatui.MediaMissing, Description: s.Summary}
	}
	if wantPx < 1 {
		wantPx = 1
	}
	return r.imageResult(s, float32(wantPx))
}

// QuoteThumb 引用块里被引消息的缩略图：凭据在 m.quote.thumb（后台的引用投影给的），走和消息图片同一条加载链路。
// 被引消息不在已加载范围也一样能取，取不到就是 Failed / Missing，引用块退回只有文字。
func (r *chatMedia) QuoteThumb(id string, wantPx int) chatui.MediaResult {
	if !r.valid(id) || r.images == nil || r.message.quote == nil || r.message.quote.thumb == nil {
		return chatui.MediaResult{State: chatui.MediaMissing}
	}
	return r.imageResult(*r.message.quote.thumb, float32(max(1, wantPx)))
}

func (r *chatMedia) imageResult(s seg, cssW float32) chatui.MediaResult {
	sticker := isSticker(s)
	k := keyOf(s, cssW, sticker, r.images.scale)
	if s.Animated && sticker {
		k.tier = min(k.tier, stickerFrameMax)
	}
	e := r.images.getRef(k, refOf(s))
	out := chatui.MediaResult{Description: s.Summary}
	if e == nil {
		out.State = chatui.MediaLoading
		return out
	}
	switch e.state {
	case imgOK:
		out.State = chatui.MediaReady
		out.Image = e.bmp
		if s.Animated && r.play != nil {
			r.play(e)
		}
	case imgFailed, imgBad:
		out.State = chatui.MediaFailed
		if _, text := imagePresentation(e); text != "" {
			out.Description = text
		}
	default:
		out.State = chatui.MediaLoading
	}
	return out
}

func (r *chatMedia) Face(id string) chatui.MediaResult {
	if r == nil || r.a == nil {
		return chatui.MediaResult{State: chatui.MediaMissing, Description: segview.FaceFallback(id, "face")}
	}
	f := r.a.fs()
	res := resolveFace(segview.FaceRef{ID: id, Kind: "face"}, f.table, f.state)
	if res.Kind != faceAsImage {
		text := res.Text
		if text == "" {
			text = res.Copy
		}
		return chatui.MediaResult{State: chatui.MediaMissing, Description: text}
	}
	if bmp := f.peek(id); bmp != nil {
		f.animate(id, r.still)
		return chatui.MediaResult{State: chatui.MediaReady, Image: bmp, Description: res.Copy}
	}
	if f.fetchPNG != nil && f.post != nil && f.ctx != nil {
		f.want(id)
	}
	if f.state(id) == faceMissing {
		return chatui.MediaResult{State: chatui.MediaMissing, Description: res.Copy}
	}
	return chatui.MediaResult{State: chatui.MediaLoading, Description: res.Copy}
}

func (r *chatMedia) Download(id string, position int) chatui.MediaResult {
	s, ok := r.segment(id, position)
	if !ok {
		return chatui.MediaResult{State: chatui.MediaMissing}
	}
	var d *downloadUI
	if r.downloads != nil {
		d = r.downloads.items[s.downloadKey]
	}
	if d == nil && r.building && r.a != nil && (r.layer == nil || r.a.forward.rendering == r.layer) {
		d = r.a.watchDownload(s)
	} else if d != nil && r.building && r.downloads != nil {
		// 这一帧用到了它：记下来。不记的话 endFrame 两帧后就把它当"没人看"取消并删掉，
		// 下一帧又新建一个、状态回到默认再被订阅推回去——悬停让界面连续重建时就是肉眼可见的来回闪
		d.seen = r.downloads.frame
	}
	if d != nil && d.resolved != nil {
		resolved := *d.resolved
		resolved.position = s.position
		s = resolved
	}
	ready := s.Media != nil && s.Download == nil && s.Kind != "video"
	status, progress, text := downloadPresentation(d, ready)
	out := chatui.MediaResult{Description: text, Progress: progress}
	switch status {
	case content.MediaLoading:
		out.State = chatui.MediaDownloading
		if d == nil || d.progress.Total <= 0 {
			out.Progress = -1
		}
	case content.MediaReady:
		out.State = chatui.MediaDownloaded
		if d == nil {
			out.State = chatui.MediaReady
		}
	case content.MediaFailed:
		out.State = chatui.MediaFailed
	default:
		out.State = chatui.MediaMissing
	}
	return out
}

func (r *chatMedia) start(id string, position int, cancel bool) {
	s, ok := r.segment(id, position)
	if !ok || r.a == nil {
		return
	}
	d := r.a.watchDownload(s)
	if d == nil {
		return
	}
	r.a.changeDownload(d, cancel)
}

func (r *chatMedia) open(id string, position int) {
	s, ok := r.segment(id, position)
	if !ok {
		return
	}
	if s.Kind == "image" || s.FileKind == "image" || (s.Kind == "emoji" && s.Sticker) {
		r.viewImage(id, position)
		return
	}
	if r.downloads != nil {
		if d := r.downloads.items[s.downloadKey]; d != nil && d.resolved != nil && d.resolved.FileKind == "image" {
			r.viewImage(id, position)
		}
	}
}

func (r *chatMedia) openForward(id string, position int) {
	s, ok := r.segment(id, position)
	if !ok || r.a == nil {
		return
	}
	r.a.openForward(r.message.source, s.Ref, r.message.forwardPath)
}

func (a *app) chatHostFor(c *ui.Context, m *msg, i int) chatui.Host {
	r := a.bindChatMedia(c, m, i)
	h := chatui.Host{
		T: a.chatTokens(), Font: c.Theme().Font, Mono: monoFamily(), Group: a.group, NoEnter: true, SquareAvatar: a.keyOf(a.sel).Platform == "local",
		SelfChar:         chatui.DefaultChar, // 我的气泡一律是原型的强调色（main 的规则，self_char_test 守着）
		OnViewImage:      func(id string, pos int) { r.viewImage(id, pos) },
		OnOpenMedia:      func(id string, pos int) { r.open(id, pos) },
		OnDownload:       func(id string, pos int) { r.start(id, pos, false) },
		OnCancelDownload: func(id string, pos int) { r.start(id, pos, true) },
		OnForward:        func(id string, pos int) { r.openForward(id, pos) },
		OnLink:           func(url string) { a.openLink(c, url) },
		OnQuote:          func(_, target string) { a.jumpTo(target) },
	}
	if a.chatMD == nil {
		a.chatMD = chatui.NewMD()
	}
	// 她的回复（msg.md）走 Markdown；群友和主人的话仍按原文
	h.MD = a.chatMD
	h.Media = r             // 图片、贴纸、行内表情都从这里取；漏了这一行，新视图里只会画 [图片]、[表情] 占位
	h.Selection = &a.selBar // 选区条总有"复制"；只有本地私聊真实后台才有"回复"
	if a.canQuote() {
		h.OnReply = a.replyToSelection
	}
	if a.canWhisper() { // 本地群聊：引用是悄悄话（复制、多选、引用 ▸），见 workbench_whisper.go
		h.WhisperTargets, h.OnWhisper = a.whisperTargets, a.whisperQuote
		h.OnMultiSelect = func(id string) { a.pick.Start(a.sel, id) }
		if a.picking() {
			h.Pick, h.PickOrder = &a.pick, a.msgOrder
		}
	}
	// 本地私聊、本地群聊：悬停操作条里的“本轮用量”（数据来自右栏已读到的报告，没有就不摆）
	h.Usage, h.OnUsage = a.turnUsageFor, a.openSideTurn
	h.Highlight, h.OnQuotePart = &a.quoteHL, a.jumpToQuote
	h.Sticky = &a.stickyAv
	h.Flash = a.flashAlpha(c)
	h.ViewRect = func() ui.Rect { // 选区条别超出聊天列里看得见的部分：列表框去掉上下两个浮层
		r := a.list.Bounds(c)
		top, bottom := a.wbOverlayInsets()
		r.Y += top
		r.H -= top + bottom
		return r
	}
	if k := a.workbench.shell.kit; k != nil && a.workbenchEnabled() {
		h.MenuTheme = k.MenuTheme()
	}
	h.StepOpen = a.stepOpenState
	h.Appear = func(string) bool { // 新到的消息、发出的话：滑入淡入。翻历史、重新挂上的行不动
		if m.entered || m.noEnter || !(m.enterNow || m.live) {
			return false
		}
		m.entered = true
		return true
	}
	h.ReactorImage = func(avatar string) ui.ImageSource {
		a.avs().want(avatar)
		if bmp := a.avs().peek(avatar); bmp != nil {
			return bmp
		}
		return nil
	}
	if m.avatar != "" {
		key := m.avatar
		id := m.id
		h.AvatarImage = func(messageID string) ui.ImageSource {
			if messageID != id {
				return nil
			}
			a.avs().want(key)
			return a.avs().peek(key)
		}
	}
	return h
}

func (r *chatMedia) viewImage(id string, position int) {
	s, ok := r.segment(id, position)
	if !ok || s.Media == nil || r.images == nil {
		return
	}
	k := keyOf(s, 256, isSticker(s), r.images.scale)
	if e := r.images.peek(k); e != nil && (e.state == imgFailed || e.state == imgBad) {
		r.images.retry(k)
	}
	v := chatLightbox{msgID: id, open: true, images: r.images, gen: r.gen, srcGen: r.sourceGen, layer: r.layer, forwardGen: r.forwardGen}
	for _, item := range r.message.segs {
		if item.Media == nil {
			continue
		}
		if item.Kind != "image" && item.FileKind != "image" && !(item.Kind == "emoji" && item.Sticker) {
			continue
		}
		if item.position == position {
			v.at = len(v.segments)
		}
		v.segments = append(v.segments, item)
	}
	if len(v.segments) == 0 {
		v.segments = []seg{s}
	}
	r.a.chatLightbox = v
}
