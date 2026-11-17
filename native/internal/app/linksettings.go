package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
)

type LinkRuntime struct {
	Values  config.LinkReaderConfig
	Version string
}

func (a *App) initLinkRuntime() {
	a.linkRuntime.Store(&LinkRuntime{Values: a.cfg.LinkReader.Clone(), Version: a.cfg.Version})
}
func (a *App) currentLinks() *LinkRuntime { return a.linkRuntime.Load() }

var _ api.LinkSettingsWriter = (*DesktopReader)(nil)

func (r *DesktopReader) LinkSettings(ctx context.Context) (api.LinkSettingsView, error) {
	ctx, done, e := r.enterModels(ctx)
	if e != nil {
		return api.LinkSettingsView{}, e
	}
	defer done()
	a := r.desk.app
	a.linkMu.Lock()
	defer a.linkMu.Unlock()
	return a.linkSettingsView(ctx)
}
func (r *DesktopReader) SetLinkSettings(ctx context.Context, ch api.LinkSettingsChange, base string) (api.LinkSettingsView, error) {
	ctx, done, e := r.enterModels(ctx)
	if e != nil {
		return api.LinkSettingsView{}, e
	}
	defer done()
	return r.desk.app.setLinkSettings(ctx, ch, base)
}

// setLinkSettings 是链接设置的写入本体：设置页和设置工具走同一个，版本检查、备份、运行发布、撤权刷新都在这里
func (a *App) setLinkSettings(ctx context.Context, ch api.LinkSettingsChange, base string) (api.LinkSettingsView, error) {
	a.linkMu.Lock()
	defer a.linkMu.Unlock()
	view, e := a.linkSettingsView(ctx)
	if e != nil {
		return view, e
	}
	allowed := func(id string) bool {
		for _, c := range view.KnownConversations {
			if c.ID == id && !c.Unavailable {
				return true
			}
		}
		return false
	}
	if ch.Key == config.LinkList {
		for _, id := range ch.Conversations {
			if !allowed(id) && !slices.Contains(view.Configured.Expand.Conversations, id) {
				return view, errors.New("不能新增本机目录之外的会话")
			}
		}
	}
	before, e := config.LoadIn(a.root, a.opt.DataDir)
	if e != nil {
		return view, e
	}
	candidate, e := config.SaveLink(ctx, before.Source, filepath.Join(a.paths.Root, "config", "history"), base, ch, a.now())
	if e != nil {
		if errors.Is(e, config.ErrConfigVersion) {
			v, readErr := a.linkSettingsView(ctx)
			return v, errors.Join(e, readErr)
		}
		return view, e
	}
	a.linkRuntime.Store(&LinkRuntime{Values: candidate.LinkReader.Clone(), Version: candidate.Version})
	if a.links != nil {
		a.links.Refresh()
	}
	view.Configured = api.LinkValuesOf(candidate.LinkReader)
	view.Running = api.LinkValuesOf(candidate.LinkReader)
	view.Version, view.RunningVersion = candidate.Version, candidate.Version
	view.KnownConversations = slices.DeleteFunc(view.KnownConversations, func(k api.KnownLinkConversation) bool {
		return k.Unavailable && !slices.Contains(candidate.LinkReader.Expand.Conversations, k.ID)
	})
	return view, nil
}
func (a *App) linkSettingsView(ctx context.Context) (api.LinkSettingsView, error) {
	c, e := config.LoadIn(a.root, a.opt.DataDir)
	if e != nil {
		return api.LinkSettingsView{}, e
	}
	current := a.currentLinks()
	v := api.LinkSettingsView{Configured: api.LinkValuesOf(c.LinkReader), Running: api.LinkValuesOf(current.Values), RunningVersion: current.Version, RunningKnown: a.opt.Mode == Receive}
	v.Version = c.Version
	if a.opt.Mode == Receive {
		v.RunningReason = "已发布，下一条消息使用；撤销许可立即停止未发送部分"
	} else {
		v.RunningReason = "未接收，开始接收后使用"
		v.DisabledReason = v.RunningReason
	}
	known, e := a.set.KnownQQConversations(ctx)
	if e != nil {
		return v, e
	}
	seen := map[string]bool{}
	add := func(id, name string, unavailable bool) {
		if !config.ValidLinkConversation(id) || seen[id] {
			return
		}
		conv, _ := types.ParseConversation(id)
		account := "当前接收账号"
		if conv.Scene == types.ScenePrivate {
			account = config.MaskID(conv.Self)
		}
		if name == "" {
			name = config.MaskID(conv.ID)
		}
		v.KnownConversations = append(v.KnownConversations, api.KnownLinkConversation{ID: id, Name: name, Kind: conv.Scene, Account: account, Target: config.MaskID(conv.ID), Unavailable: unavailable})
		seen[id] = true
	}
	for _, k := range known {
		add(k.Conversation, k.Title, false)
	}
	for _, id := range c.LinkReader.Expand.Conversations {
		add(id, "当前目录不可用", true)
	}
	if a.links != nil {
		for _, active := range a.links.Active() {
			v.Active = append(v.Active, api.LinkUse{JobID: active.JobID, Conversation: active.Conversation, Version: active.Version, Stage: active.Stage})
		}
	}
	if a.hub != nil {
		v.Plugins = a.hub.RegisteredPlugins()
	}
	return v, nil
}
