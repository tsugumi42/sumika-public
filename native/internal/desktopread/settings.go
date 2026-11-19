package desktopread

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/settingsreg"
)

// SettingsReader 每次显式读取配置；Running 由装配方给出已经启用的实际安全值。
// 构造后 Running 不再修改，不读取密钥文件，不为设置页启动任何服务。
type SettingsReader struct {
	Load    func(context.Context) (config.Config, error)
	Running map[string]any
}

// settingValue 是投影里的一项。“已启用”不再手写：它由注册表的生效方式推出来（registryLive），
// 设置页、Agent 的设置工具、自知投影对同一个键说同一句“生不生效”。
type settingValue struct {
	key   string
	value any
}

// writeListDisplay：`*` 表示所有角色，不把通配符原样给用户看
func writeListDisplay(values []string) []string {
	for _, v := range values {
		if v == "*" {
			return []string{"全部角色"}
		}
	}
	return values
}

// registryLive 说这个键有没有功能在读它：注册表里标了“尚未生效”的、或根本没登记的，都不算已启用。
func registryLive(key string) bool {
	e, ok := settingsreg.Lookup(key)
	return ok && !e.Inert()
}

func (r SettingsReader) ReadSettings(ctx context.Context) (api.Result[api.Settings], error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return api.Result[api.Settings]{State: api.Failed, Err: err}, err
	}
	if r.Load == nil {
		err := errors.New("配置读取尚未接入")
		return api.Result[api.Settings]{State: api.Unsupported, Err: err}, err
	}
	c, err := r.Load(ctx)
	if err != nil {
		err = errors.New("设置配置读取失败")
		return api.Result[api.Settings]{State: api.Failed, Err: err}, err
	}
	values := []settingValue{
		{"magpie.baseUrl", safeBaseURL(c.Magpie.BaseURL)},
		{"defaults.backgroundModel", c.Defaults.BackgroundModel}, {"defaults.backgroundThinking", string(c.Defaults.BackgroundThinking)},
		{"agent.thinking.local", string(c.Agent.Thinking.Local)}, {"agent.thinking.private", string(c.Agent.Thinking.Private)}, {"agent.thinking.group", string(c.Agent.Thinking.Group)},
		{"agent.context.window", c.Agent.Context.Window}, {"agent.context.reserve", c.Agent.Context.Reserve}, {"agent.context.softRatio", c.Agent.Context.SoftRatio}, {"agent.context.hardRatio", c.Agent.Context.HardRatio}, {"agent.context.keepTokens", c.Agent.Context.KeepTokens}, {"agent.context.topicCompactAt", c.Agent.Context.TopicCompactAt}, {"agent.context.autoCompactChat", c.Agent.Context.AutoCompactChat},
		{"memory.idleMinutes", c.Memory.IdleMinutes}, {"memory.batchEvents", c.Memory.BatchEvents}, {"memory.topK", c.Memory.TopK}, {"memory.maxChars", c.Memory.MaxChars},
		// 重排和嵌入后端（规格-嵌入不依赖外部程序）：Go 读这几项，所以是已启用；modelsRoot、modelsExe、ortLibrary 是本机路径，不进投影
		{"memory.rerank.model", c.Memory.Rerank.Model}, {"memory.rerank.k", c.Memory.Rerank.K}, {"memory.rerank.timeoutSeconds", c.Memory.Rerank.TimeoutSeconds}, {"embed.backend", c.Embed.Backend},
		{"memory.write.characters", writeListDisplay(c.Memory.Write.Characters)}, {"memory.write.summarize", writeListDisplay(c.Memory.Write.Summarize)}, {"memory.write.places", c.Memory.Write.Places}, {"memory.write.catchUp", c.Memory.Write.CatchUp},
		{"memory.write.autoConfirm", c.Memory.Write.AutoConfirm}, {"memory.write.confirmOccasions", c.Memory.Write.ConfirmOccasions}, {"memory.write.confirmKinds", c.Memory.Write.ConfirmKinds}, {"memory.write.quarantineSince", c.Memory.Write.QuarantineSince},
		{"expression.learnGroups", c.Expression.LearnGroups}, {"expression.learnPrivate", c.Expression.LearnPrivate}, {"expression.learnHome", c.Expression.LearnHome}, {"expression.autoApprove", c.Expression.AutoApprove}, {"expression.reviewHours", c.Expression.ReviewHours}, {"expression.reviewBatches", c.Expression.ReviewBatches},
		{"vision.enabled", c.Vision.Enabled}, {"vision.model", c.Vision.Model}, {"vision.concurrency", c.Vision.Concurrency}, {"vision.backfillLanes", c.Vision.BackfillLanes}, {"vision.direct", c.Vision.Direct}, {"vision.directMax", c.Vision.DirectMax},
		{"tools.workdir", c.Tools.Workdir}, {"tools.approvalMinutes", c.Tools.ApprovalMinutes}, {"tools.approvalMode", c.Tools.ApprovalMode}, {"tools.lineEnding", c.Tools.LineEnding}, {"tools.autoApproveDirs", c.Tools.AutoApproveDirs},
		{"backup.dir", c.Backup.Dir}, {"backup.daily", c.Backup.Daily}, {"backup.dailyMedia", c.Backup.DailyMedia}, {"backup.alertGB", c.Backup.AlertGB}, {"backup.minFreeGB", c.Backup.MinFreeGB}, {"backup.runMaxHours", c.Backup.RunMaxHours}, {"backup.closeWaitSeconds", c.Backup.CloseWaitSeconds},
		{"plugins.sessions", PluginSettings(&c)}, {"reply.deny", DenySettings(&c)},
		{"notifications.goal", c.Notifications.Goal}, {"notifications.loop", c.Notifications.Loop}, {"notifications.approval", c.Notifications.Approval}, {"notifications.question", c.Notifications.Question}, {"notifications.reminder", c.Notifications.Reminder},
		{"qq.remoteApproval.enabled", c.QQ.RemoteApproval.Enabled}, {"qq.remoteApproval.awayAfterSeconds", c.QQ.RemoteApproval.AwayAfterSeconds},
	}
	var ids []string
	for id := range c.Characters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		models := c.Characters[id]
		prefix := "characters." + id + "."
		values = append(values, settingValue{prefix + "model", models.Model}, settingValue{prefix + "topic", models.Topic}, settingValue{prefix + "background", models.Background})
		values = append(values, settingValue{prefix + "aliases", models.Aliases})
	}
	// 账号只展示脱敏身份和规则数量；WS 地址、令牌和授权正文没有投影入口。
	var accounts []string
	for _, a := range c.QQ.Accounts {
		accounts = append(accounts, config.MaskID(string(a.ID))+" → "+a.Character)
	}
	values = append(values, settingValue{"qq.accounts", accounts}, settingValue{"qq.groups", strconv.Itoa(len(c.QQ.Groups)) + " 条群规则"}, settingValue{"qq.privateAllow", strconv.Itoa(len(c.QQ.PrivateAllow)) + " 条私聊规则"})
	var replacements []string
	for _, a := range c.QQ.Accounts {
		if a.Token != "" {
			replacements = append(replacements, a.Token, "[已隐藏]")
		}
		if a.ID != "" {
			replacements = append(replacements, string(a.ID), config.MaskID(string(a.ID)))
		}
	}
	for _, id := range append(append([]config.QQID{c.QQ.Owner}, c.QQ.Groups...), c.QQ.PrivateAllow...) {
		if id != "" {
			replacements = append(replacements, string(id), config.MaskID(string(id)))
		}
	}
	for person := range c.Reply.Deny {
		if id, ok := strings.CutPrefix(person, "qq:"); ok {
			replacements = append(replacements, id, config.MaskID(id))
		}
	}
	for conversation := range c.Plugins.Sessions {
		parts := strings.Split(conversation, ":")
		if parts[0] == "qq" {
			for _, id := range parts[2:] {
				replacements = append(replacements, id, config.MaskID(id))
			}
		}
	}
	redact := strings.NewReplacer(replacements...)
	sanitize := func(value any) any {
		switch v := value.(type) {
		case string:
			return redact.Replace(v)
		case []string:
			out := make([]string, len(v))
			for i, s := range v {
				out[i] = redact.Replace(s)
			}
			return out
		case bool, int, int64, float64, nil:
			return value
		default:
			return nil
		}
	}
	var out api.Settings
	for _, v := range values {
		source := "default"
		configured := c.Present[v.key]
		if configured {
			source = c.Source
			if source == "" {
				source = "config"
			}
		}
		setting := api.Setting{Key: redact.Replace(v.key), Value: sanitize(v.value), Source: redact.Replace(source), Configured: configured, Supported: registryLive(v.key)}
		labelSetting(&setting, v.key, redact)
		live := registryLive(v.key)
		if running, ok := r.Running[v.key]; ok && live {
			if v.key == "magpie.baseUrl" {
				if text, ok := running.(string); ok {
					running = safeBaseURL(text)
				} else {
					running = nil
				}
			}
			setting.Effective = true
			setting.EffectiveValue = sanitize(running)
		}
		result := api.Result[api.Setting]{State: api.Ready, Value: setting}
		if !live {
			reason := "Go 尚未启用"
			if configured {
				reason = "配置存在，Go 尚未启用"
			}
			result.State = api.Unsupported
			result.Err = errors.New(reason)
		}
		out.Items.Items = append(out.Items.Items, result)
	}
	// 这些值只来自程序真正启用的界面、数据与宿主状态；缺来源时明确未接入。
	for _, key := range []string{"appearance.theme", "appearance.reducedMotion", "appearance.fontSize", "appearance.layout", "occupation", "models.capabilities", "tools.execution", "tools.uiApproval", "platform.selectedAccount", "platform.connection", "notifications.alerts", "notifications.autostart", "data.group", "data.run", "data.localPath", "data.qqPath", "data.mode", "data.size", "shortcuts.registered"} {
		setting := api.Setting{Key: key, Source: "runtime"}
		labelSetting(&setting, key, redact)
		result := api.Result[api.Setting]{State: api.Unsupported, Value: setting, Err: errors.New("尚未接入")}
		if v, ok := r.Running[key]; ok {
			safe := sanitize(v)
			if safe != nil {
				setting = api.Setting{Key: key, Value: safe, Source: "runtime", Effective: true, EffectiveValue: safe, Supported: true}
				labelSetting(&setting, key, redact)
				result = api.Result[api.Setting]{State: api.Ready, Value: setting}
			}
		}
		out.Items.Items = append(out.Items.Items, result)
	}
	if err := ctx.Err(); err != nil {
		return api.Result[api.Settings]{State: api.Failed, Err: err}, err
	}
	return api.Result[api.Settings]{State: api.Ready, Value: out}, nil
}

// labelSetting 填上中文名、说明、单位和取值的叫法；表里没有的键留空（测试会拦下漏写的）。
// 角色 id 来自配置，名字也过一遍脱敏。
func labelSetting(s *api.Setting, key string, redact *strings.Replacer) {
	l, ok := labelOf(key)
	if !ok {
		return
	}
	s.Name, s.Help, s.Unit, s.Choices = redact.Replace(characterTitle(key, l.name)), l.help, l.unit, l.choices
}

// URL 的认证信息和任意路径均不能借设置页显示出来，只投影服务端来源。
func safeBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "地址不可显示"
	}
	return u.Scheme + "://" + u.Host
}
