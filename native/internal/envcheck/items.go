package envcheck

import (
	"fmt"
	"sort"
	"strings"
)

// Secrets 是 env.secrets：密钥文件在不在、锁着还是解开、本机副本能不能用。
func Secrets(in SecretsInput) Item {
	it := Item{ID: "env.secrets", Group: "账号和密钥", Name: "密钥库"}
	if in.ReadErr != "" {
		it.State = Unknown
		it.Detail = "读密钥状态失败：" + firstLine(in.ReadErr)
		it.Next = "重启 Sumika 后重试；还不好就看日志"
		it.Affects = "QQ：不知道访问令牌能不能取到"
		return it
	}
	if !in.Exists {
		if in.NeedToken {
			it.State = Bad
			it.Detail = "密钥文件还没有，" + accountText(in.Account) + "的访问令牌没地方存"
			it.Next = "用 sumika-ctl secrets set qq.<号>.token 存访问令牌（文件会新建）"
			it.Affects = "QQ：账号拿不到访问令牌，连不上"
			return it
		}
		it.State = NA
		it.Detail = "没有密钥文件，现在也没有账号要访问令牌"
		it.Affects = "没有影响：现在没有用到密钥"
		return it
	}
	if !in.Unlocked {
		it.State = Bad
		it.Detail = "密钥库锁着"
		if n := firstLine(in.Note); n != "" {
			it.Detail += "：" + n
		}
		it.Next = "用 sumika-ctl secrets unlock 输入主密码或恢复码"
		it.Affects = "QQ：账号拿不到访问令牌，连不上"
		return it
	}
	if in.HasLocalCopy {
		it.State = OK
		it.Detail = "密钥库解开了，本机副本可用"
		it.Affects = "QQ：能取到访问令牌"
		return it
	}
	it.State = Warn
	it.Detail = "密钥库解开了，但这台电脑没有可用的本机副本"
	if n := firstLine(in.Note); n != "" {
		it.Detail += "：" + n
	}
	it.Next = "用 sumika-ctl secrets unlock 重新包一份本机副本，下次启动不用输密码"
	it.Affects = "启动：每次都要输主密码或恢复码"
	return it
}

// Platform 是 env.platform：开了 QQ，号、协议端地址、访问令牌是不是都齐了。
func Platform(in PlatformInput) Item {
	it := Item{ID: "env.platform", Group: "账号和密钥", Name: "QQ 账号"}
	if !in.Enabled {
		it.State = NA
		it.Detail = "这次没有开 QQ"
		it.Affects = "没有影响：QQ 没开"
		return it
	}
	if len(in.Accounts) == 0 {
		it.State = Bad
		it.Detail = "QQ 开着，但 qq.accounts 里没有账号"
		it.Next = "在设置 · 平台账号填号、协议端地址，再用 sumika-ctl secrets set qq.<号>.token 存令牌"
		it.Affects = "QQ：没有账号可连"
		return it
	}
	var problems []string
	for _, a := range in.Accounts {
		name := accountText(a.ID)
		if a.ID == "" {
			problems = append(problems, "有一个账号没写号")
			continue
		}
		if a.URL == "" {
			problems = append(problems, name+"没填协议端地址")
		}
		if !a.HasToken {
			problems = append(problems, name+"没有访问令牌")
		}
	}
	if len(problems) > 0 {
		it.State = Bad
		it.Detail = strings.Join(problems, "；")
		if in.Locked {
			it.Detail += "（密钥库锁着，令牌取不出来）"
			it.Next = "用 sumika-ctl secrets unlock 输入主密码或恢复码；再在设置 · 平台账号补齐，令牌用 sumika-ctl secrets set qq.<号>.token"
		} else {
			it.Next = "在设置 · 平台账号补齐；令牌用 sumika-ctl secrets set qq.<号>.token"
		}
		it.Affects = "QQ：这些账号连不上"
		return it
	}
	it.State = OK
	it.Detail = fmt.Sprintf("%d 个 QQ 账号都有号、地址和令牌", len(in.Accounts))
	it.Affects = "QQ：账号配置齐了"
	return it
}

