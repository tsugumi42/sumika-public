package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/clientinfo"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/settingsreg"
	"sumika-native/internal/settingtool"
	"sumika-native/internal/turn"
)

func (a *App) openClientInfo() error {
	return a.hub.Use(&clientinfo.Plugin{Host: clientinfo.Host{Visible: clientInfoVisible, Read: a.inspectClient}})
}
func clientInfoVisible(aud audience.Audience) bool {
	return aud.Scene == "private" && aud.PrivateOK && aud.OwnerAsked && !strings.Contains(aud.Conversation, ":group:") && (aud.Channel == "console" || aud.Channel == "qq")
}
func validClientQQID(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

// owner is a normalized actor ID. The actual QQ account is checked against the
// trusted conversation peer and current host configuration, never a tool argument.
func (a *App) clientInfoOwner(in kernel.Invocation, aud audience.Audience) bool {
	s := in.Scope
	if !clientInfoVisible(aud) || in.Kind != kernel.KindForeground || in.ParentRunID != "" || s.ActorKind != "owner" || s.ActorID != "owner" || s.CharacterID == "" || s.ScopeID == "" || s.Conversation != aud.Conversation || s.Channel != aud.Channel || s.Scene != aud.Scene {
		return false
	}
	if s.Channel == "console" {
		return s.Platform == "local" && s.AccountID == "desktop" && strings.HasPrefix(s.Conversation, "console:") && a.authority != nil && a.localClientAuthority(s)
	}
	conv, err := types.ParseConversation(s.Conversation)
	owner := string(a.cfg.QQ.Owner)
	return err == nil && s.Platform == "qq" && conv.Channel == "qq" && conv.Scene == "private" && validClientQQID(owner) && validClientQQID(a.account) && owner != a.account && s.AccountID == a.account && conv.Self == a.account && conv.ID == owner && s.CharacterID == a.character
}
func (a *App) localClientAuthority(s kernel.Scope) bool {
	_, err := a.authority.Resolve(s)
	return err == nil
}

func (a *App) inspectClient(ctx context.Context, in kernel.Invocation, aud audience.Audience, manage bool) (clientinfo.View, error) {
	if err := a.hub.CheckInvocation(ctx, in); err != nil {
		return clientinfo.View{}, err
	}
	if a.shutDowned.Load() || in.Scope.Conversation != aud.Conversation || in.Scope.Channel != aud.Channel || in.Scope.Scene != aud.Scene || in.Kind != kernel.KindForeground {
		return clientinfo.View{}, errors.New("客户端查询身份无效")
	}
	if manage && !a.clientInfoOwner(in, aud) {
		return clientinfo.View{}, errors.New("客户端查询未授权")
	}
	models, links := a.currentModels(), a.currentLinks()
	v := clientinfo.View{Capabilities: a.clientCapabilities(in.Scope, aud, links)}
	if !manage {
		v.Version = fmt.Sprintf("%x", sha256.Sum256([]byte(models.Version+"|"+links.Version)))
		return v, nil
	}
	saved, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return v, errors.New("设置读取失败")
	}
	// Fresh configuration may revoke the owner while the process retains its startup snapshot.
	if in.Scope.Channel == "qq" && saved.QQ.Owner != a.cfg.QQ.Owner {
		return v, errors.New("所有者身份已变更")
	}
	if in.Scope.Channel == "qq" && !slices.ContainsFunc(saved.QQ.Accounts, func(account config.QQAccount) bool {
		return string(account.ID) == a.account && account.Character == in.Scope.CharacterID
	}) {
		return v, errors.New("接收账号绑定已变更")
	}
	v.ConfigVersion = saved.Version
	// Read the SAME registry as the desktop; do not read cards or desktop files just
	// to throw their private content away. Filter values before serializing.
	values, err := settingsreg.Read(saved, nil, "")
	if err != nil {
		return v, errors.New("设置注册表读取失败")
	}
	running := a.clientRunningConfig(models, links)
	active, err := settingsreg.Read(running, nil, "")
	if err != nil {
		return v, errors.New("运行设置读取失败")
	}
	activeValues := map[string]any{}
	for _, x := range active {
		activeValues[x.Key] = x.Value
	}
	for _, x := range values {
		if !clientSettingAllowed(x.Key, in.Scope.CharacterID, in.Scope.Conversation) {
			continue
		}
		rv := a.cfg.Version
		if strings.HasPrefix(x.Key, "linkReader.") {
			rv = links.Version
		} else if strings.HasPrefix(x.Key, "agent.") || strings.HasPrefix(x.Key, "characters.") || strings.HasPrefix(x.Key, "defaults.") {
			rv = models.Version
		}
		key := x.Key
		known := a.opt.Mode == Receive
		if (strings.HasSuffix(x.Key, ".background") || x.Key == "defaults.backgroundModel" || x.Key == "defaults.backgroundThinking") && len(models.ApplyErrors) > 0 {
			known = false
		}
		capability := "client"
		if strings.HasPrefix(x.Key, "linkReader.") {
			capability = "link-reader"
		}
		s := clientinfo.Setting{Capability: capability, Default: x.Default, Options: x.Options, Min: x.Min, Max: x.Max, MinExclusive: x.MinExclusive, MaxExclusive: x.MaxExclusive, Key: key, Name: x.Name, Description: x.Description, Type: x.Type, Effect: x.Effect, Source: x.Source, Configured: x.Value, Running: activeValues[x.Key], RunningKnown: known, RunningVersion: rv, Writable: false, Reason: "当前阶段只读；尚未提供设置写入工具"}
		if _, writable := settingToolSpec(x.Key); writable {
			s.Writable, s.Reason = true, "可以用 preview_setting_change 提议修改，所有者在审批卡上确认后才写入；每次改动有记录，能回滚"
		}
		if x.Key == "location" {
			// 所在地每次调用时重读配置，保存了就是运行值（生效“立即”），不看启动时的快照
			s.Capability, s.Running, s.RunningVersion = "life-tools", x.Value, saved.Version
		}
		if !known {
			s.Running = nil
		}
		v.Settings = append(v.Settings, s)
	}
	slices.SortFunc(v.Settings, func(a, b clientinfo.Setting) int { return strings.Compare(a.Key, b.Key) })
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	v.Version = fmt.Sprintf("%x", sha256.Sum256(raw))
	if err := ctx.Err(); err != nil {
		return clientinfo.View{}, err
	}
	return v, nil
}

