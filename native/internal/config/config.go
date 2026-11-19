// Package config 读取、校验配置，并通过白名单补丁写回设置。
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Thinking 是思考档位（off / minimal / low / medium / high / xhigh / max）
type Thinking string

var thinkingLevels = map[Thinking]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// QQID 是 QQ 号（照 daemon/config.ts 的 qqId：正整数或者 /^\d+$/ 的字符串，统一转成字符串）：号码大于 2^53 的
// 也不丢精度，不经过 float64。是不是数字串在 check 里查，好给出一项一项的说明
type QQID string

// UnmarshalYAML 按 YAML 的类型认（照 qqId）：整数转十进制字符串；浮点必须是大于 0 的整数值、不超过 2^53
// （Bun 里 1e2、100.0 都是 100）；字符串原样留着（是不是数字串在 check 里查）；布尔、null、列表、映射报错。
// 不带引号的整数先按原文自己看一遍：goccy 把前导零当八进制（0100 读成 64），Bun 的 yaml 是 1.2、读成 100
func (q *QQID) UnmarshalYAML(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if !quotedScalar(raw) {
		switch {
		case decimalInt.MatchString(raw):
			return q.setDecimal(raw)
		case strings.Contains(raw, "_"):
			// 1_000：Bun 的 yaml 按 1.2 读成字符串，过不了数字串的校验；这里也直接不认
			return unsupportedQQID(raw)
		}
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return unsupportedQQID(raw)
	}
	switch n := v.(type) {
	case int:
		return q.setInt(int64(n), raw)
	case int64:
		return q.setInt(n, raw)
	case uint64:
		if n == 0 {
			return positiveQQID(raw)
		}
		*q = QQID(strconv.FormatUint(n, 10))
		return nil
	case float64:
		return q.setFloat(n, raw)
	case string:
		// 不带小数点、只有指数的数（1e2）：goccy 按 YAML 1.1 当字符串，Bun 用的 yaml 按 1.2 当浮点
		if exponentFloat.MatchString(raw) {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return unsupportedQQID(raw)
			}
			return q.setFloat(f, raw)
		}
		*q = QQID(n)
		return nil
	}
	return unsupportedQQID(raw)
}

// setDecimal 收下不带引号的十进制整数原文（YAML 1.2 核心模式：可以带 + 和前导零）：去掉 + 和前导零以后按十进制解，
// 逐位不丢精度；全零（0、00）不是 QQ 号，超出 uint64 的也不是
func (q *QQID) setDecimal(raw string) error {
	digits := strings.TrimLeft(strings.TrimPrefix(raw, "+"), "0")
	if digits == "" {
		return positiveQQID(raw)
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return fmt.Errorf("QQ 号超出范围，实际是 %s", MaskID(raw))
	}
	*q = QQID(strconv.FormatUint(n, 10))
	return nil
}

// setInt 收下整数：必须大于 0（照 z.number().int().positive()）
func (q *QQID) setInt(n int64, raw string) error {
	if n <= 0 {
		return positiveQQID(raw)
	}
	*q = QQID(strconv.FormatInt(n, 10))
	return nil
}

// setFloat 收下浮点：必须大于 0、是整数值、不超过 2^53（再大的 float64 已经存不准，宁可报错也不给个错的号）
func (q *QQID) setFloat(f float64, raw string) error {
	if f <= 0 || f != math.Trunc(f) || f > 1<<53 {
		return positiveQQID(raw)
	}
	*q = QQID(strconv.FormatFloat(f, 'f', -1, 64))
	return nil
}

// exponentFloat 认不带小数点、只有指数的数（1e2、1E-2，YAML 1.2 的浮点写法）
var exponentFloat = regexp.MustCompile(`^[-+]?[0-9]+[eE][-+]?[0-9]+$`)

// decimalInt 认不带引号的十进制整数原文（YAML 1.2 核心模式：可以带前导零、可以带 +）
var decimalInt = regexp.MustCompile(`^\+?[0-9]+$`)

// quotedScalar 说原文是不是带引号的字符串（goccy 给 UnmarshalYAML 的原文带着引号）
func quotedScalar(raw string) bool {
	return strings.HasPrefix(raw, `"`) || strings.HasPrefix(raw, "'")
}

// unsupportedQQID 说这一项不是数字也不是数字串；原值打码，避免启动错误泄露号码。
func unsupportedQQID(raw string) error {
	return fmt.Errorf("QQ 号要写成数字或数字串，实际是 %s", MaskID(raw))
}

// positiveQQID 说这一项不是大于 0 的整数
func positiveQQID(raw string) error {
	return fmt.Errorf("QQ 号要写成正整数，实际是 %s", MaskID(raw))
}

// QQAccount 是一个 QQ 账号（qq.accounts 的一项）
type QQAccount struct {
	ID        QQID   `yaml:"id"`
	Character string `yaml:"character"`
	URL       string `yaml:"url"`   // 协议端的正向 WebSocket 地址；receive 选中的账号必填，不自动发现
	Token     string `yaml:"token"` // 访问令牌：明文、${环境变量}（连接时展开）或者空（密钥里）
}

// Group 是群聊闸门的默认参数（规格-群聊闸门第三、四节；某个群、某个时段在设置里另外调）
type Group struct {
	Frequency float64 `yaml:"frequency"` // 0 只有主人 @ 才回，1 不设门槛（ADR-061）
	Jitter    float64 `yaml:"jitter"`    // 门槛加的抖动（±），0 就是完全确定（ADR-077）
	Mode      string  `yaml:"mode"`      // 群的默认档位：off / dry / speak
}

// GateJitter 是 group.jitter 给闸门用的写法（channel.GateConfig.Jitter）：Bun 的 0 是"不抖"，
// 闸门的 0 却是"用默认 0.03"、负数才是不抖（native/internal/channel/gate.go 的 GateConfig），所以配置是 0 时给 -1
func (g Group) GateJitter() float64 {
	if g.Jitter == 0 {
		return -1
	}
	return g.Jitter
}

