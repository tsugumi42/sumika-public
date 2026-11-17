package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
)

// ───────────── 审批怎么判（daemon/tools/policy.ts 的 decide、spec.ts 的声明、grants.ts 的"同一类"） ─────────────
// 顺序：渠道 → 只碰自己的记忆 → 不是主人发起的只问一次 → 默认放行档 → 受保护路径 → 后台自己的地址+端口 → YOLO 下拆不开 / 动态执行又带删除 → 灾难性命令 → 主人授权过的同一类 → 范围档 → 按模式 → 兜底问

// Spec 是工具自己声明的风险（spec.ts ToolSpec）
type Spec struct {
	Effects   []string // self 只读写她自己的记忆；read / write 文件；exec 跑命令；net 联网
	Audience  string   // owner-console：只给主人 × 主窗口；any：effects 全是 self
	Parallel  bool
	TimeoutMs int
	Paths     func(args jsjson.Value) []string
	Classify  func(args jsjson.Value) []string
	// Plain 表示这个工具不碰路径也不跑命令，效果只看 Classify（剪贴板）：只读放行的判断和 auto 下的写入都按效果走
	Plain bool
	// Tier 是不碰路径也不跑命令的工具怎么放行（ADR-330 第 2 条，规格-审批分级第七节）；空的按 effects 走模式
	Tier string
}

// 放行档（Spec.Tier）。
const (
	TierOpen   = "open"   // 默认放行：只读地查公开资料，边界（内网、大小、时限）在服务自己那里
	TierScoped = "scoped" // 手动问；自动只放只读的；YOLO 放行；卡片上能记成"这个对话""记住"
)

// 这次是谁放行的（Decision.By），写进步骤记录。once 是主人点的"允许一次"。
const (
	ByOnce         = "once"
	ByConversation = "conversation"
	ByAlways       = "always"
	ByDefault      = "default" // 默认放行档
	ByMode         = "mode"    // 手动、自动、YOLO 的模式规则
	BySelf         = "self"    // 只碰她自己的记忆，不算放行
	ByRule         = "rule"    // 主人自己写的放行规则（规则存储里 effect 为 allow 的条目）
)

// JSString 像 JS 的 String(x)（没有这个键时给 def，像 args.path ?? def）
func JSString(args jsjson.Value, key, def string) string {
	v, ok := args.Obj[key]
	if !ok || v.Kind == jsjson.Null {
		return def
	}
	return jsString(v)
}

