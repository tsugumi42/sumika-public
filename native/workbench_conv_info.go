package main

// 这个对话的信息（ADR-351，规格-输入框胶囊和会话信息）：上下文用量画在标题栏头像外圈、也写在右栏“上下文”栏顶部；
// 从输入框搬来的模型、工作区、审批方式放在同一块里。用量和右栏的轮次列表是同一份数据、同一个刷新节奏。

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/winshell"
)

// convUsage 是最近一次请求用了多少上下文。ok 为假：没请求过，或者模型的窗口不知道，就不画。
type convUsage struct {
	used, window int64
	ok           bool
}

func (u convUsage) ratio() float32 {
	if !u.ok || u.window <= 0 {
		return 0
	}
	return float32(float64(u.used) / float64(u.window))
}

// hint 是“上下文用了 62%（8.1 万 / 13 万）”。
func (u convUsage) hint() string {
	if !u.ok {
		return ""
	}
	return fmt.Sprintf("上下文用了 %d%%（%s / %s）", int(u.ratio()*100+0.5), wanTokens(u.used), wanTokens(u.window))
}

// wanTokens 把 token 数写成“8.1 万”“3200”。
func wanTokens(n int64) string {
	if n < 10000 {
		return strconv.FormatInt(n, 10)
	}
	v := float64(n) / 10000
	if v >= 100 {
		return fmt.Sprintf("%.0f 万", v)
	}
	return fmt.Sprintf("%.1f 万", v)
}

// ensureConvTurns 让这个会话的轮次数据跟着走：换会话清掉，到点刷新。聊天区每帧调一次（头像环要数），右栏也用它。
// 只有本地会话有这份数据（QQ 场合右栏是“活动”）。
func (a *app) ensureConvTurns(c *ui.Context) *sideContextState {
	st := &a.workbench.shell.sideContext
	if st.conv != a.sel {
		gen := st.gen + 1
		*st = sideContextState{conv: a.sel, gen: gen}
	}
	if a.keyOf(a.sel).Platform != "local" || a.run.src == nil {
		return st
	}
	now := c.Now()
	if !st.loading && !now.Before(st.due) {
		a.sideContextFetch(st, now)
	}
	if st.due.After(now) {
		c.After(st.due.Sub(now))
	}
	return st
}

// usageRequest 是最近一轮里最后一次成功、有输入的请求；新的轮在前，请求按时间排。
func (a *app) usageRequest() (ctlproto.ConvRequest, bool) {
	st := &a.workbench.shell.sideContext
	if !st.has || st.conv != a.sel {
		return ctlproto.ConvRequest{}, false
	}
	for _, t := range st.report.Turns {
		for i := len(t.Requests) - 1; i >= 0; i-- {
			if r := t.Requests[i]; r.Failed == "" && r.Input > 0 {
				return r, true
			}
		}
	}
	return ctlproto.ConvRequest{}, false
}

// currentUsage 是那次请求的输入 ÷ 窗口。窗口先用请求自己记下的（context_json 里的上限，发请求时实际用的，读轮次时顺带取），
// 没记再用模型列表里报了来源的窗口；都没有就不画（规格第四节：不拿默认值凑）。
func (a *app) currentUsage() convUsage {
	r, ok := a.usageRequest()
	if !ok {
		return convUsage{}
	}
	w := r.Window
	if w <= 0 {
		w = a.modelWindow(r.Model)
	}
	if w <= 0 {
		return convUsage{}
	}
	return convUsage{used: r.Input, window: w, ok: true}
}

// modelWindow 是这个模型的上下文窗口；来源是“默认”（不知道，界面标“未知”）的不算，宁可不画也不画错的百分比。
func (a *app) modelWindow(id string) int64 {
	for _, m := range a.models.view.Models {
		if m.ID == id && m.ContextWindow > 0 && m.WindowSource != "default" {
			return int64(m.ContextWindow)
		}
	}
	return 0
}

// wbHeaderUsage 把用量填进标题栏：头像外圈的环，点头像打开右栏的“上下文”栏。
func (a *app) wbHeaderUsage(c *ui.Context, h *winshell.HeaderSpec) {
	k := a.keyOf(a.sel)
	if k.Platform != "local" {
		return
	}
	a.loadModelsOnce()
	a.ensureConvTurns(c)
	u := a.currentUsage()
	h.Usage, h.UsageOK, h.UsageHint = u.ratio(), u.ok, u.hint()
	h.OnAvatar = func() {
		w := &a.workbench.shell
		w.sideTab = 1
		w.st.Panes.SideOpen = true
	}
}

// sideConvSummary 是右栏“上下文”栏顶部的“这个对话”：上下文用量，加上从输入框搬来的模型、工作区、审批方式。
func (a *app) sideConvSummary(c *ui.Context, kit *winshell.Kit) {
	g, p := kit.G, kit.P
	k := a.keyOf(a.sel)
	tools := a.wbComposerTools(c, k)
	if a.demoMode { // 演示模式没有后台：模型药丸照演示的模型设置，角色取会话的观察者
		tools = nil
		if cv := a.convByID(a.sel); cv != nil && len(cv.Observers) > 0 {
			tools = a.wbDemoTools(c, cv.Observers[0])
		}
	}
	u := a.currentUsage()
	ui.Column(c.Key("conv-info")).Label("这个对话").FillWidth().Padding(g.SpaceSmall).Gap(g.SpaceSmall).Radius(g.RadiusRow).Background(p.Bubble).Children(func() {
		ui.Text(c.Key("conv-info-title"), "这个对话").FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).FontWeight(g.StrongWeight).Padding(0, g.ControlInset)
		ui.Column(c.Key("conv-usage")).FillWidth().Gap(g.SpaceTight).Padding(0, g.ControlInset).Children(func() {
			text := "上下文用量：还没有请求，或者模型没报窗口大小"
			if u.ok {
				text = u.hint()
			}
			ui.Text(c.Key("conv-usage-text"), text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
			if u.ok {
				ink := p.Accent
				if u.ratio() >= 0.8 {
					ink = p.Danger
				}
				track := ui.Row(c.Key("conv-usage-bar")).FillWidth().Height(g.SpaceTight).Radius(g.SpaceTight / 2).Background(p.Line).Clip()
				fill := track.AnimateTo("usage", min(u.ratio(), 1), winshell.RouteMotion)
				track.Children(func() { // 用了的一段和剩下的一段按比例分宽
					ui.Box(c.Key("conv-usage-fill")).Grow(fill).Basis(0).FillHeight().Radius(g.SpaceTight / 2).Background(ink)
					ui.Box(c.Key("conv-usage-rest")).Grow(1 - fill).Basis(0).FillHeight()
				})
			}
		})
		if tools != nil {
			ui.Row(c.Key("conv-tools")).FillWidth().Wrap().Gap(g.SpaceTight).Children(tools)
		}
	})
}