// GateFrequency 是 group.frequency 给闸门用的写法（channel.GateConfig.Frequency）：闸门的 0 是"用默认 0.3"，
// 而 Bun 的 0 是"只有主人 @ 才回"，所以配置是 0 时第二个返回值是 true：装配处要往 gate_rules 写一条 "*" 的 0，
// 光给 GateConfig.Frequency 0 会悄悄变成 0.3（gate.go 的 GateConfig 注释）
func (g Group) GateFrequency() (float64, bool) {
	return g.Frequency, g.Frequency == 0
}

// Embed 是 receive 模式的本地嵌入子进程；archive 不启动。只走自带的 sumika-models.exe（规格-嵌入不依赖外部程序）。
// 旧的 legacy 后端（借外部 Python 嵌入服务的 server.py）已删：旧配置里写着 legacy 时读配置不失败，由 embed.Local 报
// “已移除、改成 go-models”，嵌入不启动（规格第 2 条）。
type Embed struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"` // 没有默认值（ADR-098：不写死本机路径；规格-Go本地模型 2.8）
	// Backend 现在只有 go-models（空当 go-models）。legacy 不再是合法取值；它留在校验里只为让旧配置读得进来、
	// 由嵌入启动处给一句明白的原因（配置加载就报错的话，主人只会看到“配置坏了”，不知道要改哪一项）
	Backend string `yaml:"backend"`
	// 三项都是本机路径。ModelsRoot 空 = <数据目录>/models；ModelsExe 空 = <资源根>/sumika-models.exe（只认这一处，不搜索不回退）；
	// OrtLibrary 空 = sumika-models.exe 所在目录里的 onnxruntime.dll。写相对路径时相对数据目录解析（工作目录不固定，不许按它解析）
	ModelsRoot string `yaml:"modelsRoot"`
	ModelsExe  string `yaml:"modelsExe"`
	OrtLibrary string `yaml:"ortLibrary"`
}

// BackendGoModels 是嵌入唯一的后端（embed.BackendGoModels 引用这里，config 在 embed 下面）
const BackendGoModels = "go-models"

// CharacterModels 是每个角色用哪个模型（ADR-084：放配置，不放角色卡）
type CharacterModels struct {
	Model          string   `yaml:"model"`
	Topic          string   `yaml:"topic"`
	Background     string   `yaml:"background"`
	FallbackModels []string `yaml:"fallbackModels"`
	GroupReminder  bool     `yaml:"groupReminder"`
	Aliases        []string `yaml:"aliases"` // 主人填写的叫名表；空表不启用叫名（ADR-187）
}

// MemoryWrite 是记忆写入的开关和参数（规格-记忆写入上线 W5、ADR-377 第 1 条）。Characters 默认 ["*"]（所有角色的 remember 可写）；
// Summarize 空 = 不整理；CatchUp 没有默认，开后台整理前必须自己选。键在 sumika.example.yml 里逐个写了说明
type MemoryWrite struct {
	Characters       []string `yaml:"characters"`       // 允许写入的角色 id（主人私聊里的 remember）；"*" = 所有角色，空 = 全部只读
	Summarize        []string `yaml:"summarize"`        // 其中再允许后台整理的角色，必须也在 Characters 里（"*" 同样认）
	Places           []string `yaml:"places"`           // 允许整理的场合，取值见 WritePlaceNames；默认只有 owner-private
	CatchUp          string   `yaml:"catchUp"`          // 开后台整理前必选：latest | cursor；没有默认
	AutoConfirm      bool     `yaml:"autoConfirm"`      // 待确认攒够证据真的提升；false = 只记 would_confirm 计数
	ConfirmOccasions int      `yaml:"confirmOccasions"` // 攒几次证据算数，至少 2，默认 3（原来规格里写的 memory.confirmOccasions）
	ConfirmKinds     []string `yaml:"confirmKinds"`     // 哪几种事实攒够证据可以自动提升；默认 fact、preference，promise 和 boundary 要主人放行
	QuarantineSince  string   `yaml:"quarantineSince"`  // 止血：填启动日志"写入已开"那一行里的 summarize_enabled_at=（没开过整理填 write_enabled_at=，毫秒）后，那之后整理写的、整理提升的东西不再被召回；空 = 不隔离
}

// Remembers：这个角色能不能写 remember。名单里有 "*" 或这个 id 就行；空名单全部只读
func (w MemoryWrite) Remembers(id string) bool {
	return slices.Contains(w.Characters, "*") || slices.Contains(w.Characters, id)
}

// Summarizes：这个角色能不能做后台整理。名单里有 "*" 或这个 id 就行；空名单不整理
func (w MemoryWrite) Summarizes(id string) bool {
	return slices.Contains(w.Summarize, "*") || slices.Contains(w.Summarize, id)
}

// minQuarantineMillis：quarantineSince 最小认多大（2001-09-09 的毫秒数）。再小的只可能是填成了秒
const minQuarantineMillis = 1_000_000_000_000

// WritePlaceNames 是 memory.write.places 认的取值（和 memory.AllWritePlaces 同一组，memwrite 的 TestPlaceNamesAgree 钉住两边一致；config 是叶子包，不引 memory）
var WritePlaceNames = []string{"owner-private", "local-private", "local-group", "platform-private", "platform-group"}

// WriteKindNames 是 memory.write.confirmKinds 认的事实种类
var WriteKindNames = []string{"fact", "promise", "preference", "boundary"}

