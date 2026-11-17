package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/alert"
	"sumika-native/internal/approval"
	"sumika-native/internal/artifact"
	"sumika-native/internal/audience"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/mcpplugin"
	"sumika-native/internal/policy"
	"sumika-native/internal/secrets"
	"sumika-native/internal/skills"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"time"
)

func (a *App) openKernelEcosystem(ctx context.Context) error {
	a.authority = ecosystem.NewAuthority()
	a.authorityIssued = map[string]kernel.Scope{}
	a.ecosystemCaps = []string{"local.read", "local.write", "local.exec"}
	c := a.cfg.Ecosystem
	if c.Skills.Enabled {
		a.ecosystemCaps = append(a.ecosystemCaps, "skill:builtin")
		for _, root := range c.Skills.Roots {
			a.ecosystemCaps = append(a.ecosystemCaps, "skill:"+root.ID)
		}
	}
	if c.Web.Enabled {
		a.ecosystemCaps = append(a.ecosystemCaps, "web:search", "web:fetch")
	}
	if c.Subagents.Enabled {
		a.ecosystemCaps = append(a.ecosystemCaps, "task:reader", "task:worker", "task:research", "task:mailbox", "public-delegation")
	}
	var servers mcpclient.Configuration
	data, err := os.ReadFile(a.paths.MCPConfigFile())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		servers, err = mcpclient.ParseConfiguration(data, a.paths.MCPConfigFile(), a.ecosystemSecret)
		if err != nil {
			return err
		}
		if servers.Sumika.Enabled {
			a.mcpServers = map[string]mcpclient.ServerConfig{}
			for id, server := range servers.MCPServers {
				if server.Sumika.Enabled {
					a.ecosystemCaps = append(a.ecosystemCaps, "mcp:"+id)
					a.mcpServers[id] = server
				}
			}
		}
	}
	webKeys, webKeyErrs := map[string]string{}, map[string]error{}
	if c.Web.Enabled {
		for service, keyRef := range map[string]string{"tavily": c.Web.KeyRef, "exa": c.Web.ExaKeyRef, "brave": c.Web.BraveKeyRef, "firecrawl": c.Web.FirecrawlKeyRef, "deepseek": c.Web.DeepSeekKeyRef} {
			if keyRef == "" {
				continue
			}
			ref := strings.TrimSuffix(strings.TrimPrefix(keyRef, "${"), "}")
			kind, name, ok := strings.Cut(ref, ":")
			if !ok {
				webKeyErrs[service] = fmt.Errorf("web %s 的密钥引用必须是 env 或 secret", service)
				continue
			}
			if webKeys[service], err = a.ecosystemSecret(kind, name); err != nil {
				webKeyErrs[service] = err
			}
		}
	}
	host := ecosystem.ToolHost{
		Access: func(call turn.ToolCall) (ecosystem.Access, error) {
			if err := a.hub.CheckInvocation(context.Background(), call.Identity); err != nil {
				return ecosystem.Access{}, err
			}
			return a.authority.Resolve(call.Identity.Scope)
		},
		Invocation: func(call turn.ToolCall) (kernel.Invocation, error) {
			return call.Identity, a.hub.CheckInvocation(context.Background(), call.Identity)
		},
		ContextAccess: func(in turn.ContextIn) (ecosystem.Access, error) { return a.authority.Resolve(in.Identity.Scope) },
		Visible: func(aud audience.Audience, name string) bool {
			if aud.Channel != "console" || aud.Scene == "group" || !aud.PrivateOK || strings.Contains(aud.Conversation, ":group:") {
				return false
			}
			if !strings.HasPrefix(aud.Conversation, "internal:") {
				a.authorityMu.Lock()
				valid := false
				for _, scope := range a.authorityIssued {
					if scope.Conversation == aud.Conversation {
						if _, err := a.authority.Resolve(scope); err == nil {
							valid = true
							break
						}
					}
				}
				a.authorityMu.Unlock()
				if !valid {
					return false
				}
			}
			switch name {
			case "skill":
				return c.Skills.Enabled
			case "mcp":
				return servers.Sumika.Enabled
			case "web_search", "web_fetch", "wiki_lookup":
				return c.Web.Enabled
			case "task", "task_result", "task_list", "task_message", "task_cancel":
				return c.Subagents.Enabled
			}
			return false
		},
	}
	ports := EcosystemPorts{Hub: a.hub, Store: store.EcosystemStore{DB: a.set.TraceStore().Trace}, Host: host, Lifetime: a.hub.Cfg.Lifetime, Budget: a.hub.Cfg.Budget, Results: taskResultStore{a.hub.Cfg.Results, a.set.TraceStore()}, Keys: webKeys, KeyErrs: webKeyErrs, Logf: a.logf, MCPLogDir: filepath.Join(a.paths.LogsDir(), "mcp"),
		ParentValid: func(ctx context.Context, in kernel.Invocation) bool { return a.hub.CheckInvocation(ctx, in) == nil }, ParentContext: a.hub.InvocationContext, Notify: a.hub.NotifyMaterials,
		ParentWorkspace: a.hub.InvocationWorkspace,
		ApproveRead: func(ctx context.Context, access ecosystem.Access, path string) error {
			s := access.Scope()
			in := kernel.InvocationFromContext(ctx)
			// 读资料的审批按这一轮冻结的目录判“目录内”，不用全局默认
			ws, err := a.hub.InvocationWorkspace(ctx, in)
			if err != nil {
				return fmt.Errorf("资料读取未获准：找不到这一轮的工作目录：%w", err)
			}
			blocked, reason := a.approvalGate.Check(ctx, ws.CWD, s.CharacterID, policy.Audience{Channel: "console", PrivateOk: true}, s.Conversation, approval.Call{Identity: in, ID: "source:" + ecosystem.ID(), Name: "read", Args: jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.String, Str: path}}}})
			if blocked {
				return fmt.Errorf("资料读取未获准：%s", reason)
			}
			return nil
		},
		ApproveMCP: func(ctx context.Context, target *mcpclient.ResolvedCall) approval.Clearance {
			in := kernel.InvocationFromContext(ctx)
			return a.approvalGate.CheckMCP(ctx, in.Scope.CharacterID, in.Scope.Conversation, in.ToolID, target)
		},
		DeliverSkill: func(_ turn.ToolCall, page skills.Page) (tools.Result, error) {
			return tools.Result{Text: page.Text, Controlled: true, Acceptable: page.Valid, Disposition: page.Disposition}, nil
		},
	}
	e, err := OpenEcosystem(ctx, c, ports)
	if err != nil {
		return err
	}
	a.ecosystem = e
	e.MCP.SetOnChange(a.refreshMCPAlert)
	e.MCP.SetLogf(a.logf)
	a.recoverMCPCalls(ctx, ports.Store)
	a.hub.Cfg.BindInput = a.bindKernelInput
	a.hub.Cfg.InternalAuthorize = a.authorizeInternal
	if e.Tasks != nil || a.cfg.Agent.Advisor.Enabled {
		a.hub.Cfg.Materials = a.kernelMaterials
	}
	if e.Skills != nil {
		a.hub.Cfg.ForgetContext = e.Skills.Forget
	}
	if results, ok := a.hub.Cfg.Results.(*artifact.Store); ok {
		results.SourceAllowed = func(scope kernel.Scope, name string) bool {
			return a.authorizeTool(context.Background(), scope, name) == nil
		}
	}
	if servers.Sumika.Enabled {
		for id, server := range servers.MCPServers {
			if !server.Sumika.Enabled {
				continue
			}
			if err := a.connectMCPServer(ctx, id, server); err != nil {
				// 一个 MCP 服务连不上不该拖垮整个后台：写日志、发提醒、跳过它（ADR-311：失败要查得到）
				a.logf("MCP 服务 %s 没连上，这次不用它：%v", id, err)
				if errors.Is(err, mcpclient.ErrQuarantined) {
					a.refreshMCPAlert(id) // 被隔离：提醒里写清怎么解除
				} else {
					a.alerts.Raise(alert.Alert{Key: "mcp:" + id, Class: "mcp", Title: "MCP 服务 " + id + " 没连上", Body: "这次启动跳过了它，其余照常：" + err.Error()})
				}
			}
		}
		if err := e.EnableMCP(ports); err != nil {
			return err
		}
		plugin := &mcpplugin.Plugin{Client: e.MCP, Host: host, Results: ports.Results, Approve: ports.ApproveMCP, ParentContext: ports.ParentContext, Lifetime: ports.Lifetime}
		a.hub.Cfg.Resolver = ecosystemResolver{plugin}
	}
	return nil
}

