// Package plugintrace 是每个插件自己的结构化日志（规格-插件两层 第二点五节，格式照规格-Bun后台第5批 9.7 一字不改）：
// <数据目录>/logs/plugins/<插件 id>/<本机日期 YYYY-MM-DD>.jsonl，一行一个 JSON。Bun 的 server/src/plugins/stats-cli.ts 能直接读。
//
// 插件只交 Event 的那几个字段（Registry.Trace），格式、路径、清洗、每天上限、保留天数都在这里；t、plugin、gen、event、tool 由宿主填，
// 插件伪造不了（Event 里没有这几个字段）。写盘同步追加（量很小），任何失败只记一次宿主日志，不往外报，不影响插件和聊天。
// 没给数据目录就什么也不写。
//
// 对照 server/src/plugins/tracelog.ts 逐条写：字段顺序 t、plugin、gen、event、tool、site、route、url、ms、result、kind、error、note；
// 清洗、截断、上限、保留的规则相同。对拍在 trace_bun_test.go（有 bun 才跑）。
package plugintrace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 规则里的数（照 tracelog.ts）
const (
	DailyMax = 20000 // 每个插件每天最多这么多行，超了写一行 host.limit，当天不再写
	KeepDays = 30    // 日志文件留多少天
	URLMax   = 300
	ErrorMax = 500
	NoteMax  = 200
	LabelMax = 64
	NotAURL  = "(不是网址)"
	idMax    = 40
)

// 一行的 event
const (
	EventPlugin  = "plugin"
	EventLoad    = "host.load"
	EventUnload  = "host.unload"
	EventTool    = "host.tool"
	EventContext = "host.context"
	EventSeen    = "host.seen"
	EventLimit   = "host.limit"
)

// result 的三个值
const (
	OK   = "ok"
	Fail = "fail"
	Skip = "skip" // 没做（不算成功率）
)

// Event 是插件交的一条（照 plugin-api 的 TraceEvent）。Result 必填（不是三个值之一就不写这个字段，汇总时那一行算坏行）
type Event struct {
	Site  string // 站点或子系统，如 bilibili
	Route string // 线路或来源，如 bili_view
	URL   string
	Took  time.Duration // 这一次花了多久；Timed 为真才写（0 毫秒也写）
	Timed bool
	// Result 是 ok、fail、skip
	Result string
	Kind   string // 失败分类，如 network、timeout
	Error  string // 底层错误原文
	Note   string
}

// FileRE 是日志文件名
var FileRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)

// idRE 是插件 id 的写法（manifest.ts 的 ID_RE）
var idRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidID 说这个插件 id 能不能当目录名
func ValidID(id string) bool { return len(id) <= idMax && idRE.MatchString(id) }

// LocalDate 是本机日期 YYYY-MM-DD（不是 UTC）
func LocalDate(t time.Time) string { return t.Local().Format("2006-01-02") }

// Root 是日志根目录：<数据目录>/logs/plugins
func Root(dataDir string) string { return filepath.Join(dataDir, "logs", "plugins") }

// Options 是造 Log 用的
type Options struct {
	DataDir  string                           // 数据目录；空就不写
	Logf     func(format string, args ...any) // 宿主日志；nil 不记
	Now      func() time.Time                 // 测试可换
	DailyMax int                              // 0 用 DailyMax
	KeepDays int                              // 0 用 KeepDays
}

type dayState struct {
	count   int
	limited bool
}

// Log 是宿主这边的写日志的。零值和 nil 都不写
type Log struct {
	root   string
	o      Options
	mu     sync.Mutex
	days   map[string]*dayState // 插件|日期 → 当天写了几行
	logged map[string]bool
}

// New 造一个；DataDir 为空返回的也能用，只是什么都不写
func New(o Options) *Log {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DailyMax <= 0 {
		o.DailyMax = DailyMax
	}
	if o.KeepDays <= 0 {
		o.KeepDays = KeepDays
	}
	l := &Log{o: o, days: map[string]*dayState{}, logged: map[string]bool{}}
	if o.DataDir != "" {
		l.root = Root(o.DataDir)
	}
	return l
}

// Enabled 说会不会写
func (l *Log) Enabled() bool { return l != nil && l.root != "" }

func (l *Log) logOnce(key, format string, args ...any) {
	if l.logged[key] {
		return
	}
	if len(l.logged) >= 200 {
		clear(l.logged)
	}
	l.logged[key] = true
	if l.o.Logf != nil {
		func() {
			defer func() { _ = recover() }() // 日志出错也不往外抛
			l.o.Logf(format, args...)
		}()
	}
}

