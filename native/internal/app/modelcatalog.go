package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/fileatomic"
	"sumika-native/internal/llm"
)

// 模型目录不当启动门槛（规格-模型配置 6.1）：列模型成功就把原始列表落盘；取不到时用上一份同来源的缓存照常启动，
// 后台按退避自己重试，来源回来以后不用重启。

// modelCatalogCache 是落盘的模型目录：当初连的来源、读到的时间、服务商给的模型项（解析前、不含用户覆盖）
type modelCatalogCache struct {
	Source string      `json:"source"`
	ReadAt time.Time   `json:"readAt"`
	Models []llm.Model `json:"models"`
}

// catalogRetryDelays 是模型目录取不到时后台重试前等的时长；用完之后一直用最后一个
var catalogRetryDelays = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

// CatalogRetry 是模型目录后台重试的节奏；零值就是规格里的 30 秒、1 分、2 分、5 分（之后一直 5 分）。只供测试替换
type CatalogRetry struct {
	// Delays 是每次重试前等多久，用完一直用最后一个
	Delays []time.Duration
	// Wait 替换等待；返回 false 表示 ctx 结束了、不再重试。测试用它推进，不真的等
	Wait func(ctx context.Context, d time.Duration) bool
}

// catalogSource 是目录缓存认的来源地址：客户端连的地址变了，旧缓存就不用（规格-模型配置 6.1 第 2 条）。
// 末尾斜杠不算变（同一个地址）
func (a *App) catalogSource() string {
	if a.llm == nil {
		return strings.TrimRight(a.cfg.MagpieBaseURL(), "/")
	}
	return strings.TrimRight(a.llm.Source().BaseURL, "/")
}

// loadModelCatalog 列模型。成功写缓存、返回读到的时刻；失败返回同来源的缓存（cached 为真）或空目录，err 是失败原因。
// 不在这里记日志：启动时报一次，后台重试只报状态变化（规格 6.1 第 5 条、第六节"每种状态只记一次"）
func (a *App) loadModelCatalog(ctx context.Context) (models []llm.Model, readAt time.Time, cached bool, err error) {
	models, err = a.llm.Models(ctx)
	if err == nil {
		readAt = a.now()
		if e := writeModelCatalogCache(a.paths.ModelCatalogCache(), a.catalogSource(), readAt, models); e != nil {
			a.logf("模型目录缓存没写成：%v", e)
		}
		return models, readAt, false, nil
	}
	models, readAt, cached = a.readModelCatalogCache(a.catalogSource())
	return models, readAt, cached, err
}

// readModelCatalogCache 读缓存；文件不在、坏了、来源地址对不上，都当没有缓存，各记一行原因（只记一次、不刷屏）
func (a *App) readModelCatalogCache(source string) (models []llm.Model, readAt time.Time, ok bool) {
	b, err := os.ReadFile(a.paths.ModelCatalogCache())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.logf("模型目录缓存读不了，当没有缓存：%v", err)
		}
		return nil, time.Time{}, false
	}
	var c modelCatalogCache
	if err := json.Unmarshal(b, &c); err != nil {
		a.logf("模型目录缓存坏了，当没有缓存：%v", err)
		return nil, time.Time{}, false
	}
	if c.Source != source {
		a.logf("模型目录缓存是另一个来源（%s）的，当没有缓存", c.Source)
		return nil, time.Time{}, false
	}
	return c.Models, c.ReadAt, true
}

// writeModelCatalogCache 写目录缓存：先写 .tmp 再改名替换。调用方只记日志、不让保存失败挡住启动
func writeModelCatalogCache(path, source string, readAt time.Time, models []llm.Model) error {
	b, err := json.Marshal(modelCatalogCache{Source: source, ReadAt: readAt, Models: models})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := fileatomic.Replace(tmp, path, 2*time.Second); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// startCatalogRetry 起后台重试：目录取不到时等一段再列一次，成功就发布新目录、清 CatalogError、写缓存、
// 记一行"模型来源恢复了"（catalogRetryLoop）。关机时随 App 停（stopCatalogRetry）；手动刷新取到了、CatalogError 空了也停。
// 已经在跑就不重复起
func (a *App) startCatalogRetry() {
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	if a.catalogCancel != nil || a.opt.Mode != Receive {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.catalogCancel, a.catalogDone = cancel, done
	go func() {
		defer close(done)
		a.catalogRetryLoop(ctx)
		a.catalogMu.Lock()
		if a.catalogDone == done { // 自己跑完（取到了）就把位置让出来，以后失败还能再起
			a.catalogCancel, a.catalogDone = nil, nil
		}
		a.catalogMu.Unlock()
	}()
}

// stopCatalogRetry 停掉后台重试并等它退出；ctx 到时限也不再等
func (a *App) stopCatalogRetry(ctx context.Context) {
	a.catalogMu.Lock()
	cancel, done := a.catalogCancel, a.catalogDone
	a.catalogCancel, a.catalogDone = nil, nil
	a.catalogMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		a.logf("模型目录重试没停下来：%v", ctx.Err())
	}
}

// catalogRetryLoop 是重试本身：CatalogError 不空时按 catalogRetryDelays 等，之后重新列模型
func (a *App) catalogRetryLoop(ctx context.Context) {
	delays := catalogRetryDelays
	if len(a.opt.CatalogRetry.Delays) > 0 {
		delays = a.opt.CatalogRetry.Delays
	}
	wait := a.opt.CatalogRetry.Wait
	if wait == nil {
		wait = waitCatalog
	}
	lastLogged := ""
	for failures := 0; ; failures++ {
		if ctx.Err() != nil || a.currentModels().CatalogError == "" {
			return
		}
		d := delays[min(failures, len(delays)-1)]
		if !wait(ctx, d) {
			return
		}
		if ctx.Err() != nil || a.currentModels().CatalogError == "" {
			return
		}
		a.modelMu.Lock()
		err := a.refreshModels(ctx)
		models := a.currentModels().Models
		a.modelMu.Unlock()
		if err == nil {
			a.logf("模型来源恢复了：%d 个模型", len(models))
			return
		}
		if ctx.Err() != nil {
			return // 关机取消的，不是来源又坏了
		}
		// 同一种原因只记一次（第六节"日志每个服务商每种状态只记一次变化"）
		if msg := err.Error(); msg != lastLogged {
			a.logf("模型目录还是没取到，接着重试：%v", err)
			lastLogged = msg
		}
	}
}

// waitCatalog 是真的等待（测试给 Options.CatalogRetry.Wait，不靠墙钟）
func waitCatalog(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// catalogFailure 把"列模型失败"变成一句人话：有缓存时说明用的是几点的缓存，没有时说先用空列表
func catalogFailure(cached bool, readAt time.Time, n int, err error) string {
	if cached {
		return fmt.Sprintf("模型目录没取到，用的是 %s 的缓存（%d 个模型）：%v", readAt.Local().Format("15:04"), n, err)
	}
	return fmt.Sprintf("模型目录没取到，也没有可用的缓存，先用空列表：%v", err)
}
