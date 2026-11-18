package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/config"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/mcpplugin"
	"sumika-native/internal/skills"
	"sumika-native/internal/store"
	"sumika-native/internal/subagent"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"sumika-native/internal/web"
	"sumika-native/internal/web/wiki"
)

// EcosystemPorts 只接受宿主提供的真实端口。没有集成前置时不启用对应来源。
type EcosystemPorts struct {
	Hub             *turn.Hub
	Store           store.EcosystemStore
	Host            ecosystem.ToolHost
	Lifetime        kernel.Lifetime
	Budget          kernel.Budget
	Results         kernel.ResultStore
	ApproveRead     func(context.Context, ecosystem.Access, string) error
	ApproveMCP      func(context.Context, *mcpclient.ResolvedCall) approval.Clearance
	DeliverSkill    func(turn.ToolCall, skills.Page) (tools.Result, error)
	ParentValid     func(context.Context, kernel.Invocation) bool
	ParentContext   func(context.Context, kernel.Invocation) (context.Context, error)
	ParentWorkspace func(context.Context, kernel.Invocation) (turn.Workspace, error)
	Notify          func(kernel.Scope)
	Keys            map[string]string    // 联网服务的 key，键是服务名：tavily、exa、brave、firecrawl
	KeyErrs         map[string]error     // 对应 keyRef 解析失败的原因
	MCPLogDir       string               // MCP stdio 服务的 stderr 日志目录；空 = 不写文件
	Logf            func(string, ...any) // 日志；联网余额变化只写“服务商名 剩余 N”
}
type Ecosystem struct {
	Skills *skills.Catalog
	MCP    *mcpclient.Client
	Web    *web.Service
	// WebBalances 记各家搜索服务的真实余额（Tavily、Firecrawl 查接口，Brave 读响应头）；联网没开是 nil。
	WebBalances *web.Balances
	// WebOrder 是 auto 时各家的先后；设置页拖动排序后写进来，立即生效。联网没开是 nil。
	WebOrder *web.Order
	Tasks    *subagent.Manager
	hub      *turn.Hub
}

