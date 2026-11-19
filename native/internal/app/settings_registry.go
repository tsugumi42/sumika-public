package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/lineend"
	"sumika-native/internal/settingsreg"
)

var _ api.SettingsRegistry = (*DesktopReader)(nil)

func (r *DesktopReader) ReadSettingsRegistry(ctx context.Context) (api.SettingsView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.SettingsView{}, err
	}
	defer done()
	return r.readSettings(ctx)
}

func (r *DesktopReader) readSettings(ctx context.Context) (api.SettingsView, error) {
	if err := ctx.Err(); err != nil {
		return api.SettingsView{}, err
	}
	a := r.desk.app
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return api.SettingsView{}, err
	}
	st := desksettings.New(a.paths.Root)
	// 一次返回的值与版本来自同一桌面文件快照；外部并发修改则重读。
	_, before, err := st.Document()
	if err != nil {
		return api.SettingsView{}, err
	}
	items, err := settingsreg.Read(c, st, a.paths.CharactersDir())
	if err != nil {
		return api.SettingsView{}, err
	}
	_, after, err := st.Document()
	if err != nil {
		return api.SettingsView{}, err
	}
	for i := range items {
		if items[i].Key == "data.root" {
			items[i].Value = a.root
			items[i].Description = "当前进程的数据根目录，启动来源：" + a.rootLocation.Source + "；修改根文件后须重启。"
			if a.rootLocation.Source == "sumika.root" || a.rootLocation.Source == "命令行 -root" {
				items[i].Source = "桌面设置"
			}
		}
	}
	if before != after {
		return api.SettingsView{}, desksettings.ErrSettingsVersion
	}
	return api.SettingsView{Items: items, Version: api.SettingsVersion{Config: c.Version, Desktop: after}, EmbedMissingModel: a.embedMissingModel()}, nil
}

func (r *DesktopReader) WriteSetting(ctx context.Context, ch api.SettingChange, base api.SettingsVersion) (api.SettingsView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.SettingsView{}, err
	}
	defer done()
	entry, ok := registryEntry(ch.Key)
	if !ok || entry.ReadOnly {
		return api.SettingsView{}, errors.New("设置不存在或只读")
	}
	if !json.Valid(ch.Value) || bytes.Equal(bytes.TrimSpace(ch.Value), []byte("null")) {
		return api.SettingsView{}, errors.New("设置需要一个非空 JSON 值")
	}
	a := r.desk.app
	saved := false
	switch {
	case strings.HasPrefix(ch.Key, "desktop."):
		err = desksettings.New(a.paths.Root).SetSetting(ctx, strings.TrimPrefix(ch.Key, "desktop."), ch.Value, base.Desktop)
		saved = err == nil
	case modelSettingKey(ch.Key):
		var change config.ModelChange
		change, err = registryModelChange(ch)
		if err == nil {
			_, err = r.SetModelSettings(ctx, change, base.Config)
			var partial *api.SavedModelSettingsError
			saved = err == nil || errors.As(err, &partial)
		}
	case config.WritableSetting(ch.Key):
		err = a.writeConfigSetting(ctx, ch, base.Config)
		saved = err == nil
	default:
		var change config.LinkChange
		change, err = a.registryLinkChange(ch)
		if err == nil {
			_, err = a.setLinkSettings(ctx, change, base.Config)
		}
		saved = err == nil
	}
	if saved && ch.Key == "tools.approvalMode" {
		// 写进配置以后当场改运行中的默认档：不用重启接收；各会话自己切过的不受影响
		var mode string
		if json.Unmarshal(ch.Value, &mode) == nil {
			if e := a.approvalGate.SetMode(mode); e != nil {
				err = errors.Join(err, e)
			}
		}
	}
	if saved && ch.Key == "tools.lineEnding" {
		// 写进配置以后当场改：下一次写文件就按新值，审批预览也是
		var mode string
		if json.Unmarshal(ch.Value, &mode) == nil {
			lineend.SetDefault(mode)
		}
	}
	if saved && (ch.Key == "web.searchOrder" || ch.Key == "web.fetchOrder") {
		// 联网各家的先后写进配置以后当场改运行中的顺序：不用重启，下一次搜索或抓网页就按新顺序走
		a.applyWebOrder(ch.Key, ch.Value)
	}
	view, readErr := r.readSettings(ctx)
	if saved {
		view.Effect = entry.Effect
		if readErr != nil {
			readErr = &api.SavedSettingsError{Cause: readErr}
		}
	}
	return view, errors.Join(err, readErr)
}