// Config 是第 0 步用得到的配置
type Config struct {
	Ecosystem `yaml:",inline"`
	// Present 只记录显式配置的键，不保存原始 YAML 或秘密值。
	Present map[string]bool `yaml:"-"`
	// RemovedKeys 是配置里还写着、但已经删掉的键（现在只有 embed.python、embed.serverDir）。读配置不因此失败，
	// 启动时由宿主各警告一次（规格-嵌入不依赖外部程序 第 2 条）；未知键照旧静默忽略
	RemovedKeys []string `yaml:"-"`
	// Notices 是读配置时要告诉用户的提示（不影响加载）：现在只有 providers.magpie 和 magpie.baseUrl 并存
	Notices []string `yaml:"-"`
	Source  string   `yaml:"-"`
	Version string   `yaml:"-"`
	Memory  struct {
		IdleMinutes float64 `yaml:"idleMinutes"`
		BatchEvents int     `yaml:"batchEvents"`
		TopK        int     `yaml:"topK"`
		MaxChars    int     `yaml:"maxChars"`
		// Rerank 是明确回忆（memory_search）要不要过一遍重排（规格-记忆写入上线 W1a）：Model 空 = 不重排；
		// K 是合并出前多少条交给重排；TimeoutSeconds 是重排自己的时限，超了这一轮按原顺序
		Rerank struct {
			Model          string  `yaml:"model"`
			K              int     `yaml:"k"`
			TimeoutSeconds float64 `yaml:"timeoutSeconds"`
		} `yaml:"rerank"`
		Write MemoryWrite `yaml:"write"`
	} `yaml:"memory"`
	Expression struct {
		LearnGroups   bool    `yaml:"learnGroups"`
		LearnPrivate  bool    `yaml:"learnPrivate"`
		LearnHome     bool    `yaml:"learnHome"`
		AutoApprove   bool    `yaml:"autoApprove"`
		ReviewHours   float64 `yaml:"reviewHours"`
		ReviewBatches int     `yaml:"reviewBatches"`
	} `yaml:"expression"`
	Vision struct {
		Enabled       bool   `yaml:"enabled"`
		Model         string `yaml:"model"`
		Concurrency   int    `yaml:"concurrency"`
		BackfillLanes int    `yaml:"backfillLanes"`
		Direct        bool   `yaml:"direct"`
		DirectMax     int    `yaml:"directMax"`
	} `yaml:"vision"`
	Backup struct {
		Dir        string  `yaml:"dir"`
		Daily      bool    `yaml:"daily"`
		DailyMedia string  `yaml:"dailyMedia"`
		AlertGB    float64 `yaml:"alertGB"`
		// MinFreeGB：备份前目标盘至少要剩多少 GB（还要至少是预计大小的 2 倍，取大的）；0 = 不查
		MinFreeGB float64 `yaml:"minFreeGB"`
		// RunMaxHours：一份备份最多做几小时，超过就取消并记失败
		RunMaxHours float64 `yaml:"runMaxHours"`
		// CloseWaitSeconds：退出时最多等正在做的备份收尾几秒，不无限等
		CloseWaitSeconds float64 `yaml:"closeWaitSeconds"`
	} `yaml:"backup"`
	// Watchdog 看门狗：心跳、卡死判定、重启次数上限（规格-看门狗）
	Watchdog Watchdog `yaml:"watchdog"`
	// Logs 日志级别和按模块覆盖（规格-日志分级）；默认 info
	Logs LogSettings `yaml:"logs"`
	// Selfcheck 运行时自检：数几条守恒和卡住的不变量，每天写一个 data/selfcheck/<日期>.json（规格-运行时自检）
	Selfcheck      Selfcheck      `yaml:"selfcheck"`
	ProjectContext ProjectContext `yaml:"projectContext"`
	Console        struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"console"`
	Magpie struct {
		BaseURL string `yaml:"baseUrl"`
	} `yaml:"magpie"`
	// Providers 是模型服务商（规格-模型配置，ADR-390）。读配置时没有 magpie 就在内存里补一条，
	// 不写回文件；M1 只读形状，请求仍走合成出来的 magpie
	Providers map[string]Provider `yaml:"providers"`
	// DefaultProvider 是裸模型引用归谁（规范写法是 服务商ID/模型ID）；没写就是 magpie
	DefaultProvider string                     `yaml:"defaultProvider"`
	DataDir         string                     `yaml:"dataDir"`
	CharactersDir   string                     `yaml:"charactersDir"`
	Characters      map[string]CharacterModels `yaml:"characters"`
	Defaults        struct {
		BackgroundModel    string   `yaml:"backgroundModel"`
		BackgroundThinking Thinking `yaml:"backgroundThinking"`
		FallbackModels     []string `yaml:"fallbackModels"`
	} `yaml:"defaults"`
	Tools struct {
		Workdir         string   `yaml:"workdir"` // 读写文件、跑命令的默认工作目录；空 = 用户目录
		ApprovalMinutes float64  `yaml:"approvalMinutes"`
		ApprovalMode    string   `yaml:"approvalMode"`
		LineEnding      string   `yaml:"lineEnding"` // write 新建文件的换行兜底：auto（Windows CRLF、别处 LF）/ lf / crlf；已有文件、.editorconfig、Git 规则排在它前面
		AutoApproveDirs []string `yaml:"autoApproveDirs"`
		Checkpoints     struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"checkpoints"`
	} `yaml:"tools"`
	// QQ 渠道（规格-QQ渠道）：receive 选中的账号必须填写 URL；archive 不要求账号 URL。
	// 路径只按配置读取或显式修改，不自动发现。
	QQ struct {
		Owner        QQID        `yaml:"owner"`
		Accounts     []QQAccount `yaml:"accounts"`
		Groups       []QQID      `yaml:"groups"`       // 群白名单
		PrivateAllow []QQID      `yaml:"privateAllow"` // 私聊白名单
		Reply        bool        `yaml:"reply"`        // QQ 私聊自动回复，默认关（ADR-037）
		// GoSend 是"把发送权交给 Go"的开关（规格-QQ发送上线 5.1 第 1 条）。默认 false，Bun 不读这一项；
		// 开了也只表示想交给 Go，还得意图文件是 go、拿到独占锁、交接记录是 protocol 1 的 released
		GoSend         bool   `yaml:"goSend"`
		QQDir          string `yaml:"qqDir"`
		QQDataDir      string `yaml:"qqDataDir"`
		ProtocolDir    string `yaml:"protocolDir"`
		ProtocolLogDir string `yaml:"protocolLogDir"`
		// Media 管收到的图片、表情和视频；关下载不影响消息入库。视频独立限额，工具路径先配置再找 PATH。
		Media struct {
			Download bool   `yaml:"download"`
			FFmpeg   string `yaml:"ffmpeg"`
			FFprobe  string `yaml:"ffprobe"`
		} `yaml:"media"`
		// ADR-239：空档默认都补，白名单群也补；没有接管时刻和最大年龄下限。
		Backfill struct {
			Enabled bool `yaml:"enabled"`
			Groups  bool `yaml:"groups"`
		} `yaml:"backfill"`
		Forward struct {
			Fetch bool `yaml:"fetch"`
		} `yaml:"forward"`
		// RemoteApproval 是主人 QQ 私聊审批（规格-主人QQ私聊审批）：默认关，关着时行为和没有这一项一样。
		// 打开后主人 QQ 私聊回复“同意”就能批准本机操作，也能在私聊里发起执行；账号被盗就能执行，所以默认关、保存后立即生效。
		RemoteApproval struct {
			Enabled bool `yaml:"enabled"`
			// AwayAfterSeconds：本地审批等了这么久主窗口还没人处理、人也不在，才发到 QQ；0 是一出单就发
			AwayAfterSeconds int `yaml:"awayAfterSeconds"`
		} `yaml:"remoteApproval"`
	} `yaml:"qq"`
	Group Group `yaml:"group"`
	// 主动联系（规格-主动联系）：默认关，真实配置由主人打开
	Proactive Proactive `yaml:"proactive"`
	// Location 是主人所在地（规格-主动联系 第十九节、规格-客户端自知 第十一节）：天气工具不传地点时用它；默认空，不进 git。
	// 主人本人的场合看完整的，主人开放的群只知道城市，别人的私聊当不知道（ADR-330 第 6 条）
	Location Location `yaml:"location"`
	// FreeAPI 是免费接口的设置（规格-免费接口）：探测、每个工具每人每群的次数、提供方上限
	FreeAPI FreeAPI `yaml:"freeApi"`
	// 提醒（规格-提醒）：她自己建、到点找主人；发 QQ 默认关
	Reminders Reminders `yaml:"reminders"`
	// Backend：本地私聊由 Go 还是 Bun 管（规格-本地私聊切到Bun）；默认 go
	Backend Backend `yaml:"backend"`
	// Notifications 是桌面系统通知按类的开关，默认全开
	Notifications Notifications `yaml:"notifications"`
	// Calendar 是日历工具的设置
	Calendar struct {
		// HolidayURL 是法定假日数据的地址模板，<年> 换成年份（默认 holiday-cn 的原始文件）
		HolidayURL string `yaml:"holidayUrl"`
	} `yaml:"calendar"`
	// Hooks 是用户钩子的参数（规格-用户钩子）；钩子本身在数据目录的 config/hooks.json
	Hooks   Hooks         `yaml:"hooks"`
	Plugins PluginsConfig `yaml:"plugins"` // plugins.sessions（会话开关）和 plugins.<id>（插件开关、设置，见 plugins.go）
	Reply   struct {
		Deny map[string]string `yaml:"deny"` // 平台:号 → ai / bot / blocked；默认空（ADR-116）
	} `yaml:"reply"`
	Embed Embed `yaml:"embed"`
	// 链接上下文与自动展开发送互相独立；展开默认关闭且白名单为空。
	LinkReader LinkReaderConfig `yaml:"linkReader"`
	// 模型窗口和单次输出的来源：用户覆盖、models.dev 缓存（ADR-334）
	ModelMeta ModelMetaConfig `yaml:"modelMeta"`
	Agent     struct {
		Lifecycle      Lifecycle `yaml:"lifecycle"`
		MaxLive        int       `yaml:"maxLive"`
		IdleMinutes    float64   `yaml:"idleMinutes"`
		RecentMessages int       `yaml:"recentMessages"`
		Work           Work      `yaml:"work"`
		ReviewConfig   `yaml:",inline"`
		Scheduler      Scheduler `yaml:"scheduler"`
		Budget         Budget    `yaml:"budget"`
		// Context 是上下文的几道线（规格-本地话题与压缩第二节、第八节；Go 的 T21）：window 封顶（不写跟着模型自己报的长度）、
		// reserve 留给回复和工具往返；超过 window × softRatio 本轮之前的工具结果在发出去的内容里换成短占位；超过硬门槛
		// （window × hardRatio 和 window − reserve 里小的）本地闲聊压缩（autoCompactChat），关掉的、渠道的、压缩没成的只带最近 keepTokens 的话
		Context struct {
			Window          int     `yaml:"window"`
			Reserve         int     `yaml:"reserve"`
			SoftRatio       float64 `yaml:"softRatio"`
			HardRatio       float64 `yaml:"hardRatio"`
			KeepTokens      int     `yaml:"keepTokens"`
			TopicCompactAt  int     `yaml:"topicCompactAt"` // 话题到这么大就压（ADR-074）；Go 还没有话题，先读进来
			AutoCompactChat bool    `yaml:"autoCompactChat"`
		} `yaml:"context"`
		// IdleCompact 是本地私聊的空闲压缩（规格-私聊从上下文起点重建 第五节、ADR-388）：一个会话最后一轮结束以后
		// 闲满 After 分钟、实例至少 MinTokens 才在后台压一次。主人 QQ 私聊、群聊不做。
		IdleCompact struct {
			Enabled   bool    `yaml:"enabled"`
			After     float64 `yaml:"after"` // 分钟，照 idleMinutes；默认 50（赶在一小时缓存过期前）
			MinTokens int     `yaml:"minTokens"`
		} `yaml:"idleCompact"`
		Thinking struct {
			Local   Thinking `yaml:"local"`
			Private Thinking `yaml:"private"`
			Group   Thinking `yaml:"group"`
		} `yaml:"thinking"`

		// FirstContentSeconds、ContentIdleSeconds 是模型流式调用的"内容空闲"时限（秒）：从发出到第一段内容、两段内容之间最长等多久。
		// 只算正文、思考、工具参数，SSE 心跳不算；超了断开这一次、按一时的错误重试。0 = 不管。默认 180；先想很久又不流式返回思考的模型调大
		FirstContentSeconds float64 `yaml:"firstContentSeconds"`
		ContentIdleSeconds  float64 `yaml:"contentIdleSeconds"`
	} `yaml:"agent"`
}

