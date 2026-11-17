package app

// MCP 服务的隔离状态怎么给主人看、怎么解除（质检 X-08，后续路线 P0-3）。
//
// 库里（store/mcp_calls.go）记着哪些调用结果未知；Client 在内存里记连接状态。这里把两边拼成主人看的样子：
// 命令行 sumika-ctl mcp status、提醒（alert 键 mcp:<服务>）、自检项（selfcheck mcp.quarantined）读同一份。
// 解除只有主人在本地管理入口（ctl）能做，不是模型能调的工具（测试：mcp_quarantine_test.go 遍历工具清单）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/applog"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/kernel"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// mcpReleaseActor 是解除留痕里“谁”的写法：现在只有命令行一个入口
const mcpReleaseActor = "主人（命令行 sumika-ctl）"

func (a *App) mcpStore() store.EcosystemStore {
	return store.EcosystemStore{DB: a.set.TraceStore().Trace}
}

func mcpAlertTitle(id string) string { return "MCP 服务 " + id + " 被隔离" }

func mcpReconnectAlertTitle(id string) string {
	return "MCP 服务 " + id + " 断了，自动重连没成功"
}

// mcpCauseText 是原因的中文
func mcpCauseText(cause string) string {
	switch cause {
	case store.MCPCauseTimeout:
		return "调用时限到了还没回结果"
	case store.MCPCauseCancelled:
		return "调用被停止"
	case store.MCPCauseError:
		return "调用出错或断线"
	case store.MCPCauseHostExit:
		return "程序退出时这次调用还没有结果"
	}
	return "原因没记"
}

