package userhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// ID 是插件的身份
const ID = "userhooks"

// ProjectFile 是工作区根下的项目级配置
var ProjectFile = filepath.Join(".sumika", "hooks.json")

// Trust 和项目说明同一份信任根；nil 表示信任根没开，项目级不跑
type Trust interface {
	Trusted(context.Context, turn.Workspace) (bool, error)
}

// Options 是装配时给的东西
type Options struct {
	GlobalFile     string
	Trust          Trust
	Degrade        *taskrun.Degrader
	Timeout        time.Duration // 没写 timeout 的命令用它
	MaxOutputBytes int
	MaxParallel    int
	StopMax        int
	Run            Runner // 测试换假的；nil 用真的
}

// Plugin 是接进内核的用户钩子
type Plugin struct {
	opts   Options
	run    Runner
	slots  chan struct{}
	seq    atomic.Int64
	loader loader
}

// New 建插件。默认值在配置那一层已经定好，这里只防零值
func New(o Options) *Plugin {
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = 8192
	}
	if o.MaxParallel <= 0 {
		o.MaxParallel = 4
	}
	run := o.Run
	if run == nil {
		run = tools.RunShellCommand
	}
	return &Plugin{opts: o, run: run, slots: make(chan struct{}, o.MaxParallel)}
}

func (p *Plugin) ID() string { return ID }

// Manifest 是清单（规格-插件两层 第二节第 1 条）；没写开关时沿用 hooks.enabled（装配处定）
func (p *Plugin) Manifest() turn.Manifest {
	return turn.Manifest{Name: "用户钩子", Sub: "主人配置的命令在回合的前后运行（改参数要重启）"}
}

// Setup 注册两个挂点
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	r.OnToolResult(p.onToolResult)
	r.OnStop(p.opts.StopMax, p.onStop)
	return nil, nil
}

// local 是第二道门：内核的 in.local() 之外再看一次听众。QQ 私聊、QQ 群绝不跑
func local(a audience.Audience) bool { return a.Channel == "console" }

// processStep 是一条钩子在过程气泡里的样子
type processStep struct {
	id      string
	event   string
	handler Handler
	state   string
	started time.Time
	output  string
}

func (p *Plugin) processFn(stage turn.StageTools) func(processStep) {
	if stage.Process == nil {
		return nil
	}
	return func(s processStep) {
		ev := turn.ProcessStep{ID: s.id, Kind: "tool", Tool: "钩子", Target: s.event + " · " + s.handler.label(), State: s.state, Started: s.started.UnixMilli(), Output: s.output}
		if s.state != "running" {
			ev.Seconds = max(int(time.Since(s.started).Seconds()), 0)
		}
		stage.Process(ev)
	}
}

