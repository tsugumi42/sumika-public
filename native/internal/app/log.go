package app

// 装配处统一过滤日志：令牌、会话号、长数字都打码。模型错误已被下层转成文字，
// 只从固定错误前缀提取 HTTP 状态码，余下正文整段丢掉，不尝试清洗聊天原文。

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/applog"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/secrets"
)

// maskConv 是日志里的会话名：群号、她的号、对方的号都打码
func maskConv(c types.Conversation) string {
	if c.Scene == types.SceneGroup {
		return c.Channel + ":group:" + config.MaskID(c.ID)
	}
	return c.Channel + ":private:" + config.MaskID(c.Self) + ":" + config.MaskID(c.ID)
}

var conversationID = regexp.MustCompile(`qq:(?:group|private):[0-9]+(?::[0-9]+)?`)
var groupID = regexp.MustCompile(`(?:group:|群\s*)[0-9]+`)
var longNumber = regexp.MustCompile(`[0-9]{5,}`)

// userProfile 认 Windows 用户目录的前缀（盘符:\Users\<用户名>，两种斜杠都认），换成 %USERPROFILE%：
// 用户名常是真名，会随日志和状态截图传出去；后面的相对路径照留，排错够用。不在用户目录下的路径（D:\Projects、数据目录）不动。
var userProfile = regexp.MustCompile(`(?i)[A-Z]:[\\/]+Users[\\/]+[^\\/\s:"'<>|*?]+`)
var httpFailure = regexp.MustCompile(`(?:^|出错[：:]\s*)([1-5][0-9]{2})(?:[: ]|$)`)

// modelErrorLogs 是下层把模型错误连同正文写出来的固定模板（channel/settle.go、turn 的 hub.go 和 compact.go）。
// 只认行首的整句模板，不按子串认："收拾上一个进程没收尾的回合出错：database is locked" 不是模型错误，
// 不能整段换成"模型请求失败"。模板之后到行尾是供应商原文，整段丢掉。
var modelErrorLogs = []*regexp.Regexp{
	regexp.MustCompile(`^回合 \S+ 第 [0-9]+ 步的模型请求出错：`),
	regexp.MustCompile(`^会话 \S+ 压缩的摘要请求出错：`),
	regexp.MustCompile(`^会话 \S+ 压缩没成（`),
	regexp.MustCompile(`^会话 \S+ 后台提前压缩没用上：`),
	regexp.MustCompile(`^会话 \S+ 计划 \S+：error（回合出错：`),
}

// maskModelFailure 认出模型错误就只留类别和能提取的 HTTP 状态码，认不出原样返回。
func maskModelFailure(s string) string {
	for _, re := range modelErrorLogs {
		at := re.FindStringIndex(s)
		if at == nil {
			continue
		}
		if m := httpFailure.FindStringSubmatch(s[at[1]:]); m != nil {
			return s[:at[1]] + "网关 HTTP 错误，状态码 " + m[1]
		}
		return s[:at[1]] + "模型请求失败（" + modelFailureKind(s[at[1]:]) + "）"
	}
	return s
}

// modelFailureKinds：从供应商原文里认类别，只留类别、不留原文（静默失败盘点 二-8：以前全压成一句"模型请求失败"）
var modelFailureKinds = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`(?i)deadline exceeded|timeout|timed out|超时`), "超时"},
	{regexp.MustCompile(`(?i)connection refused|no such host|dial tcp|连不上`), "连不上网关"},
	{regexp.MustCompile(`(?i)\bEOF\b|connection reset|broken pipe|断开`), "连接断开"},
	{regexp.MustCompile(`(?i)rate.?limit|too many requests|quota|usage limit|限流|额度`), "限流或额度用完"},
	{regexp.MustCompile(`(?i)context.?length|too long|maximum context|prompt is too long|上下文`), "上下文超长"},
	{regexp.MustCompile(`(?i)content.?filter|safety|moderation|refus`), "内容被拦"},
	{regexp.MustCompile(`(?i)context canceled|已取消`), "被取消"},
	{regexp.MustCompile(`(?i)model.*(not found|does not exist|unavailable)|模型不可用`), "模型不可用"},
}

func modelFailureKind(tail string) string {
	for _, k := range modelFailureKinds {
		if k.re.MatchString(tail) {
			return k.kind
		}
	}
	return "原因未分类"
}

