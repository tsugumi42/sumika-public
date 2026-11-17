// Package applog 是后台和桌面程序日志的统一出口（规格-日志分级第四节）。
//
// 一行的格式是 `时间 级别 模块 消息`，级别 DEBUG|INFO|WARN|ERROR，模块来自下面的封闭名单（Module 的字段没有导出，
// 名单外的字符串造不出 Module）。Root 持有写文件的 filelog.Logger 和当前级别；模块拿到的是注入好模块名的函数或 *slog.Logger，
// 不自己填模块名。
//
// 打码由入口用 SetRedact 注入一个函数，每条路只打一次（同一行打两遍不幂等：HTTP 分支会被改写）：
//   - Logf、At、Warnf、Errorf、Debugf 这些 Printf 形状的出口：级别开着时对格式化后的消息正文打码再写。
//     不在整行上打码，是因为 maskModelFailure 的模板行首锚定，行首有 `时间 级别 模块` 就认不出了。
//     默认安全：直接拿 Root 写日志的入口（看门狗、桌面启动行、sumika-go 的启动行）不用自己再打码。
//   - Masked：调用方已经自己打过码的出口（app.levelFor 带着装配的令牌表、host.logf 和 shell.logf 用宿主的），这里不再打。
//     新代码不要用它，除非自己真的在打码；用错了就是漏。
//   - Slog 出口（slog 默认 logger、标准库 log 包都从这里进来，第三方的行也是）：Handle 对消息、属性名和每个属性值各打一次
//     （属性值先打码再截断，别把令牌切成两半留一截）。
//
// 没注入打码函数时原样写（只有测试和没有数据目录的情况）；入口一律要注入，接线的函数是 app.RouteLogs，有测试盯着。
package applog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/filelog"
)

// Module 是日志的模块标签。字段没有导出：只能用下面的常量。
type Module struct{ name string }

// String 给出模块标签（写进日志行里的那个词）
func (m Module) String() string { return m.name }

// 封闭名单。新增模块在这里加一行，并进 all。
var (
	App        = Module{"app"}
	Desk       = Module{"desk"}
	Host       = Module{"host"}
	Go         = Module{"go"}
	Ctl        = Module{"ctl"}
	Watch      = Module{"watch"}
	Turn       = Module{"turn"}
	Channel    = Module{"channel"}
	Snowluma   = Module{"snowluma"}
	Linkplugin = Module{"linkplugin"}
	Memory     = Module{"memory"}
	Memplugin  = Module{"memplugin"}
	Memwrite   = Module{"memwrite"}
	Watchdog   = Module{"watchdog"}
	Approval   = Module{"approval"}
	Taskrun    = Module{"taskrun"}
	Store      = Module{"store"}
	Lifetools  = Module{"lifetools"}
	Askowner   = Module{"askowner"}
	Proactive  = Module{"proactive"}
	Mediafetch = Module{"mediafetch"}
	Llm        = Module{"llm"}
	Whisper    = Module{"whisper"}
	Remind     = Module{"remind"}
	Loop       = Module{"loop"}
	Misc       = Module{"misc"} // 没有归属的日志（第三方、标准库 log 包）
	Applog     = Module{"applog"}
	Selfcheck  = Module{"selfcheck"} // 运行时自检的状态变化和写文件失败
	Backup     = Module{"backup"}    // 备份：每天自动备份、手动备份、轮换、占用提醒
)

var all = []Module{App, Desk, Host, Go, Ctl, Watch, Turn, Channel, Snowluma, Linkplugin, Memory, Memplugin,
	Memwrite, Watchdog, Approval, Taskrun, Store, Lifetools, Askowner, Proactive, Mediafetch, Llm, Whisper, Remind, Loop, Misc, Applog, Selfcheck, Backup}

// Names 是全部模块名，给配置校验用（logs.modules 的键只许写这些）
func Names() []string {
	out := make([]string, 0, len(all))
	for _, m := range all {
		out = append(out, m.name)
	}
	return out
}

// ByName 按名字找模块（配置里写的）；名单外返回 false
func ByName(name string) (Module, bool) {
	for _, m := range all {
		if m.name == name {
			return m, true
		}
	}
	return Module{}, false
}

// levelWords 是文件里的级别词，和 filelog.ParseLine 认的一致
var levelWords = map[slog.Level]string{
	slog.LevelDebug: "DEBUG",
	slog.LevelInfo:  "INFO",
	slog.LevelWarn:  "WARN",
	slog.LevelError: "ERROR",
}

