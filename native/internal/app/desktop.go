package app

// 桌面入口装配查询、标已读，本地私聊和本地群聊的可选写入口，以及宿主状态源（规格-界面接Go后台 1.1、4.x）。
// 桌面关后台时先 Desktop.Close（等私聊和群的回合、界面查询和订阅退出），再 App.Close（关库、放锁）。

import (
	"context"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/applog"
	"sumika-native/internal/config"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/localgroup"
	"sumika-native/internal/memview"
	"sumika-native/internal/persona"
)

// Desktop 是一个 App 交给桌面的接口；同一个 App 只建一份。
type Desktop struct {
	Reader        DesktopReader
	Memory        api.MemoryReader // 记忆页的只读接口；只给桌面窗口（internal/archcheck 钉住别的包不引它）
	app           *App
	runtime       *desktopread.Runtime
	mem           *memview.Service
	mu            sync.Mutex
	closed        bool
	chatCtx       context.Context
	chatCancel    context.CancelFunc
	chatWork      sync.WaitGroup
	runs          map[string]context.CancelFunc
	approvalWakes map[chan struct{}]struct{}
	statusWakes   map[chan struct{}]struct{}
}

// Desktop 装配桌面接口：角色显示资料只读卡（读不到的单独报失败），设置每次显式重读配置。
// receive 下提供本地私聊、本地群聊写能力与本 App 的提醒；archive 保持只读且没有提醒源。
func (a *App) Desktop() *Desktop {
	a.desktopOnce.Do(func() {
		initial := api.RuntimeView{Mode: string(a.opt.Mode), Phase: "ready", Account: a.account}
		if a.opt.Mode == Archive {
			initial.Connection = "未连接 QQ"
			initial.Reason = "只看历史"
		} else {
			initial.Connection = "连接状态尚未接入界面"
		}
		rt := desktopread.NewRuntime(initial, a.alerts)
		chars := a.displayCharacters()
		svc := desktopread.NewResults(a.set, chars, rt).WithData(a.paths.Root).WithReadMark(a.opt.Mode != Archive) // archive 只读打开、约定不写：不标已读
		svc.WithBackfills(a.backfillProvider())
		if a.forwards != nil {
			svc.WithForwardFetcher(a.forwards)
		}
		if a.media != nil {
			svc.WithVideoDownloads(a.media)
		}
		if a.groups != nil {
			svc.WithLocalGroups(a.groups)
		}
		mem := a.memoryService(chars)
		root, dataDir := a.root, a.opt.DataDir
		a.desktop = &Desktop{app: a, runtime: rt, Memory: mem, mem: mem, Reader: DesktopReader{Reader: desktopread.Reader{
			Service: svc,
			SettingsReader: desktopread.SettingsReader{
				Load: func(context.Context) (config.Config, error) { return config.LoadIn(root, dataDir) },
				Running: map[string]any{
					"data.mode":      string(a.opt.Mode),
					"data.localPath": a.paths.ChatDB("local"),
					"data.qqPath":    a.paths.ChatDB("qq"),
				},
			},
		}}}
		if a.opt.Mode == Receive {
			running := a.desktop.Reader.SettingsReader.Running
			running["plugins.sessions"] = desktopread.PluginSettings(&a.cfg)
			running["reply.deny"] = desktopread.DenySettings(&a.cfg)
			for id, ch := range a.cfg.Characters {
				running["characters."+id+".aliases"] = ch.Aliases
			}
		}
		a.desktop.Reader.desk = a.desktop
		a.desktop.initChat()
		a.desktopPtr.Store(a.desktop)
	})
	return a.desktop
}

// Publish 由宿主在后台线程调用，提交已经确认的阶段与连接事实；App 关了以后不再发布。
func (d *Desktop) Publish(v api.RuntimeView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.app.shutDowned.Load() {
		return
	}
	d.runtime.Publish(v)
}

// Close 取消界面的查询和订阅并等它们退出；调完再关 App。可以重复调用。
func (d *Desktop) Close() {
	d.closeChat()
	if d.app.groups != nil {
		d.app.groups.Close()
	}
	if d.app.whisper != nil {
		d.app.whisper.Close()
	}
	d.Reader.Service.Close()
	d.mem.Close()
}

// displayCharacters 列数据目录里有卡的角色；某张卡坏了只这一个报失败，目录本身读不到时整组报一条失败。
func (a *App) displayCharacters() []api.Result[api.CharacterView] {
	ids, err := a.characterIDs()
	if err != nil {
		return []api.Result[api.CharacterView]{{State: api.Failed, Value: api.CharacterView{Name: "角色目录"}, Err: err}}
	}
	out := make([]api.Result[api.CharacterView], 0, len(ids))
	for _, id := range ids {
		name, color, model, err := persona.Display(a.paths.CharactersDir(), id, a.cfg.Characters[id].Model)
		if err != nil {
			out = append(out, api.Result[api.CharacterView]{State: api.Failed, Value: api.CharacterView{ID: id, Name: id, Status: "读不到"}, Err: err})
			continue
		}
		status := "只看历史"
		if a.opt.Mode == Receive {
			if a.opt.LocalOnly {
				status = "这次没开 QQ"
			} else {
				status = "接收中"
				if id != a.character {
					status = "没有接收"
				}
			}
		}
		// 没选模型不算资料坏：Model 留空，界面显示"未选"
		out = append(out, api.Result[api.CharacterView]{State: api.Ready, Value: api.CharacterView{ID: id, Name: name, Color: color, Model: model, Status: status}})
	}
	return out
}

// 角色资料只在装配时冻结；坏卡不能被建群入口选中。
func (a *App) openLocalGroups() {
	var chars []api.CharacterView
	for _, c := range a.displayCharacters() {
		if c.State == api.Ready {
			chars = append(chars, c.Value)
		}
	}
	st, ok := a.set.Platform("local")
	if !ok {
		// local 的库没打开：本地私聊、本地群聊整个不可用（不拿 nil 去建，也不当成空库继续跑）。
		a.warnFor(applog.App)("本地库没打开，本地私聊和本地群聊不可用：%v", a.set.Unavailable()["local"])
		return
	}
	a.groups = localgroup.New(st, a.hub, chars, a.now)
	a.groups.OnStored = a.observeLocalLinks
}
