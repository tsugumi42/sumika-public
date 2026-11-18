package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
)

// 设置 → 插件 → “免费接口”分组（规格-免费接口 第七节）：每类每家的状态、成功率、今日用量，置顶、关闭、立即检测，
// 探测开关和间隔，各工具每人每天、每群每小时的次数。状态放在 linkPageState 里，离开插件页时一起收掉。

type freeAPIPageState struct {
	writer                          api.FreeAPISettingsWriter
	view                            api.FreeAPIView
	entered, loaded, busy, expanded bool
	openID                          string // 展开的提供方，同时只展开一个
	note                            string
	load                            loadstate.Loader
	request                         uint64
	editBox                         overlay.BoxState
	edit                            api.FreeAPIAction // 正在编辑的数：Kind、Tool、Field
	editTitle, editError            string
	number                          settings.NumberState
}

func (s *freeAPIPageState) leave() {
	s.entered = false
	s.load.Cancel()
	s.editBox.Open = false
}

func (a *app) enterFreeAPI() {
	s := &a.linkSettings.free
	if s.entered {
		return
	}
	s.entered = true
	if s.writer == nil && a.demoMode {
		s.writer = newDemoFreeAPI()
	}
	if s.writer == nil {
		s.writer, _ = a.reader.(api.FreeAPISettingsWriter)
	}
	if s.writer == nil {
		s.note = "当前后台没有提供免费接口设置"
		return
	}
	a.freeJob(nil)
}

// freeJob 在后台读或改；act 为 nil 是读。结果回到界面线程
func (a *app) freeJob(act *api.FreeAPIAction) {
	s := &a.linkSettings.free
	if s.busy || s.writer == nil {
		return
	}
	s.busy = true
	s.load.Begin()
	s.request++
	request, generation, writer, version := s.request, a.srcGen, s.writer, s.view.Version
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var v api.FreeAPIView
		var err error
		if act == nil {
			v, err = writer.FreeAPI(ctx)
		} else {
			x := *act
			x.ConfigBase = version
			v, err = writer.FreeAPIAct(ctx, x)
		}
		a.post(func() {
			if a.srcGen != generation || a.linkSettings.free.request != request {
				return
			}
			s := &a.linkSettings.free
			s.busy = false
			if err == nil || errors.Is(err, config.ErrConfigVersion) || len(v.Categories) > 0 {
				s.view, s.loaded = v, true
			}
			if s.loaded {
				s.load.Succeed()
			} else {
				s.load.Fail()
			}
			switch {
			case errors.Is(err, config.ErrConfigVersion):
				s.note = "配置刚被别处改过，已刷新，请再试一次"
			case err != nil:
				s.note = err.Error()
			default:
				s.note = v.Note
			}
		})
	}()
}

func freeStateText(p freeapi.ProviderView) string {
	at := func() string { return p.Until.Local().Format("15:04") }
	switch p.State {
	case freeapi.StateBackoff:
		return "退避到 " + at()
	case freeapi.StatePaused:
		return "暂停到 " + at()
	case freeapi.StateAttention:
		if p.Reason == freeapi.KSchema {
			return "需处理（格式变了）"
		}
		return "需处理（被拒绝）"
	case freeapi.StateOff:
		return "已关闭"
	case freeapi.StateQuota:
		return "今天上限用完"
	}
	if p.Pinned {
		return "正常 · 置顶"
	}
	return "正常"
}

func freeRateText(p freeapi.ProviderView) string {
	if p.Success24h < 0 {
		return "未测"
	}
	return fmt.Sprintf("%d%%", int(p.Success24h*100+0.5))
}

func (a *app) freeEdit(c *ui.Context, title, value string, act api.FreeAPIAction) {
	s := &a.linkSettings.free
	a.settingsRow(c.Key(title), title, settings.ButtonOptions{Value: value, Arrow: true, Disabled: s.busy, OnActivate: func() {
		s.edit, s.editTitle, s.editError = act, title, ""
		s.number.Text = strconv.Itoa(act.Int)
		s.editBox.Open = true
	}})
}

// pageFreeAPI 画“免费接口”一组；放在内置插件列表里
func (a *app) pageFreeAPI(c *ui.Context) {
	a.enterFreeAPI()
	s := &a.linkSettings.free
	lv := s.load.View(c.Now())
	loadui.Wake(c, lv)
	a.settingsRow(c, "免费接口（天气、节假日、汇率）", settings.ButtonOptions{Value: func() string {
		if s.expanded {
			return "收起"
		}
		return "设置"
	}(), Arrow: true, OnActivate: func() { s.expanded = !s.expanded }})
	if s.expanded {
		if !s.loaded {
			msg := "　"
			if loadui.Visible(lv) {
				msg = "正在读取免费接口…"
			}
			if s.note != "" {
				msg = s.note
			}
			a.modelNote(c, msg, s.note != "")
		} else {
			a.freeAPIBody(c, lv)
		}
	}
	a.freeAPIDialog(c)
}

