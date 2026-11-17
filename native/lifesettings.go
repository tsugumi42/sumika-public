package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/remotepick"
	"sumika-native/internal/winshell"
)

// 插件设置页的“日历和天气”：所在地用搜索下拉选，选中存规范结果（显示名、城市、经纬度、时区），不手打。
// 候选来自 Open-Meteo 地理编码（language=zh，中文、英文、拼音都行）；写入和设置工具走同一个服务。

type lifePageState struct {
	writer  api.LocationSettings
	view    api.LocationView
	loaded  bool
	busy    bool
	entered bool
	note    string
	request uint64
	pick    remotepick.State
}

func (a *app) lifeContext() context.Context {
	if a.viewCtx != nil {
		return a.viewCtx
	}
	return a.base()
}

func (a *app) enterLife() {
	s := &a.lifeSettings
	if s.entered {
		return
	}
	s.entered = true
	if s.writer == nil && a.demoMode {
		s.writer = newDemoLocation()
	}
	if s.writer == nil {
		s.writer, _ = a.reader.(api.LocationSettings)
	}
	if s.writer == nil {
		s.note = "当前后台没有提供所在地设置"
		return
	}
	writer := s.writer
	s.pick.Post = a.post
	s.pick.Fetch = func(ctx context.Context, q string) ([]remotepick.Candidate, error) {
		places, err := writer.SearchPlaces(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]remotepick.Candidate, 0, len(places))
		for _, p := range places {
			out = append(out, remotepick.Candidate{ID: p.ID, Label: p.Label, Detail: p.Detail, Data: p})
		}
		return out, nil
	}
	a.lifeLoad()
}

func (s *lifePageState) leave() {
	s.entered = false
	s.pick.Reset()
}

func (a *app) lifeLoad() {
	s := &a.lifeSettings
	s.request++
	request, generation, writer, ctx := s.request, a.srcGen, s.writer, a.lifeContext()
	go func() {
		v, err := writer.Location(ctx)
		a.post(func() {
			if a.srcGen != generation || a.lifeSettings.request != request {
				return
			}
			s := &a.lifeSettings
			if err != nil {
				s.note = err.Error()
				return
			}
			s.view, s.loaded, s.note = v, true, ""
		})
	}()
}

// lifeSave 写入所在地；loc 为 nil 是清空
func (a *app) lifeSave(loc *config.Location) {
	s := &a.lifeSettings
	if s.busy || s.writer == nil {
		return
	}
	s.busy = true
	s.request++
	request, generation, writer, base, ctx := s.request, a.srcGen, s.writer, s.view.Version, a.lifeContext()
	go func() {
		v, err := writer.SetLocation(ctx, loc, base)
		a.post(func() {
			if a.srcGen != generation || a.lifeSettings.request != request {
				return
			}
			s := &a.lifeSettings
			s.busy = false
			if err == nil {
				s.view, s.loaded, s.note = v, true, ""
				return
			}
			if errors.Is(err, config.ErrConfigVersion) {
				s.view, s.loaded = v, true
				s.note = "设置在别处改过，已重新读取，请再选一次"
				return
			}
			s.note = err.Error()
		})
	}()
}

func (a *app) pageLifeSettings(c *ui.Context) {
	a.enterLife()
	s := &a.lifeSettings
	ui.Column(c.Key("life-settings")).FillWidth().MinWidth(0).Gap(0).Children(func() {
		a.settingsHeading(c, "日历和天气")
		shown := "没有设置"
		if s.view.Location.Set() {
			shown = s.view.Location.Name
		}
		if k := a.settingsKit(); k != nil && s.writer != nil {
			k.Row(c, "life-location", "所在地", "天气默认查这里，群里只说到城市", func() {
				value := ""
				if s.view.Location.Set() {
					value = shown
				}
				k.RemotePicker(c, winshell.RemotePicker{Key: "life-place", State: &s.pick, Value: value, Empty: "没有设置", Placeholder: "搜索城市或地区", Busy: s.busy,
					OnPick: func(p remotepick.Candidate) {
						if place, ok := p.Data.(api.Place); ok {
							loc := place.Location
							a.lifeSave(&loc)
						}
					},
					OnClear: func() { a.lifeSave(nil) }})
			})
		} else {
			a.settingsRow(c, "所在地", settings.ButtonOptions{Value: shown, Disabled: true, MutedValue: true})
			if s.writer != nil {
				a.modelNote(c, "在新界面里搜索选择", false)
			}
		}
		a.modelNote(c, s.note, true)
	})
}

// 演示用的所在地设置：只保留在当前窗口
type demoLocation struct {
	mu  sync.Mutex
	loc config.Location
	ver int
}

func newDemoLocation() *demoLocation { return &demoLocation{} }

func demoPlaces() []api.Place {
	lat := func(v float64) *float64 { return &v }
	return []api.Place{
		{ID: "1", Label: "海淀，北京市，中国", Detail: "群里说到：北京市", Location: config.Location{Name: "海淀，北京市，中国", CityName: "北京市", Region: "北京市", Country: "中国", Latitude: lat(39.99), Longitude: lat(116.29), CityLatitude: lat(39.91), CityLongitude: lat(116.4), Timezone: "Asia/Shanghai"}},
		{ID: "2", Label: "海淀，某省，某国", Detail: "只知道到省，群里当不知道", Location: config.Location{Name: "海淀，某省，某国", Region: "某省", Country: "某国", Latitude: lat(10), Longitude: lat(20), Timezone: "UTC"}},
		{ID: "3", Label: "上海市，中国", Detail: "群里说到：上海市", Location: config.Location{Name: "上海市，中国", CityName: "上海市", Region: "上海市", Country: "中国", Latitude: lat(31.23), Longitude: lat(121.47), CityLatitude: lat(31.23), CityLongitude: lat(121.47), Timezone: "Asia/Shanghai"}},
	}
}

func (d *demoLocation) SearchPlaces(ctx context.Context, q string) ([]api.Place, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var out []api.Place
	for _, p := range demoPlaces() {
		if strings.Contains(strings.ToLower(p.Label), q) || (q == "haidian" && strings.HasPrefix(p.Label, "海淀")) || (q == "shanghai" && strings.HasPrefix(p.Label, "上海")) {
			out = append(out, p)
		}
	}
	return slices.Clone(out), nil
}

func (d *demoLocation) view() api.LocationView {
	return api.LocationView{Location: d.loc, Version: "v" + string(rune('0'+d.ver%10))}
}

func (d *demoLocation) Location(ctx context.Context) (api.LocationView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.view(), ctx.Err()
}

func (d *demoLocation) SetLocation(ctx context.Context, loc *config.Location, base string) (api.LocationView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if base != d.view().Version {
		return d.view(), config.ErrConfigVersion
	}
	d.loc = config.Location{}
	if loc != nil {
		d.loc = *loc
	}
	d.ver++
	return d.view(), ctx.Err()
}
