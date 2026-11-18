package settingsreg

// 每个设置键一条记录：键、类型、范围、可选值、生效方式、后台谁读它、谁能改、分类、给用户看的名字和说明，
// 都写在 records.go 的同一条里。加一个新键只动那一处；类型和默认值从 config 的结构体推出来，不再抄一份。
// 记录里的分类、生效方式、类型、存储用英文标识；中文只在最下面的 label 表里，也就是显示那一层。

type (
	effectID   string
	categoryID string
	storageID  string
	typeID     string
)

// 生效方式：每个键都要明说，没有默认值。零值（空串）不合法，测试会拦下漏写的。
const (
	fxNow            effectID = "now"             // 改完就管用
	fxNextTurn       effectID = "next-turn"       // 下一轮对话起管用
	fxRestartReceive effectID = "restart-receive" // 读取处只在开始接收时取一次
	fxRestartApp     effectID = "restart-app"     // 读取处只在程序启动时取一次
	fxNone           effectID = "none"            // 后台没有任何地方读它：写了也不会变
)

const (
	catModel      categoryID = "model"
	catNotify     categoryID = "notifications"
	catPlatform   categoryID = "platform"
	catChatRules  categoryID = "chat-rules"
	catMemory     categoryID = "memory"
	catTools      categoryID = "tools"
	catPlugins    categoryID = "plugins"
	catData       categoryID = "data"
	catCharacter  categoryID = "character"
	catAbout      categoryID = "about"
	catGeneral    categoryID = "general"
	catAppearance categoryID = "appearance"
	catShortcuts  categoryID = "shortcuts"
)

// 存储：零值是 yml（sumika.yml 里的键）。
const (
	stDesktop storageID = "desktop"
	stHost    storageID = "host"
	stPlanned storageID = "planned"
	stCard    storageID = "card"
)

// 类型：零值表示从 config 结构体推出来；写了就是覆盖。
const (
	tySwitch      typeID = "switch"
	tyNumber      typeID = "number"
	tyList        typeID = "list"
	tyTable       typeID = "table"
	tyObject      typeID = "object"
	tyText        typeID = "text"
	tyPath        typeID = "path"
	tyEnum        typeID = "enum"
	tySecret      typeID = "secret"
	tyUnavailable typeID = "unavailable"
	tyUnknown     typeID = "unknown"
)

// 显示那一层：Entry 里出去的仍是这些中文，界面、设置工具、自知投影看到的字没有变。
var (
	effectLabels = map[effectID]string{fxNow: EffectNow, fxNextTurn: EffectNextTurn, fxRestartReceive: EffectRestartReceive, fxRestartApp: EffectRestartApp, fxNone: EffectNone}
	typeLabels   = map[typeID]string{tySwitch: "开关", tyNumber: "数字", tyList: "列表", tyTable: "表", tyObject: "对象", tyText: "文本", tyPath: "路径", tyEnum: "枚举", tySecret: "密钥", tyUnavailable: "不可用", tyUnknown: "未知"}
	storageLabel = map[storageID]string{"": "yml", stDesktop: "桌面设置", stHost: "宿主", stPlanned: "未实现", stCard: "角色卡"}
	categoryText = map[categoryID]string{catModel: "模型", catNotify: "通知", catPlatform: "平台与账号", catChatRules: "会话规则", catMemory: "记忆", catTools: "工具与权限", catPlugins: "插件", catData: "数据与备份", catCharacter: "角色", catAbout: "关于与诊断", catGeneral: "通用", catAppearance: "外观", catShortcuts: "快捷键"}
)

// span 是数字的范围；hi 为空表示只有下限。
type span struct {
	lo float64
	hi *float64
}

func between(lo, hi float64) *span { return &span{lo, &hi} }
func atLeast(lo float64) *span     { return &span{lo: lo} }

// Label 是设置页给用户看的样子：名字、一行说明、单位、取值的叫法。
// 说明按设计语言第十一节写：常驻最多一行，说“这个会怎样”，不讲原理，不写字段名。
type Label struct {
	Name, Help, Unit string
	Choices          map[string]string
}

// ThinkingChoices 是思考档位的中文叫法。
var ThinkingChoices = map[string]string{"off": "关", "minimal": "最低", "low": "低", "medium": "中", "high": "高", "xhigh": "很高", "max": "最高"}

// record 是一个设置键的全部登记信息。
type record struct {
	key      string
	name     string // 登记名：设置工具和自知投影给模型看的名字
	effect   effectID
	cat      categoryID
	storage  storageID
	kind     typeID // 空：从 config 结构体推出来（有 options 就是枚举）
	def      any    // 非 yml 存储的默认值；也可以是 func() any
	writable bool   // 设置页和设置工具能不能写；不写就是只读（权限默认值，不放宽）
	reason   string // 只读时给界面的原因；空则用默认原因
	rng      *span
	// minExclusive 为真时下限本身不算（> 而不是 >=）
	minExclusive bool
	options      []string
	desc         string   // 手写的说明；空表示还没人写（registry_test 里有清单，只许缩短）
	aliases      []string // 真有人这样叫它才写
	// readers 是后台谁读它：包路径:函数（方法写成 类型.方法），可选 #字段 指定函数体里该出现的字段名。
	// 标了尚未生效的键必须为空，其余必须有；registry_test 逐条核对函数存在、函数体里真的出现了这个字段。
	readers []string
	// pending 非空表示这个键的生效方式还没核实，是照原值写的；原因写在这里。
	pending string
	ui      *Label // 设置页里给用户看的名字和说明；没有就留空
}
