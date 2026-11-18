package app

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"sumika-native/internal/envcheck"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// envInputsState 是环境自检运行部分要用的只读连接。它和 selfcheck runner 分开：
// selfcheck.enabled=false 时环境清单照样要能算（规格-环境自检 坑 2）。
type envInputsState struct {
	mu    sync.Mutex
	trace *sql.DB
}

// EnvInputs 给桌面宿主算环境快照的运行部分（规格-环境自检 设计第 1 条）：模型来源、模型资料库、
// 嵌入、内核、插件、MCP、命令分析器、备份。只读，不起外部程序；后台不在接收模式时返回零值，
// 宿主会把运行部分记 unknown 并写原因。
func (a *App) EnvInputs(ctx context.Context) envcheck.Inputs {
	if a == nil || a.opt.Mode != Receive {
		return envcheck.Inputs{}
	}
	in := envcheck.Inputs{}

	source, readAt, catalogErr := a.CatalogState()
	in.Gateway = envcheck.GatewayInput{Available: true, Source: source, ReadAt: readAt, Err: catalogErr}

	if a.modelDev != nil {
		st := a.modelDev.State()
		in.Catalog = envcheck.CatalogInput{Available: true, Enabled: st.Enabled, Entries: st.Entries,
			FetchedAt: st.FetchedAt, FromCache: st.FromCache, Err: st.Error}
	} else {
		in.Catalog = envcheck.CatalogInput{Available: true}
	}

	if a.hub != nil {
		in.Plugins = envcheck.PluginsInput{Available: true, Failed: failedPlugins(a.hub.PluginStatuses())}
	}

	if a.approvalGate != nil && a.approvalGate.Analyzer != nil {
		st := a.approvalGate.Analyzer.Status()
		in.Analyzer = envcheck.AnalyzerInput{Available: true, OK: st.Available, Path: st.Path, LastErr: st.LastErr}
	} else {
		in.Analyzer = envcheck.AnalyzerInput{Available: true, LastErr: "审批门没有接命令分析器"}
	}

	env := a.selfcheckEnvInputs(ctx)
	res := selfcheck.EvalNow(ctx, env, a.selfcheckThresholds(),
		"embed.unavailable", "kernel.faulted", "mcp.quarantined", "backup.daily_stale", "backup.drill_stale")
	byID := make(map[string]envcheck.CheckResult, len(res))
	for id, r := range res {
		byID[id] = checkResultOf(r)
	}
	in.Embed = checkInputFor(byID, "embed.unavailable")
	in.Kernel = checkInputFor(byID, "kernel.faulted")
	in.MCP = envcheck.MCPInput{CheckInput: checkInputFor(byID, "mcp.quarantined")}
	in.Backup = envcheck.BackupInput{Available: true,
		Daily: byID["backup.daily_stale"], Drill: byID["backup.drill_stale"]}
	if a.ecosystem != nil && a.ecosystem.MCP != nil {
		in.MCP.NeverConnected = neverConnected(a.ecosystem.MCP.Health(), byID["mcp.quarantined"].Example["servers"])
	}
	return in
}

// checkResultOf 把自检结果拷成 envcheck 的普通值（状态、备注、下一步、例子）。
func checkResultOf(r selfcheck.Result) envcheck.CheckResult {
	return envcheck.CheckResult{State: string(r.State), Note: r.Note, Next: r.Next, Example: r.Example}
}

func checkInputFor(m map[string]envcheck.CheckResult, id string) envcheck.CheckInput {
	return envcheck.CheckInput{Available: true, CheckResult: m[id]}
}

// failedPlugins 取启用失败的插件（Hub.PluginStatuses 里 state=failed 的那些）。
func failedPlugins(statuses []turn.PluginStatus) []envcheck.PluginFailure {
	var out []envcheck.PluginFailure
	for _, st := range statuses {
		if st.State == "failed" {
			out = append(out, envcheck.PluginFailure{ID: st.ID, Err: st.Err})
		}
	}
	return out
}

// neverConnected 补 S11 没覆盖的口径（坑 11）：健康状态里 Connected=false、又不是被隔离或重连用完的服务。
// already 是 S11 例子里已经列出的服务（逗号分隔），避免同一个服务写两遍。
func neverConnected(health []mcpclient.Health, already string) []envcheck.NeverConnectedService {
	known := map[string]bool{}
	for _, name := range strings.Split(already, ",") {
		if name = strings.TrimSpace(name); name != "" {
			known[name] = true
		}
	}
	var out []envcheck.NeverConnectedService
	for _, h := range health {
		if h.Connected || h.Reconnect == "exhausted" || known[h.ID] {
			continue
		}
		out = append(out, envcheck.NeverConnectedService{Name: h.ID, Cause: h.Note})
	}
	return out
}

// selfcheckEnvInputs 拼出 EvalNow 看的 Env：只接 S9～S13 用到的那几份状态，不接 runner 的计数器和 KV。
func (a *App) selfcheckEnvInputs(context.Context) *selfcheck.Env {
	return &selfcheck.Env{
		Now:    a.now(),
		Backup: a.backupSelfcheck,
		Drill:  a.drillSelfcheck,
		Faults: a.kernelFaultsSelfcheck,
		StrayEnds: func() int {
			if a.hub == nil {
				return 0
			}
			return a.hub.StrayEndFailures()
		},
		MCP:   a.mcpSelfcheck(a.envTraceReader()),
		Embed: a.embedSelfcheck,
	}
}

// envTraceReader 是环境自检读 MCP 隔离行的只读连接；第一次用到才开，失败下次再试。
func (a *App) envTraceReader() *sql.DB {
	a.envInputs.mu.Lock()
	defer a.envInputs.mu.Unlock()
	if a.envInputs.trace != nil {
		return a.envInputs.trace
	}
	db, err := store.OpenReadOnlyFile(a.paths.GoTraceDB())
	if err != nil {
		return nil
	}
	a.envInputs.trace = db
	return db
}

// notifyEnvChanged 告诉宿主环境快照该重算了（配置保存、密钥库变化）。不阻塞调用方：宿主自己决定怎么做。
func (a *App) notifyEnvChanged() {
	if a != nil && a.opt.EnvChanged != nil {
		a.opt.EnvChanged()
	}
}

// closeEnvInputs 关环境自检的只读连接；可重复调。
func (a *App) closeEnvInputs() {
	a.envInputs.mu.Lock()
	defer a.envInputs.mu.Unlock()
	if a.envInputs.trace != nil {
		a.envInputs.trace.Close()
		a.envInputs.trace = nil
	}
}