// applyWebOrder 把已落盘的联网顺序交给运行中的联网服务；联网没开（没有 Order）就只有配置变了。
func (a *App) applyWebOrder(key string, raw json.RawMessage) {
	if a.ecosystem == nil || a.ecosystem.WebOrder == nil {
		return
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || config.ValidWebOrder(key, ids) != nil {
		return
	}
	if key == "web.searchOrder" {
		a.ecosystem.WebOrder.SetSearch(ids)
	} else {
		a.ecosystem.WebOrder.SetFetch(ids)
	}
}

// writeConfigSetting 是普通配置键的写入本体（有版本检查、备份）：设置页和设置工具走同一个
func (a *App) writeConfigSetting(ctx context.Context, ch api.SettingChange, base string) error {
	var value any
	dec := json.NewDecoder(bytes.NewReader(ch.Value))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return err
	}
	_, err = config.SaveSetting(ctx, c.Source, filepath.Join(a.paths.Root, "config", "history"), base, config.SettingChange{Key: ch.Key, Value: value}, a.now())
	if err == nil {
		a.notifyEnvChanged()
	}
	return err
}

func registryEntry(key string) (settingsreg.Entry, bool) { return settingsreg.Lookup(key) }

func modelSettingKey(key string) bool {
	switch config.ModelField(key) {
	case config.BackgroundModel, config.BackgroundThinking, config.ThinkingLocal, config.ThinkingPrivate, config.ThinkingGroup, config.ContextWindow, config.ContextReserve, config.ContextSoftRatio, config.ContextHardRatio, config.ContextKeepTokens, config.ContextAutoCompact:
		return true
	}
	return strings.HasPrefix(key, "characters.") && (strings.HasSuffix(key, ".model") || strings.HasSuffix(key, ".background"))
}

func registryModelChange(ch api.SettingChange) (config.ModelChange, error) {
	c := config.ModelChange{Field: config.ModelField(ch.Key)}
	if strings.HasPrefix(ch.Key, "characters.") {
		tail := strings.TrimPrefix(ch.Key, "characters.")
		at := strings.LastIndexByte(tail, '.')
		c.Character, c.Field = tail[:at], config.ModelField("character."+tail[at+1:])
	}
	var err error
	switch c.Field {
	case config.ContextWindow, config.ContextReserve, config.ContextKeepTokens:
		err = json.Unmarshal(ch.Value, &c.Int)
	case config.ContextSoftRatio, config.ContextHardRatio:
		err = json.Unmarshal(ch.Value, &c.Float)
	case config.ContextAutoCompact:
		err = json.Unmarshal(ch.Value, &c.Bool)
	default:
		err = json.Unmarshal(ch.Value, &c.String)
	}
	if err != nil {
		return c, errors.New("设置值类型不正确")
	}
	return c, nil
}