// ModelsGateway 是 env.models.gateway：模型来源（Magpie 或直连）连不连得上。
func ModelsGateway(in GatewayInput) Item {
	it := Item{ID: "env.models.gateway", Group: "模型", Name: "模型来源"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "模型来源")
		it.Next = "启动接收后重查；模型来源地址在设置 · 模型里"
		it.Affects = "模型：不知道现在能不能用"
		return it
	}
	source := in.Source
	if source == "" {
		source = "配置里的地址"
	}
	if in.Err != "" {
		it.State = Bad
		it.Detail = "模型来源 " + source + " 连不上：" + firstLine(in.Err)
		it.Next = "打开 Magpie，或在设置 · 模型里换地址"
		it.Affects = "模型：聊天和后台阶段用不了"
		return it
	}
	if in.ReadAt.IsZero() {
		it.State = Unknown
		it.Detail = "还没有读到模型目录"
		it.Next = "打开 Magpie，或在设置 · 模型里换地址后重启 Sumika"
		it.Affects = "模型：不知道现在能不能用"
		return it
	}
	it.State = OK
	it.Detail = fmt.Sprintf("模型来源 %s，目录读到的：%s", source, in.ReadAt.Format("2006-01-02 15:04"))
	it.Affects = "模型：正常"
	return it
}

// ModelsCatalog 是 env.models.catalog：models.dev 模型资料库取没取到、是不是在用旧缓存。
func ModelsCatalog(in CatalogInput) Item {
	it := Item{ID: "env.models.catalog", Group: "模型", Name: "模型资料库"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "模型资料库")
		it.Next = "启动接收后重查"
		it.Affects = "模型：不知道资料库是不是新的"
		return it
	}
	if !in.Enabled {
		it.State = NA
		it.Detail = "models.dev 资料库关着（modelMeta.modelsDev.enabled: false）"
		it.Affects = "没有影响：资料库是关着的"
		return it
	}
	if in.Err != "" {
		it.State = Warn
		it.Detail = "资料库取不到：" + firstLine(in.Err)
		if in.Entries > 0 {
			it.Detail += fmt.Sprintf("；现在用旧缓存（%d 条）", in.Entries)
		}
		it.Next = "联网后重启 Sumika，或关掉 modelMeta.modelsDev.enabled"
		it.Affects = "模型：显示名和上下文参数可能过时"
		return it
	}
	if in.FromCache {
		it.State = Warn
		it.Detail = fmt.Sprintf("这次没联网成功，用的是缓存（%d 条）", in.Entries)
		it.Next = "联网后重启 Sumika，或关掉 modelMeta.modelsDev.enabled"
		it.Affects = "模型：显示名和上下文参数可能过时"
		return it
	}
	if in.FetchedAt.IsZero() {
		it.State = Warn
		it.Detail = "还没有模型资料缓存"
		it.Next = "联网后重启 Sumika，或关掉 modelMeta.modelsDev.enabled"
		it.Affects = "模型：显示名和上下文参数用默认值"
		return it
	}
	it.State = OK
	it.Detail = fmt.Sprintf("资料库 %d 条，读取于 %s", in.Entries, in.FetchedAt.Format("2006-01-02 15:04"))
	it.Affects = "模型：资料库正常"
	return it
}

// Embed 是 env.embed：本地嵌入模型起没起来。输入是 S13 算出来的结果，这里只呈递，不重判阈值。
func Embed(in CheckInput) Item {
	it := Item{ID: "env.embed", Group: "模型", Name: "本地嵌入"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "本地嵌入")
		it.Next = "启动接收后重查"
		it.Affects = "记忆：不知道能不能做语义检索"
		return it
	}
	return fromCheck(it, in, checkText{
		okDetail:      "本地嵌入就绪",
		badDetail:     "本地嵌入没就绪",
		unknown:       "本地嵌入状态还不知道",
		next:          "把模型放到 embed.modelsRoot 或 <数据目录>/models 里，修好以后重启接收",
		badAffect:     "记忆：检索退回全文检索；发送前检查只剩原文比较",
		okAffect:      "记忆：语义检索正常",
		unknownAffect: "记忆：暂时只有全文检索",
	})
}

