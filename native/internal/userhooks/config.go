// Package userhooks 是用户配置的钩子（规格-用户钩子）：读 hooks.json（形状照 Claude Code），工具跑完、回合要结束时跑一条命令，
// 把输出给模型。挂在 turn.Registry 的两个挂点上，内核不知道这里的任何东西。
//
// 钩子失败不影响工具和回合；项目级要过项目说明那套信任根；工具参数只进标准输入和环境变量，绝不拼进命令文字。
package userhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 支持的事件
const (
	EventPostToolUse = "PostToolUse"
	EventStop        = "Stop"
)

// 单条命令的时限不能超过这个（Claude Code 的默认值）
const maxTimeout = 600 * time.Second

// 配置文件的大小上限：再大多半不是钩子配置
const maxConfigBytes = 256 << 10

// Claude Code 文档里有、这版不支持的事件：跳过时说清楚，不当错字
var knownEvents = map[string]bool{"PreToolUse": true, "PermissionRequest": true, "PermissionDenied": true, "PostToolUseFailure": true, "PostToolBatch": true,
	"UserPromptSubmit": true, "UserPromptExpansion": true, "Notification": true, "MessageDisplay": true, "SubagentStart": true, "SubagentStop": true, "TaskCreated": true, "TaskCompleted": true,
	"StopFailure": true, "TeammateIdle": true, "InstructionsLoaded": true, "ConfigChange": true, "CwdChanged": true, "DirectoryAdded": true, "FileChanged": true, "WorktreeCreate": true, "WorktreeRemove": true,
	"PreCompact": true, "PostCompact": true, "PreModelSwitch": true, "PostModelSwitch": true, "Elicitation": true, "ElicitationResult": true, "SessionStart": true, "SessionEnd": true, "Setup": true,
	"Interrupt": true, "PreModelRequest": true, "PostModelRequest": true}

// Handler 是一条要跑的命令
type Handler struct {
	Command  string
	Shell    string // ""（PowerShell）或 "bash"
	Timeout  time.Duration
	If       *ifRule
	Block    bool   // onFailure: "block"
	Feedback string // always、failure、never
	Label    string // statusMessage，显示用
}

// Group 是一个 matcher 组
type Group struct {
	Matcher  matcher
	Paths    []*regexp.Regexp
	Handlers []Handler
}

// Set 是一个文件读出来的钩子
type Set struct {
	Events map[string][]Group
	Diag   []string // 跳过了哪些，一条一句，不含命令原文以外的内容
}

// Parse 解析一个 hooks.json。整个文件不对返回 error（这个文件一条都不跑）；单条不对跳过并记进 Diag。
// defaultTimeout 给没写 timeout 的命令。
func Parse(data []byte, defaultTimeout time.Duration) (*Set, error) {
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("文件超过 %d KiB", maxConfigBytes>>10)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // 记事本存的 UTF-8 带 BOM
	set := &Set{Events: map[string][]Group{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return set, nil
	}
	var top struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("不是合法的 JSON：%v", err)
	}
	if len(top.Hooks) == 0 || string(top.Hooks) == "null" {
		return set, nil
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(top.Hooks, &events); err != nil {
		return nil, fmt.Errorf("hooks 要是一个对象：%v", err)
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case name == EventPostToolUse || name == EventStop:
		case knownEvents[name]:
			set.Diag = append(set.Diag, name+" 这版不支持，跳过")
			continue
		default:
			set.Diag = append(set.Diag, "不认识的事件 "+name+"，跳过")
			continue
		}
		var groups []rawGroup
		if err := json.Unmarshal(events[name], &groups); err != nil {
			set.Diag = append(set.Diag, name+" 要是一个数组，跳过："+err.Error())
			continue
		}
		for i, g := range groups {
			where := fmt.Sprintf("%s[%d]", name, i)
			group, ok := g.build(name, where, defaultTimeout, &set.Diag)
			if ok {
				set.Events[name] = append(set.Events[name], group)
			}
		}
	}
	return set, nil
}

type rawGroup struct {
	Matcher json.RawMessage `json:"matcher"`
	Paths   json.RawMessage `json:"paths"`
	Hooks   []rawHandler    `json:"hooks"`
}