// This is only an exposure allowlist. Names, types, defaults, explanations and
// configured values belong to settingsreg, not a parallel field table.
func clientSettingAllowed(key, character, conv string) bool {
	if strings.HasPrefix(key, "characters."+character+".") {
		return key == "characters."+character+".model" || key == "characters."+character+".background"
	}
	switch key {
	case "location", "defaults.backgroundModel", "defaults.backgroundThinking", "agent.thinking.local", "agent.thinking.private", "agent.thinking.group",
		"agent.context.window", "agent.context.reserve", "agent.context.softRatio", "agent.context.hardRatio", "agent.context.keepTokens", "agent.context.autoCompactChat",
		"linkReader.context.enabled", "linkReader.expand.enabled", "linkReader.expand.maxLinks", "linkReader.expand.bareBiliID", "linkReader.expand.rotateOnReject", "linkReader.expand.panoramaOriginals", "linkReader.expand.imageMaxMB", "linkReader.expand.video.enabled", "linkReader.expand.video.timeoutSeconds", "linkReader.expand.video.maxMB", "linkReader.expand.privateStatus":
		return true
	}
	return false
}
func (a *App) clientRunningConfig(models *ModelRuntime, links *LinkRuntime) config.Config {
	c := a.cfg
	c.Characters = maps.Clone(c.Characters)
	m := models.Values
	for id, x := range m.Characters {
		ch := c.Characters[id]
		ch.Model = x.Model
		ch.Background = x.Background
		ch.FallbackModels = slices.Clone(x.FallbackModels)
		c.Characters[id] = ch
	}
	c.Defaults.BackgroundModel = m.BackgroundModel
	c.Defaults.FallbackModels = slices.Clone(m.FallbackModels)
	c.Defaults.BackgroundThinking = m.BackgroundThinking
	c.Agent.Thinking.Local = m.Local
	c.Agent.Thinking.Private = m.Private
	c.Agent.Thinking.Group = m.Group
	c.Agent.Context.Window = m.Context.Window
	c.Agent.Context.Reserve = m.Context.Reserve
	c.Agent.Context.SoftRatio = m.Context.SoftRatio
	c.Agent.Context.HardRatio = m.Context.HardRatio
	c.Agent.Context.KeepTokens = m.Context.KeepTokens
	c.Agent.Context.AutoCompactChat = m.Context.AutoCompactChat
	c.LinkReader = links.Values.Clone()
	return c
}
func (a *App) clientCapabilities(s kernel.Scope, aud audience.Audience, links *LinkRuntime) []clientinfo.Capability {
	descriptions := []struct{ id, description string }{
		{"client", "Sumika 客户端的模型、思考与上下文设置"},
		{"memory", "检索和整理角色记忆；写入仍受独立许可限制"},
		{"link-reader", "读取链接资料；自动展开独立于角色回复"},
		{"repeater", "在允许的群会话自动跟随复读"}, {"poke", "在允许的 QQ 会话自动戳回去"},
		{"skills", "按需读取技能说明"}, {"web", "搜索和抓取网页"}, {"subagent", "创建和查询子任务"}, {"projectcontext", "读取可信工作区说明"}, {"mcp", "调用配置允许的 MCP 工具"},
		{"channel-responder", "把渠道消息交给角色并处理回合结果"}, {clientinfo.ID, "查询客户端能力与设置，只读"}, {settingtool.ID, "提议修改设置、所有者确认后写入、查改动记录、回滚"},
	}
	registered := a.hub.RegisteredPlugins()
	for _, id := range registered {
		if !slices.ContainsFunc(descriptions, func(d struct{ id, description string }) bool { return d.id == id }) {
			descriptions = append(descriptions, struct{ id, description string }{id, "宿主已注册；尚无用途说明"})
		}
	}
	// 关着的插件也列出来（她能看到装了哪些，设置页能再打开）；没有静态说明的用清单里的一行说明
	closed := map[string]bool{}
	for _, st := range a.hub.PluginStatuses() {
		if st.State != "off" {
			continue
		}
		closed[st.ID] = true
		if !slices.ContainsFunc(descriptions, func(d struct{ id, description string }) bool { return d.id == st.ID }) {
			desc := st.Sub
			if desc == "" {
				desc = "宿主已注册；尚无用途说明"
			}
			descriptions = append(descriptions, struct{ id, description string }{st.ID, desc})
		}
	}
	out := make([]clientinfo.Capability, 0, len(descriptions))
	conv, convErr := types.ParseConversation(s.Conversation)
	for _, d := range descriptions {
		on := slices.Contains(registered, d.id)
		c := clientinfo.Capability{AvailabilityKnown: true, ID: d.id, Description: d.description, State: "未注册", Reason: "未启用或未装配；没有健康探测结果"}
		enabled, hasSwitch := map[string]bool{"skills": a.cfg.Skills.Enabled, "web": a.cfg.Web.Enabled, "subagent": a.cfg.Subagents.Enabled, "projectcontext": a.cfg.ProjectContext.Enabled}[d.id]
		if hasSwitch && !enabled {
			c.State = "已关闭"
			c.Reason = "启动配置未启用"
		}
		if closed[d.id] {
			c.State = "已关闭"
			c.Reason = "插件开关关着；设置页打开后再用"
		}
		if on {
			c.State = "已注册"
			c.Available = true
			c.Reason = "已装配，不表示每次调用都能成功"
		}
		switch d.id {
		case "client":
			c.State = "已装配"
			c.Available = clientInfoVisible(aud)
			c.Reason = "设置查询只读，执行时验证所有者身份"
		case "link-reader":
			c.Available = on && links.Values.Context.Enabled
			c.Reason = fmt.Sprintf("给角色的链接资料=%t；本会话自动展开=%t；实际发送另受许可检查", c.Available, convErr == nil && s.Channel == "qq" && links.Values.Expand.Allows(conv))
		case "repeater", "poke":
			c.State = "已装配"
			c.Available = false
			c.Reason = "当前会话不适用或未开启"
			if convErr == nil && s.Channel == "qq" && a.adapter != nil {
				enabled, set := a.cfg.PluginSwitch(d.id, s.Conversation)
				if !set {
					enabled = d.id == "poke" && s.Scene == "private"
				}
				c.Available = enabled && (d.id == "poke" || s.Scene == "group") && a.modeOf(conv) == channel.ModeSpeak
				if d.id == "poke" && s.Scene == "private" {
					c.Available = c.Available && a.qqPrivateReplyOK(conv) && a.roster.CanTrigger("qq", a.roster.Resolve("qq", conv.ID).ID)
				}
			}
		case "projectcontext":
			c.Available, c.Reason = a.projectContextState(s)
		case "skills", "web", "subagent", "mcp":
			c.Available = on && s.Channel == "console" && s.Scene == "private" && a.clientInfoOwner(kernel.Invocation{Scope: s, Kind: kernel.KindForeground}, aud)
			if !c.Available {
				c.Reason = "未启用，或当前场合不开放此能力"
			}
		case clientinfo.ID:
			c.Available = on && clientInfoVisible(aud)
			if !c.Available {
				c.Reason = "管理查询只给所有者本地私聊和 QQ 私聊"
			}
		}
		out = append(out, c)
	}
	return out
}