// levelWord 把任意 slog 级别归到四个级别词里：自定义级别（slog.Level(12)、Warn+1）也要有级别词，
// 否则行里级别位是空的，读取侧把它当旧格式行，--level 和 --errors 都过滤不到。
// 区间：<=Debug 归 DEBUG，<Warn 归 INFO，<Error 归 WARN，其余归 ERROR。
func levelWord(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return levelWords[slog.LevelDebug]
	case l < slog.LevelWarn:
		return levelWords[slog.LevelInfo]
	case l < slog.LevelError:
		return levelWords[slog.LevelWarn]
	}
	return levelWords[slog.LevelError]
}

// ParseLevel 认配置里的级别（debug|info|warn|error，大小写都行）
func ParseLevel(s string) (slog.Level, bool) {
	for l, w := range levelWords {
		if strings.EqualFold(s, w) {
			return l, true
		}
	}
	return 0, false
}

// Root 是一个进程一份的日志出口。file 为 nil 时所有行都丢（测试和没有数据目录的情况）。
type Root struct {
	file *filelog.Logger
	now  func() time.Time // 行的时间；测试换假时钟

	mu     sync.RWMutex
	min    slog.Level            // 全局最低级别，默认 INFO
	mods   map[string]slog.Level // 按模块覆盖
	redact func(string) string   // Slog 路径的打码函数；nil = 不打码
}

// New 建一个出口。级别默认 INFO：调试级别默认不写（规格第十节）。
func New(file *filelog.Logger, now func() time.Time) *Root {
	if now == nil {
		now = time.Now
	}
	return &Root{file: file, now: now, min: slog.LevelInfo, mods: map[string]slog.Level{}}
}

// scanLimit 是过滤时最多扫的行数（规格第八节）：按行数不按字节
const scanLimit = 20000

// Filter 是 sumika-ctl logs 的过滤：Level 是最低级别（""=不限；debug|info|warn|error），Module 是模块名（""=不限）
type Filter struct{ Level, Module string }

// Result 是一次过滤查询：Lines 是最后 n 行（旧的在前）；Scanned 扫了多少行；Matched 匹配多少行；
// Old 是扫到的旧格式行（没有级别）数，过滤时它们不参与
type Result struct {
	Lines            []string
	Scanned, Matched int
	Old              int
}

// Query 在最近 scanLimit 行里按 f 过滤，取最后 n 行。什么都不过滤时旧行照常保留（和 Tail 一样）
func (r *Root) Query(n int, f Filter) (Result, error) {
	var res Result
	if r.file == nil {
		return res, nil
	}
	raw, err := r.file.TailLines(scanLimit)
	if err != nil {
		return res, err
	}
	res.Scanned = len(raw)
	filtering := f.Level != "" || f.Module != ""
	minLevel, _ := ParseLevel(f.Level)
	var kept []string
	for _, line := range raw {
		if !filtering {
			kept = append(kept, line)
			continue
		}
		e, ok := filelog.ParseLine(line)
		if !ok || e.Level == "" {
			res.Old++
			continue
		}
		if lv, _ := ParseLevel(e.Level); f.Level != "" && lv < minLevel {
			continue
		}
		if f.Module != "" && e.Module != f.Module {
			continue
		}
		kept = append(kept, line)
	}
	res.Matched = len(kept)
	if n > 0 && len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	res.Lines = kept
	return res, nil
}

// Tail 给读取侧（sumika-ctl logs）用：最后 n 行原文，旧的在前
func (r *Root) Tail(n int) ([]string, error) {
	if r.file == nil {
		return nil, nil
	}
	return r.file.Tail(n)
}

// Close 关掉日志文件（进程退出的最后一步）；没有文件时什么也不做
func (r *Root) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

// SetLevel 设全局最低级别（配置热刷新时调）
func (r *Root) SetLevel(l slog.Level) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.min = l
}

// SetModules 整体替换按模块的覆盖。键必须是名单里的名字（调用方用 ByName 校验）
func (r *Root) SetModules(levels map[string]slog.Level) {
	next := make(map[string]slog.Level, len(levels))
	for k, v := range levels {
		next[k] = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mods = next
}

// SetRedact 注入打码函数（令牌、会话号、长数字）。入口在写第一行之前调（接线见 app.RouteLogs）。
// 函数要能并发调用，并且对一小段文字（消息正文、属性名或一个属性值）起作用；Masked 出口不经过它。
func (r *Root) SetRedact(f func(string) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.redact = f
}

func (r *Root) redactor() func(string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.redact
}

// Enabled 看这个模块的这一级别写不写：先看模块覆盖，没有就看全局
func (r *Root) Enabled(m Module, l slog.Level) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	min := r.min
	if v, ok := r.mods[m.name]; ok {
		min = v
	}
	return l >= min
}