func (a *app) freeAPIBody(c *ui.Context, lv loadstate.View) {
	s := &a.linkSettings.free
	v := s.view
	a.settingsHeading(c, "检测")
	a.settingsToggle(c, "free-probe-on", "定期检测接口", v.ProbeEnabled, s.busy, func(b bool) { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIProbeOn, Bool: b}) })
	a.freeEdit(c, "检测间隔（小时，1～48）", strconv.Itoa(v.ProbeHours), api.FreeAPIAction{Kind: api.FreeAPIProbeHours, Int: v.ProbeHours})
	a.settingsRow(c, "立即检测全部", settings.ButtonOptions{Disabled: s.busy, OnActivate: func() { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIProbe}) }})
	a.settingsBreak(c, "检测只带固定参数，不带你的地点；最近有成功调用的接口会跳过。")
	a.settingsHeading(c, "次数（0 不限，主人不计）")
	for _, l := range v.Limits {
		a.freeEdit(c, l.Name+"每人每天", fmt.Sprintf("%d 次 · 今天共 %d 次", l.PerActorDaily, l.UsedToday), api.FreeAPIAction{Kind: api.FreeAPILimit, Tool: l.Tool, Field: "actor", Int: l.PerActorDaily})
		a.freeEdit(c, l.Name+"每群每小时", fmt.Sprintf("%d 次", l.PerConversationHourly), api.FreeAPIAction{Kind: api.FreeAPILimit, Tool: l.Tool, Field: "conversation", Int: l.PerConversationHourly})
	}
	for _, cat := range v.Categories {
		a.settingsHeading(c, cat.Name)
		for _, p := range cat.Providers {
			a.freeProvider(c, p)
		}
	}
	if n := len(v.Events); n > 0 {
		a.settingsHeading(c, "最近事件")
		for i := n - 1; i >= 0 && i >= n-5; i-- {
			e := v.Events[i]
			a.modelNote(c, time.UnixMilli(e.T).Local().Format("01-02 15:04")+" "+e.ID+" "+e.Text, false)
		}
	}
	a.modelNote(c, s.note, true)
	if s.busy && loadui.Visible(lv) {
		a.modelNote(c, "正在保存或读取…", false)
	}
}

func (a *app) freeProvider(c *ui.Context, p freeapi.ProviderView) {
	s := &a.linkSettings.free
	name := p.Name
	if p.Grey {
		name += "（非公开接口）"
	}
	val := fmt.Sprintf("%s · %s · %d/%d", freeStateText(p), freeRateText(p), p.UsedToday, p.DailyCap)
	a.settingsRow(c.Key("free-"+p.ID), name, settings.ButtonOptions{Value: val, Arrow: true, OnActivate: func() {
		if s.openID == p.ID {
			s.openID = ""
		} else {
			s.openID = p.ID
		}
	}})
	if s.openID != p.ID {
		return
	}
	a.settingsToggle(c, "free-pin", "置顶", p.Pinned, s.busy, func(b bool) { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIPin, ID: p.ID, Bool: b}) })
	a.settingsToggle(c, "free-off", "关闭", p.Off, s.busy, func(b bool) { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIOff, ID: p.ID, Bool: b}) })
	a.settingsRow(c, "立即检测", settings.ButtonOptions{Disabled: s.busy, OnActivate: func() { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIProbe, ID: p.ID}) }})
	if p.State == freeapi.StateBackoff || p.State == freeapi.StatePaused {
		a.settingsRow(c, "清除退避", settings.ButtonOptions{Disabled: s.busy, OnActivate: func() { a.freeJob(&api.FreeAPIAction{Kind: api.FreeAPIClearBackoff, ID: p.ID}) }})
	}
	last := "还没检测过"
	if !p.LastCheck.IsZero() {
		last = "最近一次 " + p.LastCheck.Local().Format("01-02 15:04") + "（" + freeKindText(p.LastKind) + "）"
		if p.MedianMS > 0 {
			last += fmt.Sprintf("，中位 %d 毫秒", p.MedianMS)
		}
	}
	route := "走代理（有的话）"
	if p.Network == freeapi.NetDirect {
		route = "直连"
	}
	a.modelNote(c, last+" · "+route, false)
}

func freeKindText(k freeapi.Kind) string {
	switch k {
	case freeapi.KOK:
		return "通"
	case freeapi.KNotFound:
		return "答了，查无结果"
	case freeapi.KTimeout:
		return "超时"
	case freeapi.K5xx:
		return "对方出错"
	case freeapi.KRate:
		return "太频繁"
	case freeapi.KAuth:
		return "被拒绝"
	case freeapi.KBlocked:
		return "被拦"
	case freeapi.KSchema:
		return "格式不对"
	}
	return "没连上"
}

func (a *app) freeAPIDialog(c *ui.Context) {
	s := &a.linkSettings.free
	save := func() {
		n, e := strconv.Atoi(s.number.Text)
		if e != nil || n < 0 || (s.edit.Kind == api.FreeAPIProbeHours && (n < config.FreeAPIMinProbeHours || n > config.FreeAPIMaxProbeHours)) {
			s.editError = "请输入范围内的整数"
			return
		}
		act := s.edit
		act.Int = n
		s.editBox.Open = false
		a.freeJob(&act)
	}
	a.settingsBox(c, "free-editor", &s.editBox, s.editTitle, overlay.BoxOptions{Actions: []overlay.BoxAction{{ID: "save", Label: "保存", Default: true, KeepOpen: true, Disabled: s.busy}, {ID: "cancel", Label: "取消"}}, OnAction: func(id string) {
		if id == "save" {
			save()
		}
	}}, func() {
		a.settingsNumber(c, "free-number-input", &s.number, "编辑免费接口数值", 100000, true, false, func(value string) { s.number.Text = value; save() })
		a.modelNote(c, s.editError, true)
	})
}