type rawHandler struct {
	Type          string          `json:"type"`
	Command       string          `json:"command"`
	Shell         string          `json:"shell"`
	Timeout       json.RawMessage `json:"timeout"`
	If            string          `json:"if"`
	OnFailure     string          `json:"onFailure"`
	Feedback      string          `json:"feedback"`
	StatusMessage string          `json:"statusMessage"`
	Async         bool            `json:"async"`
	Args          json.RawMessage `json:"args"`
}

// UnmarshalJSON 单条 handler 类型不对不连累同组别的：解不开就留一个空的，build 时跳过
func (h *rawHandler) UnmarshalJSON(b []byte) error {
	type plain rawHandler
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		*h = rawHandler{Type: "（类型不对）"}
		return nil
	}
	*h = rawHandler(v)
	return nil
}

func (g rawGroup) build(event, where string, defaultTimeout time.Duration, diag *[]string) (Group, bool) {
	skip := func(why string) (Group, bool) {
		*diag = append(*diag, where+" 跳过："+why)
		return Group{}, false
	}
	pattern := ""
	if len(g.Matcher) > 0 && string(g.Matcher) != "null" {
		if err := json.Unmarshal(g.Matcher, &pattern); err != nil {
			return skip("matcher 要是字符串")
		}
	}
	m, err := newMatcher(pattern)
	if err != nil {
		return skip("matcher 不是合法的正则：" + err.Error())
	}
	globs, err := parsePaths(g.Paths)
	if err != nil {
		return skip(err.Error())
	}
	out := Group{Matcher: m, Paths: globs}
	for i, raw := range g.Hooks {
		h, why := raw.build(event, defaultTimeout)
		if why != "" {
			*diag = append(*diag, fmt.Sprintf("%s.hooks[%d] 跳过：%s", where, i, why))
			continue
		}
		if raw.Async {
			*diag = append(*diag, fmt.Sprintf("%s.hooks[%d] 的 async 不支持，按同步跑", where, i))
		}
		out.Handlers = append(out.Handlers, h)
	}
	if len(out.Handlers) == 0 {
		return Group{}, false
	}
	return out, true
}

func (h rawHandler) build(event string, defaultTimeout time.Duration) (Handler, string) {
	if h.Type != "command" {
		return Handler{}, "只支持 type 为 command，这里是 " + h.Type
	}
	if len(h.Args) > 0 {
		return Handler{}, "args 不支持，把参数写进 command"
	}
	if strings.TrimSpace(h.Command) == "" {
		return Handler{}, "command 是空的"
	}
	out := Handler{Command: h.Command, Label: h.StatusMessage, Timeout: defaultTimeout}
	switch h.Shell {
	case "", "powershell":
	case "bash":
		out.Shell = "bash"
	default:
		return Handler{}, "shell 只认 powershell 或 bash，这里是 " + h.Shell
	}
	switch h.OnFailure {
	case "":
	case "block":
		out.Block = true
	default:
		return Handler{}, "onFailure 只认 block，这里是 " + h.OnFailure
	}
	switch h.Feedback {
	case "", "always":
		out.Feedback = "always"
	case "failure", "never":
		out.Feedback = h.Feedback
	default:
		return Handler{}, "feedback 只认 always、failure、never，这里是 " + h.Feedback
	}
	if len(h.Timeout) > 0 && string(h.Timeout) != "null" {
		var secs float64
		if err := json.Unmarshal(h.Timeout, &secs); err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) {
			return Handler{}, "timeout 要是秒数"
		}
		out.Timeout = clampTimeout(secs)
	}
	if h.If != "" {
		if event != EventPostToolUse {
			return Handler{}, "if 只对工具事件有效，Stop 上写了不跑" // Claude Code 文档："On other events, a hook with if set never runs"
		}
		rule, err := parseIf(h.If)
		if err != nil {
			return Handler{}, err.Error()
		}
		out.If = rule
	}
	return out, ""
}

// clampTimeout：小于 1 秒按 1 秒（Claude Code 同样），大于上限按上限
func clampTimeout(secs float64) time.Duration {
	switch {
	case secs < 1:
		return time.Second
	case secs >= maxTimeout.Seconds():
		return maxTimeout
	}
	return time.Duration(secs * float64(time.Second))
}

func parsePaths(raw json.RawMessage) ([]*regexp.Regexp, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []string
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		list = []string{one}
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("paths 要是字符串或字符串数组")
	}
	var out []*regexp.Regexp
	for _, p := range list {
		re, err := globRegexp(p)
		if err != nil {
			return nil, fmt.Errorf("paths 里的 %q 不合法：%v", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}