// Kernel 是 env.kernel：回合内核还在不在受理新输入。输入是 S10 算出来的结果，这里只呈递，不重判阈值。
func Kernel(in CheckInput) Item {
	it := Item{ID: "env.kernel", Group: "后台", Name: "回合内核"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "内核状态")
		it.Next = "启动接收后重查"
		it.Affects = "接收：不知道还能不能受理"
		return it
	}
	it = fromCheck(it, in, checkText{
		okDetail:      "内核在正常受理",
		badDetail:     "回合内核停止受理新输入",
		unknown:       "内核状态还不知道",
		next:          "运行 sumika-ctl kernel retry 重试；不行就重启 Sumika（启动时会核对）",
		badAffect:     "接收：停止受理新输入",
		okAffect:      "接收：正常",
		unknownAffect: "接收：不知道还能不能受理",
	})
	if it.State == Bad {
		if scope := in.Example["scope"]; scope == "conversation" && in.Example["conversation"] != "" {
			it.Detail = "会话 " + in.Example["conversation"] + " 停止受理新输入"
			it.Affects = "接收：这个会话停止受理新输入，别的会话照常"
		}
		if kind := in.Example["kind"]; kind != "" {
			it.Detail += "（" + kind + "）"
		}
	}
	return it
}

// Plugins 是 env.plugins：内置插件有没有启用失败的。
func Plugins(in PluginsInput) Item {
	it := Item{ID: "env.plugins", Group: "后台", Name: "内置插件"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "插件状态")
		it.Next = "启动接收后重查"
		it.Affects = "后台：不知道插件有没有起来"
		return it
	}
	if len(in.Failed) > 0 {
		first := in.Failed[0]
		it.State = Bad
		it.Detail = "插件 " + first.ID + " 启用失败"
		if e := firstLine(first.Err); e != "" {
			it.Detail += "：" + e
		}
		if len(in.Failed) > 1 {
			it.Detail += fmt.Sprintf("（共 %d 个插件失败）", len(in.Failed))
		}
		it.Next = "看日志；在设置 · 插件里关掉它，或修好配置后重启"
		it.Affects = "后台：这些插件提供的功能用不了"
		return it
	}
	it.State = OK
	it.Detail = "没有启用失败的插件"
	it.Affects = "后台：插件正常"
	return it
}