func OpenEcosystem(ctx context.Context, c config.Ecosystem, p EcosystemPorts) (*Ecosystem, error) {
	e := &Ecosystem{hub: p.Hub}
	fail := func(err error) (*Ecosystem, error) {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, e.Close(closeCtx))
	}
	if c.Skills.Enabled {
		if p.Host.ContextAccess == nil || p.ApproveRead == nil || p.DeliverSkill == nil {
			return fail(fmt.Errorf("skill 缺身份、读取审批或结果处置端口"))
		}
		var roots []skills.Root
		for _, r := range c.Skills.Roots {
			roots = append(roots, skills.Root{ID: r.ID, Path: r.Path, Scope: r.Scope})
		}
		var err error
		e.Skills, err = skills.New(skills.Options{Enabled: true, Roots: roots, Approve: p.ApproveRead, IndexBytes: c.Skills.IndexBytes, PageItems: c.Skills.PageItems, ReadBytes: c.Skills.ReadBytes, FileMaxBytes: c.Skills.FileMaxBytes, ContextMaxBytes: c.Skills.ContextMaxBytes, ScanTimeoutMs: c.Skills.ScanTimeoutMs})
		if err != nil {
			return fail(err)
		}
		if err = p.Hub.Use(&skills.Plugin{Catalog: e.Skills, Access: p.Host.Access, ContextAccess: p.Host.ContextAccess, Visible: p.Host.Visible, Delivery: p.DeliverSkill}); err != nil {
			return fail(err)
		}
	}
	// MCP 连接由显式配置装载后调用 Connect；Setup 和 OnContext 均不联网。
	e.MCP = mcpclient.New(p.Store)
	e.MCP.SetLogDir(p.MCPLogDir)
	if c.Web.Enabled {
		e.WebBalances = web.NewBalances()
		if p.Logf != nil {
			e.WebBalances.OnUpdate = func(id string, b web.Balance) { p.Logf("联网：%s", web.BalanceLine(id, b)) }
		}
		e.WebOrder = web.NewOrder(orderOrDefault(c.Web.SearchOrder, config.DefaultSearchOrder()), orderOrDefault(c.Web.FetchOrder, config.DefaultFetchOrder()))
		provider, fetch, err := chooseWebProviders(c.Web, p.Keys, p.KeyErrs, nil, e.WebBalances, e.WebOrder)
		if err != nil {
			return fail(err)
		}
		e.Web, err = web.New(web.Options{Enabled: true, Lifetime: p.Lifetime, Timeout: time.Duration(c.Web.TimeoutMs) * time.Millisecond, Policy: web.URLPolicy{}, Journal: p.Store, DailySearch: c.Web.DailySearch, DailyFetch: c.Web.DailyFetch, MonthlyMicroCredits: c.Web.MonthlyMicroCredits, Timezone: c.Web.Timezone, ResponseMaxBytes: c.Web.ResponseMaxBytes, MaxInflight: c.Web.MaxInflight, MaxInflightPerTask: c.Web.MaxInflightPerTask, MaxQueriesPerTask: c.Web.MaxQueriesPerTask, MaxFetchesPerTask: c.Web.MaxFetchesPerTask, MaxDispatchesPerTask: c.Web.MaxDispatchesPerTask, SearchCacheSeconds: c.Web.SearchCacheSeconds, FetchCacheSeconds: c.Web.FetchCacheSeconds, CacheBytes: c.Web.CacheBytes, CacheEntries: c.Web.CacheEntries}, provider, fetch)
		if err != nil {
			return fail(err)
		}
		e.Web.SetWiki(wiki.New(orderOrDefault(c.Web.Wiki.MoegirlHosts, config.EcosystemDefaults().Web.Wiki.MoegirlHosts), nil))
		if err = p.Hub.Use(&web.Plugin{Service: e.Web, Host: p.Host}); err != nil {
			return fail(err)
		}
	}
	if c.Subagents.Enabled {
		var err error
		definitions := subagent.Definitions(c.Subagents.Model)
		for index := range definitions {
			definition := &definitions[index]
			definition.SoftRequests = int64(c.Subagents.SoftRequestBudget)
			if definition.ID == "research" {
				definition.Timeout = time.Duration(c.Subagents.ResearchTimeoutMs) * time.Millisecond
			} else {
				definition.Timeout = time.Duration(c.Subagents.LocalTimeoutMs) * time.Millisecond
			}
		}
		e.Tasks, err = subagent.New(subagent.Options{Enabled: true, Definitions: definitions, Repository: p.Store, Runner: p.Hub, Lifetime: p.Lifetime, Budget: p.Budget, Results: p.Results, ParentValid: p.ParentValid, ParentContext: p.ParentContext, ParentWorkspace: p.ParentWorkspace, Notify: p.Notify, MaxDepth: c.Subagents.MaxDepth, MaxActivePerRoot: c.Subagents.MaxActivePerRoot, MaxActiveGlobal: c.Subagents.MaxActiveGlobal, MaxActivePerRequester: c.Subagents.MaxActivePerRequester, CreateTimeout: time.Duration(c.Subagents.CreateTimeoutMs) * time.Millisecond, ResultTimeout: time.Duration(c.Subagents.ResultTimeoutMs) * time.Millisecond})
		if err != nil {
			return fail(err)
		}
		if err = p.Store.RecoverTasks(ctx); err != nil {
			return fail(err)
		}
		if err = p.Hub.Use(&subagent.Plugin{Manager: e.Tasks, Host: p.Host}); err != nil {
			return fail(err)
		}
	}
	return e, nil
}
func (e *Ecosystem) EnableMCP(p EcosystemPorts) error {
	return e.hub.Use(&mcpplugin.Plugin{Client: e.MCP, Host: p.Host, Results: p.Results, Approve: p.ApproveMCP, ParentContext: p.ParentContext, Lifetime: p.Lifetime})
}
func (e *Ecosystem) Close(ctx context.Context) error {
	for _, id := range []string{"subagent", "web", "mcp", "skills"} {
		if e.hub != nil {
			e.hub.StopPlugin(id)
		}
	}
	var errs []error
	if e.Tasks != nil {
		errs = append(errs, e.Tasks.Close(ctx))
	}
	if e.Web != nil {
		errs = append(errs, e.Web.Close(ctx))
	}
	if e.MCP != nil {
		errs = append(errs, e.MCP.Close())
	}
	if e.Skills != nil {
		errs = append(errs, e.Skills.Close())
	}
	return errors.Join(errs...)
}