type ecosystemResolver struct{ mcp *mcpplugin.Plugin }

func (r ecosystemResolver) Prepare(ctx context.Context, call turn.ToolCall) (turn.PreparedCall, error) {
	if call.Name != "mcp_call" {
		return turn.PreparedCall{}, nil
	}
	return r.mcp.Prepare(ctx, call)
}

func (a *App) bindKernelInput(ctx context.Context, in kernel.Invocation) (kernel.Invocation, error) {
	if err := ctx.Err(); err != nil {
		return in, err
	}
	s := in.Scope
	// 话题线（规格-主动联系）永远不签本地授权：线里没有执行能力，转述主人的话也是 system 行，不是主人本人
	if audience.IsThread(s.Conversation) || s.Platform != "local" || s.Channel != "console" || s.Scene != "private" || s.ActorKind != "owner" || strings.Contains(s.Conversation, ":group:") {
		return in, nil
	}
	if s.AccountID == "" {
		s.AccountID = "desktop"
	}
	if s.AuthorityID != "" {
		_, err := a.authority.Resolve(s)
		return in, err
	}
	s.AuthorityID = "local:" + ecosystem.Digest(s)
	s.AuthorityRevision = 1
	in.Scope = s
	a.authorityMu.Lock()
	defer a.authorityMu.Unlock()
	if _, issued := a.authorityIssued[s.AuthorityID]; issued {
		if _, err := a.authority.Resolve(s); err == nil {
			return in, nil
		}
		// 普通聊天继续受理，撤销的来源身份不进入新回合，也不自动重签。
		in.Scope.AuthorityID, in.Scope.AuthorityRevision = "", 0
		return in, nil
	}
	// 来源授权仅随本宿主存活；撤权后不自动重签，重启也不恢复任务执行。
	_, err := a.authority.Issue(s, a.ecosystemCaps, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		a.authorityIssued[s.AuthorityID] = s
	}
	return in, err
}