// filePathOf 是工具参数里的文件路径（绝对路径）；不是文件工具就是空
func filePathOf(name string, args jsjson.Value, cwd string) string {
	if name == "powershell" || args.Kind != jsjson.Object {
		return ""
	}
	v := args.Obj["path"]
	if v.Kind != jsjson.String || v.Str == "" {
		return ""
	}
	path := v.Str
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func baseEnv(ws turn.Workspace, event string) []string {
	env := []string{"SUMIKA_HOOK_EVENT=" + event}
	if ws.Root != "" {
		env = append(env, "SUMIKA_PROJECT_DIR="+ws.Root, "CLAUDE_PROJECT_DIR="+ws.Root)
	}
	return env
}

// setEnv 追加一个来自工具参数的变量：带 NUL 的进程起不来，不设
func setEnv(env []string, key, value string) []string {
	if value == "" || strings.ContainsRune(value, 0) {
		return env
	}
	return append(env, key+"="+value)
}

// dedupe：同一事件里完全相同的命令只跑一次（Claude Code、Crush 同样）
func dedupe(hs []Handler) []Handler {
	seen := map[string]bool{}
	out := hs[:0:0]
	for _, h := range hs {
		key := h.Shell + "\x00" + h.Command
		if !seen[key] {
			seen[key] = true
			out = append(out, h)
		}
	}
	return out
}

func (p *Plugin) onToolResult(ctx context.Context, e turn.ToolResultEvent) ([]string, error) {
	if !local(e.Audience) {
		return nil, nil
	}
	file := filePathOf(e.Name, e.Args, e.Workspace.CWD)
	command := ""
	if e.Name == "powershell" && e.Args.Kind == jsjson.Object {
		command = e.Args.Obj["command"].Str
	}
	var matched []Handler
	for _, set := range p.sets(ctx, e.Workspace) {
		for _, g := range set.Events[EventPostToolUse] {
			if !g.Matcher.matches(e.Name) {
				continue
			}
			if g.Paths != nil && (file == "" || !pathMatches(g.Paths, file, e.Workspace.Root)) {
				continue
			}
			for _, h := range g.Handlers {
				if h.If == nil || h.If.allows(e.Name, file, command, e.Workspace.Root) {
					matched = append(matched, h)
				}
			}
		}
	}
	matched = dedupe(matched)
	if len(matched) == 0 {
		return nil, nil
	}
	env := baseEnv(e.Workspace, EventPostToolUse)
	env = setEnv(env, "SUMIKA_TOOL_NAME", e.Name)
	env = setEnv(env, "SUMIKA_FILE_PATH", file)
	stdin := postToolInput(e, file)
	outs := p.runAll(ctx, EventPostToolUse, e.Name, matched, e.Workspace.CWD, env, stdin, e.Identity.RootID, e.Stage.Span, p.processFn(e.Stage))
	var notes []string
	for _, o := range outs {
		if note := p.toolNote(o); note != "" {
			notes = append(notes, note)
		}
	}
	return notes, nil
}

// toolNote 是一条命令给模型的尾注（规格第三节的表）；没有就空
func (p *Plugin) toolNote(o outcome) string {
	head := "[用户钩子 PostToolUse：" + o.handler.label()
	limit := p.opts.MaxOutputBytes
	if o.json != nil {
		return jsonNote(o, head, limit)
	}
	switch o.class {
	case classCancelled:
		return ""
	case taskrun.ClassTimeout, taskrun.ClassSpawnFailed:
		return head + "，" + o.failure + "]"
	case taskrun.ClassCheckFailed:
		text := o.stderr
		if text == "" {
			text = o.stdout
		}
		if text == "" {
			text = "（退出码 2，没有输出）"
		}
		return head + "，检查没过]\n" + clipMiddle(text, limit)
	case taskrun.ClassExitNonzero:
		if o.handler.Feedback == "never" {
			return ""
		}
		text := strings.TrimSpace(strings.Join(nonEmpty(o.stdout, o.stderr), "\n"))
		if text == "" {
			return fmt.Sprintf("%s，退出码 %d，没有输出]", head, o.code)
		}
		return fmt.Sprintf("%s，退出码 %d]\n%s", head, o.code, clipMiddle(text, limit))
	}
	if o.stdout != "" && o.handler.Feedback == "always" {
		return head + "]\n" + clipMiddle(o.stdout, limit)
	}
	return ""
}

func jsonNote(o outcome, head string, limit int) string {
	stop := ""
	if o.json.Continue != nil && !*o.json.Continue {
		stop = o.json.StopReason
	}
	text := strings.TrimSpace(strings.Join(nonEmpty(o.json.Specific.AdditionalContext, blockReason(o.json), stop), "\n"))
	if text == "" {
		return ""
	}
	return head + "]\n" + clipMiddle(text, limit)
}

func blockReason(j *hookJSON) string {
	if j.Decision == "block" {
		return j.Reason
	}
	return ""
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, s := range parts {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *Plugin) onStop(ctx context.Context, e turn.StopEvent) (turn.StopDecision, error) {
	if !local(e.Audience) {
		return turn.StopDecision{}, nil
	}
	var changed []string
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, f := range e.Tools {
		if !f.OK {
			continue
		}
		used[f.Name] = true
		if f.Name == "edit" || f.Name == "write" {
			if path := filePathOf(f.Name, f.Args, e.Workspace.CWD); path != "" && !seen[path] {
				seen[path] = true
				changed = append(changed, path)
			}
		}
	}
	var matched []Handler
	for _, set := range p.sets(ctx, e.Workspace) {
		for _, g := range set.Events[EventStop] {
			if !g.Matcher.all {
				hit := false
				for name := range used {
					hit = hit || g.Matcher.matches(name)
				}
				if !hit {
					continue
				}
			}
			if g.Paths != nil {
				hit := false
				for _, path := range changed {
					hit = hit || pathMatches(g.Paths, path, e.Workspace.Root)
				}
				if !hit {
					continue
				}
			}
			matched = append(matched, g.Handlers...)
		}
	}
	matched = dedupe(matched)
	if len(matched) == 0 {
		return turn.StopDecision{}, nil
	}
	stdin := stopInput(e, changed)
	env := baseEnv(e.Workspace, EventStop)
	outs := p.runAll(ctx, EventStop, "", matched, e.Workspace.CWD, env, stdin, e.Identity.RootID, e.Stage.Span, p.processFn(e.Stage))
	var d turn.StopDecision
	var reasons []string
	for i := range outs {
		o := &outs[i]
		reason, halt := stopReason(o)
		if halt {
			d.Halt = true
		}
		if reason != "" {
			reasons = append(reasons, "[用户钩子 Stop："+o.handler.label()+"]\n"+clipMiddle(reason, p.opts.MaxOutputBytes))
		}
	}
	if len(reasons) > 0 {
		d.Continue, d.Reason = true, strings.Join(reasons, "\n\n")
		if e.Last && !d.Halt {
			// 到了上限：这一轮照常结束，但要让主人在运行页看到"它还没过"
			p.opts.Degrade.Degrade(taskrun.ModuleHooks, taskrun.ClassLoopLimit, fmt.Errorf("Stop 钩子续跑 %d 次还没过，这一轮结束", e.Continued), taskrun.Opts{Outcome: taskrun.OutcomeDegraded, RootID: e.Identity.RootID})
		}
	}
	return d, nil
}

// stopReason 是这条命令要不要这一轮继续（规格第三节的表）：非空就继续；halt 压过别的继续
func stopReason(o *outcome) (reason string, halt bool) {
	first := func(parts ...string) string {
		for _, s := range parts {
			if strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
	if j := o.json; j != nil {
		if j.Continue != nil && !*j.Continue {
			return "", true
		}
		return first(blockReason(j), j.Specific.AdditionalContext), false
	}
	switch o.class {
	case taskrun.ClassCheckFailed:
		return first(o.stderr, o.stdout, "检查没过（钩子没有说原因）。"), false
	case taskrun.ClassExitNonzero:
		if o.handler.Block {
			return first(strings.Join(nonEmpty(o.stdout, o.stderr), "\n"), fmt.Sprintf("检查没过（退出码 %d，没有输出）。", o.code)), false
		}
	case taskrun.ClassTimeout, taskrun.ClassSpawnFailed:
		if o.handler.Block {
			return o.failure, false
		}
	}
	return "", false
}

// 标准输入（字段照 Claude Code）。太大的只留路径，钩子要全文自己去读
const maxToolInputBytes = 256 << 10
const maxToolResponseBytes = 64 << 10

func postToolInput(e turn.ToolResultEvent, file string) []byte {
	input := json.RawMessage(jsjson.Stringify(e.Args))
	if !json.Valid(input) {
		input = json.RawMessage("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err == nil && file != "" {
		obj["file_path"], _ = json.Marshal(file)
		input, _ = json.Marshal(obj)
	}
	if len(input) > maxToolInputBytes {
		small := map[string]any{"truncated": true}
		if file != "" {
			small["path"], small["file_path"] = file, file
		}
		input, _ = json.Marshal(small)
	}
	text := e.Text
	if len(text) > maxToolResponseBytes {
		text = clipMiddle(text, maxToolResponseBytes)
	}
	response := map[string]any{"text": strings.ToValidUTF8(text, "�"), "success": true}
	if file != "" {
		response["filePath"] = file
	}
	return mustJSON(map[string]any{"session_id": e.Conversation, "transcript_path": "", "cwd": e.Workspace.CWD, "permission_mode": "default",
		"hook_event_name": EventPostToolUse, "tool_name": e.Name, "tool_use_id": e.CallID, "tool_input": input, "tool_response": response,
		"turn_id": e.TurnID, "character_id": e.CharacterID})
}

func stopInput(e turn.StopEvent, changed []string) []byte {
	if changed == nil {
		changed = []string{}
	}
	return mustJSON(map[string]any{"session_id": e.Conversation, "transcript_path": "", "cwd": e.Workspace.CWD, "permission_mode": "default",
		"hook_event_name": EventStop, "stop_hook_active": e.Continued > 0, "last_assistant_message": strings.ToValidUTF8(e.Answer, "�"),
		"turn_id": e.TurnID, "character_id": e.CharacterID, "changed_files": changed})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// sets 是这次事件要看的配置：全局在前，项目（过了信任）在后
func (p *Plugin) sets(ctx context.Context, ws turn.Workspace) []*Set {
	var out []*Set
	if p.opts.GlobalFile != "" {
		if s := p.loader.load(p, "global", p.opts.GlobalFile); s != nil {
			out = append(out, s)
		}
	}
	if s := p.projectSet(ctx, ws); s != nil {
		out = append(out, s)
	}
	return out
}

func (p *Plugin) projectSet(ctx context.Context, ws turn.Workspace) *Set {
	if ws.Root == "" {
		return nil
	}
	file := filepath.Join(ws.Root, ProjectFile)
	if _, err := os.Lstat(file); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.reportOnce("project:"+file+":stat", taskrun.ClassConfigInvalid, fmt.Errorf("项目钩子配置读不了：%w", err))
		}
		return nil
	}
	if p.opts.Trust == nil {
		p.reportOnce("project:"+file+":untrusted", taskrun.ClassUntrusted, errors.New("信任根没开（projectContext），项目钩子不跑"))
		return nil
	}
	ok, err := p.opts.Trust.Trusted(ctx, ws)
	if err != nil {
		p.reportOnce("project:"+file+":untrusted", taskrun.ClassUntrusted, fmt.Errorf("信任根核不了，项目钩子不跑：%w", err))
		return nil
	}
	if !ok {
		p.reportOnce("project:"+file+":untrusted", taskrun.ClassUntrusted, errors.New("工作区不在信任根里，项目钩子不跑"))
		return nil
	}
	// 文件可能是联接：真实位置必须还在工作区里，不然"信任这个目录"管不到它
	realFile, err := approval.RealStrict(file)
	if err != nil {
		p.reportOnce("project:"+file+":real", taskrun.ClassUntrusted, fmt.Errorf("项目钩子配置的真实位置核不了：%w", err))
		return nil
	}
	realRoot, err := approval.RealStrict(ws.Root)
	if err != nil || !inside(realRoot, realFile) {
		p.reportOnce("project:"+file+":real", taskrun.ClassUntrusted, errors.New("项目钩子配置指到工作区外面了，不跑"))
		return nil
	}
	return p.loader.load(p, "project", realFile)
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// reportOnce：同一件事只记一行、提醒一次（每个事件都会走到这里）
func (p *Plugin) reportOnce(key, class string, err error) {
	if !p.loader.first(key) {
		return
	}
	p.opts.Degrade.Degrade(taskrun.ModuleHooks, class, err, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Alert: &alert.Alert{Key: "hooks:" + class, Class: "hooks", Title: "有用户钩子没生效", Body: "到运行页的质量标签看“用户钩子”一行。"}})
}

// loader 按修改时间和大小缓存解析结果：文件改了下一次事件就生效，没改不重读
type loader struct {
	mu       sync.Mutex
	files    map[string]*cached
	reported map[string]bool
}

type cached struct {
	mod  time.Time
	size int64
	set  *Set
}

func (l *loader) first(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reported == nil {
		l.reported = map[string]bool{}
	}
	if l.reported[key] {
		return false
	}
	l.reported[key] = true
	return true
}

func (l *loader) load(p *Plugin, source, file string) *Set {
	info, err := os.Stat(file)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.reportOnce(source+":"+file+":stat", taskrun.ClassConfigInvalid, fmt.Errorf("钩子配置读不了：%w", err))
		}
		return nil
	}
	l.mu.Lock()
	c := l.files[file]
	l.mu.Unlock()
	if c != nil && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
		return c.set
	}
	set := l.parse(p, source, file, info)
	l.mu.Lock()
	if l.files == nil {
		l.files = map[string]*cached{}
	}
	l.files[file] = &cached{mod: info.ModTime(), size: info.Size(), set: set}
	l.mu.Unlock()
	return set
}

func (l *loader) parse(p *Plugin, source, file string, info os.FileInfo) *Set {
	report := func(err error) {
		// 每份内容报一次：修好再弄坏还会再报
		p.reportOnce(fmt.Sprintf("%s:%s:%d:%d", source, file, info.ModTime().UnixNano(), info.Size()), taskrun.ClassConfigInvalid, err)
	}
	if !info.Mode().IsRegular() {
		report(fmt.Errorf("%s 钩子配置不是普通文件", source))
		return nil
	}
	if info.Size() > maxConfigBytes {
		report(fmt.Errorf("%s 钩子配置超过 %d KiB", source, maxConfigBytes>>10))
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		report(fmt.Errorf("%s 钩子配置读不了：%w", source, err))
		return nil
	}
	set, err := Parse(data, p.opts.Timeout)
	if err != nil {
		report(fmt.Errorf("%s 钩子配置有问题：%w", source, err))
		return nil
	}
	if len(set.Diag) > 0 {
		report(fmt.Errorf("%s 钩子配置有 %d 处跳过：%s", source, len(set.Diag), strings.Join(set.Diag, "；")))
	}
	return set
}

var _ turn.Plugin = (*Plugin)(nil)