func jsString(v jsjson.Value) string {
	switch v.Kind {
	case jsjson.String:
		return v.Str
	case jsjson.Number:
		return jsjson.Number_(v.Num)
	case jsjson.Bool:
		if v.Bool {
			return "true"
		}
		return "false"
	case jsjson.Array:
		parts := make([]string, len(v.Arr))
		for i, e := range v.Arr {
			if e.Kind != jsjson.Null {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	case jsjson.Object:
		return "[object Object]"
	}
	return "null"
}

// PathArg 是路径类工具（read ls grep find write edit）的 path 参数：和工具自己的取法一致，
// 不是字符串（缺、null、数字、布尔、数组、对象）一律当没写，也就是 "."。以前按 JS 的 String() 转，
// 工具却把非字符串当空，两边判的不是同一个位置（grep 的 path 写成 {} 会搜整个工作目录，判定却看 "[object Object]"）。
func PathArg(args jsjson.Value) string {
	if v := args.Obj["path"]; v.Kind == jsjson.String && v.Str != "" {
		return v.Str
	}
	return "."
}

func pathArg(args jsjson.Value) []string { return []string{PathArg(args)} }

// taskEffects：reader 只读本地、research 只查公开网页（它的两个工具本来就默认放行），都按只读算；worker 和认不出的按最宽的算。
// 这里只决定派出去问不问，子任务的每一步照常过门。
func taskEffects(a jsjson.Value) []string {
	if v := a.Obj["agent"]; v.Kind == jsjson.String && (v.Str == "reader" || v.Str == "research") {
		return []string{"read"}
	}
	return []string{"read", "write", "exec", "net"}
}

var memorySpec = Spec{Effects: []string{"self"}, Audience: "any"}

// Specs 是登记过的工具（spec.ts SPECS）
var Specs = map[string]Spec{
	"memory_search": {Effects: memorySpec.Effects, Audience: "any", Parallel: true, TimeoutMs: 30_000},
	"recall_raw":    {Effects: memorySpec.Effects, Audience: "any", Parallel: true, TimeoutMs: 30_000},
	"remember":      {Effects: memorySpec.Effects, Audience: "any", TimeoutMs: 30_000},
	"memory_forget": {Effects: memorySpec.Effects, Audience: "any", TimeoutMs: 30_000},
	// read_link_detail 只读本会话她看到过的消息的链接资料（linkplugin），不联网
	// inspect_client exposes only a host-authenticated, filtered settings projection.
	"inspect_client": {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 10_000},
	// 设置工具（settingtool，规格-客户端自知与设置工具 第十一节）：真正的确认在工具里（审批卡，只认主人的点击，
	// 不吃 yolo、不吃“这个对话都允许”），所以这里是 self、any；谁能看见、谁能执行由宿主按身份判，群和别人的私聊两处都拒绝
	"preview_setting_change":  {Effects: []string{"self"}, Audience: "any", TimeoutMs: 20_000},
	"apply_setting_change":    {Effects: []string{"self"}, Audience: "any", TimeoutMs: 330_000},
	"rollback_setting_change": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 20_000},
	"list_setting_changes":    {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 5_000},
	"cancel_setting_change":   {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"read_link_detail":        {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 10_000},
	"result_read":             {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 10_000},
	// calendar、weather（lifetools，规格-主动联系 第十九节）：只读查询，不碰文件、不碰她的记忆；联网只打两个固定的公开接口
	// （假日数据、Open-Meteo），只带她自己说出来的地名，所以和 read_link_detail 一样归 self、不弹审批。
	// 谁能用默认所在地由工具自己按身份判（lifetools.Options.DefaultPlace），这里的 any 只管能不能调
	"calendar":        {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 15_000},
	"weather":         {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 15_000},
	"exchange_rate":   {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 15_000},
	"skill":           {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30000},
	"mcp_search":      {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30000},
	"mcp_call":        {Effects: []string{"net"}, Audience: "owner-console", TimeoutMs: 60000, Tier: TierScoped},
	"mcp_cancel":      {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 10000},
	"mcp_result_read": {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 10000},
	"web_search":      {Effects: []string{"net"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30000, Tier: TierOpen},
	"web_fetch":       {Effects: []string{"net"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30000, Tier: TierOpen},
	"wiki_lookup":     {Effects: []string{"net"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30000, Tier: TierOpen},
	"task":            {Effects: []string{"read", "write", "exec", "net"}, Audience: "owner-console", TimeoutMs: 5000, Tier: TierScoped, Classify: taskEffects},
	"task_result":     {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 310000},
	"task_list":       {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 5000},
	"task_message":    {Effects: []string{"write"}, Audience: "owner-console", TimeoutMs: 5000},
	"task_cancel":     {Effects: []string{"write"}, Audience: "owner-console", TimeoutMs: 5000},
	// schedule_next 只改定期任务自己的下次时间；只在本地私聊主人的定期任务那一轮里可见（app/loopwire.go）
	"schedule_next": {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 5_000},
	"todo_write":    {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 5_000},
	// ask_owner 等主人回答：等多久由工具自己按 tools.approvalMinutes 和这一轮总时限算（规格-向主人提问），
	// 这里只是个够大的保险，比总时限的上限（agent.budget.maxDurationMs，一小时）再长一点
	"ask_owner": {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 3_660_000},
	// 主动联系的话题线工具（规格-主动联系 第八、九节）：只碰线自己的状态，和"往本地私聊写一句她的话"；
	// 谁能看见由宿主的 For 定（线里、主人私聊），所以这里的听众是 any，不是 owner-console（线的 PrivateOK 为假）
	"owner_presence":   {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 10_000},
	"thread_send":      {Effects: []string{"self"}, Audience: "any", TimeoutMs: 30_000},
	"stay_quiet":       {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"end_thread":       {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"hand_back":        {Effects: []string{"self"}, Audience: "any", TimeoutMs: 10_000},
	"proactive_snooze": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	// 提醒（规格-提醒）：只碰主人自己的提醒表。谁能看见、谁能调由宿主定（app/remindwire.go：只有听众是主人的场合），
	// 所以这里的听众是 any（主动联系的线 PrivateOK 为假、QQ 私聊不是 console）；不弹审批（ADR-330 第 1 条）
	"reminder_create": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"reminder_list":   {Effects: []string{"self"}, Audience: "any", Parallel: true, TimeoutMs: 5_000},
	"reminder_update": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"reminder_finish": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"reminder_delete": {Effects: []string{"self"}, Audience: "any", TimeoutMs: 5_000},
	"clipboard": {Effects: []string{"read", "write"}, Audience: "owner-console", TimeoutMs: 10_000, Plain: true, Classify: func(a jsjson.Value) []string {
		if JSString(a, "action", "") == "read" {
			return []string{"read"}
		}
		return []string{"write"}
	}},
	"open":   {Effects: []string{"exec"}, Audience: "owner-console", TimeoutMs: 30_000},
	"notify": {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 10_000},
	"read":   {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30_000, Paths: pathArg},
	"ls":     {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30_000, Paths: pathArg},
	"grep":   {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30_000, Paths: pathArg},
	"find":   {Effects: []string{"read"}, Audience: "owner-console", Parallel: true, TimeoutMs: 30_000, Paths: pathArg},
	"write":  {Effects: []string{"write"}, Audience: "owner-console", TimeoutMs: 60_000, Paths: pathArg},
	"edit":   {Effects: []string{"write"}, Audience: "owner-console", TimeoutMs: 60_000, Paths: pathArg},
	// 后台命令的输出和停止：只碰这个对话里自己起的后台进程（起的那一下按 powershell 审批过了），不再审批；
	// 渠道仍由 owner-console 挡住，QQ 和群聊用不了
	"process_output": {Effects: []string{"self"}, Audience: "owner-console", Parallel: true, TimeoutMs: 330_000},
	"process_stop":   {Effects: []string{"self"}, Audience: "owner-console", TimeoutMs: 30_000},
	"powershell": {Effects: []string{"exec"}, Audience: "owner-console", TimeoutMs: 630_000, Classify: func(a jsjson.Value) []string {
		if IsReadOnlyCommand(JSString(a, "command", "")) {
			return []string{"read"}
		}
		return []string{"exec"}
	}},
}

// Undeclared 是没登记的工具：最危险的那一档
var Undeclared = Spec{Effects: []string{"exec", "write", "net"}, Audience: "owner-console", TimeoutMs: 60_000}

// Audience 是这个对话实例是谁在听（这里只用到渠道和是不是主人在场的私聊）
type Audience struct {
	Channel   string `json:"channel"`
	PrivateOk bool   `json:"privateOk"`
}

// Env 是判定要的设置和 gate 先查好的东西（policy.ts PolicyEnv）
type Env struct {
	Mode            string              `json:"mode"`
	Workdir         string              `json:"workdir"`
	AutoApproveDirs []string            `json:"autoApproveDirs"`
	ProtectedDirs   []string            `json:"protectedDirs"`
	Audience        Audience            `json:"audience"`
	Declared        bool                `json:"declared"`
	SelfPorts       []int               `json:"selfPorts"`
	NotOwner        bool                `json:"notOwner"`    // 这一轮不是主人发起的（本地群聊、角色接角色）：不进任何自动放行（ADR-269）
	Shell           *Verdict            `json:"shell"`       // 命令拆开看的结论；不是命令时没有
	ShellFailed     bool                `json:"shellFailed"` // 是命令但拆不开（JS 里的 shell === null）
	Granted         string              `json:"granted"`     // "" / conversation / always
	GrantDirs       []string            `json:"grantDirs"`
	Real            map[string]string   `json:"real"`
	ProjectTools    map[string][]string `json:"projectTools"`
	Rules           []UserRule          `json:"-"` // 主人自己写的放行、拒绝规则（gate 从规则存储读来）
	Conversation    string              `json:"-"` // 这一次调用所在的会话；规则的会话范围按它比（空是看不出）
	Ancestors       []string            `json:"-"` // 子任务：派它的会话（逐级向上）；只给 ask 规则的会话范围用
	MCPServer       string              `json:"-"` // mcp_call 解析出的服务器和原始工具名（规则按它们比）
	MCPTool         string              `json:"-"`
}

// Decision 是判定的结果（policy.ts Verdict）
type Decision struct {
	Kind   string `json:"kind"` // allow / ask / deny
	Reason string `json:"reason,omitempty"`
	Sticky bool   `json:"sticky,omitempty"` // 受保护路径、灾难性命令：不给"始终允许"
	Limit  string `json:"limit,omitempty"`  // once：只能允许一次；conversation：最多"这个对话里都允许"
	By     string `json:"-"`                // 放行时：按哪一档放的（By 开头的几个常量）
	// RuleKey、RuleLabel：靠主人写的规则放行、拒绝时，是哪一条（规则存储里的 key 和原文）
	RuleKey, RuleLabel string `json:"-"`
}

var (
	outsideHead = regexp.MustCompile(`^(~|[a-z]\w+:)`)
	homeTilde   = regexp.MustCompile(`^~([\\/]|$)`)
	protNames   = regexp.MustCompile(`\b(secrets\.bin|sumika\.yml|tool-grants\.yml)\b`)
	envFile     = regexp.MustCompile(`^\.env(\.[^\n\r\x{2028}\x{2029}]+)?$`)
	unicodeSp   = regexp.MustCompile("[\u00a0\u2000-\u200a\u202f\u205f\u3000]")
	gitBash     = regexp.MustCompile(`^/(?:mnt/|cygdrive/)?([a-z])(?:/([^\n\r\x{2028}\x{2029}]*))?$`)
	fileURL     = regexp.MustCompile(`^file://`)
	pipeEtc     = regexp.MustCompile(`[|;>]|&&`)
	agentName   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// OutsideForm 像 OUTSIDE_FORM：~ 开头、两个字母以上的盘符（HKCU:、Env:，后面跟着不是空白的字或结尾）、provider 写法（含 ::）当在外面
func OutsideForm(p string) bool {
	low := jsstr.LowerASCII(p)
	if strings.HasPrefix(low, "~") || strings.Contains(low, "::") {
		return true
	}
	if m := rootedDrive.FindString(low); m != "" {
		return len(m) == len(low) || !jsstr.IsSpace([]rune(low[len(m):])[0])
	}
	return false
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// expandHome：命令里的 ~ 是用户目录
func expandHome(p string) string {
	if homeTilde.MatchString(p) {
		rest := ""
		if len(p) > 2 {
			rest = p[2:]
		}
		return win32Join(home(), rest)
	}
	return p
}

func linkKey(p string) string { return strings.ToLower(resolveCwd(p)) }

// isAbsWin 像 Node 的 path.win32.isAbsolute：\ 或 / 开头、或者 X:\ X:/
func isAbsWin(p string) bool {
	if p == "" {
		return false
	}
	if isSep(p[0]) {
		return true
	}
	return len(p) > 2 && isDeviceRoot(p[0]) && p[1] == ':' && isSep(p[2])
}

// ToolPath 是读、写、改、列、搜、找工具的路径按工具自己解析的办法变成绝对路径（policy.ts toolPath；和 tools 包里 pi 的 resolveToCwd 同一个规矩）
func ToolPath(path, workdir string) string {
	p := toolPathPre(path)
	if isAbsWin(p) {
		return resolveCwd(p)
	}
	return resolve(workdir, p)
}

// toolPathPre 是 ToolPath 变成绝对路径之前的那半段：不间断空格换成空格、去掉开头的 @、git bash 写法（/c/…）、~、file:// 都换成路径。
// 拆出来是为了网络共享、设备路径的判断（IsNetworkOrDevicePath）能看到换完以后的样子，又不受 Clean 在不同系统上的差别影响。
func toolPathPre(path string) string {
	p := unicodeSp.ReplaceAllString(path, " ")
	p = strings.TrimPrefix(p, "@")
	if strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.Contains(p, `\`) {
		if m := gitBash.FindStringSubmatchIndex(jsstr.LowerASCII(p)); m != nil {
			rest := ""
			if m[4] >= 0 {
				rest = strings.ReplaceAll(p[m[4]:m[5]], "/", `\`)
			}
			p = strings.ToUpper(p[m[2]:m[3]]) + `:\` + rest
		}
	}
	switch {
	case p == "~":
		p = home()
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		p = win32Join(home(), p[2:])
	case fileURL.MatchString(p):
		if fp, err := jsstr.FileURLToPath(p); err == nil {
			p = fp
		}
	}
	return p
}

// DefaultProtectedDirs 是默认的受保护目录：Sumika 自己的数据目录 + 用户目录下放密钥的几个
func DefaultProtectedDirs(dataDir string) []string {
	h := home()
	return []string{dataDir, resolve(h, ".ssh"), resolve(h, ".aws"), resolve(h, ".gnupg")}
}

func normDir(p string) string { return strings.TrimRight(strings.ToLower(resolveCwd(p)), `\/`) }

// IsEnvFileName 是这个文件名（只含最后一段，不分大小写）属于 .env 一类：.env、.env.local、.env.production……
// 受保护路径的判断和 grep 过滤搜索结果用的是这同一个函数，规则只此一份。
func IsEnvFileName(base string) bool {
	return envFile.MatchString(jsstr.LowerASCII(base))
}

// TouchesProtected 是这个路径在受保护目录里，或者（搜索、找文件时）它是受保护目录的上级——会搜进去。
// 网络共享、设备路径（\\server\share、//server/share、\\?\、\\.\）一律算碰到：比不上本地路径，不去还原（见 unc.go）；
// 这一条在最前面，不看 recursive，也不受 Clean 在不同系统上怎么处理开头的斜杠影响
func TouchesProtected(path string, workdir string, protectedDirs []string, recursive bool) bool {
	if IsOpaquePath(path) {
		return true
	}
	abs := path
	if !isAbsWin(path) {
		abs = resolve(workdir, path)
	}
	abs = dropTrailingDotSpace(abs) // Windows 会去掉每一段末尾的点和空格：sdata.\c.yml 就是 sdata\c.yml
	if IsEnvFileName(win32Base(abs)) {
		return true
	}
	if IsInside(abs, protectedDirs, workdir) {
		return true
	}
	if !recursive {
		return false
	}
	a := normDir(abs)
	for _, d := range protectedDirs {
		if p := normDir(d); strings.HasPrefix(p, a+`\`) || strings.HasPrefix(p, a+"/") {
			return true
		}
	}
	return false
}

// win32Base 像 path.win32.basename：最后一个 \ 或 / 后面那段（结尾的分隔符先去掉）
func win32Base(p string) string {
	p = strings.TrimRight(p, `\/`)
	if len(p) >= 2 && p[1] == ':' && isDeviceRoot(p[0]) {
		p = p[2:]
	}
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// CommandTouchesProtected 是命令文字里写到了受保护的路径或文件名（大小写、正反斜杠都认）
func CommandTouchesProtected(command string, protectedDirs []string) bool {
	return commandTouchesProtected(command, protectedDirs, true)
}

// commandTouchesProtected：withDrive 为假时不做命令文字上的“驱动器相对路径”判断——分析器拆开了就以它给的为准
// （echo a:b/c 这类输出文字不是路径；真正的路径参数由 Verdict.PathDrive 管），拆不开才靠文字。
func commandTouchesProtected(command string, protectedDirs []string, withDrive bool) bool {
	if commandHasNetworkPath(command) || (withDrive && commandHasDriveRelative(command)) {
		return true
	}
	if protNames.MatchString(jsstr.LowerASCII(command)) {
		return true
	}
	c := strings.ReplaceAll(strings.ToLower(command), "/", `\`)
	nc := normCommandText(command) // 双反斜杠、\.\ 段、段末尾点空格、.. 绕回都规整掉再比一遍
	for _, d := range protectedDirs {
		plain := strings.ReplaceAll(normDir(d), "/", `\`)
		if strings.Contains(c, plain) {
			return true
		}
		if nd := strings.TrimRight(normCommandText(plain), `\`); nd != "" && strings.Contains(nc, nd) {
			return true
		}
	}
	return false
}

type placeT struct {
	canon       func(string) string
	inside      func(string) bool
	shellInside func(string) bool
}

// placeOf："在工作目录或允许目录里"：字面上、联接解析以后都要在里面（gate 给的 Real；没给的按字面）
func placeOf(env Env) placeT {
	canon := func(p string) string {
		if r, ok := env.Real[linkKey(p)]; ok {
			return r
		}
		return resolveCwd(p)
	}
	dirs := append([]string{env.Workdir}, env.AutoApproveDirs...)
	realDirs := make([]string, len(dirs))
	for i, d := range dirs {
		realDirs[i] = canon(d)
	}
	inside := func(abs string) bool {
		return IsInside(abs, dirs, env.Workdir) && IsInside(canon(abs), realDirs, env.Workdir)
	}
	return placeT{canon, inside, func(p string) bool { return !OutsideForm(p) && !IsOpaquePath(p) && inside(resolve(env.Workdir, p)) }}
}

func shellPaths(sh *Verdict) []string {
	if sh == nil {
		return nil
	}
	return append(append([]string(nil), sh.FileTargets...), sh.Runs...)
}

// OutsideTargets 是命令碰到的工作目录、允许目录外面的位置（真实路径）；provider 写法落不到目录上，记成 blind
func OutsideTargets(env Env) (paths []string, blind bool) {
	pl := placeOf(env)
	paths = []string{}
	seen := map[string]bool{}
	for _, t := range shellPaths(env.Shell) {
		if pl.shellInside(t) {
			continue
		}
		p := expandHome(t)
		if OutsideForm(p) || IsOpaquePath(p) { // 网络共享、设备路径、驱动器相对路径：看不清，不去解析也不记目录
			blind = true
			continue
		}
		c := pl.canon(resolve(env.Workdir, p))
		if !seen[c] {
			seen[c] = true
			paths = append(paths, c)
		}
	}
	return paths, blind
}

// LinkPaths 是 gate 要先解析联接的路径：工具的路径、命令会动的文件、按路径调用的程序、工作目录、允许目录、受保护目录
func LinkPaths(spec Spec, args jsjson.Value, env Env) []string {
	var out []string
	if spec.Paths != nil {
		for _, p := range spec.Paths(args) {
			if IsOpaquePath(p) { // 网络共享、设备路径一律问，不去解析：解析会真的连过去（SMB）
				continue
			}
			out = append(out, ToolPath(p, env.Workdir))
		}
	}
	for _, p := range shellPaths(env.Shell) {
		if p = expandHome(p); !OutsideForm(p) && !IsOpaquePath(p) {
			out = append(out, resolve(env.Workdir, p))
		}
	}
	out = append(append(append(out, env.Workdir), env.AutoApproveDirs...), env.ProtectedDirs...)
	for i, p := range out {
		out[i] = resolveCwd(p)
	}
	return out
}

func every(xs []string, pred func(string) bool) bool {
	for _, x := range xs {
		if !pred(x) {
			return false
		}
	}
	return true
}

func some(xs []string, pred func(string) bool) bool {
	for _, x := range xs {
		if pred(x) {
			return true
		}
	}
	return false
}

// onceOnly：只能允许一次的看不清——拆不开、语法不完整、动态执行 / 编码过的；看不清却没有类别的当最严的算
func onceOnly(env Env) bool {
	if env.ShellFailed {
		return true
	}
	sh := env.Shell
	if sh == nil || !sh.Opaque {
		return false
	}
	return len(sh.OpaqueKinds) == 0 || some(sh.OpaqueKinds, func(k string) bool { return k == "parse" || k == "dynamic" })
}

const (
	whyProtected   = "要碰 Sumika 自己的数据目录或放密钥的地方（里面有配置、密钥、记忆），每次都要所有者看过"
	whyUnclearPath = "这条命令要读的文件路径是算出来的（变量、括号、管道送来的、前面换过目录看不清），看不出读哪里"
	whySumikaDir   = "要写 .sumika 目录（里面的 hooks.json 是用户钩子，命令不经审批就会跑），每次都要所有者看过"
	whySelfCtl     = "这条命令会让 Sumika 自己的遥控器（sumika-ctl）改程序的状态（解除 MCP 隔离、退出、改群档位、开始或停止接收、备份……），每次都要所有者看过，最宽档也一样——免得她自己改安全边界"
	whySelfPort    = "这条命令碰到了 Sumika 自己的后台地址（主窗口的接口），每次都要所有者看过——免得她调自己的接口改设置、切档、批自己的审批"
	whyOpaqueDel   = "这条命令拆不开、或者是动态执行 / 编码过的（iex、-EncodedCommand、内联代码之类），看不清它到底会删什么，又带着删除动作，YOLO 下也要所有者看过"
	whyCatastrophe = "这条命令可能毁掉整台电脑的数据（格式化、分区、删盘符根……），每次都要所有者看过"
	whyUndeclared  = "这个工具没有声明自己会做什么"
	whyNotOwner    = "这一轮不是所有者发起的，不进自动放行，每次都要所有者看过"
	whyOpen        = "默认放行：只读地查公开资料"
	whyScoped      = "会调外部服务或派出子任务，可以只批这一次，也可以记住"
)

// Decide 是审批怎么判（policy.ts decide）。
// 判完以后再过一道 ask 规则（只会更严）：
//   - 拒绝：原样返回；
//   - 放行又被 ask 规则盖住：改成"这一次问"，不给"这个对话""记住"（每次都问）；
//   - 本来就问、又被 ask 规则盖住：问还是问，范围档收成"只这一次"（别在 ask 规则底下记下一条授权，撤了规则它就悄悄放行了），原因保留；
//   - 本来就只问一次的：原样返回。
//
// 所以 ask 规则不会让问变成放行，也不会多出范围档。顺序就是 拒绝 > ask > 放行（Claude Code 的 permissions：deny、ask、allow）。
func Decide(tool string, spec Spec, args jsjson.Value, env Env) Decision {
	d := decide(tool, spec, args, env)
	if d.Kind == "allow" || (d.Kind == "ask" && d.Limit != "once") {
		if r := MatchAsk(env.Rules, tool, args, env); r != nil {
			if d.Kind == "ask" {
				d.Sticky, d.Limit, d.RuleKey, d.RuleLabel = true, "once", r.Key, r.Raw
				return d
			}
			return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: "你设了“每次都问”的规则「" + r.Raw + "」", RuleKey: r.Key, RuleLabel: r.Raw}
		}
	}
	return d
}

func decide(tool string, spec Spec, args jsjson.Value, env Env) Decision {
	// 1. 渠道
	if spec.Audience == "owner-console" && !(env.Audience.Channel == "console" && env.Audience.PrivateOk) {
		return Decision{Kind: "deny", Reason: "这个工具只在主窗口给所有者用。"}
	}
	effects := spec.Effects
	if spec.Classify != nil {
		effects = spec.Classify(args)
	}
	// 1.5 主人写的拒绝规则：先于一切放行（场合已经在上面挡过了，这里只会让更多的被拒）
	if r := MatchDeny(env.Rules, tool, args, env); r != nil {
		return Decision{Kind: "deny", Reason: "你设的拒绝规则「" + r.Raw + "」不让这一步做。不要换个方式重试同一件事，先问所有者想怎么做。", RuleKey: r.Key, RuleLabel: r.Raw}
	}
	// 2. 只碰她自己的记忆
	if every(effects, func(e string) bool { return e == "self" }) {
		return Decision{Kind: "allow", Reason: "只碰自己的记忆", By: BySelf}
	}
	// 2.1 不是主人发起的：下面所有放行（默认档、授权、模式）都不吃（ADR-269）
	if env.NotOwner {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whyNotOwner}
	}
	// 2.2 默认放行档：可用场合在第 1 步挡过了
	if spec.Tier == TierOpen {
		return Decision{Kind: "allow", Reason: whyOpen, By: ByDefault}
	}
	// 3. 受保护路径
	command := ""
	if v := args.Obj["command"]; v.Kind == jsjson.String {
		command = v.Str
	}
	var paths []string
	rawNetwork := false
	var alts []string // 无盘符根相对路径（\data\x、/data/x）的另外几种解释
	if spec.Paths != nil {
		for _, p := range spec.Paths(args) {
			paths = append(paths, ToolPath(p, env.Workdir))
			rawNetwork = rawNetwork || IsOpaquePath(p) // 原文也看一遍：Clean 之后开头的两个斜杠在有的系统上会没
			alts = append(alts, rootedAlternatives(p, env.Workdir)...)
		}
	}
	// open 的 target 也是路径（不放进 paths：那会让 YOLO 把“打开工作目录里的文件”也放行），只用来判受保护和不透明
	protPaths := paths
	if tool == "open" {
		if t, ok := openTargetArg(args); ok {
			protPaths = append(append([]string(nil), paths...), ToolPath(t, env.Workdir))
			rawNetwork = rawNetwork || IsOpaquePath(t)
			alts = append(alts, rootedAlternatives(t, env.Workdir)...)
		}
	}
	pl := placeOf(env)
	recursive := tool == "grep" || tool == "find"
	// 读文件类命令里路径是算出来的：静态能代入已知值的（"$HOME\.ssh\…"、"$PWD\data\…"）按代入后的路径判受保护；
	// 代不出来的是“值不确定”，下面单独处理（普通的问，不是每次都问）
	hiddenProtected, hiddenUnclear := false, false
	if tool == "powershell" {
		hiddenProtected, hiddenUnclear = hiddenReadsVerdict(env)
	}
	if rawNetwork || some(alts, func(p string) bool {
		return TouchesProtected(p, env.Workdir, env.ProtectedDirs, recursive) || TouchesProtected(pl.canon(p), env.Workdir, env.ProtectedDirs, recursive)
	}) || some(protPaths, func(p string) bool {
		return TouchesProtected(p, env.Workdir, env.ProtectedDirs, recursive) || TouchesProtected(pl.canon(p), env.Workdir, env.ProtectedDirs, recursive)
	}) || (command != "" && commandTouchesProtected(command, env.ProtectedDirs, env.Shell == nil)) ||
		shellTargetsTouchProtected(env, command) || hiddenProtected || (tool == "powershell" && wildcardTouchesProtected(env, command)) ||
		(env.Shell != nil && some(shellPaths(env.Shell), func(p string) bool { return IsOpaquePath(expandHome(p)) })) {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whyProtected}
	}
	// 3.4 写 .sumika 目录（项目级用户钩子的配置在里面）：钩子命令不经审批就会跑，所以写它等于拿到一次不审批的执行，
	// 每次都要主人看过，YOLO 和"始终允许"都不例外。只读不拦（规格-用户钩子 第五节）
	if writesSumikaDir(tool, spec, args, env, effects, paths, command) {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whySumikaDir}
	}
	// 3.5 碰到后台自己的地址 + 端口
	if command != "" && TouchesSelfEndpoint(command, env.SelfPorts) {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whySelfPort}
	}
	// 3.5b 跑 Sumika 自己的遥控器 sumika-ctl 里会改状态的子命令（解除 MCP 隔离、退出、改群档位……）：任何档位都要主人看过，
	// 只能批这一次；只读的子命令（status、logs、mcp status……）不在此列，照原来的档位走（selfctl.go）
	if command != "" && SelfCtlNeedsAsk(command, env.Shell) {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whySelfCtl}
	}
	// 3.6 YOLO 下拆不开、语法不完整、动态执行又带删除动词
	if env.Mode == "yolo" && command != "" && HasDeleteVerb(command) && onceOnly(env) {
		return Decision{Kind: "ask", Sticky: true, Limit: "once", Reason: whyOpaqueDel}
	}
	covered := func() bool {
		sh := env.Shell
		if sh == nil {
			return false
		}
		if sh.Opaque && !(len(sh.OpaqueKinds) > 0 && every(sh.OpaqueKinds, func(k string) bool { return k == "piped" })) {
			return false
		}
		out, blind := OutsideTargets(env)
		var ok []string
		if env.Granted == "conversation" {
			ok = env.GrantDirs
		}
		return !blind && every(out, func(p string) bool {
			return some(ok, func(d string) bool { return IsInside(p, []string{d}, env.Workdir) })
		})
	}
	// 4. 灾难性命令
	if command != "" && IsCatastrophic(command) {
		if env.Granted == "conversation" && (tool != "powershell" || covered()) {
			return Decision{Kind: "allow", Reason: "这个对话里你允许过同一类命令", By: ByConversation}
		}
		return Decision{Kind: "ask", Sticky: true, Limit: "conversation", Reason: whyCatastrophe}
	}
	// 4.5 主人给过授权的同一类操作（mustAsk 的只认"这个对话"）
	mustAsk := (env.Shell != nil && len(env.Shell.MustAsk) > 0) || hiddenUnclear
	if (env.Granted == "conversation" || (env.Granted == "always" && !mustAsk)) && (tool != "powershell" || covered()) {
		if env.Granted == "always" {
			return Decision{Kind: "allow", Reason: "你设过始终允许这一类", By: ByAlways}
		}
		return Decision{Kind: "allow", Reason: "这个对话里你允许过这一类", By: ByConversation}
	}
	// 4.55 主人写的放行规则：排在受保护路径、灾难性命令、自己的端口和"不是主人发起的"后面，不能越过它们；
	// 命令还要拆得开、没有 mustAsk、碰到的外面都在允许目录里（和"记住"同一套条件）
	if r := MatchAllow(env.Rules, tool, args, env); r != nil && !mustAsk && (tool != "powershell" || covered()) {
		return Decision{Kind: "allow", Reason: "你写的放行规则「" + r.Raw + "」", By: ByRule, RuleKey: r.Key, RuleLabel: r.Raw}
	}
	// 4.6 范围档：和别的工具一样排在授权后面、按模式走；不碰路径，不进下面按路径、按命令的判断
	if spec.Tier == TierScoped {
		if env.Mode == "yolo" {
			return Decision{Kind: "allow", Reason: "YOLO：外部工具和子任务不问", By: ByMode}
		}
		if env.Mode == "auto" && every(effects, func(e string) bool { return e == "read" }) {
			return Decision{Kind: "allow", Reason: "自动模式：只读的子任务", By: ByMode}
		}
		return Decision{Kind: "ask", Reason: whyScoped}
	}
	readEffects := every(effects, func(e string) bool { return e == "read" })
	shellReadOnly := env.Shell != nil && env.Shell.ReadOnly
	// PowerShell 命令以拆开命令的分析为准（Judge 的 ReadOnly：没有任何改动、没有看不清的部分，运行脚本、调方法、动态执行都会让它为假）。
	// 以前还要同时过 IsReadOnlyCommand 的命令名白名单，Get-PSDrive 这类不在名单里的只读命令落到兜底去问（2026-11-08 实测）。
	// 受保护目录、自己的端口、灾难性命令在上面已经先判过了。
	if tool == "powershell" && shellReadOnly {
		readEffects = true
	}
	// 读文件类命令的路径值不确定（$f、(Join-Path …)、来自管道、前面换过目录看不清）：不当“只读放行”，自动和 YOLO 下问；
	// “这个对话”授权照样管用（上面 4.5 已经先过了），不是每次都问
	if tool == "powershell" && hiddenUnclear {
		shellReadOnly, readEffects = false, false
		if env.Mode == "yolo" {
			return Decision{Kind: "ask", Reason: whyUnclearPath}
		}
	}
	// 5. 按模式
	if env.Mode == "yolo" {
		if !env.Declared {
			return Decision{Kind: "ask", Reason: "这个工具没有声明自己会做什么，YOLO 下也要问"}
		}
		if readEffects && (spec.Classify == nil || shellReadOnly || spec.Plain) {
			return Decision{Kind: "allow", Reason: "YOLO：只读", By: ByMode}
		}
		if spec.Classify == nil {
			if len(paths) > 0 && every(paths, pl.inside) {
				return Decision{Kind: "allow", Reason: "YOLO：只写工作目录或允许目录里的文件", By: ByMode}
			}
			return Decision{Kind: "ask", Reason: "要写到工作目录外面，YOLO 下也要所有者看过"}
		}
		sh := env.Shell
		if sh == nil || sh.Opaque {
			if command != "" && HasDeleteVerb(command) {
				return Decision{Kind: "ask", Limit: "conversation", Reason: "这条命令看不清它会删到哪（目标从管道来、没写死、参数认不出之类），YOLO 下也要所有者看过"}
			}
			return Decision{Kind: "ask", Reason: "这条命令看不清它到底会做什么（目标没写死、动态执行、内联代码之类），YOLO 下也要所有者看过"}
		}
		var fetched []Fetch
		for _, f := range sh.Fetches {
			if !f.Declarable || !contains(env.ProjectTools[linkKey(resolve(env.Workdir, f.Dir))], f.Name) {
				fetched = append(fetched, f)
			}
		}
		if len(fetched) > 0 {
			names := make([]string, len(fetched))
			allDecl, anyOut := true, false
			for i, f := range fetched {
				names[i] = f.Name
				allDecl = allDecl && f.Declarable
				anyOut = anyOut || !pl.shellInside(f.Dir)
			}
			why := "项目的 package.json 里没写它"
			if !allDecl {
				why = "写了版本号或网址、或者是 uvx、npm create、pnpm dlx 这类总是现下载的，不按项目里装好的算"
			} else if anyOut {
				why = "它指到了工作目录外面的项目（--prefix 之类）"
			}
			return Decision{Kind: "ask", Reason: "要下载 " + strings.Join(names, "、") + " 来跑（" + why + "），YOLO 下也要所有者看过"}
		}
		if len(sh.MustAsk) > 0 {
			return Decision{Kind: "ask", Reason: sh.MustAsk[0] + "，YOLO 下也要所有者看过"}
		}
		if some(sh.Runs, func(p string) bool { return !pl.shellInside(p) }) {
			return Decision{Kind: "ask", Reason: "要运行工作目录外面的程序或脚本，YOLO 下也要所有者看过"}
		}
		if some(sh.FileTargets, func(p string) bool { return !pl.shellInside(p) }) {
			return Decision{Kind: "ask", Reason: "这条命令会动工作目录外面的文件，YOLO 下也要所有者看过"}
		}
		return Decision{Kind: "allow", Reason: "YOLO：看得清、只碰工作目录里的东西", By: ByMode}
	}
	// 手动：只有声明本身就是只读的工具放行，PowerShell 哪怕命令只读也问
	if env.Mode == "manual" {
		if every(effects, func(e string) bool { return e == "read" }) && (spec.Classify == nil || spec.Plain) {
			return Decision{Kind: "allow", Reason: "只读", By: ByMode}
		}
		if env.Declared {
			return Decision{Kind: "ask"}
		}
		return Decision{Kind: "ask", Reason: whyUndeclared}
	}
	// 自动：按这次的参数收窄
	if readEffects && (spec.Classify == nil || shellReadOnly || spec.Plain) {
		return Decision{Kind: "allow", Reason: "自动模式：只读", By: ByMode}
	}
	if env.Mode == "auto" && every(effects, func(e string) bool { return e == "read" || e == "write" }) && len(paths) > 0 && every(paths, pl.inside) {
		return Decision{Kind: "allow", Reason: "自动模式：在允许的目录里", By: ByMode}
	}
	if env.Mode == "auto" && spec.Plain && every(effects, func(e string) bool { return e == "read" || e == "write" }) {
		return Decision{Kind: "allow", Reason: "自动模式：不碰文件和命令的读写", By: ByMode}
	}
	// 6. 兜底
	if env.Declared {
		return Decision{Kind: "ask"}
	}
	return Decision{Kind: "ask", Reason: whyUndeclared}
}

// Kind 是"同一类"（grants.ts kindOf）：命令按用到的命令名，写文件按目录，子任务按代理类型，别的按工具名；认不出就 nil。
// mcp_call 的同一类由 Gate.CheckMCP 按服务器加原始工具名算，这里拿不到解析后的目标
type Kind struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// KindOf 照 grants.ts kindOf
func KindOf(tool string, args jsjson.Value, workdir string, shell *Verdict) *Kind {
	if tool == "powershell" {
		if shell == nil || len(shell.Names) == 0 {
			return nil
		}
		if SelfCtlNeedsAsk(JSString(args, "command", ""), shell) {
			return nil // 会改程序状态的 sumika-ctl 命令不产生能被“这个对话”“始终允许”授权的键：只能一次一批，也不和只读的共用键
		}
		prefix := ""
		if IsCatastrophic(JSString(args, "command", "")) {
			prefix = "catastrophic:"
		}
		return &Kind{Key: prefix + "powershell:" + strings.Join(shell.Names, " + "), Label: "「" + strings.Join(shell.Names, "、") + "」"}
	}
	if tool == "write" || tool == "edit" {
		dir := win32Dir(ToolPath(PathArg(args), workdir))
		return &Kind{Key: tool + ":" + strings.ToLower(dir), Label: "「" + dir + " 里的文件」"}
	}
	if tool == "task" {
		v := args.Obj["agent"]
		agent := v.Str
		if v.Kind != jsjson.String {
			agent = ""
		}
		if !agentName.MatchString(agent) {
			return nil // 类型错、名字怪的不记：只能允许一次，Manager 还会再查一遍这个代理存不存在
		}
		return &Kind{Key: "task:" + agent, Label: "「派 " + agent + " 子任务」"}
	}
	return &Kind{Key: "tool:" + tool, Label: "「" + tool + "」"}
}

// win32Dir 像 path.win32.dirname（这里只会拿到 ToolPath 给的绝对路径）
func win32Dir(p string) string {
	d := filepath.Dir(p)
	return d
}

var neverAlways = set(`rm del erase rd rmdir ri remove-item powershell pwsh cmd python py node bun bunx npx npm
iex invoke-expression start-process saps start curl irm iwr invoke-restmethod invoke-webrequest reg sc net shutdown format diskpart git
format-volume clear-disk initialize-disk remove-partition bcdedit vssadmin cipher takeown set-mppreference`)

// CanAlways 照 grants.ts canAlways：没登记的工具、复杂的命令（管道、分号、重定向、看不清的）、名单里的命令、mustAsk 的都不能"始终允许"
func CanAlways(tool string, args jsjson.Value, declared bool, shell *Verdict) bool {
	if !declared {
		return false
	}
	if tool != "powershell" {
		return true
	}
	if pipeEtc.MatchString(JSString(args, "command", "")) || shell == nil || len(shell.Unclear) > 0 {
		return false
	}
	if len(shell.MustAsk) > 0 || len(shell.HiddenReads) > 0 || shell.CwdUnknown { // 路径值不确定的只能“这个对话”
		return false
	}
	return every(shell.Names, func(n string) bool { return !neverAlways[n] })
}

// Resolve 像 Node 的 path.win32.resolve(base, p)；ResolveCwd 像 path.resolve(p)；LinkKey 是 Real 的键（resolve 过、小写）；IsAbsWin 像 path.win32.isAbsolute
func Resolve(base, p string) string { return resolve(base, p) }

// ResolveCwd 见 Resolve
func ResolveCwd(p string) string { return resolveCwd(p) }

// LinkKey 见 Resolve
func LinkKey(p string) string { return linkKey(p) }

// IsAbsWin 见 Resolve
func IsAbsWin(p string) bool { return isAbsWin(p) }

// sumikaDirText 认命令文字里的 .sumika 路径段：前后是分隔符、引号、空白或字符串两端
var sumikaDirText = regexp.MustCompile(`(?i)(^|[\\/'"\s=;(,:])\.sumika($|[\\/'"\s;),])`)

// hasSumikaSegment 是这个路径有没有一段叫 .sumika（不分大小写、正反斜杠都认）
func hasSumikaSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' }) {
		if strings.EqualFold(strings.TrimRight(seg, ". "), ".sumika") {
			return true
		}
	}
	return false
}

// writesSumikaDir：非只读的调用碰到了 .sumika 路径段（工具路径、命令拆出的目标、命令文字、联接解开后的真实路径都看）
func writesSumikaDir(tool string, spec Spec, args jsjson.Value, env Env, effects, paths []string, command string) bool {
	readOnly := every(effects, func(e string) bool { return e == "read" || e == "self" }) &&
		(spec.Classify == nil || spec.Plain || (env.Shell != nil && env.Shell.ReadOnly))
	if readOnly {
		return false
	}
	pl := placeOf(env)
	hit := func(p string) bool { return hasSumikaSegment(p) || hasSumikaSegment(pl.canon(p)) }
	if some(paths, hit) {
		return true
	}
	if command != "" && sumikaDirText.MatchString(command) {
		return true
	}
	if sh := env.Shell; sh != nil {
		for _, group := range [][]string{sh.Targets, sh.FileTargets, sh.Runs} {
			if some(group, func(p string) bool { return hit(resolve(env.Workdir, expandHome(p))) }) {
				return true
			}
		}
	}
	return false
}