// Defaults 是一份全默认的配置（和 daemon/config.ts 的 zod 默认值一样）
func Defaults() Config {
	var c Config
	c.Console.Host, c.Console.Port = "127.0.0.1", 7720
	c.Magpie.BaseURL = "http://127.0.0.1:3425/v1"
	c.DataDir, c.CharactersDir = "data", "characters"
	c.Characters = map[string]CharacterModels{}
	c.Defaults.BackgroundThinking = "low"
	c.Agent.MaxLive, c.Agent.IdleMinutes, c.Agent.RecentMessages = 8, 45, 30
	c.Agent.IdleCompact.Enabled, c.Agent.IdleCompact.After, c.Agent.IdleCompact.MinTokens = true, 50, 200_000
	c.Agent.FirstContentSeconds, c.Agent.ContentIdleSeconds = 180, 180
	c.Agent.Lifecycle = DefaultLifecycle()
	c.Watchdog = DefaultWatchdog()
	c.Logs = DefaultLogSettings()
	c.Selfcheck = DefaultSelfcheck()
	c.Agent.Work = DefaultWork()
	c.Agent.ReviewConfig = DefaultReviews()
	c.Ecosystem = EcosystemDefaults()
	c.Agent.Scheduler = DefaultScheduler()
	c.Agent.Budget = DefaultBudget()
	ctx := &c.Agent.Context
	ctx.Reserve, ctx.SoftRatio, ctx.HardRatio, ctx.KeepTokens, ctx.TopicCompactAt, ctx.AutoCompactChat = 16_384, 0.7, 0.85, 20_000, 200_000, true
	c.Agent.Thinking.Local, c.Agent.Thinking.Private, c.Agent.Thinking.Group = "medium", "low", "low"
	// 群聊闸门（daemon/config.ts 的 group）和嵌入（embed）：默认值照 zod
	c.Group.Frequency, c.Group.Jitter, c.Group.Mode = 0.3, 0.03, "dry"
	c.Embed.Enabled, c.Embed.Backend = true, BackendGoModels
	c.LinkReader = DefaultLinks()
	c.ModelMeta = DefaultModelMeta()
	c.Proactive = DefaultProactive()
	c.Reminders = DefaultReminders()
	c.Backend = DefaultBackend()
	c.Notifications = DefaultNotifications()
	c.Calendar.HolidayURL = DefaultHolidayURL
	c.FreeAPI = DefaultFreeAPI()
	c.Hooks = DefaultHooks()
	c.QQ.Media.Download = true
	c.QQ.Backfill.Enabled, c.QQ.Backfill.Groups = true, true
	c.QQ.Forward.Fetch = true
	c.Memory.IdleMinutes, c.Memory.BatchEvents, c.Memory.TopK, c.Memory.MaxChars = 10, 40, 8, 1200
	c.Memory.Rerank.K, c.Memory.Rerank.TimeoutSeconds = 40, 5
	c.Memory.Write.Characters = []string{"*"}
	c.Memory.Write.Places, c.Memory.Write.ConfirmOccasions, c.Memory.Write.ConfirmKinds = []string{"owner-private"}, 3, []string{"fact", "preference"}
	c.Expression.LearnGroups, c.Expression.LearnPrivate = true, true
	c.Expression.ReviewHours, c.Expression.ReviewBatches = 3, 10
	c.Vision.Enabled, c.Vision.Concurrency, c.Vision.BackfillLanes, c.Vision.Direct, c.Vision.DirectMax = true, 2, 2, true, 4
	c.Tools.ApprovalMinutes, c.Tools.ApprovalMode, c.Tools.LineEnding = 10, "manual", "auto"
	c.Tools.Checkpoints.Enabled = true
	c.QQ.RemoteApproval.AwayAfterSeconds = 60
	c.ProjectContext.Enabled = true // 开着，但不信任的目录一个说明文件也不读：发现 AGENTS.md 时问一次，答过记住（规格-审批分级 8.5）
	c.Backup.Daily, c.Backup.DailyMedia, c.Backup.AlertGB = true, "stickers", 10
	c.Backup.MinFreeGB, c.Backup.RunMaxHours, c.Backup.CloseWaitSeconds = 5, 6, 30
	return c
}

