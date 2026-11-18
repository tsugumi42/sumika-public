// Package filelog 是后台日志写文件的地方：诊断入口（cmd/sumika-go，文件名前缀 go）和桌面程序（前缀 desk）共用。
//
// 每一行带本地时间，写进 <数据目录>/logs/<前缀>-<日期>.log；日期用本地时区（LESSONS：TS 那边 toISOString 用 UTC，
// 跨零点会写到前一天的文件里），每写一行前看一眼日期，跨过零点就换文件。out 不是 nil 时同一行也打到 out
// （命令行窗口）；桌面程序是 -H windowsgui 编的，没有控制台，传 nil，文件就是唯一的出口。
//
// 这里不打码：令牌、会话号、长数字是装配处在调用 Printf / Line 之前抹的（app.newLogf、host.logf、shell.logf）。
// 数据目录身份证还没出现时（filelog 不建 logs/），行先放进早期缓冲，身份证出现后按顺序写进文件。
//
// 行格式（规格-日志分级）：`时间 级别 模块 消息`。Line 只负责加时间、换行转义、选文件；级别和模块由 applog 拼。
// ParseLine 是读取侧唯一的解析函数，旧格式（没有级别和模块的行）照常认。
package filelog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/paths"
)

// StampLayout 是每行开头的时间；DayLayout 是日志文件名里的日期（都是本地日期）
const (
	StampLayout = "2006-01-02 15:04:05"
	DayLayout   = "2006-01-02"
)

// 早期缓冲的上限：数据目录身份证出现前的行最多留这么多，先到先算，满了丢最旧的
const (
	earlyMaxLines = 200
	earlyMaxBytes = 64 << 10
)

// FileName 是某一天的日志文件名，比如 go-2026-10-29.log
func FileName(prefix string, t time.Time) string {
	return prefix + "-" + t.Format(DayLayout) + ".log"
}

// Logger 把每一行写进当天的日志文件，同时（out 不是 nil 时）打到 out。now 可换：测试要试本地日期和跨零点换文件
type Logger struct {
	root   string // 数据目录
	prefix string
	out    io.Writer
	now    func() time.Time

	mu           sync.Mutex
	day          string // 现在开着的文件是哪一天
	file         *os.File
	closed       bool
	pending      []string // 身份证出现前的行（已经是完整的一行），文件开了就按顺序写进去
	pendingBytes int
	dropped      int // 早期缓冲满而丢掉的行数，写进文件时补一行说明
}

// New 建日志。不碰磁盘：第一行要写时才找数据目录，那时数据目录里有身份证（paths.MetaFile）才建 logs/ 和文件，
// 没有就只打 out，行先进早期缓冲。先建 logs/ 会把一个还没初始化的目录建出来，让之后的 receive 当成"已经有目录、认不出身份证"报错
// （桌面默认只看历史，目录不存在时 archive 不建它；receive 才 Init）。
func New(dataDir, prefix string, out io.Writer, now func() time.Time) *Logger {
	return &Logger{root: dataDir, prefix: prefix, out: out, now: now}
}

// Printf 写一行（就是 app.Options.Logf 的签名）：时间用 now()，内容是原样的消息
func (l *Logger) Printf(format string, args ...any) {
	l.Line(l.now(), fmt.Sprintf(format, args...))
}

// Line 写一行：前面加上 at 的时间（本地），内容里的换行转义成两个字符 \n（见 oneLine）。
// at 决定写进哪一天的文件；调用方传的是这一行的时间（applog 用记录的时间）。
func (l *Logger) Line(at time.Time, body string) {
	text := at.Format(StampLayout) + " " + oneLine(body)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out != nil {
		fmt.Fprintln(l.out, text)
	}
	f := l.fileFor(at)
	if f == nil {
		l.keepEarly(text)
		return
	}
	l.flushEarly(f)
	fmt.Fprintln(f, text)
}

// keepEarly 把还没写进文件的行放进早期缓冲；Close 以后不再缓冲（退出收尾的行只打 out，同之前）
func (l *Logger) keepEarly(text string) {
	if l.closed {
		return
	}
	l.pending = append(l.pending, text)
	l.pendingBytes += len(text) + 1
	for len(l.pending) > earlyMaxLines || l.pendingBytes > earlyMaxBytes {
		l.pendingBytes -= len(l.pending[0]) + 1
		l.pending = l.pending[1:]
		l.dropped++
	}
}