// Write 记一行。plugin 是宿主给的插件 id（再核一遍写法，进不了 ..）；tool 只有宿主记工具调用时给（空就不写）
func (l *Log) Write(plugin string, gen int, event string, e Event, tool string) {
	if !l.Enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() {
		if x := recover(); x != nil {
			l.logOnce("panic:"+clipBytes(plugin, 40), "插件日志：写 %s 的日志出错了，这条丢掉：%v", clipBytes(plugin, 40), x)
		}
	}()
	if !ValidID(plugin) {
		p := clipRunes(plugin, 80)
		l.logOnce("id:"+p, "插件日志：插件 id %q 写法不对，这条记录丢掉", p)
		return
	}
	when := l.o.Now()
	date := LocalDate(when)
	dir := filepath.Join(l.root, plugin)
	file := filepath.Join(dir, date+".jsonl")
	key := plugin + "|" + date
	st := l.days[key]
	if st == nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			l.logOnce("io:"+plugin, "插件日志：写 %s 的日志没成功，之后这类失败不再重复记：%v", plugin, err)
			return
		}
		l.dropOld(plugin, dir, when)
		st = &dayState{count: countLines(file)}
		if st.count > l.o.DailyMax {
			st.limited = true // 重启后接着写同一天：上限已经到过
		}
		for k := range l.days {
			if strings.HasPrefix(k, plugin+"|") {
				delete(l.days, k) // 这个插件只留当天的计数
			}
		}
		l.days[key] = st
	}
	if st.limited {
		return
	}
	var line []byte
	if st.count >= l.o.DailyMax {
		st.limited = true
		line = encodeLine(when, plugin, gen, EventLimit, nil, "")
	} else {
		line = encodeLine(when, plugin, gen, event, &e, tool)
	}
	st.count++
	if err := appendLine(file, line); err != nil {
		l.logOnce("io:"+plugin, "插件日志：写 %s 的日志没成功，之后这类失败不再重复记：%v", plugin, err)
	}
}

func appendLine(file string, line []byte) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func countLines(file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte{'\n'})
}

// dropOld 删这个插件目录里早于保留天数的日志；只认 YYYY-MM-DD.jsonl 这种名字，别的文件不碰
func (l *Log) dropOld(plugin, dir string, now time.Time) {
	n := now.Local()
	cutoff := LocalDate(time.Date(n.Year(), n.Month(), n.Day()-l.o.KeepDays, 0, 0, 0, 0, time.Local))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return // 读不了目录就算了，下一天再来
	}
	for _, ent := range ents {
		m := FileRE.FindStringSubmatch(ent.Name())
		if m == nil || m[1] >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ent.Name())); err != nil {
			l.logOnce("rm:"+plugin, "插件日志：删 %s 的旧日志没成功：%v", plugin, err)
		}
	}
}

// encodeLine 按固定的键序拼一行（和 JSON.stringify 的输出一样：不转义 <>&）
func encodeLine(when time.Time, plugin string, gen int, event string, e *Event, tool string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"t":`)
	writeStr(&b, when.UTC().Format("2006-01-02T15:04:05.000Z"))
	b.WriteString(`,"plugin":`)
	writeStr(&b, plugin)
	if gen < 0 {
		gen = 0
	}
	fmt.Fprintf(&b, `,"gen":%d,"event":`, gen)
	writeStr(&b, event)
	if tool != "" {
		b.WriteString(`,"tool":`)
		writeStr(&b, CleanField(tool, LabelMax))
	}
	if e != nil {
		for _, f := range Fields(*e) {
			b.WriteString(`,"` + f.Key + `":`)
			if f.Num {
				fmt.Fprintf(&b, "%d", f.N)
			} else {
				writeStr(&b, f.S)
			}
		}
	}
	b.WriteString("}\n")
	return b.Bytes()
}

func writeStr(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Write(bytes.TrimSuffix(tmp.Bytes(), []byte{'\n'}))
}

// Field 是清洗后要写的一个字段（按 tracelog.ts cleanTraceFields 的顺序）
type Field struct {
	Key string
	S   string
	N   int64
	Num bool
}

// Fields 把插件交的东西挑成可写的字段，顺序是 site、route、url、ms、result、kind、error、note
func Fields(e Event) []Field {
	var out []Field
	label := func(k, v string) {
		if v != "" {
			out = append(out, Field{Key: k, S: CleanField(v, LabelMax)})
		}
	}
	label("site", e.Site)
	label("route", e.Route)
	if strings.TrimFunc(e.URL, isJSSpace) != "" {
		out = append(out, Field{Key: "url", S: CleanURL(e.URL)})
	}
	if e.Timed && e.Took >= 0 {
		out = append(out, Field{Key: "ms", N: int64((e.Took + 500*time.Microsecond) / time.Millisecond), Num: true}) // Math.round
	}
	switch e.Result {
	case OK, Fail, Skip:
		out = append(out, Field{Key: "result", S: e.Result})
	}
	label("kind", e.Kind)
	if e.Error != "" {
		out = append(out, Field{Key: "error", S: CleanField(e.Error, ErrorMax)})
	}
	if e.Note != "" {
		out = append(out, Field{Key: "note", S: CleanField(e.Note, NoteMax)})
	}
	return out
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func clipBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