// File 是配置文件在哪：SUMIKA_CONFIG → 数据目录里的 config/sumika.yml → 项目根的 sumika.yml（照 TS 版的 configFile）。
// 数据目录这里用 SUMIKA_DATA_DIR 或项目根下的 data/，不用配置里的 dataDir（配置还没读）
func File(root string) string {
	file, _ := FileIn(root, "")
	return file
}

// FileIn 按指定数据目录找配置；只有文件不存在才回退，其他 Stat 错误直接返回。
func FileIn(root, data string) (string, error) {
	if f := os.Getenv("SUMIKA_CONFIG"); f != "" {
		return f, nil
	}
	if data == "" {
		data = os.Getenv("SUMIKA_DATA_DIR")
		if data == "" {
			data = filepath.Join(root, "data")
		}
	}
	moved := filepath.Join(data, "config", "sumika.yml")
	if _, err := os.Stat(moved); err == nil {
		return moved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return moved, fmt.Errorf("看配置文件 %s：%w", moved, err)
	}
	return filepath.Join(root, "sumika.yml"), nil
}

// Load 读配置；文件不存在就全部用默认值。出错时给中文说明
func Load(root string) (Config, error) {
	return LoadIn(root, "")
}

// LoadIn 按指定数据目录找配置；SUMIKA_CONFIG 仍优先，目录内没有配置再找项目根。data 为空时照 Load。
func LoadIn(root, data string) (Config, error) {
	c := Defaults()
	file, err := FileIn(root, data)
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		c.prepareProviders(nil)
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return Parse(b, file)
}

