package app

import (
	"context"
	"path/filepath"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/modelmeta"
)

// 模型的窗口和单次输出怎么定（ADR-334）：用户覆盖 > 服务商或网关返回 > models.dev 本地缓存 > 保守默认。
// 取回模型列表、改覆盖、models.dev 刷新以后都重算一遍（Resolve 可以对同一个列表反复算）。

// initModelMeta 在取模型列表之前建 models.dev 的缓存：读本地缓存（同步，一个文件），联网刷新另起后台，取不到不影响启动
func (a *App) initModelMeta() {
	c := a.cfg.ModelMeta.ModelsDev
	proxy := c.Proxy
	if proxy == "" {
		proxy = a.cfg.LinkReader.Proxy
	}
	s := &modelmeta.Store{Path: filepath.Join(a.paths.CacheDir(), "models-dev.json"), URL: c.URL, Proxy: proxy, Enabled: c.Enabled,
		TTL: time.Duration(c.RefreshHours * float64(time.Hour)), Logf: a.logfFor(applog.Llm), Now: a.now}
	if err := s.Load(); err != nil {
		a.logf("模型资料缓存：%v", err)
	}
	s.OnChange = a.reresolveModels
	a.modelDev = s
}

// resolveModels 按现在的覆盖和缓存把一张模型列表的窗口、单次输出定下来
func (a *App) resolveModels(models []llm.Model, overrides map[string]config.ModelOverride) []llm.Model {
	ov := make(map[string]modelmeta.Override, len(overrides))
	for id, o := range overrides {
		ov[id] = modelmeta.Override{ContextWindow: o.ContextWindow, MaxOutputTokens: o.MaxOutputTokens}
	}
	var cat *modelmeta.Catalog
	if a.modelDev != nil {
		cat = a.modelDev.Catalog()
	}
	return modelmeta.ResolveAll(models, ov, cat)
}

// reresolveModels 在 models.dev 缓存换了新内容以后重算已发布的模型列表（下一轮用）
func (a *App) reresolveModels() {
	if a.closing.Load() {
		return
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	old := a.currentModels()
	if old == nil {
		return
	}
	next := *old
	next.Models = a.resolveModels(old.Models, old.Values.Overrides)
	a.modelRuntime.Store(&next)
}

// startModelMetaRefresh 后台按间隔检查要不要联网刷新：开着的话启动后先看一次，之后每小时看一次是不是到期。
// 用注入的假模型客户端时（测试）不联网
func (a *App) startModelMetaRefresh(ctx context.Context) {
	if a.modelDev == nil || a.opt.LLM != nil || !a.cfg.ModelMeta.ModelsDev.Enabled {
		return
	}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if a.modelDev.Due() {
				rctx, cancel := context.WithTimeout(ctx, time.Minute)
				if err := a.modelDev.Refresh(rctx, false); err != nil {
					a.logf("模型资料库（models.dev）没取到，用缓存或默认值：%v", err)
				}
				cancel()
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// modelsDevState 给模型页看的资料库状态
func (a *App) modelsDevState() api.ModelsDevState {
	if a.modelDev == nil {
		return api.ModelsDevState{}
	}
	s := a.modelDev.State()
	return api.ModelsDevState{Enabled: s.Enabled, Entries: s.Entries, FetchedAt: s.FetchedAt, FromCache: s.FromCache, Error: s.Error}
}