// MCP 是 env.mcp：有 MCP 服务被隔离、重连次数用完，或从没连上。
// 隔离和重连用完来自 S11 的结果；从没连上的由宿主另补（坑 11）。
func MCP(in MCPInput) Item {
	it := Item{ID: "env.mcp", Group: "后台", Name: "MCP 服务"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "MCP 状态")
		it.Next = "启动接收后重查"
		it.Affects = "MCP：不知道这些服务能不能用"
		return it
	}
	var parts []string
	var next []string
	if note := firstLine(in.Note); note != "" {
		parts = append(parts, note)
	}
	if in.Next != "" {
		next = append(next, in.Next)
	}
	// S11 读不到隔离记录时，健康状态里的“从没连上”分不清是真没连上还是被隔离：整项记 unknown，不报 bad。
	s11Unknown := in.State != "ok" && in.State != "bad"
	bad := in.State == "bad" || (!s11Unknown && len(in.NeverConnected) > 0)
	unknown := !bad && in.State != "ok"
	never := append([]NeverConnectedService(nil), in.NeverConnected...)
	sort.Slice(never, func(i, j int) bool { return never[i].Name < never[j].Name })
	if s11Unknown && len(never) > 0 {
		parts = append(parts, "读不到隔离记录，分不清是没连上还是被隔离")
	}
	for _, s := range never {
		detail := s.Name + "（从没连上"
		if c := firstLine(s.Cause); c != "" {
			detail += "，原因：" + c
		}
		parts = append(parts, detail+"）")
	}
	if len(never) > 0 && !s11Unknown {
		next = append(next, "从没连上的查这个服务的命令和参数")
	}
	switch {
	case bad:
		it.State = Bad
		it.Detail = strings.Join(parts, "；")
		if it.Detail == "" {
			it.Detail = "有 MCP 服务用不了"
		}
		if len(next) == 0 {
			next = append(next, "用 sumika-ctl mcp status 看原因；被隔离的用 mcp release <服务> --reason <原因> --accept-unknown-risk 解除，重连用完的用 mcp reconnect <服务>")
		}
		it.Next = strings.Join(next, "；")
		it.Affects = "MCP：这些服务用不了"
		return it
	case unknown:
		it.State = Unknown
		it.Detail = strings.Join(parts, "；")
		if it.Detail == "" {
			it.Detail = "MCP 状态还不知道"
		}
		if len(next) == 0 {
			next = append(next, "启动接收后重查；一直不知道就用 sumika-ctl mcp status 看")
		}
		it.Next = strings.Join(next, "；")
		it.Affects = "MCP：不知道这些服务能不能用"
		return it
	}
	it.State = OK
	it.Detail = strings.Join(parts, "；")
	if it.Detail == "" {
		it.Detail = "没有 MCP 服务被隔离、断线或从没连上"
	}
	if len(next) > 0 {
		it.Next = strings.Join(next, "；")
	}
	it.Affects = "MCP：正常"
	return it
}

// Analyzer 是 env.analyzer：命令分析器（PATH 里的 pwsh）起没起来。执行工具能退回 Windows PowerShell 5.1，是另一件事。
func Analyzer(in AnalyzerInput) Item {
	it := Item{ID: "env.analyzer", Group: "审批", Name: "命令分析器"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "命令分析器")
		it.Next = "启动接收后重查；或先确认 pwsh 在 PATH 里"
		it.Affects = "审批：不知道命令能不能先拆开看"
		return it
	}
	if in.OK {
		it.State = OK
		it.Detail = "命令分析器可用"
		if in.Path != "" {
			it.Detail += "（" + in.Path + "）"
		}
		it.Affects = "审批：能先拆开命令再判断"
		return it
	}
	it.State = Warn
	it.Detail = "命令分析器没起来"
	if e := firstLine(in.LastErr); e != "" {
		it.Detail += "：" + e
	} else if in.Path == "" {
		it.Detail += "：PATH 里没有 pwsh"
	}
	it.Next = "装 PowerShell 7（pwsh 进 PATH）后重启 Sumika"
	it.Affects = "审批：每条命令都会问你，检查点不拍"
	return it
}

// Backup 是 env.backup：每天备份和每周抽验的结果。输入是 S9、S12 算出来的结果，这里只合并呈递，不重判阈值。
func Backup(in BackupInput) Item {
	it := Item{ID: "env.backup", Group: "数据", Name: "备份和抽验"}
	if !in.Available {
		it.State = Unknown
		it.Detail = unavailableDetail(in.Reason, "备份状态")
		it.Next = "启动接收后重查"
		it.Affects = "数据：不知道备份是不是好的"
		return it
	}
	var parts, next []string
	bad, unknown := false, false
	for _, r := range []CheckResult{in.Daily, in.Drill} {
		if note := firstLine(r.Note); note != "" {
			parts = append(parts, note)
		}
		if r.Next != "" {
			next = append(next, r.Next)
		}
		switch r.State {
		case "bad":
			bad = true
		case "ok":
			// 正常：不报
		default:
			unknown = true
		}
	}
	switch {
	case bad:
		it.State = Bad
		it.Affects = "数据：换电脑或出错时这份备份不能恢复"
	case unknown:
		it.State = Unknown
		it.Affects = "数据：还不知道备份能不能恢复"
	default:
		it.State = OK
		it.Affects = "数据：备份和抽验正常"
	}
	it.Detail = strings.Join(parts, "；")
	if it.Detail == "" {
		if it.State == OK {
			it.Detail = "每天备份和每周抽验都正常"
		} else {
			it.Detail = "备份状态还不知道"
		}
	}
	if it.State != OK {
		if len(next) == 0 {
			next = append(next, "用 sumika-ctl backup list 看有没有校验通过的备份；需要的话用 backup now 重做一份")
		}
		it.Next = strings.Join(next, "；")
	}
	return it
}