// newLogf 是 app 自己的出口（模块 app）
func (a *App) newLogf() func(string, ...any) { return a.logfFor(applog.App) }

// logfFor 给某个模块的组件用的出口（规格-日志分级 4.2）：opt.Logs 设了就写进 applog 的那个模块，否则退回 opt.Logf（测试）
// 或 log.Printf。令牌原文一律换成 ***，打码只做这一次（Printf 形状的出口 applog 不再打码，见 applog 包注释）。
func (a *App) logfFor(m applog.Module) func(string, ...any) { return a.levelFor(m, slog.LevelInfo) }

// warnFor、errorFor 是固定级别的同模块出口；没有 Logs（测试）时退回 logfFor，级别信息丢掉，行为不变
func (a *App) warnFor(m applog.Module) func(string, ...any) { return a.levelFor(m, slog.LevelWarn) }

func (a *App) errorFor(m applog.Module) func(string, ...any) { return a.levelFor(m, slog.LevelError) }

func (a *App) debugFor(m applog.Module) func(string, ...any) { return a.levelFor(m, slog.LevelDebug) }

// RouteLogs 把一个进程的日志出口接好（规格-日志分级 4.2），sumika-go 和桌面外壳共用：
// 注入打码函数（Printf 形状和 slog 两条路都用它），再让 slog 默认 logger 和标准库 log 包都写进 root 的 misc 模块。
// 返回的函数把 slog 和 log 的默认值还原成调用前的样子（log 的输出和格式也要还原：slog.SetDefault 会改 log.SetOutput，
// 光换回原来的 slog 默认 logger 不够，行还会经 log 流进已经关掉的 root）。
// slog.SetDefault 是进程全局的：只在程序入口调，不放进 Open（测试里会开很多个 App）。
func RouteLogs(root *applog.Root, redact func(string) string) (restore func()) {
	root.SetRedact(redact)
	oldSlog, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(root.Slog(applog.Misc))
	return func() {
		slog.SetDefault(oldSlog)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}
}

// ApplyLogSettings 把 logs 配置交给出口：全局级别和按模块覆盖（规格-日志分级第六节）。
// 写错的模块名由配置校验（config.LogSettings.check）拦在读配置时；这里遇到名单外的名字只跳过，不再报。没有出口时不做。
// 后台每次打开（app.Open）、守护进程每次取参数都会调，所以改了 logs.* 以后下一次打开或下一次检查就生效，不需要重启整个程序。
func ApplyLogSettings(r *applog.Root, c config.LogSettings) {
	if r == nil {
		return
	}
	lv, ok := applog.ParseLevel(c.Level)
	if !ok {
		lv = slog.LevelInfo
	}
	r.SetLevel(lv)
	mods := map[string]slog.Level{}
	for name, word := range c.Modules {
		if _, ok := applog.ByName(name); !ok {
			continue
		}
		if l, ok := applog.ParseLevel(word); ok {
			mods[name] = l
		}
	}
	r.SetModules(mods)
}

// levelFor 是所有模块出口的唯一做法：级别没开时在 Sprintf 和打码之前就返回（A3：关着的 DEBUG 不做任何格式化），
// 开着才格式化、打码、写。没有 Logs（测试）时没有级别可判，照 opt.Logf 或 log.Printf 全写。
func (a *App) levelFor(m applog.Module, l slog.Level) func(string, ...any) {
	root := a.opt.Logs
	if root == nil {
		return a.redacted(a.opt.Logf)
	}
	out := a.redacted(root.Masked(m, l)) // redacted 已经按装配的令牌表打过码，Root 不再打第二遍
	return func(format string, args ...any) {
		if !root.Enabled(m, l) {
			return
		}
		out(format, args...)
	}
}

// redacted 把一个出口包成先打码再写：令牌原文换成 ***；out 为 nil 时用 log.Printf
func (a *App) redacted(out func(string, ...any)) func(string, ...any) {
	if out == nil {
		out = log.Printf
	}
	return func(format string, args ...any) {
		out("%s", RedactLog(fmt.Sprintf(format, args...), a.tokens))
	}
}

