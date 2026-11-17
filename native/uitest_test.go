package main

// 界面测试的公用小工具：接缝句柄（读元素在界面上量出来的位置）、编展示 DTO。数据全是编的。

import (
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// probes 测试用的接缝：a.probe 发句柄，测试从句柄读元素在界面上量出来的位置（row.go 的 probeOf）
type probes struct {
	hs    map[string]*ui.Handle
	ctx   *ui.Context
	theme ui.Theme // 最近一帧的主题（取值要在构建时拿，构建外 c.Theme() 是空的）
	tt    *ui.Tester
}

// refresh 再画两帧（异步的东西落地以后位置才定）
func (p *probes) refresh() { p.tt.Frame(); p.tt.Frame() }

// pixelAt 最近一帧的图里某个点的颜色
func (p *probes) pixelAt(x, y int) color.RGBA { return p.tt.Image().RGBAAt(x, y) }

func newProbes(a *app) *probes {
	p := &probes{hs: map[string]*ui.Handle{}}
	a.probe = func(name string) *ui.Handle {
		h := p.hs[name]
		if h == nil {
			h = &ui.Handle{}
			p.hs[name] = h
		}
		return h
	}
	return p
}

// box 名字对应元素最近一帧的位置；没有这个名字或没画出来就失败
func (p *probes) box(t *testing.T, name string) ui.Rect {
	t.Helper()
	h := p.hs[name]
	if h == nil {
		t.Fatalf("没有绑过 %q；有 %v", name, p.names())
	}
	r := h.Bounds(p.ctx)
	if r.W == 0 || r.H == 0 {
		t.Fatalf("%q 没画出来（%+v）", name, r)
	}
	return r
}

// rect 名字对应元素最近一帧的位置，不查大小（垫位的空位高度是 0）；没绑过就失败
func (p *probes) rect(t *testing.T, name string) ui.Rect {
	t.Helper()
	h := p.hs[name]
	if h == nil {
		t.Fatalf("没有绑过 %q；有 %v", name, p.names())
	}
	return h.Bounds(p.ctx)
}

// hasExact 屏幕上有没有一段完全等于 s 的文字（tt.HasText 是子串匹配，"2" 会命中 "12:00"）
func hasExact(tt *ui.Tester, s string) bool {
	for _, x := range tt.Texts() {
		if x == s {
			return true
		}
	}
	return false
}

func (p *probes) names() []string {
	var out []string
	for n := range p.hs {
		out = append(out, n)
	}
	return out
}

// chatTester 渲染 a.chatView，返回测试器和接缝；先画两帧，让位置落定
func chatTester(a *app, w, h int) (*ui.Tester, *probes) {
	p := newProbes(a)
	tt := testerOf(w, h, func(c *ui.Context) { p.ctx, p.theme = c, *c.Theme(); a.chatView(c) })
	tt.Frame()
	p.tt = tt
	return tt, p
}

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local).UnixMilli()

// view 编一条展示 DTO：actorKind 决定在哪一边（QQ 会话里 character 在右）
func view(id, actorKind, name, text string, at int64) api.MessageView {
	k := demoKey("qq:group:1001")
	return api.MessageView{
		Ref: api.EventRef{Conversation: k, Event: api.EventKey{Platform: "qq", EventID: id}},
		Seq: at, Version: 1, ActorKind: actorKind, ActorID: actorKind + "-" + name, DisplayName: name, Kind: "message",
		Origin: map[string]string{"character": "character", "owner": "owner"}[actorKind], CreatedAt: at, Text: text,
		Segments: []api.DisplaySegment{{Kind: "text", Text: text}},
	}
}

// quoting 编一条引用了 target 的消息；ready 为假时原消息读不到
func quoting(v api.MessageView, target, who, text string, ready bool) api.MessageView {
	k := v.Ref.Conversation
	ref := api.EventRef{Conversation: k, Event: api.EventKey{Platform: "qq", EventID: target}}
	v.ReplyTo = &ref
	if ready {
		v.Reply = api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{Ref: ref, DisplayName: who, Text: text}}
	} else {
		v.Reply.State = api.Unsupported
	}
	return v
}

func msgsOf(t *testing.T, vs ...api.MessageView) []*msg {
	t.Helper()
	var out []*msg
	for _, v := range vs {
		out = append(out, msgFromView(demoKey("qq:group:1001"), v, charOf))
	}
	return out
}