// Nightly 是 env.nightly：每日整套只在开发机上有，产品里永远不适用。
func Nightly() Item {
	return Item{
		ID:      "env.nightly",
		Group:   "数据",
		Name:    "每日整套",
		State:   NA,
		Detail:  "只在开发机上有（P1-5）",
		Affects: "没有影响：产品运行不需要每日整套",
	}
}

// Tools 是 env.tools：登记表里的外部程序找到没有、找在哪。
func Tools(in ToolsInput) Item {
	it := Item{ID: "env.tools", Group: "外部程序", Name: "外部程序"}
	if !in.Checked {
		it.State = Unknown
		it.Detail = "还没有查外部程序"
		it.Next = "启动完成后重查"
		it.Affects = "外部程序：不知道缺不缺"
		return it
	}
	if len(in.Results) == 0 {
		it.State = Unknown
		it.Detail = "这次没有拿到外部程序的查找结果"
		it.Next = "重启 Sumika 后重查"
		it.Affects = "外部程序：不知道缺不缺"
		return it
	}
	var missing []string
	requiredMissing, optionalMissing := false, false
	it.Sub = make([]SubItem, 0, len(in.Results))
	for _, r := range in.Results {
		sub := SubItem{Name: r.Name}
		if r.Found {
			sub.State = OK
			sub.Detail = r.Path + "（" + sourceText(r.Source)
			if v := strings.TrimSpace(r.Version); v != "" {
				sub.Detail += "，" + v
			}
			sub.Detail += "）"
			it.Sub = append(it.Sub, sub)
			continue
		}
		sub.Detail = "没找到"
		if r.Purpose != "" {
			sub.Detail += "，影响：" + r.Purpose
		}
		sub.Next = toolNext(r.Name)
		if r.Required {
			sub.State = Bad
			requiredMissing = true
		} else {
			sub.State = Warn
			optionalMissing = true
		}
		missing = append(missing, r.Name)
		it.Sub = append(it.Sub, sub)
	}
	if len(missing) == 0 {
		it.Detail = fmt.Sprintf("登记了 %d 个外部程序，都找到了", len(in.Results))
	} else {
		it.Detail = fmt.Sprintf("登记了 %d 个外部程序，%d 个没找到", len(in.Results), len(missing))
	}
	if len(missing) > 0 {
		var next []string
		for _, name := range missing {
			next = append(next, toolNext(name))
		}
		it.Next = strings.Join(next, "；")
	}
	switch {
	case requiredMissing:
		it.State = Bad
		it.Affects = "外部程序：搜索等必需功能用不了"
	case optionalMissing:
		it.State = Warn
		it.Affects = "外部程序：个别可选功能用不了"
	default:
		it.State = OK
		it.Affects = "外部程序：都找到了"
	}
	return it
}