// projectContextState 核验的是"这个会话的工作目录是否在信任根里"：不在就一个说明文件也不会读，而且模型这边看不出来。
// 只说本地私聊；真正的读取仍由 projectcontext 插件逐次核对信任和身份。
func (a *App) projectContextState(s kernel.Scope) (bool, string) {
	switch {
	case !a.cfg.ProjectContext.Enabled:
		return false, "启动配置未启用"
	case s.Channel != "console" || s.Scene != "private":
		return false, "只在本地私聊读取工作区说明"
	case a.projectTrust == nil:
		return false, "项目说明没有开启"
	}
	cwd, err := a.workspaceFor(context.Background(), s.Conversation)
	if err != nil {
		return false, "工作目录用不了：" + err.Error()
	}
	if cwd == "" {
		cwd = a.workdir()
	}
	ws, err := turn.NewWorkspace(s.Conversation, cwd)
	if err != nil {
		return false, "工作目录无法确认：" + err.Error()
	}
	trusted, err := a.projectTrust.Trusted(context.Background(), ws)
	switch {
	case err != nil:
		return false, "信任根无法确认：" + err.Error()
	case !trusted:
		return false, "工作目录 " + ws.Root + " 还没有信任，工作区说明（AGENTS.md 等）不会读取；里面有说明文件时，在本地聊天里开始对话会问所有者一次，答“记住”就不再问（也可以手写 projectContext.trustedRoots）"
	}
	return true, "工作目录 " + ws.Root + " 已经信任，开始对话和读写其中文件时会带上说明文件"
}
