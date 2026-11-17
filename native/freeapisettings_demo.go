package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
)

// demoFreeAPI 是演示模式和界面测试用的“免费接口”后台：数据是编的，操作只改自己，不联网。
type demoFreeAPI struct {
	mu     sync.Mutex
	view   api.FreeAPIView
	probes int
}

func newDemoFreeAPI() *demoFreeAPI {
	now := time.Now()
	d := &demoFreeAPI{}
	d.view = api.FreeAPIView{ProbeEnabled: true, ProbeHours: 12, Version: "demo-1",
		Limits: []api.FreeAPILimitView{
			{Tool: "weather", Name: "天气", PerActorDaily: 20, PerConversationHourly: 15, UsedToday: 7},
			{Tool: "exchangeRate", Name: "汇率", PerActorDaily: 20, PerConversationHourly: 15, UsedToday: 2},
			{Tool: "calendar", Name: "日历"},
		}}
	for _, c := range freeapi.DefaultCatalog().Categories {
		cv := freeapi.CategoryView{ID: c.ID, Name: c.Name}
		for _, p := range freeapi.DefaultCatalog().Providers {
			if p.Category != c.ID {
				continue
			}
			day, _ := p.DefaultCaps()
			cv.Providers = append(cv.Providers, freeapi.ProviderView{ID: p.ID, Name: p.Name, Category: p.Category, Grey: p.Grey, Network: p.Network, Regions: p.Regions,
				State: freeapi.StateOK, LastCheck: now.Add(-3 * time.Hour), LastKind: freeapi.KOK, Success24h: 1, MedianMS: 480, UsedToday: 3, DailyCap: day})
		}
		d.view.Categories = append(d.view.Categories, cv)
	}
	// 编几种状态：退避、需处理、手动关、置顶
	d.set("met-norway", func(p *freeapi.ProviderView) {
		p.State, p.Until, p.Success24h, p.LastKind = freeapi.StateBackoff, now.Add(14*time.Minute), 0.6, freeapi.K5xx
	})
	d.set("holiday-timor", func(p *freeapi.ProviderView) {
		p.State, p.Reason, p.LastKind = freeapi.StateAttention, freeapi.KSchema, freeapi.KSchema
	})
	d.set("fx-frankfurter", func(p *freeapi.ProviderView) { p.State, p.Off = freeapi.StateOff, true })
	d.set("tencent-weather", func(p *freeapi.ProviderView) { p.Pinned = true; p.UsedToday = 37 })
	d.view.Events = []freeapi.Event{{T: now.Add(-20 * time.Minute).UnixMilli(), ID: "met-norway", Text: "连续出错，退避一会儿"}}
	return d
}

func (d *demoFreeAPI) set(id string, f func(*freeapi.ProviderView)) {
	for i := range d.view.Categories {
		for j := range d.view.Categories[i].Providers {
			if d.view.Categories[i].Providers[j].ID == id {
				f(&d.view.Categories[i].Providers[j])
			}
		}
	}
}

func (d *demoFreeAPI) snapshot(note string) api.FreeAPIView {
	v := d.view
	v.Categories = append([]freeapi.CategoryView(nil), v.Categories...)
	for i := range v.Categories {
		v.Categories[i].Providers = append([]freeapi.ProviderView(nil), v.Categories[i].Providers...)
	}
	v.Limits = append([]api.FreeAPILimitView(nil), v.Limits...)
	v.Note = note
	return v
}

func (d *demoFreeAPI) FreeAPI(ctx context.Context) (api.FreeAPIView, error) {
	if e := ctx.Err(); e != nil {
		return api.FreeAPIView{}, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot(""), nil
}

func (d *demoFreeAPI) FreeAPIAct(ctx context.Context, a api.FreeAPIAction) (api.FreeAPIView, error) {
	if e := ctx.Err(); e != nil {
		return api.FreeAPIView{}, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if a.ConfigBase != "" && a.ConfigBase != d.view.Version {
		return d.snapshot(""), config.ErrConfigVersion
	}
	note := ""
	switch a.Kind {
	case api.FreeAPIPin:
		d.set(a.ID, func(p *freeapi.ProviderView) { p.Pinned = a.Bool })
	case api.FreeAPIOff:
		d.set(a.ID, func(p *freeapi.ProviderView) {
			p.Off = a.Bool
			if a.Bool {
				p.State = freeapi.StateOff
			} else {
				p.State = freeapi.StateOK
			}
		})
	case api.FreeAPIClearBackoff:
		d.set(a.ID, func(p *freeapi.ProviderView) { p.State, p.Until = freeapi.StateOK, time.Time{} })
	case api.FreeAPIProbe:
		d.probes++
		note = "检测完成：演示数据，没有联网"
	case api.FreeAPIProbeOn:
		d.view.ProbeEnabled = a.Bool
	case api.FreeAPIProbeHours:
		if a.Int < config.FreeAPIMinProbeHours || a.Int > config.FreeAPIMaxProbeHours {
			return d.snapshot(""), errors.New("设置值无效或配置无法安全修改")
		}
		d.view.ProbeHours = a.Int
	case api.FreeAPILimit:
		for i := range d.view.Limits {
			if d.view.Limits[i].Tool == a.Tool {
				if a.Field == "actor" {
					d.view.Limits[i].PerActorDaily = a.Int
				} else {
					d.view.Limits[i].PerConversationHourly = a.Int
				}
			}
		}
	default:
		return d.snapshot(""), errors.New("不认识的操作")
	}
	return d.snapshot(note), nil
}