// Paths 是 env.paths：数据目录在哪，配置里有没有指向数据目录外的绝对路径（换电脑要改）。
func Paths(in PathsInput) Item {
	it := Item{ID: "env.paths", Group: "数据", Name: "数据位置"}
	if in.Root == "" {
		it.State = Unknown
		it.Detail = "不知道数据目录在哪"
		it.Next = "检查启动参数 -root 或 exe 旁边的 sumika.root"
		it.Affects = "数据：找不到数据目录"
		return it
	}
	if len(in.Outside) > 0 {
		it.State = Warn
		var parts []string
		for _, p := range in.Outside {
			parts = append(parts, p.Key+"="+p.Value)
		}
		it.Detail = fmt.Sprintf("配置里有 %d 个指向数据目录外的绝对路径：%s", len(in.Outside), strings.Join(parts, "；"))
		it.Next = "改成相对数据目录的路径，或换电脑后在设置里改"
		it.Affects = "数据：换电脑后这些路径指不到"
		return it
	}
	it.State = OK
	it.Detail = "数据目录：" + in.Root
	if in.Source != "" {
		it.Detail += "（" + in.Source + "）"
	}
	it.Affects = "数据：换电脑后仍能找到"
	return it
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// unavailableDetail 是一条运行部分项查不了时的详情：原因（后台没开、打开失败）在前，缺的是什么在后。
func unavailableDetail(reason, what string) string {
	reason = firstLine(reason)
	if reason == "" {
		reason = "后台没开"
	}
	return reason + "，查不了" + what
}

// checkText 是把一条自检结果翻成清单项时的兜底文字。
type checkText struct {
	okDetail, badDetail, unknown string
	next                         string
	okAffect, badAffect          string
	unknownAffect                string
}

// fromCheck 按自检结果的状态填清单项：note/next 原样呈递，缺的用兜底，保证 warn/bad/unknown 都有下一步。
func fromCheck(it Item, in CheckInput, t checkText) Item {
	detail := firstLine(in.Note)
	next := in.Next
	switch in.State {
	case "ok":
		it.State = OK
		if detail == "" {
			detail = t.okDetail
		}
		it.Affects = t.okAffect
	case "bad":
		it.State = Bad
		if detail == "" {
			detail = t.badDetail
		}
		it.Affects = t.badAffect
		if next == "" {
			next = t.next
		}
	default:
		it.State = Unknown
		if detail == "" {
			detail = t.unknown
		}
		it.Affects = t.unknownAffect
		if next == "" {
			next = t.next
		}
	}
	it.Detail = detail
	it.Next = next
	return it
}

// accountText 按 config.MaskID 的规则打码：只留后两位，两位以内全部隐藏。
func accountText(id string) string {
	r := []rune(strings.TrimSpace(id))
	if len(r) == 0 {
		return "配置里的 QQ 号"
	}
	if len(r) <= 2 {
		return "账号 ***"
	}
	return "账号 ***" + string(r[len(r)-2:])
}

func sourceText(source string) string {
	switch source {
	case "bundled":
		return "自带"
	case "path":
		return "PATH"
	case "config":
		return "配置"
	case "system":
		return "系统目录"
	}
	return source
}

func toolNext(name string) string {
	switch name {
	case "pwsh":
		return "装 PowerShell 7，把 pwsh 放进 PATH、<资源根>/vendor/bin 或 Program Files 后重启 Sumika"
	case "powershell":
		return "装 PowerShell（Windows 自带的 5.1 也可以）"
	case "rg":
		return "把 rg 放进 <资源根>/vendor/bin 或 PATH"
	case "fd":
		return "把 fd 放进 <资源根>/vendor/bin 或 PATH"
	case "ffmpeg":
		return "装 ffmpeg，放进 <资源根>/vendor/bin、PATH，或在 qq.media.ffmpeg 里填路径"
	case "ffprobe":
		return "装 ffprobe，放进 <资源根>/vendor/bin、PATH，或在 qq.media.ffprobe 里填路径"
	case "git":
		return "装 Git for Windows"
	case "taskkill", "rundll32":
		return "Windows 自带的系统程序，检查 SystemRoot 和 System32 是否完整"
	}
	return "装上它，或把路径填进配置"
}