// flushEarly 把早期缓冲按顺序写进文件；丢过行的话先补一行说明（和 applog 的模块名一致）
func (l *Logger) flushEarly(f *os.File) {
	if l.dropped > 0 {
		at := time.Now()
		if len(l.pending) > 0 {
			at = l.pendingTime()
		}
		fmt.Fprintln(f, at.Format(StampLayout)+" WARN applog 早期日志缓冲满，丢了 "+fmt.Sprint(l.dropped)+" 行")
		l.dropped = 0
	}
	for _, line := range l.pending {
		fmt.Fprintln(f, line)
	}
	l.pending, l.pendingBytes = nil, 0
}

// pendingTime 取缓冲里最早一行的时间（只用来给丢行说明补一个时间戳）
func (l *Logger) pendingTime() time.Time {
	if len(l.pending) > 0 && len(l.pending[0]) >= 19 {
		if t, err := time.ParseInLocation(StampLayout, l.pending[0][:19], time.Local); err == nil {
			return t
		}
	}
	return time.Now()
}

var lineBreaks = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`)

// oneLine 把一条日志里的换行换成两个字符 \n：一次 Printf 就是日志里的一行。日志里有第三方的文字
// （群名、对方发来的话、协议端的报错）时，换行不换掉就能在 desk-*.log 里伪造出整行假的日志，sumika-ctl logs 会把假的当真的交给 Agent
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return lineBreaks.Replace(s)
}

// fileFor 给出这一天该写的文件（追加）；日期变了就关掉旧的、开新的。打不开就只打 out：日志写不了不退程序，
// 命令行窗口里立刻看得见。Close 以后不再开
func (l *Logger) fileFor(now time.Time) *os.File {
	if l.closed {
		return nil
	}
	day := now.Format(DayLayout)
	if l.file != nil && l.day == day {
		return l.file
	}
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	if _, err := os.Stat(filepath.Join(l.root, paths.MetaFile)); err != nil {
		return nil
	}
	dir := filepath.Join(l.root, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, FileName(l.prefix, now)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	l.day, l.file = day, f
	return f
}

// Close 关掉现在开着的文件；之后的 Printf 只打 out
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Entry 是读取侧认出的一行。Level、Module 对旧格式行是 ""；新格式里模块不合法时 Module 是 "?"（坏行，计数用）
type Entry struct {
	At     time.Time
	Level  string // DEBUG | INFO | WARN | ERROR；旧行为 ""
	Module string // 模块标签；旧行为 ""；坏行为 "?"
	Msg    string // 消息正文（不含级别和模块）
	Raw    string // 原行
}

var levelWords = []string{"DEBUG", "INFO", "WARN", "ERROR"}

// moduleWord 是模块标签的语法：小写字母、数字、下划线（名单在 applog 里，这里只认语法）
var moduleWord = regexp.MustCompile(`^[a-z0-9_]+$`)

// ParseLine 解析一行。时间不合法返回 false。
// 新格式：`时间 级别 模块 消息`；第 3 个字段不是级别词的是旧行，消息是 20 字符之后的全部（旧行的消息里可能有 `snowluma: ` 之类的前缀，原样保留）。
func ParseLine(line string) (Entry, bool) {
	if len(line) < 20 || line[19] != ' ' {
		return Entry{}, false
	}
	at, err := time.ParseInLocation(StampLayout, line[:19], time.Local)
	if err != nil {
		return Entry{}, false
	}
	e := Entry{At: at, Raw: line, Msg: line[20:]}
	for _, w := range levelWords {
		if !strings.HasPrefix(e.Msg, w+" ") {
			continue
		}
		rest := e.Msg[len(w)+1:]
		e.Level = w
		mod, msg, _ := strings.Cut(rest, " ")
		if moduleWord.MatchString(mod) {
			e.Module, e.Msg = mod, msg
		} else {
			e.Module, e.Msg = "?", rest
		}
		break
	}
	return e, true
}
