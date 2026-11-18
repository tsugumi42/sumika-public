// Package slash 是登记式的斜杠命令表：命令和子命令登记一处，Parse 只管认整条消息开头的 /名字。
// 它不执行命令，也不知道哪个场合允许斜杠命令（只在本地私聊认，由宿主判断）；
// 以后加 /loop 只需 Register 一个 Command。
package slash

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
)

// Sub 是命令的子命令，如 /goal clear。
type Sub struct {
	Name    string
	Aliases []string
	Summary string
	// NoArg 为真时，子命令后面还有别的文字就不算子命令：
	// "/goal clear the cache" 是目标原文，不是清除。
	NoArg bool
}

// Command 是一条登记的斜杠命令。
type Command struct {
	Name    string
	Aliases []string
	Summary string
	Subs    []Sub
}

// Invocation 是一次解析结果。Sub 是子命令的正式名，没有子命令时为空，此时 Arg 是名字后面的全部文字。
type Invocation struct {
	Command *Command
	Sub     string
	Arg     string
}

// ErrNotCommand 表示这条消息不是斜杠命令（不以 /名字 开头），应当当普通文字处理。
var ErrNotCommand = errors.New("不是斜杠命令")

// UnknownError 表示形如斜杠命令、但没登记。宿主应当把原文照发并提示“没有这个命令”。
type UnknownError struct{ Name string }

func (e *UnknownError) Error() string { return fmt.Sprintf("没有这个命令：/%s", e.Name) }

// Registry 是命令表，并发安全。
type Registry struct {
	mu    sync.RWMutex
	names map[string]*Command
}

func NewRegistry() *Registry { return &Registry{names: map[string]*Command{}} }

// Register 登记命令；名字或别名重复、名字不合法会 panic（装配期错误，启动就该暴露）。
func (r *Registry) Register(c Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd := c
	for _, n := range append([]string{c.Name}, c.Aliases...) {
		n = strings.ToLower(n)
		if !validName(n) {
			panic("slash: 命令名不合法：" + n)
		}
		if _, dup := r.names[n]; dup {
			panic("slash: 命令名重复：" + n)
		}
		r.names[n] = &cmd
	}
}

// Commands 返回已登记的命令（按名字去重，顺序不保证）。
func (r *Registry) Commands() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[*Command]bool{}
	var out []Command
	for _, c := range r.names {
		if !seen[c] {
			seen[c] = true
			out = append(out, *c)
		}
	}
	return out
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || (i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '_'))
		if !ok {
			return false
		}
	}
	return true
}

// Split 只做词法：整条消息以 /名字 开头（前面只允许空白），名字后面须是空白或结尾。
// 名字含路径符号等不合法字符时返回 ok=false，"/usr/bin 在哪" 不会被当成命令。
func Split(text string) (name, arg string, ok bool) {
	t := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	t = t[1:]
	end := strings.IndexFunc(t, unicode.IsSpace)
	if end < 0 {
		end = len(t)
	}
	name = strings.ToLower(t[:end])
	if !validName(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(t[end:]), true
}

// Parse 解析一条消息。不是命令返回 ErrNotCommand，没登记返回 *UnknownError。
func (r *Registry) Parse(text string) (Invocation, error) {
	name, arg, ok := Split(text)
	if !ok {
		return Invocation{}, ErrNotCommand
	}
	r.mu.RLock()
	c := r.names[name]
	r.mu.RUnlock()
	if c == nil {
		return Invocation{}, &UnknownError{Name: name}
	}
	inv := Invocation{Command: c, Arg: arg}
	if arg == "" {
		return inv, nil
	}
	first, rest := arg, ""
	if i := strings.IndexFunc(arg, unicode.IsSpace); i >= 0 {
		first, rest = arg[:i], strings.TrimSpace(arg[i:])
	}
	first = strings.ToLower(first)
	for _, s := range c.Subs {
		if first != strings.ToLower(s.Name) && !hasFold(s.Aliases, first) {
			continue
		}
		if s.NoArg && rest != "" {
			break
		}
		inv.Sub, inv.Arg = s.Name, rest
		break
	}
	return inv, nil
}

func hasFold(list []string, s string) bool {
	for _, x := range list {
		if strings.ToLower(x) == s {
			return true
		}
	}
	return false
}