// Parse 是启动和保存自检共用的字节解析、默认值与校验边界。
func Parse(b []byte, file string) (Config, error) {
	c := Defaults()
	original := b
	b, err := normalizeModelRefObjects(b) // 模型引用的对象写法换成字符串（只在内存里，规格-模型配置 3.2）
	if err != nil {
		return c, fmt.Errorf("%s：%w", file, err)
	}
	if err := checkStructKeys(b, reflect.TypeOf(c)); err != nil {
		return c, fmt.Errorf("%s：%w", file, err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s 不是合法的 YAML，或者某一项的类型不对：%s", file, yaml.FormatError(err, false, false))
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return c, fmt.Errorf("配置来源无法解析")
	}
	if err := validateWorkRaw(raw); err != nil {
		return c, err
	}
	if err := validateSendersRaw(raw); err != nil {
		return c, err
	}
	if err := validateProactiveRaw(raw); err != nil {
		return c, err
	}
	fitStopGrace(raw, &c.Agent.Lifecycle)
	if err := validateLifecycleRaw(raw); err != nil {
		return c, err
	}
	ec, err := ParseEcosystem(b)
	if err != nil {
		return c, err
	}
	c.Ecosystem = ec
	c.Present = map[string]bool{}
	var present func(any, string)
	present = func(value any, path string) {
		switch values := value.(type) {
		case map[string]any:
			for key, child := range values {
				next := key
				if path != "" {
					next = path + "." + key
				}
				c.Present[next] = true
				present(child, next)
			}
		case []any:
			for i, child := range values {
				next := path + "." + strconv.Itoa(i)
				c.Present[next] = true
				present(child, next)
			}
		}
	}
	present(raw, "")
	// memory.write 的两个角色名单：显式写了键但值是 YAML 的 null（`characters:` 后面留空、`characters: ~`）按空名单，
	// 不能掉回默认的 ["*"]（ADR-377 第 1 条）。Present 只记键在不在，分不出 null 和空列表，所以看解出来的 raw；
	// 键没写才用默认值，别的键（例如 places）null 仍等于没写
	if explicitNull(raw, "memory", "write", "characters") {
		c.Memory.Write.Characters = []string{}
	}
	if explicitNull(raw, "memory", "write", "summarize") {
		c.Memory.Write.Summarize = []string{}
	}
	// 删掉的嵌入键：读得进来、不用；宿主启动时警告一次，别让主人以为还在用
	for _, k := range []string{"embed.python", "embed.serverDir"} {
		if c.Present[k] {
			c.RemovedKeys = append(c.RemovedKeys, k)
		}
	}
	c.Source = file
	c.Version = ContentVersion(original) // 版本认磁盘上的原文，不认换过写法的
	if c.Characters == nil {
		c.Characters = map[string]CharacterModels{}
	}
	c.prepareProviders(raw)
	return c, c.check(raw)
}

// explicitNull：raw 里这个点分路径存在、且值是 YAML 的 null（`key:` 后面留空或 `~`）。
// 键没写、值是空列表（[]）都返回 false
func explicitNull(raw map[string]any, path ...string) bool {
	var cur any = raw
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[key]; !ok {
			return false
		}
	}
	return cur == nil
}