func (a *App) authorizeTool(ctx context.Context, scope kernel.Scope, name string) error {
	access, err := a.authority.Resolve(scope)
	if err != nil {
		return err
	}
	capability := ""
	switch name {
	case "":
		return nil
	case "read", "ls", "grep", "find", "result_read":
		capability = "local.read"
	case "write", "edit", "checkpoint_restore":
		capability = "local.write"
	case "powershell", "process_output", "process_stop":
		capability = "local.exec"
	case "skill":
		capability = "skill:builtin"
	case "todo_write":
		capability = "local.write"
	case "clipboard", "notify":
		// 剪贴板读写和通知不碰文件，和 todo_write 一样归到 local.write（这里只拿到名字，分不开读写）
		capability = "local.write"
	case "open":
		capability = "local.exec"
	case "task", "task_result", "task_list", "task_message", "task_cancel":
		capability = "task:mailbox"
	case "memory_search", "recall_raw", "remember", "memory_forget", "read_link_detail":
		if strings.HasPrefix(scope.Conversation, "internal:") {
			return ecosystem.ErrDenied
		}
		return nil
	case "web_search", "wiki_lookup":
		capability = "web:search"
	case "web_fetch":
		capability = "web:fetch"
	default:
		if strings.HasPrefix(name, "mcp_") {
			for _, cap := range a.ecosystemCaps {
				if strings.HasPrefix(cap, "mcp:") && access.Check(ctx, cap) == nil {
					return nil
				}
			}
		}
		return ecosystem.ErrDenied
	}
	return access.Check(ctx, capability)
}