// RedactLog 供后台和桌面宿主共用；已知令牌必须由装配方传入，不靠正则猜任意秘密。
func RedactLog(s string, tokens []string) string {
	s = maskModelFailure(s)
	s = userProfile.ReplaceAllLiteralString(s, "%USERPROFILE%")
	for _, token := range tokens {
		if token != "" {
			s = strings.ReplaceAll(s, token, "***")
		}
	}
	s = conversationID.ReplaceAllStringFunc(s, func(conv string) string {
		parts := strings.Split(conv, ":")
		for i := 2; i < len(parts); i++ {
			parts[i] = config.MaskID(parts[i])
		}
		return strings.Join(parts, ":")
	})
	s = groupID.ReplaceAllStringFunc(s, func(group string) string {
		at := strings.IndexAny(group, "0123456789")
		return group[:at] + config.MaskID(group[at:])
	})
	return longNumber.ReplaceAllStringFunc(s, config.MaskID)
}

// LogRedactor 保留进程见过的令牌，切后台重读配置后仍能遮住旧后台的收尾错误。
// 宿主刷新，外壳也可并发读；零值隐藏自由文本，filelog 不接触配置或密钥。
type LogRedactor struct {
	mu     sync.RWMutex
	tokens []string
	ready  bool
	cfgErr string // 配置文件本身有错时的校验文字（只取第一行）；令牌不可用等情况不记
}

// configLoadError 标记 logTokens 失败是因为配置读不进来；这类错误是我们自己的校验文字，可以给用户看。
type configLoadError struct{ err error }

func (e configLoadError) Error() string { return e.err.Error() }
func (e configLoadError) Unwrap() error { return e.err }

var backtickValue = regexp.MustCompile("`[^`]*`")

// configErrText 取校验文字的第一行并限长，再过一遍已知令牌的遮蔽。
func configErrText(err error, tokens []string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 300 {
		line = string(r[:300]) + "…"
	}
	// YAML 类型错误会把出错的值放在反引号里，值可能是误贴进来的密钥：一律换成省略号
	line = backtickValue.ReplaceAllString(line, "`…`")
	return RedactLog(line, tokens)
}

func (r *LogRedactor) Refresh(opt Options) {
	tokens, err := logTokens(opt)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = err == nil
	r.cfgErr = ""
	for _, token := range tokens {
		if token != "" && !slices.Contains(r.tokens, token) {
			r.tokens = append(r.tokens, token)
		}
	}
	var cfg configLoadError
	if errors.As(err, &cfg) {
		r.cfgErr = configErrText(cfg.err, r.tokens)
	}
}

// Redact 取不到令牌快照时不交出详情，避免配置错误本身携带未知令牌。
func (r *LogRedactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s != "" && !r.ready {
		if r.cfgErr != "" {
			// 配置文件有错：带着这段校验文字的行照常给；其余行只说明原因，不放行自由文本。
			if masked := RedactLog(s, r.tokens); strings.Contains(masked, r.cfgErr) {
				return masked
			}
			return "诊断详情已隐藏（配置文件有错：" + r.cfgErr + "）"
		}
		return "诊断详情已隐藏（脱敏配置或令牌不可用）"
	}
	return RedactLog(s, r.tokens)
}

func logTokens(opt Options) ([]string, error) {
	location, err := ResourceRoot(opt.Root)
	if err != nil {
		return nil, err
	}
	root := location.Dir
	cfg, err := config.LoadIn(root, opt.DataDir)
	if err != nil {
		return nil, configLoadError{err}
	}
	data := opt.DataDir
	if data == "" {
		data = cfg.DataDirIn(root)
	}
	var tokens []string
	var stored *secrets.Store
	readStore := false
	for _, account := range cfg.QQ.Accounts {
		if strings.TrimSpace(account.Token) != "" {
			tokens = append(tokens, account.Token)
			token, err := config.ExpandEnv(account.Token, nil)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
			continue
		}
		if !readStore {
			stored, err = secrets.Open((paths.Paths{Root: data}).SecretsFile())
			if err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return nil, err
			}
			readStore = true
		}
		if stored != nil {
			token, _ := stored.Get("qq." + string(account.ID) + ".token")
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

// clock 是 app 给的钟（types.Clock）：Now 可换（Options.Now），计时器用真的
type clock struct{ now func() time.Time }

func (c clock) Now() time.Time { return c.now() }

func (c clock) NewTimer(d time.Duration) types.Timer { return appTimer{time.NewTimer(d)} }

type appTimer struct{ t *time.Timer }

func (t appTimer) C() <-chan time.Time { return t.t.C }
func (t appTimer) Stop() bool          { return t.t.Stop() }