func (a *App) registryLinkChange(ch api.SettingChange) (config.LinkChange, error) {
	var c config.LinkChange
	var target any
	switch ch.Key {
	case "linkReader.context.enabled":
		c.Key, target = config.LinkContextEnabled, &c.Bool
	case "linkReader.expand.enabled":
		c.Key, target = config.LinkEnabled, &c.Bool
	case "linkReader.expand.maxLinks":
		c.Key, target = config.LinkMaxLinks, &c.Int
	case "linkReader.expand.imageMaxMB":
		c.Key, target = config.LinkImageMaxMB, &c.Int
	case "linkReader.expand.bareBiliID":
		c.Key, target = config.LinkBareBiliID, &c.Bool
	case "linkReader.expand.startEmoji":
		c.Key, target = config.LinkStartEmoji, &c.Text
	case "linkReader.expand.doneEmoji":
		c.Key, target = config.LinkDoneEmoji, &c.Text
	case "linkReader.expand.failEmoji":
		c.Key, target = config.LinkFailEmoji, &c.Text
	case "linkReader.expand.rotateOnReject":
		c.Key, target = config.LinkRotateOnReject, &c.Bool
	case "linkReader.expand.panoramaOriginals":
		c.Key, target = config.LinkPanoramaOriginals, &c.Bool
	case "linkReader.expand.privateStatus":
		c.Key, target = config.LinkPrivateStatus, &c.Text
	case "linkReader.expand.translate.enabled":
		c.Key, target = config.LinkTranslateEnabled, &c.Bool
	case "linkReader.expand.translate.target":
		c.Key, target = config.LinkTranslateTarget, &c.Text
	case "linkReader.expand.translate.timeoutSeconds":
		c.Key, target = config.LinkTranslateTimeout, &c.Int
	case "linkReader.expand.translate.retries":
		c.Key, target = config.LinkTranslateRetries, &c.Int
	case "linkReader.expand.translate.model":
		c.Key, target = config.LinkTranslateModel, &c.Text
	case "linkReader.expand.video.enabled":
		c.Key, target = config.LinkVideoEnabled, &c.Bool
	case "linkReader.expand.video.timeoutSeconds":
		c.Key, target = config.LinkVideoTimeout, &c.Int
	case "linkReader.expand.video.maxMB":
		c.Key, target = config.LinkVideoMaxMB, &c.Int
	case "linkReader.expand.mode", "linkReader.expand.conversations":
		before, err := config.LoadIn(a.root, a.opt.DataDir)
		if err != nil {
			return c, err
		}
		c.Key, c.Mode, c.Conversations = config.LinkList, before.LinkReader.Expand.Mode, before.LinkReader.Expand.Conversations
		if ch.Key == "linkReader.expand.mode" {
			target = &c.Mode
		} else {
			target = &c.Conversations
		}
	default:
		return c, errors.New("设置没有写入入口")
	}
	if err := json.Unmarshal(ch.Value, target); err != nil {
		return c, errors.New("设置值类型不正确")
	}
	return c, nil
}

var _ api.LocationSettings = (*DesktopReader)(nil)

// SearchPlaces 是设置页“所在地”的候选来源（和设置工具同一个：lifetools.SearchPlaces）
func (r *DesktopReader) SearchPlaces(ctx context.Context, query string) ([]api.Place, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	life := r.desk.app.life
	if life == nil {
		return nil, errors.New("日历和天气没有启用，搜不了地点")
	}
	places, err := life.SearchPlaces(ctx, query, 8)
	if err != nil {
		return nil, errors.New("没取到候选：网络不通或对方没回，稍后再试")
	}
	out := make([]api.Place, 0, len(places))
	for _, p := range places {
		detail := "只知道到省，群里当不知道"
		if city, ok := p.Location.City(); ok {
			detail = "群里说到：" + city.Name
		}
		out = append(out, api.Place{ID: p.ID, Label: p.Label, Detail: detail, Location: p.Location})
	}
	return out, nil
}

func (r *DesktopReader) Location(ctx context.Context) (api.LocationView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.LocationView{}, err
	}
	defer done()
	return r.desk.app.locationView(ctx)
}

func (a *App) locationView(ctx context.Context) (api.LocationView, error) {
	if err := ctx.Err(); err != nil {
		return api.LocationView{}, err
	}
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return api.LocationView{}, err
	}
	return api.LocationView{Location: c.Location, Version: c.Version}, nil
}

func (r *DesktopReader) SetLocation(ctx context.Context, loc *config.Location, base string) (api.LocationView, error) {
	ctx, done, err := r.enterModels(ctx)
	if err != nil {
		return api.LocationView{}, err
	}
	defer done()
	a := r.desk.app
	value := config.Location{}
	if loc != nil {
		value = *loc
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return api.LocationView{}, err
	}
	err = a.writeConfigSetting(ctx, api.SettingChange{Key: "location", Value: raw}, base)
	view, readErr := a.locationView(ctx)
	if err != nil {
		return view, errors.Join(err, readErr)
	}
	return view, readErr
}