// check：只查第 0 步用到的几项，规则和 zod 一样。raw 是原始 YAML，用来区分
// providers.magpie 是配置里写的还是内存里合成的、以及看有没有明文 key 这类不认识的字段
func (c Config) check(raw map[string]any) error {
	var bad []string
	if err := c.Agent.ReviewConfig.Check(); err != nil {
		bad = append(bad, err.Error())
	}
	if c.Console.Port < 1 || c.Console.Port > 65535 {
		bad = append(bad, fmt.Sprintf("console.port：%d 不在 1～65535", c.Console.Port))
	}
	// providers.magpie 一旦存在，magpie.baseUrl 就被取代（规格-模型配置 第四节第 1 条）：
	// 不拿被忽略的旧地址挡加载，改由 providerIssues 查 providers.magpie.baseUrl
	_, magpieDeclared := providerEntry(raw, "magpie")
	if !magpieDeclared && !strings.HasPrefix(c.Magpie.BaseURL, "http://") && !strings.HasPrefix(c.Magpie.BaseURL, "https://") {
		bad = append(bad, "magpie.baseUrl：不是网址")
	}
	bad = append(bad, c.providerIssues(magpieDeclared)...)
	bad = append(bad, c.modelRefIssues()...)
	bad = append(bad, providerRawIssues(raw)...)
	for name, t := range map[string]Thinking{"defaults.backgroundThinking": c.Defaults.BackgroundThinking, "agent.thinking.local": c.Agent.Thinking.Local, "agent.thinking.private": c.Agent.Thinking.Private, "agent.thinking.group": c.Agent.Thinking.Group} {
		if !thinkingLevels[t] {
			bad = append(bad, fmt.Sprintf("%s：%q 不是认识的思考档位", name, t))
		}
	}
	checkFallback := func(key string, models []string) {
		seen := make(map[string]bool, len(models))
		for i, model := range models {
			if model == "" || model != strings.TrimSpace(model) || seen[model] {
				bad = append(bad, fmt.Sprintf("%s[%d]：模型 ID 不能为空、含首尾空格或重复", key, i))
			}
			seen[model] = true
		}
	}
	checkFallback("defaults.fallbackModels", c.Defaults.FallbackModels)
	for id, character := range c.Characters {
		checkFallback("characters."+id+".fallbackModels", character.FallbackModels)
	}
	// agent.context 的范围照 zod（daemon/config.ts:143）
	ctx := c.Agent.Context
	for _, r := range []struct {
		ok   bool
		what string
	}{
		{ctx.Window == 0 || ctx.Window >= 1000, fmt.Sprintf("agent.context.window：%d 小于 1000", ctx.Window)},
		{ctx.Reserve >= 0, fmt.Sprintf("agent.context.reserve：%d 是负数", ctx.Reserve)},
		{ctx.SoftRatio >= 0.1 && ctx.SoftRatio <= 1, fmt.Sprintf("agent.context.softRatio：%v 不在 0.1～1", ctx.SoftRatio)},
		{ctx.HardRatio >= 0.1 && ctx.HardRatio <= 1, fmt.Sprintf("agent.context.hardRatio：%v 不在 0.1～1", ctx.HardRatio)},
		{ctx.SoftRatio <= ctx.HardRatio, "agent.context.softRatio：不能大于 hardRatio"},
		{ctx.KeepTokens >= 1000, fmt.Sprintf("agent.context.keepTokens：%d 小于 1000", ctx.KeepTokens)},
		{ctx.TopicCompactAt >= 8000, fmt.Sprintf("agent.context.topicCompactAt：%d 小于 8000", ctx.TopicCompactAt)},
		// 实例回收（daemon/config.ts:126、131）
		{c.Agent.MaxLive >= 1, fmt.Sprintf("agent.maxLive：%d 小于 1", c.Agent.MaxLive)},
		{c.Agent.IdleMinutes > 0, fmt.Sprintf("agent.idleMinutes：%v 不是正数", c.Agent.IdleMinutes)},
		{c.Agent.IdleCompact.After > 0, fmt.Sprintf("agent.idleCompact.after：%v 不是正数", c.Agent.IdleCompact.After)},
		{c.Agent.IdleCompact.MinTokens >= 1 && c.Agent.IdleCompact.MinTokens <= 1<<24, fmt.Sprintf("agent.idleCompact.minTokens：%d 不在 1～%d", c.Agent.IdleCompact.MinTokens, 1<<24)},
		// 空闲压缩要压出比保留原文多得多的东西才值得跑一次模型；下限不比保留原文的两倍大，压完跟没压差不多
		{c.Agent.IdleCompact.MinTokens > 2*ctx.KeepTokens, fmt.Sprintf("agent.idleCompact.minTokens：%d 必须大于 agent.context.keepTokens（%d）的 2 倍，不然整理完剩下的原文跟没整理差不多，白跑一次模型", c.Agent.IdleCompact.MinTokens, ctx.KeepTokens)},
		{c.Agent.FirstContentSeconds >= 0, fmt.Sprintf("agent.firstContentSeconds：%v 是负数（0 是不管）", c.Agent.FirstContentSeconds)},
		{c.Agent.ContentIdleSeconds >= 0, fmt.Sprintf("agent.contentIdleSeconds：%v 是负数（0 是不管）", c.Agent.ContentIdleSeconds)},
		// 备份（规格-数据目录与备份 10.3）
		{c.Backup.MinFreeGB >= 0 && c.Backup.MinFreeGB <= 1e6, fmt.Sprintf("backup.minFreeGB：%v 不在 0～1000000（0 是不查剩余空间；inf、NaN 不认）", c.Backup.MinFreeGB)},
		{c.Backup.RunMaxHours >= 0.5 && c.Backup.RunMaxHours <= 72, fmt.Sprintf("backup.runMaxHours：%v 不在 0.5～72", c.Backup.RunMaxHours)},
		{c.Backup.CloseWaitSeconds >= 1 && c.Backup.CloseWaitSeconds <= 600, fmt.Sprintf("backup.closeWaitSeconds：%v 不在 1～600", c.Backup.CloseWaitSeconds)},
		// 群聊闸门（daemon/config.ts 的 group）
		{c.Group.Frequency >= 0 && c.Group.Frequency <= 1, fmt.Sprintf("group.frequency：%v 不在 0～1", c.Group.Frequency)},
		{c.Group.Jitter >= 0 && c.Group.Jitter <= 0.2, fmt.Sprintf("group.jitter：%v 不在 0～0.2", c.Group.Jitter)},
		{groupModes[c.Group.Mode], fmt.Sprintf("group.mode：%q 不是 off / dry / speak", c.Group.Mode)},
		{lineEndings[c.Tools.LineEnding], fmt.Sprintf("tools.lineEnding：%q 不是 auto / lf / crlf", c.Tools.LineEnding)},
		// 嵌入后端（规格-嵌入不依赖外部程序）：只有 go-models；legacy 已删，但旧配置要读得进来，交给 embed.Local 报明白原因。
		// K 的上限是本地模型服务一次请求的条数上限（64）
		{c.Embed.Backend == "" || c.Embed.Backend == BackendGoModels || c.Embed.Backend == "legacy", fmt.Sprintf("embed.backend：%q 不认识，只有 go-models（legacy 已移除）", c.Embed.Backend)},
		{c.Memory.Rerank.K >= 1 && c.Memory.Rerank.K <= 64, fmt.Sprintf("memory.rerank.k：%d 不在 1～64", c.Memory.Rerank.K)},
		{c.Memory.Rerank.TimeoutSeconds > 0 && c.Memory.Rerank.TimeoutSeconds <= 600, fmt.Sprintf("memory.rerank.timeoutSeconds：%v 不在 (0, 600]", c.Memory.Rerank.TimeoutSeconds)},
	} {
		if !r.ok {
			bad = append(bad, r.what)
		}
	}
	bad = append(bad, c.Memory.Write.check()...)
	bad = append(bad, c.LinkReader.check()...)
	bad = append(bad, c.ModelMeta.check()...)
	bad = append(bad, c.Proactive.check()...)
	bad = append(bad, c.Reminders.check()...)
	bad = append(bad, c.Backend.check()...)
	bad = append(bad, c.checkLife()...)
	bad = append(bad, c.FreeAPI.check()...)
	bad = append(bad, c.Hooks.check()...)
	bad = append(bad, c.checkBot()...)
	bad = append(bad, c.Agent.Lifecycle.check()...)
	bad = append(bad, c.Watchdog.check()...)
	bad = append(bad, c.Logs.check()...)
	bad = append(bad, c.Selfcheck.check()...)
	if n := c.QQ.RemoteApproval.AwayAfterSeconds; n < 0 || n > 600 {
		bad = append(bad, fmt.Sprintf("qq.remoteApproval.awayAfterSeconds：%d 超出 0～600", n))
	}
	// QQ 渠道（规格-QQ渠道）：号必须是数字串、同一个号不能出现两次、账号绑的角色不能为空
	if c.QQ.Owner != "" && !digitsOf(string(c.QQ.Owner)) {
		bad = append(bad, fmt.Sprintf("qq.owner：%q 不是 QQ 号（数字串）", MaskID(string(c.QQ.Owner))))
	}
	seen := map[QQID]bool{}
	for i, a := range c.QQ.Accounts {
		if !digitsOf(string(a.ID)) {
			bad = append(bad, fmt.Sprintf("qq.accounts[%d].id：%q 不是 QQ 号（数字串）", i, MaskID(string(a.ID))))
		} else if seen[a.ID] {
			bad = append(bad, fmt.Sprintf("qq.accounts[%d].id：号 %s 出现了两次", i, MaskID(string(a.ID))))
		}
		seen[a.ID] = true
		if a.Character == "" {
			bad = append(bad, fmt.Sprintf("qq.accounts[%d].character：没写账号绑哪个角色", i))
		}
	}
	for _, l := range []struct {
		name string
		ids  []QQID
	}{{"qq.groups", c.QQ.Groups}, {"qq.privateAllow", c.QQ.PrivateAllow}} {
		for i, id := range l.ids {
			if !digitsOf(string(id)) {
				bad = append(bad, fmt.Sprintf("%s[%d]：%q 不是 QQ 号（数字串）", l.name, i, MaskID(string(id))))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("sumika.yml 有 %d 处错误，改好后再启动：\n  - %s", len(bad), strings.Join(bad, "\n  - "))
	}
	return nil
}

// groupModes 是群聊闸门的三个档位（daemon/config.ts 的 group.mode）
var groupModes = map[string]bool{"off": true, "dry": true, "speak": true}

// lineEndings 是 tools.lineEnding 的三个取值
var lineEndings = map[string]bool{"auto": true, "lf": true, "crlf": true}

// check：只查取值认不认、有没有重复；"summarize 里的角色不在 characters 里""没选 catchUp"这类前提不在这里拒绝，
// 启动时每个角色各查一遍、保持只读并写明原因（app 的 memory write 前提检查），不让一个记忆的键挡住整个后台起不来
func (w MemoryWrite) check() []string {
	var bad []string
	list := func(key string, values, allowed []string, wantID bool) {
		seen := map[string]bool{}
		for _, v := range values {
			switch {
			case wantID && (v == "" || strings.ContainsAny(v, "/\\:") || strings.Contains(v, "..")):
				bad = append(bad, fmt.Sprintf("%s：%q 不是角色 id（不能空，不能带 / \\ : ..）", key, v))
			case !wantID && !slices.Contains(allowed, v):
				bad = append(bad, fmt.Sprintf("%s：%q 不是认识的取值（可选 %s）", key, v, strings.Join(allowed, " / ")))
			case seen[v]:
				bad = append(bad, fmt.Sprintf("%s：%q 写了两遍", key, v))
			}
			seen[v] = true
		}
	}
	list("memory.write.characters", w.Characters, nil, true)
	list("memory.write.summarize", w.Summarize, nil, true)
	// "*" 是“全部角色”，和具体名字混写会让“只开几个”和“全开”两种意思搅在一起，直接拒绝
	for _, l := range []struct {
		key    string
		values []string
	}{{"memory.write.characters", w.Characters}, {"memory.write.summarize", w.Summarize}} {
		if slices.Contains(l.values, "*") && len(l.values) > 1 {
			bad = append(bad, fmt.Sprintf("%s：写了 * 就是全部角色，要只开几个就去掉 *", l.key))
		}
	}
	list("memory.write.places", w.Places, WritePlaceNames, false)
	list("memory.write.confirmKinds", w.ConfirmKinds, WriteKindNames, false)
	if w.CatchUp != "" && w.CatchUp != "latest" && w.CatchUp != "cursor" {
		bad = append(bad, fmt.Sprintf("memory.write.catchUp：%q 不是 latest 或 cursor（开后台整理前必须自己选，没有默认）", w.CatchUp))
	}
	if w.ConfirmOccasions < 2 {
		bad = append(bad, fmt.Sprintf("memory.write.confirmOccasions：%d 小于 2：新写的候选自带一次证据，一次永远不够", w.ConfirmOccasions))
	}
	if w.QuarantineSince != "" {
		if n, err := strconv.ParseInt(w.QuarantineSince, 10, 64); err != nil || n <= 0 {
			bad = append(bad, fmt.Sprintf("memory.write.quarantineSince：%q 不是毫秒时间（填日志里 summarize_enabled_at 那一行的数字）", w.QuarantineSince))
		} else if n < minQuarantineMillis {
			// 手忙脚乱时容易填成秒（10 位）：所有创建时间的毫秒数都比它大，除了主人放行的，导入的全部旧记忆会一起被藏，她像失忆
			bad = append(bad, fmt.Sprintf("memory.write.quarantineSince：%q 像是秒（不到 13 位），要填毫秒：日志里 summarize_enabled_at 那一行的数字", w.QuarantineSince))
		}
	}
	return bad
}

// digitsOf 说一个号是不是数字串（照 zod 的 /^\d+$/）
func digitsOf(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// envRef 是令牌里引环境变量的写法（照 daemon/config.ts 的 expandEnv）
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv 把 ${名字} 换成环境变量的值（照 daemon/config.ts 的 expandEnv）：变量没设或者是空的就报错，
// 说明里只带变量名、不带值；遇到第一个没设的就停，后面的不再去读（Bun 是在替换回调里直接抛）。
// 读配置时不在 Load 里展开，连接时才展开（daemon/plugins/onebot.ts:52）；getenv 为 nil 时读本进程的环境
func ExpandEnv(s string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var out strings.Builder
	last := 0
	for _, m := range envRef.FindAllStringSubmatchIndex(s, -1) {
		name := s[m[2]:m[3]]
		v := getenv(name)
		if v == "" {
			return "", fmt.Errorf("环境变量 %s 没有设置", name)
		}
		out.WriteString(s[last:m[0]])
		out.WriteString(v)
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String(), nil
}

// DataDir 是数据目录：SUMIKA_DATA_DIR 优先，否则项目根 + 配置里的 dataDir（照 daemon/main.ts）
func (c Config) DataDirIn(root string) string {
	if d := os.Getenv("SUMIKA_DATA_DIR"); d != "" {
		return d
	}
	if filepath.IsAbs(c.DataDir) {
		return c.DataDir
	}
	return filepath.Join(root, c.DataDir)
}

// FindRoot 从 dir 往上找项目根（有 sumika.example.yml 的那一层）
func FindRoot(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "sumika.example.yml")); err == nil {
			return d, nil
		}
		up := filepath.Dir(d)
		if up == d {
			return "", fmt.Errorf("从 %s 往上没找到项目根（有 sumika.example.yml 的目录）", dir)
		}
		d = up
	}
}