func mcpWhen(ms int64) string {
	if ms == 0 {
		return "时间没记"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}

// refreshMCPAlert 按库里挡着这个服务的行开或关它的提醒；Client 在隔离状态变化后回调它。
// 只关自己开的那条（标题认“被隔离”），启动时“没连上”的提醒不在这里动。
func (a *App) refreshMCPAlert(id string) {
	if a.alerts == nil || a.set == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := a.mcpStore().MCPQuarantined(ctx)
	if err != nil {
		a.warnFor(applog.Misc)("读 MCP 隔离记录失败：%v", err)
		return
	}
	var mine []store.MCPCall
	for _, c := range all {
		if c.Server == id && !a.mcpInFlight(c) {
			mine = append(mine, c)
		}
	}
	key := "mcp:" + id
	if len(mine) == 0 {
		if h, ok := a.mcpHealth(id); ok && h.Reconnect == "exhausted" {
			a.alerts.Raise(alert.Alert{Key: key, Class: "mcp", Title: mcpReconnectAlertTitle(id), Body: "本机服务的进程退出后，自动重连没有成功：" + h.Note +
				"。修好原因后用 sumika-ctl mcp reconnect " + id + " 再试；日志在 MCP 服务的 stderr 日志里。"})
			return
		}
		for _, x := range a.alerts.Open() {
			if x.Key == key && (x.Title == mcpAlertTitle(id) || x.Title == mcpReconnectAlertTitle(id)) {
				a.alerts.Clear(key, alert.Note{Title: "MCP 服务 " + id + " 恢复了", Body: "可以再调用它了。"})
			}
		}
		return
	}
	first := mine[0]
	body := fmt.Sprintf("有 %d 次调用发出后没拿到结果（最早一次：%s，%s），对方可能已经执行、也可能还在执行，为防止重复执行已暂停这个服务。"+
		"先用 sumika-ctl mcp status 看是哪几次，确认后：sumika-ctl mcp release %s --reason \"原因\" --accept-unknown-risk。重连、重启都不会解除。",
		len(mine), mcpWhen(first.Note.DispatchedAt), mcpCauseText(first.Note.Cause), id)
	for _, c := range mine {
		if c.Note.Transport == "" {
			body += "其中有旧版本留下的记录（没记传输种类），重启后不会自动收尾，要手动解除一次。"
			break
		}
	}
	a.alerts.Raise(alert.Alert{Key: key, Class: "mcp", Title: mcpAlertTitle(id), Body: body})
}

// recoverMCPCalls 在启动时给上一个进程留下的“已派发”收尾：程序被杀或崩溃时那些调用的结果永远不会回来。
// 本机服务（子进程树随宿主退出）记成已终止、不再挡；远端服务和旧版本留下的行改成“结果未知”、继续挡着，等主人解除。
// 收尾失败不拦启动：库坏了的话那些行仍是“挡着”，更保守。
func (a *App) recoverMCPCalls(ctx context.Context, repo store.EcosystemStore) {
	rec, err := repo.MCPRecoverAfterRestart(ctx, mcpclient.ChildrenDieWithHost)
	if err != nil {
		a.warnFor(applog.Misc)("MCP 遗留调用收尾失败，相关服务保持隔离：%v", err)
		return
	}
	if rec.Terminated+rec.Unknown > 0 {
		a.logf("MCP 遗留调用收尾：%d 次本机服务的调用记为已终止（进程树随程序退出），%d 次结果未知的继续挡着对应服务", rec.Terminated, rec.Unknown)
	}
}

// MCPStatus 是 sumika-ctl mcp status：配置里的、连接过的、库里有隔离记录的服务各一行。
func (a *App) MCPStatus(ctx context.Context) (ctlproto.MCPStatus, error) {
	if a.ecosystem == nil || a.ecosystem.MCP == nil {
		return ctlproto.MCPStatus{}, ctlproto.Errorf(ctlproto.CodeFailed, "MCP 没有启用")
	}
	calls, err := a.mcpStore().MCPQuarantined(ctx)
	if err != nil {
		return ctlproto.MCPStatus{}, err
	}
	rows := map[string]*ctlproto.MCPServer{}
	row := func(id string) *ctlproto.MCPServer {
		if r := rows[id]; r != nil {
			return r
		}
		r := &ctlproto.MCPServer{ID: id}
		rows[id] = r
		return r
	}
	for id, cfg := range a.mcpServers {
		if cfg.Sumika.Enabled {
			row(id).Transport = mcpTransport(cfg)
		}
	}
	for _, h := range a.ecosystem.MCP.Health() {
		r := row(h.ID)
		r.Transport, r.Connected = h.Transport, h.Connected
		r.Reconnect, r.Note = h.Reconnect, h.Note
	}
	for _, c := range calls {
		if a.mcpInFlight(c) {
			continue
		}
		r := row(c.Server)
		r.Isolated = true
		if r.Transport == "" {
			r.Transport = c.Note.Transport
		}
		r.Calls = append(r.Calls, ctlproto.MCPCall{ID: shortID(c.ID), State: c.State, Cause: c.Note.Cause, Transport: c.Note.Transport, Legacy: c.Note.Transport == "", DispatchedAt: c.Note.DispatchedAt, EndedAt: c.Note.EndedAt})
	}
	out := ctlproto.MCPStatus{Servers: make([]ctlproto.MCPServer, 0, len(rows))}
	for _, r := range rows {
		out.Servers = append(out.Servers, *r)
	}
	sort.Slice(out.Servers, func(i, j int) bool { return out.Servers[i].ID < out.Servers[j].ID })
	return out, nil
}

func mcpTransport(cfg mcpclient.ServerConfig) string {
	if cfg.Type == "http" {
		return "http"
	}
	return "stdio"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// MCPRelease 是 sumika-ctl mcp release：主人带原因接受“旧调用结果未知”的风险，解除这个服务的隔离，
// 留痕记谁、什么时候、原因、解除了哪几次旧调用（store.MCPRelease，旧行保留）；然后用配置里的现状补连一次。
// ctl 层已经校验过原因和 --accept-unknown-risk。
func (a *App) MCPRelease(ctx context.Context, server, reason string) (ctlproto.MCPReleased, error) {
	out := ctlproto.MCPReleased{Server: server}
	if a.opt.Mode == Archive {
		return out, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式没有 MCP 服务，请先开始接收")
	}
	if a.ecosystem == nil || a.ecosystem.MCP == nil {
		return out, ctlproto.Errorf(ctlproto.CodeFailed, "MCP 没有启用")
	}
	cfg, configured := a.mcpServers[server]
	n, err := a.ecosystem.MCP.Release(ctx, server, mcpReleaseActor, reason)
	switch {
	case errors.Is(err, mcpclient.ErrCallInFlight):
		return out, ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
	case err != nil:
		return out, err
	}
	if n == 0 {
		if !configured {
			return out, ctlproto.Errorf(ctlproto.CodeFailed, "没有叫 "+server+" 的 MCP 服务，也没有它的隔离记录")
		}
		return out, ctlproto.Errorf(ctlproto.CodeFailed, server+" 现在没有被隔离的旧调用；本机服务重连用完了次数的话用 sumika-ctl mcp reconnect")
	}
	out.Released = n
	a.logf("MCP 服务 %s 已由主人解除隔离（%d 次旧调用接受未知风险）", server, n)
	if !configured || !cfg.Sumika.Enabled {
		out.Note = "配置里没有启用这个服务，解除后没有重连"
		return out, nil
	}
	if connected := a.mcpConnected(server); connected {
		out.Reopened = true
		return out, nil
	}
	if err := a.connectMCPServer(ctx, server, cfg); err != nil {
		out.Note = "已解除隔离，但重新连接失败：" + err.Error()
		return out, nil
	}
	out.Reopened = true
	return out, nil
}

func (a *App) mcpHealth(id string) (mcpclient.Health, bool) {
	if a.ecosystem == nil || a.ecosystem.MCP == nil {
		return mcpclient.Health{}, false
	}
	for _, h := range a.ecosystem.MCP.Health() {
		if h.ID == id {
			return h, true
		}
	}
	return mcpclient.Health{}, false
}

func (a *App) mcpConnected(id string) bool {
	h, ok := a.mcpHealth(id)
	return ok && h.Connected
}

// MCPReconnect 是 sumika-ctl mcp reconnect：自动重连用完次数、或这次启动没连上的服务，主人修好原因后让它按配置再连一次。
// 被隔离的服务照样被拒：重连不能解除隔离。
func (a *App) MCPReconnect(ctx context.Context, server string) (ctlproto.MCPReconnected, error) {
	out := ctlproto.MCPReconnected{Server: server}
	if a.opt.Mode == Archive {
		return out, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式没有 MCP 服务，请先开始接收")
	}
	cfg, ok := a.mcpServers[server]
	if a.ecosystem == nil || a.ecosystem.MCP == nil || !ok {
		return out, ctlproto.Errorf(ctlproto.CodeFailed, "配置里没有启用叫 "+server+" 的 MCP 服务")
	}
	if err := a.connectMCPServer(ctx, server, cfg); err != nil {
		if errors.Is(err, mcpclient.ErrQuarantined) {
			return out, ctlproto.Errorf(ctlproto.CodeFailed, server+" 被隔离，重连不能解除；先用 sumika-ctl mcp status 看旧调用，确认后用 mcp release")
		}
		return out, ctlproto.Errorf(ctlproto.CodeFailed, "重连失败："+err.Error())
	}
	out.Connected = true
	return out, nil
}

// connectMCPServer 是启动时连一个服务、也是解除隔离后补连一次的同一条路：登记成 Lifetime 活动，成功后撤掉“没连上 / 被隔离”的提醒。
func (a *App) connectMCPServer(ctx context.Context, id string, server mcpclient.ServerConfig) error {
	// RunID 每次都要不同：活动编号是"RunID/activity/序号"，写死的 RunID 第二次就撞主键，整个接收起不来（2026-11-08）
	inv := kernel.Invocation{RunID: "mcp-start:" + id + ":" + store.NewID(a.now()), Kind: kernel.KindForeground}
	startup, activity, err := a.hub.Cfg.Lifetime.Begin(ctx, inv)
	if err != nil {
		return err
	}
	err = a.ecosystem.MCP.Connect(startup, server)
	activity.End(err)
	if err != nil {
		return err
	}
	a.logf("MCP 服务 %s 已连上", id)
	if a.alerts != nil {
		a.alerts.Clear("mcp:"+id, alert.Note{Title: "MCP 服务 " + id + " 已连上", Body: "可以用了。"})
	}
	return nil
}

// mcpInFlightMax 是一次 MCP 调用能“已派发”多久还算正在进行：调用时限最大 600 秒，加上落库的余量
const mcpInFlightMax = 11 * time.Minute

// mcpAlertOpen 是这个服务现在有没有开着的 MCP 提醒（隔离或重连用完，同一个键）
func (a *App) mcpAlertOpen(id string) bool {
	if a.alerts == nil {
		return false
	}
	for _, x := range a.alerts.Open() {
		if x.Key == "mcp:"+id && (x.Title == mcpAlertTitle(id) || x.Title == mcpReconnectAlertTitle(id)) {
			return true
		}
	}
	return false
}

// mcpInFlight 是这一行是不是本进程正在进行的调用（还没有结局）：它不是隔离，状态、自检、提醒都不算。
// 单次调用最长 10 分钟加落库，超过还停在“已派发”的才算卡住。
func (a *App) mcpInFlight(c store.MCPCall) bool {
	return c.State == store.MCPStateDispatched && c.Note.Process == store.ProcessID() && c.Note.DispatchedAt != 0 &&
		a.now().Sub(time.UnixMilli(c.Note.DispatchedAt)) < mcpInFlightMax
}

// mcpSelfcheck 给自检 S11（mcp.quarantined）：库里挡着调用的服务，加本机服务自动重连用完的服务。
// 读库用自检自己的只读连接；MCP 没启用时一律空（正常）。
func (a *App) mcpSelfcheck(ro *sql.DB) func(context.Context) ([]selfcheck.MCPIsolation, error) {
	return func(ctx context.Context) ([]selfcheck.MCPIsolation, error) {
		if a.ecosystem == nil || a.ecosystem.MCP == nil {
			return nil, nil
		}
		if ro == nil {
			return nil, errors.New("没有运行记录库的只读连接")
		}
		calls, err := store.EcosystemStore{DB: ro}.MCPQuarantined(ctx)
		if err != nil {
			return nil, err
		}
		byServer := map[string]*selfcheck.MCPIsolation{}
		for _, c := range calls {
			if a.mcpInFlight(c) {
				continue
			}
			m := byServer[c.Server]
			if m == nil {
				m = &selfcheck.MCPIsolation{Server: c.Server}
				byServer[c.Server] = m
			}
			m.Calls++
			since := time.Time{}
			if t := c.Note.EndedAt; t != 0 {
				since = time.UnixMilli(t)
			} else if t = c.Note.DispatchedAt; t != 0 {
				since = time.UnixMilli(t)
			}
			m.Cause = firstNonEmpty(m.Cause, c.Note.Cause)
			if m.Since.IsZero() || (!since.IsZero() && since.Before(m.Since)) {
				m.Since = since
			}
		}
		for _, h := range a.ecosystem.MCP.Health() {
			if h.Reconnect == "exhausted" {
				m := byServer[h.ID]
				if m == nil {
					m = &selfcheck.MCPIsolation{Server: h.ID}
					byServer[h.ID] = m
				}
				m.Exhausted = true
			}
		}
		out := make([]selfcheck.MCPIsolation, 0, len(byServer))
		for _, m := range byServer {
			out = append(out, *m)
			// 在途窗口过了仍停在“已派发”的（结局没写进库），当时没有人触发提醒：这一轮顺手补上；已经开着的不重复发
			if !a.mcpAlertOpen(m.Server) {
				a.refreshMCPAlert(m.Server)
			}
		}
		return out, nil
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