// write 拼出一行交给 filelog（它加时间、转义换行、选文件）。body 是 `消息`，级别和模块在这里加上。
func (r *Root) write(m Module, at time.Time, l slog.Level, msg string) {
	if r.file == nil {
		return
	}
	name := m.name
	if name == "" {
		name = Misc.name // 包外造出的零值 Module：不写空模块名，读取侧会把它认成坏行
	}
	r.file.Line(at, levelWord(l)+" "+name+" "+msg)
}

// logfAt 给出一个固定级别、固定模块的 Printf 形状函数。级别不够时直接返回，不做 Sprintf 和打码。
// masked 为 true 表示调用方已经打过码，这里不再打。
func (r *Root) logfAt(m Module, l slog.Level, masked bool) func(string, ...any) {
	return func(format string, args ...any) {
		if !r.Enabled(m, l) {
			return
		}
		msg := fmt.Sprintf(format, args...)
		if !masked {
			if red := r.redactor(); red != nil {
				msg = red(msg)
			}
		}
		r.write(m, r.now(), l, msg)
	}
}

// Masked 给出调用方已经自己打过码的同形函数：正文原样写，不再过 SetRedact 注入的函数。
// 只给自己带令牌表打码的装配处用（见包注释）；别处用它会把没打码的文字写进文件。
func (r *Root) Masked(m Module, l slog.Level) func(string, ...any) { return r.logfAt(m, l, true) }

// Logf 是现有调用点用的形状（签名和 app.Options.Logf 一样），级别 INFO
func (r *Root) Logf(m Module) func(string, ...any) { return r.logfAt(m, slog.LevelInfo, false) }

// At 给出任意固定级别的同形函数（组件需要 WARN、ERROR 时用）
func (r *Root) At(m Module, l slog.Level) func(string, ...any) { return r.logfAt(m, l, false) }

// Warnf 是 WARN 级别的同形函数（规格 5.1：出错或降级，但系统会自己接上）
func (r *Root) Warnf(m Module) func(string, ...any) { return r.logfAt(m, slog.LevelWarn, false) }

// Errorf 是 ERROR 级别的同形函数（规格 5.1：有数据会丢、或要人处理、或最终失败）
func (r *Root) Errorf(m Module) func(string, ...any) { return r.logfAt(m, slog.LevelError, false) }

// Debugf 是 DEBUG 级别的同形函数（每步细节，默认不写）
func (r *Root) Debugf(m Module) func(string, ...any) { return r.logfAt(m, slog.LevelDebug, false) }

// Slog 给新的调用点用：带模块的 slog.Logger，属性写成 ` key=value`（值超过 500 字符截断）
func (r *Root) Slog(m Module) *slog.Logger { return slog.New(&handler{r: r, m: m}) }

// maxAttrValue 是属性值写进整行前的长度上限（规格第十一节）
const maxAttrValue = 500

// handler 是 slog.Handler：级别过滤在 Enabled 里做（先于格式化），Handle 只拼消息和属性
type handler struct {
	r     *Root
	m     Module
	attrs []slog.Attr
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return h.r.Enabled(h.m, l) }

func (h *handler) Handle(_ context.Context, rec slog.Record) error {
	red := h.r.redactor()
	if red == nil {
		red = func(s string) string { return s }
	}
	var b strings.Builder
	b.WriteString(red(rec.Message))
	for _, a := range h.attrs {
		writeAttr(&b, a, red)
	}
	rec.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a, red)
		return true
	})
	h.r.write(h.m, h.r.now(), rec.Level, b.String())
	return nil
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	next := &handler{r: h.r, m: h.m, attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
	return next
}

// WithGroup 不支持分组：日志行是平的（规格第四节只定义 key=value）
func (h *handler) WithGroup(string) slog.Handler { return h }

// writeAttr 写一个属性：先展开 LogValuer（不展开 String() 只得到一个占位），属性名和值各打一次码，值打码后再截断
func writeAttr(b *strings.Builder, a slog.Attr, red func(string) string) {
	a.Value = a.Value.Resolve()
	v := red(a.Value.String())
	if r := []rune(v); len(r) > maxAttrValue {
		v = string(r[:maxAttrValue]) + "…"
	}
	b.WriteString(" " + red(a.Key) + "=" + v)
}