func (a *App) authorizeInternal(ctx context.Context, in kernel.Invocation, name string) error {
	if in.Kind != kernel.KindChild {
		return ecosystem.ErrDenied
	}
	return a.authorizeTool(ctx, in.Scope, name)
}

func (a *App) ecosystemSecret(kind, name string) (string, error) {
	if kind == "env" {
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			return "", fmt.Errorf("生态环境凭据不可用")
		}
		return value, nil
	}
	if kind != "secret" {
		return "", fmt.Errorf("生态凭据引用类型无效")
	}
	secret, err := secrets.Open(a.paths.SecretsFile())
	if err != nil {
		return "", fmt.Errorf("生态密钥不可用：%w", err)
	}
	value, ok := secret.Get(name)
	if !ok || value == "" {
		return "", fmt.Errorf("生态密钥不存在")
	}
	return value, nil
}

func (a *App) kernelMaterials(ctx context.Context, in kernel.Invocation) ([]turn.Msg, error) {
	if err := a.checkpointMaterials(ctx, in); err != nil {
		return nil, err
	}
	if a.ecosystem.Tasks != nil {
		access, err := a.authority.Resolve(in.Scope)
		if err == nil && access.Check(ctx, "task:mailbox") == nil {
			pending, err := a.ecosystem.Tasks.Take(ctx, access, in)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(pending))
			for _, result := range pending {
				raw, err := json.Marshal(result)
				if err != nil {
					return nil, err
				}
				if err := a.set.TraceStore().PutKernelMaterial(ctx, in, store.KernelMaterial{ID: "task:" + result.TaskID, Kind: "child", Text: string(raw)}); err != nil {
					return nil, err
				}
				ids = append(ids, result.TaskID)
			}
			if len(ids) > 0 {
				if err := a.ecosystem.Tasks.Commit(ctx, access, in, ids); err != nil {
					return nil, err
				}
			}
		}
	}
	materials, err := a.set.TraceStore().KernelMaterials(ctx, in)
	if err != nil {
		return nil, err
	}
	result := make([]turn.Msg, 0, len(materials))
	for _, material := range materials {
		result = append(result, turn.Msg{Role: "user", Kind: turn.KindContext, Scope: in.Scope.ScopeID, MaterialID: material.ID, Text: turn.ContextHeader + "\n任务资料（作者 " + material.Kind + "，不提供权限）：" + material.Text})
	}
	return result, nil
}

// child 的最终结论拥有独立宿主步骤，不与已返回的 task 调用争抢结果预留。
type taskResultStore struct {
	kernel.ResultStore
	trace *store.Store
}

func (s taskResultStore) Put(ctx context.Context, in kernel.Invocation, media string, body io.Reader, complete bool) (kernel.ResultRef, error) {
	if !strings.HasPrefix(in.ToolID, "task-result:") {
		return s.ResultStore.Put(ctx, in, media, body, complete)
	}
	if err := s.trace.StepStart(store.Step{ID: in.ToolID, CharacterID: in.Scope.CharacterID, Conversation: in.Scope.Conversation, TurnID: in.RunID, Kind: "tool", Name: "task_result", Status: "running", StartedAt: time.Now().UnixMilli(), Identity: in}); err != nil {
		return kernel.ResultRef{}, err
	}
	ref, err := s.ResultStore.Put(ctx, in, media, body, complete)
	output := ref.ID
	if err != nil {
		output = err.Error()
	}
	return ref, errors.Join(err, s.trace.StepEnd(in.ToolID, output, err != nil, time.Now().UnixMilli(), nil))
}
