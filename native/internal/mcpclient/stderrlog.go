package mcpclient

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 每个 stdio 服务一个 stderr 日志文件（规格-审批分级 8.4，ADR-342 里“没做”的一项）：
// 放在 <logs>/mcp/<服务id>.log，一行一条，带时间；超过 stderrLogMax 就换一个新文件，旧的依次改名为 .1、.2，
// 最多留 stderrLogKeep 份。不混进主日志。服务重启接着写同一个文件，免得反复崩的服务把旧日志挤掉。
//
// 日志是给人排查服务为什么没起来、中途报了什么用的，所以写盘前每一行都过一遍和握手失败原因同一套的抹凭据：
// 配置里 env、headers 的值，看着像 token、Authorization、Bearer 的字，都换成 ***；控制字符去掉；一行最多留 stderrLineMax 个字。
// 写不进去（目录建不出来、磁盘满）不影响服务运行，也不重复报：这一次启动里不再写。
const (
	stderrLogMax  = 1 << 20 // 单个文件上限（字节）
	stderrLogKeep = 3       // 当前的加上改名留下的旧文件，一共几份
	stderrLineMax = 2000    // 一行最多几个字
	stderrPending = 64 << 10
)

type stderrLog struct {
	mu      sync.Mutex
	dir     string
	name    string // 不带目录的文件名
	cfg     ServerConfig
	max     int64
	keep    int
	f       *os.File
	size    int64
	partial []byte // 还没收到换行的尾巴
	broken  bool
}

// logFileName 把服务 id 变成安全的文件名：只留字母数字和 ._-，别的换成 _，不能以点开头、不能为空、不超过 80 个字符。
// 不同 id 洗成同一个名字就共用一个文件（只影响排查，不影响运行）。
func logFileName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" {
		s = "server"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s + ".log"
}

// newStderrLog 没给日志目录就返回 nil（nil 的 Write、Close 什么都不做）
func newStderrLog(dir string, cfg ServerConfig) *stderrLog {
	if dir == "" {
		return nil
	}
	l := &stderrLog{dir: dir, name: logFileName(cfg.ID), cfg: cfg, max: stderrLogMax, keep: stderrLogKeep}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("=== 启动 ===")
	return l
}

// Write 总是成功：日志坏了不能让 MultiWriter 把 stderr 的另一路（握手失败原因）也断掉
func (l *stderrLog) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		l.line(string(l.partial[:i]))
		l.partial = l.partial[i+1:]
	}
	if len(l.partial) > stderrPending { // 一直不换行：当一行写下去，别无限攒
		l.line(string(l.partial))
		l.partial = nil
	}
	return len(p), nil
}

// Close 把没收到换行的尾巴写下去，关文件
func (l *stderrLog) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.line(string(l.partial))
		l.partial = nil
	}
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// line 写一行（调用时已持锁）：抹凭据、截断、加时间
func (l *stderrLog) line(text string) {
	if l.broken {
		return
	}
	text = strings.TrimRight(text, "\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	text = scrub(text, l.cfg)
	if r := []rune(text); len(r) > stderrLineMax {
		text = string(r[:stderrLineMax]) + "…"
	}
	out := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
	if l.f == nil {
		if err := l.open(); err != nil {
			l.broken = true
			return
		}
	}
	if l.size > 0 && l.size+int64(len(out)) > l.max {
		if err := l.rotate(); err != nil {
			l.broken = true
			return
		}
	}
	n, err := l.f.WriteString(out)
	l.size += int64(n)
	if err != nil {
		l.broken = true
	}
}

// open 接着写已有的文件
func (l *stderrLog) open() error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, l.name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err == nil {
		l.size = info.Size()
	}
	l.f = f
	return nil
}

// rotate 换一个新文件：当前的改名为 .1，原来的 .1 变 .2……最多留 keep 份
func (l *stderrLog) rotate() error {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	base := filepath.Join(l.dir, l.name)
	if l.keep > 1 {
		_ = os.Remove(fmt.Sprintf("%s.%d", base, l.keep-1))
		for i := l.keep - 2; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", base, i), fmt.Sprintf("%s.%d", base, i+1))
		}
		if err := os.Rename(base, base+".1"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := os.Remove(base); err != nil && !os.IsNotExist(err) {
		return err
	}
	l.size = 0
	return l.open()
}
