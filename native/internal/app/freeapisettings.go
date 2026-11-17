package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
	"sumika-native/internal/lifetools"
)

var _ api.FreeAPISettingsWriter = (*DesktopReader)(nil)

func (r *DesktopReader) FreeAPI(ctx context.Context) (api.FreeAPIView, error) {
	ctx, done, e := r.enterModels(ctx)
	if e != nil {
		return api.FreeAPIView{}, e
	}
	defer done()
	return r.desk.app.freeAPIView("")
}

func (r *DesktopReader) FreeAPIAct(ctx context.Context, act api.FreeAPIAction) (api.FreeAPIView, error) {
	ctx, done, e := r.enterModels(ctx)
	if e != nil {
		return api.FreeAPIView{}, e
	}
	defer done()
	return r.desk.app.freeAPIAct(ctx, act)
}

// 工具名到配置键、页面上的名字
var freeAPITools = []struct{ tool, key, name string }{
	{lifetools.ToolWeather, "weather", "天气"},
	{lifetools.ToolExchangeRate, "exchangeRate", "汇率"},
	{lifetools.ToolCalendar, "calendar", "日历"},
}

func (a *App) freeAPIView(note string) (api.FreeAPIView, error) {
	eng := a.FreeAPI()
	if eng == nil {
		return api.FreeAPIView{}, errors.New("免费接口还没装配")
	}
	c, e := config.LoadIn(a.root, a.opt.DataDir)
	if e != nil {
		return api.FreeAPIView{}, e
	}
	v := api.FreeAPIView{Categories: eng.Snapshot(), Events: eng.Events(), ProbeEnabled: c.FreeAPI.Probe.Enabled, ProbeHours: c.FreeAPI.Probe.IntervalHours, Version: c.Version, Note: note}
	for _, t := range freeAPITools {
		l := c.FreeAPI.Limits.Weather
		switch t.key {
		case "exchangeRate":
			l = c.FreeAPI.Limits.ExchangeRate
		case "calendar":
			l = c.FreeAPI.Limits.Calendar
		}
		v.Limits = append(v.Limits, api.FreeAPILimitView{Tool: t.key, Name: t.name, PerActorDaily: l.PerActorDaily, PerConversationHourly: l.PerConversationHourly, UsedToday: eng.ToolUsedToday(t.tool)})
	}
	return v, nil
}

func (a *App) freeAPIAct(ctx context.Context, act api.FreeAPIAction) (api.FreeAPIView, error) {
	eng := a.FreeAPI()
	if eng == nil {
		return api.FreeAPIView{}, errors.New("免费接口还没装配")
	}
	note := ""
	var err error
	switch act.Kind {
	case api.FreeAPIPin:
		err = eng.SetPinned(act.ID, act.Bool)
	case api.FreeAPIOff:
		err = eng.SetOff(act.ID, act.Bool)
	case api.FreeAPIClearBackoff:
		err = eng.ClearBackoff(act.ID)
	case api.FreeAPIProbe:
		note = probeNote(eng.ProbeNow(ctx, act.ID))
	case api.FreeAPIProbeOn, api.FreeAPIProbeHours, api.FreeAPILimit:
		var ch config.SettingChange
		ch, err = freeAPIChange(act)
		if err != nil {
			break
		}
		before, e := config.LoadIn(a.root, a.opt.DataDir)
		if e != nil {
			return api.FreeAPIView{}, e
		}
		base := act.ConfigBase
		if base == "" {
			base = before.Version
		}
		_, err = config.SaveSetting(ctx, before.Source, filepath.Join(a.paths.Root, "config", "history"), base, ch, a.now())
	default:
		err = errors.New("不认识的操作")
	}
	v, e := a.freeAPIView(note)
	if e != nil {
		return v, e
	}
	return v, err
}

// freeAPIChange 把页面上的改动换成配置键。键是固定的几个，不接受页面拼的名字
func freeAPIChange(act api.FreeAPIAction) (config.SettingChange, error) {
	switch act.Kind {
	case api.FreeAPIProbeOn:
		return config.SettingChange{Key: "freeApi.probe.enabled", Value: act.Bool}, nil
	case api.FreeAPIProbeHours:
		return config.SettingChange{Key: "freeApi.probe.intervalHours", Value: jsonInt(act.Int)}, nil
	}
	for _, t := range freeAPITools {
		if t.key != act.Tool {
			continue
		}
		field := map[string]string{"actor": "perActorDaily", "conversation": "perConversationHourly"}[act.Field]
		if field == "" {
			break
		}
		return config.SettingChange{Key: "freeApi.limits." + t.key + "." + field, Value: jsonInt(act.Int)}, nil
	}
	return config.SettingChange{}, errors.New("不认识的设置")
}

func probeNote(rs []freeapi.ProbeResult) string {
	var ok, bad, skip int
	var first string
	for _, r := range rs {
		switch {
		case r.Skipped != "":
			skip++
			if first == "" {
				first = r.Skipped
			}
		case r.Kind == freeapi.KOK:
			ok++
		default:
			bad++
		}
	}
	if len(rs) == 1 && skip == 1 {
		return first
	}
	parts := []string{fmt.Sprintf("检测完成：%d 通", ok)}
	if bad > 0 {
		parts = append(parts, fmt.Sprintf("%d 不通", bad))
	}
	if skip > 0 {
		parts = append(parts, fmt.Sprintf("%d 跳过", skip))
	}
	return strings.Join(parts, "，")
}

// jsonInt：配置写入要 json.Number 形状的整数
func jsonInt(n int) json.Number { return json.Number(strconv.Itoa(n)) }
